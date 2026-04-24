//go:build experiment

// Triage-hypothesis experiment — tests whether ticket richness changes
// executor compliance behaviour. Matrix: 2 tickets (operator-drafted A,
// CEO-simulated B) × 2 models (haiku, opus) × 3 iterations = 12 runs.
//
// Build-tag-gated (`experiment`) so it never fires via the normal
// integration / live_acceptance suites. Invoke explicitly:
//
//   go test -tags=experiment -count=1 -timeout=2h -v \
//           -run=TestTriageHypothesisMatrix .
//
// Requires:
//   - real `claude` on $PATH (2.1.117 or compatible)
//   - spike-mempalace + spike-docker-proxy containers running
//   - Anthropic credentials configured for the claude binary
//
// Budget safety: hard-caps combined agent_instances.total_cost_usd at
// $3.80 across all runs. If the running total crosses the cap the
// remaining iterations are skipped (partial results still written).
//
// Produces (relative to the supervisor working directory, i.e.
// the worktree root's experiment-results/ after `cd ..`):
//   - experiment-results/run-<N>-<variant>-<model>.log   raw supervisor log
//   - experiment-results/MATRIX.md                       12-row table
//   - experiment-results/raw-data.json                   per-run structured dump
//
// The test body only collects data; operator writes FINDINGS.md from
// the MATRIX + raw-data + per-run logs.

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- ticket bodies under test -----------------------------------------

// ticketABody — operator-drafted (verbatim from experiment spec). Note
// the "Expected finalize shape" section — the methodological
// confounder Ticket B controls for.
const ticketABody = `## Objective
Create a changelog entry documenting this ticket's work.

## Context
We're establishing a per-ticket changelog discipline so future
maintenance has traceable history. Every engineering ticket that
ships code or config changes should produce a changes/hello-$TICKET_ID.md
file describing what was done and why.

## Acceptance criteria
- File exists at changes/hello-$TICKET_ID.md
- File contains at least one paragraph (≥50 characters)
- Paragraph references $TICKET_ID by value
- Paragraph describes what was done in factual, past-tense prose
- Paragraph avoids placeholder text (no "lorem ipsum", no "[description]")

## Constraints
- Single file only; no additional changes
- Write only to changes/ directory
- Do not modify existing changelog files

## Why engineering
This is a code-adjacent documentation task that belongs in the
engineering workspace. A manager layer isn't needed for work
this atomic.

## Expected finalize shape
Your finalize_ticket payload should include:
- outcome: one-line description of what changelog entry you created
- diary_entry.rationale: brief note on why your paragraph wording
  reflects the work (since the work itself is just "document that
  you did this")
- diary_entry.artifacts: ["changes/hello-<ticket_id>.md"]
- kg_triples: at least one establishing that this ticket produced
  the changelog entry`

// ticketBBody — CEO-simulated. No mention of finalize_ticket,
// diary_entry, kg_triples, MemPalace, or prescribed artifacts list.
// Structure matches Ticket A's richness minus the protocol tell.
const ticketBBody = `## Objective
Establish the per-ticket changelog convention for engineering, starting with this ticket's own entry.

## Context
We want every engineering ticket to leave behind a durable, human-readable record of what shipped and why. A single markdown file per ticket, stored in the repo's changes/ directory, is the format. This ticket is the pilot — bootstrap the convention by producing the first entry, describing this very ticket's work, so future tickets have an example to follow. No tooling or CI enforcement yet; that's a later ticket.

## Acceptance criteria
- A new markdown file lives at changes/hello-<ticket id>.md, substituting this ticket's UUID for <ticket id>.
- The file contains at least one well-formed paragraph (50+ characters) describing the work in factual, past-tense prose.
- The paragraph references this ticket's UUID by value.
- The paragraph is free of placeholder text ("lorem ipsum", "[description]", "<fill this in>", etc.).
- No other files are created or modified.

## Constraints
- In scope: a single new file under changes/, containing the changelog entry for this ticket only.
- Out of scope: modifying existing changelog files, adding READMEs or templates for future entries, tooling or CI to enforce the convention.
- Out of scope: anything outside the changes/ directory.

## Why engineering
Code-adjacent documentation lives where engineering ships it; no separate docs department exists yet and the work is small enough to not need a manager layer.`

// ---- experiment harness ----------------------------------------------

// experimentRun captures the per-iteration data the findings report
// needs. Ordered exactly as the raw-data table columns in the spec.
type experimentRun struct {
	Index             int     `json:"index"`                         // 1..12
	Ticket            string  `json:"ticket"`                        // "A" or "B"
	Model             string  `json:"model"`                         // haiku/opus short label
	RunInGroup        int     `json:"run_in_group"`                  // 1..3
	FinalizeCalled    bool    `json:"finalize_called"`               // (4)
	FinalizeAttempt   string  `json:"finalize_attempt"`              // "1"/"2"/"3"/"N/A"
	FirstAttemptError string  `json:"first_attempt_error,omitempty"` // (6)
	ExitReasons       string  `json:"exit_reasons"`                  // pipe-joined per role
	HygieneStatuses   string  `json:"hygiene_statuses"`              // pipe-joined
	TotalCostUSD      float64 `json:"total_cost_usd"`                // summed across instances
	WallClockSeconds  float64 `json:"wall_clock_seconds"`            // max(finished_at-started_at)
	FinalColumnSlug   string  `json:"final_column_slug"`             // where the ticket ended up
	TicketUUID        string  `json:"ticket_uuid"`
	RunErr            string  `json:"run_err,omitempty"`
	OpusPalacePhaseS  float64 `json:"opus_palace_phase_seconds,omitempty"` // (11) best-effort
}

const experimentBudgetCap = 3.80 // USD; leaves slack under the $4 hard cap.

// TestTriageHypothesisMatrix — runs the 12-iteration matrix. Sequential
// by design: parallel runs would confound palace state between runs
// even with distinct palace paths (shared sidecar, shared docker sock).
//
// Per-run palace isolation is via GARRISON_PALACE_PATH=/palace-exp-<N>;
// mempalace init --yes creates the directory on first access. Agents
// are UPDATEd to the committed M2.2.2 seed content before each run
// (SeedM22 installs placeholder seeds).
func TestTriageHypothesisMatrix(t *testing.T) {
	requireSpikeStack(t)

	realClaude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}

	// Load the real committed seeds once; injected into each run.
	// Resolved against the supervisor package dir so the relative
	// path matches the existing live-acceptance pattern.
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

	// Matrix order: ticket-major, then model, then iteration.
	// Sequential iteration by design — cost / palace predictability.
	variants := []struct {
		label string // "A" / "B"
		body  string
	}{
		{"A", ticketABody},
		{"B", ticketBBody},
	}
	models := []struct {
		short string // for filenames; avoids path-hostile version suffix
		full  string
	}{
		{"haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{"opus-4-7", "claude-opus-4-7"},
	}

	runs := make([]experimentRun, 0, 12)
	var grandCost float64
	index := 0

	for _, v := range variants {
		for _, m := range models {
			for i := 1; i <= 3; i++ {
				index++
				if grandCost > experimentBudgetCap {
					t.Logf("[experiment] BUDGET CAP hit at run=%d grand=$%.4f; "+
						"skipping remaining iterations", index, grandCost)
					// Record a skip row so MATRIX.md stays 12 rows.
					runs = append(runs, experimentRun{
						Index:      index,
						Ticket:     v.label,
						Model:      m.short,
						RunInGroup: i,
						RunErr:     "skipped: budget cap",
					})
					continue
				}

				// Scope the iteration under a t.Run so its t.Cleanup
				// (supervisor SIGTERM + log persistence) fires between
				// iterations — not piled up at the end of the outer test.
				subName := fmt.Sprintf("run%02d_%s_%s_iter%d", index, v.label, m.short, i)
				var run experimentRun
				t.Run(subName, func(t *testing.T) {
					run = runOneExperimentIteration(t, iterationInputs{
						Index:        index,
						Variant:      v.label,
						TicketBody:   v.body,
						ModelShort:   m.short,
						ModelFull:    m.full,
						RunInGroup:   i,
						ClaudeBin:    realClaude,
						EngineerSeed: string(engineerSeed),
						QAEngineerMD: string(qaSeed),
						OutDir:       outDir,
					})
				})
				grandCost += run.TotalCostUSD
				runs = append(runs, run)

				t.Logf("[experiment] run=%d ticket=%s model=%s iter=%d "+
					"finalize_called=%v attempt=%s cost=$%.4f cum=$%.4f "+
					"hygiene=%s exit=%s err=%q",
					run.Index, run.Ticket, run.Model, run.RunInGroup,
					run.FinalizeCalled, run.FinalizeAttempt, run.TotalCostUSD,
					grandCost, run.HygieneStatuses, run.ExitReasons, run.RunErr)
			}
		}
	}

	// Always write the matrix + raw dump, even if some rows were
	// budget-skipped. Findings author reads from these outputs.
	if err := writeRawData(filepath.Join(outDir, "raw-data.json"), runs, grandCost); err != nil {
		t.Errorf("write raw-data.json: %v", err)
	}
	if err := writeMatrixMarkdown(filepath.Join(outDir, "MATRIX.md"), runs, grandCost); err != nil {
		t.Errorf("write MATRIX.md: %v", err)
	}

	t.Logf("[experiment] DONE grand_cost=$%.4f runs=%d (of 12) files in %s",
		grandCost, len(runs), outDir)
}

// ---- per-iteration runner --------------------------------------------

type iterationInputs struct {
	Index        int
	Variant      string // "A"/"B"
	TicketBody   string
	ModelShort   string // "haiku-4-5-20251001"
	ModelFull    string // "claude-haiku-4-5-20251001"
	RunInGroup   int
	ClaudeBin    string
	EngineerSeed string
	QAEngineerMD string
	OutDir       string
}

func runOneExperimentIteration(t *testing.T, in iterationInputs) experimentRun {
	t.Helper()
	run := experimentRun{
		Index:           in.Index,
		Ticket:          in.Variant,
		Model:           in.ModelShort,
		RunInGroup:      in.RunInGroup,
		FinalizeAttempt: "N/A",
	}

	// Palace isolation: unique path per iteration. mempalace init --yes
	// requires the target directory to exist (verified manually:
	// `mempalace init --yes /nonexistent` errors "Directory not found:
	// ...; exit 1"), so we mkdir via docker exec before handing the
	// path to the supervisor. rm -rf at t.Cleanup drops the palace
	// state so subsequent experiments don't inherit it.
	palacePath := fmt.Sprintf("/palace-exp-%02d-%s-%s",
		in.Index, in.Variant, in.ModelShort)
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

	pool := testdb.Start(t) // TRUNCATEs + cleanup registered.
	workspace := t.TempDir()
	_, _ = testdb.SeedM22(t, workspace)

	// Upgrade agents to real M2.2.2 seeds (SeedM22 installs placeholders).
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
	// Align agents.model with this iteration's model per M2.2.1's
	// spawn.go precedence (agent.Model > env).
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

	testdb.SetAgentROPassword(t, "experiment-ro-pw")
	testdb.SetAgentMempalacePassword(t, "experiment-mp-pw")

	bin := buildSupervisorBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)

	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+in.ClaudeBin,
		"GARRISON_CLAUDE_MODEL="+in.ModelFull,
		"GARRISON_AGENT_RO_PASSWORD=experiment-ro-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=experiment-mp-pw",
		"GARRISON_MCP_CONFIG_DIR="+mcpDir,
		"GARRISON_MEMPALACE_CONTAINER="+m22MempalaceContainer,
		"GARRISON_PALACE_PATH="+palacePath,
		"DOCKER_HOST="+m22DockerProxyHost,
		"GARRISON_SUBPROCESS_TIMEOUT=180s",
		"GARRISON_HYGIENE_DELAY=2s",
		"GARRISON_HYGIENE_SWEEP_INTERVAL=5s",
		"GARRISON_FINALIZE_WRITE_TIMEOUT=30s",
		"GARRISON_CLAUDE_BUDGET_USD=0.20",
		"GARRISON_LOG_LEVEL=info",
	)

	// 15-minute ceiling per spec ("assume hung, kill, note as failure").
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
	// Always shut down + persist log, even on partial / errored runs.
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		logPath := filepath.Join(in.OutDir,
			fmt.Sprintf("run-%02d-%s-%s.log", in.Index, in.Variant, in.ModelShort))
		_ = os.WriteFile(logPath, []byte(stdout.String()), 0o644)
	})

	if err := waitForHealth(healthPort, 30*time.Second); err != nil {
		run.RunErr = fmt.Sprintf("supervisor health: %v", err)
		return run
	}

	// Insert the ticket with the variant body in objective. Leave
	// acceptance_criteria NULL — the body carries its own AC section;
	// splitting would dilute the richness we're measuring.
	var ticketID pgtype.UUID
	if err := pool.QueryRow(runCtx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, $2, 'in_dev')
		RETURNING id`, deptID, in.TicketBody,
	).Scan(&ticketID); err != nil {
		run.RunErr = fmt.Sprintf("insert ticket: %v", err)
		return run
	}
	run.TicketUUID = uuidString(ticketID)

	// Wait for terminal state. Happy-path: two agent_instances
	// (engineer + qa-engineer) terminate via the in_dev→qa_review
	// transition. Failure path: engineer dies with finalize_invalid /
	// budget_exceeded / finalize_never_called, no transition happens,
	// and the qa spawn never arrives.
	//
	// Avoid waiting 12 min when we already know there's no 2nd spawn
	// coming: once the engineer is terminal, wait a short settling
	// window (hygiene checker runs on ticket_transitions; the QA
	// spawn, if any, should land promptly). If no 2nd row after the
	// settle window and no transition row, we're done.
	settleWindow := 30 * time.Second
	hardCeiling := 10 * time.Minute // covers slow opus runs
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
			// If the engineer is terminal AND no transition row
			// exists AND the settle window has elapsed, no 2nd spawn
			// is coming.
			if transitions == 0 && time.Since(engineerTerminalAt) >= settleWindow {
				return true, nil
			}
			// If a transition row exists, keep waiting up to the
			// hard ceiling for the qa spawn to terminate.
		}
		return false, nil
	})
	// If we got 0 or 1 terminal rows we still proceed — timing out here
	// is itself an observation (records "1 row" or "0 rows" in the
	// evidence).

	// Collect per-instance evidence.
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

	// Aggregate evidence columns.
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

	// Transitions + hygiene_status.
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

	// finalize_called + successful-attempt: parsed from supervisor slog.
	// The finalize handler emits "finalize: schema rejection" for each
	// failed attempt and "finalize: payload accepted" on success,
	// each carrying the `attempt` integer. finalize_attempts_from_log
	// returns (rejections, successAttempt) where successAttempt > 0
	// means the Nth attempt succeeded.
	rejections, successAttempt := finalizeAttemptsFromLog(stdout.String())
	if rejections > 0 || successAttempt > 0 {
		run.FinalizeCalled = true
	}
	if successAttempt > 0 {
		run.FinalizeAttempt = fmt.Sprintf("%d", successAttempt)
	}

	// Final column_slug.
	var finalCol string
	if err := pool.QueryRow(runCtx,
		`SELECT column_slug FROM tickets WHERE id=$1`, ticketID,
	).Scan(&finalCol); err == nil {
		run.FinalColumnSlug = finalCol
	}

	// First-attempt-error: best-effort parse of the supervisor's stdout
	// log for the first finalize tool_result payload containing
	// error_type + field + constraint. We defer extraction to
	// post-hoc log grep because the slog output shape evolves; record
	// the first "finalize_attempt_failed" slog line we see.
	if first := firstFinalizeErrorFromLog(stdout.String()); first != "" {
		run.FirstAttemptError = first
	}
	if in.ModelShort == "opus-4-7" {
		run.OpusPalacePhaseS = palaceExplorationPhaseSeconds(stdout.String())
	}

	return run
}

// finalizeAttemptsFromLog parses the supervisor's captured stdout
// for finalize-handler activity. Returns (rejections, successAttempt).
// The supervisor emits structured slog lines with msg="finalize
// tool_result" carrying `"ok":true|false` and `"attempt":N`.
// successAttempt is the attempt number on which ok=true landed
// (0 if finalize never succeeded); rejections is the count of
// ok=false observations (i.e. schema rejections).
//
// The finalize-handler's own "finalize: schema rejection" /
// "finalize: payload accepted" messages go to a child logger whose
// output does not reliably reach the captured stdout, so we match
// on the pipeline-level envelope observed in M2.2.1 / M2.2.2
// production logs instead.
func finalizeAttemptsFromLog(s string) (rejections, successAttempt int) {
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, `"msg":"finalize tool_result"`) {
			continue
		}
		attempt := extractIntField(line, "attempt")
		if strings.Contains(line, `"ok":true`) {
			if attempt > 0 {
				successAttempt = attempt
			} else {
				successAttempt = 1
			}
			continue
		}
		if strings.Contains(line, `"ok":false`) {
			rejections++
		}
	}
	return
}

// extractIntField returns the integer value of `<name>=<N>` or
// `"<name>":<N>` in line, or 0 if not found.
func extractIntField(line, name string) int {
	for _, probe := range []string{name + "=", `"` + name + `":`} {
		idx := strings.Index(line, probe)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(probe):]
		// Strip a leading quote if JSON attribute wraps the value.
		rest = strings.TrimLeft(rest, `"`)
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		n := 0
		for i := 0; i < end; i++ {
			n = n*10 + int(rest[i]-'0')
		}
		return n
	}
	return 0
}

// firstFinalizeErrorFromLog scans the supervisor's captured stdout
// for the first finalize validation-failure signal. The supervisor
// emits slog lines around tool invocations; we match any line that
// names finalize_ticket and carries a non-empty "failure"/"field"/
// "constraint" token. Best-effort only — absence does NOT mean no
// rejection occurred, just that our fuzzy match missed it; raw log
// is persisted per-run for authoritative post-hoc inspection.
func firstFinalizeErrorFromLog(s string) string {
	for _, line := range strings.Split(s, "\n") {
		lo := strings.ToLower(line)
		if !strings.Contains(lo, "finalize") {
			continue
		}
		if !(strings.Contains(line, "\"failure\"") ||
			strings.Contains(line, "\"field\"") ||
			strings.Contains(line, "\"constraint\"") ||
			strings.Contains(line, "validation")) {
			continue
		}
		// Strip structured-prefix noise; keep the line short for the
		// table.
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 400 {
			trimmed = trimmed[:400] + "...[truncated]"
		}
		return trimmed
	}
	return ""
}

// palaceExplorationPhaseSeconds returns the elapsed seconds between
// the first mempalace_* tool_use log line and the first non-palace
// tool_use line. Operates on log timestamps (slog default layout
// "2006-01-02T15:04:05.000Z07:00"). Returns 0 if the pattern can't be
// established — e.g. no palace calls, or no tool calls at all. This
// is intentionally fuzzy: the test captures raw logs for exact
// post-hoc analysis.
func palaceExplorationPhaseSeconds(s string) float64 {
	var firstPalace, firstNonPalace time.Time
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "tool_use") && !strings.Contains(line, "tool=") {
			continue
		}
		ts := parseSlogTimestamp(line)
		if ts.IsZero() {
			continue
		}
		if strings.Contains(line, "mempalace_") {
			if firstPalace.IsZero() {
				firstPalace = ts
			}
			continue
		}
		// Non-palace tool call (Read, Write, Edit, postgres_*, finalize_*).
		if firstPalace.IsZero() {
			continue
		}
		if firstNonPalace.IsZero() {
			firstNonPalace = ts
			break
		}
	}
	if firstPalace.IsZero() || firstNonPalace.IsZero() {
		return 0
	}
	return firstNonPalace.Sub(firstPalace).Seconds()
}

// parseSlogTimestamp extracts an RFC3339ish timestamp from the leading
// portion of a slog line. Returns zero time if no parseable token.
func parseSlogTimestamp(line string) time.Time {
	// slog default TextHandler: `time=2026-04-24T15:04:05.000+00:00 ...`
	// slog JSONHandler:         `{"time":"2026-04-24T15:04:05.000Z", ...`
	if i := strings.Index(line, "time="); i >= 0 {
		rest := line[i+len("time="):]
		if j := strings.IndexAny(rest, " \t"); j > 0 {
			if t, err := time.Parse(time.RFC3339Nano, rest[:j]); err == nil {
				return t
			}
		}
	}
	if i := strings.Index(line, `"time":"`); i >= 0 {
		rest := line[i+len(`"time":"`):]
		if j := strings.IndexByte(rest, '"'); j > 0 {
			if t, err := time.Parse(time.RFC3339Nano, rest[:j]); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// ---- output writers --------------------------------------------------

func writeRawData(path string, runs []experimentRun, grand float64) error {
	type envelope struct {
		Runs      []experimentRun `json:"runs"`
		GrandCost float64         `json:"grand_cost_usd"`
		Cap       float64         `json:"budget_cap_usd"`
	}
	b, err := json.MarshalIndent(envelope{Runs: runs, GrandCost: grand, Cap: experimentBudgetCap}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeMatrixMarkdown(path string, runs []experimentRun, grand float64) error {
	var sb strings.Builder
	sb.WriteString("# Triage-hypothesis experiment — raw matrix\n\n")
	sb.WriteString(fmt.Sprintf("Grand total cost: $%.4f (cap $%.2f)\n\n", grand, experimentBudgetCap))
	sb.WriteString("| # | ticket | model | iter | finalize_called | attempt | exit_reasons | hygiene | cost $ | wall s | col | first_finalize_err | ticket_uuid | run_err |\n")
	sb.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")

	// Sorted by index for determinism.
	sorted := make([]experimentRun, len(runs))
	copy(sorted, runs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })
	for _, r := range sorted {
		firstErr := truncateForTable(r.FirstAttemptError, 80)
		sb.WriteString(fmt.Sprintf("| %d | %s | %s | %d | %v | %s | `%s` | `%s` | %.4f | %.1f | %s | `%s` | %s | %s |\n",
			r.Index, r.Ticket, r.Model, r.RunInGroup,
			r.FinalizeCalled, r.FinalizeAttempt,
			truncateForTable(r.ExitReasons, 60),
			truncateForTable(r.HygieneStatuses, 60),
			r.TotalCostUSD, r.WallClockSeconds,
			r.FinalColumnSlug,
			firstErr,
			r.TicketUUID,
			truncateForTable(r.RunErr, 40),
		))
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

func truncateForTable(s string, n int) string {
	// Pipes in markdown table cells must be escaped or they split the row.
	s = strings.ReplaceAll(s, "|", "¦")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Keep pgxpool import live (shared with the rest of the suite).
var _ = pgxpool.New
