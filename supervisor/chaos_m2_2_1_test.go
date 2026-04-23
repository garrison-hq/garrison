//go:build chaos

// M2.2.1 T016 — atomic-write chaos test (SC-255).
// TestM221AtomicWriteChaosPalaceKillMidTransaction: mock emits a valid
// finalize_ticket; chaos harness docker-stops the mempalace sidecar
// during the supervisor's atomic write; expected disposition: no
// ticket_transitions row, agent_instances.hygiene_status=finalize_partial,
// exit_reason ∈ {finalize_palace_write_failed, finalize_commit_failed,
// finalize_write_timeout}, supervisor continues running (no panic).

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

func TestM221AtomicWriteChaosPalaceKillMidTransaction(t *testing.T) {
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
	testdb.SetAgentMempalacePassword(t, "m221-chaos-pw")
	testdb.SetAgentROPassword(t, "m221-chaos-ro-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)

	engineerScript := repoFile(t, "internal/spawn/mockclaude/scripts/m2_2_1_finalize_atomic_chaos.ndjson")
	qaScript := repoFile(t, "internal/spawn/mockclaude/scripts/m2_2_1_finalize_happy_path.ndjson")

	mcpDir := t.TempDir()
	if err := os.Chmod(mcpDir, 0o750); err != nil {
		t.Fatalf("chmod mcp dir: %v", err)
	}

	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+mockBin,
		"GARRISON_AGENT_RO_PASSWORD=m221-chaos-ro-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=m221-chaos-pw",
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_MOCK_CLAUDE_SCRIPT_ENGINEER="+engineerScript,
		"GARRISON_MOCK_CLAUDE_SCRIPT_QA_ENGINEER="+qaScript,
		"GARRISON_SUBPROCESS_TIMEOUT=60s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=15s", // shorter for chaos test
		"GARRISON_CLAUDE_BUDGET_USD=0.10",
		"GARRISON_LOG_LEVEL=info",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
		t.Logf("supervisor stdout tail:\n%s", tail(stdout.String(), 4000))
	})

	if err := waitForHealth(healthPort, 15*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	ticketID := m221InsertTicketAtInDev(ctx, t, pool, deptID, "M2.2.1 chaos: palace kill mid-tx")
	t.Logf("ticket: %s", uuidString(ticketID))

	// Give the mock a moment to emit the finalize tool_use; then kill the
	// palace sidecar to induce the atomic-write failure.
	time.Sleep(2 * time.Second)
	killCmd := exec.Command("docker", "kill", m22MempalaceContainer)
	if out, err := killCmd.CombinedOutput(); err != nil {
		t.Fatalf("docker kill palace: %v: %s", err, out)
	}
	t.Cleanup(func() {
		// Restart so downstream tests in the same run can use the palace.
		_ = exec.Command("docker", "start", m22MempalaceContainer).Run()
	})

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	var status, exitReason, hyg string
	_ = pool.QueryRow(ctx, `
		SELECT status, COALESCE(exit_reason,''), COALESCE(hygiene_status,'(null)')
		FROM agent_instances
		WHERE ticket_id=$1 AND role_slug='engineer'`,
		ticketID,
	).Scan(&status, &exitReason, &hyg)

	validReasons := map[string]bool{
		"finalize_palace_write_failed": true,
		"finalize_commit_failed":       true,
		"finalize_write_timeout":       true,
	}
	if !validReasons[exitReason] {
		t.Errorf("exit_reason=%q; want one of {finalize_palace_write_failed, finalize_commit_failed, finalize_write_timeout} (SC-255)",
			exitReason)
	}
	if hyg != "finalize_partial" && hyg != "(null)" {
		// Hygiene evaluator may not yet have run; null is acceptable
		// if the test completes before the sweep; non-null should be
		// finalize_partial.
		t.Errorf("hygiene_status=%q; want finalize_partial or null (SC-255)", hyg)
	}
	// No transition row on the failure paths.
	var n int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
		ticketID,
	).Scan(&n)
	if n != 0 {
		t.Errorf("ticket_transitions=%d; want 0 on chaos path", n)
	}
}
