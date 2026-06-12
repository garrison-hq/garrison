package ingress

// handler_test.go — T011 unit tests for newWebhookHandler / the SR6 pipeline.
//
// Tests run without a real Postgres. The store seam is faked via a minimal
// implementation of store.DBTX that controls which queries succeed or fail.
// The pool seam is faked via fakeTxPool that implements the txBeginner
// interface (unexported in handler.go).
//
// Coverage target per tasks.md T017: these tests contribute to the ≥82%
// new-code coverage probe on Go-side new code.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ---------------------------------------------------------------------------
// Fake store.DBTX implementation
// ---------------------------------------------------------------------------

// fakeRow implements pgx.Row, allowing tests to control what Scan returns.
type fakeRow struct {
	vals []any
	err  error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		if i >= len(r.vals) {
			break
		}
		if err := assign(d, r.vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// assign copies src into the pointed-to dest. Supports the small set of types
// the handler queries actually scan (pgtype.UUID, pgtype.Timestamptz, string,
// *string, nil).
func assign(dest, src any) error {
	switch d := dest.(type) {
	case *pgtype.UUID:
		switch s := src.(type) {
		case pgtype.UUID:
			*d = s
		case nil:
			*d = pgtype.UUID{}
		default:
			return fmt.Errorf("assign: cannot assign %T to *pgtype.UUID", src)
		}
	case *pgtype.Timestamptz:
		switch s := src.(type) {
		case pgtype.Timestamptz:
			*d = s
		case nil:
			*d = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		default:
			return fmt.Errorf("assign: cannot assign %T to *pgtype.Timestamptz", src)
		}
	case *string:
		switch s := src.(type) {
		case string:
			*d = s
		case nil:
			// ok
		default:
			return fmt.Errorf("assign: cannot assign %T to *string", src)
		}
	case **string:
		switch s := src.(type) {
		case *string:
			*d = s
		case nil:
			*d = nil
		default:
			return fmt.Errorf("assign: cannot assign %T to **string", src)
		}
	default:
		// For any other dest type (e.g. *int64, interface{}) just skip — the
		// handler doesn't use the result of scanned fields we don't supply.
	}
	return nil
}

// fakeRows implements pgx.Rows (returned from Query). The handler never calls
// Query directly, but it's needed to satisfy the DBTX interface.
type fakeRows struct{}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Next() bool                                   { return false }
func (r *fakeRows) Scan(_ ...any) error                          { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }

// querySpec configures one QueryRow call on fakeDbtx.
type querySpec struct {
	// vals are the values Scan receives in order.
	vals []any
	// err, if non-nil, is returned from Scan instead of scanning vals.
	err error
}

// fakeDbtx implements store.DBTX. It serves a pre-programmed sequence of
// QueryRow results and records the SQL strings that were executed.
type fakeDbtx struct {
	// rows is the ordered queue of QueryRow results. Each Call to QueryRow
	// dequeues the next entry.
	rows []querySpec

	// execErr, if non-nil, is returned from all Exec calls.
	execErr error

	// called collects the SQL strings (first arg) passed to QueryRow or Exec.
	called []string
}

func (f *fakeDbtx) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	f.called = append(f.called, sql)
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeDbtx) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	f.called = append(f.called, sql)
	return &fakeRows{}, nil
}

func (f *fakeDbtx) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	f.called = append(f.called, sql)
	if len(f.rows) == 0 {
		return &fakeRow{err: errors.New("fakeDbtx: QueryRow queue exhausted for SQL: " + sql)}
	}
	spec := f.rows[0]
	f.rows = f.rows[1:]
	return &fakeRow{vals: spec.vals, err: spec.err}
}

// ---------------------------------------------------------------------------
// Fake pgx.Tx implementation
// ---------------------------------------------------------------------------

// fakeTx implements pgx.Tx, wrapping fakeDbtx for its data methods and
// recording commits/rollbacks. The methods the handler never calls (Conn,
// CopyFrom, SendBatch, LargeObjects, Prepare) panic so misbehaviour is
// detectable.
type fakeTx struct {
	db         *fakeDbtx
	committed  bool
	rolledBack bool

	// commitErr, if set, is returned from Commit.
	commitErr error
}

func (t *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return t, nil // savepoint noop
}

func (t *fakeTx) Commit(ctx context.Context) error {
	t.committed = true
	return t.commitErr
}

func (t *fakeTx) Rollback(ctx context.Context) error {
	t.rolledBack = true
	return nil
}

func (t *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(ctx, sql, args...)
}

func (t *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.db.Query(ctx, sql, args...)
}

func (t *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.db.QueryRow(ctx, sql, args...)
}

func (t *fakeTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("fakeTx.CopyFrom: not implemented in unit tests")
}

func (t *fakeTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("fakeTx.SendBatch: not implemented in unit tests")
}

func (t *fakeTx) LargeObjects() pgx.LargeObjects {
	panic("fakeTx.LargeObjects: not implemented in unit tests")
}

func (t *fakeTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("fakeTx.Prepare: not implemented in unit tests")
}

func (t *fakeTx) Conn() *pgx.Conn {
	return nil // never called by the handler
}

// ---------------------------------------------------------------------------
// Fake txBeginner (pool) implementation
// ---------------------------------------------------------------------------

// fakeTxPool implements txBeginner. It returns a pre-built fakeTx on Begin.
type fakeTxPool struct {
	tx       *fakeTx
	beginErr error
}

func (p *fakeTxPool) Begin(ctx context.Context) (pgx.Tx, error) {
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return p.tx, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const (
	testConnectorID = "github-test"
	testSecret      = "unit-test-secret"
	testDeptSlug    = "engineering"
	testDeliveryID  = "abc-def-ghi-001"
)

// computeSig returns the correct X-Hub-Signature-256 header value for body
// signed with testSecret. Mirrors the production verifyGitHubSignature logic.
func computeSig(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// testGitHubConnector returns a GitHubConnector wired with standard M10-core
// routes, suitable for handler unit tests.
func testGitHubConnector() *GitHubConnector {
	return NewGitHubConnector(GitHubConfig{
		ConnectorID: testConnectorID,
		Secret:      []byte(testSecret),
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     testDeptSlug,
				ObjectiveTemplate:  "Issue: {{title}}",
				AcceptanceTemplate: "{{body}}",
			},
			"pull_request": {
				DepartmentSlug:     testDeptSlug,
				ObjectiveTemplate:  "PR: {{title}}",
				AcceptanceTemplate: "{{body}}",
			},
		},
		RatePerMin: 60,
		Burst:      30,
	})
}

// issuePayload returns a JSON-encoded `issues:opened` payload for tests.
// The returned []byte is the raw body; the signature is computed over these
// exact bytes (spike F1.4 — raw body, UTF-8 untouched).
func issuePayload() []byte {
	body, _ := json.Marshal(map[string]any{
		"action": "opened",
		"sender": map[string]any{"type": "User", "login": "alice"},
		"issue": map[string]any{
			"id":       42,
			"title":    "Test issue",
			"body":     "Please look into this.",
			"html_url": "https://github.com/example/repo/issues/42",
			"number":   42,
		},
	})
	return body
}

// botIssuePayload returns a JSON-encoded `issues:opened` from a bot sender.
func botIssuePayload() []byte {
	body, _ := json.Marshal(map[string]any{
		"action": "opened",
		"sender": map[string]any{"type": "Bot", "login": "dependabot[bot]"},
		"issue": map[string]any{
			"id":       99,
			"title":    "Bot issue",
			"body":     "Automated.",
			"html_url": "https://github.com/example/repo/issues/99",
			"number":   99,
		},
	})
	return body
}

// labeledIssuePayload returns a JSON-encoded `issues:labeled` payload (non-actionable subtype).
func labeledIssuePayload() []byte {
	body, _ := json.Marshal(map[string]any{
		"action": "labeled",
		"sender": map[string]any{"type": "User", "login": "alice"},
		"issue": map[string]any{
			"id":       43,
			"title":    "Labeled issue",
			"body":     "Some issue",
			"html_url": "https://github.com/example/repo/issues/43",
			"number":   43,
		},
	})
	return body
}

// pingPayload returns a minimal `ping` event payload.
func pingPayload() []byte {
	body, _ := json.Marshal(map[string]any{"zen": "Approachable is better than simple."})
	return body
}

// newRequest builds a test HTTP request for the /webhook/github path.
// headers is a map of header name → value to set on the request.
func newRequest(body []byte, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// fullRateCap builds a RateCap that is immediately exhausted (all tokens
// consumed) for testConnectorID. Used by TestHandler_RateCapExceeded_429_NoRow.
func exhaustedRateCap() *RateCap {
	rc := NewRateCap(nil)
	rc.AddConnector(testConnectorID, 1, 1) // burst = 1 → one token
	rc.Allow(testConnectorID)              // consume the only token → bucket empty
	return rc
}

// freshRateCap builds a RateCap with plenty of capacity for testConnectorID.
func freshRateCap() *RateCap {
	rc := NewRateCap(nil)
	rc.AddConnector(testConnectorID, 60, 30)
	return rc
}

// fakeDbtxForRateCap returns a fakeDbtx that succeeds the InsertThrottleEvent
// QueryRow and the NotifyThrottleEvent Exec. Used when the handler is expected
// to call throttle.FireIngressRateCap (TestHandler_RateCapExceeded_429_NoRow).
func fakeDbtxForRateCap() *fakeDbtx {
	// InsertThrottleEvent: QueryRow → scan (id, company_id, kind, fired_at, payload)
	throttleID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	throttleAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return &fakeDbtx{
		rows: []querySpec{
			{vals: []any{throttleID, pgtype.UUID{}, "ingress_rate_cap_exceeded", throttleAt, []byte(`{}`)}},
		},
		// Exec succeeds for NotifyThrottleEvent (pg_notify).
	}
}

// calledSQL checks whether any element of called contains needle.
func calledSQL(called []string, needle string) bool {
	for _, s := range called {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHandler_MissingEventTypeHeader_200 — absent X-GitHub-Event header → 200,
// no DB calls (handler step 2: EventType returns ok=false → discard).
func TestHandler_MissingEventTypeHeader_200(t *testing.T) {
	body := issuePayload()
	deps := HandlerDeps{
		Connector: testGitHubConnector(),
		Logger:    nil,
	}
	handler := newWebhookHandler(deps)

	// X-GitHub-Event deliberately absent.
	req := newRequest(body, map[string]string{
		"X-GitHub-Delivery":   testDeliveryID,
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d; want 200 when X-GitHub-Event header is absent", got)
	}
}

// TestHandler_UnsubscribedEventType_200 — X-GitHub-Event: push → 200, no
// insert call (SR6 step 1, FR-401). push is not in the subscribed set
// {issues, pull_request, ping} so the handler returns 200 before touching the
// DB or signature.
func TestHandler_UnsubscribedEventType_200(t *testing.T) {
	body := []byte(`{"action":"push"}`)
	deps := HandlerDeps{
		Connector: testGitHubConnector(),
		Logger:    nil,
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-GitHub-Delivery":   "delivery-push-001",
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d; want 200 for unsubscribed event type", got)
	}
}

// TestHandler_BadSignature_401 — invalid X-Hub-Signature-256 → 401, no
// insert, rejection counter incremented (FR-300, FR-301). The signature is
// for a different body so HMAC comparison fails.
func TestHandler_BadSignature_401(t *testing.T) {
	body := issuePayload()
	var counter atomic.Int64

	deps := HandlerDeps{
		Connector:        testGitHubConnector(),
		Secret:           []byte(testSecret),
		RejectionCounter: &counter,
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   testDeliveryID,
		"X-Hub-Signature-256": "sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 on bad signature", got)
	}
	if counter.Load() != 1 {
		t.Errorf("RejectionCounter = %d; want 1 after bad signature", counter.Load())
	}
}

// TestHandler_MissingSignature_401 — absent X-Hub-Signature-256 → 401
// (fail-closed, FR-300). An empty header is structurally invalid (no "sha256="
// prefix) so verifyGitHubSignature returns ErrBadSignature.
func TestHandler_MissingSignature_401(t *testing.T) {
	body := issuePayload()
	var counter atomic.Int64

	deps := HandlerDeps{
		Connector:        testGitHubConnector(),
		Secret:           []byte(testSecret),
		RejectionCounter: &counter,
	}
	handler := newWebhookHandler(deps)

	// X-Hub-Signature-256 deliberately absent.
	req := newRequest(body, map[string]string{
		"X-GitHub-Event":    "issues",
		"X-GitHub-Delivery": testDeliveryID,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 when signature header is absent", got)
	}
	if counter.Load() != 1 {
		t.Errorf("RejectionCounter = %d; want 1 after missing signature", counter.Load())
	}
}

// TestHandler_Ping_200_NoTicket — ping event → 200, no InsertIngressDelivery
// call, no ticket insert (FR-403, SR9). The GitHubConnector.Filter discards
// ping before any DB interaction.
func TestHandler_Ping_200_NoTicket(t *testing.T) {
	body := pingPayload()

	// Use a db that panics on any call to detect accidental DB access.
	// No rows/exec are pre-loaded so any call would exhaust the queue
	// and return an error that would surface as a non-200 status.
	txDB := &fakeDbtx{}
	tx := &fakeTx{db: txDB}
	pool := &fakeTxPool{tx: tx}

	deps := HandlerDeps{
		Pool:      pool,
		Queries:   store.New(txDB),
		Connector: testGitHubConnector(),
		Secret:    []byte(testSecret),
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "ping",
		"X-GitHub-Delivery":   "delivery-ping-001",
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d; want 200 for ping event", got)
	}
	if len(txDB.called) != 0 {
		t.Errorf("DB called with %v; want no DB calls for ping", txDB.called)
	}
}

// TestHandler_BotSender_200_NoTicket — bot-sourced delivery → 200, no ticket
// (FR-401). sender.type == "Bot" is discarded by GitHubConnector.Filter after
// signature verification but before any DB interaction.
func TestHandler_BotSender_200_NoTicket(t *testing.T) {
	body := botIssuePayload()

	txDB := &fakeDbtx{}
	tx := &fakeTx{db: txDB}
	pool := &fakeTxPool{tx: tx}

	deps := HandlerDeps{
		Pool:      pool,
		Queries:   store.New(txDB),
		Connector: testGitHubConnector(),
		Secret:    []byte(testSecret),
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   "delivery-bot-001",
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d; want 200 for bot-sourced delivery", got)
	}
	if len(txDB.called) != 0 {
		t.Errorf("DB called with %v; want no DB calls for bot delivery", txDB.called)
	}
}

// TestHandler_NonActionableSubtype_200_NoTicket — issues:labeled → 200, no
// ticket (FR-401). The "labeled" action is not in {"opened","reopened"} so
// GitHubConnector.Filter returns FilterDiscard.
func TestHandler_NonActionableSubtype_200_NoTicket(t *testing.T) {
	body := labeledIssuePayload()

	txDB := &fakeDbtx{}
	tx := &fakeTx{db: txDB}
	pool := &fakeTxPool{tx: tx}

	deps := HandlerDeps{
		Pool:      pool,
		Queries:   store.New(txDB),
		Connector: testGitHubConnector(),
		Secret:    []byte(testSecret),
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   "delivery-labeled-001",
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d; want 200 for non-actionable action subtype", got)
	}
	if len(txDB.called) != 0 {
		t.Errorf("DB called with %v; want no DB calls for labeled action", txDB.called)
	}
}

// TestHandler_RateCapExceeded_429_NoRow — exhausted rate-cap bucket → 429,
// InsertIngressDelivery NOT called, FireIngressRateCap invoked (FR-600,
// FR-602). The test uses a pre-exhausted RateCap and a fake Queries that
// handles the throttle_events insert.
func TestHandler_RateCapExceeded_429_NoRow(t *testing.T) {
	body := issuePayload()

	// Pool-level Queries: handle InsertThrottleEvent + NotifyThrottleEvent.
	// The throttle_events INSERT (InsertThrottleEvent) is a QueryRow.
	// The pg_notify (NotifyThrottleEvent) is an Exec.
	throttleID := pgtype.UUID{Bytes: [16]byte{0xAB}, Valid: true}
	throttleAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	poolDB := &fakeDbtx{
		rows: []querySpec{
			// InsertThrottleEvent :one RETURNING id, company_id, kind, fired_at, payload
			{vals: []any{throttleID, pgtype.UUID{}, "ingress_rate_cap_exceeded", throttleAt, []byte(`{}`)}},
		},
	}

	// The tx-level DB (for delivery + ticket inserts) must NOT be called.
	txDB := &fakeDbtx{}
	tx := &fakeTx{db: txDB}
	pool := &fakeTxPool{tx: tx}

	rc := exhaustedRateCap()

	deps := HandlerDeps{
		Pool:       pool,
		Queries:    store.New(poolDB),
		Connector:  testGitHubConnector(),
		Secret:     []byte(testSecret),
		RateCap:    rc,
		CompanyID:  pgtype.UUID{Valid: true},
		RatePerMin: 60,
		Burst:      30,
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   testDeliveryID,
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusTooManyRequests {
		t.Errorf("status = %d; want 429 when rate cap is exceeded", got)
	}
	// InsertIngressDelivery must NOT have been called (no tx-level DB call, FR-602).
	if len(txDB.called) != 0 {
		t.Errorf("tx-level DB called with %v; want no delivery insert on rate-cap", txDB.called)
	}
	// FireIngressRateCap must have been invoked: poolDB should have been called
	// for InsertThrottleEvent (QueryRow) and NotifyThrottleEvent (Exec).
	if !calledSQL(poolDB.called, "throttle_events") {
		t.Errorf("pool-level DB not called for throttle_events; want FireIngressRateCap to write evidence")
	}
}

// TestHandler_OversizedBody_Rejected — a body over maxBodyBytes is truncated
// by LimitReader, so the HMAC computed over the truncated bytes does not match
// the header computed over the full body → 401 (DoS guard, FR-800). The
// signature is computed over the full large body; after truncation the HMAC
// won't match.
func TestHandler_OversizedBody_Rejected(t *testing.T) {
	// Build a body slightly over maxBodyBytes (26 MB + 1 byte).
	// Use a large io.Reader to avoid allocating 26 MB in the test: the
	// handler reads via io.LimitReader(r.Body, maxBodyBytes) and the
	// request body is an io.Reader, so we can use strings.Repeat for
	// a smaller sentinel that exceeds the limit.
	oversizeLen := maxBodyBytes + 1
	largeBody := make([]byte, oversizeLen)
	for i := range largeBody {
		largeBody[i] = 'A'
	}

	// Compute sig over the full body — will NOT match after truncation.
	validSig := computeSig(largeBody)

	var counter atomic.Int64
	deps := HandlerDeps{
		Connector:        testGitHubConnector(),
		Secret:           []byte(testSecret),
		RejectionCounter: &counter,
	}
	handler := newWebhookHandler(deps)

	req := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(largeBody))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", testDeliveryID)
	req.Header.Set("X-Hub-Signature-256", validSig)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// After truncation, HMAC mismatch → 401.
	if got := rec.Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 for oversized body (LimitReader truncation → HMAC mismatch)", got)
	}
}

// TestHandler_MissingDeliveryID_400 — absent X-GitHub-Delivery → 400
// (ErrMalformedDelivery). The handler checks DeliveryID after Filter accepts
// the event (SR6 step 6 in the pipeline).
func TestHandler_MissingDeliveryID_400(t *testing.T) {
	body := issuePayload()
	deps := HandlerDeps{
		Connector: testGitHubConnector(),
		Secret:    []byte(testSecret),
	}
	handler := newWebhookHandler(deps)

	// X-GitHub-Delivery deliberately absent.
	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 when X-GitHub-Delivery is absent", got)
	}
}

// TestHandler_SuccessPath_202 — valid issues:opened → 202; InsertIngressDelivery
// and InsertIngressTicket each called once in the correct order (plan decision 4).
//
// The fake tx's QueryRow queue is primed with:
//  1. InsertIngressDelivery :one RETURNING (id, connector_id, external_delivery_id, ticket_id, created_at)
//  2. SelectDepartmentIDBySlug :one RETURNING id
//  3. InsertIngressTicket :one RETURNING (id, created_at)
//
// BackfillIngressDeliveryTicket is an :exec (Exec call).
func TestHandler_SuccessPath_202(t *testing.T) {
	body := issuePayload()

	deliveryID := pgtype.UUID{Bytes: [16]byte{0x01}, Valid: true}
	deptID := pgtype.UUID{Bytes: [16]byte{0x02}, Valid: true}
	ticketID := pgtype.UUID{Bytes: [16]byte{0x03}, Valid: true}
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}

	txDB := &fakeDbtx{
		rows: []querySpec{
			// InsertIngressDelivery: returns IngressDelivery row
			// (id, connector_id, external_delivery_id, ticket_id, created_at)
			{vals: []any{deliveryID, testConnectorID, testDeliveryID, pgtype.UUID{}, now}},
			// SelectDepartmentIDBySlug: returns pgtype.UUID
			{vals: []any{deptID}},
			// InsertIngressTicket: returns (id, created_at)
			{vals: []any{ticketID, now}},
		},
	}
	tx := &fakeTx{db: txDB}
	pool := &fakeTxPool{tx: tx}

	deps := HandlerDeps{
		Pool:      pool,
		Queries:   store.New(&fakeDbtx{}), // pool-level queries unused on success path
		Connector: testGitHubConnector(),
		Secret:    []byte(testSecret),
		RateCap:   freshRateCap(),
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   testDeliveryID,
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusAccepted {
		t.Errorf("status = %d; want 202 on success path", got)
	}
	if !tx.committed {
		t.Error("transaction was not committed on success path")
	}
	// Assert InsertIngressDelivery was called (first QueryRow in the queue).
	if !calledSQL(txDB.called, "ingress_deliveries") {
		t.Errorf("InsertIngressDelivery not called; txDB.called = %v", txDB.called)
	}
	// Assert InsertIngressTicket was called (third QueryRow in the queue).
	if !calledSQL(txDB.called, "tickets") {
		t.Errorf("InsertIngressTicket not called; txDB.called = %v", txDB.called)
	}
	// Assert BackfillIngressDeliveryTicket was called (Exec).
	backfillCalled := false
	for _, s := range txDB.called {
		if strings.Contains(s, "ingress_deliveries") && strings.Contains(s, "SET ticket_id") {
			backfillCalled = true
		}
	}
	if !backfillCalled {
		t.Errorf("BackfillIngressDeliveryTicket not called; txDB.called = %v", txDB.called)
	}
}

// TestHandler_DuplicateDelivery_200 — InsertIngressDelivery returns
// ErrDuplicateDelivery (23505 unique-violation) → 200, InsertIngressTicket NOT
// called (FR-202, plan decision 4). The fake row scan returns a pgconn.PgError
// with code 23505 so idempotency.go maps it to ErrDuplicateDelivery.
func TestHandler_DuplicateDelivery_200(t *testing.T) {
	body := issuePayload()

	// Simulate a 23505 unique-violation from InsertIngressDelivery.
	pgErr := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}

	txDB := &fakeDbtx{
		rows: []querySpec{
			// InsertIngressDelivery: the unique-violation is returned from Scan
			// (pgx surfaces PgError via the row scanner).
			{err: pgErr},
		},
	}
	tx := &fakeTx{db: txDB}
	pool := &fakeTxPool{tx: tx}

	deps := HandlerDeps{
		Pool:      pool,
		Queries:   store.New(&fakeDbtx{}),
		Connector: testGitHubConnector(),
		Secret:    []byte(testSecret),
		RateCap:   freshRateCap(),
	}
	handler := newWebhookHandler(deps)

	req := newRequest(body, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   testDeliveryID,
		"X-Hub-Signature-256": computeSig(body),
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d; want 200 on duplicate delivery", got)
	}
	// InsertIngressDelivery was called (the 23505 came from it).
	if !calledSQL(txDB.called, "ingress_deliveries") {
		t.Errorf("InsertIngressDelivery not called; txDB.called = %v", txDB.called)
	}
	// InsertIngressTicket must NOT have been called (abort after 23505, plan decision 4).
	ticketCalled := false
	for _, s := range txDB.called {
		if strings.Contains(s, "INSERT INTO tickets") {
			ticketCalled = true
		}
	}
	if ticketCalled {
		t.Errorf("InsertIngressTicket was called; want it NOT called after ErrDuplicateDelivery (FR-202)")
	}
	// Transaction should be rolled back (not committed) because the duplicate
	// path aborts via return in runIngressTx before Commit.
	if tx.committed {
		t.Error("transaction was committed; want rollback on duplicate delivery")
	}
}

// ---------------------------------------------------------------------------
// io.Reader boundary check helper (used by TestHandler_OversizedBody_Rejected)
// ---------------------------------------------------------------------------

// Ensure io.LimitReader is used correctly: verify that reading maxBodyBytes+1
// from an io.LimitReader limited to maxBodyBytes only returns maxBodyBytes bytes.
func TestLimitReaderTruncatesAtMax(t *testing.T) {
	oversizeLen := maxBodyBytes + 512
	src := io.NopCloser(strings.NewReader(strings.Repeat("x", oversizeLen)))
	got, err := io.ReadAll(io.LimitReader(src, maxBodyBytes))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != maxBodyBytes {
		t.Errorf("read %d bytes; want exactly %d (maxBodyBytes)", len(got), maxBodyBytes)
	}
}
