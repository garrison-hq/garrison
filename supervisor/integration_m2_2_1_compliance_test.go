//go:build live_acceptance

// M2.2.1 T017 — compliance live-acceptance test (SC-261).
// TestM221ComplianceModelIndependent runs the happy-path scenario
// twice: once with claude-haiku-4-5-20251001, once with
// claude-opus-4-7. Compares outcomes.
//
// Build-tag-gated (live_acceptance) so this only runs on explicit
// operator invocation with real Anthropic credentials. Expected spend:
// ~$0.05-0.20 per run × 2 models × 2 agents per run ≈ $0.20-$0.80.
//
// Run:
//   go test -tags=live_acceptance -count=1 -timeout=15m \
//           -run=TestM221ComplianceModelIndependent .
//
// Requires:
//   - real `claude` binary on $PATH (2.1.117)
//   - spike-mempalace + spike-docker-proxy containers running
//   - Anthropic credentials configured for the claude binary

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
	"github.com/jackc/pgx/v5/pgxpool"
)

type runResult struct {
	Model           string
	HygieneStatuses []string
	AgentInstances  int
	ExitReasons     []string
	CombinedCostUSD float64
	Error           string
}

func TestM221ComplianceModelIndependent(t *testing.T) {
	requireSpikeStack(t)

	realClaude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}

	models := []string{
		"claude-haiku-4-5-20251001",
		"claude-opus-4-7",
	}
	results := make([]runResult, 0, len(models))

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			res := runComplianceOnce(t, realClaude, model)
			results = append(results, res)
			t.Logf("COMPLIANCE RUN model=%s instances=%d hygiene=%v exit=%v cost=$%.4f",
				res.Model, res.AgentInstances, res.HygieneStatuses, res.ExitReasons, res.CombinedCostUSD)
			if res.Error != "" {
				t.Errorf("run failed: %s", res.Error)
			}
			if res.AgentInstances != 2 {
				t.Errorf("agent_instances=%d; want 2 (SC-261)", res.AgentInstances)
			}
			for _, s := range res.HygieneStatuses {
				if s != "clean" {
					t.Errorf("hygiene_status=%q; want clean (SC-261)", s)
				}
			}
			if res.CombinedCostUSD >= 0.20 {
				t.Logf("WARN: combined cost=$%.4f exceeds soft SC-251 cap of $0.20", res.CombinedCostUSD)
			}
		})
	}

	// Cross-model comparison (SC-261 headline).
	if len(results) == 2 {
		if results[0].AgentInstances != results[1].AgentInstances {
			t.Errorf("SC-261: agent_instances counts differ: %s=%d %s=%d",
				results[0].Model, results[0].AgentInstances,
				results[1].Model, results[1].AgentInstances)
		}
		if len(results[0].HygieneStatuses) != len(results[1].HygieneStatuses) {
			t.Errorf("SC-261: hygiene-status counts differ: %s=%d %s=%d",
				results[0].Model, len(results[0].HygieneStatuses),
				results[1].Model, len(results[1].HygieneStatuses))
		}
		t.Logf("SC-261 cross-model summary: haiku=%v opus=%v",
			results[0].HygieneStatuses, results[1].HygieneStatuses)
	}
}

func runComplianceOnce(t *testing.T, claudeBin, model string) runResult {
	t.Helper()
	res := runResult{Model: model}

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	var deptID pgtype.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM departments WHERE slug='engineering'`,
	).Scan(&deptID); err != nil {
		res.Error = fmt.Sprintf("lookup dept: %v", err)
		return res
	}
	// Override the seeded agents.model so the spawn path uses the
	// requested model (spawn.go prefers agent.Model over
	// deps.ClaudeModel when both are set; SeedM22 hard-codes
	// claude-haiku-4-5-20251001 which would override the env).
	if _, err := pool.Exec(context.Background(),
		`UPDATE agents SET model = $1 WHERE role_slug IN ('engineer','qa-engineer')`,
		model,
	); err != nil {
		res.Error = fmt.Sprintf("override agent model: %v", err)
		return res
	}
	testdb.SetAgentMempalacePassword(t, "live-compliance-mp-pw")
	testdb.SetAgentROPassword(t, "live-compliance-ro-pw")

	bin := buildSupervisorBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)

	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+claudeBin,
		"GARRISON_CLAUDE_MODEL="+model,
		"GARRISON_AGENT_RO_PASSWORD=live-compliance-ro-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=live-compliance-mp-pw",
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH=/palace",
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_SUBPROCESS_TIMEOUT=180s",
		"GARRISON_HYGIENE_DELAY=2s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=5s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=30s",
		"GARRISON_CLAUDE_BUDGET_USD=0.20",
		"GARRISON_LOG_LEVEL=info",
	)

	runCtx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	var stdout, stderr safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		res.Error = fmt.Sprintf("start supervisor: %v", err)
		return res
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		logPath := fmt.Sprintf("/tmp/m221-compliance-%s.log", model)
		_ = os.WriteFile(logPath, []byte(stdout.String()), 0o644)
		t.Logf("supervisor log (%s): %d bytes → %s", model, len(stdout.String()), logPath)
	})

	if err := waitForHealth(healthPort, 30*time.Second); err != nil {
		res.Error = fmt.Sprintf("supervisor health: %v", err)
		return res
	}

	// Insert one ticket at in_dev with a substantive objective so claude
	// has something to work with.
	var ticketID pgtype.UUID
	if err := pool.QueryRow(runCtx, `
		INSERT INTO tickets (id, department_id, objective, column_slug, acceptance_criteria)
		VALUES (gen_random_uuid(), $1,
		        'Write a short markdown file changes/hello-<ticket_id>.md with one paragraph describing M2.2.1 completion.',
		        'in_dev',
		        'File exists under the engineering workspace, mentions the ticket id, reads as coherent prose.')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		res.Error = fmt.Sprintf("insert ticket: %v", err)
		return res
	}
	t.Logf("[%s] ticket: %s", model, uuidString(ticketID))

	// Wait up to 6 minutes for two terminal agent_instances.
	if err := waitFor(runCtx, 6*time.Minute, func() (bool, error) {
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
		res.Error = fmt.Sprintf("waiting for terminal rows: %v", err)
		// Continue to best-effort observation.
	}

	// Collect evidence.
	rows, err := pool.Query(runCtx, `
		SELECT role_slug, status, COALESCE(exit_reason,''),
		       COALESCE(total_cost_usd::text,'')
		FROM agent_instances
		WHERE ticket_id=$1 ORDER BY started_at`, ticketID)
	if err != nil {
		res.Error = fmt.Sprintf("query rows: %v", err)
		return res
	}
	defer rows.Close()
	for rows.Next() {
		var role, status, er, cost string
		if err := rows.Scan(&role, &status, &er, &cost); err != nil {
			continue
		}
		res.AgentInstances++
		res.ExitReasons = append(res.ExitReasons, er)
		if cost != "" {
			var jn json.Number
			if err := json.Unmarshal([]byte(cost), &jn); err == nil {
				if f, err := jn.Float64(); err == nil {
					res.CombinedCostUSD += f
				}
			}
		}
		t.Logf("[%s] role=%s status=%s exit=%s cost=%s", model, role, status, er, cost)
	}

	trRows, err := pool.Query(runCtx, `
		SELECT from_column, to_column, COALESCE(hygiene_status,'(null)')
		FROM ticket_transitions WHERE ticket_id=$1 ORDER BY at`, ticketID)
	if err != nil {
		return res
	}
	defer trRows.Close()
	for trRows.Next() {
		var from *string
		var to, hyg string
		if err := trRows.Scan(&from, &to, &hyg); err != nil {
			continue
		}
		res.HygieneStatuses = append(res.HygieneStatuses, hyg)
		fromStr := ptrVal(from)
		t.Logf("[%s] transition: %s → %s hygiene=%s", model, fromStr, to, hyg)
	}
	return res
}

// unused-import silencer — keeps live_acceptance build tag-compile
// stable even when none of the named symbols is referenced.
var _ = pgxpool.New
