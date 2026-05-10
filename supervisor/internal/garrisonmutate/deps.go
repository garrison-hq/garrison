// Package garrisonmutate implements the in-tree MCP server backing the
// chat-driven mutation surface (M5.3). Lives at supervisor mcp
// garrison-mutate; mirrors internal/finalize's shape (in-process
// JSON-RPC over stdio, pgx pool injected via Deps, atomic per-tool
// Postgres transactions with chat_mutation_audit row + post-commit
// pg_notify).
//
// Sealed verb set per docs/security/chat-threat-model.md Rule 1: the
// Verbs slice in verbs.go is the single source of truth; adding a
// verb requires a code change here, a threat-model amendment update,
// and a registry-test update.
package garrisonmutate

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps bundles the server's construction inputs. Logger is optional
// (stderr default); the rest are required for any verb to write its
// audit row.
//
// AgentInstanceID is M8's agent-caller seam: when supervisor wires
// the in-tree MCP server for a spawned agent, it sets this field so
// create_ticket's audit row anchors on agent_instance_id rather than
// chat_session_id (FR-005, FR-401). For chat-side spawns it stays
// zero-valued — assertExactlyOneCallerAnchor rejects the wiring bug
// where both anchors land simultaneously.
type Deps struct {
	Pool            *pgxpool.Pool
	ChatSessionID   pgtype.UUID
	ChatMessageID   pgtype.UUID
	AgentInstanceID pgtype.UUID
	Logger          *slog.Logger
}
