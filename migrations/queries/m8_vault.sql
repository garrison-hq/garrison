-- M8 vault-grant queries.
-- Used by supervisor/internal/vault/grants.go.
--
-- The M2.3 agent_role_secrets table is extended in M8 with:
--   - id UUID NOT NULL PRIMARY KEY DEFAULT gen_random_uuid()
--   - agent_id UUID NULL REFERENCES agents(id) ON DELETE CASCADE
-- Existing role-scoped grants have agent_id IS NULL. M8 agent-scoped
-- grants (MCPJungle bearer tokens) have agent_id non-NULL. Partial
-- unique indexes enforce per-anchor uniqueness.

-- name: ListGrantsForRoleAndAgent :many
-- Returns both role-scoped grants (role_slug=<role> AND agent_id IS NULL)
-- and agent-scoped grants (agent_id=<agent>). Used at spawn time to fetch
-- every vault path the agent is authorised to read.
SELECT id, role_slug, agent_id, secret_path, env_var_name, customer_id, granted_at
  FROM agent_role_secrets
 WHERE customer_id = sqlc.arg(customer_id)
   AND (
     (role_slug = sqlc.arg(role_slug) AND agent_id IS NULL)
     OR
     (agent_id = sqlc.arg(agent_id))
   )
 ORDER BY env_var_name;

-- name: InsertAgentScopedSecret :exec
-- Used by the supervisor's mcpjungle reconciler at agent activation:
-- after creating the McpClient + writing the bearer token to Infisical
-- at mcpjungle/agents/<agent-id>, insert this grant so the spawn-time
-- vault fetcher will resolve the path for that specific agent.
-- role_slug carries the agent's role (for forensics + the trigger's
-- secret_metadata.allowed_role_slugs aggregation); agent_id is the
-- discriminator that makes the grant agent-scoped.
INSERT INTO agent_role_secrets (
    role_slug, agent_id, secret_path, env_var_name, customer_id, granted_by
) VALUES (
    sqlc.arg(role_slug),
    sqlc.arg(agent_id),
    sqlc.arg(secret_path),
    sqlc.arg(env_var_name),
    sqlc.arg(customer_id),
    sqlc.arg(granted_by)
);
