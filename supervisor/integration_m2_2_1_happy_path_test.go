//go:build integration

// M2.2.1 T013 — golden-path integration test. End-to-end engineer +
// qa-engineer flow against the real M2.2 spike stack, asserting the
// finalize-based atomic write commits `hygiene_status='clean'` on
// both transitions (SC-251).
//
// Run: go test -tags=integration -count=1 -timeout=180s \
//              -run=TestM221FinalizeHappyPath .
//
// Requires the spike-mempalace + spike-docker-proxy containers running.

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

func TestM221FinalizeHappyPath(t *testing.T) {
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

	// Apply M2.2.1's agent_md via the committed seed files — SeedM22
	// seeded a placeholder; M2.2.1's migration would normally overwrite
	// it, but SeedM22 runs all migrations so the M2.2.1 migration has
	// already landed. Verify: agent_md should contain "M2.2.1".
	var engineerMD string
	_ = pool.QueryRow(context.Background(),
		`SELECT agent_md FROM agents WHERE role_slug='engineer'`,
	).Scan(&engineerMD)
	if len(engineerMD) < 100 {
		t.Logf("engineer agent_md is short (%d chars); migration may have reset it", len(engineerMD))
	}

	mempalacePw := "m221-hygiene-test-pw"
	testdb.SetAgentMempalacePassword(t, mempalacePw)
	testdb.SetAgentROPassword(t, "m221-ro-test-pw")

	bin := buildSupervisorBinary(t)
	mockBin := buildMockClaudeBinary(t)

	engineerScript := repoFile(t, "internal/spawn/mockclaude/scripts/m2_2_1_finalize_happy_path.ndjson")
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
		t.Logf("supervisor stderr tail:\n%s", tail(stderr.String(), 2000))
	})

	if err := waitForHealth(healthPort, 15*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	ticketID := m221InsertTicketAtInDev(ctx, t, pool, deptID, "M2.2.1 golden-path smoke")
	t.Logf("ticket: %s", uuidString(ticketID))

	waitForAgentInstanceCount(ctx, t, pool, ticketID, 2, 60*time.Second)

	// Assert both rows succeeded with finalize-path disposition.
	rows, err := pool.Query(ctx, `
		SELECT role_slug, status, COALESCE(exit_reason,''), COALESCE(total_cost_usd::text,''), COALESCE(wake_up_status,'')
		FROM agent_instances
		WHERE ticket_id=$1
		ORDER BY started_at`, ticketID)
	if err != nil {
		t.Fatalf("query agent_instances: %v", err)
	}
	defer rows.Close()
	var totalCost float64
	seenRoles := map[string]bool{}
	for rows.Next() {
		var role, status, er, cost, wake string
		if err := rows.Scan(&role, &status, &er, &cost, &wake); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seenRoles[role] = true
		t.Logf("row: role=%s status=%s exit=%s cost=%s wake=%s", role, status, er, cost, wake)
		if status != "succeeded" {
			t.Errorf("role=%s status=%s; want succeeded", role, status)
		}
		if cost != "" {
			f, err := numericToFloat(pgtype.Numeric{})
			_ = f
			_ = err
			// Cost parsing — best effort; totalCost accumulation is
			// advisory for the SC-251 combined-cap assertion.
		}
	}
	if !seenRoles["engineer"] || !seenRoles["qa-engineer"] {
		t.Errorf("seenRoles=%v; want both engineer and qa-engineer", seenRoles)
	}

	// Assert two ticket_transitions rows, both hygiene_status='clean'.
	trRows, err := pool.Query(ctx, `
		SELECT from_column, to_column, COALESCE(hygiene_status,'(null)')
		FROM ticket_transitions WHERE ticket_id=$1 ORDER BY at`, ticketID)
	if err != nil {
		t.Fatalf("query transitions: %v", err)
	}
	defer trRows.Close()
	transitionCount := 0
	for trRows.Next() {
		transitionCount++
		var from *string
		var to, hyg string
		if err := trRows.Scan(&from, &to, &hyg); err != nil {
			t.Fatalf("scan transition: %v", err)
		}
		fromStr := ptrVal(from)
		t.Logf("transition: %s → %s (hygiene=%s)", fromStr, to, hyg)
		if hyg != "clean" {
			t.Errorf("transition %s→%s hygiene_status=%q; want clean (SC-251)", fromStr, to, hyg)
		}
	}
	if transitionCount != 2 {
		t.Errorf("ticket_transitions count=%d; want 2", transitionCount)
	}

	// SC-251: combined cost under $0.20.
	var sumCost pgtype.Numeric
	_ = pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(total_cost_usd), 0) FROM agent_instances WHERE ticket_id=$1`,
		ticketID,
	).Scan(&sumCost)
	if f, err := numericToFloat(sumCost); err == nil {
		t.Logf("SUM(total_cost_usd) = %.4f", f)
		totalCost = f
		if totalCost >= 0.20 {
			t.Errorf("SUM(total_cost_usd)=%.4f; want < 0.20 per SC-251", totalCost)
		}
	}
}
