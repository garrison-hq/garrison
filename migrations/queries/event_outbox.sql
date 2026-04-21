-- name: GetEventByID :one
SELECT * FROM event_outbox WHERE id = $1;

-- name: SelectUnprocessedEvents :many
SELECT id, channel, payload, created_at
FROM event_outbox
WHERE processed_at IS NULL
ORDER BY created_at
LIMIT 100;

-- name: LockEventForProcessing :one
SELECT id, channel, payload, created_at, processed_at
FROM event_outbox
WHERE id = $1
FOR UPDATE;

-- name: MarkEventProcessed :exec
UPDATE event_outbox
SET processed_at = NOW()
WHERE id = $1 AND processed_at IS NULL;
