package spawn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/concurrency"
	"github.com/garrison-hq/garrison/supervisor/internal/mcpconfig"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ShutdownSignalGrace is the NFR-005 fixed window between the SIGTERM
// forwarded to a running subprocess on supervisor shutdown and the SIGKILL
// escalation that follows. Not operator-tunable.
const ShutdownSignalGrace = 5 * time.Second

// TerminalWriteGrace bounds the final terminal-row update after the
// subprocess exits. The root ctx is already cancelled on shutdown, so the
// terminal writers run against a detached ctx — this cap keeps a stuck DB
// from holding the supervisor past the operator's shutdown budget.
const TerminalWriteGrace = 5 * time.Second

// MCPBailSignalGrace bounds the SIGTERM→SIGKILL escalation window on the
// MCP-bail path. NFR-106 budget is 2 s from init-observation to the group
// being signalled; the first SIGTERM lands within microseconds of the
// observation, so this grace governs only how long we wait before
// escalating. 2 seconds is a deliberate parity with the total NFR budget.
const MCPBailSignalGrace = 2 * time.Second

// Deps bundles Spawn's runtime collaborators. Constructed once in
// cmd/supervisor and handed to the dispatcher so every event invocation
// shares the same pool, logger, and config.
type Deps struct {
	Pool              *pgxpool.Pool
	Queries           *store.Queries
	Logger            *slog.Logger
	SubprocessTimeout time.Duration

	// SigkillEscalations counts subprocesses the supervisor had to escalate
	// from SIGTERM to SIGKILL on shutdown (NFR-005). nil is tolerated
	// (tests skip tracking); production wires an atomic counter so
	// cmd/supervisor can return exit code 5 per contracts/cli.md when
	// counter > 0 at shutdown.
	SigkillEscalations *atomic.Int64

	// Fake-agent escape hatch. FakeAgentCmd being non-empty implicitly
	// flips UseFakeAgent true so M1 integration tests and chaos_test.go
	// keep working without touching every call site. Production daemons
	// set UseFakeAgent explicitly from config.
	FakeAgentCmd string
	UseFakeAgent bool

	// M2.1 real-Claude path collaborators. Unused when UseFakeAgent is
	// true. AgentRODSN is composed by config.AgentRODSN() and handed in
	// whole (the unexported password never leaves config).
	AgentsCache     *agents.Cache
	ClaudeBin       string
	ClaudeModel     string
	ClaudeBudgetUSD float64
	MCPConfigDir    string
	SupervisorBin   string
	AgentRODSN      string
}

// UseFake decides which branch Spawn runs. UseFakeAgent wins if set;
// otherwise a non-empty FakeAgentCmd implies fake mode for back-compat
// with existing M1 tests that predate the explicit flag. Exported so
// tests can pin the dispatch contract without reaching for reflection.
func (d Deps) UseFake() bool {
	return d.UseFakeAgent || d.FakeAgentCmd != ""
}

// Spawn handles one work.ticket.created event end-to-end. Idempotent: a
// second call with the same event_id is a no-op via the
// LockEventForProcessing dedupe check, which is the guard against the
// LISTEN/poll race described in plan §"Dedupe on handling".
//
// The first short transaction (LockEventForProcessing → department lookup
// → CheckCap → InsertRunningInstance → commit) is shared by both the
// fake-agent and real-Claude paths. Only after that commit do the two
// paths diverge — the fake path runs the M1 exec-and-scan loop; the real
// path runs the M2.1 agent-resolve + MCP-config + NDJSON-pipeline flow.
func Spawn(ctx context.Context, deps Deps, eventID pgtype.UUID) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("spawn: begin dedupe tx: %w", err)
	}
	q := deps.Queries.WithTx(tx)

	evt, err := q.LockEventForProcessing(ctx, eventID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: LockEventForProcessing: %w", err)
	}
	if evt.ProcessedAt.Valid {
		_ = tx.Rollback(ctx)
		return nil
	}

	var payload struct {
		TicketID     string `json:"ticket_id"`
		DepartmentID string `json:"department_id"`
	}
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: decode payload: %w", err)
	}
	ticketUUID, err := parseUUID(payload.TicketID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: ticket_id: %w", err)
	}
	deptUUID, err := parseUUID(payload.DepartmentID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: department_id: %w", err)
	}

	dept, err := q.GetDepartmentByID(ctx, deptUUID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: GetDepartmentByID: %w", err)
		}
		// Deleted-department edge: mark the event processed with no
		// agent_instances row, log once at error level, return.
		if err := q.MarkEventProcessed(ctx, eventID); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: MarkEventProcessed missing-dept: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("spawn: commit missing-dept: %w", err)
		}
		deps.Logger.Error("department missing for event",
			"event_id", formatUUID(eventID),
			"ticket_id", payload.TicketID,
			"department_id", payload.DepartmentID,
			"reason", ExitDepartmentMissing,
		)
		return nil
	}

	allowed, capN, running, err := concurrency.CheckCap(ctx, q, deptUUID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: CheckCap: %w", err)
	}
	if !allowed {
		_ = tx.Rollback(ctx)
		deps.Logger.Info("defer: concurrency cap reached",
			"event_id", formatUUID(eventID),
			"department_id", payload.DepartmentID,
			"cap", capN,
			"running", running,
		)
		return nil
	}

	instanceID, err := q.InsertRunningInstance(ctx, store.InsertRunningInstanceParams{
		DepartmentID: deptUUID,
		TicketID:     ticketUUID,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: InsertRunningInstance: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("spawn: commit dedupe+running: %w", err)
	}

	if deps.UseFake() {
		return runFakeAgent(ctx, deps, instanceID, eventID, ticketUUID, payload)
	}
	return runRealClaude(ctx, deps, instanceID, eventID, ticketUUID, dept, payload)
}

// -----------------------------------------------------------------------
// Fake-agent path (M1 lifecycle, preserved verbatim for test suites)
// -----------------------------------------------------------------------

// runFakeAgent is the M1 lifecycle: BuildCommand → cmd.Start → line-scan
// stdout/stderr → wait → Classify → writeTerminal. PID-level signalling is
// sufficient here because the fake agent does not spawn children; keeping
// this path identical to M1 is what lets every M1 integration test pass
// unchanged.
func runFakeAgent(
	ctx context.Context,
	deps Deps,
	instanceID, eventID, _ pgtype.UUID,
	payload struct {
		TicketID     string `json:"ticket_id"`
		DepartmentID string `json:"department_id"`
	},
) error {
	execCtx, execCancel := context.WithTimeout(context.Background(), deps.SubprocessTimeout)
	defer execCancel()

	cmd, err := BuildCommand(execCtx, deps.FakeAgentCmd, payload.TicketID, payload.DepartmentID)
	if err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "build_command_error")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "stdout_pipe_error")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "stderr_pipe_error")
	}
	if err := cmd.Start(); err != nil {
		return writeTerminal(ctx, deps, instanceID, eventID, "failed", "start_error")
	}

	logger := deps.Logger.With(
		"event_id", formatUUID(eventID),
		"ticket_id", payload.TicketID,
		"department_id", payload.DepartmentID,
		"pid", cmd.Process.Pid,
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go scanStream(&wg, stdout, logger, "stdout")
	go scanStream(&wg, stderr, logger, "stderr")

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var (
		exitErr           error
		shutdownCtxErr    error
		shutdownSigkilled bool
	)
	select {
	case err := <-waitDone:
		exitErr = err
	case <-ctx.Done():
		shutdownCtxErr = ctx.Err()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-waitDone:
			exitErr = err
		case <-time.After(ShutdownSignalGrace):
			_ = cmd.Process.Kill()
			shutdownSigkilled = true
			exitErr = <-waitDone
		}
	}
	wg.Wait()
	_ = exitErr

	var ctxErr error
	switch {
	case shutdownCtxErr != nil:
		ctxErr = context.Canceled
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		ctxErr = context.DeadlineExceeded
	}

	exitCode, sigName := extractExit(cmd.ProcessState)
	cls := Classify(exitCode, sigName, ctxErr, shutdownSigkilled)

	if shutdownSigkilled && deps.SigkillEscalations != nil {
		deps.SigkillEscalations.Add(1)
	}

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TerminalWriteGrace)
	defer cancel()
	return writeTerminal(termCtx, deps, instanceID, eventID, cls.Status, cls.ExitReason)
}

// -----------------------------------------------------------------------
// Real-Claude path (M2.1 — agents cache + MCP config + NDJSON pipeline)
// -----------------------------------------------------------------------

// runRealClaude implements steps 3–12 of plan §internal/spawn for the real
// Claude Code subprocess. The 12-step sequence is preserved in the code
// layout below; each block is annotated with its plan step.
func runRealClaude(
	ctx context.Context,
	deps Deps,
	instanceID, eventID, ticketUUID pgtype.UUID,
	dept store.Department,
	payload struct {
		TicketID     string `json:"ticket_id"`
		DepartmentID string `json:"department_id"`
	},
) error {
	logger := deps.Logger.With(
		"event_id", formatUUID(eventID),
		"instance_id", formatUUID(instanceID),
		"ticket_id", payload.TicketID,
		"department_id", payload.DepartmentID,
	)

	// Step 3: resolve agent row from the startup cache. A missing cache or
	// a missing row is terminal with exit_reason='agent_missing' — the
	// supervisor has already committed a running row and must close it out.
	if deps.AgentsCache == nil {
		logger.Error("agents cache not wired; cannot resolve engineer")
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitAgentMissing, pgtype.Numeric{}, false)
	}
	agent, err := deps.AgentsCache.GetForDepartmentAndRole(ctx, dept.ID, "engineer")
	if err != nil {
		logger.Error("no engineer agent for department", "err", err)
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitAgentMissing, pgtype.Numeric{}, false)
	}

	// Step 4: write per-invocation MCP config. Disk errors here land in
	// the spawn_failed terminal (clarify-session Q2) — dispatcher continues
	// onto the next event.
	mcpPath, err := mcpconfig.Write(ctx, deps.MCPConfigDir, instanceID, deps.SupervisorBin, deps.AgentRODSN)
	if err != nil {
		logger.Error("mcpconfig.Write failed; recording spawn_failed", "err", err)
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitSpawnFailed, pgtype.Numeric{}, false)
	}
	// Remove the MCP config file on every exit path.
	defer func() {
		if rmErr := mcpconfig.Remove(mcpPath); rmErr != nil {
			logger.Warn("mcpconfig.Remove failed; continuing", "path", mcpPath, "err", rmErr)
		}
	}()

	// Step 5 + 6: build argv + configure exec.Cmd.
	execCtx, execCancel := context.WithTimeout(context.Background(), deps.SubprocessTimeout)
	defer execCancel()

	model := agent.Model
	if model == "" {
		model = deps.ClaudeModel
	}
	taskDescription := fmt.Sprintf(
		"You are the engineer on ticket %s. Read it from Postgres and perform the task described in your system prompt.",
		payload.TicketID,
	)
	budget := deps.ClaudeBudgetUSD
	if budget <= 0 {
		budget = 0.05
	}
	argv := []string{
		"-p", taskDescription,
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--model", model,
		"--max-budget-usd", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", budget), "0"), "."),
		"--mcp-config", mcpPath,
		"--strict-mcp-config",
		"--system-prompt", agent.AgentMD,
		"--permission-mode", "bypassPermissions",
	}
	cmd := exec.CommandContext(execCtx, deps.ClaudeBin, argv...)
	if dept.WorkspacePath != nil && *dept.WorkspacePath != "" {
		cmd.Dir = *dept.WorkspacePath
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext's default Cancel kills the PID only. Override
	// so the timeout path signals the whole process group.
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }
	// WaitDelay escalates to a second Cancel (which we steer to SIGKILL
	// via a latch) if the process is still alive after 5 seconds.
	cmd.WaitDelay = ShutdownSignalGrace
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		logger.Error("open /dev/null failed", "err", err)
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitSpawnFailed, pgtype.Numeric{}, false)
	}
	defer stdin.Close()
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitSpawnFailed, pgtype.Numeric{}, false)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitSpawnFailed, pgtype.Numeric{}, false)
	}

	// Step 7: cmd.Start.
	if err := cmd.Start(); err != nil {
		logger.Error("claude cmd.Start failed; recording spawn_failed", "err", err)
		return writeTerminalCost(ctx, deps, instanceID, eventID, ticketUUID, "failed", ExitSpawnFailed, pgtype.Numeric{}, false)
	}

	logger = logger.With("pid", cmd.Process.Pid, "session_prep_model", model)
	logger.Info("claude subprocess started")

	// Step 8: backfill pid (M1 retro §4 fix).
	if err := deps.Queries.UpdatePID(ctx, store.UpdatePIDParams{
		ID:  instanceID,
		Pid: int32Ptr(int32(cmd.Process.Pid)),
	}); err != nil {
		logger.Warn("UpdatePID failed; continuing without backfill", "err", err)
	}

	// Bail hook: the pipeline calls this on MCP-health failure or parse
	// error. The handler signals the whole group and latches a flag so
	// the wait-loop can escalate to SIGKILL after the grace.
	var bailed atomic.Bool
	onBail := func(_ string) {
		if !bailed.CompareAndSwap(false, true) {
			return
		}
		if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
			logger.Warn("killProcessGroup on bail returned error", "err", err)
		}
	}

	// Step 9 + stderr goroutine: the NDJSON pipeline reads stdout to
	// EOF; a sibling goroutine mirrors stderr into slog. Both must drain
	// their pipes BEFORE cmd.Wait runs — os/exec's StdoutPipe docs are
	// explicit that calling Wait before all reads complete is incorrect
	// (a concurrent Wait closes the pipe while the scanner is still
	// reading, losing the last events).
	var (
		result      Result
		pipelineErr error
	)
	pipelineDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		defer close(pipelineDone)
		result, pipelineErr = Run(execCtx, stdout, instanceID, ticketUUID, logger, onBail)
	}()
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			logger.Info(scanner.Text(), "stream", "stderr")
		}
	}()

	// Step 10: wait for the pipeline to drain (stdout EOF, which
	// happens when the subprocess exits) with supervisor-shutdown
	// sequencing. Pipeline completion is the signal that all events
	// have been observed; cmd.Wait below reaps the subprocess.
	var (
		shutdownCtxErr    error
		shutdownSigkilled bool
	)
	select {
	case <-pipelineDone:
		// Subprocess wrote its final line and closed stdout — natural
		// exit (including the exec.CommandContext timeout path, which
		// routes through cmd.Cancel → killProcessGroup(SIGTERM) →
		// WaitDelay escalation, eventually closing stdout).
	case <-ctx.Done():
		shutdownCtxErr = ctx.Err()
		if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
			logger.Warn("killProcessGroup(SIGTERM) on shutdown returned error", "err", err)
		}
		select {
		case <-pipelineDone:
		case <-time.After(ShutdownSignalGrace):
			if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
				logger.Warn("killProcessGroup(SIGKILL) on shutdown returned error", "err", err)
			}
			shutdownSigkilled = true
			<-pipelineDone
		}
	}
	<-stderrDone

	// Now safe to reap. cmd.Wait closes stdout/stderr pipes but those
	// are already drained, so no data is lost; it just returns the
	// ProcessState.
	_ = cmd.Wait()

	if shutdownSigkilled && deps.SigkillEscalations != nil {
		deps.SigkillEscalations.Add(1)
	}

	if pipelineErr != nil && !result.ParseError {
		logger.Warn("pipeline.Run returned a non-parse error", "err", pipelineErr)
	}

	// Collect the wait-side detail Adjudicate needs.
	exitCode, sigName := extractExit(cmd.ProcessState)
	var execCtxErr error
	switch {
	case shutdownCtxErr != nil:
		execCtxErr = context.Canceled
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		execCtxErr = context.DeadlineExceeded
	}
	wait := WaitDetail{
		ContextErr:        execCtxErr,
		ShutdownInitiated: shutdownCtxErr != nil,
		ExitCode:          exitCode,
	}
	if sigName != "" && !wait.ShutdownInitiated && !errors.Is(execCtxErr, context.DeadlineExceeded) && !bailed.Load() {
		wait.Signaled = true
		wait.Signal = signalFromName(sigName)
	}

	// Step 11: post-run hello.txt check (deferred per plan §pipeline.
	// Adjudicate — only considered if nothing more urgent already matched).
	helloTxtOK := false
	if dept.WorkspacePath != nil && *dept.WorkspacePath != "" {
		helloTxtOK = checkHelloTxt(*dept.WorkspacePath, payload.TicketID)
	}

	status, exitReason := Adjudicate(result, wait, helloTxtOK)

	// Cost stays NULL unless a result event landed; that keeps the
	// aggregate cost query honest about what Claude actually billed.
	cost, _ := parseCostToNumeric(result.TotalCostUSD)
	if !result.ResultSeen {
		cost = pgtype.Numeric{}
	}

	logger.Info("claude subprocess terminal",
		"status", status,
		"exit_reason", exitReason,
		"total_cost_usd", result.TotalCostUSD,
		"result_seen", result.ResultSeen,
		"assistant_seen", result.AssistantSeen,
	)

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TerminalWriteGrace)
	defer cancel()
	insertTransition := status == "succeeded"
	return writeTerminalCost(termCtx, deps, instanceID, eventID, ticketUUID, status, exitReason, cost, insertTransition)
}

// checkHelloTxt reads workspace/hello.txt and returns true iff the contents,
// stripped of at most one trailing newline, equal ticketID exactly. Any
// read error (file missing, permission denied, etc.) yields false — the
// acceptance check is fail-closed.
func checkHelloTxt(workspacePath, ticketID string) bool {
	b, err := os.ReadFile(filepath.Join(workspacePath, "hello.txt"))
	if err != nil {
		return false
	}
	got := strings.TrimRight(string(b), "\n")
	return got == ticketID
}

// parseCostToNumeric converts Claude's json.Number-preserved decimal string
// into pgtype.Numeric via the pgx Scan path. Returns the zero Numeric and
// the error on failure so the caller can decide whether to surface or
// swallow it. An empty cost string yields the zero Numeric with nil error.
func parseCostToNumeric(cost string) (pgtype.Numeric, error) {
	if cost == "" {
		return pgtype.Numeric{}, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(cost); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// signalFromName maps the canonical SIG* string that signalName() emits
// back to syscall.Signal so FormatSignalled can render the exit_reason.
// Unknown strings return 0 and FormatSignalled falls through to its
// numeric shim ("signaled_signal_N").
func signalFromName(s string) syscall.Signal {
	switch s {
	case "SIGKILL":
		return syscall.SIGKILL
	case "SIGTERM":
		return syscall.SIGTERM
	case "SIGINT":
		return syscall.SIGINT
	case "SIGHUP":
		return syscall.SIGHUP
	case "SIGQUIT":
		return syscall.SIGQUIT
	case "SIGSEGV":
		return syscall.SIGSEGV
	case "SIGABRT":
		return syscall.SIGABRT
	case "SIGPIPE":
		return syscall.SIGPIPE
	default:
		return 0
	}
}

func int32Ptr(n int32) *int32 { return &n }

// -----------------------------------------------------------------------
// Terminal writers
// -----------------------------------------------------------------------

// Classification is the (status, exit_reason) pair written to agent_instances
// by the M1 fake-agent path. The M2.1 real path uses pipeline.Adjudicate
// instead; both paths share the underlying column vocabulary.
type Classification struct {
	Status     string
	ExitReason string
}

// Classify is the pure subprocess-exit → (status, exit_reason) function used
// by the M1 fake-agent path. Inputs intentionally avoid *os.ProcessState so
// unit tests exercise the mapping without fork/exec.
func Classify(exitCode int, sigName string, ctxErr error, shutdownSigkilled bool) Classification {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return Classification{Status: "timeout", ExitReason: "timeout"}
	case errors.Is(ctxErr, context.Canceled):
		if shutdownSigkilled {
			return Classification{Status: "failed", ExitReason: "supervisor_shutdown_sigkill"}
		}
		return Classification{Status: "failed", ExitReason: "supervisor_shutdown"}
	case sigName != "":
		return Classification{Status: "failed", ExitReason: "signal_" + sigName}
	case exitCode == 0:
		return Classification{Status: "succeeded", ExitReason: "exit_code_0"}
	default:
		return Classification{Status: "failed", ExitReason: fmt.Sprintf("exit_code_%d", exitCode)}
	}
}

// writeTerminal is the M1 two-statement terminal tx (UpdateInstanceTerminal
// + MarkEventProcessed). Preserved verbatim so the fake-agent path stays
// byte-identical with M1.
func writeTerminal(ctx context.Context, deps Deps, instanceID, eventID pgtype.UUID, status, exitReason string) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("spawn: begin terminal tx: %w", err)
	}
	q := deps.Queries.WithTx(tx)
	reason := exitReason
	if err := q.UpdateInstanceTerminal(ctx, store.UpdateInstanceTerminalParams{
		ID:         instanceID,
		Status:     status,
		ExitReason: &reason,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: UpdateInstanceTerminal: %w", err)
	}
	if err := q.MarkEventProcessed(ctx, eventID); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: MarkEventProcessed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("spawn: commit terminal: %w", err)
	}
	return nil
}

// writeTerminalCost is the M2.1 widened terminal tx: UpdateInstanceTerminal
// WithCost + (optional) InsertTicketTransition + (optional)
// UpdateTicketColumnSlug + MarkEventProcessed, all in a single
// transaction. The transition + column update only fire when
// insertTransition is true — i.e. on the succeeded path, per FR-114.
func writeTerminalCost(
	ctx context.Context,
	deps Deps,
	instanceID, eventID, ticketID pgtype.UUID,
	status, exitReason string,
	cost pgtype.Numeric,
	insertTransition bool,
) error {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("spawn: begin terminal tx: %w", err)
	}
	q := deps.Queries.WithTx(tx)
	reason := exitReason
	if err := q.UpdateInstanceTerminalWithCost(ctx, store.UpdateInstanceTerminalWithCostParams{
		ID:           instanceID,
		Status:       status,
		ExitReason:   &reason,
		TotalCostUsd: cost,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: UpdateInstanceTerminalWithCost: %w", err)
	}
	if insertTransition && ticketID.Valid {
		fromCol := "todo"
		if _, err := q.InsertTicketTransition(ctx, store.InsertTicketTransitionParams{
			TicketID:                   ticketID,
			FromColumn:                 &fromCol,
			ToColumn:                   "done",
			TriggeredByAgentInstanceID: instanceID,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: InsertTicketTransition: %w", err)
		}
		if err := q.UpdateTicketColumnSlug(ctx, store.UpdateTicketColumnSlugParams{
			ID:         ticketID,
			ColumnSlug: "done",
		}); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: UpdateTicketColumnSlug: %w", err)
		}
	}
	if err := q.MarkEventProcessed(ctx, eventID); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: MarkEventProcessed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("spawn: commit terminal: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------
// Shared helpers (unchanged from M1 except for location)
// -----------------------------------------------------------------------

func scanStream(wg *sync.WaitGroup, r io.ReadCloser, logger *slog.Logger, stream string) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		logger.Info(scanner.Text(), "stream", stream)
	}
}

func extractExit(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, signalName(ws.Signal())
	}
	return ps.ExitCode(), ""
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	default:
		return fmt.Sprintf("signal_%d", int(sig))
	}
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, err
	}
	return u, nil
}

func formatUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		u.Bytes[0:4],
		u.Bytes[4:6],
		u.Bytes[6:8],
		u.Bytes[8:10],
		u.Bytes[10:16],
	)
}
