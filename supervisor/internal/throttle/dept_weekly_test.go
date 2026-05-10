//go:build integration

package throttle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deptWeeklyFixture wraps the seeded state for the dept-weekly gate
// integration tests.
type deptWeeklyFixture struct {
	pool      *pgxpool.Pool
	companyID pgtype.UUID
	deptID    pgtype.UUID
	deptSlug  string
}

func seedDeptWeekly(t *testing.T, ticketCount int, budget *int32) (deptWeeklyFixture, *store.Queries) {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE throttle_events, agent_instances, ticket_transitions, tickets,
		         agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name) VALUES ('dept-weekly-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	deptSlug := "engineering"
	if budget != nil {
		if err := pool.QueryRow(ctx,
			`INSERT INTO departments (company_id, slug, name, workspace_path, weekly_ticket_budget)
			 VALUES ($1, $2, 'Engineering', '/tmp/dw', $3)
			 RETURNING id`, companyID, deptSlug, *budget).Scan(&deptID); err != nil {
			t.Fatalf("seed dept w/ budget: %v", err)
		}
	} else {
		if err := pool.QueryRow(ctx,
			`INSERT INTO departments (company_id, slug, name, workspace_path)
			 VALUES ($1, $2, 'Engineering', '/tmp/dw')
			 RETURNING id`, companyID, deptSlug).Scan(&deptID); err != nil {
			t.Fatalf("seed dept null-budget: %v", err)
		}
	}
	for i := 0; i < ticketCount; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO tickets (department_id, objective) VALUES ($1, 'seed')`,
			deptID); err != nil {
			t.Fatalf("seed ticket %d: %v", i, err)
		}
	}
	return deptWeeklyFixture{
		pool:      pool,
		companyID: companyID,
		deptID:    deptID,
		deptSlug:  deptSlug,
	}, store.New(pool)
}

func TestDeptWeeklyGateBlocksAtBudgetPlusOne(t *testing.T) {
	budget := int32(5)
	fx, q := seedDeptWeekly(t, 5, &budget)
	d, err := throttle.CheckDeptWeekly(context.Background(), q, fx.deptID)
	if err != nil {
		t.Fatalf("CheckDeptWeekly: %v", err)
	}
	if d.Allowed {
		t.Errorf("expected Allowed=false; budget=5, current=5, 6th rejects")
	}
	if d.CurrentCount != 5 {
		t.Errorf("CurrentCount = %d; want 5", d.CurrentCount)
	}
	if d.Budget == nil || *d.Budget != 5 {
		t.Errorf("Budget = %v; want 5", d.Budget)
	}
}

func TestDeptWeeklyGateAllowsUnderBudget(t *testing.T) {
	budget := int32(10)
	fx, q := seedDeptWeekly(t, 5, &budget)
	d, err := throttle.CheckDeptWeekly(context.Background(), q, fx.deptID)
	if err != nil {
		t.Fatalf("CheckDeptWeekly: %v", err)
	}
	if !d.Allowed {
		t.Errorf("expected Allowed=true; current=5 < budget=10")
	}
}

func TestDeptWeeklyGateNullBudgetUnlimited(t *testing.T) {
	fx, q := seedDeptWeekly(t, 1000, nil)
	d, err := throttle.CheckDeptWeekly(context.Background(), q, fx.deptID)
	if err != nil {
		t.Fatalf("CheckDeptWeekly: %v", err)
	}
	if !d.Allowed {
		t.Errorf("expected Allowed=true with NULL budget (unlimited); got blocked")
	}
	if d.Budget != nil {
		t.Errorf("Budget = %v; want nil for NULL budget", d.Budget)
	}
}

func TestFireDeptWeeklyWritesEventAndPayload(t *testing.T) {
	budget := int32(5)
	fx, q := seedDeptWeekly(t, 5, &budget)
	decision, err := throttle.CheckDeptWeekly(context.Background(), q, fx.deptID)
	if err != nil {
		t.Fatalf("CheckDeptWeekly: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected Allowed=false")
	}
	if err := throttle.FireDeptWeekly(context.Background(), q, fx.companyID, decision, fx.deptID, "agent-instance-test"); err != nil {
		t.Fatalf("FireDeptWeekly: %v", err)
	}
	var kind string
	var payload []byte
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT kind, payload FROM throttle_events ORDER BY fired_at DESC LIMIT 1`).Scan(&kind, &payload); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if kind != throttle.KindDeptWeeklyBudgetExceeded {
		t.Errorf("kind = %q; want %q", kind, throttle.KindDeptWeeklyBudgetExceeded)
	}
	if !strings.Contains(string(payload), "engineering") || !strings.Contains(string(payload), "agent-instance-test") {
		t.Errorf("payload missing dept_slug or attempted_caller_id: %s", payload)
	}
}
