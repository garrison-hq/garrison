-- M7 per-agent runtime queries.
-- agent_container_events writes + reads, host_uid allocator,
-- agents extension columns. Used by:
--   - supervisor/internal/agentcontainer/socketproxy.go (lifecycle events)
--   - supervisor/internal/agentcontainer/reconcile.go (restart adoption)
--   - supervisor/internal/migrate7/run.go (grandfathering migration)
--   - supervisor/internal/garrisonmutate/approve.go (post-approval activation).

-- name: InsertAgentContainerEvent :one
INSERT INTO agent_container_events (
    agent_id, kind, image_digest, started_at, stopped_at,
    stop_reason, cgroup_caps_jsonb, retention_class
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at;

-- name: GetLatestContainerEventForAgent :one
SELECT * FROM agent_container_events
WHERE agent_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListContainerEventsByAgent :many
SELECT * FROM agent_container_events
WHERE agent_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListContainerEventsByKind :many
SELECT * FROM agent_container_events
WHERE kind = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: AllocateNextHostUID :one
-- FR-206a sequential allocator. Returns MAX(host_uid)+1 within the
-- per-customer range; if no rows exist in range, returns range start.
-- Caller guards against ceiling-exhaustion (returned value > range_end).
SELECT COALESCE(MAX(host_uid), $1::int - 1) + 1 AS next_uid
FROM agents
WHERE host_uid >= $1::int AND host_uid <= $2::int;

-- name: SetAgentImageDigest :exec
UPDATE agents
SET image_digest = $1
WHERE id = $2;

-- name: SetAgentHostUID :exec
UPDATE agents
SET host_uid = $1
WHERE id = $2;

-- name: SetAgentLastGrandfathered :exec
UPDATE agents
SET last_grandfathered_at = NOW(),
    image_digest = $1,
    host_uid = $2
WHERE id = $3 AND last_grandfathered_at IS NULL;

-- name: ListAgentsNotGrandfathered :many
-- Read at supervisor startup by migrate7.Run; agents with NULL
-- last_grandfathered_at are M2.x-seeded and need the M7 cutover.
SELECT * FROM agents
WHERE last_grandfathered_at IS NULL
ORDER BY created_at ASC;

-- name: ListActiveAgentsWithContainerState :many
-- Read by agentcontainer.Reconcile at supervisor restart. Joins each
-- active agent with its most recent container event so the reconciler
-- can compare against `docker ps`.
SELECT
    a.id AS agent_id,
    a.image_digest,
    a.host_uid,
    a.last_grandfathered_at,
    e.kind AS last_event_kind,
    e.created_at AS last_event_at
FROM agents a
LEFT JOIN LATERAL (
    SELECT kind, created_at
    FROM agent_container_events
    WHERE agent_id = a.id
    ORDER BY created_at DESC
    LIMIT 1
) e ON true
WHERE a.status = 'active';

-- name: SetAgentRuntimeCaps :exec
UPDATE agents
SET runtime_caps = $1
WHERE id = $2;

-- name: SetAgentEgressGrants :exec
UPDATE agents
SET egress_grant_jsonb = $1
WHERE id = $2;

-- name: SetAgentMCPServers :exec
UPDATE agents
SET mcp_servers_jsonb = $1
WHERE id = $2;
