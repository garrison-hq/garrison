package spawn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultDependencySatisfactionColumns is the per-dept default when a
// department row has NULL for dependency_satisfaction_columns. Mirrors
// the M8 migration's seed shape so the gate is reproducible.
var defaultDependencySatisfactionColumns = []string{"qa_review", "done"}

// checkDependencySatisfied evaluates whether the candidate ticket's
// `depends_on_ticket_id` is in a satisfied state (its column_slug is
// in the predecessor's dept's `dependency_satisfaction_columns`).
// Returns (true, nil) when no dependency is set (the most common
// case), or when the dependency is satisfied. Returns (false, nil)
// when the dependency is unsatisfied — caller defers the spawn.
//
// The DB lookup runs inside the supplied tx-bound queries so the
// candidate ticket's state is consistent with the rest of
// prepareSpawn's reads.
func checkDependencySatisfied(ctx context.Context, q *store.Queries, ticketID pgtype.UUID) (bool, error) {
	ticket, err := q.GetTicketByID(ctx, ticketID)
	if err != nil {
		return false, fmt.Errorf("spawn: GetTicketByID for dep gate: %w", err)
	}
	if !ticket.DependsOnTicketID.Valid {
		return true, nil
	}
	state, err := q.GetTicketSatisfactionState(ctx, ticket.DependsOnTicketID)
	if err != nil {
		// If the predecessor is missing (e.g., deleted post-create with
		// FK ON DELETE SET NULL not yet propagated to this snapshot),
		// treat as satisfied — the FK will fix up via the cascade.
		return true, nil
	}
	allowed := decodeSatisfactionColumns(state.DependencySatisfactionColumns)
	for _, col := range allowed {
		if col == state.ColumnSlug {
			return true, nil
		}
	}
	return false, nil
}

// decodeSatisfactionColumns parses the department's
// dependency_satisfaction_columns JSONB array. Returns the M8 default
// (`["qa_review","done"]`) when the column is NULL or unparseable.
func decodeSatisfactionColumns(raw []byte) []string {
	if len(raw) == 0 {
		return defaultDependencySatisfactionColumns
	}
	var cols []string
	if err := json.Unmarshal(raw, &cols); err != nil || len(cols) == 0 {
		return defaultDependencySatisfactionColumns
	}
	return cols
}

// NotifyBlockedDependents is the dispatcher-side hook the M8
// transition listener calls after a `work.ticket.transitioned`
// event. It queries every `tickets WHERE depends_on_ticket_id =
// <transitioning_id> AND column_slug = 'todo'` and emits a synthetic
// `work.ticket.created.<dept>.<column>` pg_notify for each. The
// existing dispatcher dedupes the resulting (LISTEN + poll) firings
// via event_outbox.id (see plan §"Dedupe on handling").
//
// Best-effort: any error is logged and skipped — the poll-fallback
// path in the M1 dispatcher will pick up the unblocked dependents on
// its next sweep regardless.
func NotifyBlockedDependents(ctx context.Context, pool *pgxpool.Pool, q *store.Queries, transitioningTicketID pgtype.UUID, logger *slog.Logger) {
	rows, err := q.ListBlockedDependents(ctx, transitioningTicketID)
	if err != nil {
		if logger != nil {
			logger.Warn("spawn: ListBlockedDependents failed",
				"ticket_id", uuidString(transitioningTicketID), "err", err)
		}
		return
	}
	for _, row := range rows {
		emitSyntheticCreate(ctx, pool, q, row, logger)
	}
}

// EnsureMcpjungleTokenForAgent resolves the per-agent MCPJungle
// bearer token via the M2.3 vault fetcher. The token is stored at
// path `mcpjungle/agents/<agent-id>` (written by the M8 reconciler in
// internal/mcpjungle) and bound to env var
// vault.MCPJungleBearerTokenEnvVar by the agent-scoped grant the
// reconciler inserts.
//
// Returns the env-var assignment string `MCPJUNGLE_BEARER_TOKEN=<token>`
// for direct injection into the agent container's process env at
// spawn time. The caller is responsible for zeroing the returned
// secret bytes after the env-var has been consumed by exec.
//
// Returns (string, vault.ErrVaultSecretNotFound) when the agent has
// no agent-scoped grant yet (typical during M2.x→M8 grandfathering
// before the reconciler runs); the spawn path treats this as a
// degrade-with-warning case per FR-308.
func EnsureMcpjungleTokenForAgent(
	ctx context.Context,
	deps Deps,
	agentID pgtype.UUID,
	logger *slog.Logger,
) (string, error) {
	if deps.Vault == nil {
		return "", errors.New("spawn: vault fetcher not wired (Deps.Vault is nil)")
	}
	if !agentID.Valid {
		return "", errors.New("spawn: agentID is zero-valued")
	}
	if deps.Queries == nil {
		return "", errors.New("spawn: Queries not wired")
	}
	rows, err := deps.Queries.ListGrantsForRoleAndAgent(ctx, store.ListGrantsForRoleAndAgentParams{
		CustomerID: deps.CustomerID,
		RoleSlug:   "", // role-scoped match is OR'd; we only need the agent-scoped row
		AgentID:    agentID,
	})
	if err != nil {
		return "", fmt.Errorf("spawn: ListGrantsForRoleAndAgent: %w", err)
	}
	var grantPath string
	for _, row := range rows {
		if row.AgentID.Valid && row.AgentID.Bytes == agentID.Bytes && row.EnvVarName == vault.MCPJungleBearerTokenEnvVar {
			grantPath = row.SecretPath
			break
		}
	}
	if grantPath == "" {
		return "", vault.ErrVaultSecretNotFound
	}
	values, err := deps.Vault.Fetch(ctx, []vault.GrantRow{{
		EnvVarName: vault.MCPJungleBearerTokenEnvVar,
		SecretPath: grantPath,
		CustomerID: deps.CustomerID,
		AgentID:    agentID,
	}})
	if err != nil {
		return "", fmt.Errorf("spawn: vault.Fetch mcpjungle: %w", err)
	}
	val, ok := values[vault.MCPJungleBearerTokenEnvVar]
	if !ok {
		return "", vault.ErrVaultSecretNotFound
	}
	defer val.Zero()
	out := vault.MCPJungleBearerTokenEnvVar + "=" + string(val.UnsafeBytes())
	if logger != nil {
		logger.Debug("spawn: mcpjungle token resolved",
			"agent_id", uuidFromBytes(agentID), "env_var", vault.MCPJungleBearerTokenEnvVar)
	}
	return out, nil
}

// uuidFromBytes mirrors uuidString in pipeline.go without colliding
// on the unexported symbol. Used only by the m8 logging path.
func uuidFromBytes(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}

// emitSyntheticCreate resolves the row's dept slug and emits the
// synthetic create event. Best-effort.
func emitSyntheticCreate(ctx context.Context, pool *pgxpool.Pool, q *store.Queries, row store.ListBlockedDependentsRow, logger *slog.Logger) {
	dept, err := q.GetDepartmentByID(ctx, row.DepartmentID)
	if err != nil {
		if logger != nil {
			logger.Warn("spawn: GetDepartmentByID for synthetic create",
				"ticket_id", uuidString(row.ID), "err", err)
		}
		return
	}
	channel := fmt.Sprintf("work.ticket.created.%s.%s", dept.Slug, row.ColumnSlug)
	payload := fmt.Sprintf(
		`{"event_id":%q,"ticket_id":%q,"department_id":%q,"column_slug":%q}`,
		uuidString(row.ID), uuidString(row.ID), uuidString(row.DepartmentID), row.ColumnSlug,
	)
	if _, err := pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload); err != nil {
		if logger != nil {
			logger.Warn("spawn: synthetic pg_notify failed",
				"channel", channel, "ticket_id", uuidString(row.ID), "err", err)
		}
	}
}
