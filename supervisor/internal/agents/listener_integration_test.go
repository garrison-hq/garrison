//go:build integration

package agents_test

import (
	"context"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
)

// TestListenerReceivesAndAppliesAgentsChangedNotify covers the load-bearing
// path from M4 / FR-100 / T014 plan §"Supervisor `internal/agents` cache
// invalidation lifecycle":
//
//  1. supervisor cache pre-loaded
//  2. dashboard emits pg_notify('agents.changed', role_slug)
//  3. listener receives the notification, calls Cache.Reset
//  4. cache reflects the latest agents row state
//
// Approach: open a real Postgres testdb pool, seed the engineering
// department + engineer agent, build the cache, start the listener,
// emit a NOTIFY from a separate connection (mimicking the dashboard's
// editAgent server action), update agents.agent_md so the post-Reset
// cache is observably different from the pre-Reset cache, then assert
// the cache picks up the new value.
func TestListenerReceivesAndAppliesAgentsChangedNotify(t *testing.T) {
	pool := testdb.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Re-seed engineering+engineer (idempotent ON CONFLICT pattern as
	// in agents_integration_test.go).
	if _, err := pool.Exec(ctx, `
		INSERT INTO companies (id, name)
		SELECT gen_random_uuid(), 'test co'
		WHERE NOT EXISTS (SELECT 1 FROM companies)
	`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO departments (id, company_id, slug, name, workspace_path, concurrency_cap)
		SELECT gen_random_uuid(), c.id, 'engineering', 'Engineering', '/tmp/wp', 3
		  FROM companies c
		 WHERE NOT EXISTS (SELECT 1 FROM departments WHERE slug = 'engineering')
		 LIMIT 1
	`); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agents (department_id, role_slug, model, agent_md, listens_for, status)
		SELECT d.id, 'engineer', 'haiku', '# initial', '[]'::jsonb, 'active'
		  FROM departments d
		 WHERE d.slug = 'engineering'
		   AND NOT EXISTS (SELECT 1 FROM agents WHERE role_slug = 'engineer')
		 LIMIT 1
	`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Build the cache.
	queries := store.New(pool)
	cache, err := agents.NewCache(ctx, queries)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if cache.Len() == 0 {
		t.Fatal("expected at least one cached agent")
	}

	// Start the listener.
	if err := agents.StartChangeListener(ctx, pool, cache); err != nil {
		t.Fatalf("StartChangeListener: %v", err)
	}

	// Update the agent's agent_md to a value we can assert on
	// post-reset.
	if _, err := pool.Exec(ctx, `
		UPDATE agents SET agent_md = '# post-reset' WHERE role_slug = 'engineer'
	`); err != nil {
		t.Fatalf("update agent_md: %v", err)
	}

	// Emit the NOTIFY (mimics the dashboard's editAgent server
	// action's pg_notify call).
	if _, err := pool.Exec(ctx, `SELECT pg_notify('agents.changed', 'engineer')`); err != nil {
		t.Fatalf("pg_notify: %v", err)
	}

	// Wait up to 2s for the listener to apply the reset.
	deadline := time.Now().Add(2 * time.Second)
	var observed string
	for time.Now().Before(deadline) {
		// Look up the engineer agent to inspect the cached agent_md.
		// We need to find the dept id for the lookup.
		row := pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug = 'engineering' LIMIT 1`)
		var deptID [16]byte
		var deptIDPg = struct {
			Bytes [16]byte
			Valid bool
		}{}
		_ = row.Scan(&deptID)
		_ = deptIDPg

		// Build the typed dept id for cache lookup.
		// (The cache stores pgtype.UUID; reconstruct.)
		var dept = make([]byte, 16)
		_ = pool.QueryRow(ctx, `SELECT id FROM departments WHERE slug = 'engineering' LIMIT 1`).Scan(&dept)

		// Use direct SQL to read from the cache's perspective
		// instead — query both the DB and the cache and compare.
		// Simpler: query the cache after a short wait.
		time.Sleep(50 * time.Millisecond)

		// Read directly via a cache lookup using the same role.
		var rows []store.Agent
		rows, err = queries.ListActiveAgents(ctx)
		if err != nil {
			t.Fatalf("ListActiveAgents: %v", err)
		}
		if len(rows) > 0 {
			// Find engineer.
			for _, r := range rows {
				if r.RoleSlug == "engineer" {
					a, gerr := cache.GetForDepartmentAndRole(ctx, r.DepartmentID, "engineer")
					if gerr == nil {
						observed = a.AgentMD
						if observed == "# post-reset" {
							return // pass
						}
					}
				}
			}
		}
	}

	t.Fatalf("cache did not pick up updated agent_md within 2s; observed=%q", observed)
}
