-- M6 — CEO ticket decomposition + hygiene + cost-throttle.
--
-- Schema additions for the three M6 threads (see
-- specs/015-m6-decomposition-hygiene-throttle/spec.md). Every change is
-- additive; no destructive drops, no row updates required. Existing
-- rows default to NULL semantics on the new nullable columns.
--
-- 1. tickets.parent_ticket_id (UUID NULLABLE, FK → tickets.id) — the
--    parent/child linkage for CEO-driven decomposition (FR-001..FR-003).
--    Self-referencing FK; the child shares the parent's department_id
--    by spec validation in the verb handler (T010), not by SQL CHECK,
--    because cross-table CHECK isn't supported and the validation
--    error message needs to be operator-readable.
--
-- 2. companies.daily_budget_usd (NUMERIC(10,2) NULLABLE) — per-company
--    rolling-24h spend cap (FR-030). NULL = no enforcement; positive
--    value = enforced cap; zero = full pause (semantically equivalent
--    to concurrency_cap=0 for the spend axis).
--
-- 3. companies.pause_until (TIMESTAMPTZ NULLABLE) — per-company
--    rate-limit back-off window (FR-040). NULL or past timestamp = no
--    pause; future timestamp = subsequent event_outbox rows for this
--    company are deferred until the timestamp elapses.
--
-- 4. throttle_events table — append-only audit trail for every
--    company-budget defer + rate-limit pause (FR-033, FR-042). The
--    kind column is constrained to the two enumerated values via
--    CHECK (rather than a Postgres ENUM type) so the constraint can
--    be amended in a future migration without an ALTER TYPE round.
--    The dashboard's hygiene-throttle sub-table (T016) reads via
--    GRANT SELECT to garrison_dashboard_app; the supervisor INSERTs
--    via the schema-owner connection (no constrained role).
--
-- pg_notify channel `work.throttle.event` is emitted in the same tx
-- as the throttle_events INSERT (FR-045 + plan §1 atomicity contract).
-- No schema element here defines the channel — it's a string literal
-- the supervisor passes to pg_notify(); future audits should grep
-- `internal/throttle/events.go` for the canonical channel name.

-- +goose Up

-- 1. tickets.parent_ticket_id
ALTER TABLE tickets
  ADD COLUMN parent_ticket_id UUID NULL REFERENCES tickets(id);
CREATE INDEX idx_tickets_parent
  ON tickets (parent_ticket_id)
  WHERE parent_ticket_id IS NOT NULL;

-- 2 + 3. companies.daily_budget_usd + companies.pause_until
ALTER TABLE companies
  ADD COLUMN daily_budget_usd NUMERIC(10,2) NULL,
  ADD COLUMN pause_until TIMESTAMPTZ NULL;

-- 4. throttle_events table
CREATE TABLE throttle_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id UUID NOT NULL REFERENCES companies(id),
  kind TEXT NOT NULL CHECK (kind IN ('company_budget_exceeded', 'rate_limit_pause')),
  fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  payload JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX idx_throttle_events_company_fired
  ON throttle_events (company_id, fired_at DESC);

GRANT SELECT ON throttle_events TO garrison_dashboard_app;

-- +goose Down

REVOKE SELECT ON throttle_events FROM garrison_dashboard_app;
DROP INDEX IF EXISTS idx_throttle_events_company_fired;
DROP TABLE IF EXISTS throttle_events;
ALTER TABLE companies DROP COLUMN IF EXISTS pause_until;
ALTER TABLE companies DROP COLUMN IF EXISTS daily_budget_usd;
DROP INDEX IF EXISTS idx_tickets_parent;
ALTER TABLE tickets DROP COLUMN IF EXISTS parent_ticket_id;
