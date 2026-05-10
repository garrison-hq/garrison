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

// TestM8AuditAgentAnchoredSurface exercises spec FR-502 + SC-012:
// after an agent caller commits a create_ticket, the audit row
// resolves to its originating ticket + agent role within one query
// (ResolveAgentAuditAnchors). The dashboard /activity?agent_instance_id
// surface uses ListAuditByAgentInstance which pins the equivalent
// shape on the read side.
func TestM8AuditAgentAnchoredSurface(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, agent_instances, tickets, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	q := store.New(pool)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('audit-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/audit')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, status)
		 VALUES ($1, 'engineer', '# x', 'claude-h', '[]'::jsonb, '[]'::jsonb, '["x"]'::jsonb, 'active')
		 RETURNING id`, deptID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	var parentTicketID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'parent', 'in_dev') RETURNING id`, deptID).Scan(&parentTicketID); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	var agentInstanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		 VALUES ($1, 'engineer', $2, 'running') RETURNING id`,
		deptID, parentTicketID).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seed agent_instance: %v", err)
	}

	deps := garrisonmutate.Deps{Pool: pool, AgentInstanceID: agentInstanceID}
	res, err := garrisonmutate.FindVerb("create_ticket").Handler(ctx, deps,
		json.RawMessage(`{"objective":"audit follow-up","department_slug":"engineering"}`))
	if err != nil {
		t.Fatalf("create_ticket: %v", err)
	}
	if !res.Success {
		t.Fatalf("create_ticket rejected: %+v", res)
	}

	// ListAuditByAgentInstance (the M8 sqlc query the dashboard
	// surface uses) returns the row.
	rows, err := q.ListAuditByAgentInstance(ctx, store.ListAuditByAgentInstanceParams{
		AgentInstanceID: agentInstanceID,
		LimitN:          50,
	})
	if err != nil {
		t.Fatalf("ListAuditByAgentInstance: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows by agent_instance = %d; want 1", len(rows))
	}
	if rows[0].Verb != "create_ticket" {
		t.Errorf("verb = %s; want create_ticket", rows[0].Verb)
	}
	if rows[0].Outcome != "success" {
		t.Errorf("outcome = %s; want success", rows[0].Outcome)
	}
	if rows[0].ChatSessionID.Valid {
		t.Errorf("chat_session_id should be NULL on agent-anchored row")
	}

	// ResolveAgentAuditAnchors (SC-012): single query yields agent
	// role + originating ticket.
	resolved, err := q.ResolveAgentAuditAnchors(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("ResolveAgentAuditAnchors: %v", err)
	}
	if resolved.RoleSlug != "engineer" {
		t.Errorf("role_slug = %s; want engineer", resolved.RoleSlug)
	}
	if resolved.TicketID.Bytes != parentTicketID.Bytes {
		t.Errorf("ticket_id mismatch: got %v want %v (the agent's current ticket)",
			resolved.TicketID, parentTicketID)
	}
}
