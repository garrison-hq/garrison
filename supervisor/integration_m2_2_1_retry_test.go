//go:build integration

// M2.2.1 T014 — retry integration tests.
//
// Three cases:
//   - TestM221FinalizeRetryThenSuccess: 2 schema failures + 1 valid →
//     hygiene_status=clean, transition commits (SC-252)
//   - TestM221FinalizeFailsAfterThreeRetries: 3 schema failures →
//     exit_reason=finalize_invalid, hygiene_status=finalize_failed,
//     no transition row (SC-253)
//   - TestM221FinalizeBudgetExhaustedDuringRetry: 2 fails + budget
//     terminal → exit_reason=budget_exceeded (wins over finalize_invalid),
//     hygiene_status=finalize_failed per SC-258

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
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestM221FinalizeRetryThenSuccess — SC-252.
func TestM221FinalizeRetryThenSuccess(t *testing.T) {
	m221RunFixture(t, "m2_2_1_finalize_retry_then_success.ndjson",
		retryThenSuccessAssertions)
}

// TestM221FinalizeFailsAfterThreeRetries — SC-253.
func TestM221FinalizeFailsAfterThreeRetries(t *testing.T) {
	m221RunFixture(t, "m2_2_1_finalize_retry_exhausted.ndjson",
		retryExhaustedAssertions)
}

// TestM221FinalizeBudgetExhaustedDuringRetry — SC-258.
func TestM221FinalizeBudgetExhaustedDuringRetry(t *testing.T) {
	m221RunFixture(t, "m2_2_1_finalize_retry_budget_exhausted.ndjson",
		budgetExhaustedAssertions)
}

// m221RunFixture wires up the supervisor with the named engineer
// fixture and hands the pool+ticket to the per-test assertion closure.
// Body of this helper is inlined from T013's pattern; extracting
// reduces the T014/T015 files to one closure each.
func m221RunFixture(t *testing.T, fixtureName string, assertFn func(*testing.T, context.Context, *pgxpool.Pool, pgtype.UUID)) {
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
	testdb.SetAgentMempalacePassword(t, "m221-test-pw")
	testdb.SetAgentROPassword(t, "m221-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)

	engineerScript := repoFile(t, "internal/spawn/mockclaude/scripts/"+fixtureName)
	// QA script uses happy-path (these tests exercise the engineer-side
	// failure only; qa never spawns because no transition lands).
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
		"GARRISON_AGENT_RO_PASSWORD=m221-ro-test-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=m221-test-pw",
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_MOCK_CLAUDE_SCRIPT_ENGINEER="+engineerScript,
		"GARRISON_MOCK_CLAUDE_SCRIPT_QA_ENGINEER="+qaScript,
		"GARRISON_SUBPROCESS_TIMEOUT=30s",
		"GARRISON_HYGIENE_DELAY=1s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=3s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=30s",
		"GARRISON_CLAUDE_BUDGET_USD=0.10",
		"GARRISON_LOG_LEVEL=info",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	ticketID := m221InsertTicketAtInDev(ctx, t, pool, deptID, "M2.2.1 retry fixture: "+fixtureName)
	t.Logf("ticket: %s", uuidString(ticketID))

	// Wait for at least 1 agent_instance row to reach terminal state.
	waitForAgentInstanceCount(ctx, t, pool, ticketID, 1, 60*time.Second)

	assertFn(t, ctx, pool, ticketID)
}

func retryThenSuccessAssertions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) {
	var status, exitReason, hygStatus string
	_ = pool.QueryRow(ctx, `
		SELECT ai.status, COALESCE(ai.exit_reason,''), COALESCE(tt.hygiene_status,'(null)')
		FROM agent_instances ai
		JOIN ticket_transitions tt ON tt.triggered_by_agent_instance_id = ai.id
		WHERE ai.ticket_id=$1 AND ai.role_slug='engineer'`,
		ticketID,
	).Scan(&status, &exitReason, &hygStatus)
	if status != "succeeded" {
		t.Errorf("status=%q; want succeeded (SC-252)", status)
	}
	if hygStatus != "clean" {
		t.Errorf("hygiene_status=%q; want clean", hygStatus)
	}
}

func retryExhaustedAssertions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) {
	var status, exitReason string
	_ = pool.QueryRow(ctx, `
		SELECT status, COALESCE(exit_reason,'')
		FROM agent_instances
		WHERE ticket_id=$1 AND role_slug='engineer'`,
		ticketID,
	).Scan(&status, &exitReason)
	if status != "failed" {
		t.Errorf("status=%q; want failed (SC-253)", status)
	}
	if exitReason != "finalize_invalid" {
		t.Errorf("exit_reason=%q; want finalize_invalid (SC-253)", exitReason)
	}
	// No ticket_transitions row should exist.
	var n int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
		ticketID,
	).Scan(&n)
	if n != 0 {
		t.Errorf("ticket_transitions rows=%d; want 0 on SC-253 failure path", n)
	}
}

func budgetExhaustedAssertions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) {
	var status, exitReason string
	_ = pool.QueryRow(ctx, `
		SELECT status, COALESCE(exit_reason,'')
		FROM agent_instances
		WHERE ticket_id=$1 AND role_slug='engineer'`,
		ticketID,
	).Scan(&status, &exitReason)
	if status != "failed" {
		t.Errorf("status=%q; want failed (SC-258)", status)
	}
	if exitReason != "budget_exceeded" {
		t.Errorf("exit_reason=%q; want budget_exceeded (budget outranks finalize_invalid per T002)", exitReason)
	}
	var n int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
		ticketID,
	).Scan(&n)
	if n != 0 {
		t.Errorf("ticket_transitions rows=%d; want 0 on budget-exhausted path", n)
	}
}
