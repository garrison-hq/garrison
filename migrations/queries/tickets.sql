-- name: GetTicketByID :one
SELECT * FROM tickets WHERE id = $1;

-- name: InsertTicket :one
INSERT INTO tickets (department_id, objective)
VALUES ($1, $2)
RETURNING *;
