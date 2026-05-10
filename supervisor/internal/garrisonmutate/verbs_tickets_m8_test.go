//go:build integration

package garrisonmutate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// agentCallerFixture extends integrationFixture with an agent_instances
// row + its parent ticket so M8 tests can exercise the auto-inherit
// path. The fixture's deps carries AgentInstanceID set (and
// ChatSessionID intentionally zeroed) so assertExactlyOneCallerAnchor
// admits the call.
type agentCallerFixture struct {
	integrationFixture
	agentInstanceID pgtype.UUID
	parentTicketID  pgtype.UUID
}

func setupAgentCaller(t *testing.T) agentCallerFixture {
	t.Helper()
	base := setupIntegration(t)
	ctx := context.Background()

	// Seed the parent ticket the agent is currently working on.
	var parentID pgtype.UUID
	if err := base.pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'parent ticket', 'in_dev')
		 RETURNING id`, base.departmentID).Scan(&parentID); err != nil {
		t.Fatalf("seed parent ticket: %v", err)
	}

	// Seed the agent_instances row linking the calling agent to that
	// ticket. The schema is light by design — many newer fields are
	// nullable; the test only needs the (department_id, ticket_id,
	// role_slug, status) tuple the auto-inherit + audit paths read.
	var instanceID pgtype.UUID
	if err := base.pool.QueryRow(ctx, `
		INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		VALUES ($1, $2, $3, 'running')
		RETURNING id`,
		base.departmentID, base.agentRoleSlug, parentID,
	).Scan(&instanceID); err != nil {
		t.Fatalf("seed agent_instances: %v", err)
	}

	// Agent-caller deps: zero ChatSessionID/ChatMessageID; set AgentInstanceID.
	base.deps = Deps{
		Pool:            base.pool,
		AgentInstanceID: instanceID,
	}
	return agentCallerFixture{
		integrationFixture: base,
		agentInstanceID:    instanceID,
		parentTicketID:     parentID,
	}
}

func TestCreateTicketAgentCallerSucceeds(t *testing.T) {
	fx := setupAgentCaller(t)
	args := `{"objective":"agent follow-up","department_slug":"engineering"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected Success; got %+v", r)
	}
	// Audit row landed with agent_instance_id populated and
	// chat_session_id NULL.
	var sessionAnchor pgtype.UUID
	var agentAnchor pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT chat_session_id, agent_instance_id
		   FROM chat_mutation_audit
		  WHERE verb = 'create_ticket'
		  ORDER BY created_at DESC LIMIT 1`).Scan(&sessionAnchor, &agentAnchor); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if sessionAnchor.Valid {
		t.Errorf("chat_session_id = %v; want NULL", sessionAnchor)
	}
	if !agentAnchor.Valid || agentAnchor.Bytes != fx.agentInstanceID.Bytes {
		t.Errorf("agent_instance_id = %v; want %v", agentAnchor, fx.agentInstanceID)
	}
}

func TestCreateTicketAgentCallerAutoInheritsParent(t *testing.T) {
	fx := setupAgentCaller(t)
	args := `{"objective":"child","department_slug":"engineering"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil || !r.Success {
		t.Fatalf("handler: %v / %+v", err, r)
	}
	var parentID pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT parent_ticket_id FROM tickets WHERE id::text = $1`,
		r.AffectedResourceID,
	).Scan(&parentID); err != nil {
		t.Fatalf("readback parent: %v", err)
	}
	if !parentID.Valid {
		t.Fatalf("parent_ticket_id NULL; want %v", fx.parentTicketID)
	}
	if parentID.Bytes != fx.parentTicketID.Bytes {
		t.Errorf("parent_ticket_id = %v; want %v", parentID, fx.parentTicketID)
	}
}

func TestCreateTicketAgentExplicitParentOverridesAutoInherit(t *testing.T) {
	fx := setupAgentCaller(t)
	// Seed a second ticket the agent will explicitly point at instead
	// of its auto-inherit parent.
	var explicitParent pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(),
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'sibling parent', 'in_dev')
		 RETURNING id`, fx.departmentID).Scan(&explicitParent); err != nil {
		t.Fatalf("seed explicit parent: %v", err)
	}
	args := fmt.Sprintf(
		`{"objective":"x","department_slug":"engineering","parent_ticket_id":%q}`,
		fmtUUID(explicitParent),
	)
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil || !r.Success {
		t.Fatalf("handler: %v / %+v", err, r)
	}
	var parentID pgtype.UUID
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT parent_ticket_id FROM tickets WHERE id::text = $1`, r.AffectedResourceID,
	).Scan(&parentID); err != nil {
		t.Fatalf("readback parent: %v", err)
	}
	if parentID.Bytes != explicitParent.Bytes {
		t.Errorf("explicit parent override failed: got %v want %v", parentID, explicitParent)
	}
}

func TestCreateTicketBothAnchorsRejects(t *testing.T) {
	fx := setupAgentCaller(t)
	// Forge a Deps that has BOTH anchors set — simulates a supervisor
	// wiring bug. The verb must reject without touching the DB.
	bad := Deps{
		Pool:            fx.pool,
		ChatSessionID:   fx.chatSessionID,
		ChatMessageID:   fx.chatMessageID,
		AgentInstanceID: fx.agentInstanceID,
	}
	args := `{"objective":"x","department_slug":"engineering"}`
	r, err := realCreateTicketHandler(context.Background(), bad, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want validation_failed", r.ErrorKind)
	}
	if !strings.Contains(r.Message, "wiring bug") {
		t.Errorf("message = %q; want wiring-bug surfaced", r.Message)
	}
}

func TestCreateTicketDependencyCycleRejects(t *testing.T) {
	fx := setupAgentCaller(t)
	ctx := context.Background()
	// Build A→B→A graph by raw SQL: A depends_on=NULL, B depends_on=A,
	// then UPDATE A.depends_on_ticket_id=B → cycle. The walker should
	// detect the cycle when we try to create C with depends_on=A.
	var a pgtype.UUID
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'A', 'todo') RETURNING id`, fx.departmentID).Scan(&a); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	var b pgtype.UUID
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug, depends_on_ticket_id)
		 VALUES ($1, 'B', 'todo', $2) RETURNING id`, fx.departmentID, a).Scan(&b); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`UPDATE tickets SET depends_on_ticket_id = $2 WHERE id = $1`, a, b); err != nil {
		t.Fatalf("close cycle: %v", err)
	}
	args := fmt.Sprintf(
		`{"objective":"C","department_slug":"engineering","depends_on_ticket_id":%q}`,
		fmtUUID(a),
	)
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrDependencyCycle) {
		t.Errorf("ErrorKind = %q; want dependency_cycle", r.ErrorKind)
	}
}

func TestCreateTicketDependencyChainTooDeep(t *testing.T) {
	fx := setupAgentCaller(t)
	ctx := context.Background()
	// Seed a 33-hop chain: t0 depends_on=NULL, t1 depends_on=t0, ...,
	// t32 depends_on=t31. Attempting to create a new ticket with
	// depends_on=t32 must reject as too deep.
	var prev pgtype.UUID
	var head pgtype.UUID
	for i := 0; i < 33; i++ {
		var id pgtype.UUID
		if !prev.Valid {
			if err := fx.pool.QueryRow(ctx,
				`INSERT INTO tickets (department_id, objective, column_slug)
				 VALUES ($1, $2, 'todo') RETURNING id`,
				fx.departmentID, fmt.Sprintf("t%d", i),
			).Scan(&id); err != nil {
				t.Fatalf("seed t%d: %v", i, err)
			}
		} else {
			if err := fx.pool.QueryRow(ctx,
				`INSERT INTO tickets (department_id, objective, column_slug, depends_on_ticket_id)
				 VALUES ($1, $2, 'todo', $3) RETURNING id`,
				fx.departmentID, fmt.Sprintf("t%d", i), prev,
			).Scan(&id); err != nil {
				t.Fatalf("seed t%d: %v", i, err)
			}
		}
		prev = id
		head = id
	}
	args := fmt.Sprintf(
		`{"objective":"too_deep","department_slug":"engineering","depends_on_ticket_id":%q}`,
		fmtUUID(head),
	)
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrDependencyChainTooDeep) {
		t.Errorf("ErrorKind = %q; want dependency_chain_too_deep", r.ErrorKind)
	}
}

func TestCreateTicketDeptWeeklyBudgetExceeded(t *testing.T) {
	fx := setupAgentCaller(t)
	ctx := context.Background()
	// Set budget=1 + insert 1 ticket; second create must reject.
	if _, err := fx.pool.Exec(ctx,
		`UPDATE departments SET weekly_ticket_budget = 1 WHERE id = $1`, fx.departmentID); err != nil {
		t.Fatalf("set budget: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'fill the budget', 'todo')`, fx.departmentID); err != nil {
		t.Fatalf("fill budget: %v", err)
	}
	args := `{"objective":"over budget","department_slug":"engineering"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrDeptWeeklyBudgetExceeded) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrDeptWeeklyBudgetExceeded)
	}
	var throttleCount int
	if err := fx.pool.QueryRow(ctx,
		`SELECT count(*) FROM throttle_events WHERE kind = 'dept_weekly_ticket_budget_exceeded'`,
	).Scan(&throttleCount); err != nil {
		t.Fatalf("count throttle_events: %v", err)
	}
	if throttleCount != 1 {
		t.Errorf("throttle_events = %d; want 1", throttleCount)
	}
}

func TestCreateTicketAgentOnlyAuditCommitsCleanly(t *testing.T) {
	fx := setupAgentCaller(t)
	args := `{"objective":"x","department_slug":"engineering"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil || !r.Success {
		t.Fatalf("handler: %v / %+v", err, r)
	}
	var count int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_mutation_audit
		  WHERE chat_session_id IS NULL
		    AND agent_instance_id IS NOT NULL
		    AND verb = 'create_ticket'`).Scan(&count); err != nil {
		t.Fatalf("count agent-only audit: %v", err)
	}
	if count != 1 {
		t.Errorf("agent-only audit rows = %d; want 1", count)
	}
}

func TestCreateTicketCrossDeptGateScopesAgainstTarget(t *testing.T) {
	fx := setupAgentCaller(t)
	ctx := context.Background()
	// Seed a second department + set ITS budget to 1; fill it. The
	// agent caller (currently in engineering) attempts to create in
	// the second dept; the second dept's gate must fire even though
	// engineering has no budget set.
	var marketingDept pgtype.UUID
	if err := fx.pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path, weekly_ticket_budget)
		 VALUES ((SELECT company_id FROM departments WHERE id = $1), 'marketing', 'Marketing', '/tmp/mkt', 1)
		 RETURNING id`, fx.departmentID).Scan(&marketingDept); err != nil {
		t.Fatalf("seed marketing: %v", err)
	}
	if _, err := fx.pool.Exec(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'fill marketing budget', 'todo')`, marketingDept); err != nil {
		t.Fatalf("fill marketing: %v", err)
	}
	args := `{"objective":"engineering->marketing cross","department_slug":"marketing"}`
	r, err := realCreateTicketHandler(context.Background(), fx.deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrDeptWeeklyBudgetExceeded) {
		t.Errorf("ErrorKind = %q; want budget-exceeded", r.ErrorKind)
	}
}

// fmtUUID renders a pgtype.UUID as a canonical 36-char string for
// JSON-embedded UUIDs in test args. Mirrors uuidString in
// verbs_tickets.go (which is unexported).
func fmtUUID(u pgtype.UUID) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
