package claudeproto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// RouterAction tells the caller (pipeline.Run) whether to continue
// consuming events or to immediately bail and terminate the subprocess.
// Only OnInit returns RouterActionBail in practice (MCP health failure);
// the other On* methods are observational and Route returns
// RouterActionContinue on their behalf.
type RouterAction int

const (
	RouterActionContinue RouterAction = iota
	RouterActionBail
)

// Router is the dispatch interface. One method per event type keeps the
// surface exhaustively checkable at compile time — adding a new event
// type here forces every implementation to add a method or fail to
// compile. OnUnknown handles forward compatibility (FR-107).
//
// Only OnInit returns RouterAction. Other methods are observational
// (plan §internal/claudeproto), so Route converts their void returns
// into RouterActionContinue. This lets implementations stay minimal
// while preserving the "init can bail" semantics.
type Router interface {
	OnInit(ctx context.Context, e InitEvent) RouterAction
	OnAssistant(ctx context.Context, e AssistantEvent)
	OnUser(ctx context.Context, e UserEvent)
	OnRateLimit(ctx context.Context, e RateLimitEvent)
	OnResult(ctx context.Context, e ResultEvent)
	OnStreamEvent(ctx context.Context, e StreamEvent)
	OnTaskStarted(ctx context.Context, e TaskStartedEvent)
	OnUnknown(ctx context.Context, e UnknownEvent)
}

// envelope covers just enough of any stream-json line to decide which
// concrete type to unmarshal into. Keeping this minimal avoids allocating
// the full event twice.
type envelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

// Route parses raw as a single stream-json line and dispatches to the
// matching Router method. Returns (RouterActionBail, error) when the
// line is not valid JSON — per FR-106 the caller treats a parse error
// as a fatal condition (process-group SIGTERM, exit_reason=parse_error).
//
// Empty lines are the caller's concern; Route assumes a non-empty input.
// (pipeline.Run skips empty lines before calling Route.)
func Route(ctx context.Context, raw []byte, r Router) (RouterAction, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return RouterActionBail, fmt.Errorf("claudeproto: parse envelope: %w", err)
	}

	switch env.Type {
	case "system":
		return routeSystem(ctx, raw, env, r)
	case "assistant":
		return routeAssistant(ctx, raw, r)
	case "user":
		return routeUser(ctx, raw, r)
	case "rate_limit_event":
		return routeRateLimit(ctx, raw, r)
	case "result":
		return routeResult(ctx, raw, r)
	case "stream_event":
		return routeStreamEvent(ctx, raw, r)
	default:
		r.OnUnknown(ctx, UnknownEvent{Type: env.Type, Subtype: env.Subtype, Raw: raw})
		return RouterActionContinue, nil
	}
}

func routeSystem(ctx context.Context, raw []byte, env envelope, r Router) (RouterAction, error) {
	switch env.Subtype {
	case "init":
		var e InitEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return RouterActionBail, fmt.Errorf("claudeproto: decode init: %w", err)
		}
		e.Raw = raw
		return r.OnInit(ctx, e), nil
	case "task_started":
		r.OnTaskStarted(ctx, TaskStartedEvent{Raw: raw})
		return RouterActionContinue, nil
	default:
		r.OnUnknown(ctx, UnknownEvent{Type: env.Type, Subtype: env.Subtype, Raw: raw})
		return RouterActionContinue, nil
	}
}

func routeAssistant(ctx context.Context, raw []byte, r Router) (RouterAction, error) {
	e, err := decodeAssistant(raw)
	if err != nil {
		return RouterActionBail, fmt.Errorf("claudeproto: decode assistant: %w", err)
	}
	r.OnAssistant(ctx, e)
	return RouterActionContinue, nil
}

func routeUser(ctx context.Context, raw []byte, r Router) (RouterAction, error) {
	e, err := decodeUser(raw)
	if err != nil {
		return RouterActionBail, fmt.Errorf("claudeproto: decode user: %w", err)
	}
	r.OnUser(ctx, e)
	return RouterActionContinue, nil
}

func routeRateLimit(ctx context.Context, raw []byte, r Router) (RouterAction, error) {
	var e RateLimitEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return RouterActionBail, fmt.Errorf("claudeproto: decode rate_limit_event: %w", err)
	}
	e.Raw = raw
	r.OnRateLimit(ctx, e)
	return RouterActionContinue, nil
}

// streamEventWire mirrors the JSON shape of one Claude stream_event
// line. The inner `event.type` discriminates shape (delta, message,
// message_delta, etc.); fields not carried by a given shape stay
// zero-value after decode.
type streamEventWire struct {
	SessionID string               `json:"session_id"`
	Event     streamEventInnerWire `json:"event"`
}

type streamEventInnerWire struct {
	Type       string                    `json:"type"`
	Index      int                       `json:"index"`
	Delta      streamEventDeltaWire      `json:"delta"`
	Message    streamEventMessageWire    `json:"message"`
	DeltaUsage streamEventDeltaUsageWire `json:"-"` // not used; reserved
	StopReason string                    `json:"stop_reason"`
}

type streamEventDeltaWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type streamEventMessageWire struct {
	Usage streamEventUsageWire `json:"usage"`
}

type streamEventUsageWire struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type streamEventDeltaUsageWire struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// messageDeltaUsageWire is decoded as a second pass for stream_event
// lines whose `event.type == "message_delta"` — those carry usage at
// event.usage rather than event.message.usage.
type messageDeltaUsageWire struct {
	Event struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"event"`
}

func routeStreamEvent(ctx context.Context, raw []byte, r Router) (RouterAction, error) {
	var wire streamEventWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return RouterActionBail, fmt.Errorf("claudeproto: decode stream_event: %w", err)
	}

	var msgDeltaWire messageDeltaUsageWire
	_ = json.Unmarshal(raw, &msgDeltaWire)

	e := StreamEvent{
		SessionID: wire.SessionID,
		InnerType: wire.Event.Type,
		Raw:       raw,
		Inner: StreamInner{
			Index:              wire.Event.Index,
			DeltaType:          wire.Event.Delta.Type,
			DeltaText:          wire.Event.Delta.Text,
			StopReason:         wire.Event.StopReason,
			InputTokens:        wire.Event.Message.Usage.InputTokens,
			OutputTokens:       wire.Event.Message.Usage.OutputTokens,
			CacheReadInput:     wire.Event.Message.Usage.CacheReadInputTokens,
			CacheCreationInput: wire.Event.Message.Usage.CacheCreationInputTokens,
		},
	}

	// message_delta's usage lives at event.usage rather than
	// event.message.usage; merge in if not already populated.
	if e.Inner.OutputTokens == 0 && msgDeltaWire.Event.Usage.OutputTokens > 0 {
		e.Inner.OutputTokens = msgDeltaWire.Event.Usage.OutputTokens
	}
	if e.Inner.InputTokens == 0 && msgDeltaWire.Event.Usage.InputTokens > 0 {
		e.Inner.InputTokens = msgDeltaWire.Event.Usage.InputTokens
	}

	r.OnStreamEvent(ctx, e)
	return RouterActionContinue, nil
}

func routeResult(ctx context.Context, raw []byte, r Router) (RouterAction, error) {
	// Use decoder.UseNumber so total_cost_usd round-trips with full
	// decimal precision into json.Number (no float64 conversion).
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var e ResultEvent
	if err := dec.Decode(&e); err != nil {
		return RouterActionBail, fmt.Errorf("claudeproto: decode result: %w", err)
	}
	e.Raw = raw
	// Derive a single-word TerminalReason from subtype (+ stop_reason
	// fallback). On 2.1.117 the value is "success" on happy paths.
	switch {
	case e.Subtype != "":
		e.TerminalReason = e.Subtype
	case e.StopReason != "":
		e.TerminalReason = e.StopReason
	default:
		e.TerminalReason = "completed"
	}
	r.OnResult(ctx, e)
	return RouterActionContinue, nil
}

// decodeAssistant pulls out the fields the supervisor actually logs
// without requiring the caller to mirror Claude's full content-block
// schema. The raw line is preserved for full-fidelity debug logging.
func decodeAssistant(raw []byte) (AssistantEvent, error) {
	var wire struct {
		Message struct {
			Model   string `json:"model"`
			Content []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`  // tool_use
				ID    string          `json:"id"`    // tool_use
				Input json.RawMessage `json:"input"` // tool_use
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return AssistantEvent{}, err
	}
	types := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	var toolUses []ToolUseBlock
	for _, c := range wire.Message.Content {
		if _, ok := seen[c.Type]; !ok {
			seen[c.Type] = struct{}{}
			types = append(types, c.Type)
		}
		if c.Type == "tool_use" {
			toolUses = append(toolUses, ToolUseBlock{
				Name:      c.Name,
				ToolUseID: c.ID,
				InputRaw:  c.Input,
			})
		}
	}
	return AssistantEvent{
		Model:             wire.Message.Model,
		ContentBlockCount: len(wire.Message.Content),
		ContentTypes:      types,
		ToolUses:          toolUses,
		Raw:               raw,
	}, nil
}

// decodeUser pulls out tool_result items. Claude's real wire shape wraps
// content in message (mirroring assistant). Each tool_result has tool_use_id,
// is_error, and content[] (text blocks). Detail is a short excerpt — just
// the first text block, trimmed, for the warn-level log line.
func decodeUser(raw []byte) (UserEvent, error) {
	var wire struct {
		Message struct {
			Content []struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Content   json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return UserEvent{}, err
	}
	var results []ToolResultSummary
	for _, c := range wire.Message.Content {
		if c.Type != "tool_result" {
			continue
		}
		results = append(results, ToolResultSummary{
			ToolUseID: c.ToolUseID,
			IsError:   c.IsError,
			Detail:    summarizeDetail(c.Content),
		})
	}
	return UserEvent{ToolResults: results, Raw: raw}, nil
}

// summarizeDetail extracts a short text excerpt from the tool_result's
// content, which can be a string or an array of content blocks. Returns
// the empty string if nothing useful is found.
func summarizeDetail(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	// Try string form first.
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	// Then array of {type, text} blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}
