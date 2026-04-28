package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ = fmt.Errorf // retained for callers that wrap store errors

// Sentinels surfaced by the persistence helpers. Callers (chat.Worker)
// translate these into chat.ErrorKind values for the assistant-row
// terminal write.
var (
	ErrSessionNotFound = errors.New("chat: session not found")
	ErrSessionEnded    = errors.New("chat: session is not active")
	ErrCostCapReached  = errors.New("chat: session cost cap reached")
)

// EnsureActiveSession returns the session row if it exists and is
// status='active'; ErrSessionEnded if it's ended/aborted; ErrSessionNotFound
// otherwise. chat.Worker calls this before doing any work.
func EnsureActiveSession(ctx context.Context, q *store.Queries, id pgtype.UUID) (store.ChatSession, error) {
	sess, err := q.GetChatSession(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ChatSession{}, ErrSessionNotFound
		}
		return store.ChatSession{}, fmt.Errorf("chat: get session: %w", err)
	}
	if sess.Status != "active" {
		return sess, ErrSessionEnded
	}
	return sess, nil
}

// EnsureCostCapNotExceeded returns ErrCostCapReached if the session's
// total_cost_usd is at or above the deps-supplied cap (FR-061 reactive
// check). The cap is applied as a float64 compare against the
// pgtype.Numeric column — precision loss at the per-cent level is
// acceptable for a soft cap.
func EnsureCostCapNotExceeded(ctx context.Context, deps Deps, sess store.ChatSession) error {
	if deps.SessionCostCapUSD <= 0 {
		return nil // disabled
	}
	cur, err := numericToFloat(sess.TotalCostUsd)
	if err != nil {
		// If we can't parse, default to "not exceeded" (fail-open is
		// safer than blocking the operator on a parse glitch).
		deps.Logger.Warn("chat: failed to parse total_cost_usd; cost cap check skipped",
			"session_id", uuidString(sess.ID), "err", err)
		return nil
	}
	if cur >= deps.SessionCostCapUSD {
		return ErrCostCapReached
	}
	return nil
}

// AssignAssistantTurnIndex computes the assistant turn_index as
// operator_turn_index + 1. Mirrors the clarify Q2 contract.
func AssignAssistantTurnIndex(operatorTurnIndex int32) int32 {
	return operatorTurnIndex + 1
}

// InsertAssistantPending creates the assistant row at status='pending'.
// Wraps store.InsertAssistantPending for callers that don't import store.
func InsertAssistantPending(ctx context.Context, q *store.Queries, sessionID pgtype.UUID, turnIndex int32) (store.ChatMessage, error) {
	return q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: sessionID,
		TurnIndex: turnIndex,
	})
}

// numericToFloat parses a pgtype.Numeric into float64. Returns 0 + nil
// for NULL values; an error if Numeric is non-NULL but unparseable.
func numericToFloat(n pgtype.Numeric) (float64, error) {
	if !n.Valid {
		return 0, nil
	}
	f, err := n.Float64Value()
	if err != nil {
		return 0, err
	}
	if !f.Valid {
		return 0, nil
	}
	return f.Float64, nil
}

// uuidString formats a pgtype.UUID for log lines. Local copy of
// internal/spawn/pipeline.go's helper to keep the chat package free of
// cross-package dependencies on internal helpers.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	const hex = "0123456789abcdef"
	b := u.Bytes
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
