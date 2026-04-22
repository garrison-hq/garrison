-- name: InsertRunningInstance :one
INSERT INTO agent_instances (department_id, ticket_id, pid, status)
VALUES ($1, $2, $3, 'running')
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
