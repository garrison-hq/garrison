package spawn

import (
	"bufio"
	"context"
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

// pipelineRouter implements claudeproto.Router and streams observations
// into a captured Result. It performs no I/O of its own beyond slog lines;
// the bail side effect (killProcessGroup) is delegated to the caller via
// the onBail callback that Run owns.
type pipelineRouter struct {
	logger     *slog.Logger
	instanceID pgtype.UUID
	result     *Result
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
}

func (p *pipelineRouter) OnUser(_ context.Context, e claudeproto.UserEvent) {
	for _, tr := range e.ToolResults {
		if tr.IsError {
			p.logger.Warn("claude tool_result reported error",
				"instance_id", uuidString(p.instanceID),
				"tool_use_id", tr.ToolUseID,
				"detail", tr.Detail)
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
	logger *slog.Logger,
	onBail func(reason string),
) (Result, error) {
	if logger == nil {
		return Result{}, errors.New("pipeline: logger is required")
	}
	result := Result{}
	r := &pipelineRouter{
		logger:     logger,
		instanceID: instanceID,
		result:     &result,
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
func Adjudicate(result Result, wait WaitDetail, helloTxtOK bool) (status, exitReason string) {
	switch {
	case result.MCPBailed:
		return "failed", FormatMCPFailure(result.MCPOffenderName, result.MCPOffenderStatus)
	case result.ParseError:
		return "failed", ExitParseError
	case wait.ShutdownInitiated:
		return "failed", ExitSupervisorShutdown
	case errors.Is(wait.ContextErr, context.DeadlineExceeded):
		return "timeout", ExitTimeout
	case wait.Signaled:
		return "failed", FormatSignalled(wait.Signal)
	case !result.ResultSeen:
		return "failed", ExitNoResult
	case result.IsError:
		return "failed", ExitClaudeError
	case !helloTxtOK:
		return "failed", ExitAcceptanceFailed
	default:
		return "succeeded", ExitCompleted
	}
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
