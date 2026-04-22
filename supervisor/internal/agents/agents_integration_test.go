//go:build integration

package agents_test

import (
	"context"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestCacheLoadsEngineerSeedFromRealDB confirms NewCache loads the
// engineer row produced by the M2.1 migration seed (plan.md Appendix A /
// T003), including the JSONB listens_for decoding step that the unit
// tests exercise against hand-crafted payloads.
//
// testdb.Start applies every migration (including M2.1) at container boot
// and the per-test TRUNCATE CASCADE on departments wipes `agents` through
// the FK. To stay order-independent regardless of which other integration
// tests have already run, this test re-seeds a minimal engineering
// department + agent whose row values mirror the migration's seed. When
// the migration's seed is still present (first test to run) the seed
// already carries engineering+engineer; in that case the re-seed step
// uses ON CONFLICT DO NOTHING so the test remains idempotent.
func TestCacheLoadsEngineerSeedFromRealDB(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	// Re-seed engineering+engineer only if they are missing. ON CONFLICT
	// handles the case where the migration's seed is still in place.
	if _, err := pool.Exec(ctx, `
		INSERT INTO companies (id, name)
		SELECT gen_random_uuid(), 'test co'
		WHERE NOT EXISTS (SELECT 1 FROM companies)
	`); err != nil {
		t.Fatalf("re-seed company: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		SELECT gen_random_uuid(), c.id, 'engineering', 'Engineering', 1, '/workspaces/engineering'
		FROM companies c
		WHERE NOT EXISTS (SELECT 1 FROM departments WHERE slug = 'engineering')
		LIMIT 1
	`); err != nil {
		t.Fatalf("re-seed department: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
		SELECT gen_random_uuid(), d.id, 'engineer',
			'# Engineer (M2.1)\n\nseed body for agents integration test',
			'claude-haiku-4-5-20251001',
			'[]'::jsonb, '[]'::jsonb,
			'["work.ticket.created.engineering.todo"]'::jsonb,
			NULL, 'active'
		FROM departments d
		WHERE d.slug = 'engineering'
		  AND NOT EXISTS (
		    SELECT 1 FROM agents WHERE department_id = d.id AND role_slug = 'engineer'
		  )
	`); err != nil {
		t.Fatalf("re-seed agent: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug = 'engineering'`).Scan(&deptID); err != nil {
		t.Fatalf("lookup engineering department: %v", err)
	}

	q := store.New(pool)
	cache, err := agents.NewCache(ctx, q)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if cache.Len() < 1 {
		t.Fatalf("cache Len = %d; want at least 1 (the engineer seed)", cache.Len())
	}

	a, err := cache.GetForDepartmentAndRole(ctx, deptID, "engineer")
	if err != nil {
		t.Fatalf("GetForDepartmentAndRole(engineer): %v", err)
	}
	if a.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q; want claude-haiku-4-5-20251001", a.Model)
	}
	if len(a.AgentMD) < 10 {
		t.Errorf("AgentMD length = %d; want non-trivial seed text", len(a.AgentMD))
	}
	if len(a.ListensFor) != 1 || a.ListensFor[0] != "work.ticket.created.engineering.todo" {
		t.Errorf("ListensFor = %v; want [work.ticket.created.engineering.todo]", a.ListensFor)
	}
	if a.Role != "engineer" {
		t.Errorf("Role = %q; want engineer", a.Role)
	}
}
