//go:build integration

// Package ingress — integration_test.go covers the M10 milestone smoke tests
// (T013). All tests run against a real Postgres testcontainer (the shared
// testdb harness). The ingress.Server is exercised through its exported
// handler path: tests either post directly to an httptest.Server wrapping
// the handler, or (for vault-fail-closed) construct a full ingress.Server
// to prove the construction error path.
//
// Tests mirror the naming mandated by tasks.md T013 completion condition.
package ingress

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// integTestSecret is the HMAC key used throughout this file. It is never a
// production secret — integration tests only (FR-302 applies to
// production, not to testdb sessions). Named to avoid collision with the
// unit-test constant testSecret declared in handler_test.go.
var integTestSecret = []byte("integration-test-webhook-secret")

// signBodyInteg computes sha256=<hex-hmac> over body using integTestSecret.
// Named to avoid collision with computeSig declared in handler_test.go.
func signBodyInteg(body []byte) string {
	mac := hmac.New(sha256.New, integTestSecret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// issueOpenedPayload returns a minimal GitHub issues:opened JSON body.
// issueBody may be nil (null JSON) to exercise the null-body fallback.
func issueOpenedPayload(t *testing.T, deliveryID string, issueBody *string) ([]byte, string) {
	t.Helper()
	bodyField := "null"
	if issueBody != nil {
		b, _ := json.Marshal(*issueBody)
		bodyField = string(b)
	}
	payload := []byte(fmt.Sprintf(`{
		"action": "opened",
		"sender": {"type": "User", "login": "alice"},
		"issue": {
			"id": 12345678,
			"number": 42,
			"title": "Test issue title",
			"html_url": "https://github.com/garrison-hq/garrison/issues/42",
			"body": %s
		}
	}`, bodyField))
	return payload, deliveryID
}

// prReviewRequestedPayload returns a minimal pull_request:review_requested JSON body.
func prReviewRequestedPayload(t *testing.T, deliveryID string) ([]byte, string) {
	t.Helper()
	payload := []byte(`{
		"action": "review_requested",
		"sender": {"type": "User", "login": "bob"},
		"pull_request": {
			"id": 99887766,
			"number": 7,
			"title": "Add feature X",
			"html_url": "https://github.com/garrison-hq/garrison/pull/7",
			"body": "PR description here"
		}
	}`)
	return payload, deliveryID
}

// botSenderPayload returns a GitHub issues:opened payload from a bot sender.
func botSenderPayload(t *testing.T, deliveryID string) ([]byte, string) {
	t.Helper()
	payload := []byte(`{
		"action": "opened",
		"sender": {"type": "Bot", "login": "dependabot[bot]"},
		"issue": {
			"id": 55554444,
			"number": 99,
			"title": "Automated issue",
			"html_url": "https://github.com/garrison-hq/garrison/issues/99",
			"body": "bot generated"
		}
	}`)
	return payload, deliveryID
}

// postWebhook sends a signed POST /webhook/github to srvURL.
func postWebhook(t *testing.T, srvURL, eventType, deliveryID string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+"/webhook/github", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", signBodyInteg(body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook/github: %v", err)
	}
	return resp
}

// postWebhookBadSig sends a POST with a deliberately wrong signature.
func postWebhookBadSig(t *testing.T, srvURL, eventType, deliveryID string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+"/webhook/github", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", "sha256=aaabbbcccdddeeefff000111222333444555666777888999aaabbbcccdddeeeff")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook/github (bad sig): %v", err)
	}
	return resp
}

// seedIngressDept inserts a company + department and returns (companyID, deptID).
// It is separate from testdb.SeedM21 so we can use a custom dept slug.
func seedIngressDept(t *testing.T, pool *pgxpool.Pool, deptSlug string) (companyID, deptID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'ingress-test-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("seedIngressDept: insert company: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, $2, $3, 10, '/tmp')
		RETURNING id`,
		companyID, deptSlug, strings.ToTitle(deptSlug[:1])+deptSlug[1:],
	).Scan(&deptID); err != nil {
		t.Fatalf("seedIngressDept: insert department: %v", err)
	}
	return companyID, deptID
}

// buildHandler constructs a HandlerDeps + HandlerDeps + httptest.Server for
// the given connector and pool. The RateCap starts full (high burst so tests
// that don't test rate-capping don't hit the cap).
func buildHandler(pool *pgxpool.Pool, companyID pgtype.UUID, deptSlug string) (*httptest.Server, *atomic.Int64) {
	q := store.New(pool)
	rateCap := NewRateCap(nil)
	const connectorID = "github-test"
	rateCap.AddConnector(connectorID, 600, 300) // generous: 600/min, burst 300

	var rejCounter atomic.Int64
	conn := NewGitHubConnector(GitHubConfig{
		ConnectorID: connectorID,
		Secret:      integTestSecret,
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     deptSlug,
				ObjectiveTemplate:  "Triage GitHub issue: {{title}} ({{url}})",
				AcceptanceTemplate: "{{body}}",
			},
			"pull_request": {
				DepartmentSlug:     deptSlug,
				ObjectiveTemplate:  "Review GitHub PR: {{title}} ({{url}})",
				AcceptanceTemplate: "{{body}}",
			},
		},
		RatePerMin: 600,
		Burst:      300,
	})

	hdeps := HandlerDeps{
		Pool:             pool,
		Queries:          q,
		Connector:        conn,
		Secret:           integTestSecret,
		RejectionCounter: &rejCounter,
		RateCap:          rateCap,
		CompanyID:        companyID,
		RatePerMin:       600,
		Burst:            300,
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhook/github", newWebhookHandler(hdeps))
	srv := httptest.NewServer(mux)
	return srv, &rejCounter
}

// listenForChannel opens a dedicated LISTEN connection on the given Postgres
// channel and returns a channel that yields each notify payload. The
// connection is released in t.Cleanup.
func listenForChannel(t *testing.T, pool *pgxpool.Pool, channel string) <-chan string {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("listenForChannel: acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, `LISTEN "`+channel+`"`); err != nil {
		conn.Release()
		t.Fatalf("listenForChannel: LISTEN %s: %v", channel, err)
	}
	out := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			default:
			}
			waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			notif, err := conn.Conn().WaitForNotification(waitCtx)
			cancel()
			if err != nil {
				return
			}
			out <- notif.Payload
		}
	}()
	t.Cleanup(func() {
		close(done)
		conn.Release()
	})
	return out
}

// countTickets returns the number of tickets with origin='ingress' in the
// given department.
func countIngressTickets(t *testing.T, pool *pgxpool.Pool, deptID pgtype.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM tickets WHERE department_id = $1 AND origin = 'ingress'`,
		deptID,
	).Scan(&n); err != nil {
		t.Fatalf("countIngressTickets: %v", err)
	}
	return n
}

// countDeliveries returns the count of ingress_deliveries rows for the
// given connector ID.
func countDeliveries(t *testing.T, pool *pgxpool.Pool, connectorID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM ingress_deliveries WHERE connector_id = $1`,
		connectorID,
	).Scan(&n); err != nil {
		t.Fatalf("countDeliveries: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// failingVaultFetcher satisfies vault.Fetcher and always returns an error.
// Used to exercise the fail-closed construction path (FR-302).
// ---------------------------------------------------------------------------

type failingVaultFetcher struct{}

func (failingVaultFetcher) Fetch(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
	return nil, errors.New("vault: simulated unreachable server (integration test)")
}

// ---------------------------------------------------------------------------
// T013 integration tests
// ---------------------------------------------------------------------------

// TestIngress_IssueOpened_CreatesOneTicket — POST a signature-valid
// issues:opened to a live handler; assert exactly one tickets row with
// origin='ingress', column_slug='todo', metadata carrying the three
// provenance keys; one ingress_deliveries row with ticket_id backfilled;
// the work.ticket.created.<dept>.todo notify observed; handler returns 202.
// (SC-001, US1-AS1, tasks.md T013)
func TestIngress_IssueOpened_CreatesOneTicket(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")
	_ = deptID

	const notifyChannel = "work.ticket.created.concierge.todo"
	notifyCh := listenForChannel(t, pool, notifyChannel)

	srv, _ := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	body := "First issue body"
	payload, deliveryID := issueOpenedPayload(t, "delivery-uuid-001", &body)

	resp := postWebhook(t, srv.URL, "issues", deliveryID, payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	// Assert exactly one tickets row.
	if n := countIngressTickets(t, pool, deptID); n != 1 {
		t.Fatalf("ingress tickets = %d, want 1", n)
	}

	// Assert ticket fields: origin, column_slug, metadata provenance.
	var origin, columnSlug string
	var metadata map[string]any
	var rawMeta []byte
	err := pool.QueryRow(context.Background(), `
		SELECT origin, column_slug, metadata
		  FROM tickets
		 WHERE department_id = $1 AND origin = 'ingress'`, deptID,
	).Scan(&origin, &columnSlug, &rawMeta)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if origin != "ingress" {
		t.Errorf("origin = %q, want ingress", origin)
	}
	if columnSlug != "todo" {
		t.Errorf("column_slug = %q, want todo", columnSlug)
	}
	if err := json.Unmarshal(rawMeta, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if v, _ := metadata["ingress_connector"].(string); v == "" {
		t.Errorf("metadata.ingress_connector is missing or empty")
	}
	if v, _ := metadata["external_id"].(string); v == "" {
		t.Errorf("metadata.external_id is missing or empty")
	}
	if v, _ := metadata["external_url"].(string); v == "" {
		t.Errorf("metadata.external_url is missing or empty")
	}

	// Assert one ingress_deliveries row with ticket_id backfilled.
	if n := countDeliveries(t, pool, "github-test"); n != 1 {
		t.Fatalf("ingress_deliveries rows = %d, want 1", n)
	}
	var ticketIDValid bool
	if err := pool.QueryRow(context.Background(),
		`SELECT ticket_id IS NOT NULL FROM ingress_deliveries WHERE connector_id = 'github-test'`,
	).Scan(&ticketIDValid); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if !ticketIDValid {
		t.Error("delivery row: ticket_id is NULL, want backfilled")
	}

	// Assert work.ticket.created.concierge.todo notify arrived.
	select {
	case payload := <-notifyCh:
		if payload == "" {
			t.Log("notify arrived (empty payload)")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("work.ticket.created.concierge.todo notify never arrived")
	}
}

// TestIngress_PullRequestReviewRequested_CreatesOneTicket — same flow for
// pull_request:review_requested (SC-001, US1-AS3).
func TestIngress_PullRequestReviewRequested_CreatesOneTicket(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")
	_ = deptID

	const notifyChannel = "work.ticket.created.concierge.todo"
	notifyCh := listenForChannel(t, pool, notifyChannel)

	srv, _ := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	payload, deliveryID := prReviewRequestedPayload(t, "delivery-pr-001")
	resp := postWebhook(t, srv.URL, "pull_request", deliveryID, payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if n := countIngressTickets(t, pool, deptID); n != 1 {
		t.Fatalf("ingress tickets = %d, want 1", n)
	}

	// Verify ticket metadata carries the PR URL.
	var rawMeta []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata FROM tickets WHERE department_id = $1 AND origin = 'ingress'`, deptID,
	).Scan(&rawMeta); err != nil {
		t.Fatalf("read ticket metadata: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	extURL, _ := meta["external_url"].(string)
	if !strings.Contains(extURL, "pull/7") {
		t.Errorf("external_url = %q, want to contain pull/7", extURL)
	}

	select {
	case <-notifyCh:
	case <-time.After(3 * time.Second):
		t.Fatal("work.ticket.created.concierge.todo notify never arrived")
	}
}

// TestIngress_NullIssueBody_GracefulFallback — null issue.body must create a
// ticket whose acceptance_criteria contains the fallback literal "(no
// description provided)" with no error (US1-AS4, spike QS4).
func TestIngress_NullIssueBody_GracefulFallback(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")
	_ = deptID

	srv, _ := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	payload, deliveryID := issueOpenedPayload(t, "delivery-null-body-001", nil)
	resp := postWebhook(t, srv.URL, "issues", deliveryID, payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if n := countIngressTickets(t, pool, deptID); n != 1 {
		t.Fatalf("ingress tickets = %d, want 1", n)
	}

	// acceptance_criteria should contain the fallback literal.
	var acceptance *string
	if err := pool.QueryRow(context.Background(),
		`SELECT acceptance_criteria FROM tickets WHERE department_id = $1 AND origin = 'ingress'`, deptID,
	).Scan(&acceptance); err != nil {
		t.Fatalf("read acceptance_criteria: %v", err)
	}
	if acceptance == nil || !strings.Contains(*acceptance, "(no description provided)") {
		t.Errorf("acceptance_criteria = %v, want to contain fallback literal", acceptance)
	}
}

// TestIngress_SerialRedelivery_NoSecondTicket — same GUID posted twice serially;
// second delivery returns 200, no second ticket, no second delivery row, no
// second notify (FR-202, US2-AS1).
func TestIngress_SerialRedelivery_NoSecondTicket(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")
	_ = deptID

	const notifyChannel = "work.ticket.created.concierge.todo"
	notifyCh := listenForChannel(t, pool, notifyChannel)

	srv, _ := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	body := "Issue for idempotency test"
	payload, deliveryID := issueOpenedPayload(t, "delivery-idempotency-001", &body)

	// First POST → 202 and a ticket.
	resp1 := postWebhook(t, srv.URL, "issues", deliveryID, payload)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST status = %d, want 202", resp1.StatusCode)
	}

	// Drain the notify so we can detect any second one.
	select {
	case <-notifyCh:
	case <-time.After(3 * time.Second):
		t.Fatal("first notify never arrived")
	}

	// Second POST → 200, no side effects.
	resp2 := postWebhook(t, srv.URL, "issues", deliveryID, payload)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second (redelivery) POST status = %d, want 200", resp2.StatusCode)
	}

	// Still exactly one ticket.
	if n := countIngressTickets(t, pool, deptID); n != 1 {
		t.Fatalf("ingress tickets after redelivery = %d, want 1 (FR-202)", n)
	}

	// Still exactly one delivery row.
	if n := countDeliveries(t, pool, "github-test"); n != 1 {
		t.Fatalf("ingress_deliveries after redelivery = %d, want 1", n)
	}

	// No second notify.
	select {
	case payload := <-notifyCh:
		t.Fatalf("second notify arrived unexpectedly: %q (FR-202)", payload)
	case <-time.After(500 * time.Millisecond):
		// Good: no second notify.
	}
}

// TestIngress_BadSignature_NoTicket_401 — forged signature against the live
// stack → 401, zero tickets, zero delivery rows (SC-003, US3-AS1, FR-300).
func TestIngress_BadSignature_NoTicket_401(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")
	_ = deptID

	srv, rejCounter := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	body := "Forged issue"
	payload, deliveryID := issueOpenedPayload(t, "delivery-forged-001", &body)

	resp := postWebhookBadSig(t, srv.URL, "issues", deliveryID, payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if n := countIngressTickets(t, pool, deptID); n != 0 {
		t.Fatalf("ingress tickets after bad sig = %d, want 0 (FR-300)", n)
	}
	if n := countDeliveries(t, pool, "github-test"); n != 0 {
		t.Fatalf("ingress_deliveries after bad sig = %d, want 0", n)
	}
	if got := rejCounter.Load(); got != 1 {
		t.Errorf("rejection counter = %d, want 1 (FR-301)", got)
	}
}

// TestIngress_BotSender_NoTicket_200 — bot-sourced issues:opened → 200,
// zero tickets (SC-004, US3-AS4, FR-401, spike F7).
func TestIngress_BotSender_NoTicket_200(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")
	_ = deptID

	srv, _ := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	payload, deliveryID := botSenderPayload(t, "delivery-bot-001")

	resp := postWebhook(t, srv.URL, "issues", deliveryID, payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bot sender → discard)", resp.StatusCode)
	}
	if n := countIngressTickets(t, pool, deptID); n != 0 {
		t.Fatalf("ingress tickets after bot sender = %d, want 0 (FR-401)", n)
	}
	if n := countDeliveries(t, pool, "github-test"); n != 0 {
		t.Fatalf("ingress_deliveries after bot sender = %d, want 0", n)
	}
}

// TestIngress_IngressTicketCountsAgainstDeptBudget — an ingress ticket
// created inside an M8 dept-weekly window counts against that window's
// budget (FR-603, SC-005-partial). This test reuses the M8 budget
// GetDeptWeeklyState query to assert the count is visible.
func TestIngress_IngressTicketCountsAgainstDeptBudget(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := seedIngressDept(t, pool, "concierge")

	// Set a weekly_ticket_budget of 5 on the department.
	budget := int32(5)
	if _, err := pool.Exec(context.Background(),
		`UPDATE departments SET weekly_ticket_budget = $1 WHERE id = $2`, budget, deptID,
	); err != nil {
		t.Fatalf("set budget: %v", err)
	}

	srv, _ := buildHandler(pool, companyID, "concierge")
	defer srv.Close()

	bodyText := "Budget counting test"
	payload, deliveryID := issueOpenedPayload(t, "delivery-budget-001", &bodyText)

	resp := postWebhook(t, srv.URL, "issues", deliveryID, payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	// The ingress ticket must be counted in the dept-weekly rolling window.
	q := store.New(pool)
	state, err := q.GetDeptWeeklyState(context.Background(), deptID)
	if err != nil {
		t.Fatalf("GetDeptWeeklyState: %v", err)
	}
	if state.CurrentCount != 1 {
		t.Errorf("dept weekly current_count = %d, want 1 (ingress ticket counts toward budget, FR-603)", state.CurrentCount)
	}
}

// TestIngress_VaultUnavailable_FailsClosed — constructing ingress.Server
// with GitHub enabled and an unreachable vault must return an error. The
// supervisor never starts signature-blind (FR-302, US3 edge).
func TestIngress_VaultUnavailable_FailsClosed(t *testing.T) {
	pool := testdb.Start(t)
	_, _ = seedIngressDept(t, pool, "concierge")

	q := store.New(pool)
	var rejCounter atomic.Int64
	cfg := &config.Config{
		IngressPort:              19082, // arbitrary non-conflicting port
		IngressGitHubEnabled:     true,
		IngressGitHubConnectorID: "github-sortie",
		IngressGitHubDepartment:  "concierge",
		IngressGitHubRatePerMin:  60,
		IngressGitHubBurst:       30,
		ShutdownGrace:            2 * time.Second,
	}

	// Use a failing vault fetcher to simulate vault unavailability.
	deps := Deps{
		Pool:             pool,
		Queries:          q,
		VaultClient:      failingVaultFetcher{},
		CustomerID:       pgtype.UUID{},
		RejectionCounter: &rejCounter,
	}

	_, err := NewServer(context.Background(), cfg, deps, nil)
	if err == nil {
		t.Fatal("NewServer with unreachable vault returned nil error; want an error (FR-302 fail-closed)")
	}
	if !strings.Contains(err.Error(), "vault") {
		t.Errorf("error %q does not mention vault — want a descriptive vault-fetch error", err)
	}
}
