-- M8 ticket queries.
-- Used by:
--   - supervisor/internal/garrisonmutate/verbs_tickets.go (create_ticket
--     agent-caller extension + cycle walker + parent auto-inherit).
--   - supervisor/internal/spawn/spawn.go (dependency-satisfaction check
--     at spawn-prep + transition-listener unblock).

-- name: InsertTicketM8 :one
-- Variant of InsertChatTicket carrying both parent_ticket_id (M6) and
-- depends_on_ticket_id (M8). Caller resolves auto-inherit + cycle check
-- before invocation.
INSERT INTO tickets (
    department_id, objective, acceptance_criteria,
    column_slug, metadata, origin,
    created_via_chat_session_id, parent_ticket_id, depends_on_ticket_id
) VALUES (
    sqlc.arg(department_id),
    sqlc.arg(objective),
    sqlc.arg(acceptance_criteria),
    sqlc.arg(column_slug),
    sqlc.arg(metadata),
    sqlc.arg(origin),
    sqlc.arg(created_via_chat_session_id),
    sqlc.arg(parent_ticket_id),
    sqlc.arg(depends_on_ticket_id)
)
RETURNING id, created_at;

-- name: GetTicketDependencyChain :many
-- O(N) walk for cycle detection. Caller iterates up to depth_cap (32 per
-- FR-103); rejects on cycle (next_id == start_id) or exhaustion.
WITH RECURSIVE chain(chain_id, depth) AS (
    SELECT t.depends_on_ticket_id, 1
      FROM tickets t
     WHERE t.id = sqlc.arg(start_id)
       AND t.depends_on_ticket_id IS NOT NULL
    UNION ALL
    SELECT t.depends_on_ticket_id, c.depth + 1
      FROM tickets t
      JOIN chain c ON t.id = c.chain_id
     WHERE t.depends_on_ticket_id IS NOT NULL
       AND c.depth < sqlc.arg(depth_cap)::int
)
SELECT chain_id, depth FROM chain;

-- name: ListBlockedDependents :many
-- Read by the dispatcher's transition listener (FR-105). For every
-- ticket transition, find tickets with depends_on_ticket_id pointing at
-- the transitioning ticket that are still in column_slug='todo'.
SELECT id, department_id, column_slug
  FROM tickets
 WHERE depends_on_ticket_id = sqlc.arg(predecessor_id)
   AND column_slug = 'todo';

-- name: GetTicketSatisfactionState :one
-- Read by spawn-prep (FR-102) to decide whether to spawn a dependent.
-- Returns the predecessor's column_slug + the predecessor's dept's
-- dependency_satisfaction_columns array. Caller compares.
SELECT t.column_slug,
       d.dependency_satisfaction_columns
  FROM tickets t
  JOIN departments d ON d.id = t.department_id
 WHERE t.id = sqlc.arg(predecessor_id);

-- name: GetAgentInstanceTicketID :one
-- Used by the create_ticket verb's auto-inherit path (FR-006): when an
-- agent caller omits parent_ticket_id, fall back to the agent's current
-- spawn's ticket_id.
SELECT ticket_id
  FROM agent_instances
 WHERE id = sqlc.arg(agent_instance_id);
