package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel-name constants for the chat event bus. Two namespaces:
//   - The dashboard → supervisor signal lives at "chat.message.sent"
//   - Supervisor → SSE-bridge deltas at "chat.assistant.delta"
//   - Supervisor → activity-feed audit at "work.chat.session_started",
//     "work.chat.message_sent", "work.chat.session_ended"
//
// FR-050 / FR-051 / FR-070 / FR-071 enumerate these.
const (
	ChannelChatMessageSent        = "chat.message.sent"
	ChannelChatAssistantDelta     = "chat.assistant.delta"
	ChannelWorkChatSessionStarted = "work.chat.session_started"
	ChannelWorkChatMessageSent    = "work.chat.message_sent"
	ChannelWorkChatSessionEnded   = "work.chat.session_ended"
)

// MaxNotifyPayloadBytes caps each delta payload so we stay safely
// below Postgres's 8000-byte NOTIFY payload ceiling (FR-051). At 7KB
// the supervisor coalesces or splits text_delta events.
const MaxNotifyPayloadBytes = 7000

// pgNotifyExecSQL is the parameterised pg_notify call shared by every
// chat-namespaced emit helper. Centralised so the literal isn't
// duplicated across delta / scrub / tool_use / tool_result /
// assistant_error paths and so the contract stays uniform.
const pgNotifyExecSQL = `SELECT pg_notify($1, $2)`

// M5.3 chat-namespaced channels for live tool-call observability. The
// dashboard's useChatStream hook subscribes (via the existing
// /api/sse/chat producer chain) and renders ToolCallChip per event.
// Mirrors the EmitDelta shape: ID-only payloads (Rule 6 backstop),
// JSON-encoded server-side, decoded client-side.
const (
	ChannelChatToolUse    = "chat.tool.use"
	ChannelChatToolResult = "chat.tool.result"
	// ChannelChatAssistantError carries chat-policy-driven errors
	// (tool-call ceiling, cost-cap fire) that the dashboard renders as
	// a typed-error chip.
	ChannelChatAssistantError = "chat.assistant.error"
)

// EmitDelta calls pg_notify('chat.assistant.delta', json_payload) with
// the supplied seq + delta_text. The payload is composed inside the DB
// via json_build_object so the SSE bridge consumer (dashboard) decodes
// directly without an extra trip through the supervisor.
//
// `block` is the per-message_start counter — claude can emit multiple
// message_start events in a single turn (text → tool_use → text round
// trips) and each message's text_delta should be accumulated into a
// fresh client-side buffer, otherwise the dashboard renders prior
// intermediate text before the final answer streams in. The dashboard
// keys partialDeltas by (messageId, block) and resets the visible
// buffer whenever block increments.
//
// Returns an error if the payload (when serialised) would exceed
// MaxNotifyPayloadBytes; callers split deltas before reaching that bound.
func EmitDelta(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, block int, seq int, deltaText string) error {
	return emitDeltaImpl(ctx, pool, messageID, block, seq, deltaText, false)
}

// EmitScrub emits a special "clear visible buffer" notification for
// the given (messageId, block). Used when claude transitions from text
// → tool_use mid-message: the prior text was a preamble that the
// operator shouldn't see lingering once the tool round-trip starts.
// The dashboard treats scrub deltas as a directive to clear
// partialDeltas[messageId] for the current block.
func EmitScrub(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, block int, seq int) error {
	return emitDeltaImpl(ctx, pool, messageID, block, seq, "", true)
}

func emitDeltaImpl(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, block int, seq int, deltaText string, scrub bool) error {
	body := struct {
		MessageID string `json:"message_id"`
		Block     int    `json:"block"`
		Seq       int    `json:"seq"`
		DeltaText string `json:"delta_text"`
		Scrub     bool   `json:"scrub,omitempty"`
	}{
		MessageID: uuidString(messageID),
		Block:     block,
		Seq:       seq,
		DeltaText: deltaText,
		Scrub:     scrub,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("chat: encode delta: %w", err)
	}
	if len(encoded) > MaxNotifyPayloadBytes {
		return fmt.Errorf("chat: delta payload %d bytes exceeds ceiling %d", len(encoded), MaxNotifyPayloadBytes)
	}
	if _, err := pool.Exec(ctx, pgNotifyExecSQL, ChannelChatAssistantDelta, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify delta: %w", err)
	}
	return nil
}

// EmitToolUse fires when claude opens a tool_use content block within
// an assistant message (M5.3 chip live-render path). Payload carries
// IDs + tool name + raw args JSON; the dashboard's chip renderer reads
// the tool name to choose between read-chip / mutation-chip styling
// per FR-440 / FR-442 / FR-443.
//
// args is the json.RawMessage from claude's content_block.input — the
// dashboard decodes it (or summarizes it) for the chip's pre-call
// label. M5.3 ships args verbatim; future polish may sanitize.
func EmitToolUse(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, toolUseID, toolName string, args json.RawMessage) error {
	body := struct {
		MessageID string          `json:"message_id"`
		ToolUseID string          `json:"tool_use_id"`
		ToolName  string          `json:"tool_name"`
		Args      json.RawMessage `json:"args,omitempty"`
	}{
		MessageID: uuidString(messageID),
		ToolUseID: toolUseID,
		ToolName:  toolName,
		Args:      args,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("chat: encode tool_use: %w", err)
	}
	if len(encoded) > MaxNotifyPayloadBytes {
		// Args too large for pg_notify — emit with args trimmed to
		// {"_truncated":true} so the dashboard knows. The full args
		// land in chat_messages.raw_event_envelope on terminal commit;
		// reconnect-replay reads from there per M5.2 FR-261.
		body.Args = json.RawMessage(`{"_truncated":true}`)
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("chat: encode trimmed tool_use: %w", err)
		}
	}
	if _, err := pool.Exec(ctx, pgNotifyExecSQL, ChannelChatToolUse, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify tool_use: %w", err)
	}
	return nil
}

// EmitToolResult fires when a tool_use's matching tool_result lands —
// either via OnUser (claude returns a tool_result block) or via the
// supervisor receiving the garrison-mutate verb's structured Result.
// Payload is keyed by tool_use_id so the dashboard's renderer transitions
// the matching pre-call chip to its post-call (or failure) state per
// FR-441 / FR-444.
func EmitToolResult(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, toolUseID string, isError bool, result json.RawMessage) error {
	body := struct {
		MessageID string          `json:"message_id"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Result    json.RawMessage `json:"result,omitempty"`
	}{
		MessageID: uuidString(messageID),
		ToolUseID: toolUseID,
		IsError:   isError,
		Result:    result,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("chat: encode tool_result: %w", err)
	}
	if len(encoded) > MaxNotifyPayloadBytes {
		body.Result = json.RawMessage(`{"_truncated":true}`)
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("chat: encode trimmed tool_result: %w", err)
		}
	}
	if _, err := pool.Exec(ctx, pgNotifyExecSQL, ChannelChatToolResult, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify tool_result: %w", err)
	}
	return nil
}

// EmitAssistantError fires the chat-policy-driven typed-error frame
// (per-turn tool-call ceiling, session cost cap, etc.). The dashboard
// renders a typed-error chip + flips the consumer's lastError field.
func EmitAssistantError(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, errorKind, message string) error {
	body := struct {
		MessageID string `json:"message_id"`
		ErrorKind string `json:"error_kind"`
		Message   string `json:"message,omitempty"`
	}{
		MessageID: uuidString(messageID),
		ErrorKind: errorKind,
		Message:   message,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("chat: encode assistant_error: %w", err)
	}
	if _, err := pool.Exec(ctx, pgNotifyExecSQL, ChannelChatAssistantError, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify assistant_error: %w", err)
	}
	return nil
}

// EmitSessionStarted writes a work.chat.session_started notify inside
// the supplied transaction. Payload carries opaque IDs only (FR-072) —
// no operator content, no message text, no tokens.
func EmitSessionStarted(ctx context.Context, tx pgx.Tx, sessionID, userID pgtype.UUID) error {
	body := struct {
		ChatSessionID   string `json:"chat_session_id"`
		StartedByUserID string `json:"started_by_user_id"`
	}{
		ChatSessionID:   uuidString(sessionID),
		StartedByUserID: uuidString(userID),
	}
	return emitWorkNotify(ctx, tx, ChannelWorkChatSessionStarted, body)
}

// EmitSessionEnded writes a work.chat.session_ended notify in tx.
func EmitSessionEnded(ctx context.Context, tx pgx.Tx, sessionID pgtype.UUID, status string) error {
	body := struct {
		ChatSessionID string `json:"chat_session_id"`
		Status        string `json:"status"`
	}{
		ChatSessionID: uuidString(sessionID),
		Status:        status,
	}
	return emitWorkNotify(ctx, tx, ChannelWorkChatSessionEnded, body)
}

// EmitMessageSent writes a work.chat.message_sent notify in tx.
// Payload is opaque IDs only.
func EmitMessageSent(ctx context.Context, tx pgx.Tx, sessionID, messageID pgtype.UUID) error {
	body := struct {
		ChatSessionID string `json:"chat_session_id"`
		ChatMessageID string `json:"chat_message_id"`
	}{
		ChatSessionID: uuidString(sessionID),
		ChatMessageID: uuidString(messageID),
	}
	return emitWorkNotify(ctx, tx, ChannelWorkChatMessageSent, body)
}

func emitWorkNotify(ctx context.Context, tx pgx.Tx, channel string, body any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("chat: encode notify body: %w", err)
	}
	if _, err := tx.Exec(ctx, pgNotifyExecSQL, channel, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify %s: %w", channel, err)
	}
	return nil
}
