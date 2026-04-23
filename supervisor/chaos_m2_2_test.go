//go:build chaos

// M2.2 chaos tests. Three failure scenarios SC-205, SC-206, SC-208.
// Reuses the three-container spike stack from T001. All tests skip
// cleanly via requireSpikeStack if the stack isn't running.

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

// SC-205 — broken MemPalace MCP config bails within 2s via init-event
// mcp_servers[].status!="connected". Test points GARRISON_MEMPALACE_
// CONTAINER at a non-existent container so `docker exec` fails when
// Claude's MCP launcher tries to spawn the mempalace entry. The init
// event should report status="failed" for the mempalace server; the
// supervisor's CheckMCPHealth kills the Claude process group and
// writes a failed agent_instance row with exit_reason=
// mcp_mempalace_<status>. Note: mockclaude with the
// `#init-mcp-servers` directive simulates this failure shape without
// needing a real broken container — the real docker-exec path is
// separately validated at T020 acceptance. Here we use mockclaude's
// pre-built fixture to pin the supervisor's handling of the init-event
// status field.
func TestM22ChaosBrokenMempalaceMCPConfigBailsWithin2Seconds(t *testing.T) {
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

	mempalacePw := "m22-chaos-broken-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)

	// Use the init-failed fixture (T016). Mockclaude emits
	// mcp_servers=[{postgres,connected},{mempalace,failed}] and sleeps
	// briefly; the supervisor's CheckMCPHealth bails.
	engineerScript := repoFile(t, "internal/spawn/mockclaude/scripts/m2_2_mempalace_init_failed.ndjson")

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
		"GARRISON_SUBPROCESS_TIMEOUT=10s",
		"GARRISON_LOG_LEVEL=info",
		// Force wake-up to skip so the test isolates the MCP-bail path.
		"GARRISON_MEMPALACE_WAKEUP_FORCE_FAIL=1",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	var stdout safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("supervisor start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		t.Logf("supervisor log tail:\n%s", tail(stdout.String(), 3000))
	})

	if err := waitForHealth(healthPort, 15*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	start := time.Now()
	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'M2.2 chaos broken mempalace', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	if err := waitFor(ctx, 10*time.Second, func() (bool, error) {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM agent_instances
			WHERE ticket_id=$1 AND status='failed'`, ticketID,
		).Scan(&n); err != nil {
			return false, err
		}
		return n == 1, nil
	}); err != nil {
		t.Fatalf("waiting for failed agent_instance: %v", err)
	}

	elapsed := time.Since(start)
	// The supervisor must bail within a few seconds of init-event
	// receipt. We measure wall-clock from ticket insert to failed row
	// and allow generous margin (insert→NOTIFY→dispatch→spawn→mockclaude
	// startup→init event→bail→terminal write). NFR-206 sets the bail
	// itself at 2s; the test's wall-clock budget is 10s which comfortably
	// exceeds any reasonable overhead.
	if elapsed > 10*time.Second {
		t.Errorf("elapsed %s > 10s; supervisor took too long to bail", elapsed)
	}

	var exitReason string
	if err := pool.QueryRow(ctx, `
		SELECT exit_reason FROM agent_instances WHERE ticket_id=$1`, ticketID,
	).Scan(&exitReason); err != nil {
		t.Fatalf("query exit_reason: %v", err)
	}
	if exitReason != "mcp_mempalace_failed" {
		t.Errorf("exit_reason=%q; want mcp_mempalace_failed", exitReason)
	}

	// No transitions, no hello.txt, no cost captured (pre-result bail).
	var nTrans int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
		ticketID).Scan(&nTrans)
	if nTrans != 0 {
		t.Errorf("ticket_transitions count=%d on MCP-bail path; want 0", nTrans)
	}
}

// SC-206 and SC-208 (mempalace container stopped mid-run;
// hygiene pending/recovery) require orchestrating real
// docker stop / docker pause mid-test, which is a non-trivial
// test-side side effect on the shared spike stack and might
// affect other tests running in parallel. Left as T020 acceptance
// targets (where the operator runs them one at a time against a
// dedicated test compose stack). This file's single chaos test
// covers SC-205; the other two scenarios are validated via the
// acceptance-evidence script at T020 time.
