// Package chat owns the M5.1 CEO chat runtime: per-message Claude
// Code subprocess spawn, NDJSON stream parsing via the spawn.Policy
// abstraction, lifecycle (LISTEN dispatch + restart sweep + idle
// timeout), and persistence orchestration on the chat_sessions /
// chat_messages tables.
//
// Read-only by design — no mutation MCP tools mount on the chat
// container; the per-spawn config exposes only the existing postgres
// (read-only via garrison_agent_ro) and mempalace MCP servers. Per
// ARCHITECTURE.md:574 + RATIONALE.md:127 the chat is summoned per
// message; chat-driven mutations are deferred to M5.3+ behind a
// separate threat-model amendment.
package chat

import (
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps wires the chat subsystem's collaborators and tunables. The
// constructor in cmd/supervisor/main.go (T014) populates this from
// config + already-constructed shared resources (the pgx pool, the
// vault client, etc.). Tests can substitute fakes for VaultClient and
// DockerExec without booting Docker / Infisical.
type Deps struct {
	Pool        *pgxpool.Pool
	Queries     *store.Queries
	VaultClient vault.Fetcher
	DockerExec  dockerexec.DockerExec
	Logger      *slog.Logger

	// CustomerID is the Garrison customer UUID — composed into the
	// vault path's /<customer_id>/ prefix at fetch time and passed as
	// vault.GrantRow.CustomerID for vault_access_log audit-row
	// integrity (see plan §5.3 for the dual purpose). Single-tenant
	// Garrison: one customer_id per supervisor.
	CustomerID pgtype.UUID

	// OAuthVaultPath is the secret-path SUFFIX, e.g.
	// "/operator/CLAUDE_CODE_OAUTH_TOKEN". The supervisor composes the
	// full vault.GrantRow.SecretPath as
	//   "/" + cfg.CustomerID.String() + suffix
	// to match the existing M2.3 splitSecretPath convention.
	OAuthVaultPath string

	// ChatContainerImage is the docker image tag the chat runtime
	// spawns. Production: "garrison-claude:m5". CI: overridden to
	// "garrison-mockclaude:m5" so integration tests run without a real
	// CLAUDE_CODE_OAUTH_TOKEN.
	ChatContainerImage string

	// MCPConfigDir is the host-side directory the supervisor writes
	// per-message MCP config files into (one file per chat message).
	// Bind-mounted into the chat container as /etc/garrison/mcp.json
	// (read-only). Default: /var/lib/garrison/mcp/.
	MCPConfigDir string

	// DockerNetwork is the compose network name the chat container
	// joins. Default: "garrison-net". The supervisor + the docker-proxy
	// + the postgres + mempalace containers all live on this network;
	// the chat container needs to reach them via DNS for the MCP
	// server invocations.
	DockerNetwork string

	// TurnTimeout caps a single chat turn (operator INSERTs message →
	// supervisor receives result event from the spawned container).
	// On timeout the supervisor process-group SIGTERMs the docker
	// subprocess and terminal-writes the chat_messages row with
	// error_kind='turn_timeout' (FR-016). Default: 5 minutes.
	TurnTimeout time.Duration

	// SessionIdleTimeout caps the wall-clock between an operator's
	// last message and the next one. After this window the idle
	// sweep marks the session 'ended' and subsequent operator
	// messages on it get error_kind='session_ended' (FR-081).
	// Default: 30 minutes.
	SessionIdleTimeout time.Duration

	// IdleSweepTick is the cadence at which RunIdleSweep checks for
	// timed-out sessions. Production: 60s. Tests can override to a
	// smaller interval for a faster CI signal. Zero falls back to
	// the production default.
	IdleSweepTick time.Duration

	// SessionCostCapUSD is the soft per-session cap. Reactive check
	// (clarify Q5): if chat_sessions.total_cost_usd >= cap at spawn
	// time, the supervisor refuses the next turn with error_kind=
	// 'session_cost_cap_reached'. Default: 1.00.
	SessionCostCapUSD float64

	// TerminalWriteGrace bounds how long the supervisor will wait for
	// terminal writes (chat_messages row commits) to complete via
	// context.WithoutCancel during shutdown. AGENTS.md concurrency
	// rule 6. Reused from spawn.TerminalWriteGrace at construction.
	TerminalWriteGrace time.Duration

	// ShutdownSignalGrace bounds the SIGTERM-to-SIGKILL escalation
	// window for the chat docker subprocess. AGENTS.md concurrency
	// rule 7. Reused from spawn.ShutdownSignalGrace.
	ShutdownSignalGrace time.Duration

	// ClaudeBinInContainer is the path to the claude binary inside
	// the chat container. Both garrison-claude:m5 and
	// garrison-mockclaude:m5 ship with /usr/local/bin/claude as the
	// entrypoint target — this field is the supervisor's record of
	// that contract; if the image's binary path ever moves the
	// supervisor doesn't need to be rebuilt.
	ClaudeBinInContainer string

	// DefaultModel is the model name passed via --model on the chat
	// invocation. Operator can override via env var. Default:
	// "claude-sonnet-4-6".
	DefaultModel string
}
