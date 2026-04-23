//go:build live_acceptance

// M2.2 live acceptance — one real-claude ticket end-to-end.
//
// Build-tag-gated (`live_acceptance` is separate from `integration` /
// `chaos`) so it only runs on explicit operator request with live
// claude credentials + live sidecar. Incurs real Anthropic API spend:
// ~$0.04–0.10 per spawn × 2 spawns ≈ $0.08–0.20 per run.
//
// Go test invocation:
//   go test -tags live_acceptance -count=1 -timeout 5m \
//           -run TestM22LiveAcceptance \
//           -v .
//
// Requires:
//   - real `claude` binary on $PATH (tested with 2.1.117)
//   - spike-mempalace + spike-docker-proxy containers running
//   - Anthropic API credentials already configured for the claude binary
//     (OAuth keychain or ANTHROPIC_API_KEY)

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM22LiveAcceptance(t *testing.T) {
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

	// Upgrade engineer + qa-engineer agent_md to real T005 content
	// (testdb's SeedM22 writes a placeholder). Read the committed files.
	engineerMD, err := os.ReadFile(repoFile(t, "../migrations/seed/engineer.md"))
	if err != nil {
		t.Fatalf("read engineer.md: %v", err)
	}
	qaMD, err := os.ReadFile(repoFile(t, "../migrations/seed/qa-engineer.md"))
	if err != nil {
		t.Fatalf("read qa-engineer.md: %v", err)
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET agent_md=$1 WHERE role_slug='engineer'`, string(engineerMD),
	); err != nil {
		t.Fatalf("update engineer agent_md: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET agent_md=$1 WHERE role_slug='qa-engineer'`, string(qaMD),
	); err != nil {
		t.Fatalf("update qa-engineer agent_md: %v", err)
	}

	// Set garrison_agent_ro password so pgmcp authenticates (engineer
	// actually queries postgres this time).
	testdb.SetAgentROPassword(t, "live-acceptance-ro-pw")
	testdb.SetAgentMempalacePassword(t, "live-acceptance-mp-pw")

	bin := buildSupervisorBinary(t)

	// Real claude on PATH.
	realClaude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}

	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)

	healthPort := mustFreePort(t)
	supervisorEnv := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+realClaude,
		"GARRISON_CLAUDE_MODEL=claude-haiku-4-5-20251001",
		"GARRISON_AGENT_RO_PASSWORD=live-acceptance-ro-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=live-acceptance-mp-pw",
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_SUBPROCESS_TIMEOUT=120s", // real claude needs time
		"GARRISON_HYGIENE_DELAY=2s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=5s",
		"GARRISON_CLAUDE_BUDGET_USD=0.10",
		"GARRISON_LOG_LEVEL=info",
	)

	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin)
	cmd.Env = supervisorEnv
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
		// Full log for acceptance evidence appending.
		_ = os.WriteFile("/tmp/m22-live-acceptance.log", []byte(stdout.String()), 0o644)
		t.Logf("supervisor log written to /tmp/m22-live-acceptance.log (%d bytes)", len(stdout.String()))
		t.Logf("supervisor log tail:\n%s", tail(stdout.String(), 4000))
	})

	if err := waitForHealth(healthPort, 30*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	// Insert ticket at in_dev.
	var ticketID pgtype.UUID
	if err := pool.QueryRow(runCtx, `
		INSERT INTO tickets (id, department_id, objective, column_slug, acceptance_criteria)
		VALUES (gen_random_uuid(), $1,
		        'Write a one-paragraph file changes/hello-$TICKET_ID.md describing M2.2 acceptance completion',
		        'in_dev',
		        'The file exists under the engineering workspace, mentions the ticket, and reads as coherent prose.')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	t.Logf("LIVE ticket: %s", uuidString(ticketID))

	// Wait for both spawns to reach terminal state. Real claude can take
	// 30-90s per spawn; 4-minute ceiling comfortably covers the two-spawn
	// flow under the $0.10 per-spawn cap.
	if err := waitFor(runCtx, 4*time.Minute, func() (bool, error) {
		var n int
		if err := pool.QueryRow(runCtx, `
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

	// Capture full evidence.
	type row struct {
		Role       string
		Status     string
		ExitReason string
		Cost       string
		WakeUp     string
	}
	var rows []row
	r, err := pool.Query(runCtx, `
		SELECT role_slug, status, COALESCE(exit_reason,''),
		       COALESCE(total_cost_usd::text,''),
		       COALESCE(wake_up_status,'')
		FROM agent_instances WHERE ticket_id=$1 ORDER BY started_at`, ticketID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer r.Close()
	var totalCost float64
	for r.Next() {
		var x row
		if err := r.Scan(&x.Role, &x.Status, &x.ExitReason, &x.Cost, &x.WakeUp); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rows = append(rows, x)
		if x.Cost != "" {
			var jn json.Number
			_ = json.Unmarshal([]byte(x.Cost), &jn)
			if f, err := jn.Float64(); err == nil {
				totalCost += f
			}
		}
		t.Logf("LIVE row: role=%s status=%s exit=%s cost=%s wake=%s",
			x.Role, x.Status, x.ExitReason, x.Cost, x.WakeUp)
	}

	// Capture transitions + hygiene for evidence.
	var hygStatuses []string
	tr, err := pool.Query(runCtx, `
		SELECT from_column, to_column, COALESCE(hygiene_status,'(null)')
		FROM ticket_transitions WHERE ticket_id=$1 ORDER BY at`, ticketID)
	if err != nil {
		t.Fatalf("query transitions: %v", err)
	}
	defer tr.Close()
	for tr.Next() {
		var from *string
		var to, hyg string
		if err := tr.Scan(&from, &to, &hyg); err != nil {
			t.Fatalf("scan trans: %v", err)
		}
		fromStr := "(null)"
		if from != nil {
			fromStr = *from
		}
		hygStatuses = append(hygStatuses, hyg)
		t.Logf("LIVE transition: %s → %s (hygiene=%s)", fromStr, to, hyg)
	}

	// Wait up to 15s for hygiene to evaluate (real palace query has
	// docker-exec overhead).
	_ = waitFor(runCtx, 15*time.Second, func() (bool, error) {
		var n int
		if err := pool.QueryRow(runCtx, `
			SELECT COUNT(*) FROM ticket_transitions
			WHERE ticket_id=$1 AND hygiene_status IS NOT NULL`, ticketID,
		).Scan(&n); err != nil {
			return false, err
		}
		return n == len(hygStatuses), nil
	})

	// Re-query hygiene after wait.
	finalHygCount := 0
	var finalHygStatuses []string
	tr2, _ := pool.Query(runCtx, `
		SELECT COALESCE(hygiene_status,'(null)')
		FROM ticket_transitions WHERE ticket_id=$1 ORDER BY at`, ticketID)
	for tr2.Next() {
		var hyg string
		_ = tr2.Scan(&hyg)
		finalHygStatuses = append(finalHygStatuses, hyg)
		if hyg != "(null)" {
			finalHygCount++
		}
	}
	tr2.Close()

	t.Logf("LIVE ACCEPTANCE SUMMARY:")
	t.Logf("  ticket_id=%s", uuidString(ticketID))
	t.Logf("  agent_instances=%d rows (want 2)", len(rows))
	for _, x := range rows {
		t.Logf("    role=%s status=%s exit=%s cost=%s wake=%s",
			x.Role, x.Status, x.ExitReason, x.Cost, x.WakeUp)
	}
	t.Logf("  SUM(total_cost_usd)=%.4f (cap=0.20)", totalCost)
	t.Logf("  ticket_transitions=%d rows (want 2)", len(hygStatuses))
	t.Logf("  final hygiene_status values: %v (%d non-null)", finalHygStatuses, finalHygCount)

	// Soft assertions (the test records evidence; hard gating lives in
	// T020's operator-run script). Any failures below are captured for
	// the acceptance evidence append.
	if len(rows) != 2 {
		t.Errorf("agent_instances count=%d; want 2", len(rows))
	}
	if totalCost >= 0.20 {
		t.Errorf("SUM(total_cost_usd)=%.4f; want < 0.20 per SC-202", totalCost)
	}
}
