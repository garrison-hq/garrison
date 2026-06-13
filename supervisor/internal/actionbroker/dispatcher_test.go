//go:build integration

// Package actionbroker — dispatcher_test.go
//
// Integration-tagged unit tests for dispatcher.go (the per-row state machine
// of Worker.Handle). These tests run against a real Postgres testcontainer
// for transaction correctness (FOR UPDATE, status transitions, outcome writes)
// while injecting fake VaultFetcher and GitHubPoster collaborators so no real
// vault or GitHub API is reached.
//
// Tests named in tasks.md T009 (all must pass):
//
//   - TestHandleApprovedActionPostsAndRecordsExecuted   (US2 #2 / SC-007)
//   - TestHandleNeverExecutesPendingApprove             (US4 #3)
//   - TestHandleNeverExecutesHumanOnly                  (US4 #4 / FR-017)
//   - TestHandleAutoTierExecutesWithoutGate             (US4 #1)
//   - TestHandleNotifyTierExecutesThenNotifies          (US4 #2 / FR-028)
//   - TestHandleVaultUnavailableFailsClosed             (FR-023 / SC-005)
//   - TestHandleRecoverableFailureMarksFailedNoRetry    (FR-022 / Edge Cases)
package actionbroker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Fake VaultFetcher
// ---------------------------------------------------------------------------

// fakeVault implements VaultFetcher. When errToReturn is non-nil it returns
// that error; otherwise it returns a map with "GITHUB_PAT" → the given PAT.
type fakeVault struct {
	pat         string
	errToReturn error
}

func (f *fakeVault) Fetch(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
	if f.errToReturn != nil {
		return nil, f.errToReturn
	}
	return map[string]vault.SecretValue{
		"GITHUB_PAT": vault.New([]byte(f.pat)),
	}, nil
}

// ---------------------------------------------------------------------------
// Fake GitHubPoster
// ---------------------------------------------------------------------------

// fakeGitHub implements GitHubPoster. It records every PostComment call and
// returns a fixed URL or a scripted error.
type fakeGitHub struct {
	calls       int
	urlToReturn string
	errToReturn error
}

func (f *fakeGitHub) PostComment(_ context.Context, _ Target, _, _ string) (string, error) {
	f.calls++
	return f.urlToReturn, f.errToReturn
}

// ---------------------------------------------------------------------------
// Test fixture
// ---------------------------------------------------------------------------

// dispatcherFixture holds the minimum working set for a dispatcher unit test:
// a migrated pool, a seeded agent_instances row (required by the FK on
// pending_actions.agent_instance_id), and constructed fake collaborators.
type dispatcherFixture struct {
	pool            *pgxpool.Pool
	queries         *store.Queries
	agentInstanceID pgtype.UUID
	vault           *fakeVault
	github          *fakeGitHub
}

// setupDispatcher seeds the test database and returns a dispatcherFixture.
// It registers a t.Cleanup that truncates the tables it touches.
func setupDispatcher(t *testing.T) dispatcherFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()

	// Seed the minimum required FK chain: company → department → agent → agent_instance → ticket.
	// The pending_actions FK only requires agent_instance_id; ticket_id is optional.
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('dispatch-test-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (company_id, slug, name, workspace_path)
		VALUES ($1, 'engineering', 'Engineering', '/tmp/dispatch')
		RETURNING id`, companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("seed department: %v", err)
	}

	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
		VALUES ($1, 'engineer', '[]'::jsonb, 'md', 'claude-x', 'active')
		RETURNING id`, deptID,
	).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// The pending_actions FK references agent_instances; seed a row for it.
	// We also need a ticket_id reference since agent_instances.ticket_id is NOT NULL.
	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (department_id, objective, column_slug)
		VALUES ($1, 'dispatch test ticket', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	var instanceID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		VALUES ($1, 'engineer', $2, 'running')
		RETURNING id`, deptID, ticketID,
	).Scan(&instanceID); err != nil {
		t.Fatalf("seed agent_instances: %v", err)
	}

	// Truncate M11 tables after the test so the next test starts clean.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"TRUNCATE work.pending_action_outcomes, work.pending_actions CASCADE")
	})

	return dispatcherFixture{
		pool:            pool,
		queries:         store.New(pool),
		agentInstanceID: instanceID,
		vault:           &fakeVault{pat: "fake-pat-token"},
		github:          &fakeGitHub{urlToReturn: "https://github.com/owner/repo/issues/1#issuecomment-100"},
	}
}

// newWorker builds a Worker from the fixture with the given collaborator
// overrides applied. The logger writes to os.Stderr at debug level.
func (fx dispatcherFixture) newWorker() (*Worker, error) {
	return New(Deps{
		Pool:    fx.pool,
		Queries: fx.queries,
		Vault:   fx.vault,
		GitHub:  fx.github,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PATPath: "actions/GITHUB_PAT",
		Now:     func() time.Time { return time.Now().UTC() },
	})
}

// insertPendingAction inserts a pending_actions row with the given tier and
// status, and returns its UUID. Used by tests to seed dispatchable rows.
func (fx dispatcherFixture) insertPendingAction(
	t *testing.T,
	actionType, tier, status string,
	target []byte,
) pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	// The CHECK constraint pending_actions_floor_is_approve prevents inserting
	// github_issue_comment with tier != 'approve'. For tests that need an
	// auto/notify-tier row, use a non-floor action type.
	var id pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `
		INSERT INTO pending_actions
		    (action_type, target, rendered_payload, agent_instance_id, tier, tier_reason, status)
		VALUES ($1, $2::jsonb, 'test payload', $3, $4, 'test-reason', $5)
		RETURNING id`,
		actionType, string(target), fx.agentInstanceID, tier, status,
	).Scan(&id); err != nil {
		t.Fatalf("insertPendingAction(%s, %s, %s): %v", actionType, tier, status, err)
	}
	return id
}

// outcomeCount returns the number of pending_action_outcomes rows for the given action ID.
func (fx dispatcherFixture) outcomeCount(t *testing.T, actionID pgtype.UUID) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_action_outcomes WHERE pending_action_id = $1`, actionID,
	).Scan(&n); err != nil {
		t.Fatalf("outcomeCount: %v", err)
	}
	return n
}

// outcomeWithType returns true when at least one outcome row for the action has the given outcome value.
func (fx dispatcherFixture) hasOutcome(t *testing.T, actionID pgtype.UUID, outcome string) bool {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_action_outcomes WHERE pending_action_id = $1 AND outcome = $2`,
		actionID, outcome,
	).Scan(&n); err != nil {
		t.Fatalf("hasOutcome: %v", err)
	}
	return n > 0
}

// readStatus reads the current status of a pending_actions row.
func (fx dispatcherFixture) readStatus(t *testing.T, actionID pgtype.UUID) string {
	t.Helper()
	var status string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&status); err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	return status
}

// readDispatchedAt reads the dispatched_at timestamptz (NULL → zero Time).
func (fx dispatcherFixture) readDispatchedAt(t *testing.T, actionID pgtype.UUID) pgtype.Timestamptz {
	t.Helper()
	var ts pgtype.Timestamptz
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT dispatched_at FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&ts); err != nil {
		t.Fatalf("readDispatchedAt: %v", err)
	}
	return ts
}

// readStructuredOutcome returns the structured_outcome JSONB for the first
// 'executed' outcome row of the given action.
func (fx dispatcherFixture) readExecutedStructuredOutcome(t *testing.T, actionID pgtype.UUID) map[string]string {
	t.Helper()
	var raw []byte
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT structured_outcome FROM pending_action_outcomes
		  WHERE pending_action_id = $1 AND outcome = 'executed'
		  ORDER BY created_at LIMIT 1`,
		actionID,
	).Scan(&raw); err != nil {
		t.Fatalf("readExecutedStructuredOutcome: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse structured_outcome JSON: %v", err)
	}
	return m
}

// githubTarget is the JSONB target for github_issue_comment rows.
var githubTarget = []byte(`{"owner":"test-org","repo":"test-repo","issue_number":42}`)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHandleApprovedActionPostsAndRecordsExecuted — US2 #2 / SC-007.
//
// An approve-tier row at status='approved' is dispatched: the fake
// GitHubPoster is called exactly once, the row transitions to 'executed',
// dispatched_at is set, and an 'executed' outcome row carries
// structured_outcome.comment_url.
func TestHandleApprovedActionPostsAndRecordsExecuted(t *testing.T) {
	fx := setupDispatcher(t)
	commentURL := "https://github.com/test-org/test-repo/issues/42#issuecomment-999"
	fx.github.urlToReturn = commentURL

	actionID := fx.insertPendingAction(t, "github_issue_comment", "approve", "approved", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment called exactly once.
	if fx.github.calls != 1 {
		t.Errorf("PostComment calls = %d; want 1", fx.github.calls)
	}

	// Status transitioned to 'executed'.
	if got := fx.readStatus(t, actionID); got != "executed" {
		t.Errorf("status = %q; want 'executed'", got)
	}

	// dispatched_at is set.
	if ts := fx.readDispatchedAt(t, actionID); !ts.Valid {
		t.Error("dispatched_at is NULL; want a timestamp")
	}

	// One 'executed' outcome row with structured_outcome.comment_url.
	if !fx.hasOutcome(t, actionID, "executed") {
		t.Error("no 'executed' outcome row found")
	}
	so := fx.readExecutedStructuredOutcome(t, actionID)
	if so["comment_url"] != commentURL {
		t.Errorf("structured_outcome.comment_url = %q; want %q", so["comment_url"], commentURL)
	}
}

// TestHandleNeverExecutesPendingApprove — US4 #3.
//
// An approve-tier row still at status='pending' (not yet approved by the
// operator) is NOT dispatched. The claim query requires status='approved' for
// approve-tier rows, so Handle finds nothing and returns a no-op.
// PostComment is never called.
func TestHandleNeverExecutesPendingApprove(t *testing.T) {
	fx := setupDispatcher(t)

	// Insert an approve-tier row at 'pending' (awaiting operator click).
	actionID := fx.insertPendingAction(t, "github_issue_comment", "approve", "pending", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment must not have been called.
	if fx.github.calls != 0 {
		t.Errorf("PostComment calls = %d; want 0 — approve-tier pending row must not be dispatched", fx.github.calls)
	}

	// Status remains 'pending'.
	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Errorf("status = %q; want 'pending'", got)
	}
}

// TestHandleNeverExecutesHumanOnly — US4 #4 / FR-017.
//
// A human_only row must never be dispatched; when one is encountered (via the
// ClaimFn test seam that bypasses the claim filter), the dispatcher writes a
// 'skipped_human_only' outcome and commits without calling PostComment.
//
// Note: ClaimDispatchablePendingAction filters tier <> 'human_only', so the
// row is never claimed through the normal LISTEN path. The ClaimFn seam
// (added to Deps by T009) lets this test inject the row directly to exercise
// the defence-in-depth guard in Handle.
func TestHandleNeverExecutesHumanOnly(t *testing.T) {
	fx := setupDispatcher(t)

	// Use a non-floor action type for the human_only row — the CHECK constraint
	// only restricts github_issue_comment. We use a fictional type.
	actionID := fx.insertPendingAction(t, "test_human_only_op", "human_only", "pending", []byte(`{}`))

	// Re-read the full row so we can inject it via ClaimFn.
	var humanRow store.PendingAction
	if err := fx.pool.QueryRow(context.Background(), `
		SELECT id, action_type, target, rendered_payload, agent_instance_id, ticket_id,
		       tier, tier_reason, status, approved_by, dispatched_at, created_at
		  FROM pending_actions WHERE id = $1`, actionID,
	).Scan(
		&humanRow.ID, &humanRow.ActionType, &humanRow.Target,
		&humanRow.RenderedPayload, &humanRow.AgentInstanceID, &humanRow.TicketID,
		&humanRow.Tier, &humanRow.TierReason, &humanRow.Status,
		&humanRow.ApprovedBy, &humanRow.DispatchedAt, &humanRow.CreatedAt,
	); err != nil {
		t.Fatalf("read human_only row: %v", err)
	}

	w, err := New(Deps{
		Pool:    fx.pool,
		Queries: fx.queries,
		Vault:   fx.vault,
		GitHub:  fx.github,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PATPath: "actions/GITHUB_PAT",
		// ClaimFn test seam: return the human_only row regardless of the
		// real claim filter (which would exclude it because tier <> 'human_only').
		ClaimFn: func(_ context.Context, _ *store.Queries) (store.PendingAction, error) {
			return humanRow, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment must NOT have been called.
	if fx.github.calls != 0 {
		t.Errorf("PostComment calls = %d; want 0 — human_only rows are never dispatched (FR-017)", fx.github.calls)
	}

	// A 'skipped_human_only' outcome must have been written.
	if !fx.hasOutcome(t, actionID, "skipped_human_only") {
		t.Error("expected a 'skipped_human_only' outcome row; found none")
	}

	// The row must NOT have transitioned to a terminal dispatch status.
	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Errorf("status = %q; want 'pending' — human_only rows must not be status-transitioned by the dispatcher", got)
	}

	// dispatched_at must remain NULL.
	if ts := fx.readDispatchedAt(t, actionID); ts.Valid {
		t.Error("dispatched_at is set; want NULL — human_only rows must not be marked executed")
	}
}

// TestHandleAutoTierExecutesWithoutGate — US4 #1.
//
// An auto-tier row at status='pending' is dispatched immediately without
// requiring an operator approval. PostComment is called once and the row
// transitions to 'executed'.
func TestHandleAutoTierExecutesWithoutGate(t *testing.T) {
	fx := setupDispatcher(t)
	commentURL := "https://github.com/test-org/test-repo/issues/1#issuecomment-auto"
	fx.github.urlToReturn = commentURL

	// auto-tier uses a non-floor action type to avoid the CHECK constraint.
	actionID := fx.insertPendingAction(t, "test_auto_action", "auto", "pending", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment called exactly once — no gate.
	if fx.github.calls != 1 {
		t.Errorf("PostComment calls = %d; want 1 (auto-tier dispatches without gate)", fx.github.calls)
	}

	// Status transitioned to 'executed'.
	if got := fx.readStatus(t, actionID); got != "executed" {
		t.Errorf("status = %q; want 'executed'", got)
	}

	// 'executed' outcome row present.
	if !fx.hasOutcome(t, actionID, "executed") {
		t.Error("no 'executed' outcome row found")
	}
}

// TestHandleNotifyTierExecutesThenNotifies — US4 #2 / FR-028.
//
// A notify-tier row is dispatched (PostComment called once) and then both
// an 'executed' and a 'notified' outcome row are written so the Outbox can
// surface it as a post-hoc feed item (D17/FR-028).
func TestHandleNotifyTierExecutesThenNotifies(t *testing.T) {
	fx := setupDispatcher(t)
	commentURL := "https://github.com/test-org/test-repo/issues/2#issuecomment-notify"
	fx.github.urlToReturn = commentURL

	actionID := fx.insertPendingAction(t, "test_notify_action", "notify", "pending", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment called exactly once.
	if fx.github.calls != 1 {
		t.Errorf("PostComment calls = %d; want 1", fx.github.calls)
	}

	// Status transitioned to 'executed'.
	if got := fx.readStatus(t, actionID); got != "executed" {
		t.Errorf("status = %q; want 'executed'", got)
	}

	// Both 'executed' and 'notified' outcome rows must be present.
	if !fx.hasOutcome(t, actionID, "executed") {
		t.Error("no 'executed' outcome row found for notify-tier action")
	}
	if !fx.hasOutcome(t, actionID, "notified") {
		t.Error("no 'notified' outcome row found for notify-tier action (FR-028)")
	}

	// Total outcomes = 2 (executed + notified).
	if n := fx.outcomeCount(t, actionID); n != 2 {
		t.Errorf("outcome count = %d; want 2 (executed + notified)", n)
	}
}

// TestHandleVaultUnavailableFailsClosed — FR-023 / SC-005.
//
// When the vault is unavailable (Fetch returns an error), the dispatcher:
//   - does NOT call PostComment (no execution attempt with missing credentials),
//   - marks the row status='failed',
//   - writes a 'failed' outcome with detail containing "vault unavailable".
//
// This is the fail-closed posture: the dispatcher never falls back to an
// unscoped credential or agent-visible path (FR-023).
func TestHandleVaultUnavailableFailsClosed(t *testing.T) {
	fx := setupDispatcher(t)
	fx.vault.errToReturn = errors.New("vault connection refused")

	// Use an approve-tier row at 'approved' so it's dispatchable.
	actionID := fx.insertPendingAction(t, "github_issue_comment", "approve", "approved", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment must NOT have been called (fail-closed, FR-023).
	if fx.github.calls != 0 {
		t.Errorf("PostComment calls = %d; want 0 — vault unavailable must fail-closed", fx.github.calls)
	}

	// Row must be marked 'failed'.
	if got := fx.readStatus(t, actionID); got != "failed" {
		t.Errorf("status = %q; want 'failed'", got)
	}

	// A 'failed' outcome with detail mentioning "vault unavailable".
	if !fx.hasOutcome(t, actionID, "failed") {
		t.Error("no 'failed' outcome row found")
	}

	var detail string
	if err := fx.pool.QueryRow(context.Background(), `
		SELECT detail FROM pending_action_outcomes
		 WHERE pending_action_id = $1 AND outcome = 'failed'
		 ORDER BY created_at LIMIT 1`, actionID,
	).Scan(&detail); err != nil {
		t.Fatalf("read outcome detail: %v", err)
	}
	if detail == "" {
		t.Error("outcome detail is empty; want it to mention 'vault unavailable'")
	}
}

// TestHandleRecoverableFailureMarksFailedNoRetry — FR-022 / Edge Cases.
//
// When PostComment returns ErrRecoverable (transient 5xx/rate-limit), the
// dispatcher:
//   - marks the row status='failed',
//   - writes one 'failed' outcome row,
//   - does NOT call PostComment again (no auto-retry — exactly-once discipline,
//     D12).
//
// The row surfaces in the Outbox for operator-initiated re-request.
func TestHandleRecoverableFailureMarksFailedNoRetry(t *testing.T) {
	fx := setupDispatcher(t)
	fx.github.errToReturn = ErrRecoverable

	// Use an approved approve-tier row.
	actionID := fx.insertPendingAction(t, "github_issue_comment", "approve", "approved", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment called exactly once — no auto-retry.
	if fx.github.calls != 1 {
		t.Errorf("PostComment calls = %d; want exactly 1 (no auto-retry, FR-022)", fx.github.calls)
	}

	// Row must be marked 'failed'.
	if got := fx.readStatus(t, actionID); got != "failed" {
		t.Errorf("status = %q; want 'failed'", got)
	}

	// One 'failed' outcome row.
	if !fx.hasOutcome(t, actionID, "failed") {
		t.Error("no 'failed' outcome row found")
	}

	// Exactly one outcome row (no retry means no second failed outcome).
	if n := fx.outcomeCount(t, actionID); n != 1 {
		t.Errorf("outcome count = %d; want 1 (no retry produces no second outcome)", n)
	}
}

// TestNewRejectsNilDeps verifies that New returns an error for each missing
// required Dep (Pool, Queries, Vault, GitHub). These are the four nil-guard
// branches inside New() (dispatcher.go lines 106–117).
func TestNewRejectsNilDeps(t *testing.T) {
	fx := setupDispatcher(t)

	// Pool nil.
	_, err := New(Deps{Pool: nil, Queries: fx.queries, Vault: fx.vault, GitHub: fx.github})
	if err == nil {
		t.Error("New(nil Pool) should return error")
	}

	// Queries nil.
	_, err = New(Deps{Pool: fx.pool, Queries: nil, Vault: fx.vault, GitHub: fx.github})
	if err == nil {
		t.Error("New(nil Queries) should return error")
	}

	// Vault nil.
	_, err = New(Deps{Pool: fx.pool, Queries: fx.queries, Vault: nil, GitHub: fx.github})
	if err == nil {
		t.Error("New(nil Vault) should return error")
	}

	// GitHub nil.
	_, err = New(Deps{Pool: fx.pool, Queries: fx.queries, Vault: fx.vault, GitHub: nil})
	if err == nil {
		t.Error("New(nil GitHub) should return error")
	}
}

// TestNewDefaultsLogger verifies that when Logger is nil, New substitutes
// slog.Default() so the worker never dereferences a nil logger. The
// returned worker must be non-nil (covers dispatcher.go line 119).
func TestNewDefaultsLogger(t *testing.T) {
	fx := setupDispatcher(t)
	w, err := New(Deps{
		Pool:    fx.pool,
		Queries: fx.queries,
		Vault:   fx.vault,
		GitHub:  fx.github,
		Logger:  nil, // explicitly nil → must be defaulted
	})
	if err != nil {
		t.Fatalf("New with nil Logger returned error: %v", err)
	}
	if w == nil {
		t.Fatal("New with nil Logger returned nil worker")
	}
}

// TestRunLoopCancelExits verifies that RunLoop returns nil immediately when
// the context is cancelled. This covers the <-ctx.Done() branch in RunLoop
// (dispatcher.go lines 128–156) — specifically the interval fallback path
// and the graceful exit.
func TestRunLoopCancelExits(t *testing.T) {
	fx := setupDispatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before RunLoop even starts

	deps := Deps{
		Pool:         fx.pool,
		Queries:      fx.queries,
		Vault:        fx.vault,
		GitHub:       fx.github,
		PollInterval: 1, // 1ns so the ticker fires fast if ctx isn't done yet
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunLoop(ctx, deps)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("RunLoop returned non-nil error on cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunLoop did not exit within 5s after context cancellation")
	}
}

// TestRunLoopDefaultsIntervalWhenZero verifies that RunLoop substitutes 30s
// when PollInterval is zero or negative (dispatcher.go lines 132–135).
// We cancel the context immediately so the test doesn't block 30s; the
// important thing is that the Ticker construction doesn't panic with ≤0.
func TestRunLoopDefaultsIntervalWhenZero(t *testing.T) {
	fx := setupDispatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deps := Deps{
		Pool:         fx.pool,
		Queries:      fx.queries,
		Vault:        fx.vault,
		GitHub:       fx.github,
		PollInterval: 0, // zero → must default to 30s internally
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunLoop(ctx, deps)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("RunLoop with zero interval returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunLoop did not exit within 5s after context cancellation")
	}
}

// TestHandleUnknownTierSkipsRow verifies the defence-in-depth path in
// Handle's switch that handles an unknown tier value (dispatcher.go
// lines 243–247). When a ClaimFn injects a row with an unrecognised tier
// string, Handle must skip it without calling PostComment and return nil
// so the dispatcher loop continues.
func TestHandleUnknownTierSkipsRow(t *testing.T) {
	fx := setupDispatcher(t)

	// Inject a row with a fabricated tier value via ClaimFn.
	// We cannot insert it into the DB (the tier CHECK would reject it),
	// so we construct the struct directly.
	unknownRow := store.PendingAction{
		ID:              pgtype.UUID{Valid: true, Bytes: [16]byte{0xAA, 0xBB, 0xCC, 0xDD}},
		ActionType:      "test_unknown_tier_action",
		Target:          []byte(`{}`),
		RenderedPayload: "test payload",
		AgentInstanceID: fx.agentInstanceID,
		Tier:            "totally_made_up_tier",
		TierReason:      "test",
		Status:          "pending",
	}

	w, err := New(Deps{
		Pool:    fx.pool,
		Queries: fx.queries,
		Vault:   fx.vault,
		GitHub:  fx.github,
		PATPath: "actions/GITHUB_PAT",
		ClaimFn: func(_ context.Context, _ *store.Queries) (store.PendingAction, error) {
			return unknownRow, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle with unknown tier returned error: %v", err)
	}

	// PostComment must NOT have been called.
	if fx.github.calls != 0 {
		t.Errorf("PostComment calls = %d; want 0 — unknown tier must be skipped", fx.github.calls)
	}
}

// TestHandleUnmarshalTargetError verifies the target-JSONB unmarshal error
// path (dispatcher.go lines 266–269): when the pending_actions.target is
// not valid JSON that can be decoded into Target, Handle marks the row
// 'failed' and writes a 'failed' outcome without calling PostComment.
func TestHandleUnmarshalTargetError(t *testing.T) {
	fx := setupDispatcher(t)

	// Insert an approve-tier row with a JSON target that is valid JSONB but
	// cannot unmarshal into Target (it is a JSON string, not an object).
	// Use the same schema-unqualified table name the fixture helpers use.
	var actionID pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(), `
		INSERT INTO pending_actions
		    (action_type, target, rendered_payload, agent_instance_id, tier, tier_reason, status)
		VALUES ('github_issue_comment', '"not-an-object"'::jsonb, 'test payload', $1, 'approve', 'test-reason', 'approved')
		RETURNING id`, fx.agentInstanceID,
	).Scan(&actionID); err != nil {
		t.Fatalf("insert malformed-target row: %v", err)
	}

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// PostComment must NOT have been called.
	if fx.github.calls != 0 {
		t.Errorf("PostComment calls = %d; want 0 — malformed target must not reach provider", fx.github.calls)
	}

	// Row must be marked 'failed'.
	if got := fx.readStatus(t, actionID); got != "failed" {
		t.Errorf("status = %q; want 'failed' on target unmarshal error", got)
	}

	// A 'failed' outcome must exist.
	if !fx.hasOutcome(t, actionID, "failed") {
		t.Error("no 'failed' outcome row found for target unmarshal error")
	}
}

// TestHandleTerminalProviderFailureMarksFailed verifies the terminal-
// failure path (dispatcher.go lines 290–292): when PostComment returns
// a non-ErrRecoverable error (e.g. 404/422), the dispatcher marks the row
// 'failed' and does not retry.
func TestHandleTerminalProviderFailureMarksFailed(t *testing.T) {
	fx := setupDispatcher(t)
	fx.github.errToReturn = errors.New("actionbroker: terminal provider error: HTTP 404")

	actionID := fx.insertPendingAction(t, "github_issue_comment", "approve", "approved", githubTarget)

	w, err := fx.newWorker()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := w.Handle(context.Background(), pgtype.UUID{}); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if fx.github.calls != 1 {
		t.Errorf("PostComment calls = %d; want exactly 1 (no retry on terminal error)", fx.github.calls)
	}

	if got := fx.readStatus(t, actionID); got != "failed" {
		t.Errorf("status = %q; want 'failed'", got)
	}

	if !fx.hasOutcome(t, actionID, "failed") {
		t.Error("no 'failed' outcome row found")
	}
}
