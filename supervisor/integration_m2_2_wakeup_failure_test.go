//go:build integration

// M2.2 SC-207 — wake-up failure is non-blocking. Test uses the same
// three-container topology as TestM22EngineerPlusQAHappyPath, but sets
// GARRISON_MEMPALACE_WAKEUP_FORCE_FAIL=1 so mempalace.Wakeup returns
// StatusFailed without attempting docker exec. MCP init still works
// (via the separate docker exec path); the spawn proceeds normally
// and the engineer/qa-engineer succeed with wake_up_status='failed'.

package supervisor_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM22WakeUpFailureIsNonBlocking(t *testing.T) {
	requireSpikeStack(t)

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)
	var deptID pgtype.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM departments WHERE slug='engineering'`,
	).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept id: %v", err)
	}

	mempalacePw := "m22-wakeup-failure-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)

	engineerScript := repoFile(t, "internal/spawn/mockclaude/scripts/m2_2_happy_path.ndjson")
	qaScript := repoFile(t, "internal/spawn/mockclaude/scripts/m2_2_qa_happy_path.ndjson")

	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)

	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+mockBin,
		"GARRISON_AGENT_RO_PASSWORD=ro-unused",
		"GARRISON_AGENT_MEMPALACE_PASSWORD="+mempalacePw,
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_MOCK_CLAUDE_SCRIPT_ENGINEER="+engineerScript,
		"GARRISON_MOCK_CLAUDE_SCRIPT_QA_ENGINEER="+qaScript,
		"GARRISON_SUBPROCESS_TIMEOUT=30s",
		"GARRISON_HYGIENE_DELAY=1s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=3s",
		"GARRISON_LOG_LEVEL=info",
		// The load-bearing test hook: force wake-up to return
		// StatusFailed without attempting docker exec. Isolates SC-207.
		"GARRISON_MEMPALACE_WAKEUP_FORCE_FAIL=1",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	var stdout, stderr safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("supervisor start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		t.Logf("supervisor stdout tail:\n%s", tail(stdout.String(), 3000))
	})

	if err := waitForHealth(healthPort, 15*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'M2.2 wake-up-failure smoke', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	if err := waitFor(ctx, 40*time.Second, func() (bool, error) {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM agent_instances
			WHERE ticket_id=$1 AND status IN ('succeeded','failed','timeout')`,
			ticketID,
		).Scan(&n); err != nil {
			return false, err
		}
		return n >= 2, nil
	}); err != nil {
		t.Fatalf("waiting for two terminal agent_instances: %v", err)
	}

	// Both rows should have wake_up_status='failed'.
	rows, err := pool.Query(ctx, `
		SELECT role_slug, status, exit_reason, wake_up_status
		FROM agent_instances
		WHERE ticket_id=$1 ORDER BY started_at`, ticketID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role, status, exitReason string
		var wakeUp *string
		if err := rows.Scan(&role, &status, &exitReason, &wakeUp); err != nil {
			t.Fatalf("scan: %v", err)
		}
		roles = append(roles, role)
		if status != "succeeded" {
			t.Errorf("role=%s status=%s; want succeeded (spawn must still proceed on wake-up fail)", role, status)
		}
		if exitReason != "completed" {
			t.Errorf("role=%s exit=%s; want completed", role, exitReason)
		}
		if wakeUp == nil || *wakeUp != "failed" {
			t.Errorf("role=%s wake_up_status=%v; want 'failed' under FORCE_FAIL", role, ptrVal(wakeUp))
		}
	}
	if len(roles) != 2 {
		t.Fatalf("expected 2 agent_instances rows; got %d", len(roles))
	}

	// Two transitions still recorded — wake-up failure doesn't block
	// the workflow per SC-207.
	var nTrans int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
		ticketID,
	).Scan(&nTrans); err != nil {
		t.Fatalf("query transitions: %v", err)
	}
	if nTrans != 2 {
		t.Errorf("expected 2 ticket_transitions; got %d (SC-207: wake-up failure must not block the workflow)", nTrans)
	}
}
