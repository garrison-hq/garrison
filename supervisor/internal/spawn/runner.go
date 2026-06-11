package spawn

// runner.go factors steps 9–12 of the real-Claude spawn flow (the NDJSON
// pipeline + stderr mirror + shutdown sequencing + wait detail + acceptance
// gate + finalize short-circuit + Adjudicate + terminal write) out of
// spawn.go's runRealClaude into a transport-parameterized session runner
// (M7.1 T009). Direct-exec wraps the legacy process-group kill ladder and
// the drain-then-cmd.Wait reap; the M7.1 container path (T011) supplies a
// docker-exec-backed transport. The runner is behavior-identical to the
// pre-T009 inline block — the existing spawn/pipeline test suites are its
// acceptance gate.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// transport abstracts where a claude session's byte streams come from and
// how the supervisor terminates and reaps it. Direct-exec wires the
// subprocess pipes + killProcessGroup; the container path (T011) wires the
// demuxed exec streams + Controller.Restart.
type transport struct {
	// Stdout carries the stream-json NDJSON feed the pipeline consumes.
	Stdout io.Reader
	// Stderr is mirrored line-by-line into slog.
	Stderr io.Reader
	// Terminate asks the transport to stop the session. escalate=false is
	// the SIGTERM-equivalent first ask; escalate=true is the
	// SIGKILL-equivalent backstop fired after ShutdownSignalGrace.
	Terminate func(escalate bool) error
	// ExitDetail reaps the session and reports how it left. The runner
	// calls it only after BOTH the pipeline and stderr readers have
	// drained to EOF (concurrency rule 8: never Wait before all pipe
	// reads complete). The transport reports the exit code, terminating
	// signal, and the timeout-deadline ContextErr it can observe itself;
	// the runner overlays the shutdown→Canceled mapping plus the
	// Signaled precedence gating, since it owns the shutdown select and
	// the bail latch.
	ExitDetail func(ctx context.Context) WaitDetail
}

// sessionParams carries what the pre-T009 inline block in runRealClaude
// closed over: the invocation identifiers, role + origin column, the
// resolved agent and department rows, wake-up status, finalize
// expectations, transition columns, and the acceptance-gate workspace
// path (on the container transport this is the agent-ID-keyed dir, not
// the per-department path).
type sessionParams struct {
	InstanceID   pgtype.UUID
	EventID      pgtype.UUID
	TicketUUID   pgtype.UUID
	TicketIDText string
	RoleSlug     string
	// OriginColumn is the ticket's column at spawn time (the event
	// payload's column_slug); drives the acceptance-gate skip table.
	OriginColumn string
	Agent        agents.Agent
	Dept         store.Department
	WakeUpStatus mempalace.Status

	// FinalizeExpected + FromCol/ToCol are precomputed by the caller via
	// finalizeExpectedForRole / transitionColumns so both transports
	// share one derivation site.
	FinalizeExpected bool
	FromCol          string
	ToCol            string

	// WorkspacePath is the acceptance-gate directory for the M1
	// hello.txt fallback check. Empty disables the file check (matching
	// the legacy nil/empty dept.WorkspacePath behavior).
	WorkspacePath string

	// Bailed is the transport-side bail latch: true once the bail hook
	// (MCP-gate / parse-error) terminated the session, which suppresses
	// the Signaled classification exactly like the pre-T009 block did.
	// OnBail is handed to the pipeline policy unchanged.
	Bailed *atomic.Bool
	OnBail func(reason string)

	Logger *slog.Logger
}

// runClaudeSession drives one claude session over the supplied transport:
// pipeline goroutine + stderr mirror + shutdown select + exit detail +
// acceptance gate + finalize-committed short-circuit + Adjudicate +
// terminal write. ctx is the dispatcher context — its cancellation means
// supervisor shutdown and triggers the Terminate(false→true) ladder.
//
// execCtx is the per-invocation timeout context the pipeline and
// finalize writes run against. It is deliberately distinct from ctx:
// the caller detaches it from the parent via context.WithoutCancel so
// shutdown sequencing — not abrupt cancellation — drives session
// teardown. nil falls back to ctx.
func runClaudeSession(ctx, execCtx context.Context, deps Deps, t transport, p sessionParams) error {
	logger := p.Logger
	if execCtx == nil {
		execCtx = ctx
	}

	var (
		result      Result
		pipelineErr error
	)
	pipelineDone := make(chan struct{})
	stderrDone := make(chan struct{})

	// M2.2.1 T011: wire the finalize observer. For (role, column) pairs
	// outside the finalize flow FinalizeExpected is false and Run's
	// finalize branches short-circuit, preserving M2.2 behavior.
	finalizeState := &FinalizeState{Expected: p.FinalizeExpected}
	onCommit := finalizeOnCommit(execCtx, deps, p, &result, logger)

	// Step 9 + stderr goroutine: the NDJSON pipeline reads stdout to
	// EOF; a sibling goroutine mirrors stderr into slog. Both must drain
	// their streams BEFORE the transport's ExitDetail reaps the session —
	// os/exec's StdoutPipe docs are explicit that calling Wait before all
	// reads complete is incorrect (a concurrent Wait closes the pipe
	// while the scanner is still reading, losing the last events).
	go func() {
		defer close(pipelineDone)
		result = Result{}
		policy := NewFinalizePolicy(logger, p.InstanceID, p.TicketUUID, &result, FinalizeDeps{
			Expected:            p.FinalizeExpected,
			State:               finalizeState,
			OnCommit:            onCommit,
			ResultGrace:         deps.FinalizeResultGrace,
			OnRateLimitRejected: rateLimitHook(deps, p, &result, logger),
		}, p.OnBail)
		result, pipelineErr = Run(execCtx, t.Stdout, policy, logger)
	}()
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(t.Stderr)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			logger.Info(scanner.Text(), "stream", "stderr")
		}
	}()

	// Step 10: wait for the pipeline to drain (stdout EOF, which happens
	// when the session ends) with supervisor-shutdown sequencing.
	// Pipeline completion is the signal that all events have been
	// observed; ExitDetail below reaps the session.
	shutdownCtxErr, shutdownSigkilled := awaitPipelineDrain(ctx, t, pipelineDone, logger)
	<-stderrDone

	// Now safe to reap: both streams are fully drained (concurrency rule
	// 8 — pipelineDone and stderrDone have both closed), so ExitDetail
	// closing the underlying pipes loses no data.
	wait := t.ExitDetail(ctx)

	if shutdownSigkilled && deps.SigkillEscalations != nil {
		deps.SigkillEscalations.Add(1)
	}

	if pipelineErr != nil && !result.ParseError {
		logger.Warn("pipeline.Run returned a non-parse error", "err", pipelineErr)
	}

	wait = overlayRunnerWaitDetail(wait, shutdownCtxErr, p.Bailed)

	// Step 11: post-run acceptance gate. M2.2+ roles trust the terminal
	// result event + mempalace writes; the M1 hello.txt fallback only
	// runs for (role, column) pairs outside the skip table and only when
	// a workspace path exists.
	helloTxtOK := acceptanceGateSatisfied(p.RoleSlug, p.OriginColumn)
	if !helloTxtOK && p.WorkspacePath != "" {
		helloTxtOK = checkHelloTxt(p.WorkspacePath, p.TicketIDText)
	}

	// M2.2.1 T011: if the pipeline's OnCommit already committed the
	// atomic write, WriteFinalize wrote the terminal agent_instances row
	// inside its own transaction — we MUST NOT call
	// writeTerminalCostAndWakeup again (double-write the terminal row +
	// attempt a second InsertTicketTransition). The session's
	// post-commit events were already observed + logged by the pipeline;
	// nothing more to do for this spawn.
	if finalizeState.Committed {
		logger.Info("finalize already committed atomic tx; skipping M2.1 terminal write",
			"ticket_id", p.TicketIDText,
			"instance_id", formatUUID(p.InstanceID),
		)
		return nil
	}

	// Step 12: Adjudicate receives a snapshot of the finalize state so
	// the precedence rows (budget > finalize_invalid, timeout >
	// finalize_never_called, etc.) fire correctly.
	status, exitReason := Adjudicate(result, wait, helloTxtOK, *finalizeState)

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

	return writeTerminalCostAndWakeup(termCtx, deps, terminalWriteParams{
		InstanceID:       p.InstanceID,
		EventID:          p.EventID,
		TicketID:         p.TicketUUID,
		Status:           status,
		ExitReason:       exitReason,
		Cost:             cost,
		WakeUpStatus:     string(p.WakeUpStatus),
		InsertTransition: insertTransition,
		FromCol:          p.FromCol,
		ToCol:            p.ToCol,
	})
}

// finalizeOnCommit builds the pipeline's OnCommit hook: validate the
// finalize payload and run the atomic WriteFinalize transaction.
// result points at the runner's pipeline result variable — the hook
// reads the cost fields the stream parser has accumulated so far
// (OnCommit fires on the finalize tool_result, typically before the
// result event, so a NULL cost here is correct; any later cost signal
// is log-observed only).
func finalizeOnCommit(execCtx context.Context, deps Deps, p sessionParams, result *Result, logger *slog.Logger) func(json.RawMessage) error {
	return func(rawPayload json.RawMessage) error {
		if deps.Palace == nil {
			logger.Error("finalize onCommit: deps.Palace is nil; skipping atomic write")
			return fmt.Errorf("finalize: no palace client wired")
		}
		parsed, verr := finalize.Validate(rawPayload)
		if verr != nil {
			// The finalize MCP server already validated this payload
			// before it sent ok=true, so a re-validation failure here
			// indicates a schema drift between server and spawn — a bug.
			logger.Error("finalize onCommit: re-validation failed",
				"err", verr.Error(),
				"field", verr.Field)
			return fmt.Errorf("finalize re-validate: %w", verr)
		}
		cost, _ := parseCostToNumeric(result.TotalCostUSD)
		if !result.ResultSeen {
			cost = pgtype.Numeric{}
		}
		wing := ""
		if p.Agent.PalaceWing != nil {
			wing = *p.Agent.PalaceWing
		}
		return WriteFinalize(execCtx, FinalizeWriteDeps{
			Pool:         deps.Pool,
			Queries:      deps.Queries,
			Palace:       deps.Palace,
			Logger:       logger,
			WriteTimeout: deps.FinalizeWriteTimeout,
		}, parsed, FinalizeMeta{
			AgentInstanceID: p.InstanceID,
			TicketID:        p.TicketUUID,
			EventID:         p.EventID,
			Wing:            wing,
			FromColumn:      p.FromCol,
			ToColumn:        p.ToCol,
			Cost:            cost,
			WakeUpStatus:    string(p.WakeUpStatus),
		})
	}
}

// rateLimitHook builds the M6 T008 rate-limit pause actuator, or nil
// when the spawn has no company scope / throttle pool. The FirePause
// call rides a short independent tx so the in-flight stream read isn't
// blocked; on FirePause failure the closure logs at warn level and the
// spawn keeps running per FR-043. result points at the runner's
// pipeline result variable (the cost detail read at fire time).
func rateLimitHook(deps Deps, p sessionParams, result *Result, logger *slog.Logger) func(context.Context, claudeproto.RateLimitEvent) {
	if !p.Dept.CompanyID.Valid || deps.Throttle.Pool == nil {
		return nil
	}
	companyID := p.Dept.CompanyID
	throttleDeps := deps.Throttle
	return func(rlCtx context.Context, e claudeproto.RateLimitEvent) {
		detail := throttle.RateLimitDetail{
			Status:        e.Info.Status,
			RateLimitType: e.Info.RateLimitType,
			TotalCostUSD:  result.TotalCostUSD,
		}
		if err := pgx.BeginFunc(rlCtx, throttleDeps.Pool, func(tx pgx.Tx) error {
			return throttle.FirePause(rlCtx, throttleDeps, store.New(tx), companyID, detail)
		}); err != nil {
			logger.Warn("throttle: FirePause failed; in-flight spawn continues",
				"company_id", formatUUID(companyID),
				"err", err)
		}
	}
}

// awaitPipelineDrain blocks until the pipeline goroutine drains stdout
// to EOF, applying the supervisor-shutdown Terminate(false→true) ladder
// when ctx (the dispatcher context) cancels first. It returns the
// shutdown ctx error (nil on a natural exit) and whether the
// SIGKILL-equivalent escalation rung fired. On return pipelineDone has
// closed — the caller still owns the stderr drain and the ExitDetail
// reap ordering (concurrency rule 8).
func awaitPipelineDrain(ctx context.Context, t transport, pipelineDone <-chan struct{}, logger *slog.Logger) (shutdownCtxErr error, shutdownSigkilled bool) {
	select {
	case <-pipelineDone:
		// Session wrote its final line and closed stdout — natural exit
		// (including the timeout path, which the transport terminates
		// itself, eventually closing stdout).
	case <-ctx.Done():
		shutdownCtxErr = ctx.Err()
		if err := t.Terminate(false); err != nil {
			logger.Warn("transport terminate on shutdown returned error", "err", err)
		}
		select {
		case <-pipelineDone:
		case <-time.After(ShutdownSignalGrace):
			if err := t.Terminate(true); err != nil {
				logger.Warn("transport terminate escalation on shutdown returned error", "err", err)
			}
			shutdownSigkilled = true
			<-pipelineDone
		}
	}
	return shutdownCtxErr, shutdownSigkilled
}

// overlayRunnerWaitDetail overlays the runner-owned wait detail on the
// transport's observation: shutdown outranks the transport's deadline
// observation, and a terminating signal only classifies as Signaled
// when it didn't originate from shutdown, timeout, or the bail hook —
// those paths outrank it in Adjudicate's precedence table.
func overlayRunnerWaitDetail(wait WaitDetail, shutdownCtxErr error, bailed *atomic.Bool) WaitDetail {
	if shutdownCtxErr != nil {
		wait.ContextErr = context.Canceled
		wait.ShutdownInitiated = true
	}
	if wait.Signaled &&
		(wait.ShutdownInitiated ||
			errors.Is(wait.ContextErr, context.DeadlineExceeded) ||
			(bailed != nil && bailed.Load())) {
		wait.Signaled = false
		wait.Signal = 0
	}
	return wait
}
