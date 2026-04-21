-- Tables and partial indexes for M1 — verbatim from
-- specs/_context/m1-context.md §"Data model for M1".
-- gen_random_uuid() is built into Postgres 13+; no pgcrypto extension needed.

-- +goose Up

-- Schema: org
CREATE TABLE departments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  concurrency_cap INT NOT NULL DEFAULT 3,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Schema: work
CREATE TABLE tickets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  department_id UUID NOT NULL REFERENCES departments(id),
  objective TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE event_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  channel TEXT NOT NULL,
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  processed_at TIMESTAMPTZ
);

CREATE INDEX idx_event_outbox_unprocessed
  ON event_outbox (created_at)
  WHERE processed_at IS NULL;

CREATE TABLE agent_instances (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  department_id UUID NOT NULL REFERENCES departments(id),
  ticket_id UUID NOT NULL REFERENCES tickets(id),
  pid INT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at TIMESTAMPTZ,
  status TEXT NOT NULL,
  exit_reason TEXT
);

CREATE INDEX idx_agent_instances_running
  ON agent_instances (department_id)
  WHERE status = 'running';

-- +goose Down
DROP INDEX IF EXISTS idx_agent_instances_running;
DROP TABLE IF EXISTS agent_instances;
DROP INDEX IF EXISTS idx_event_outbox_unprocessed;
DROP TABLE IF EXISTS event_outbox;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS departments;
