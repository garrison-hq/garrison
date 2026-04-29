package garrisonmutate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// chatChannelPrefix scopes every M5.3 mutation channel under the
// work.chat.* namespace established by M5.1/M5.2 (work.chat.session_*,
// work.chat.message_sent etc.). Per spec FR-460 + plan §1.3.
const chatChannelPrefix = "work.chat."

// chatNotifyPayload is the Rule 6 backstop shape for every chat-mutation
// pg_notify payload: IDs and verb only, never raw chat content. The
// activity-feed listener side decodes this JSON and renders the
// corresponding EventRow branch.
type chatNotifyPayload struct {
	ChatSessionID        string `json:"chatSessionId"`
	ChatMessageID        string `json:"chatMessageId"`
	Verb                 string `json:"verb"`
	AffectedResourceID   string `json:"affectedResourceId,omitempty"`
	AffectedResourceType string `json:"affectedResourceType,omitempty"`
	ActorUserID          string `json:"actorUserId,omitempty"`
	// Optional verb-specific extras (FromStatus/ToStatus for
	// transition_ticket, AgentRoleSlug for agent verbs, TicketID for
	// spawn_agent etc.). Kept as a free-form map so each verb's notify
	// helper can pass what its EventRow branch needs without
	// proliferating channel-specific structs. Activity-feed listener
	// reads via JSON-typed accessors; Rule 6 still binds (no chat
	// content).
	Extras map[string]string `json:"extras,omitempty"`
}

// EmitChatMutationNotify pg_notify's the chat-namespaced channel for
// the verb's <entity>.<action>. Called post-commit per Rule 3 with a
// context.WithoutCancel(parent) so a SIGTERM mid-emit doesn't cancel
// the notify.
func EmitChatMutationNotify(ctx context.Context, conn DBConn, channelSuffix string, payload chatNotifyPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("garrisonmutate: marshal notify payload: %w", err)
	}
	if len(body) >= 8000 {
		// Rule 6 backstop: chat-mutation payloads should be
		// IDs-only. Anything approaching the 8KB pg_notify limit
		// signals raw content leaked into Extras.
		return fmt.Errorf("garrisonmutate: notify payload too large (%d bytes); audit table holds full args", len(body))
	}
	channel := chatChannelPrefix + channelSuffix
	_, err = conn.Exec(ctx, "SELECT pg_notify($1, $2)", channel, string(body))
	if err != nil {
		return fmt.Errorf("garrisonmutate: pg_notify %s: %w", channel, err)
	}
	return nil
}

// DBConn is the minimal interface notify uses, so callers can pass
// either *pgxpool.Pool or pgx.Tx (post-commit lifecycle never re-opens
// a tx, so the pool is the typical caller).
type DBConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
