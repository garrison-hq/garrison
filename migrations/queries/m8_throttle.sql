-- M8 runaway-control queries.
-- Used by supervisor/internal/throttle/dept_weekly.go.

-- name: GetDeptWeeklyState :one
-- Returns the rolling-7-day ticket-creation count for the department + the
-- configured budget (NULL = unlimited; M8 alpha default). Caller decides
-- whether count + 1 would exceed budget; on rejection writes a
-- throttle_events row (M6's existing InsertThrottleEvent query, with
-- kind='dept_weekly_ticket_budget_exceeded' added to the CHECK in M8).
SELECT
    d.id AS department_id,
    d.slug AS department_slug,
    d.weekly_ticket_budget,
    (SELECT COUNT(*) FROM tickets
       WHERE department_id = d.id
         AND created_at > NOW() - INTERVAL '7 days')::bigint AS current_count
  FROM departments d
 WHERE d.id = sqlc.arg(department_id);

-- name: ListDeptWeeklyStateAll :many
-- Used by the dashboard /hygiene runaway-control surface (FR-501).
-- Returns one row per department with the rolling-window count + budget +
-- the most recent throttle_events.fired_at for kind='dept_weekly_ticket_budget_exceeded'
-- (NULL if never fired). Single query so the dashboard render is one DB
-- round-trip.
SELECT
    d.id AS department_id,
    d.slug AS department_slug,
    d.weekly_ticket_budget,
    (SELECT COUNT(*) FROM tickets
       WHERE department_id = d.id
         AND created_at > NOW() - INTERVAL '7 days')::bigint AS current_count,
    (SELECT MAX(fired_at) FROM throttle_events
       WHERE kind = 'dept_weekly_ticket_budget_exceeded'
         AND (payload->>'department_id')::uuid = d.id) AS last_fired_at
  FROM departments d
 ORDER BY d.slug ASC;
