//go:build chaos

// Package ingress — chaos_test.go covers the M10 T014 chaos suite:
//
//  1. TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket: two goroutines POST
//     the same delivery GUID concurrently while contending on the UNIQUE
//     (connector_id, external_delivery_id) insert. Exactly one ticket and one
//     ingress_deliveries row must land — the 23505 unique-violation is the dedup
//     signal, not a pre-check SELECT (SC-002, US2-AS2, FR-201).
//
//  2. TestIngress_BurstExceedsCap_BoundedTickets: drives 2×Burst deliveries as
//     fast as possible at a low-cap connector; asserts ticket count ≤ Burst,
//     throttle_events evidence written, 429s observed, and a fresh redelivery of
//     a 429'd GUID processes to one ticket once the bucket refills (SC-005,
//     FR-600, FR-601, FR-602).
//
// Runs under `go test -tags=chaos ./internal/ingress/...`.
// Helpers carry a "chaos" prefix so a combined-tag build (-tags="integration chaos")
// does not collide with the integration-tagged helpers in integration_test.go.
package ingress

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Shared chaos helpers (prefixed to avoid collision with integration_test.go)
// ---------------------------------------------------------------------------

// chaosTestSecret is the HMAC key used throughout this file.
var chaosTestSecret = []byte("chaos-test-webhook-secret")

// chaosSignBody computes sha256=<hex-hmac> over body using chaosTestSecret.
func chaosSignBody(body []byte) string {
	mac := hmac.New(sha256.New, chaosTestSecret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// chaosSeedDept inserts a company + department and returns (companyID, deptID).
func chaosSeedDept(t *testing.T, pool *pgxpool.Pool, deptSlug string) (companyID, deptID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), $1) RETURNING id`,
		"chaos-test-co-"+deptSlug,
	).Scan(&companyID); err != nil {
		t.Fatalf("chaosSeedDept: insert company: %v", err)
	}
	name := strings.ToUpper(deptSlug[:1]) + deptSlug[1:]
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, $2, $3, 10, '/tmp')
		RETURNING id`,
		companyID, deptSlug, name,
	).Scan(&deptID); err != nil {
		t.Fatalf("chaosSeedDept: insert department: %v", err)
	}
	return companyID, deptID
}

// chaosIssueOpenedPayload returns a minimal GitHub issues:opened JSON body.
func chaosIssueOpenedPayload(deliveryGUID string) []byte {
	return []byte(fmt.Sprintf(`{
		"action": "opened",
		"sender": {"type": "User", "login": "operator"},
		"issue": {
			"id": 77778888,
			"number": 1,
			"title": "Chaos test issue %s",
			"html_url": "https://github.com/garrison-hq/garrison/issues/1",
			"body": "Chaos test body"
		}
	}`, deliveryGUID))
}

// buildChaosHandler constructs an httptest.Server wrapping the ingress handler
// for the given connector ID with the given rate parameters.
func buildChaosHandler(
	pool *pgxpool.Pool,
	companyID pgtype.UUID,
	deptSlug, connectorID string,
	ratePerMin, burst int,
) (*httptest.Server, *atomic.Int64) {
	q := store.New(pool)
	rateCap := NewRateCap(nil)
	rateCap.AddConnector(connectorID, ratePerMin, burst)

	var rejCounter atomic.Int64
	conn := NewGitHubConnector(GitHubConfig{
		ConnectorID: connectorID,
		Secret:      chaosTestSecret,
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     deptSlug,
				ObjectiveTemplate:  "Chaos issue: {{title}}",
				AcceptanceTemplate: "{{body}}",
			},
		},
		RatePerMin: ratePerMin,
		Burst:      burst,
	})

	hdeps := HandlerDeps{
		Pool:             pool,
		Queries:          q,
		Connector:        conn,
		Secret:           chaosTestSecret,
		RejectionCounter: &rejCounter,
		RateCap:          rateCap,
		CompanyID:        companyID,
		RatePerMin:       ratePerMin,
		Burst:            burst,
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhook/github", newWebhookHandler(hdeps))
	srv := httptest.NewServer(mux)
	return srv, &rejCounter
}

// chaosSignedRequest builds an *http.Request with the correct HMAC signature.
func chaosSignedRequest(srvURL, eventType, deliveryID string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, srvURL+"/webhook/github", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", chaosSignBody(body))
	return req, nil
}

// chaosCountRows runs a COUNT(*) query and returns the result.
func chaosCountRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("chaosCountRows %q: %v", query, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket
// ---------------------------------------------------------------------------

// TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket proves the M1
// concurrent-delivery race: two goroutines POST the same delivery GUID
// concurrently while contending on the UNIQUE (connector_id,
// external_delivery_id) insert. Exactly one tickets row and exactly one
// ingress_deliveries row must land — the 23505 unique-violation is the dedup
// signal, NOT a pre-check SELECT (SC-002, US2-AS2, FR-201).
//
// Both goroutines must complete with a 2xx response: the winner returns 202
// (ticket created); the loser whose INSERT hits the unique constraint returns
// 200 (ErrDuplicateDelivery → 200, no side effects, FR-202).
func TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := chaosSeedDept(t, pool, "chaosrace")

	const connectorID = "chaos-race-connector"
	// Generous rate cap so the race test is purely about idempotency, not capping.
	srv, _ := buildChaosHandler(pool, companyID, "chaosrace", connectorID, 600, 300)
	defer srv.Close()

	const deliveryGUID = "chaos-concurrent-delivery-uuid-001"
	payload := chaosIssueOpenedPayload(deliveryGUID)

	// Start barrier: release both goroutines simultaneously to maximise
	// the chance of true concurrent INSERT contention on the unique key.
	start := make(chan struct{})
	type result struct {
		status int
		err    error
	}
	results := make(chan result, 2)

	for i := 0; i < 2; i++ {
		go func() {
			<-start
			req, err := chaosSignedRequest(srv.URL, "issues", deliveryGUID, payload)
			if err != nil {
				results <- result{err: err}
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			resp.Body.Close()
			results <- result{status: resp.StatusCode}
		}()
	}

	// Release both goroutines simultaneously.
	close(start)

	// Collect both results.
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent goroutine %d: HTTP error: %v", i, r.err)
			}
			// Both must be 2xx: 202 for the winner, 200 for the duplicate.
			if r.status != http.StatusAccepted && r.status != http.StatusOK {
				t.Errorf("goroutine %d: status = %d, want 200 or 202 (SC-002)", i, r.status)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("concurrent goroutine never returned; handler may be wedged")
		}
	}

	// Exactly one tickets row with origin='ingress' in this department (SC-002).
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM tickets WHERE department_id = $1 AND origin = 'ingress'`, deptID,
	); n != 1 {
		t.Fatalf("tickets after concurrent redelivery = %d, want exactly 1 (SC-002, FR-201)", n)
	}

	// Exactly one ingress_deliveries row for this connector + delivery GUID.
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM ingress_deliveries WHERE connector_id = $1 AND external_delivery_id = $2`,
		connectorID, deliveryGUID,
	); n != 1 {
		t.Fatalf("ingress_deliveries after concurrent redelivery = %d, want exactly 1 (FR-201, SC-002)", n)
	}
}

// ---------------------------------------------------------------------------
// TestIngress_BurstExceedsCap_BoundedTickets
// ---------------------------------------------------------------------------

// TestIngress_BurstExceedsCap_BoundedTickets drives 2×Burst deliveries as fast
// as possible at a configured low-cap connector and asserts:
//
//	(a) ticket count ≤ Burst — fan-out bounded at cap (SC-005, FR-600).
//	(b) at least one throttle_events row with kind='ingress_rate_cap_exceeded'
//	    was written (FR-601).
//	(c) over-cap responses are 429 (FR-600).
//	(d) a subsequent fresh GUID (one whose 429 created no delivery row)
//	    processes to exactly one ticket when the bucket is no longer empty
//	    (FR-602; composes with idempotency — the 429'd GUID created no row,
//	    so its "redelivery" is treated as a first-time delivery).
func TestIngress_BurstExceedsCap_BoundedTickets(t *testing.T) {
	pool := testdb.Start(t)
	companyID, deptID := chaosSeedDept(t, pool, "chaosburst")

	const (
		connectorID = "chaos-burst-connector"
		burst       = 5
		// ratePerMin=5 → 1 token per 12 s; the bucket drains to empty in exactly
		// burst=5 deliveries, then refills slowly so we can observe 429s before
		// any natural refill within the burst phase.
		ratePerMin = 5
	)

	srv, _ := buildChaosHandler(pool, companyID, "chaosburst", connectorID, ratePerMin, burst)
	defer srv.Close()

	// Drive 2×burst unique delivery GUIDs as fast as possible.
	totalDeliveries := 2 * burst
	statusCodes := make([]int, totalDeliveries)
	deliveryGUIDs := make([]string, totalDeliveries)
	for i := 0; i < totalDeliveries; i++ {
		deliveryGUIDs[i] = fmt.Sprintf("burst-delivery-%04d", i)
	}

	var wg sync.WaitGroup
	var statusMu sync.Mutex
	start := make(chan struct{})

	for i := 0; i < totalDeliveries; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := chaosIssueOpenedPayload(deliveryGUIDs[i])
			req, err := chaosSignedRequest(srv.URL, "issues", deliveryGUIDs[i], payload)
			if err != nil {
				t.Errorf("burst goroutine %d: build request: %v", i, err)
				return
			}
			<-start
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("burst goroutine %d: do: %v", i, err)
				return
			}
			resp.Body.Close()
			statusMu.Lock()
			statusCodes[i] = resp.StatusCode
			statusMu.Unlock()
		}()
	}

	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("burst goroutines never completed; handler may be wedged")
	}

	// --- (a) ticket count ≤ burst ---
	ticketCount := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM tickets WHERE department_id = $1 AND origin = 'ingress'`, deptID,
	)
	if ticketCount > burst {
		t.Errorf("ticket count = %d, want ≤ %d (SC-005, FR-600: fan-out bounded at burst cap)", ticketCount, burst)
	}

	// --- (b) at least one throttle_events row with the cap kind ---
	capEventCount := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM throttle_events WHERE kind = 'ingress_rate_cap_exceeded'`,
	)
	if capEventCount < 1 {
		t.Errorf("throttle_events with kind='ingress_rate_cap_exceeded' = %d, want ≥ 1 (FR-601)", capEventCount)
	}

	// --- (c) over-cap responses are 429 ---
	var got429 int
	for _, sc := range statusCodes {
		if sc == http.StatusTooManyRequests {
			got429++
		}
	}
	if got429 == 0 {
		t.Errorf("no 429 responses observed in %d deliveries; want at least one over-cap 429 (FR-600)", totalDeliveries)
	}

	// Every 429'd delivery must have created no ingress_deliveries row —
	// the cap fires BEFORE the delivery-row insert (plan R1, FR-602).
	for i, sc := range statusCodes {
		if sc != http.StatusTooManyRequests {
			continue
		}
		guid := deliveryGUIDs[i]
		n := chaosCountRows(t, pool,
			`SELECT COUNT(*) FROM ingress_deliveries WHERE connector_id = $1 AND external_delivery_id = $2`,
			connectorID, guid,
		)
		if n != 0 {
			t.Errorf("429'd delivery %q has %d ingress_deliveries row(s), want 0 (FR-602)", guid, n)
		}
	}

	// --- (d) fresh redelivery of a 429'd GUID processes once the bucket refills ---
	// Find any GUID that received 429 — it has no delivery row (asserted above).
	var rejectedGUID string
	for i, sc := range statusCodes {
		if sc == http.StatusTooManyRequests {
			rejectedGUID = deliveryGUIDs[i]
			break
		}
	}
	if rejectedGUID == "" {
		// All deliveries fit within cap; the burst test's precondition wasn't met.
		// This can happen if Postgres latency allowed natural refill between requests.
		// Skip rather than fail: the cap assertions above already passed.
		t.Skip("no 429'd delivery found; burst may have fit within cap due to Postgres latency; earlier assertions passed")
	}

	// Use the rejected GUID itself (not a new one) to prove FR-602:
	// since it created no delivery row, treating it as a "redelivery" is fresh.
	// Retry until the bucket has at least one token (ratePerMin=5 → ~12s per token).
	const (
		maxAttempts = 15
		retryWait   = 2 * time.Second
	)
	freshPayload := chaosIssueOpenedPayload(rejectedGUID)

	var redeliveryStatus int
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := chaosSignedRequest(srv.URL, "issues", rejectedGUID, freshPayload)
		if err != nil {
			t.Fatalf("redelivery attempt %d: build request: %v", attempt, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("redelivery attempt %d: do: %v", attempt, err)
		}
		resp.Body.Close()
		redeliveryStatus = resp.StatusCode
		if redeliveryStatus == http.StatusAccepted {
			break
		}
		// 429 → bucket still empty; wait for refill.
		time.Sleep(retryWait)
	}
	if redeliveryStatus != http.StatusAccepted {
		t.Errorf("redelivery of previously-429'd GUID got status %d after %d retries, want 202 (FR-602)",
			redeliveryStatus, maxAttempts)
	}

	// The fresh redelivery must have created exactly one delivery row.
	deliveryCount := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM ingress_deliveries WHERE connector_id = $1 AND external_delivery_id = $2`,
		connectorID, rejectedGUID,
	)
	if deliveryCount != 1 {
		t.Errorf("ingress_deliveries for redelivered GUID %q = %d, want 1 (FR-602)", rejectedGUID, deliveryCount)
	}
}
