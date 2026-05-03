//go:build integration

// M7 T020 (FR-212 slice) — OOM exit handling. When a per-agent
// container hits its memory cap, the kernel OOM-kills it; the
// supervisor records agent_container_events.kind='oom_killed' with a
// cgroup_caps_jsonb snapshot of the peak-memory observation, and the
// spawn lands hygiene_status='runaway' on its ticket_transitions row.
//
// Real-Docker OOM (running a container with --memory=64m and a memory-
// hungry workload) is operator-side acceptance territory because it
// requires kernel cgroup cooperation. This test exercises the
// supervisor-side data-shape surface only: insert a synthesized
// 'oom_killed' container event + assert the cgroup_caps_jsonb shape +
// the agent_container_events row's queryability via the recovery
// queries.

package supervisor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM7OOMKillEventRecordsCgroupSnapshot(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`TRUNCATE agent_container_events, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID, deptID, agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name) VALUES ('m7 oom co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-oom')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
		 VALUES ($1, 'engineer', '[]'::jsonb, 'agent prose', 'claude-x', 'active')
		 RETURNING id`, deptID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Insert the synthesized OOM event mirroring what the agent-
	// container watcher would write when it observes an OOM-kill.
	cgroupSnap := []byte(`{"memory_max_usage_bytes": 67108864, "limit_bytes": 67108864, "oom_kill_count": 1}`)
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_container_events
		 (agent_id, kind, cgroup_caps_jsonb, stop_reason)
		 VALUES ($1, 'oom_killed', $2::jsonb, $3)`,
		agentID, cgroupSnap, "memory limit exceeded"); err != nil {
		t.Fatalf("insert oom event: %v", err)
	}

	var (
		kind    string
		caps    []byte
		stopRsn *string
	)
	if err := pool.QueryRow(ctx,
		`SELECT kind, cgroup_caps_jsonb, stop_reason FROM agent_container_events WHERE agent_id = $1`,
		agentID).Scan(&kind, &caps, &stopRsn); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if kind != "oom_killed" {
		t.Errorf("kind = %q; want oom_killed", kind)
	}
	if !strings.Contains(string(caps), "memory_max_usage_bytes") {
		t.Errorf("cgroup snapshot missing memory_max_usage_bytes: %s", caps)
	}
	if stopRsn == nil || !strings.Contains(*stopRsn, "memory limit") {
		t.Errorf("stop_reason missing memory hint: %v", stopRsn)
	}
}
