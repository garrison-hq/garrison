//go:build experiment

// Post-UUID-fix compliance matrix — 6 iterations × 2 models × Ticket A
// only, stream-json captured on every run, run on top of the three
// pgmcp fixes (envelope wrapper, UUID normalization, agent_instances
// grant). The milestone-closing compliance test for the M2.2.x arc.
//
// Narrow question: across 12 runs (6 per model), how reliably does the
// compliance mechanism produce clean completion on Ticket A now that
// the three pgmcp bugs the triage-hypothesis matrix surfaced are
// fixed?
//
// Build-tag-gated (`experiment`) so it never fires via the normal
// integration / live_acceptance suites. Invoke explicitly:
//
//   go test -tags=experiment -count=1 -timeout=2h -v \
//           -run=TestPostUUIDFixMatrix .
//
// Requires:
//   - real `claude` on $PATH
//   - spike-mempalace + spike-docker-proxy containers running
//   - Anthropic credentials configured for the claude binary
//   - experiment-results/tools/tee-claude.sh executable
//
// Budget safety: hard-caps combined agent_instances.total_cost_usd at
// $3.80 across all 12 runs. If the running total crosses the cap the
// remaining iterations are skipped (partial results still written).
// Per-run cap is set via GARRISON_CLAUDE_BUDGET_USD=0.30.
//
// Produces (relative to the worktree root's experiment-results/):
//   - post-uuid-fix-run-NN-<model>.log            raw supervisor log
//   - stream-json/matrix-NN-<model>/*.jsonl       claude stdout capture
//   - post-uuid-fix-matrix-raw.md                 12-row table
//   - post-uuid-fix-raw-data.json                 per-run structured dump
//
// The findings document (matrix-post-uuid-fix.md) is written by the
// operator from these outputs; this test only collects data.

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// postFixExperimentRun extends experimentRun with the two columns the
// post-UUID-fix brief requires on top of the triage-hypothesis fields.
// Kept as a separate struct so the triage harness doesn't drift.
type postFixExperimentRun struct {
	experimentRun

	// FileLocation: where the changelog file landed on disk.
	//   "workspace"  — <workspace>/changes/hello-<ticket_id>.md
	//   "host_home"  — ~/changes/hello-<ticket_id>.md (sandbox escape)
	//   "missing"    — no file with the expected name in either place
	FileLocation string `json:"file_location"`

	// FileContentValid: true iff the file exists AND is ≥50 chars
	// AND substring-contains the ticket UUID AND lacks obvious
	// placeholder strings. Past-tense-ness is NOT checked here; that's
	// a qualitative assertion the findings author verifies manually.
	FileContentValid bool `json:"file_content_valid"`

	// FileBytes: size of the file at its resolved location, for
	// operator debugging. 0 if missing.
	FileBytes int64 `json:"file_bytes"`

	// Errors: miscellaneous contract-violation observations (empty if
	// none). Populated from the supervisor log: pgmcp column-not-
	// exist, permission-denied, finalize state-rejection hint text.
	Errors string `json:"errors,omitempty"`
}

const (
	postFixPerRunBudgetUSD = 0.30
	postFixGrandBudgetUSD  = 3.80
)

// TestPostUUIDFixMatrix — runs 6 haiku + 6 opus on Ticket A only,
// with tee-claude.sh capturing stream-json on every run. Sequential:
// parallel runs would confound palace state between runs even with
// distinct palace paths (shared sidecar, shared docker sock).
func TestPostUUIDFixMatrix(t *testing.T) {
	requireSpikeStack(t)

	realClaude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	supervisorDir := filepath.Dir(thisFile)
	wrapper := filepath.Join(supervisorDir, "..", "experiment-results", "tools", "tee-claude.sh")
	if st, err := os.Stat(wrapper); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("tee wrapper missing / not executable at %s: %v", wrapper, err)
	}

	engineerSeed, err := os.ReadFile(repoFile(t, "../migrations/seed/engineer.md"))
	if err != nil {
		t.Fatalf("read engineer.md: %v", err)
	}
	qaSeed, err := os.ReadFile(repoFile(t, "../migrations/seed/qa-engineer.md"))
	if err != nil {
		t.Fatalf("read qa-engineer.md: %v", err)
	}

	outDir := repoFile(t, "../experiment-results")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir experiment-results: %v", err)
	}
	streamRoot := filepath.Join(outDir, "stream-json")
	if err := os.MkdirAll(streamRoot, 0o755); err != nil {
		t.Fatalf("mkdir stream-json: %v", err)
	}

	// Matrix order: haiku-first, then opus. Avoids interleaving that
	// would slow up-front operator inspection of early results.
	models := []struct {
		short string
		full  string
	}{
		{"haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{"opus-4-7", "claude-opus-4-7"},
	}

	runs := make([]postFixExperimentRun, 0, 12)
	var grandCost float64
	index := 0

	for _, m := range models {
		for i := 1; i <= 6; i++ {
			index++
			if grandCost > postFixGrandBudgetUSD {
				t.Logf("[post-uuid-fix] BUDGET CAP hit at run=%d grand=$%.4f; "+
					"skipping remaining iterations", index, grandCost)
				runs = append(runs, postFixExperimentRun{
					experimentRun: experimentRun{
						Index:      index,
						Ticket:     "A",
						Model:      m.short,
						RunInGroup: i,
						RunErr:     "skipped: budget cap",
					},
				})
				continue
			}

			caseTeeDir := filepath.Join(streamRoot,
				fmt.Sprintf("matrix-%02d-%s", index, m.short))
			if err := os.MkdirAll(caseTeeDir, 0o755); err != nil {
				t.Fatalf("mkdir case tee dir: %v", err)
			}

			subName := fmt.Sprintf("run%02d_A_%s_iter%d", index, m.short, i)
			var run postFixExperimentRun
			t.Run(subName, func(t *testing.T) {
				run = runOnePostFixIteration(t, postFixIterationInputs{
					Index:        index,
					ModelShort:   m.short,
					ModelFull:    m.full,
					RunInGroup:   i,
					ClaudeWrap:   wrapper,
					ClaudeReal:   realClaude,
					TeeDir:       caseTeeDir,
					EngineerSeed: string(engineerSeed),
					QAEngineerMD: string(qaSeed),
					OutDir:       outDir,
				})
			})
			grandCost += run.TotalCostUSD
			runs = append(runs, run)

			t.Logf("[post-uuid-fix] run=%d model=%s iter=%d "+
				"finalize_called=%v attempt=%s cost=$%.4f cum=$%.4f "+
				"hygiene=%s exit=%s file=%s valid=%v err=%q",
				run.Index, run.Model, run.RunInGroup,
				run.FinalizeCalled, run.FinalizeAttempt, run.TotalCostUSD,
				grandCost, run.HygieneStatuses, run.ExitReasons,
				run.FileLocation, run.FileContentValid, run.RunErr)
		}
	}

	// Always write outputs, even if some rows were budget-skipped.
	if err := writePostFixRawData(filepath.Join(outDir, "post-uuid-fix-raw-data.json"), runs, grandCost); err != nil {
		t.Errorf("write post-uuid-fix-raw-data.json: %v", err)
	}
	if err := writePostFixMatrixMarkdown(filepath.Join(outDir, "post-uuid-fix-matrix-raw.md"), runs, grandCost); err != nil {
		t.Errorf("write post-uuid-fix-matrix-raw.md: %v", err)
	}

	t.Logf("[post-uuid-fix] DONE grand_cost=$%.4f runs=%d (of 12) files in %s",
		grandCost, len(runs), outDir)
}

// ---- per-iteration runner --------------------------------------------

type postFixIterationInputs struct {
	Index        int
	ModelShort   string
	ModelFull    string
	RunInGroup   int
	ClaudeWrap   string // path to the tee wrapper
	ClaudeReal   string // path to the real claude (passed via env)
	TeeDir       string // stream-json/matrix-NN-<model>/
	EngineerSeed string
	QAEngineerMD string
	OutDir       string
}

func runOnePostFixIteration(t *testing.T, in postFixIterationInputs) postFixExperimentRun {
	t.Helper()
	run := postFixExperimentRun{
		experimentRun: experimentRun{
			Index:           in.Index,
			Ticket:          "A",
			Model:           in.ModelShort,
			RunInGroup:      in.RunInGroup,
			FinalizeAttempt: "N/A",
		},
		FileLocation: "missing",
	}

	// Palace isolation: unique path per iteration.
	palacePath := fmt.Sprintf("/palace-post-uuid-fix-%02d-%s",
		in.Index, in.ModelShort)
	if out, err := exec.Command("docker", "exec", m22MempalaceContainer,
		"mkdir", "-p", palacePath,
	).CombinedOutput(); err != nil {
		run.RunErr = fmt.Sprintf("mkdir palace %s: %v: %s", palacePath, err, string(out))
		return run
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", m22MempalaceContainer,
			"rm", "-rf", palacePath).Run()
	})

	pool := testdb.Start(t)
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET agent_md=$1 WHERE role_slug='engineer'`, in.EngineerSeed,
	); err != nil {
		run.RunErr = fmt.Sprintf("update engineer agent_md: %v", err)
		return run
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET agent_md=$1 WHERE role_slug='qa-engineer'`, in.QAEngineerMD,
	); err != nil {
		run.RunErr = fmt.Sprintf("update qa-engineer agent_md: %v", err)
		return run
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET model=$1 WHERE role_slug IN ('engineer','qa-engineer')`,
		in.ModelFull,
	); err != nil {
		run.RunErr = fmt.Sprintf("update agents.model: %v", err)
		return run
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM departments WHERE slug='engineering'`,
	).Scan(&deptID); err != nil {
		run.RunErr = fmt.Sprintf("lookup dept id: %v", err)
		return run
	}

	testdb.SetAgentROPassword(t, "post-uuid-fix-ro-pw")
	testdb.SetAgentMempalacePassword(t, "post-uuid-fix-mp-pw")

	bin := buildSupervisorBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)

	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+in.ClaudeWrap,
		"GARRISON_CLAUDE_MODEL="+in.ModelFull,
		"GARRISON_AGENT_RO_PASSWORD=post-uuid-fix-ro-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=post-uuid-fix-mp-pw",
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH="+palacePath,
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_SUBPROCESS_TIMEOUT=180s",
		"GARRISON_HYGIENE_DELAY=2s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=5s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=30s",
		fmt.Sprintf("GARRISON_CLAUDE_BUDGET_USD=%.2f", postFixPerRunBudgetUSD),
		"GARRISON_LOG_LEVEL=info",
		// Wrapper env — tee-claude.sh reads these to duplicate
		// claude's stdout into per-invocation JSONL captures.
		"CLAUDE_REAL="+in.ClaudeReal,
		"GARRISON_EXPERIMENT_TEE_DIR="+in.TeeDir,
	)

	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	var stdout safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		run.RunErr = fmt.Sprintf("start supervisor: %v", err)
		return run
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		logPath := filepath.Join(in.OutDir,
			fmt.Sprintf("post-uuid-fix-run-%02d-%s.log", in.Index, in.ModelShort))
		_ = os.WriteFile(logPath, []byte(stdout.String()), 0o644)
	})

	if err := waitForHealth(healthPort, 30*time.Second); err != nil {
		run.RunErr = fmt.Sprintf("supervisor health: %v", err)
		return run
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(runCtx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, $2, 'in_dev')
		RETURNING id`, deptID, ticketABody,
	).Scan(&ticketID); err != nil {
		run.RunErr = fmt.Sprintf("insert ticket: %v", err)
		return run
	}
	run.TicketUUID = uuidString(ticketID)

	// Same wait pattern as the main triage-hypothesis matrix.
	settleWindow := 30 * time.Second
	hardCeiling := 10 * time.Minute
	var engineerTerminalAt time.Time
	_ = waitFor(runCtx, hardCeiling, func() (bool, error) {
		var terminalCount, transitions int
		if err := pool.QueryRow(runCtx, `
			SELECT COUNT(*) FROM agent_instances
			WHERE ticket_id=$1 AND status IN ('succeeded','failed','timeout')`,
			ticketID,
		).Scan(&terminalCount); err != nil {
			return false, err
		}
		if terminalCount >= 2 {
			return true, nil
		}
		if err := pool.QueryRow(runCtx, `
			SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
			ticketID,
		).Scan(&transitions); err != nil {
			return false, err
		}
		if terminalCount >= 1 {
			if engineerTerminalAt.IsZero() {
				engineerTerminalAt = time.Now()
			}
			if transitions == 0 && time.Since(engineerTerminalAt) >= settleWindow {
				return true, nil
			}
		}
		return false, nil
	})

	// ---- per-instance evidence ----
	type instRow struct {
		Role        string
		Status      string
		ExitReason  string
		CostUSD     float64
		DurationSec float64
	}
	var instances []instRow
	rows, err := pool.Query(runCtx, `
		SELECT role_slug, status, COALESCE(exit_reason,''),
		       COALESCE(total_cost_usd, 0.0),
		       COALESCE(EXTRACT(EPOCH FROM (finished_at - started_at)), 0.0)
		FROM agent_instances
		WHERE ticket_id=$1 ORDER BY started_at`, ticketID)
	if err != nil {
		run.RunErr = fmt.Sprintf("query agent_instances: %v", err)
		return run
	}
	for rows.Next() {
		var ir instRow
		var cost pgtype.Numeric
		if err := rows.Scan(&ir.Role, &ir.Status, &ir.ExitReason,
			&cost, &ir.DurationSec); err != nil {
			rows.Close()
			run.RunErr = fmt.Sprintf("scan agent_instance: %v", err)
			return run
		}
		if f, cerr := numericToFloat(cost); cerr == nil {
			ir.CostUSD = f
		}
		instances = append(instances, ir)
	}
	rows.Close()

	var exits []string
	var maxDuration float64
	for _, ir := range instances {
		exits = append(exits, ir.Role+"="+ir.ExitReason)
		run.TotalCostUSD += ir.CostUSD
		if ir.DurationSec > maxDuration {
			maxDuration = ir.DurationSec
		}
	}
	run.ExitReasons = strings.Join(exits, "|")
	run.WallClockSeconds = maxDuration

	trs, err := pool.Query(runCtx, `
		SELECT from_column, to_column, COALESCE(hygiene_status,'(null)')
		FROM ticket_transitions WHERE ticket_id=$1 ORDER BY at`, ticketID)
	if err != nil {
		run.RunErr = fmt.Sprintf("query transitions: %v", err)
		return run
	}
	var hygs []string
	for trs.Next() {
		var from *string
		var to, hyg string
		if err := trs.Scan(&from, &to, &hyg); err == nil {
			hygs = append(hygs, to+":"+hyg)
		}
	}
	trs.Close()
	run.HygieneStatuses = strings.Join(hygs, "|")

	rejections, successAttempt := finalizeAttemptsFromLog(stdout.String())
	if rejections > 0 || successAttempt > 0 {
		run.FinalizeCalled = true
	}
	if successAttempt > 0 {
		run.FinalizeAttempt = fmt.Sprintf("%d", successAttempt)
	}

	var finalCol string
	if err := pool.QueryRow(runCtx,
		`SELECT column_slug FROM tickets WHERE id=$1`, ticketID,
	).Scan(&finalCol); err == nil {
		run.FinalColumnSlug = finalCol
	}
	if first := firstFinalizeErrorFromLog(stdout.String()); first != "" {
		run.FirstAttemptError = first
	}
	if in.ModelShort == "opus-4-7" {
		run.OpusPalacePhaseS = palaceExplorationPhaseSeconds(stdout.String())
	}

	// ---- file-location + content check ----
	// Check both the workspace's changes/ (expected location) and
	// /home/jeroennouws/changes/ (sandbox-escape location observed
	// during haiku validation; tracked in
	// docs/issues/agent-workspace-sandboxing.md). Do NOT clean up
	// sandbox-escape artifacts here — operator's call at session end.
	fname := fmt.Sprintf("hello-%s.md", run.TicketUUID)
	wsPath := filepath.Join(workspace, "changes", fname)
	hostPath := filepath.Join(os.Getenv("HOME"), "changes", fname)
	run.FileLocation, run.FileBytes, run.FileContentValid = classifyFile(wsPath, hostPath, run.TicketUUID)

	// ---- pgmcp contract-violation sweep ----
	// Scan the supervisor log for signatures of the four bugs the
	// fixes addressed. Expected to be ZERO now. Verbatim capture
	// feeds the findings report's pattern-observation section.
	run.Errors = scanPgmcpContractViolations(stdout.String())

	return run
}

// classifyFile resolves the changelog file's on-disk location and
// checks whether its content meets the mechanical slice of the Ticket-A
// acceptance criteria (≥50 chars, references the UUID, no placeholder).
// Past-tense-ness is left to the findings author; a regex for verb
// tense would be fragile.
func classifyFile(wsPath, hostPath, ticketUUID string) (loc string, bytes int64, valid bool) {
	for _, candidate := range []struct {
		label string
		path  string
	}{
		{"workspace", wsPath},
		{"host_home", hostPath},
	} {
		b, err := os.ReadFile(candidate.path)
		if err != nil {
			continue
		}
		loc = candidate.label
		bytes = int64(len(b))
		valid = validateChangelogContent(b, ticketUUID)
		return
	}
	return "missing", 0, false
}

// validateChangelogContent applies the three mechanical checks:
//  1. ≥50 chars (substantive paragraph)
//  2. contains the ticket UUID as a substring
//  3. does NOT contain any known placeholder token
func validateChangelogContent(b []byte, ticketUUID string) bool {
	if len(b) < 50 {
		return false
	}
	s := string(b)
	if !strings.Contains(s, ticketUUID) {
		return false
	}
	placeholders := []string{
		"lorem ipsum",
		"Lorem ipsum",
		"[description]",
		"<fill this in>",
		"<description>",
		"TODO: describe",
	}
	for _, p := range placeholders {
		if strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// scanPgmcpContractViolations looks for signatures of the four bugs
// the fixes addressed. Returns a pipe-joined list of signatures seen,
// or empty if none. Expected post-fix state: empty on every run.
func scanPgmcpContractViolations(log string) string {
	type sig struct {
		label string
		probe string
	}
	sigs := []sig{
		// Bug 1: MCP envelope. Pre-fix, successful pgmcp calls rendered
		// as "completed with no output" in the agent's tool_result view.
		{"envelope_empty_output", "completed with no output"},
		// Bug 2: UUID as int array. Pre-fix, UUIDs came back as
		// [192,186,...] — agents can't round-trip that.
		{"uuid_int_array", `"id":[`},
		// Bug 3: agent_instances permission denied. Pre-fix, every
		// finalize precheck failed with this error.
		{"agent_instances_permission_denied", "permission denied for table agent_instances"},
		// Bug 4 (column-not-exist pattern from prior matrix §6): an
		// opus run attempted a query against a non-existent column.
		// Generic probe; expected to be zero now.
		{"column_not_exist", `does not exist (SQLSTATE 42703)`},
		// Bug 5 (finalize precheck's own hint, which would indicate
		// any residual state-rejection path is firing): the
		// canonical phrase from handler.go.
		{"finalize_state_rejection", "internal error checking finalize state"},
	}
	var hits []string
	for _, s := range sigs {
		if strings.Contains(log, s.probe) {
			hits = append(hits, s.label)
		}
	}
	return strings.Join(hits, "|")
}

// ---- output writers --------------------------------------------------

func writePostFixRawData(path string, runs []postFixExperimentRun, grand float64) error {
	type envelope struct {
		Runs      []postFixExperimentRun `json:"runs"`
		GrandCost float64                `json:"grand_cost_usd"`
		GrandCap  float64                `json:"grand_budget_cap_usd"`
		PerRunCap float64                `json:"per_run_budget_cap_usd"`
	}
	b, err := json.MarshalIndent(envelope{
		Runs:      runs,
		GrandCost: grand,
		GrandCap:  postFixGrandBudgetUSD,
		PerRunCap: postFixPerRunBudgetUSD,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writePostFixMatrixMarkdown(path string, runs []postFixExperimentRun, grand float64) error {
	var sb strings.Builder
	sb.WriteString("# Post-UUID-fix compliance matrix — raw table\n\n")
	sb.WriteString(fmt.Sprintf("Grand total cost: $%.4f (grand cap $%.2f, per-run cap $%.2f)\n\n",
		grand, postFixGrandBudgetUSD, postFixPerRunBudgetUSD))
	sb.WriteString("| # | model | iter | ticket_uuid | finalize_attempt | exit_reasons | hygiene | file_location | file_bytes | file_content_valid | cost $ | wall s | col | first_err | contract_violations | run_err |\n")
	sb.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")

	sorted := make([]postFixExperimentRun, len(runs))
	copy(sorted, runs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })

	for _, r := range sorted {
		firstErr := truncateForTable(r.FirstAttemptError, 60)
		sb.WriteString(fmt.Sprintf("| %d | %s | %d | %s | %s | `%s` | `%s` | %s | %d | %v | %.4f | %.1f | %s | `%s` | `%s` | %s |\n",
			r.Index, r.Model, r.RunInGroup, r.TicketUUID,
			r.FinalizeAttempt,
			truncateForTable(r.ExitReasons, 50),
			truncateForTable(r.HygieneStatuses, 50),
			r.FileLocation, r.FileBytes, r.FileContentValid,
			r.TotalCostUSD, r.WallClockSeconds,
			r.FinalColumnSlug,
			firstErr,
			truncateForTable(r.Errors, 40),
			truncateForTable(r.RunErr, 30),
		))
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
