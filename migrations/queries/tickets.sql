-- name: GetTicketByID :one
SELECT * FROM tickets WHERE id = $1;

-- name: InsertTicket :one
INSERT INTO tickets (department_id, objective)
VALUES ($1, $2)
RETURNING *;

-- name: UpdateTicketColumnSlug :exec
UPDATE tickets SET column_slug = $2 WHERE id = $1;

-- name: InsertTicketTransition :one
INSERT INTO ticket_transitions (ticket_id, from_column, to_column, triggered_by_agent_instance_id)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: SelectTicketObjective :one
-- M2.2.1: read the ticket's objective text for the FR-263 diary
-- serialization — the supervisor's atomic writer prepends this prose
-- to the drawer body so mempalace_search queries keyed on objective
-- text return the drawer (semantic-similarity fails on raw UUIDs per
-- M2.2 live-run finding).
SELECT objective FROM tickets WHERE id = $1;
