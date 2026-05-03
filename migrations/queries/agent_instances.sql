-- name: InsertRunningInstance :one
INSERT INTO agent_instances (department_id, ticket_id, pid, status, role_slug)
VALUES ($1, $2, $3, 'running', $4)
RETURNING id;

-- name: UpdateInstanceTerminal :exec
UPDATE agent_instances
SET status = $2,
    finished_at = NOW(),
    exit_reason = $3
WHERE id = $1;

-- name: UpdateInstanceTerminalWithCost :exec
UPDATE agent_instances
SET status = $2,
    finished_at = NOW(),
    exit_reason = $3,
    total_cost_usd = $4
WHERE id = $1;

-- name: UpdateInstanceTerminalWithCostAndWakeup :exec
UPDATE agent_instances
SET status = $2,
    finished_at = NOW(),
    exit_reason = $3,
    total_cost_usd = $4,
    wake_up_status = $5
WHERE id = $1;

-- name: GetAgentInstanceRunWindow :one
-- Resolves agent_instance_id → (started_at, finished_at, palace_wing, role_slug)
-- for the hygiene checker (FR-213). Palace wing comes from the agents table
-- via the (department_id, role_slug) key.
SELECT ai.id,
       ai.started_at,
       ai.finished_at,
       ai.department_id,
       ai.role_slug,
       a.palace_wing
FROM agent_instances ai
LEFT JOIN agents a
  ON a.department_id = ai.department_id AND a.role_slug = ai.role_slug
WHERE ai.id = $1;

-- name: UpdatePID :exec
UPDATE agent_instances
SET pid = $2
WHERE id = $1;

-- name: UpdateInstanceM7Hashes :exec
-- M7 FR-303 / FR-304 / FR-305: every spawn records the immutable
-- preamble's hash, the cwd CLAUDE.md hash (NULL when claude is
-- invoked without a CLAUDE.md in the workspace), and the per-agent
-- container image digest (empty string when the spawn ran in
-- direct-exec mode pre-migrate7). Called after InsertRunningInstance
-- as part of step 3a in spawn.runRealClaude.
UPDATE agent_instances
   SET preamble_hash = sqlc.arg(preamble_hash),
       claude_md_hash = sqlc.arg(claude_md_hash),
       image_digest = sqlc.arg(image_digest)
 WHERE id = sqlc.arg(id);

-- name: CountRunningByDepartment :one
SELECT COUNT(*) FROM agent_instances
WHERE department_id = $1 AND status = 'running';

-- name: RecoverStaleRunning :execrows
UPDATE agent_instances
SET status = 'failed',
    exit_reason = 'supervisor_restarted',
    finished_at = NOW()
WHERE status = 'running'
  AND started_at < NOW() - INTERVAL '5 minutes';

-- name: SelectAgentInstanceFinalizedState :one
-- M2.2.1 FR-260 + T008: the finalize MCP server calls this on every
-- tool call to detect the already-committed state (Clarification
-- 2026-04-23 Q2). The hygiene listener/sweep also uses exit_reason to
-- route between the finalize path (EvaluateFinalizeOutcome) and the
-- legacy M2.2 palace-query path (Evaluate).
SELECT
    ai.status,
    ai.exit_reason,
    EXISTS(
        SELECT 1 FROM ticket_transitions tt
        WHERE tt.triggered_by_agent_instance_id = ai.id
    ) AS has_transition
FROM agent_instances ai
WHERE ai.id = $1;
