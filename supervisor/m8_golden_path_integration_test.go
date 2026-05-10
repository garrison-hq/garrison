//go:build integration

package supervisor_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/garrisonmutate"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestM8GoldenPathAgentCreatesFollowupTicket exercises spec US1 end-
// to-end without the live spawn pipeline: an engineer agent's
// garrison-mutate connection invokes create_ticket with a follow-up
// objective; the new ticket lands with column_slug='todo' and the
// chat_mutation_audit row carries agent_instance_id != NULL +
// chat_session_id IS NULL, parent_ticket_id auto-inherits the
// engineer's current ticket.
//
// The live spawn pipeline (M7 substrate) is covered by separate
// golden-path tests; this M8 test pins the agent-caller surface
// shape independent of the container boot path.
func TestM8GoldenPathAgentCreatesFollowupTicket(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, chat_messages, chat_sessions, agent_instances, tickets, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	q := store.New(pool)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('golden-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (company_id, slug, name, workspace_path)
		VALUES ($1, 'engineering', 'Engineering', '/tmp/m8gold')
		RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	var parentTicketID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'parent objective', 'in_dev') RETURNING id`, deptID).Scan(&parentTicketID); err != nil {
		t.Fatalf("seed parent ticket: %v", err)
	}
	var agentInstanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		 VALUES ($1, 'engineer', $2, 'running') RETURNING id`, deptID, parentTicketID).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seed agent_instance: %v", err)
	}

	deps := garrisonmutate.Deps{
		Pool:            pool,
		AgentInstanceID: agentInstanceID,
	}
	args := `{"objective":"investigate auth retry semantics","department_slug":"engineering"}`
	verb := garrisonmutate.FindVerb("create_ticket")
	if verb == nil {
		t.Fatal("create_ticket verb missing from registry")
	}
	res, err := verb.Handler(ctx, deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("create_ticket handler: %v", err)
	}
	if !res.Success {
		t.Fatalf("create_ticket rejected: %+v", res)
	}

	// Assert: new ticket row lands at column 'todo', parent =
	// engineer's current ticket.
	var newTicketID pgtype.UUID
	var newColumn string
	var newParent pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id, column_slug, parent_ticket_id FROM tickets WHERE id::text = $1`,
		res.AffectedResourceID).Scan(&newTicketID, &newColumn, &newParent); err != nil {
		t.Fatalf("readback ticket: %v", err)
	}
	if newColumn != "todo" {
		t.Errorf("column = %q; want todo", newColumn)
	}
	if !newParent.Valid || newParent.Bytes != parentTicketID.Bytes {
		t.Errorf("parent_ticket_id = %v; want auto-inherit %v", newParent, parentTicketID)
	}
	_ = q

	// Assert: audit row anchored on agent_instance_id, NOT chat_session_id.
	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat_mutation_audit
		  WHERE verb = 'create_ticket'
		    AND outcome = 'success'
		    AND agent_instance_id = $1
		    AND chat_session_id IS NULL`, agentInstanceID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("agent-anchored audit rows = %d; want 1", auditCount)
	}
}
