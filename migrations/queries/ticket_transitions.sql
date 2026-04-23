-- name: UpdateTicketTransitionHygiene :exec
-- At-most-once-to-terminal per FR-215: only overwrites NULL or 'pending'.
-- Once a terminal status (clean|missing_diary|missing_kg|thin) is set,
-- subsequent calls for the same row are no-ops.
UPDATE ticket_transitions
SET hygiene_status = $2
WHERE id = $1
  AND (hygiene_status IS NULL OR hygiene_status = 'pending');

-- name: ListStuckHygieneTransitions :many
-- Rows older than the delay interval whose hygiene is unresolved. Used by
-- the periodic sweep goroutine (FR-216) to recover rows the LISTEN path
-- missed or couldn't evaluate (e.g. palace unreachable → StatusPending).
-- $1 is the delay interval (e.g. '5 seconds'); $2 is the batch cap.
SELECT id,
       ticket_id,
       triggered_by_agent_instance_id,
       from_column,
       to_column,
       at,
       hygiene_status
FROM ticket_transitions
WHERE (hygiene_status IS NULL OR hygiene_status = 'pending')
  AND at < NOW() - $1::interval
ORDER BY at ASC
LIMIT $2;
