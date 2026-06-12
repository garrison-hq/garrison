-- M10 — Ingress connectors (the company becomes reactive to the outside world).
--
-- Lands the M10 schema in one migration (plan §Data model):
--
-- 1. ingress_deliveries — per-delivery idempotency record keyed unique on
--    (connector_id, external_delivery_id). The unique-constraint-on-INSERT
--    is the dedup signal surviving the M1 dedup-commit-vs-terminal-commit
--    race (FR-201, SR2, plan decision 3+4). Append-only (FR-203). ticket_id
--    is nullable because the row is inserted before the ticket in the same
--    tx, then backfilled; it stays NULL only on the abort path (never
--    committed to the DB in that state). The index on (connector_id,
--    created_at DESC) serves the connector-status read surface (decision 14).
--
-- 2. throttle_events.kind CHECK extension — adds 'ingress_rate_cap_exceeded'
--    to the existing {company_budget_exceeded, rate_limit_pause,
--    dept_weekly_ticket_budget_exceeded} set, following the M8 precedent
--    (M8 migration extended the same CHECK with dept_weekly_ticket_budget_exceeded).
--    The Down migration deletes M10-era rows before restoring the M8/M9
--    three-value CHECK (M8/M9 Down pattern).
--
-- 3. Dashboard grants — GRANT SELECT ON ingress_deliveries TO
--    garrison_dashboard_app (connector-status surface is read-only;
--    supervisor-only writes, mirroring M9's scheduled_task_runs pattern).
--
-- Migration version: 20260612000000 (today 2026-06-12; latest existing is
-- 20260610000002_m9_scheduled_wakeups.sql; no same-day collision checked
-- at plan time, M9 gotcha 5 / plan decision 13).

-- +goose Up

-- 1. ingress_deliveries
CREATE TABLE ingress_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id TEXT NOT NULL,
    external_delivery_id TEXT NOT NULL,
    ticket_id UUID NULL REFERENCES tickets(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, external_delivery_id)    -- the dedup signal (FR-201, SR2)
);

-- connector-status reads: last delivery, accepted count (decision 14)
CREATE INDEX idx_ingress_deliveries_connector_created
    ON ingress_deliveries (connector_id, created_at DESC);

-- 2. throttle_events.kind CHECK extension (M8 pattern)
ALTER TABLE throttle_events DROP CONSTRAINT IF EXISTS throttle_events_kind_check;
ALTER TABLE throttle_events
  ADD CONSTRAINT throttle_events_kind_check
  CHECK (kind IN (
    'company_budget_exceeded',
    'rate_limit_pause',
    -- M8 per-department weekly ticket-creation budget gate fire.
    'dept_weekly_ticket_budget_exceeded',
    -- M10 per-connector rate-cap breach.
    'ingress_rate_cap_exceeded'
  ));

-- 3. Dashboard grants (read-only; supervisor writes only)
GRANT SELECT ON ingress_deliveries TO garrison_dashboard_app;

-- +goose Down

-- 2. Delete M10-era throttle_events rows before restoring the M8/M9 CHECK
--    (M8/M9 Down precedent: clear rows before reverting a CHECK).
DELETE FROM throttle_events WHERE kind = 'ingress_rate_cap_exceeded';

ALTER TABLE throttle_events DROP CONSTRAINT IF EXISTS throttle_events_kind_check;
ALTER TABLE throttle_events
  ADD CONSTRAINT throttle_events_kind_check
  CHECK (kind IN (
    'company_budget_exceeded',
    'rate_limit_pause',
    'dept_weekly_ticket_budget_exceeded'
  ));

-- 3. Revoke dashboard grant
REVOKE SELECT ON ingress_deliveries FROM garrison_dashboard_app;

-- 1. Drop ingress_deliveries
DROP INDEX IF EXISTS idx_ingress_deliveries_connector_created;
DROP TABLE IF EXISTS ingress_deliveries;
