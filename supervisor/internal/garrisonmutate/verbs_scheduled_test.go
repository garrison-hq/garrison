//go:build integration

package garrisonmutate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// validScheduledTaskArgs builds a create_scheduled_task args payload
// valid against setupIntegration's seed (engineering department,
// engineering.engineer active agent).
func validScheduledTaskArgs(name, expr string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"name":%q,"department_slug":"engineering","role_slug":"engineering.engineer","mode":"ticket","schedule_expr":%q,"objective_template":"Run the standup for {{date}}","acceptance_criteria_template":"Summary posted to the board"}`,
		name, expr,
	))
}

// scheduledTaskCount returns the live scheduled_tasks row count.
func scheduledTaskCount(t *testing.T, fx integrationFixture) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM scheduled_tasks WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("scheduled task count: %v", err)
	}
	return n
}

// auditOutcomeCount counts create_scheduled_task audit rows for the
// fixture's session with the given outcome.
func auditOutcomeCount(t *testing.T, fx integrationFixture, outcome string) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM chat_mutation_audit
		  WHERE chat_session_id = $1 AND verb = 'create_scheduled_task' AND outcome = $2`,
		fx.chatSessionID, outcome).Scan(&n); err != nil {
		t.Fatalf("audit outcome count: %v", err)
	}
	return n
}

// TestCreateScheduledTaskHappyPath: valid args land a scheduled_tasks
// row with the ValidateTask-computed next_fire_at plus a Tier-3
// chat-session-anchored audit row carrying the full args (FR-600,
// FR-602; plan §4 test plan).
func TestCreateScheduledTaskHappyPath(t *testing.T) {
	fx := setupIntegration(t)
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "15m")
	before := time.Now().UTC()

	r, err := realCreateScheduledTaskHandler(context.Background(), fx.deps, validScheduledTaskArgs("standup", "daily@09:00"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}
	if r.AffectedResourceID == "" {
		t.Error("expected scheduled task id in result")
	}
	if !strings.HasPrefix(r.AffectedResourceURL, scheduledTaskURLPrefix) {
		t.Errorf("URL prefix wrong: %q", r.AffectedResourceURL)
	}

	// Row landed with the validated shape + a future-dated computed
	// next_fire_at.
	var (
		name, mode, expr string
		nextFireAt       pgtype.Timestamptz
		deptID           pgtype.UUID
	)
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT name, mode, schedule_expr, next_fire_at, department_id
		   FROM scheduled_tasks WHERE id = $1`, r.AffectedResourceID).
		Scan(&name, &mode, &expr, &nextFireAt, &deptID); err != nil {
		t.Fatalf("select task row: %v", err)
	}
	if name != "standup" || mode != "ticket" || expr != "daily@09:00" {
		t.Errorf("row shape: name=%q mode=%q expr=%q", name, mode, expr)
	}
	if deptID != fx.departmentID {
		t.Errorf("department_id = %v; want %v", deptID, fx.departmentID)
	}
	if !nextFireAt.Valid || !nextFireAt.Time.After(before) {
		t.Errorf("next_fire_at = %v; want valid + future-dated", nextFireAt)
	}

	// Tier-3 audit row anchored on the chat session with full args.
	var (
		outcome      string
		class        int16
		resourceType *string
		argsJSON     []byte
	)
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT outcome, reversibility_class, affected_resource_type, args_jsonb
		   FROM chat_mutation_audit
		  WHERE chat_session_id = $1 AND verb = 'create_scheduled_task'`,
		fx.chatSessionID).Scan(&outcome, &class, &resourceType, &argsJSON); err != nil {
		t.Fatalf("select audit row: %v", err)
	}
	if outcome != "success" || class != 3 {
		t.Errorf("audit outcome=%q class=%d; want success/3", outcome, class)
	}
	if resourceType == nil || *resourceType != "scheduled_task" {
		t.Errorf("audit affected_resource_type = %v; want scheduled_task", resourceType)
	}
	var auditArgs CreateScheduledTaskArgs
	if err := json.Unmarshal(argsJSON, &auditArgs); err != nil {
		t.Fatalf("audit args_jsonb unmarshal: %v", err)
	}
	if auditArgs.Name != "standup" || auditArgs.ObjectiveTemplate == "" || auditArgs.AcceptanceCriteriaTemplate == "" {
		t.Errorf("audit args_jsonb did not capture full args: %+v", auditArgs)
	}
}

// TestCreateScheduledTaskRejectsAgentCaller: the verb is chat-only —
// AgentInstanceID callers get validation_failed ("agents cannot
// schedule work") and no row lands (threat-model §3 threat 7).
func TestCreateScheduledTaskRejectsAgentCaller(t *testing.T) {
	fx := setupIntegration(t)
	agentDeps := Deps{
		Pool:            fx.pool,
		AgentInstanceID: pgtype.UUID{Valid: true, Bytes: [16]byte{0x42}},
	}

	r, err := realCreateScheduledTaskHandler(context.Background(), agentDeps, validScheduledTaskArgs("agent-sneak", "daily@09:00"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Fatal("agent caller should be rejected")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, "agents cannot schedule work") {
		t.Errorf("Message = %q; want it to name the chat-only rule", r.Message)
	}
	if n := scheduledTaskCount(t, fx); n != 0 {
		t.Errorf("scheduled_tasks rows = %d; want 0", n)
	}
}

// TestCreateScheduledTaskRejectsBadGrammar: an expression outside the
// FR-103 vocabulary (full cron syntax) maps to validation_failed with
// the parse detail; the failure audit row carries validation_failed.
func TestCreateScheduledTaskRejectsBadGrammar(t *testing.T) {
	fx := setupIntegration(t)
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "15m")

	r, err := realCreateScheduledTaskHandler(context.Background(), fx.deps, validScheduledTaskArgs("cron-task", "0 9 * * *"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Fatal("bad grammar should be rejected")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, "invalid schedule expression") {
		t.Errorf("Message = %q; want the parse detail", r.Message)
	}
	if n := scheduledTaskCount(t, fx); n != 0 {
		t.Errorf("scheduled_tasks rows = %d; want 0", n)
	}
	if n := auditOutcomeCount(t, fx, "validation_failed"); n != 1 {
		t.Errorf("validation_failed audit rows = %d; want 1", n)
	}
}

// TestCreateScheduledTaskRejectsSubMinInterval: an effective interval
// below GARRISON_SCHED_MIN_INTERVAL rejects with validation_failed
// (FR-404); audit row carries validation_failed.
func TestCreateScheduledTaskRejectsSubMinInterval(t *testing.T) {
	fx := setupIntegration(t)
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "15m")

	r, err := realCreateScheduledTaskHandler(context.Background(), fx.deps, validScheduledTaskArgs("too-fast", "every@5m"))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Fatal("sub-minimum interval should be rejected")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, "minimum firing interval") {
		t.Errorf("Message = %q; want the min-interval detail", r.Message)
	}
	if n := scheduledTaskCount(t, fx); n != 0 {
		t.Errorf("scheduled_tasks rows = %d; want 0", n)
	}
	if n := auditOutcomeCount(t, fx, "validation_failed"); n != 1 {
		t.Errorf("validation_failed audit rows = %d; want 1", n)
	}
}

// TestCreateScheduledTaskRejectsDuplicateName: name uniqueness among
// live tasks (idx_scheduled_tasks_name_live) rejects the second create
// with validation_failed; the first row survives untouched.
func TestCreateScheduledTaskRejectsDuplicateName(t *testing.T) {
	fx := setupIntegration(t)
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "15m")

	first, err := realCreateScheduledTaskHandler(context.Background(), fx.deps, validScheduledTaskArgs("nightly-report", "daily@22:00"))
	if err != nil {
		t.Fatalf("first create error: %v", err)
	}
	if !first.Success {
		t.Fatalf("first create should succeed; got %+v", first)
	}

	second, err := realCreateScheduledTaskHandler(context.Background(), fx.deps, validScheduledTaskArgs("nightly-report", "daily@23:00"))
	if err != nil {
		t.Fatalf("second create error: %v", err)
	}
	if second.Success {
		t.Fatal("duplicate name should be rejected")
	}
	if second.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", second.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(second.Message, "already exists") {
		t.Errorf("Message = %q; want the duplicate-name detail", second.Message)
	}
	if n := scheduledTaskCount(t, fx); n != 1 {
		t.Errorf("scheduled_tasks rows = %d; want 1", n)
	}
	if n := auditOutcomeCount(t, fx, "success"); n != 1 {
		t.Errorf("success audit rows = %d; want 1", n)
	}
	if n := auditOutcomeCount(t, fx, "validation_failed"); n != 1 {
		t.Errorf("validation_failed audit rows = %d; want 1", n)
	}
}

// TestCreateScheduledTaskRejectsUnknownDepartment: a department slug
// with no row maps to validation_failed with the slug named, writes
// the failure audit, and inserts nothing (FR-105 via the verb's own
// slug-resolution step — ValidateTask is never reached).
func TestCreateScheduledTaskRejectsUnknownDepartment(t *testing.T) {
	fx := setupIntegration(t)
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "15m")

	args := json.RawMessage(`{"name":"ghost-task","department_slug":"astrology","role_slug":"engineering.engineer","mode":"ticket","schedule_expr":"daily@09:00","objective_template":"o","acceptance_criteria_template":"a"}`)
	r, err := realCreateScheduledTaskHandler(context.Background(), fx.deps, args)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Fatal("unknown department should be rejected")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, `department "astrology" not found`) {
		t.Errorf("Message = %q; want the unknown-department detail", r.Message)
	}
	if n := scheduledTaskCount(t, fx); n != 0 {
		t.Errorf("scheduled_tasks rows = %d; want 0", n)
	}
	if n := auditOutcomeCount(t, fx, "validation_failed"); n != 1 {
		t.Errorf("validation_failed audit rows = %d; want 1", n)
	}
}
