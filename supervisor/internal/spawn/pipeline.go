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
	"time"

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

// Policy abstracts pipeline.Run's lifecycle hooks so the finalize-shaped
// (existing M2.2.1+) and chat-shaped (M5.1+) flows can share the scanner
// loop without forking it. Embeds claudeproto.Router; OnTerminate is the
// new hook Run calls on parse error / read error / bail.
//
// Result returns whatever the policy considers the canonical "scan
// summary" — for FinalizePolicy it's the accumulated Result struct
// spawn.go's Adjudicate consumes; for ChatPolicy it's a zero Result
// because chat has its own terminal-write path (chat_messages row
// commit) and doesn't use Adjudicate.
type Policy interface {
	claudeproto.Router
	// OnTerminate is invoked once when Run is about to return because of
	// a parse error, scanner read error, or RouterActionBail from a
	// previous event. reason is one of:
	//   "parse_error" — claudeproto.Route returned a non-nil error
	//   "bail"        — a router method returned RouterActionBail
	// The policy decides what to do (FinalizePolicy invokes its onBail
	// closure with a derived exit_reason; ChatPolicy terminal-writes
	// the assistant chat_messages row).
	OnTerminate(ctx context.Context, reason string)
	// Result returns the accumulated scan summary the caller asked for.
	// FinalizePolicy returns its tracked Result; ChatPolicy returns a
	// zero Result.
	Result() Result
}

// FinalizePolicy is the M2.2.1+ Router/Policy implementation: streams
// observations into a captured Result, dispatches mempalace_* and
// finalize_ticket tool_use/tool_result pairs, and (when configured)
// invokes the WriteFinalize commit callback on the first ok=true
// finalize tool_result.
//
// Renamed from FinalizePolicy in M5.1 to make space for ChatPolicy
// alongside it; the struct and all methods are byte-for-byte the M2.2.1
// implementation, just promoted from package-private to package-exported
// so chat can reference the type assertion.
type FinalizePolicy struct {
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

	// bailFn is the supervisor's killProcessGroup closure (or a
	// counter-driven SIGTERM closure for finalize cap exhaustion).
	// Stored at the policy level (not just finalizeHook) so OnTerminate
	// can invoke it for non-finalize roles too. Pre-M5.1 the same
	// closure was passed as a separate Run() parameter; the Policy
	// refactor consolidates lifecycle state into the policy struct.
	bailFn func(reason string)
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

	// M6 T006: result-grace fields. resultGrace > 0 enables the
	// deferred-commit path — handleFinalizeToolResult marks Committed
	// = true + stashes the validated payload + sets pendingCommit, but
	// does NOT call onCommit immediately. Run() consumes the pending-
	// commit signal, waits up to resultGrace for the result event to
	// land naturally (so result.TotalCostUSD is populated), and then
	// fires onCommit. resultGrace == 0 preserves the M2.2.1 synchronous
	// commit shape for tests + flows that don't carry a Deps with the
	// new field set.
	resultGrace    time.Duration
	pendingCommit  bool
	pendingPayload json.RawMessage
}

// FinalizeDeps is the public constructor argument Run accepts. Expected
// is the role-level toggle: false means M2.2 behaviour unchanged. State
// is a pointer because spawn.go reads the populated fields after Run
// returns (Adjudicate takes the populated FinalizeState).
type FinalizeDeps struct {
	Expected bool
	State    *FinalizeState
	// OnCommit is invoked from the stream parser on the first successful
	// finalize_ticket tool_result. Returning a non-nil error is logged
	// at error level and translates to ExitFinalizeCommitFailed downstream;
	// the stream parser continues reading either way. payload carries the
	// raw input that validated.
	//
	// M6 T006: when ResultGrace > 0, OnCommit fires AFTER Run observes
	// either result.ResultSeen=true or the grace window expires. This
	// closes the cost-telemetry blind-spot (docs/issues/cost-telemetry-
	// blind-spot.md): the OnCommit callback reads result.TotalCostUSD
	// at firing time, so the deferred firing means the callback sees
	// the populated cost value instead of the empty pre-result string.
	OnCommit func(payload json.RawMessage) error
	// ResultGrace is the post-commit grace window the pipeline waits
	// for the result event to arrive. Zero preserves M2.2.1 synchronous
	// commit semantics; positive values enable the deferred-commit path.
	// spawn.Deps.FinalizeResultGrace flows here at construction time.
	ResultGrace time.Duration
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

func (p *FinalizePolicy) OnInit(_ context.Context, e claudeproto.InitEvent) claudeproto.RouterAction {
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

func (p *FinalizePolicy) OnAssistant(_ context.Context, e claudeproto.AssistantEvent) {
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
		switch {
		case isMempalaceToolName(tu.Name):
			p.recordMempalaceToolUse(tu)
		case isFinalizeToolName(tu.Name) && p.finalize != nil && p.finalize.state != nil:
			p.recordFinalizeToolUse(tu)
		}
	}
}

// recordMempalaceToolUse logs a pending mempalace_* tool_use and tracks
// its tool_use_id so OnUser can pair the matching tool_result. Pure
// observational path per FR-218a.
func (p *FinalizePolicy) recordMempalaceToolUse(tu claudeproto.ToolUseBlock) {
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
}

// recordFinalizeToolUse handles M2.2.1 FR-276: info-level structured log
// for every finalize_ticket tool_use plus the Attempted-flag bookkeeping.
// Counter increments only on the matching tool_result (OnUser); a
// tool_use without a tool_result is incomplete and should not consume an
// attempt per plan §"Subsystem state machines > Finalize attempt state
// machine".
func (p *FinalizePolicy) recordFinalizeToolUse(tu claudeproto.ToolUseBlock) {
	if p.finalizeToolUse == nil {
		p.finalizeToolUse = make(map[string]struct{}, 4)
	}
	p.finalizeToolUse[tu.ToolUseID] = struct{}{}
	if p.finalize.toolUseInputs == nil {
		p.finalize.toolUseInputs = make(map[string][]byte, 4)
	}
	if len(tu.InputRaw) > 0 {
		// Copy the input raw bytes; claudeproto may reuse buffers.
		buf := make([]byte, len(tu.InputRaw))
		copy(buf, tu.InputRaw)
		p.finalize.toolUseInputs[tu.ToolUseID] = buf
	}
	if !p.finalize.state.Committed {
		p.finalize.state.Attempted = true
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

func (p *FinalizePolicy) OnUser(_ context.Context, e claudeproto.UserEvent) {
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
func (p *FinalizePolicy) handleFinalizeToolResult(tr claudeproto.ToolResultSummary) {
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
		// M6 T006: if a result-grace window is configured, defer the
		// onCommit fire so it lands AFTER result.TotalCostUSD has been
		// populated by OnResult. Run() drives the post-commit wait via
		// HasPendingCommit / FirePendingCommit. ResultGrace == 0 keeps
		// M2.2.1 synchronous semantics (existing tests stay green).
		if p.finalize.resultGrace > 0 && p.finalize.onCommit != nil {
			p.finalize.pendingCommit = true
			p.finalize.pendingPayload = payload
			return
		}
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

func (p *FinalizePolicy) OnRateLimit(_ context.Context, e claudeproto.RateLimitEvent) {
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

func (p *FinalizePolicy) OnResult(_ context.Context, e claudeproto.ResultEvent) {
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

func (p *FinalizePolicy) OnTaskStarted(_ context.Context, _ claudeproto.TaskStartedEvent) {
	p.logger.Debug("claude task_started",
		"instance_id", uuidString(p.instanceID))
}

func (p *FinalizePolicy) OnUnknown(_ context.Context, e claudeproto.UnknownEvent) {
	p.logger.Warn("claude unknown event",
		"instance_id", uuidString(p.instanceID),
		"type", e.Type,
		"subtype", e.Subtype,
		"raw", string(e.Raw))
}

// OnStreamEvent is a no-op for finalize-shaped flows. Pre-M5.1
// stream_event lines (produced when --include-partial-messages is
// passed) routed to OnUnknown; the M2.x ticket-spawn path doesn't
// pass that flag and so never sees them. M5.1 chat invocations DO
// pass the flag — ChatPolicy.OnStreamEvent (internal/chat/policy.go)
// is the consumer that aggregates text_delta events into per-batch
// pg_notify deltas. FinalizePolicy ignores the events explicitly so
// that if a future ticket-spawn path ever passes the flag, the
// behaviour is "stream_event observed, no side effect" rather than
// "logged at warn via OnUnknown."
func (p *FinalizePolicy) OnStreamEvent(_ context.Context, _ claudeproto.StreamEvent) {
	// observational only; no side effects for finalize flows
}

// OnTerminate is the Policy hook Run calls when the scanner stops
// because of a parse error or RouterActionBail. The reason argument is
// either "parse_error" or "bail"; FinalizePolicy translates that into
// the legacy spawn.go onBail-closure call with the appropriate
// exit_reason string (parse_error → ExitParseError, bail → either the
// MCP-formatted reason or the generic "bail" depending on what state
// OnInit recorded).
//
// Existing M2.x callers wired Run to set result.ParseError directly;
// after the M5.1 Policy refactor, Run delegates that side effect to the
// policy via this method so chat-shaped policies can do their own
// terminal write instead.
func (p *FinalizePolicy) OnTerminate(_ context.Context, reason string) {
	switch reason {
	case "parse_error":
		p.result.ParseError = true
		if p.bailFn != nil {
			p.bailFn(ExitParseError)
		}
	case "bail":
		if p.bailFn == nil {
			return
		}
		// MCPBailed was set by OnInit; format the reason here so the
		// outer onBail closure sees the same exit_reason the M2.1
		// dispatchStreamLine used to format inline.
		if p.result.MCPBailed {
			p.bailFn(FormatMCPFailure(p.result.MCPOffenderName, p.result.MCPOffenderStatus))
			return
		}
		p.bailFn("bail")
	}
}

// Result exposes the accumulated Result for spawn.go's Adjudicate
// caller. Returns by value so the caller can't accidentally mutate
// the policy's internal pointer.
func (p *FinalizePolicy) Result() Result {
	if p.result == nil {
		return Result{}
	}
	return *p.result
}

// Build-time interface conformance assertion: FinalizePolicy satisfies
// the Policy interface (and therefore claudeproto.Router). M5.1's
// ChatPolicy adds a second implementer; both share Run.
var _ Policy = (*FinalizePolicy)(nil)

// Run consumes Claude's stream-json stdout line by line, dispatches each
// event to the supplied Policy via claudeproto.Route, and returns
// policy.Result() at EOF (or on the first parse error). When Route
// returns RouterActionBail or a parse error, Run calls
// policy.OnTerminate(ctx, reason) — the policy owns translating that
// into a side effect (FinalizePolicy invokes the killProcessGroup
// closure it stored on construction; ChatPolicy commits an aborted
// chat_messages row). Run does not close stdout; the caller owns the
// Pipe lifecycle.
//
// M5.1 Policy refactor: pre-M5.1 the (instanceID, ticketID, finalize,
// onBail) arguments lived on Run's signature; they're now consolidated
// into the FinalizePolicy struct so a second policy implementation
// (ChatPolicy in internal/chat/) can carry its own lifecycle state
// without polluting Run's signature.
//
// ctx is not used for cancelling the read (cmd.Wait handles that once
// the process exits); it is threaded through to Route + OnTerminate.
func Run(
	ctx context.Context,
	stdout io.Reader,
	policy Policy,
	logger *slog.Logger,
) (Result, error) {
	if logger == nil {
		return Result{}, errors.New("pipeline: logger is required")
	}
	if policy == nil {
		return Result{}, errors.New("pipeline: policy is required")
	}

	// M6 T006: pump the scanner output through a channel so the main
	// loop can select on (a) a new stream line, (b) a grace-window
	// timer that fires after a finalize commit deferred its onCommit
	// callback, and (c) ctx cancellation. Pre-M6 the scanner ran
	// directly inline; that shape couldn't compose a wait because
	// scanner.Scan() blocks on the read syscall, leaving no opening
	// for a timer or context check between events.
	type scanItem struct {
		line []byte
		err  error
	}
	streamEvents := make(chan scanItem, 1)
	scannerCtx, cancelScanner := context.WithCancel(ctx)
	defer cancelScanner()
	go func() {
		defer close(streamEvents)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64<<10), stdoutBufferMax)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			buf := make([]byte, len(line))
			copy(buf, line)
			select {
			case streamEvents <- scanItem{line: buf}:
			case <-scannerCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			select {
			case streamEvents <- scanItem{err: err}:
			case <-scannerCtx.Done():
			}
		}
	}()

	fp, _ := policy.(*FinalizePolicy) // nil for non-finalize policies (e.g. ChatPolicy)

	var graceCh <-chan time.Time
	const graceTickInterval = 50 * time.Millisecond
	var ticker *time.Ticker

	// firePending is the single exit-side commit-fire helper. Used by
	// every Run-return path that observed HasPendingCommit so we never
	// drop the deferred audit. Errors are already logged inside the
	// FirePendingCommit method; we ignore the return here so an error
	// doesn't shadow the in-flight ctx error or scanner error.
	firePending := func() {
		if fp != nil && fp.HasPendingCommit() {
			_ = fp.FirePendingCommit()
		}
	}

	for {
		var tickC <-chan time.Time
		if ticker != nil {
			tickC = ticker.C
		}
		select {
		case sr, ok := <-streamEvents:
			if !ok {
				// Scanner exited (EOF or read error already drained).
				firePending()
				if ticker != nil {
					ticker.Stop()
				}
				return policy.Result(), nil
			}
			if sr.err != nil {
				firePending()
				if ticker != nil {
					ticker.Stop()
				}
				return policy.Result(), fmt.Errorf("pipeline: scan: %w", sr.err)
			}
			if err := dispatchStreamLine(ctx, sr.line, policy, logger); err != nil {
				firePending()
				if ticker != nil {
					ticker.Stop()
				}
				return policy.Result(), err
			}
			// Post-dispatch: if finalize just deferred its commit, start
			// the grace window. If the result event already landed
			// (ResultSeen=true) along with the same dispatch turn — or
			// arrived previously — fire the deferred commit immediately.
			if fp != nil && fp.HasPendingCommit() {
				if policy.Result().ResultSeen {
					firePending()
					if ticker != nil {
						ticker.Stop()
					}
					return policy.Result(), nil
				}
				if graceCh == nil && fp.finalize != nil {
					graceCh = time.After(fp.finalize.resultGrace)
					ticker = time.NewTicker(graceTickInterval)
				}
			}
		case <-graceCh:
			// Grace window expired. Fire pending commit (cost may still
			// be NULL — that's the FR-021 failure-mode contract).
			firePending()
			if ticker != nil {
				ticker.Stop()
			}
			return policy.Result(), nil
		case <-tickC:
			// Periodic re-check during the grace window. If the result
			// event landed via a stream-events tick we may have already
			// fired the commit; this branch covers the edge where the
			// scanner is starved (claude paused) and we want a
			// 50-ms-bounded re-check loop per the task spec.
			if fp != nil && fp.HasPendingCommit() && policy.Result().ResultSeen {
				firePending()
				if ticker != nil {
					ticker.Stop()
				}
				return policy.Result(), nil
			}
		case <-ctx.Done():
			// SIGTERM. Fire any pending commit so we don't drop the
			// audit row, but preserve the ctx err for the caller.
			firePending()
			if ticker != nil {
				ticker.Stop()
			}
			return policy.Result(), ctx.Err()
		}
	}
}

// NewFinalizePolicy constructs the FinalizePolicy the scanner loop
// dispatches into for ticket-execution flows. finalize.State must be
// non-nil so spawn.go's Adjudicate call can read the populated fields
// after Run returns. onBail is the supervisor's killProcessGroup
// closure (or counter-driven SIGTERM for cap exhaustion); the
// finalize-cap-exhaustion path also reuses it via finalizeHook.onBail.
//
// Renamed from newPipelineRouter in M5.1 + signature surfaces the
// *Result pointer publicly so callers can construct the result-tracking
// state alongside the policy.
func NewFinalizePolicy(
	logger *slog.Logger,
	instanceID, ticketID pgtype.UUID,
	result *Result,
	finalize FinalizeDeps,
	onBail func(reason string),
) *FinalizePolicy {
	r := &FinalizePolicy{
		logger:     logger,
		instanceID: instanceID,
		ticketID:   ticketID,
		result:     result,
		bailFn:     onBail,
	}
	if finalize.Expected && finalize.State != nil {
		r.finalize = &finalizeHook{
			state:       finalize.State,
			onCommit:    finalize.OnCommit,
			onBail:      onBail,
			resultGrace: finalize.ResultGrace,
		}
		finalize.State.Expected = true
	}
	return r
}

// HasPendingCommit reports whether handleFinalizeToolResult observed a
// successful finalize tool_result but deferred firing onCommit per the
// M6 T006 result-grace window. Run() polls this between scanner reads
// to drive the post-commit wait. Returns false when no finalize hook
// is wired (non-finalize-expected roles).
func (p *FinalizePolicy) HasPendingCommit() bool {
	return p.finalize != nil && p.finalize.pendingCommit
}

// FirePendingCommit fires the deferred onCommit callback with the
// stashed payload. Caller-driven (Run() invokes it once result.ResultSeen
// observes true OR the grace window expires). Idempotent: clearing the
// pendingCommit flag on entry guarantees a second call is a no-op even
// if the first fired the callback. Errors from onCommit are logged here
// (mirroring the M2.2.1 synchronous-commit error-handling shape).
func (p *FinalizePolicy) FirePendingCommit() error {
	if p.finalize == nil || !p.finalize.pendingCommit {
		return nil
	}
	p.finalize.pendingCommit = false
	if p.finalize.onCommit == nil {
		return nil
	}
	if err := p.finalize.onCommit(p.finalize.pendingPayload); err != nil {
		p.logger.Error("finalize onCommit returned error (deferred path)",
			"instance_id", uuidString(p.instanceID),
			"ticket_id", uuidString(p.ticketID),
			"err", err)
		return err
	}
	return nil
}

// dispatchStreamLine routes one stream-json line through claudeproto
// and invokes policy.OnTerminate with the appropriate reason on parse
// error or bail. Returns a non-nil error only on parse failure; bail
// signals fall through so the scanner keeps draining.
func dispatchStreamLine(
	ctx context.Context,
	buf []byte,
	policy Policy,
	logger *slog.Logger,
) error {
	action, err := claudeproto.Route(ctx, buf, policy)
	if err != nil {
		logger.Error("claude stream parse error; bailing",
			"err", err,
			"line", string(buf))
		policy.OnTerminate(ctx, "parse_error")
		return fmt.Errorf("pipeline: %w", err)
	}
	if action == claudeproto.RouterActionBail {
		policy.OnTerminate(ctx, "bail")
	}
	return nil
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
	case finalize.CapExhausted && result.ResultSeen && isBudgetTerminalReason(result.TerminalReason):
		// M2.2.1 precedence (T002 / SC-258): budget_exceeded wins over
		// finalize_invalid when the subprocess reports a budget-shaped
		// result before/during the retry counter's SIGTERM. Checked
		// BEFORE the bare CapExhausted branch so budget stays the
		// canonical reason when both apply.
		return "failed", ExitBudgetExceeded
	case finalize.CapExhausted:
		// M2.2.1 (T006 / FR-257): the retry counter flagged cap-
		// exhaustion after three failed finalize attempts. Independent
		// of wait.Signaled because spawn.go's bailed flag suppresses
		// Signaled for counter-driven bails (to distinguish them from
		// operator SIGKILL / external signals).
		return "failed", ExitFinalizeInvalid
	case wait.Signaled:
		return "failed", FormatSignalled(wait.Signal)
	case !result.ResultSeen:
		return "failed", ExitNoResult
	case result.ResultSeen && isBudgetTerminalReason(result.TerminalReason):
		// M2.2.2 FR-306: budget signal beats IsError when both apply.
		// Claude's is_error=true + terminal_reason="error_max_budget_usd"
		// combination classifies as budget_exceeded (the cost root
		// cause) rather than claude_error (the symptom). Pre-M2.2.2
		// this check ran AFTER result.IsError, which hid the cost
		// signal under the generic claude_error bucket — M2.2.1's
		// live-run append documented the bug.
		//
		// The original M2.2 / FR-220 rationale still applies for
		// is_error=false + budget-terminal-reason: when Claude wraps
		// up mid-turn under a budget ceiling, we still classify as
		// budget_exceeded here (kept ABOVE the hello.txt check
		// because a truncated run shouldn't be re-classified as
		// acceptance_failed on a missing artefact — it's a cost issue).
		// The exact TerminalReason string on 2.1.117 is not spike-pinned;
		// case-insensitive substring match on "budget" is the defensive
		// shim per plan §"Error vocabulary".
		return "failed", ExitBudgetExceeded
	case result.IsError:
		return "failed", ExitClaudeError
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
