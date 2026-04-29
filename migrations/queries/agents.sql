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

-- name: FindAgentByRoleSlug :one
-- M5.3: chat verbs accept role_slug as a single string. The agents
-- schema has UNIQUE (department_id, role_slug); within active rows,
-- a slug may appear in multiple departments. Returns the single
-- match deterministically ordered by department_id; verbs use
-- CountAgentsByRoleSlug first to reject multi-department slugs as
-- ambiguous.
SELECT id, department_id, role_slug, status FROM agents
WHERE role_slug = @role_slug
ORDER BY department_id
LIMIT 1;

-- name: CountAgentsByRoleSlug :one
-- M5.3 ambiguity check: returns the number of agents matching the
-- given role_slug. Verbs reject calls where count > 1 to force the
-- operator to disambiguate.
SELECT count(*)::int8 AS count FROM agents WHERE role_slug = @role_slug;

-- name: UpdateAgentStatus :exec
-- M5.3 garrison-mutate.pause_agent / resume_agent. Sets the agent's
-- status; pause sets 'paused', resume sets 'active'. Existing M2.x
-- spawn loop already filters on status='active', so paused agents
-- naturally stop receiving new spawns.
UPDATE agents SET status = @status WHERE id = @id;

-- name: UpdateAgentConfigFields :exec
-- M5.3 garrison-mutate.edit_agent_config. Partial-update of operator-
-- editable agent fields. Verb resolves COALESCE on the Go side after
-- the leak-scan pass.
UPDATE agents
   SET model = @model,
       agent_md = @agent_md,
       palace_wing = @palace_wing
 WHERE id = @id;
