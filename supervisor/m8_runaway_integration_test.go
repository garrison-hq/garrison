//go:build integration

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/garrisonmutate"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runawayFixture struct {
	pool            *pgxpool.Pool
	deptID          pgtype.UUID
	agentInstanceID pgtype.UUID
}

func seedRunaway(t *testing.T, ctx context.Context, slug string, budget *int32, fillN int) runawayFixture {
	t.Helper()
	pool := testdb.Start(t)
	if _, err := pool.Exec(ctx,
		`TRUNCATE throttle_events, chat_mutation_audit, agent_instances, tickets, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('runaway-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if budget != nil {
		if err := pool.QueryRow(ctx,
			`INSERT INTO departments (company_id, slug, name, workspace_path, weekly_ticket_budget)
			 VALUES ($1, $2, 'Department', '/tmp/m8run', $3) RETURNING id`,
			companyID, slug, *budget).Scan(&deptID); err != nil {
			t.Fatalf("seed dept: %v", err)
		}
	} else {
		if err := pool.QueryRow(ctx,
			`INSERT INTO departments (company_id, slug, name, workspace_path)
			 VALUES ($1, $2, 'Department', '/tmp/m8run') RETURNING id`,
			companyID, slug).Scan(&deptID); err != nil {
			t.Fatalf("seed dept null-budget: %v", err)
		}
	}
	for i := 0; i < fillN; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO tickets (department_id, objective, column_slug)
			 VALUES ($1, 'fill', 'todo')`, deptID); err != nil {
			t.Fatalf("fill[%d]: %v", i, err)
		}
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
		 VALUES ($1, 'engineer', $2, 'running') RETURNING id`, deptID, parentTicketID).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seed agent_instance: %v", err)
	}
	return runawayFixture{pool: pool, deptID: deptID, agentInstanceID: agentInstanceID}
}

// TestM8RunawayBudgetExceeded exercises spec US3: dept with budget=50
// + 50 tickets already present; the 51st create_ticket rejects with
// dept_weekly_ticket_budget_exceeded; one throttle_events row lands.
func TestM8RunawayBudgetExceeded(t *testing.T) {
	ctx := context.Background()
	budget := int32(50)
	fx := seedRunaway(t, ctx, "engineering", &budget, 50)
	deps := garrisonmutate.Deps{Pool: fx.pool, AgentInstanceID: fx.agentInstanceID}
	res, err := garrisonmutate.FindVerb("create_ticket").Handler(ctx, deps,
		json.RawMessage(`{"objective":"over","department_slug":"engineering"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.Success {
		t.Errorf("expected rejection; got %+v", res)
	}
	if res.ErrorKind != "dept_weekly_ticket_budget_exceeded" {
		t.Errorf("ErrorKind = %q; want dept_weekly_ticket_budget_exceeded", res.ErrorKind)
	}
	var throttleCount int
	if err := fx.pool.QueryRow(ctx,
		`SELECT count(*) FROM throttle_events
		 WHERE kind = 'dept_weekly_ticket_budget_exceeded'`).Scan(&throttleCount); err != nil {
		t.Fatalf("count throttle: %v", err)
	}
	if throttleCount != 1 {
		t.Errorf("throttle_events = %d; want 1", throttleCount)
	}
}

// TestM8RunawayNullBudgetUnlimited — NULL budget bypasses the gate
// even at high counts.
func TestM8RunawayNullBudgetUnlimited(t *testing.T) {
	ctx := context.Background()
	fx := seedRunaway(t, ctx, "engineering", nil, 1000)
	deps := garrisonmutate.Deps{Pool: fx.pool, AgentInstanceID: fx.agentInstanceID}
	res, err := garrisonmutate.FindVerb("create_ticket").Handler(ctx, deps,
		json.RawMessage(`{"objective":"unbounded","department_slug":"engineering"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Success {
		t.Errorf("expected success with NULL budget; got %+v", res)
	}
}

// TestM8RunawayCrossDeptGateScopesTarget — engineering caller, target
// marketing; marketing's budget binds even though engineering has no
// budget set.
func TestM8RunawayCrossDeptGateScopesTarget(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Start(t)
	if _, err := pool.Exec(ctx,
		`TRUNCATE throttle_events, chat_mutation_audit, agent_instances, tickets, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('cross-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var engDeptID, mktDeptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/eng')
		 RETURNING id`, companyID).Scan(&engDeptID); err != nil {
		t.Fatalf("seed eng: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path, weekly_ticket_budget)
		 VALUES ($1, 'marketing', 'Marketing', '/tmp/mkt', 1)
		 RETURNING id`, companyID).Scan(&mktDeptID); err != nil {
		t.Fatalf("seed mkt: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'fill mkt', 'todo')`, mktDeptID); err != nil {
		t.Fatalf("fill mkt: %v", err)
	}
	var parentTicketID, agentInstanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'eng parent', 'in_dev') RETURNING id`, engDeptID).Scan(&parentTicketID); err != nil {
		t.Fatalf("seed eng parent: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		 VALUES ($1, 'engineer', $2, 'running') RETURNING id`, engDeptID, parentTicketID).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seed agent_instance: %v", err)
	}
	deps := garrisonmutate.Deps{Pool: pool, AgentInstanceID: agentInstanceID}
	res, err := garrisonmutate.FindVerb("create_ticket").Handler(ctx, deps,
		json.RawMessage(`{"objective":"eng->mkt","department_slug":"marketing"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.Success {
		t.Errorf("expected mkt-budget rejection; got %+v", res)
	}
	if res.ErrorKind != "dept_weekly_ticket_budget_exceeded" {
		t.Errorf("ErrorKind = %q; want budget-exceeded", res.ErrorKind)
	}
}

// TestM8RunawayRollingWindowExpiry — when the oldest ticket in the
// rolling 7d falls out of the window (we shift its created_at
// older), the gate allows the next create.
//
// The dept-weekly state counts EVERY ticket in the dept (including the
// agent_instance parent ticket). Setting budget=3 + fillN=2 yields
// 3 tickets in window (2 fill + 1 parent); shifting one out of the
// window drops count to 2 so the next create (2+1 ≤ 3) is allowed.
func TestM8RunawayRollingWindowExpiry(t *testing.T) {
	ctx := context.Background()
	budget := int32(3)
	fx := seedRunaway(t, ctx, "engineering", &budget, 2)
	// Shift the oldest fill ticket past the 7-day window so dept_weekly_state
	// drops from 3 → 2.
	if _, err := fx.pool.Exec(ctx,
		`UPDATE tickets SET created_at = NOW() - INTERVAL '8 days'
		 WHERE id = (SELECT id FROM tickets WHERE department_id = $1 AND objective = 'fill'
		             ORDER BY created_at ASC LIMIT 1)`, fx.deptID); err != nil {
		t.Fatalf("age oldest ticket: %v", err)
	}
	deps := garrisonmutate.Deps{Pool: fx.pool, AgentInstanceID: fx.agentInstanceID}
	res, err := garrisonmutate.FindVerb("create_ticket").Handler(ctx, deps,
		json.RawMessage(fmt.Sprintf(
			`{"objective":"%s","department_slug":"engineering"}`, "rolling-window-fits")))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.Success {
		t.Errorf("expected success after rolling window expiry; got %+v", res)
	}
}
