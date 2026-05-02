//go:build integration

package garrisonmutate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationFixture seeds a minimal company/department/agent + a fresh
// chat_session + chat_message so the verb's audit-row INSERT has valid
// FKs to commit against. Returns the fixture with all ID handles each
// verb test needs. Also TRUNCATEs the chat-side tables that testdb.Start
// doesn't (chat_sessions, chat_messages, chat_mutation_audit,
// hiring_proposals, agent_role_secrets, vault_access_log,
// secret_metadata, ticket_comments).
type integrationFixture struct {
	pool           *pgxpool.Pool
	deps           Deps
	departmentID   pgtype.UUID
	departmentSlug string
	agentRoleSlug  string
	agentID        pgtype.UUID
	chatSessionID  pgtype.UUID
	chatMessageID  pgtype.UUID
}

func setupIntegration(t *testing.T) integrationFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()

	// Wipe chat-side rows that testdb.Start doesn't touch — CASCADE
	// removes chat_mutation_audit + hiring_proposals + chat_messages
	// when chat_sessions is truncated.
	truncate := func() {
		_, _ = pool.Exec(ctx, "TRUNCATE chat_sessions, hiring_proposals, chat_mutation_audit, chat_messages CASCADE")
	}
	truncate()
	t.Cleanup(truncate)

	// Minimal company + department + agent seed (echoes testdb.SeedM21
	// without the workspace_path requirement so this test stays
	// supervisor-binary-free).
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('test-co') RETURNING id`).
		Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/engineering')
		 RETURNING id`, companyID).
		Scan(&deptID); err != nil {
		t.Fatalf("seed department: %v", err)
	}
	roleSlug := "engineering.engineer"
	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
		 VALUES ($1, $2, '["work.ticket.created.engineering.todo"]'::jsonb, 'agent prose', 'claude-x', 'active')
		 RETURNING id`, deptID, roleSlug).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Operator user for the chat session — chat_sessions needs a
	// started_by_user_id; for tests we just use a deterministic UUID.
	operatorID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xee}}

	var sessionID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd)
		 VALUES ($1, 'active', 0)
		 RETURNING id`, operatorID).Scan(&sessionID); err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	var messageID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status, content)
		 VALUES ($1, 0, 'operator', 'completed', 'hello')
		 RETURNING id`, sessionID).Scan(&messageID); err != nil {
		t.Fatalf("seed chat message: %v", err)
	}

	return integrationFixture{
		pool:           pool,
		deps:           Deps{Pool: pool, ChatSessionID: sessionID, ChatMessageID: messageID},
		departmentID:   deptID,
		departmentSlug: "engineering",
		agentRoleSlug:  roleSlug,
		agentID:        agentID,
		chatSessionID:  sessionID,
		chatMessageID:  messageID,
	}
}

// auditCount returns the number of chat_mutation_audit rows for this
// fixture's session — a quick way to assert "the verb's audit row
// committed" without locking down exact column values.
func auditCount(t *testing.T, fx integrationFixture, verb string) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM chat_mutation_audit
		  WHERE chat_session_id = $1 AND verb = $2`,
		fx.chatSessionID, verb).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

func TestCreateTicket_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	args := `{"objective":"build the chat verb integration suite","department_slug":"engineering","acceptance_criteria":"all 8 verbs covered"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}
	if r.AffectedResourceID == "" {
		t.Error("expected ticket id in result")
	}
	if !strings.HasPrefix(r.AffectedResourceURL, "/tickets/") {
		t.Errorf("URL prefix wrong: %q", r.AffectedResourceURL)
	}
	if got := auditCount(t, fx, "create_ticket"); got != 1 {
		t.Errorf("audit rows = %d; want 1", got)
	}
	// Ticket actually exists.
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM tickets WHERE created_via_chat_session_id = $1`,
		fx.chatSessionID).Scan(&n); err != nil {
		t.Fatalf("ticket count: %v", err)
	}
	if n != 1 {
		t.Errorf("ticket rows = %d; want 1", n)
	}
}

func TestCreateTicket_UnknownDepartment(t *testing.T) {
	fx := setupIntegration(t)
	args := `{"objective":"x","department_slug":"does-not-exist"}`
	r, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if r.Success {
		t.Fatal("expected failure")
	}
	if r.ErrorKind != string(ErrResourceNotFound) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrResourceNotFound)
	}
	// Failure audit row should be written separately.
	if got := auditCount(t, fx, "create_ticket"); got != 1 {
		t.Errorf("failure audit rows = %d; want 1", got)
	}
}

func TestEditTicket_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	// Create a ticket first.
	create := `{"objective":"orig","department_slug":"engineering"}`
	created, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(create))
	if err != nil || !created.Success {
		t.Fatalf("seed ticket: %v / %+v", err, created)
	}
	// Now edit it.
	editArgs, _ := json.Marshal(EditTicketArgs{
		TicketID:  created.AffectedResourceID,
		Objective: ptrString("updated objective text"),
	})
	r, err := realEditTicketHandler(context.Background(), fx.deps, editArgs)
	if err != nil {
		t.Fatalf("edit handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected edit Success; got %+v", r)
	}
	// Verify the change landed.
	var obj string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT objective FROM tickets WHERE id::text = $1`, created.AffectedResourceID).
		Scan(&obj); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if obj != "updated objective text" {
		t.Errorf("objective = %q; want updated", obj)
	}
}

func TestTransitionTicket_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	create := `{"objective":"x","department_slug":"engineering"}`
	created, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(create))
	if !created.Success {
		t.Fatalf("seed: %+v", created)
	}
	args, _ := json.Marshal(TransitionTicketArgs{
		TicketID: created.AffectedResourceID,
		ToColumn: "in_progress",
	})
	r, err := realTransitionTicketHandler(context.Background(), fx.deps, args)
	if err != nil {
		t.Fatalf("transition err: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}
	var col string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT column_slug FROM tickets WHERE id::text = $1`, created.AffectedResourceID).Scan(&col); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if col != "in_progress" {
		t.Errorf("column = %q; want in_progress", col)
	}
}

func TestTransitionTicket_IdempotentSameColumn(t *testing.T) {
	fx := setupIntegration(t)
	create := `{"objective":"x","department_slug":"engineering"}`
	created, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(create))
	args, _ := json.Marshal(TransitionTicketArgs{
		TicketID: created.AffectedResourceID,
		ToColumn: "todo", // already in todo
	})
	r, err := realTransitionTicketHandler(context.Background(), fx.deps, args)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !r.Success || !strings.Contains(r.Message, "already in column") {
		t.Errorf("expected idempotent success; got %+v", r)
	}
}

func TestPauseAgent_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	args, _ := json.Marshal(PauseAgentArgs{AgentRoleSlug: fx.agentRoleSlug})
	r, err := realPauseAgentHandler(context.Background(), fx.deps, args)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}
	var status string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status FROM agents WHERE id = $1`, fx.agentID).Scan(&status); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "paused" {
		t.Errorf("agent status = %q; want paused", status)
	}
}

func TestResumeAgent_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	// First pause.
	pa, _ := json.Marshal(PauseAgentArgs{AgentRoleSlug: fx.agentRoleSlug})
	if _, err := realPauseAgentHandler(context.Background(), fx.deps, pa); err != nil {
		t.Fatalf("pause: %v", err)
	}
	ra, _ := json.Marshal(ResumeAgentArgs{AgentRoleSlug: fx.agentRoleSlug})
	r, err := realResumeAgentHandler(context.Background(), fx.deps, ra)
	if err != nil || !r.Success {
		t.Fatalf("resume: %v / %+v", err, r)
	}
	var status string
	_ = fx.pool.QueryRow(context.Background(), `SELECT status FROM agents WHERE id = $1`, fx.agentID).Scan(&status)
	if status != "active" {
		t.Errorf("agent status = %q; want active", status)
	}
}

func TestPauseAgent_UnknownRole(t *testing.T) {
	fx := setupIntegration(t)
	args, _ := json.Marshal(PauseAgentArgs{AgentRoleSlug: "engineering.does-not-exist"})
	r, _ := realPauseAgentHandler(context.Background(), fx.deps, args)
	if r.Success {
		t.Fatal("expected failure")
	}
	if r.ErrorKind != string(ErrResourceNotFound) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrResourceNotFound)
	}
}

func TestSpawnAgent_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	// Need a real ticket to attach the spawn intent to.
	create := `{"objective":"x","department_slug":"engineering"}`
	tk, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(create))
	args, _ := json.Marshal(SpawnAgentArgs{AgentRoleSlug: fx.agentRoleSlug, TicketID: tk.AffectedResourceID})
	r, err := realSpawnAgentHandler(context.Background(), fx.deps, args)
	if err != nil || !r.Success {
		t.Fatalf("spawn: %v / %+v", err, r)
	}
	if got := auditCount(t, fx, "spawn_agent"); got != 1 {
		t.Errorf("audit rows = %d; want 1", got)
	}
}

func TestEditAgentConfig_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	newModel := "claude-updated"
	newWing := "engineering"
	args, _ := json.Marshal(EditAgentConfigArgs{
		AgentRoleSlug: fx.agentRoleSlug,
		Model:         &newModel,
		PalaceWing:    &newWing,
	})
	r, err := realEditAgentConfigHandler(context.Background(), fx.deps, args)
	if err != nil || !r.Success {
		t.Fatalf("edit_agent_config: %v / %+v", err, r)
	}
	var got store.Agent
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT model, palace_wing FROM agents WHERE id = $1`, fx.agentID).
		Scan(&got.Model, &got.PalaceWing); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got.Model != "claude-updated" || got.PalaceWing == nil || *got.PalaceWing != "engineering" {
		t.Errorf("config not applied: %+v", got)
	}
}

func TestEditAgentConfig_RejectsLeak(t *testing.T) {
	fx := setupIntegration(t)
	leakedMD := "real key: sk-abcdefghij1234567890abcdef"
	args, _ := json.Marshal(EditAgentConfigArgs{
		AgentRoleSlug: fx.agentRoleSlug,
		AgentMD:       &leakedMD,
	})
	r, _ := realEditAgentConfigHandler(context.Background(), fx.deps, args)
	if r.Success {
		t.Fatal("expected failure")
	}
	if r.ErrorKind != string(ErrLeakScanFailed) {
		t.Errorf("ErrorKind = %q; want leak_scan_failed", r.ErrorKind)
	}
}

func TestProposeHire_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	args, _ := json.Marshal(ProposeHireArgs{
		RoleTitle:       "growth-strategist",
		DepartmentSlug:  fx.departmentSlug,
		JustificationMD: "we need this role to drive the growth funnel for Q3 push",
		SkillsSummaryMD: "growth, analytics, brand",
	})
	r, err := realProposeHireHandler(context.Background(), fx.deps, args)
	if err != nil || !r.Success {
		t.Fatalf("propose_hire: %v / %+v", err, r)
	}
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM hiring_proposals WHERE proposed_by_chat_session_id = $1`,
		fx.chatSessionID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("hiring_proposals = %d; want 1", n)
	}
}

func ptrString(s string) *string { return &s }

// -------- M6 T010 parent_ticket_id validation ---------------------------

// seedQAEngineerDept inserts a second department + a ticket in 'in_dev'
// so the cross-dept rejection test has a parent in another department.
func seedQAEngineerDept(t *testing.T, fx integrationFixture) (deptID, ticketID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 SELECT company_id, 'qa-engineer', 'QA Engineer', '/tmp/qa-engineer'
		   FROM departments WHERE id = $1
		 RETURNING id`, fx.departmentID).Scan(&deptID); err != nil {
		t.Fatalf("seed qa-engineer dept: %v", err)
	}
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'parent in qa-engineer', 'in_dev')
		 RETURNING id`, deptID).Scan(&ticketID); err != nil {
		t.Fatalf("seed qa-engineer ticket: %v", err)
	}
	return
}

// TestCreateTicketWithParent_HappyPath — parent + child in the same dept,
// parent not in done. The handler accepts and the audit row reflects
// the parent linkage.
func TestCreateTicketWithParent_HappyPath(t *testing.T) {
	fx := setupIntegration(t)
	parentJSON := `{"objective":"parent","department_slug":"engineering","column_slug":"in_dev"}`
	parent, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(parentJSON))
	if !parent.Success {
		t.Fatalf("seed parent: %+v", parent)
	}

	childJSON := `{"objective":"child","department_slug":"engineering","parent_ticket_id":"` + parent.AffectedResourceID + `"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(childJSON))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}

	var parentLinked pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT parent_ticket_id FROM tickets WHERE id::text = $1`, r.AffectedResourceID,
	).Scan(&parentLinked); err != nil {
		t.Fatalf("read child parent_ticket_id: %v", err)
	}
	if !parentLinked.Valid {
		t.Errorf("expected parent_ticket_id set on child")
	}
}

// TestCreateTicketWithParent_RejectsCrossDept — parent in one department,
// child requested in another. validation_failed with a clear message.
func TestCreateTicketWithParent_RejectsCrossDept(t *testing.T) {
	fx := setupIntegration(t)
	_, qaParentID := seedQAEngineerDept(t, fx)
	parentIDStr := uuidString(qaParentID)

	childJSON := `{"objective":"child","department_slug":"engineering","parent_ticket_id":"` + parentIDStr + `"}`
	r, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(childJSON))
	if r.Success {
		t.Fatal("expected failure for cross-dept parent")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, "different department") {
		t.Errorf("Message %q missing 'different department'", r.Message)
	}
}

// TestCreateTicketWithParent_RejectsClosedParent — parent already closed
// (column_slug='done'). validation_failed.
func TestCreateTicketWithParent_RejectsClosedParent(t *testing.T) {
	fx := setupIntegration(t)
	parentJSON := `{"objective":"parent","department_slug":"engineering","column_slug":"done"}`
	parent, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(parentJSON))
	if !parent.Success {
		t.Fatalf("seed parent: %+v", parent)
	}
	childJSON := `{"objective":"child","department_slug":"engineering","parent_ticket_id":"` + parent.AffectedResourceID + `"}`
	r, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(childJSON))
	if r.Success {
		t.Fatal("expected failure for closed parent")
	}
	if !strings.Contains(r.Message, "already closed") {
		t.Errorf("Message %q missing 'already closed'", r.Message)
	}
}

// TestCreateTicketWithParent_RejectsMissingParent — parent_ticket_id is
// a syntactically valid UUID but no row matches.
func TestCreateTicketWithParent_RejectsMissingParent(t *testing.T) {
	fx := setupIntegration(t)
	bogusUUID := "00000000-0000-0000-0000-000000000123"
	childJSON := `{"objective":"child","department_slug":"engineering","parent_ticket_id":"` + bogusUUID + `"}`
	r, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(childJSON))
	if r.Success {
		t.Fatal("expected failure for missing parent")
	}
	if !strings.Contains(r.Message, "does not exist") {
		t.Errorf("Message %q missing 'does not exist'", r.Message)
	}
}

// TestCreateTicketWithParent_NilParentSucceeds — explicit empty
// parent_ticket_id behaves the same as omitting it; the existing happy
// path is unchanged.
func TestCreateTicketWithParent_NilParentSucceeds(t *testing.T) {
	fx := setupIntegration(t)
	args := `{"objective":"no-parent ticket","department_slug":"engineering","parent_ticket_id":""}`
	r, _ := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}
}
