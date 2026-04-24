//go:build experiment

// Stream-capture subset: re-runs 4 targeted iterations of the triage
// matrix (A-haiku, A-opus, B-haiku, B-opus) with a tee-wrapper around
// the claude binary so the per-invocation stream-json stdout is
// captured into experiment-results/stream-json/. Post-hoc analysis
// uses these captures to extract the finalize_ticket tool_use.input
// payloads and the corresponding tool_result envelopes (including the
// `hint` text) — neither of which lands in the supervisor's slog.
//
// Invoke:
//   go test -tags=experiment -count=1 -timeout=30m -v \
//           -run=TestTriageHypothesisStreamCapture .
//
// Pre-requisites: same as TestTriageHypothesisMatrix (spike stack up,
// real claude on PATH, Anthropic credentials configured). Uses the
// same iteration harness so the SUT is unchanged.

package supervisor_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestTriageHypothesisStreamCapture(t *testing.T) {
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
	teeDir := filepath.Join(outDir, "stream-json")
	if err := os.MkdirAll(teeDir, 0o755); err != nil {
		t.Fatalf("mkdir stream-json: %v", err)
	}

	// One of each cell. Index numbering continues the main matrix so
	// the stream-json capture files don't collide with it.
	cases := []struct {
		index      int
		variant    string
		body       string
		modelShort string
		modelFull  string
	}{
		{13, "A", ticketABody, "haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{14, "A", ticketABody, "opus-4-7", "claude-opus-4-7"},
		{15, "B", ticketBBody, "haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{16, "B", ticketBBody, "opus-4-7", "claude-opus-4-7"},
	}

	for _, c := range cases {
		subName := fmt.Sprintf("stream%02d_%s_%s", c.index, c.variant, c.modelShort)
		t.Run(subName, func(t *testing.T) {
			// Per-case tee subdir keeps capture files scoped to the
			// iteration they describe.
			caseTeeDir := filepath.Join(teeDir, fmt.Sprintf("run-%02d-%s-%s", c.index, c.variant, c.modelShort))
			if err := os.MkdirAll(caseTeeDir, 0o755); err != nil {
				t.Fatalf("mkdir case tee dir: %v", err)
			}

			runStreamCaptureIteration(t, streamCaptureInputs{
				Index:        c.index,
				Variant:      c.variant,
				TicketBody:   c.body,
				ModelShort:   c.modelShort,
				ModelFull:    c.modelFull,
				ClaudeBin:    wrapper,
				ClaudeReal:   realClaude,
				TeeDir:       caseTeeDir,
				EngineerSeed: string(engineerSeed),
				QAEngineerMD: string(qaSeed),
				OutDir:       outDir,
			})
		})
	}
}

type streamCaptureInputs struct {
	Index        int
	Variant      string
	TicketBody   string
	ModelShort   string
	ModelFull    string
	ClaudeBin    string // path to the wrapper
	ClaudeReal   string // path to the real claude binary (passed to wrapper via env)
	TeeDir       string // where the wrapper writes jsonl captures
	EngineerSeed string
	QAEngineerMD string
	OutDir       string
}

// runStreamCaptureIteration is a trimmed copy of
// runOneExperimentIteration — identical wiring except GARRISON_CLAUDE_BIN
// points at the tee wrapper and the wrapper's env is extended. No
// supervisor-side behaviour changes.
func runStreamCaptureIteration(t *testing.T, in streamCaptureInputs) {
	t.Helper()

	palacePath := fmt.Sprintf("/palace-exp-%02d-%s-%s",
		in.Index, in.Variant, in.ModelShort)
	if out, err := exec.Command("docker", "exec", m22MempalaceContainer,
		"mkdir", "-p", palacePath,
	).CombinedOutput(); err != nil {
		t.Fatalf("mkdir palace %s: %v: %s", palacePath, err, string(out))
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
		t.Fatalf("update engineer agent_md: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET agent_md=$1 WHERE role_slug='qa-engineer'`, in.QAEngineerMD,
	); err != nil {
		t.Fatalf("update qa-engineer agent_md: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET model=$1 WHERE role_slug IN ('engineer','qa-engineer')`,
		in.ModelFull,
	); err != nil {
		t.Fatalf("update agents.model: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM departments WHERE slug='engineering'`,
	).Scan(&deptID); err != nil {
		t.Fatalf("lookup dept id: %v", err)
	}

	testdb.SetAgentROPassword(t, "stream-capture-ro-pw")
	testdb.SetAgentMempalacePassword(t, "stream-capture-mp-pw")

	bin := buildSupervisorBinary(t)
	mcpDir := t.TempDir()
	_ = os.Chmod(mcpDir, 0o750)

	healthPort := mustFreePort(t)
	env := append(os.Environ(),
		"GARRISON_DATABASE_URL="+testdb.URL(t),
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", healthPort),
		"GARRISON_CLAUDE_BIN="+in.ClaudeBin,
		"GARRISON_CLAUDE_MODEL="+in.ModelFull,
		"GARRISON_AGENT_RO_PASSWORD=stream-capture-ro-pw",
		"GARRISON_AGENT_MEMPALACE_PASSWORD=stream-capture-mp-pw",
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
		// Wrapper env.
		"CLAUDE_REAL="+in.ClaudeReal,
		"GARRISON_EXPERIMENT_TEE_DIR="+in.TeeDir,
	)

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin)
	cmd.Env = env
	cmd.Dir = workspace
	var stdout safeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		logPath := filepath.Join(in.OutDir,
			fmt.Sprintf("run-%02d-%s-%s.log", in.Index, in.Variant, in.ModelShort))
		_ = os.WriteFile(logPath, []byte(stdout.String()), 0o644)
	})

	if err := waitForHealth(healthPort, 30*time.Second); err != nil {
		t.Fatalf("supervisor health: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(runCtx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, $2, 'in_dev')
		RETURNING id`, deptID, in.TicketBody,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	t.Logf("[stream-capture] run=%d ticket=%s model=%s ticket_id=%s tee_dir=%s",
		in.Index, in.Variant, in.ModelShort, uuidString(ticketID), in.TeeDir)

	// Same wait pattern as main matrix.
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

	// List produced capture files so the log tells the operator what to read.
	if entries, err := os.ReadDir(in.TeeDir); err == nil {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Logf("[stream-capture] run=%d captured %d file(s): %s",
			in.Index, len(names), strings.Join(names, ", "))
	}
}
