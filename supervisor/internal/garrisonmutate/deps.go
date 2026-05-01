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
type Deps struct {
	Pool          *pgxpool.Pool
	ChatSessionID pgtype.UUID
	ChatMessageID pgtype.UUID
	Logger        *slog.Logger
}
