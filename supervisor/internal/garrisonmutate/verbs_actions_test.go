//go:build integration

// T008 — Unit tests for the request_external_action verb handler.
//
// These tests exercise the six-step handler logic in verbs_actions.go
// against a real Postgres testcontainer (via setupAgentCaller / testdb).
// They cover the five completion-condition scenarios named in tasks.md T008:
//
//   - TestRequestExternalActionWritesExactlyOnePendingRow (SC-001 / US1 #1)
//   - TestRequestExternalActionReturnsQueuedResult        (FR-004 / US1 #2)
//   - TestRequestExternalActionIgnoresAgentSuppliedTier   (FR-005 / US1 #4 / US3 #3)
//   - TestRequestExternalActionRejectsChatCaller          (D7 / Q-D)
//   - TestRequestExternalActionAutoTierEmitsDispatchNotify (D18)
//
// The tests rely on the agentCallerFixture helper (verbs_tickets_m8_test.go)
// which seeds an agent_instances row — required because pending_actions has a
// NOT NULL FK on agent_instance_id. All tests use the production
// realRequestExternalActionHandler (injected at package init by verbs_actions.go).

package garrisonmutate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/actionbroker"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// validGitHubCommentArgs returns a JSON-encoded request_external_action
// args payload for a github_issue_comment action on a known test repo.
// github_issue_comment is on the permanent-Approve floor (TierApprove).
func validGitHubCommentArgs() json.RawMessage {
	return json.RawMessage(`{
		"action_type": "github_issue_comment",
		"target": {"owner":"test-org","repo":"test-repo","issue_number":1},
		"payload": "This is a test comment."
	}`)
}

// pendingActionCount returns the number of pending_actions rows for the
// given agent_instance_id — used to assert "exactly one row was written".
func pendingActionCount(t *testing.T, fx agentCallerFixture) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_actions WHERE agent_instance_id = $1`,
		fx.agentInstanceID).Scan(&n); err != nil {
		t.Fatalf("pending_action count: %v", err)
	}
	return n
}

// pendingActionOutcomeCount returns the number of pending_action_outcomes
// rows for the most-recently created pending_actions row owned by the
// fixture's agent instance. Returns 0 when no pending_actions row exists.
func pendingActionOutcomeCount(t *testing.T, fx agentCallerFixture) int {
	t.Helper()
	var n int
	err := fx.pool.QueryRow(context.Background(), `
		SELECT COUNT(pao.id)
		  FROM pending_action_outcomes pao
		  JOIN pending_actions pa ON pao.pending_action_id = pa.id
		 WHERE pa.agent_instance_id = $1`,
		fx.agentInstanceID).Scan(&n)
	if err != nil {
		t.Fatalf("pending_action_outcome count: %v", err)
	}
	return n
}

// latestPendingActionTier returns the tier value of the most-recently
// created pending_actions row owned by the fixture's agent instance.
// Fails the test if no row exists.
func latestPendingActionTier(t *testing.T, fx agentCallerFixture) string {
	t.Helper()
	var tier string
	err := fx.pool.QueryRow(context.Background(), `
		SELECT tier FROM pending_actions
		 WHERE agent_instance_id = $1
		 ORDER BY created_at DESC LIMIT 1`,
		fx.agentInstanceID).Scan(&tier)
	if err != nil {
		t.Fatalf("latestPendingActionTier: %v", err)
	}
	return tier
}

// actionBrokerAuditCount returns the number of chat_mutation_audit rows
// for the given verb tied to the fixture's agent instance.
func actionBrokerAuditCount(t *testing.T, fx agentCallerFixture, verb string) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM chat_mutation_audit
		  WHERE agent_instance_id = $1 AND verb = $2`,
		fx.agentInstanceID, verb).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

// TestRequestExternalActionWritesExactlyOnePendingRow — SC-001 / US1 #1.
//
// An agent caller with a valid github_issue_comment action results in:
//   - exactly one work.pending_actions row anchored on the calling agent_instance_id,
//     status 'pending', tier 'approve' (from the policy table, not from any
//     agent-supplied field),
//   - exactly one work.pending_action_outcomes row with outcome 'requested',
//   - exactly one chat_mutation_audit row for verb 'request_external_action'.
//
// No external HTTP call is made — the verb only writes rows; the dispatcher
// is the door (FR-003, FR-007).
func TestRequestExternalActionWritesExactlyOnePendingRow(t *testing.T) {
	fx := setupAgentCaller(t)

	r, err := realRequestExternalActionHandler(
		context.Background(), fx.deps, validGitHubCommentArgs(),
	)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success=true; got %+v", r)
	}

	// Exactly one pending_actions row for this agent.
	if got := pendingActionCount(t, fx); got != 1 {
		t.Errorf("pending_actions rows = %d; want 1", got)
	}

	// Row has the correct agent_instance_id, status, and tier.
	var (
		storedAgentID pgtype.UUID
		storedStatus  string
		storedTier    string
	)
	if err := fx.pool.QueryRow(context.Background(), `
		SELECT agent_instance_id, status, tier
		  FROM pending_actions
		 WHERE agent_instance_id = $1
		 ORDER BY created_at DESC LIMIT 1`,
		fx.agentInstanceID,
	).Scan(&storedAgentID, &storedStatus, &storedTier); err != nil {
		t.Fatalf("read pending_actions row: %v", err)
	}
	if storedAgentID.Bytes != fx.agentInstanceID.Bytes {
		t.Errorf("agent_instance_id mismatch: got %v want %v", storedAgentID, fx.agentInstanceID)
	}
	if storedStatus != "pending" {
		t.Errorf("status = %q; want 'pending'", storedStatus)
	}
	if storedTier != "approve" {
		t.Errorf("tier = %q; want 'approve' (policy table, not agent-supplied)", storedTier)
	}

	// Exactly one outcome row with outcome 'requested'.
	if got := pendingActionOutcomeCount(t, fx); got != 1 {
		t.Errorf("pending_action_outcomes rows = %d; want 1", got)
	}
	var outcomeVal string
	if err := fx.pool.QueryRow(context.Background(), `
		SELECT pao.outcome
		  FROM pending_action_outcomes pao
		  JOIN pending_actions pa ON pao.pending_action_id = pa.id
		 WHERE pa.agent_instance_id = $1
		 ORDER BY pao.created_at DESC LIMIT 1`,
		fx.agentInstanceID,
	).Scan(&outcomeVal); err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if outcomeVal != "requested" {
		t.Errorf("outcome = %q; want 'requested'", outcomeVal)
	}

	// Exactly one chat_mutation_audit row for verb 'request_external_action'.
	if got := actionBrokerAuditCount(t, fx, verbRequestExternalAction); got != 1 {
		t.Errorf("audit rows = %d; want 1", got)
	}
}

// TestRequestExternalActionReturnsQueuedResult — FR-004 / US1 #2.
//
// The handler returns a Result where:
//   - Success is true,
//   - AffectedResourceURL is "/admin/outbox",
//   - Message says "queued at the approve tier",
//   - Message does NOT imply the action was performed.
func TestRequestExternalActionReturnsQueuedResult(t *testing.T) {
	fx := setupAgentCaller(t)

	r, err := realRequestExternalActionHandler(
		context.Background(), fx.deps, validGitHubCommentArgs(),
	)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success=true; got %+v", r)
	}
	if r.AffectedResourceURL != "/admin/outbox" {
		t.Errorf("AffectedResourceURL = %q; want %q", r.AffectedResourceURL, "/admin/outbox")
	}

	// Message must say "queued at the approve tier" (FR-004).
	if !strings.Contains(r.Message, "queued at the approve tier") {
		t.Errorf("Message = %q; want it to contain 'queued at the approve tier'", r.Message)
	}

	// Message must NOT imply the action was already performed.
	for _, forbiddenPhrase := range []string{"posted", "sent", "published", "commented", "executed"} {
		if strings.Contains(strings.ToLower(r.Message), forbiddenPhrase) {
			t.Errorf("Message %q implies action performed (contains %q); FR-004 requires queued-only language",
				r.Message, forbiddenPhrase)
		}
	}
}

// TestRequestExternalActionIgnoresAgentSuppliedTier — FR-005 / US1 #4 / US3 #3.
//
// When the agent's JSON args carry a stray "tier":"auto" key (not part of
// RequestExternalActionArgs), the key is silently dropped on unmarshal.
// The stored tier is still "approve" because Classify uses the policy table,
// not any agent-supplied value.
func TestRequestExternalActionIgnoresAgentSuppliedTier(t *testing.T) {
	fx := setupAgentCaller(t)

	// Args include a stray "tier" key the struct does not declare.
	strayTierArgs := json.RawMessage(`{
		"action_type": "github_issue_comment",
		"target": {"owner":"test-org","repo":"test-repo","issue_number":2},
		"payload": "Another test comment.",
		"tier": "auto"
	}`)

	r, err := realRequestExternalActionHandler(
		context.Background(), fx.deps, strayTierArgs,
	)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success=true; got %+v", r)
	}

	// The stored tier must be 'approve' — the stray "tier":"auto" was ignored.
	if got := latestPendingActionTier(t, fx); got != "approve" {
		t.Errorf("stored tier = %q; want 'approve' (agent-supplied tier must be ignored)", got)
	}
}

// TestRequestExternalActionRejectsChatCaller — D7 / Q-D.
//
// When AgentInstanceID is not set (zero value, as in chat-mode), the handler
// returns a validation_failed Result with a message containing "callable only
// by agents". The DB is never reached (deps.Pool is nil — the validation path
// exits before any pool call).
func TestRequestExternalActionRejectsChatCaller(t *testing.T) {
	// Chat-mode deps: no AgentInstanceID set.
	chatDeps := validationDeps()

	r, err := realRequestExternalActionHandler(
		context.Background(), chatDeps, validGitHubCommentArgs(),
	)
	if err != nil {
		t.Fatalf("handler returned an error; want nil error + typed Result: %v", err)
	}
	if r.Success {
		t.Fatal("expected Success=false for non-agent caller")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, "callable only by agents") {
		t.Errorf("Message = %q; want it to contain 'callable only by agents'", r.Message)
	}
}

// TestRequestExternalActionAutoTierEmitsDispatchNotify — D18.
//
// For an auto-tier action type the verb emits a work.action.dispatch_requested
// pg_notify post-commit so the dispatcher reacts immediately. For an
// approve-tier action (github_issue_comment) the notify is NOT emitted —
// the approve Server Action handles that after operator approval.
//
// This test:
//  1. LISTENs on work.action.dispatch_requested before calling the handler.
//  2. Temporarily registers a hypothetical auto-tier action type in the
//     policy table via actionbroker.RegisterTestPolicyEntry (T008 test seam).
//  3. Calls the handler with the auto-tier action type.
//  4. Asserts the notify is received within 3 s (auto-tier path).
//  5. Calls the handler with github_issue_comment (approve-tier).
//  6. Asserts no notify is received within a short window (approve-tier path).
func TestRequestExternalActionAutoTierEmitsDispatchNotify(t *testing.T) {
	fx := setupAgentCaller(t)
	ctx := context.Background()

	// Open a dedicated single connection for LISTEN (pgxpool cannot
	// WaitForNotification; the pool recycles connections).
	listenConn, err := fx.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire listen conn: %v", err)
	}
	defer listenConn.Release()

	if _, err := listenConn.Exec(ctx,
		"LISTEN "+pgx.Identifier{actionBrokerDispatchChannel}.Sanitize(),
	); err != nil {
		t.Fatalf("LISTEN %s: %v", actionBrokerDispatchChannel, err)
	}

	// Register a hypothetical auto-tier action type. The type name is
	// chosen to be clearly non-floor (not github_issue_comment) and unique
	// to this test so it cannot interact with other tests.
	const testAutoType = "test_internal_log_only"
	cleanup := actionbroker.RegisterTestPolicyEntry(testAutoType, actionbroker.TierAuto)
	defer cleanup()

	// Auto-tier action target (no DB FK requirement on target content).
	autoArgs := json.RawMessage(`{
		"action_type": "` + testAutoType + `",
		"target": {"kind":"log","ref":"T008"},
		"payload": "auto-tier test payload"
	}`)

	// Step 3: call the handler — should emit the notify post-commit.
	r, err := realRequestExternalActionHandler(ctx, fx.deps, autoArgs)
	if err != nil {
		t.Fatalf("auto-tier handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success=true for auto-tier; got %+v", r)
	}

	// Step 4: assert the notify is received.
	notifyCtx, cancelNotify := context.WithTimeout(ctx, 3*time.Second)
	defer cancelNotify()
	notif, err := listenConn.Conn().WaitForNotification(notifyCtx)
	if err != nil {
		t.Fatalf("WaitForNotification (auto-tier): expected a notify on %s but got: %v",
			actionBrokerDispatchChannel, err)
	}
	if notif.Channel != actionBrokerDispatchChannel {
		t.Errorf("notify channel = %q; want %q", notif.Channel, actionBrokerDispatchChannel)
	}
	// Payload is the pending_actions.id UUID string (plan D18).
	if notif.Payload == "" {
		t.Error("notify payload is empty; want the pending_actions UUID string")
	}

	// Step 5: call the handler with approve-tier (github_issue_comment).
	// The notify must NOT be emitted — the approve Server Action emits it.
	approveArgs := validGitHubCommentArgs()
	r2, err := realRequestExternalActionHandler(ctx, fx.deps, approveArgs)
	if err != nil {
		t.Fatalf("approve-tier handler error: %v", err)
	}
	if !r2.Success {
		t.Fatalf("expected Success=true for approve-tier; got %+v", r2)
	}

	// Step 6: no notify should arrive within a short window.
	noNotifyCtx, cancelNoNotify := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelNoNotify()
	gotNotify, _ := listenConn.Conn().WaitForNotification(noNotifyCtx)
	if gotNotify != nil {
		t.Errorf("unexpected notify for approve-tier action: %+v", gotNotify)
	}
}
