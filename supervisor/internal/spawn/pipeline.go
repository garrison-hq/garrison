package spawn

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"syscall"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/jackc/pgx/v5/pgtype"
)

// stdoutBufferMax is the 1 MiB per-line cap for Claude's stream-json output.
// Empirically most lines are small; tool_result payloads with large rows can
// approach tens of KiB. 1 MiB is comfortably above the observed max during
// the M2 spike (plan.md §pipeline.Run).
const stdoutBufferMax = 1 << 20

// Result is the aggregate view of everything pipeline.Run observed on a
// single invocation's stdout. Adjudicate consumes this plus the wait-side
// detail the caller collects to produce the terminal (status, exit_reason)
// written to agent_instances.
type Result struct {
	// Cost and terminal classification extracted from Claude's result event.
	// TotalCostUSD carries Claude's original decimal string (via json.Number)
	// so the pgtype.Numeric Scan path preserves full precision; the spawn
	// caller does the conversion when writing the terminal tx.
	TotalCostUSD   string
	TerminalReason string
	IsError        bool
	SessionID      string

	// Observational flags used by Adjudicate's precedence table. ResultSeen
	// being false on a cmd.Wait()==nil exit is the "no_result" terminal
	// (clarify Q3).
	ResultSeen    bool
	AssistantSeen bool

	// MCP init-check bail state. If MCPBailed is true, the offender details
	// rebuild the exit_reason via exitreason.FormatMCPFailure.
	MCPBailed         bool
	MCPOffenderName   string
	MCPOffenderStatus string

	// ParseError is set when claudeproto.Route returned an error — a
	// malformed NDJSON line is a fatal condition per FR-106.
	ParseError bool
}

// WaitDetail is everything Adjudicate needs to know about how the subprocess
// left the room, beyond what the NDJSON stream told us. The fields mirror
// what exec.Cmd.Wait() + ProcessState expose; the caller (spawn.Spawn)
// populates them from its own context and wait observation.
type WaitDetail struct {
	// ContextErr is execCtx.Err() at the moment cmd.Wait returned.
	// context.DeadlineExceeded ↔ subprocess-timeout budget elapsed.
	// context.Canceled         ↔ supervisor shutdown was triggered.
	ContextErr error

	// ShutdownInitiated is true when the supervisor root context cancelled
	// the subprocess rather than the per-invocation timeout context.
	// Distinct from ContextErr==Canceled because the timeout ctx is derived
	// from a fresh Background() in the M1 pattern, so Canceled on the
	// execCtx alone is not enough to distinguish shutdown from timeout.
	ShutdownInitiated bool

	// ExitCode is cmd.ProcessState.ExitCode(); -1 when terminated by signal.
	ExitCode int

	// Signal is the terminating signal (if Signaled==true). Zero when the
	// process exited cleanly.
	Signal syscall.Signal
	// Signaled is set when Claude was killed by an external signal AND the
	// kill did not originate from shutdown/timeout — those two paths already
	// outrank this one in the precedence table (plan §pipeline.Adjudicate).
	Signaled bool
}

// FinalizeState captures the M2.2.1 finalize-flow observations Adjudicate
// needs to classify a subprocess termination correctly. Populated by the
// pipeline's stream-json parser (T006) as `tool_use` events for
// `finalize_ticket` arrive. The zero value is "this role never interacts
// with finalize" — Adjudicate then falls back to M1/M2.1/M2.2 precedence
// unchanged.
//
// Expected is true only for roles that participate in the finalize flow
// (engineer and qa-engineer in M2.2.1). A non-finalize role (the M1/M2.1
// fake-agent path, the M2.2 engineer@todo back-compat path) leaves
// Expected=false and this struct has no effect on classification.
type FinalizeState struct {
	Expected     bool // role is expected to call finalize_ticket
	Attempted    bool // at least one finalize tool_use event observed
	Committed    bool // WriteFinalize successfully committed the atomic tx
	CapExhausted bool // 3rd failed attempt reached; SIGTERM queued by counter
}

// pipelineRouter implements claudeproto.Router and streams observations
// into a captured Result. It performs no I/O of its own beyond slog lines;
// the bail side effect (killProcessGroup) is delegated to the caller via
// the onBail callback that Run owns.
//
// M2.2 extension: pipelineRouter maintains an in-flight map of
// tool_use_id → tool_name for mempalace_* tool calls so the subsequent
// tool_result event can be logged as a follow-up pair. Observational only
// (FR-218a); no dispatch consequence.
type pipelineRouter struct {
	logger     *slog.Logger
	instanceID pgtype.UUID
	ticketID   pgtype.UUID
	result     *Result

	// mempalaceToolUse tracks outstanding mempalace_* tool_use_ids so
	// OnUser can emit a follow-up "outcome" slog line. Keyed by
	// tool_use_id. Cleared on observed tool_result; any entries still
	// in the map at EOF stay at outcome="pending" per FR-218 edge-case.
	mempalaceToolUse map[string]string

	// M2.2.1 T006: finalize retry counter + commit observer. Nil-safe —
	// when finalize is nil OR finalize.Expected is false, the router
	// behaves identically to M2.2 (no finalize observation). Populated
	// by Run from the FinalizeDeps argument.
	finalize *finalizeHook
	// finalizeToolUse tracks outstanding finalize_ticket tool_use_ids so
	// OnUser can distinguish finalize tool_results from mempalace_* and
	// other tool_results.
	finalizeToolUse map[string]struct{}
}

// finalizeHook is the internal wiring for T006/T007. It bundles the
// shared *FinalizeState (also visible to spawn.go's Adjudicate caller)
// with the OnCommit callback invoked on the first successful
// finalize_ticket tool_result (T007's WriteFinalize) and the onBail
// hook invoked on the 3rd failed attempt (counter-driven SIGTERM).
type finalizeHook struct {
	state     *FinalizeState
	attempts  int // per-spawn counter; feeds CapExhausted on hitting 3
	onCommit  func(payload json.RawMessage) error
	onBail    func(reason string)
	onObserve func() // optional: per-tool_use tick for log-assertion tests

	// toolUseInputs maps tool_use_id → raw input JSON so OnUser can
	// forward the original payload to OnCommit without re-parsing.
	toolUseInputs map[string][]byte
}

// FinalizeDeps is the public constructor argument Run accepts. Expected
// is the role-level toggle: false means M2.2 behaviour unchanged. State
// is a pointer because spawn.go reads the populated fields after Run
// returns (Adjudicate takes the populated FinalizeState).
type FinalizeDeps struct {
	Expected bool
	State    *FinalizeState
	// OnCommit is invoked synchronously from the stream parser on the
	// first successful finalize_ticket tool_result. Returning a non-nil
	// error is logged at error level and translates to
	// ExitFinalizeCommitFailed downstream; the stream parser continues
	// reading either way. payload carries the raw input that validated.
	OnCommit func(payload json.RawMessage) error
}

// isFinalizeToolName matches either the bare tool name or Claude's
// mcp__finalize__finalize_ticket prefix form (parallel to
// isMempalaceToolName).
func isFinalizeToolName(name string) bool {
	const prefix = "mcp__finalize__"
	if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
		name = name[len(prefix):]
	}
	return name == "finalize_ticket"
}

// isMempalaceToolName returns true if the tool name belongs to the
// MemPalace MCP surface (either direct mempalace_* or the
// mcp__mempalace__mempalace_* prefix Claude prepends to MCP tools).
func isMempalaceToolName(name string) bool {
	const prefix = "mcp__mempalace__"
	if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
		return true
	}
	const bare = "mempalace_"
	return len(name) >= len(bare) && name[:len(bare)] == bare
}

func (p *pipelineRouter) OnInit(_ context.Context, e claudeproto.InitEvent) claudeproto.RouterAction {
	healthy, offender, status := claudeproto.CheckMCPHealth(e.MCPServers)
	if !healthy {
		p.result.MCPBailed = true
		p.result.MCPOffenderName = offender
		p.result.MCPOffenderStatus = status
		p.logger.Error("mcp server not connected at init; bailing",
			"instance_id", uuidString(p.instanceID),
			"offender", offender,
			"status", status,
			"session_id", e.SessionID)
		return claudeproto.RouterActionBail
	}
	p.result.SessionID = e.SessionID
	p.logger.Info("claude init",
		"instance_id", uuidString(p.instanceID),
		"session_id", e.SessionID,
		"model", e.Model,
		"cwd", e.CWD,
		"tool_count", len(e.Tools),
		"mcp_server_count", len(e.MCPServers))
	return claudeproto.RouterActionContinue
}

func (p *pipelineRouter) OnAssistant(_ context.Context, e claudeproto.AssistantEvent) {
	p.result.AssistantSeen = true
	p.logger.Info("claude assistant event",
		"instance_id", uuidString(p.instanceID),
		"content_block_count", e.ContentBlockCount,
		"content_types", e.ContentTypes,
		"model", e.Model)

	// FR-218 / NFR-210: structured log line for each mempalace_* tool_use.
	// Observational only (FR-218a); no dispatch consequence. Paired with
	// a follow-up "outcome" line in OnUser when the tool_result arrives.
	for _, tu := range e.ToolUses {
		if isMempalaceToolName(tu.Name) {
			if p.mempalaceToolUse == nil {
				p.mempalaceToolUse = make(map[string]string, 4)
			}
			p.mempalaceToolUse[tu.ToolUseID] = tu.Name
			p.logger.Info("mempalace tool_use",
				"instance_id", uuidString(p.instanceID),
				"ticket_id", uuidString(p.ticketID),
				"tool_name", tu.Name,
				"tool_use_id", tu.ToolUseID,
				"outcome", "pending",
			)
			continue
		}
		// M2.2.1 FR-276: info-level structured log for every
		// finalize_ticket tool_use. Counter increments only while
		// committed==false (post-commit tool_use events are logged but
		// do not increment per FR-257). Track tool_use_id so OnUser
		// can match the paired tool_result.
		if isFinalizeToolName(tu.Name) && p.finalize != nil && p.finalize.state != nil {
			if p.finalizeToolUse == nil {
				p.finalizeToolUse = make(map[string]struct{}, 4)
			}
			p.finalizeToolUse[tu.ToolUseID] = struct{}{}
			if p.finalize.toolUseInputs == nil {
				p.finalize.toolUseInputs = make(map[string][]byte, 4)
			}
			// Copy the input raw bytes; claudeproto may reuse buffers.
			if len(tu.InputRaw) > 0 {
				buf := make([]byte, len(tu.InputRaw))
				copy(buf, tu.InputRaw)
				p.finalize.toolUseInputs[tu.ToolUseID] = buf
			}
			if !p.finalize.state.Committed {
				p.finalize.state.Attempted = true
				// Counter ticks on the matching tool_result (OnUser),
				// not here — a tool_use without a tool_result is
				// incomplete and should not consume an attempt per
				// plan §"Subsystem state machines > Finalize attempt
				// state machine".
			}
			p.logger.Info("finalize tool_use",
				"instance_id", uuidString(p.instanceID),
				"ticket_id", uuidString(p.ticketID),
				"tool_use_id", tu.ToolUseID,
				"attempt_pending", p.finalize.attempts+1,
				"committed", p.finalize.state.Committed,
			)
			if p.finalize.onObserve != nil {
				p.finalize.onObserve()
			}
		}
	}
}

func (p *pipelineRouter) OnUser(_ context.Context, e claudeproto.UserEvent) {
	for _, tr := range e.ToolResults {
		// FR-218 follow-up: resolve outcome for any pending mempalace_*
		// tool_use in the in-flight map. Observational; log then clear.
		if name, ok := p.mempalaceToolUse[tr.ToolUseID]; ok {
			outcome := "ok"
			if tr.IsError {
				outcome = "error"
			}
			p.logger.Info("mempalace tool_result",
				"instance_id", uuidString(p.instanceID),
				"ticket_id", uuidString(p.ticketID),
				"tool_name", name,
				"tool_use_id", tr.ToolUseID,
				"outcome", outcome,
				"detail", tr.Detail,
			)
			delete(p.mempalaceToolUse, tr.ToolUseID)
		}

		// M2.2.1 T006: finalize_ticket tool_result handling. The
		// tool_result's Detail carries the server's envelope body,
		// which is `{"ok":bool,"attempt":N,...}` stringified. Parse
		// to decide whether to tick the counter (on ok=false) or fire
		// the commit callback (on ok=true, first occurrence only).
		if _, ok := p.finalizeToolUse[tr.ToolUseID]; ok {
			p.handleFinalizeToolResult(tr)
			delete(p.finalizeToolUse, tr.ToolUseID)
			continue
		}

		if tr.IsError {
			p.logger.Warn("claude tool_result reported error",
				"instance_id", uuidString(p.instanceID),
				"tool_use_id", tr.ToolUseID,
				"detail", tr.Detail)
		}
	}
}

// handleFinalizeToolResult parses a finalize_ticket tool_result envelope
// and enacts the state transitions:
//   - ok=true, not yet committed: fire onCommit, mark Committed=true
//   - ok=false, not yet committed: increment counter, if >= 3 mark
//     CapExhausted=true and trigger onBail("finalize_invalid")
//   - any ok value post-commit: log-only (no counter tick, no bail)
//
// Unparseable envelopes are logged and treated as failed attempts.
func (p *pipelineRouter) handleFinalizeToolResult(tr claudeproto.ToolResultSummary) {
	if p.finalize == nil || p.finalize.state == nil {
		return
	}
	var body struct {
		Ok        bool   `json:"ok"`
		Attempt   int    `json:"attempt"`
		ErrorType string `json:"error_type,omitempty"`
		Field     string `json:"field,omitempty"`
		Message   string `json:"message,omitempty"`
	}
	if tr.Detail != "" {
		_ = json.Unmarshal([]byte(tr.Detail), &body)
	}

	// Post-commit: log-only, no counter tick.
	if p.finalize.state.Committed {
		p.logger.Info("finalize tool_result (post-commit)",
			"instance_id", uuidString(p.instanceID),
			"ticket_id", uuidString(p.ticketID),
			"tool_use_id", tr.ToolUseID,
			"ok", body.Ok,
			"error_type", body.ErrorType,
			"field", body.Field,
		)
		return
	}

	p.finalize.attempts++
	p.logger.Info("finalize tool_result",
		"instance_id", uuidString(p.instanceID),
		"ticket_id", uuidString(p.ticketID),
		"tool_use_id", tr.ToolUseID,
		"attempt", p.finalize.attempts,
		"ok", body.Ok,
		"error_type", body.ErrorType,
		"field", body.Field,
	)

	if body.Ok {
		// Fire the commit callback with the original tool_use input.
		payload := p.finalize.toolUseInputs[tr.ToolUseID]
		p.finalize.state.Committed = true
		if p.finalize.onCommit != nil {
			if err := p.finalize.onCommit(payload); err != nil {
				// Commit callback failures (T007 rollbacks, etc.) are
				// logged here; T007's WriteFinalize writes the matching
				// terminal row via its own error paths so we don't
				// double-book.
				p.logger.Error("finalize onCommit returned error",
					"instance_id", uuidString(p.instanceID),
					"ticket_id", uuidString(p.ticketID),
					"err", err)
			}
		}
		return
	}

	// Failed attempt. Cap enforcement: 3 attempts → bail.
	if p.finalize.attempts >= 3 {
		p.finalize.state.CapExhausted = true
		p.logger.Warn("finalize cap exhausted; signalling bail",
			"instance_id", uuidString(p.instanceID),
			"ticket_id", uuidString(p.ticketID),
			"attempts", p.finalize.attempts)
		if p.finalize.onBail != nil {
			p.finalize.onBail(ExitFinalizeInvalid)
		}
	}
}

func (p *pipelineRouter) OnRateLimit(_ context.Context, e claudeproto.RateLimitEvent) {
	p.logger.Warn("claude rate_limit_event",
		"instance_id", uuidString(p.instanceID),
		"session_id", e.SessionID,
		"uuid", e.UUID,
		"status", e.Info.Status,
		"resets_at", e.Info.ResetsAt,
		"rate_limit_type", e.Info.RateLimitType,
		"utilization", e.Info.Utilization,
		"is_using_overage", e.Info.IsUsingOverage,
		"surpassed_threshold", e.Info.SurpassedThreshold)
}

func (p *pipelineRouter) OnResult(_ context.Context, e claudeproto.ResultEvent) {
	p.result.ResultSeen = true
	p.result.IsError = e.IsError
	p.result.TerminalReason = e.TerminalReason
	p.result.TotalCostUSD = e.TotalCostUSD.String()
	if e.SessionID != "" {
		p.result.SessionID = e.SessionID
	}
	p.logger.Info("claude result event",
		"instance_id", uuidString(p.instanceID),
		"terminal_reason", e.TerminalReason,
		"is_error", e.IsError,
		"total_cost_usd", e.TotalCostUSD.String(),
		"duration_ms", e.DurationMS,
		"permission_denials", e.PermissionDenials)
}

func (p *pipelineRouter) OnTaskStarted(_ context.Context, _ claudeproto.TaskStartedEvent) {
	p.logger.Debug("claude task_started",
		"instance_id", uuidString(p.instanceID))
}

func (p *pipelineRouter) OnUnknown(_ context.Context, e claudeproto.UnknownEvent) {
	p.logger.Warn("claude unknown event",
		"instance_id", uuidString(p.instanceID),
		"type", e.Type,
		"subtype", e.Subtype,
		"raw", string(e.Raw))
}

// Run consumes Claude's stream-json stdout line by line, dispatches each
// event through claudeproto.Route, and accumulates a Result until EOF or
// a bail. When Route returns RouterActionBail or a parse error, Run calls
// onBail(reason) — the caller installs the killProcessGroup closure there
// — then finishes draining its current buffer and returns. Run does not
// close stdout; the caller owns the Pipe lifecycle.
//
// ctx is not used for cancelling the read (cmd.Wait handles that once the
// process exits); it is threaded through to Route for future extensions.
func Run(
	ctx context.Context,
	stdout io.Reader,
	instanceID pgtype.UUID,
	ticketID pgtype.UUID,
	logger *slog.Logger,
	onBail func(reason string),
	finalize FinalizeDeps,
) (Result, error) {
	if logger == nil {
		return Result{}, errors.New("pipeline: logger is required")
	}
	result := Result{}
	r := &pipelineRouter{
		logger:     logger,
		instanceID: instanceID,
		ticketID:   ticketID,
		result:     &result,
	}
	// M2.2.1 T006: wire the finalize observer when the role expects
	// finalize. finalize.State must be non-nil so spawn.go's Adjudicate
	// call can read the populated state after Run returns. OnBail
	// defaults to the outer onBail so the counter-driven cap-exhaustion
	// path reuses the same SIGTERM infra as MCP-bail.
	if finalize.Expected && finalize.State != nil {
		r.finalize = &finalizeHook{
			state:    finalize.State,
			onCommit: finalize.OnCommit,
			onBail:   onBail,
		}
		finalize.State.Expected = true
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), stdoutBufferMax)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Copy the scanner's internal buffer — claudeproto keeps the Raw
		// slice for logging, and the scanner reuses its buffer on the next
		// call.
		buf := make([]byte, len(line))
		copy(buf, line)

		action, err := claudeproto.Route(ctx, buf, r)
		if err != nil {
			result.ParseError = true
			logger.Error("claude stream parse error; bailing",
				"instance_id", uuidString(instanceID),
				"err", err,
				"line", string(buf))
			if onBail != nil {
				onBail("parse_error")
			}
			return result, fmt.Errorf("pipeline: %w", err)
		}
		if action == claudeproto.RouterActionBail {
			reason := "bail"
			if result.MCPBailed {
				reason = FormatMCPFailure(result.MCPOffenderName, result.MCPOffenderStatus)
			}
			if onBail != nil {
				onBail(reason)
			}
			// Keep draining so we collect any follow-on events the
			// subprocess may emit before it exits — but stop on the next
			// read error, which is how the drain naturally terminates once
			// the process dies.
			continue
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("pipeline: scan: %w", err)
	}
	return result, nil
}

// Adjudicate maps (observed stream state, wait-side detail, post-run hello
// check) to (status, exit_reason). The precedence table is evaluated
// top-to-bottom; the first matching row wins — runtime-failure causes the
// supervisor directly observed outrank interpretations of Claude's own
// output, which outrank post-run artifact checks (plan §pipeline.Adjudicate).
//
// Adjudicate does not touch pgtype.Numeric cost parsing — the caller
// converts result.TotalCostUSD into pgtype.Numeric separately, because
// failure classes still capture a cost when one was emitted (claude_error,
// acceptance_failed) but a pre-result failure (mcp bail, parse, timeout,
// shutdown, no_result, signal) writes NULL.
func Adjudicate(result Result, wait WaitDetail, helloTxtOK bool, finalize FinalizeState) (status, exitReason string) {
	switch {
	case result.MCPBailed:
		return "failed", FormatMCPFailure(result.MCPOffenderName, result.MCPOffenderStatus)
	case result.ParseError:
		return "failed", ExitParseError
	case wait.ShutdownInitiated:
		return "failed", ExitSupervisorShutdown
	case errors.Is(wait.ContextErr, context.DeadlineExceeded):
		// M2.2.1 precedence (T002): timeout wins over finalize_never_called.
		// A subprocess killed by the per-invocation timeout always lands
		// here, regardless of whether finalize was expected.
		return "timeout", ExitTimeout
	case wait.Signaled && finalize.CapExhausted && result.ResultSeen && isBudgetTerminalReason(result.TerminalReason):
		// M2.2.1 precedence (T002 / SC-258): budget_exceeded wins over
		// finalize_invalid when the subprocess reports a budget-shaped
		// result before/during the retry counter's SIGTERM. Keeps M2.2's
		// budget surface stable under the M2.2.1 retry-loop scenarios.
		return "failed", ExitBudgetExceeded
	case wait.Signaled && finalize.CapExhausted:
		// M2.2.1 (T006 / FR-257): the retry counter SIGTERMed the process
		// group after three failed finalize attempts. Preferred over the
		// generic signaled_SIGTERM label because the root cause is the
		// schema-validation loop, not an external signal.
		return "failed", ExitFinalizeInvalid
	case wait.Signaled:
		return "failed", FormatSignalled(wait.Signal)
	case !result.ResultSeen:
		return "failed", ExitNoResult
	case result.IsError:
		return "failed", ExitClaudeError
	case result.ResultSeen && isBudgetTerminalReason(result.TerminalReason):
		// M2.2 / FR-220: terminal result reports the --max-budget-usd
		// was exceeded. Happens with is_error=false when Claude wraps
		// up mid-turn under a budget ceiling; kept ABOVE the hello.txt
		// check because a truncated run shouldn't be re-classified as
		// acceptance_failed on a missing artefact — it's a cost issue.
		// The exact TerminalReason string on 2.1.117 is not spike-pinned;
		// case-insensitive substring match on "budget" is the defensive
		// shim per plan §"Error vocabulary". Real-Claude observations
		// feed back into a tighter enum post-M2.2.
		return "failed", ExitBudgetExceeded
	case finalize.Expected && !finalize.Committed:
		// M2.2.1 (T002 / US5): the role was expected to finalize but the
		// subprocess exited cleanly without a successful finalize commit.
		// Distinct from finalize_invalid (which implies retries occurred)
		// — finalize_never_called means zero attempts OR attempts that
		// never converged to a committed outcome prior to clean exit.
		return "failed", ExitFinalizeNeverCalled
	case !helloTxtOK:
		return "failed", ExitAcceptanceFailed
	default:
		return "succeeded", ExitCompleted
	}
}

// isBudgetTerminalReason returns true if the string looks like a
// budget-overrun signal from Claude 2.1.117. Case-insensitive substring
// match on "budget" — narrow enough to avoid false positives on
// "completed"/"end_turn" but permissive to catch variants until the
// exact enum value is pinned through observation.
func isBudgetTerminalReason(s string) bool {
	for i := 0; i+6 <= len(s); i++ {
		c0 := toLowerASCII(s[i])
		if c0 != 'b' {
			continue
		}
		if toLowerASCII(s[i+1]) == 'u' &&
			toLowerASCII(s[i+2]) == 'd' &&
			toLowerASCII(s[i+3]) == 'g' &&
			toLowerASCII(s[i+4]) == 'e' &&
			toLowerASCII(s[i+5]) == 't' {
			return true
		}
	}
	return false
}

func toLowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}

// uuidString formats a pgtype.UUID for structured log context. Kept local
// to the pipeline package so the dependency on pgtype does not leak into
// claudeproto. Returns an empty string for invalid UUIDs so log lines do
// not carry bogus values.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	// 8-4-4-4-12 hex rendering without a third-party dependency.
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i, x := range b {
		switch i {
		case 4, 6, 8, 10:
			out[j] = '-'
			j++
		}
		out[j] = hex[x>>4]
		out[j+1] = hex[x&0x0f]
		j += 2
	}
	return string(out)
}
