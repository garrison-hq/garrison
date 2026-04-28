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
	ChannelChatMessageSent       = "chat.message.sent"
	ChannelChatAssistantDelta    = "chat.assistant.delta"
	ChannelWorkChatSessionStarted = "work.chat.session_started"
	ChannelWorkChatMessageSent    = "work.chat.message_sent"
	ChannelWorkChatSessionEnded   = "work.chat.session_ended"
)

// MaxNotifyPayloadBytes caps each delta payload so we stay safely
// below Postgres's 8000-byte NOTIFY payload ceiling (FR-051). At 7KB
// the supervisor coalesces or splits text_delta events.
const MaxNotifyPayloadBytes = 7000

// EmitDelta calls pg_notify('chat.assistant.delta', json_payload) with
// the supplied seq + delta_text. The payload is composed inside the DB
// via json_build_object so the SSE bridge consumer (dashboard) decodes
// directly without an extra trip through the supervisor.
//
// Returns an error if the payload (when serialised) would exceed
// MaxNotifyPayloadBytes; callers split deltas before reaching that bound.
func EmitDelta(ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID, seq int, deltaText string) error {
	body := struct {
		MessageID string `json:"message_id"`
		Seq       int    `json:"seq"`
		DeltaText string `json:"delta_text"`
	}{
		MessageID: uuidString(messageID),
		Seq:       seq,
		DeltaText: deltaText,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("chat: encode delta: %w", err)
	}
	if len(encoded) > MaxNotifyPayloadBytes {
		return fmt.Errorf("chat: delta payload %d bytes exceeds ceiling %d", len(encoded), MaxNotifyPayloadBytes)
	}
	if _, err := pool.Exec(ctx, "SELECT pg_notify($1, $2)", ChannelChatAssistantDelta, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify delta: %w", err)
	}
	return nil
}

// EmitSessionStarted writes a work.chat.session_started notify inside
// the supplied transaction. Payload carries opaque IDs only (FR-072) —
// no operator content, no message text, no tokens.
func EmitSessionStarted(ctx context.Context, tx pgx.Tx, sessionID, userID pgtype.UUID) error {
	body := struct {
		ChatSessionID    string `json:"chat_session_id"`
		StartedByUserID  string `json:"started_by_user_id"`
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
	if _, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", channel, string(encoded)); err != nil {
		return fmt.Errorf("chat: pg_notify %s: %w", channel, err)
	}
	return nil
}
