//go:build integration

// Package actionbroker — integration_test.go
//
// Golden-path milestone smoke tests for the M11 Action Broker (T011).
// Each test exercises the full pipeline against a real Postgres testcontainer
// and an httptest GitHub stand-in; no real vault or GitHub API is called.
//
// Tests named in tasks.md T011 (all must pass):
//
//   - TestEndToEndApproveTierGitHubCommentBack   (US1+US2 / SC-001 / SC-007)
//   - TestRejectNeverDispatches                  (US5 #1)
//   - TestHumanOnlyMarkDone                      (US5 #2)
//   - TestAutoTierExecutesWithoutGate            (US4 #1)
//   - TestNotifyTierExecutesThenSurfaces         (US4 #2)
//   - TestFloorEnforcedAtDB                      (D5c / SC-003)
//
// The "approve Server Action" (operator approve/reject/mark-done) is simulated
// by direct SQL — UPDATE pending_actions + INSERT pending_action_outcomes +
// pg_notify — mirroring what dashboard/app/.../outbox/actions.ts does at
// runtime. This keeps the test in the Go-side-only convention
// (feedback_test_scope_go_only memory).
//
// Run with: go test -tags=integration ./internal/actionbroker/...
package actionbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// httptest GitHub stand-in
// ---------------------------------------------------------------------------

// integTestGitHub is a test-scoped httptest.Server that acts as the GitHub
// REST API. Each call to PostComment is recorded atomically; the URL returned
// uses the server's base URL so the dispatcher gets a stable value to write
// to structured_outcome.
type integTestGitHub struct {
	calls  atomic.Int64
	server *httptest.Server
	client *PostCommentClient
}

// newIntegTestGitHub starts an httptest.Server that returns 201 on every POST
// and registers a t.Cleanup that shuts it down.
func newIntegTestGitHub(t *testing.T) *integTestGitHub {
	t.Helper()
	g := &integTestGitHub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// Return a minimal GitHub comment response body.
		fmt.Fprintf(w, `{"html_url":"http://localhost/comment-%d"}`, g.calls.Load())
	})

	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)

	g.client = &PostCommentClient{
		HTTPClient: g.server.Client(),
		BaseURL:    g.server.URL,
	}
	return g
}

// ---------------------------------------------------------------------------
// Integration fixture
// ---------------------------------------------------------------------------

// integrationFixture bundles the test-scoped resources shared by all T011
// tests: a migrated Postgres pool, a seeded FK chain, a fake vault, and the
// httptest GitHub stand-in. Each test gets a clean fixture (pending_actions
// and pending_action_outcomes are truncated on setup and cleanup).
type integrationFixture struct {
	pool            *pgxpool.Pool
	queries         *store.Queries
	agentInstanceID pgtype.UUID
	vault           *integFakeVault
	github          *integTestGitHub
}

// integFakeVault implements VaultFetcher. Always succeeds with a fixed PAT.
type integFakeVault struct {
	pat string
	err error // if non-nil, Fetch returns this error
}

func (v *integFakeVault) Fetch(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
	if v.err != nil {
		return nil, v.err
	}
	return map[string]vault.SecretValue{
		"GITHUB_PAT": vault.New([]byte(v.pat)),
	}, nil
}

// setupIntegration boots or reuses the shared testcontainer Postgres and seeds
// the minimum FK chain. Truncates M11 tables before and after each test.
func setupIntegration(t *testing.T) integrationFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()

	// Seed minimal FK chain: company → department → agent → ticket → agent_instance.
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('integ-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (company_id, slug, name, workspace_path)
		VALUES ($1, 'integ', 'Integ', '/tmp/integ')
		RETURNING id`, companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("seed department: %v", err)
	}

	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
		VALUES ($1, 'integ-agent', '[]'::jsonb, 'md', 'claude-x', 'active')
		RETURNING id`, deptID,
	).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (department_id, objective, column_slug)
		VALUES ($1, 'integ test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	var instanceID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		VALUES ($1, 'integ-agent', $2, 'running')
		RETURNING id`, deptID, ticketID,
	).Scan(&instanceID); err != nil {
		t.Fatalf("seed agent_instances: %v", err)
	}

	// Truncate M11 tables after the test so the next test starts clean.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"TRUNCATE work.pending_action_outcomes, work.pending_actions CASCADE")
	})

	return integrationFixture{
		pool:            pool,
		queries:         store.New(pool),
		agentInstanceID: instanceID,
		vault:           &integFakeVault{pat: "integ-test-pat"},
		github:          newIntegTestGitHub(t),
	}
}

// newWorker constructs an actionbroker.Worker from the fixture.
func (fx integrationFixture) newWorker(t *testing.T) *Worker {
	t.Helper()
	w, err := New(Deps{
		Pool:    fx.pool,
		Queries: fx.queries,
		Vault:   fx.vault,
		GitHub:  fx.github.client,
		PATPath: "actions/GITHUB_PAT",
		Now:     func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New worker: %v", err)
	}
	return w
}

// insertPendingActionRaw inserts a pending_actions row directly via SQL,
// bypassing the verb handler. Used to simulate what the verb writes and to
// test rows that the floor CHECK would otherwise block (non-floor types).
func (fx integrationFixture) insertPendingActionRaw(
	t *testing.T,
	actionType, tier, status string,
	target []byte,
) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(), `
		INSERT INTO pending_actions
		    (action_type, target, rendered_payload, agent_instance_id, tier, tier_reason, status)
		VALUES ($1, $2::jsonb, 'integ test payload', $3, $4, 'integ-test-reason', $5)
		RETURNING id`,
		actionType, string(target), fx.agentInstanceID, tier, status,
	).Scan(&id); err != nil {
		t.Fatalf("insertPendingActionRaw(%s, %s, %s): %v", actionType, tier, status, err)
	}
	return id
}

// simulateVerbRequestExternalAction writes the same rows the
// realRequestExternalActionHandler would write: one pending_actions row plus
// one pending_action_outcomes row with outcome='requested'. Returns the new
// pending_actions.id.
//
// This simulates the agent calling request_external_action without spinning
// up the full garrisonmutate MCP server, keeping the test in the actionbroker
// package and avoiding a circular import.
func (fx integrationFixture) simulateVerbRequestExternalAction(
	t *testing.T,
	actionType string,
	target []byte,
	payload string,
) pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	tier, tierReason := Classify(actionType)

	row, err := fx.queries.InsertPendingAction(ctx, store.InsertPendingActionParams{
		ActionType:      actionType,
		Target:          target,
		RenderedPayload: payload,
		AgentInstanceID: fx.agentInstanceID,
		TicketID:        pgtype.UUID{Valid: false},
		Tier:            string(tier),
		TierReason:      tierReason,
	})
	if err != nil {
		t.Fatalf("simulateVerbRequestExternalAction InsertPendingAction: %v", err)
	}

	if err := fx.queries.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   row.ID,
		AgentInstanceID:   fx.agentInstanceID,
		Outcome:           "requested",
		Detail:            "",
		StructuredOutcome: nil,
	}); err != nil {
		t.Fatalf("simulateVerbRequestExternalAction InsertPendingActionOutcome: %v", err)
	}

	return row.ID
}

// simulateApproveServerAction simulates the dashboard approve Server Action:
//   - UPDATE pending_actions SET status='approved', approved_by=<operator>
//   - INSERT pending_action_outcomes with outcome='approved'
//   - pg_notify the dispatch channel with the action ID
func (fx integrationFixture) simulateApproveServerAction(t *testing.T, actionID pgtype.UUID, approvedBy string) {
	t.Helper()
	ctx := context.Background()

	if _, err := fx.pool.Exec(ctx, `
		UPDATE pending_actions SET status='approved', approved_by=$1 WHERE id=$2`,
		approvedBy, actionID,
	); err != nil {
		t.Fatalf("simulateApproveServerAction UPDATE: %v", err)
	}

	if err := fx.queries.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   actionID,
		AgentInstanceID:   fx.agentInstanceID,
		Outcome:           "approved",
		Detail:            approvedBy,
		StructuredOutcome: nil,
	}); err != nil {
		t.Fatalf("simulateApproveServerAction InsertPendingActionOutcome: %v", err)
	}

	// Emit the dispatch notify — the approve Server Action does this post-commit.
	if _, err := fx.pool.Exec(ctx,
		"SELECT pg_notify($1, $2)",
		Channel, uuidStr(actionID),
	); err != nil {
		t.Fatalf("simulateApproveServerAction pg_notify: %v", err)
	}
}

// simulateRejectServerAction simulates the dashboard reject Server Action.
func (fx integrationFixture) simulateRejectServerAction(t *testing.T, actionID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()

	if _, err := fx.pool.Exec(ctx, `
		UPDATE pending_actions SET status='rejected' WHERE id=$1`, actionID,
	); err != nil {
		t.Fatalf("simulateRejectServerAction UPDATE: %v", err)
	}

	if err := fx.queries.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   actionID,
		AgentInstanceID:   fx.agentInstanceID,
		Outcome:           "rejected",
		Detail:            "operator rejected",
		StructuredOutcome: nil,
	}); err != nil {
		t.Fatalf("simulateRejectServerAction InsertPendingActionOutcome: %v", err)
	}
}

// simulateMarkDoneServerAction simulates the dashboard mark-as-done Server
// Action for human_only rows.
func (fx integrationFixture) simulateMarkDoneServerAction(t *testing.T, actionID pgtype.UUID, note string) {
	t.Helper()
	ctx := context.Background()

	if _, err := fx.pool.Exec(ctx, `
		UPDATE pending_actions SET status='done' WHERE id=$1`, actionID,
	); err != nil {
		t.Fatalf("simulateMarkDoneServerAction UPDATE: %v", err)
	}

	if err := fx.queries.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   actionID,
		AgentInstanceID:   fx.agentInstanceID,
		Outcome:           "done",
		Detail:            note,
		StructuredOutcome: nil,
	}); err != nil {
		t.Fatalf("simulateMarkDoneServerAction InsertPendingActionOutcome: %v", err)
	}
}

// Helper read functions.

// readStatus reads the current status of a pending_actions row.
func (fx integrationFixture) readStatus(t *testing.T, actionID pgtype.UUID) string {
	t.Helper()
	var status string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&status); err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	return status
}

// readApprovedBy reads the approved_by column of a pending_actions row.
func (fx integrationFixture) readApprovedBy(t *testing.T, actionID pgtype.UUID) string {
	t.Helper()
	var approvedBy pgtype.Text
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT approved_by FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&approvedBy); err != nil {
		t.Fatalf("readApprovedBy: %v", err)
	}
	if !approvedBy.Valid {
		return ""
	}
	return approvedBy.String
}

// readTier reads the tier column of a pending_actions row.
func (fx integrationFixture) readTier(t *testing.T, actionID pgtype.UUID) string {
	t.Helper()
	var tier string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT tier FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&tier); err != nil {
		t.Fatalf("readTier: %v", err)
	}
	return tier
}

// readAgentInstanceID reads the agent_instance_id of a pending_actions row.
func (fx integrationFixture) readAgentInstanceID(t *testing.T, actionID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var agentInstID pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT agent_instance_id FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&agentInstID); err != nil {
		t.Fatalf("readAgentInstanceID: %v", err)
	}
	return agentInstID
}

// readRenderedPayload reads the rendered_payload column.
func (fx integrationFixture) readRenderedPayload(t *testing.T, actionID pgtype.UUID) string {
	t.Helper()
	var payload string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT rendered_payload FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&payload); err != nil {
		t.Fatalf("readRenderedPayload: %v", err)
	}
	return payload
}

// hasOutcome returns true if at least one outcome row with the given outcome
// value exists for the given pending_actions.id.
func (fx integrationFixture) hasOutcome(t *testing.T, actionID pgtype.UUID, outcome string) bool {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_action_outcomes
		  WHERE pending_action_id = $1 AND outcome = $2`,
		actionID, outcome,
	).Scan(&n); err != nil {
		t.Fatalf("hasOutcome: %v", err)
	}
	return n > 0
}

// outcomeCount returns the total number of outcome rows for the given action.
func (fx integrationFixture) outcomeCount(t *testing.T, actionID pgtype.UUID) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_action_outcomes WHERE pending_action_id = $1`,
		actionID,
	).Scan(&n); err != nil {
		t.Fatalf("outcomeCount: %v", err)
	}
	return n
}

// pendingApproveCount returns the number of approve-tier pending rows.
func (fx integrationFixture) pendingApproveCount(t *testing.T) int {
	t.Helper()
	rows, err := fx.queries.ListPendingApproveActions(context.Background())
	if err != nil {
		t.Fatalf("ListPendingApproveActions: %v", err)
	}
	return len(rows)
}

// standardTarget is the canonical github_issue_comment JSONB target.
var integGitHubTarget = []byte(`{"owner":"integ-org","repo":"integ-repo","issue_number":99}`)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEndToEndApproveTierGitHubCommentBack — US1+US2 end-to-end / SC-001 / SC-007.
//
// Full path against a real Postgres + httptest GitHub stand-in:
//  1. Agent calls request_external_action (simulated) → one pending_actions row,
//     tier 'approve', status 'pending'.
//  2. ListPendingApproveActions returns it.
//  3. Simulate the approve Server Action (UPDATE→'approved', INSERT outcome
//     'approved', emit pg_notify).
//  4. Dispatcher claims row via FOR UPDATE SKIP LOCKED, posts comment via
//     stand-in, records 'executed' outcome.
//  5. ListPendingActionOutcomes reconstructs: agent_instance_id, rendered_payload,
//     tier 'approve', approved_by, 'executed' outcome — all immutable (SC-007).
func TestEndToEndApproveTierGitHubCommentBack(t *testing.T) {
	fx := setupIntegration(t)
	ctx := context.Background()

	// Step 1: agent calls request_external_action → pending row.
	const payload = "Hello from the agent. This is the reply."
	actionID := fx.simulateVerbRequestExternalAction(t,
		"github_issue_comment", integGitHubTarget, payload)

	// Verify: one pending_actions row, tier 'approve', status 'pending'.
	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Fatalf("step 1: status = %q; want 'pending'", got)
	}
	if got := fx.readTier(t, actionID); got != "approve" {
		t.Fatalf("step 1: tier = %q; want 'approve' (permanent-Approve floor)", got)
	}
	// The pending row is anchored to the calling agent.
	if got := fx.readAgentInstanceID(t, actionID); got.Bytes != fx.agentInstanceID.Bytes {
		t.Fatalf("step 1: agent_instance_id mismatch: got %v want %v", got, fx.agentInstanceID)
	}
	// The 'requested' outcome row was written.
	if !fx.hasOutcome(t, actionID, "requested") {
		t.Fatal("step 1: expected 'requested' outcome row")
	}

	// Step 2: ListPendingApproveActions returns the new row.
	pendingRows, err := fx.queries.ListPendingApproveActions(ctx)
	if err != nil {
		t.Fatalf("step 2: ListPendingApproveActions: %v", err)
	}
	if len(pendingRows) != 1 {
		t.Fatalf("step 2: ListPendingApproveActions returned %d rows; want 1", len(pendingRows))
	}
	if pendingRows[0].ID.Bytes != actionID.Bytes {
		t.Fatalf("step 2: returned row ID mismatch")
	}

	// Step 3: simulate operator approve Server Action.
	const operator = "operator@example.com"
	fx.simulateApproveServerAction(t, actionID, operator)

	if got := fx.readStatus(t, actionID); got != "approved" {
		t.Fatalf("step 3: status = %q; want 'approved' after operator click", got)
	}
	// The 'approved' outcome row was written.
	if !fx.hasOutcome(t, actionID, "approved") {
		t.Fatal("step 3: expected 'approved' outcome row")
	}

	// Step 4: dispatcher claims the approved row and posts the comment.
	w := fx.newWorker(t)
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("step 4: Handle: %v", err)
	}

	// Exactly one PostComment call on the httptest stand-in.
	if got := fx.github.calls.Load(); got != 1 {
		t.Errorf("step 4: PostComment calls = %d; want 1", got)
	}

	// Row transitioned to 'executed'.
	if got := fx.readStatus(t, actionID); got != "executed" {
		t.Errorf("step 4: status = %q; want 'executed'", got)
	}

	// Step 5: reconstruct the audit via ListPendingActionOutcomes (SC-007).
	// Expected outcomes in order: 'requested', 'approved', 'executed'.
	outcomes, err := fx.queries.ListPendingActionOutcomes(ctx, actionID)
	if err != nil {
		t.Fatalf("step 5: ListPendingActionOutcomes: %v", err)
	}
	// We need at least: requested, approved, executed.
	wantOutcomes := []string{"requested", "approved", "executed"}
	if len(outcomes) < len(wantOutcomes) {
		t.Fatalf("step 5: got %d outcome rows; want at least %d", len(outcomes), len(wantOutcomes))
	}
	outcomeNames := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		outcomeNames = append(outcomeNames, o.Outcome)
	}
	for _, want := range wantOutcomes {
		found := false
		for _, got := range outcomeNames {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("step 5: outcome %q not found in %v", want, outcomeNames)
		}
	}

	// SC-007: every outcome row must be anchored to the originating agent.
	for _, o := range outcomes {
		if o.AgentInstanceID.Bytes != fx.agentInstanceID.Bytes {
			t.Errorf("step 5: outcome %q agent_instance_id mismatch: got %v want %v",
				o.Outcome, o.AgentInstanceID, fx.agentInstanceID)
		}
	}

	// SC-007: the rendered_payload is immutable and reconstructible.
	if got := fx.readRenderedPayload(t, actionID); got != payload {
		t.Errorf("step 5: rendered_payload = %q; want %q", got, payload)
	}

	// SC-007: approved_by is recorded.
	if got := fx.readApprovedBy(t, actionID); got != operator {
		t.Errorf("step 5: approved_by = %q; want %q", got, operator)
	}
}

// TestRejectNeverDispatches — US5 #1.
//
// An approve-tier row that the operator rejects must never be dispatched.
// After the reject Server Action transitions the row to 'rejected', the
// dispatcher claims nothing and PostComment is never called.
func TestRejectNeverDispatches(t *testing.T) {
	fx := setupIntegration(t)
	ctx := context.Background()

	actionID := fx.simulateVerbRequestExternalAction(t,
		"github_issue_comment", integGitHubTarget, "comment that will be rejected")

	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Fatalf("pre-reject: status = %q; want 'pending'", got)
	}

	// Simulate reject Server Action.
	fx.simulateRejectServerAction(t, actionID)

	if got := fx.readStatus(t, actionID); got != "rejected" {
		t.Fatalf("post-reject: status = %q; want 'rejected'", got)
	}
	// A 'rejected' outcome row was written.
	if !fx.hasOutcome(t, actionID, "rejected") {
		t.Fatal("expected 'rejected' outcome row")
	}

	// Dispatcher runs — must claim nothing because the row is in a terminal state.
	w := fx.newWorker(t)
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Handle after reject: %v", err)
	}

	// PostComment must NOT have been called.
	if got := fx.github.calls.Load(); got != 0 {
		t.Errorf("PostComment calls = %d; want 0 — rejected row must not be dispatched", got)
	}

	// Status must remain 'rejected' (terminal — dispatcher did not change it).
	if got := fx.readStatus(t, actionID); got != "rejected" {
		t.Errorf("final status = %q; want 'rejected'", got)
	}

	// ListPendingApproveActions must now return 0 rows (row is no longer pending).
	if got := fx.pendingApproveCount(t); got != 0 {
		t.Errorf("ListPendingApproveActions count = %d; want 0 after reject", got)
	}
}

// TestHumanOnlyMarkDone — US5 #2.
//
// A human_only-tier pending action is never dispatched by the supervisor.
// The agent prepares the payload; the operator marks it done; a 'done'
// outcome (with an optional note) is appended to the immutable history.
// PostComment is never called.
func TestHumanOnlyMarkDone(t *testing.T) {
	fx := setupIntegration(t)
	ctx := context.Background()

	// Insert a human_only row directly — use a fictional non-floor action type
	// so the pending_actions_floor_is_approve CHECK doesn't block it.
	const humanOnlyType = "test_human_only_action"
	actionID := fx.insertPendingActionRaw(t, humanOnlyType, "human_only", "pending", []byte(`{}`))

	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Fatalf("pre-mark-done: status = %q; want 'pending'", got)
	}

	// Dispatcher runs — must not claim the human_only row (filtered by the
	// ClaimDispatchablePendingAction predicate: tier <> 'human_only').
	w := fx.newWorker(t)
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Handle (before mark-done): %v", err)
	}
	if got := fx.github.calls.Load(); got != 0 {
		t.Errorf("PostComment calls = %d before mark-done; want 0", got)
	}
	// Row must still be pending — dispatcher did not touch it.
	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Errorf("status after dispatcher run = %q; want 'pending'", got)
	}

	// Simulate operator mark-as-done Server Action.
	const note = "I replied manually on the GitHub issue."
	fx.simulateMarkDoneServerAction(t, actionID, note)

	if got := fx.readStatus(t, actionID); got != "done" {
		t.Errorf("post-mark-done: status = %q; want 'done'", got)
	}
	// A 'done' outcome row with the note was written.
	if !fx.hasOutcome(t, actionID, "done") {
		t.Error("expected 'done' outcome row")
	}

	// Read the detail to confirm the note was recorded.
	var detail string
	if err := fx.pool.QueryRow(ctx,
		`SELECT detail FROM pending_action_outcomes
		  WHERE pending_action_id = $1 AND outcome = 'done'
		  LIMIT 1`, actionID,
	).Scan(&detail); err != nil {
		t.Fatalf("read done-outcome detail: %v", err)
	}
	if detail != note {
		t.Errorf("done-outcome detail = %q; want %q", detail, note)
	}

	// Dispatcher runs again after mark-done — still must not call PostComment
	// (the row is in a terminal 'done' state, not in the claimable set).
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Handle (after mark-done): %v", err)
	}
	if got := fx.github.calls.Load(); got != 0 {
		t.Errorf("PostComment calls = %d after mark-done; want 0", got)
	}

	// Verify the outcome history via ListPendingActionOutcomes.
	outcomes, err := fx.queries.ListPendingActionOutcomes(ctx, actionID)
	if err != nil {
		t.Fatalf("ListPendingActionOutcomes: %v", err)
	}
	// Expect at least the 'done' outcome row.
	foundDone := false
	for _, o := range outcomes {
		if o.Outcome == "done" {
			foundDone = true
		}
	}
	if !foundDone {
		t.Error("ListPendingActionOutcomes: 'done' outcome not found")
	}
}

// TestAutoTierExecutesWithoutGate — US4 #1.
//
// An auto-tier pending action (using a non-floor action type) is dispatched by
// the supervisor immediately from status='pending', without any operator
// approval. PostComment is called once and the row reaches 'executed'.
func TestAutoTierExecutesWithoutGate(t *testing.T) {
	// Temporarily register a hypothetical auto-tier action type so the policy
	// table returns TierAuto for it. This avoids adding a real auto-tier type
	// to the production policy map.
	const autoType = "test_integ_auto_action"
	cleanup := RegisterTestPolicyEntry(autoType, TierAuto)
	defer cleanup()

	fx := setupIntegration(t)
	ctx := context.Background()

	// Insert the auto-tier pending row directly (simulating what the verb would write).
	autoTarget := []byte(`{"kind":"log","ref":"T011"}`)
	actionID := fx.insertPendingActionRaw(t, autoType, "auto", "pending", autoTarget)

	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Fatalf("pre-dispatch: status = %q; want 'pending'", got)
	}

	// Dispatcher claims and executes the auto-tier row — no approval gate needed.
	w := fx.newWorker(t)
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// PostComment called exactly once (no approval gate required for auto-tier).
	if got := fx.github.calls.Load(); got != 1 {
		t.Errorf("PostComment calls = %d; want 1 (auto-tier dispatches from 'pending')", got)
	}

	// Row transitioned to 'executed'.
	if got := fx.readStatus(t, actionID); got != "executed" {
		t.Errorf("status = %q; want 'executed'", got)
	}

	// 'executed' outcome row must be present.
	if !fx.hasOutcome(t, actionID, "executed") {
		t.Error("expected 'executed' outcome row for auto-tier action")
	}
}

// TestNotifyTierExecutesThenSurfaces — US4 #2.
//
// A notify-tier pending action is dispatched (PostComment called once) and then
// both an 'executed' and a 'notified' outcome row are written. The Outbox
// can distinguish post-hoc feed items from pending approvals via the 'notified'
// outcome. ListPendingActionOutcomes returns both.
func TestNotifyTierExecutesThenSurfaces(t *testing.T) {
	// Temporarily register a hypothetical notify-tier action type.
	const notifyType = "test_integ_notify_action"
	cleanup := RegisterTestPolicyEntry(notifyType, TierNotify)
	defer cleanup()

	fx := setupIntegration(t)
	ctx := context.Background()

	notifyTarget := []byte(`{"kind":"feed","ref":"T011"}`)
	actionID := fx.insertPendingActionRaw(t, notifyType, "notify", "pending", notifyTarget)

	if got := fx.readStatus(t, actionID); got != "pending" {
		t.Fatalf("pre-dispatch: status = %q; want 'pending'", got)
	}

	// Dispatcher executes the notify-tier row.
	w := fx.newWorker(t)
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// PostComment called exactly once.
	if got := fx.github.calls.Load(); got != 1 {
		t.Errorf("PostComment calls = %d; want 1", got)
	}

	// Row transitioned to 'executed'.
	if got := fx.readStatus(t, actionID); got != "executed" {
		t.Errorf("status = %q; want 'executed'", got)
	}

	// Both 'executed' and 'notified' outcome rows must be present (FR-028/D17).
	if !fx.hasOutcome(t, actionID, "executed") {
		t.Error("expected 'executed' outcome row")
	}
	if !fx.hasOutcome(t, actionID, "notified") {
		t.Error("expected 'notified' outcome row — notify-tier surfaces post-hoc (FR-028)")
	}

	// ListPendingActionOutcomes returns both outcomes.
	outcomes, err := fx.queries.ListPendingActionOutcomes(ctx, actionID)
	if err != nil {
		t.Fatalf("ListPendingActionOutcomes: %v", err)
	}
	// Expect at least 'executed' and 'notified'.
	foundExec, foundNotif := false, false
	for _, o := range outcomes {
		switch o.Outcome {
		case "executed":
			foundExec = true
		case "notified":
			foundNotif = true
		}
	}
	if !foundExec {
		t.Error("ListPendingActionOutcomes: 'executed' not found")
	}
	if !foundNotif {
		t.Error("ListPendingActionOutcomes: 'notified' not found")
	}

	// Total outcomes from ListPendingActionOutcomes: at least 2 (executed + notified).
	if len(outcomes) < 2 {
		t.Errorf("outcome count = %d; want at least 2 (executed + notified)", len(outcomes))
	}
}

// TestFloorEnforcedAtDB — D5c / SC-003.
//
// Attempt to INSERT a pending_actions row with action_type='github_issue_comment'
// and tier='auto' directly via SQL. The pending_actions_floor_is_approve CHECK
// constraint must reject the INSERT with a violation error.
func TestFloorEnforcedAtDB(t *testing.T) {
	fx := setupIntegration(t)
	ctx := context.Background()

	// Attempt a direct INSERT that violates the floor CHECK.
	_, err := fx.pool.Exec(ctx, `
		INSERT INTO pending_actions
		    (action_type, target, rendered_payload, agent_instance_id, tier, tier_reason, status)
		VALUES ('github_issue_comment', $1::jsonb, 'floor test payload', $2, 'auto',
		        'attempting to lower the floor', 'pending')`,
		string(integGitHubTarget), fx.agentInstanceID,
	)
	if err == nil {
		t.Fatal("expected a CHECK constraint violation but INSERT succeeded; " +
			"the pending_actions_floor_is_approve CHECK did not fire (SC-003, D5c)")
	}

	// The error should mention the constraint name.
	errStr := err.Error()
	// Postgres reports the constraint name in the error message.
	if !containsAny(errStr, "pending_actions_floor_is_approve", "check", "CHECK", "constraint") {
		t.Errorf("error does not appear to be a CHECK constraint violation: %v", err)
	}
}

// containsAny reports whether s contains any of the given substrings.
// Case-insensitive for flexibility across Postgres error message styles.
func containsAny(s string, substrings ...string) bool {
	sl := toLower(s)
	for _, sub := range substrings {
		if contains(sl, toLower(sub)) {
			return true
		}
	}
	return false
}

// contains reports whether s contains substr (both already lowercased).
func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && indexString(s, substr) >= 0)
}

func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

// Compile-time assertion: integFakeVault satisfies VaultFetcher.
var _ VaultFetcher = (*integFakeVault)(nil)

// Compile-time assertion: json.RawMessage is used so encoding/json is live.
var _ = json.RawMessage{}

// Compile-time assertion: fmt is used.
var _ = fmt.Sprintf
