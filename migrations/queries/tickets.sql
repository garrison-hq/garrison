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
