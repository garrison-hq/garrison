-- name: GetAgentByDepartmentAndRole :one
SELECT * FROM agents
WHERE department_id = $1 AND role_slug = $2 AND status = 'active';

-- name: ListActiveAgents :many
SELECT * FROM agents
WHERE status = 'active'
ORDER BY department_id, role_slug;

-- name: UpdateAgentMD :exec
UPDATE agents
SET agent_md = $2
WHERE id = $1;

-- name: GetAgentByID :one
SELECT * FROM agents WHERE id = $1;
