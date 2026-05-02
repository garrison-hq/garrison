-- M6 throttle-actuator queries (spec FR-031, FR-033, FR-040..FR-044).
-- All four run on the caller's transaction so the supervisor's
-- spawn-prep gate + rate-limit observer can compose audit writes
-- atomically with the side-effect (event_outbox row stays unprocessed
-- for budget defer; pause_until UPDATE for rate-limit pause).

-- name: GetCompanyThrottleState :one
-- Single read returning budget + pause + rolling-24h cost. The lateral
-- subquery sums total_cost_usd across the company's department graph
-- since NOW() - 24h; COALESCE handles companies with no agent_instances
-- yet (returns 0 instead of NULL). The throttle gate only fires after
-- US2 (FR-020) lands honest cost telemetry — until then, the sum reads
-- low for clean-finalize runs (docs/issues/cost-telemetry-blind-spot.md).
SELECT
  c.id AS company_id,
  c.daily_budget_usd,
  c.pause_until,
  COALESCE(
    (SELECT SUM(ai.total_cost_usd)
       FROM agent_instances ai
       JOIN tickets t     ON t.id = ai.ticket_id
       JOIN departments d ON d.id = t.department_id
      WHERE d.company_id = c.id
        AND ai.started_at >= NOW() - INTERVAL '24 hours'),
    0
  )::NUMERIC(10,2) AS cost_24h_usd
FROM companies c
WHERE c.id = $1;

-- name: UpdateCompanyPauseUntil :exec
-- M6 T008: rate-limit observer flips into actuator mode. Last write
-- wins on concurrent rate-limit events for the same company; the
-- throttle_events row carries the audit so both events are visible.
UPDATE companies
SET pause_until = $2
WHERE id = $1;

-- name: InsertThrottleEvent :one
-- M6 T004: append-only audit row. kind is constrained at the schema
-- level via CHECK; payload is JSONB carrying forensic detail (current
-- spend + estimated next + budget for company_budget_exceeded;
-- pause_until + back-off seconds for rate_limit_pause).
INSERT INTO throttle_events (company_id, kind, payload)
VALUES ($1, $2, $3)
RETURNING id, company_id, kind, fired_at, payload;

-- name: ListThrottleEventsByCompany :many
-- M6 T016 read-side query for the dashboard hygiene throttle sub-table.
-- ORDER BY fired_at DESC matches the dashboard's "most recent first"
-- list shape; the (company_id, fired_at DESC) index serves this query
-- directly with no sort step.
SELECT id, company_id, kind, fired_at, payload
FROM throttle_events
WHERE company_id = $1
ORDER BY fired_at DESC
LIMIT $2;

-- name: NotifyThrottleEvent :exec
-- M6 T004: in-tx pg_notify. Composes with InsertThrottleEvent on the
-- caller's tx so the audit row + notify land atomically — the
-- dashboard SSE bridge (T015) sees the notify only if the row
-- committed. Mirrors the M5.x chat pgNotifyExecSQL pattern but bound
-- to the channel constant `work.throttle.event` so the call site
-- can't typo the channel name.
SELECT pg_notify('work.throttle.event', sqlc.arg('payload')::text);
