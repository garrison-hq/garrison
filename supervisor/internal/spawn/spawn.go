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
	"sync"
	"syscall"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/concurrency"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ShutdownSignalGrace is the NFR-005 fixed window between the SIGTERM
// forwarded to a running subprocess on supervisor shutdown and the SIGKILL
// escalation that follows. Not operator-tunable in M1.
const ShutdownSignalGrace = 5 * time.Second

// Deps bundles Spawn's runtime collaborators. Constructed once in
// cmd/supervisor (T012) and handed to the dispatcher (T010) so every event
// invocation shares the same pool, logger, and config.
type Deps struct {
	Pool              *pgxpool.Pool
	Queries           *store.Queries
	FakeAgentCmd      string
	SubprocessTimeout time.Duration
	Logger            *slog.Logger
}

// Spawn handles one work.ticket.created event end-to-end per plan.md
// §"Subprocess lifecycle manager". Idempotent: a second call with the same
// event_id is a no-op via the LockEventForProcessing dedupe check, which is
// the guard against the LISTEN/poll race described in plan.md §"Dedupe on
// handling".
func Spawn(ctx context.Context, deps Deps, eventID pgtype.UUID) error {
	// Step 1 (dedupe + gate + insert in one short tx). Holding the FOR UPDATE
	// row-lock across the concurrency check and InsertRunningInstance means a
	// second handler replaying the same event_id cannot slip past the dedupe
	// guard even if the first handler hasn't yet reached step 7's terminal tx.
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

	// Deleted-department edge (data-model.md invariant #5): mark the event
	// processed with no agent_instances row, log once at error level, return.
	if _, err := q.GetDepartmentByID(ctx, deptUUID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("spawn: GetDepartmentByID: %w", err)
		}
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
			"reason", "department_missing",
		)
		return nil
	}

	// Step 2: concurrency gate. Defer (rollback + return nil) if blocked — the
	// fallback poller will pick the event up on a later cycle.
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

	// Step 3: insert running instance and commit the short tx.
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

	// Step 4: exec.CommandContext with subprocess-timeout context. The exec
	// ctx is deliberately detached from the supervisor shutdown ctx so the
	// SIGTERM→5s→SIGKILL shutdown sequence below can be driven manually per
	// plan.md step 8 rather than via exec's one-shot SIGKILL cancel.
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

	// Step 5: per-stream line-scanning goroutines, one slog record per line.
	var wg sync.WaitGroup
	wg.Add(2)
	go scanStream(&wg, stdout, logger, "stdout")
	go scanStream(&wg, stderr, logger, "stderr")

	// Steps 6 + 8: wait for exit; on shutdown, manual SIGTERM→SIGKILL.
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
	_ = exitErr // retained for future use; ProcessState carries what we need.

	// Determine why the exit happened — shutdown outranks subprocess timeout.
	var ctxErr error
	switch {
	case shutdownCtxErr != nil:
		ctxErr = context.Canceled
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		ctxErr = context.DeadlineExceeded
	}

	exitCode, signalName := extractExit(cmd.ProcessState)
	cls := Classify(exitCode, signalName, ctxErr, shutdownSigkilled)

	// Step 7: single terminal tx — UpdateInstanceTerminal + MarkEventProcessed.
	// Use the supervisor root ctx (not execCtx) so the terminal write still
	// runs during shutdown, up to the shutdown-grace budget enforced above.
	return writeTerminal(ctx, deps, instanceID, eventID, cls.Status, cls.ExitReason)
}

// Classification is the (status, exit_reason) pair written to agent_instances.
// Field names mirror the column names verbatim for grep-ability.
type Classification struct {
	Status     string
	ExitReason string
}

// Classify is the pure subprocess-exit → (status, exit_reason) function.
// Inputs intentionally avoid *os.ProcessState so unit tests exercise the
// mapping without fork/exec. Precedence: supervisor shutdown and subprocess
// timeout win over the raw exit code, because operators read those reasons
// as policy outcomes, not process accidents.
//
// ctxErr semantics: context.DeadlineExceeded = subprocess-timeout budget
// elapsed; context.Canceled = supervisor root ctx cancelled (shutdown).
// shutdownSigkilled is true only when the 5s SIGTERM grace expired and the
// supervisor escalated to SIGKILL.
func Classify(exitCode int, signalName string, ctxErr error, shutdownSigkilled bool) Classification {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return Classification{Status: "timeout", ExitReason: "timeout"}
	case errors.Is(ctxErr, context.Canceled):
		if shutdownSigkilled {
			return Classification{Status: "failed", ExitReason: "supervisor_shutdown_sigkill"}
		}
		return Classification{Status: "failed", ExitReason: "supervisor_shutdown"}
	case signalName != "":
		return Classification{Status: "failed", ExitReason: "signal_" + signalName}
	case exitCode == 0:
		return Classification{Status: "succeeded", ExitReason: "exit_code_0"}
	default:
		return Classification{Status: "failed", ExitReason: fmt.Sprintf("exit_code_%d", exitCode)}
	}
}

// writeTerminal writes the terminal agent_instances row update and the
// event_outbox.processed_at completion in a single transaction (FR-006).
// Shared tx is the atomicity guarantee: a crash between the two statements
// leaves the event replayable, never duplicate.
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

// scanStream reads one line at a time from r and emits an slog record per
// line. The goroutine exits when the pipe closes, which happens when the
// child process exits.
func scanStream(wg *sync.WaitGroup, r io.ReadCloser, logger *slog.Logger, stream string) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		logger.Info(scanner.Text(), "stream", stream)
	}
}

// extractExit pulls (exit_code, signal_name) from ps. Signal exit is reported
// as exit_code=-1 (os.ProcessState convention); signalName is non-empty only
// on signal exits.
func extractExit(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, signalName(ws.Signal())
	}
	return ps.ExitCode(), ""
}

// signalName returns the canonical SIG... form used in exit_reason values.
// syscall.Signal.String() is locale-ish ("killed", "terminated"); the SIG-
// prefixed form is what data-model.md §"exit_reason vocabulary" requires.
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

// parseUUID decodes a canonical-form UUID string into pgtype.UUID via the
// pgx Scan() path so the exact same representation round-trips into the db.
func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, err
	}
	return u, nil
}

// formatUUID renders a pgtype.UUID as its canonical 8-4-4-4-12 hex string for
// slog fields. Returns empty for !Valid; log sites tolerate empty.
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
