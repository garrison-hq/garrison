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
