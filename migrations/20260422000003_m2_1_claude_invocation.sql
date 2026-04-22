-- M2.1 migration: Claude Code invocation schema, role, trigger, and seeds.
-- Builds on the M1 schema (20260421000001, 20260421000002). Adds:
--   - agent_instances.total_cost_usd column (FR-120, NFR-108)
--   - companies, agents, ticket_transitions tables
--   - columns on departments + tickets the M2.1 seed/trigger reference
--   - garrison_agent_ro role with SELECT-only grants (NFR-104)
--   - rewritten emit_ticket_created trigger emitting qualified channels
--   - one companies row, one engineering department, one engineer agent
--     (placeholder agent_md — replaced by T003)
--
-- The garrison_agent_ro role is created with LOGIN but no password. The role
-- cannot authenticate via password until cmd/supervisor/migrate.go applies
-- GARRISON_AGENT_RO_PASSWORD via ALTER ROLE post-migrate. Running this
-- migration through external `goose` (not via supervisor --migrate) leaves
-- the role password-less; supervisor startup's config validation will refuse
-- to run without the env var, so the failure layer is clear.

-- +goose Up

-- Section 1 — schema changes

-- (1a) Cost capture on agent_instances (FR-120, NFR-108).
ALTER TABLE agent_instances ADD COLUMN total_cost_usd NUMERIC(10, 6);

-- (1b) companies — organizational root; M2.1 seeds exactly one row.
CREATE TABLE companies (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- (1c) agents — per-role config table (FR-118, FR-122).
CREATE TABLE agents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  department_id UUID NOT NULL REFERENCES departments(id),
  role_slug TEXT NOT NULL,
  agent_md TEXT NOT NULL,
  model TEXT NOT NULL,
  skills JSONB NOT NULL DEFAULT '[]'::jsonb,
  mcp_tools JSONB NOT NULL DEFAULT '[]'::jsonb,
  listens_for JSONB NOT NULL,
  palace_wing TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (department_id, role_slug)
);

CREATE INDEX idx_agents_active_by_dept
  ON agents (department_id, role_slug)
  WHERE status = 'active';

-- (1d) ticket_transitions — supervisor-written audit trail (FR-113).
CREATE TABLE ticket_transitions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id UUID NOT NULL REFERENCES tickets(id),
  from_column TEXT,
  to_column TEXT NOT NULL,
  triggered_by_agent_instance_id UUID REFERENCES agent_instances(id),
  triggered_by_user BOOLEAN NOT NULL DEFAULT FALSE,
  at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  hygiene_status TEXT
);

CREATE INDEX idx_ticket_transitions_by_ticket ON ticket_transitions (ticket_id, at);

-- (1e) departments — fields the M2.1 seed row needs (FR-121).
-- workspace_path is nullable at the schema level; the seed row populates it
-- and the supervisor's FR-123 startup check fails loudly on NULL.
ALTER TABLE departments ADD COLUMN company_id UUID REFERENCES companies(id);
ALTER TABLE departments ADD COLUMN workspace_path TEXT;
ALTER TABLE departments ADD COLUMN workflow JSONB NOT NULL DEFAULT '{}'::jsonb;

-- (1f) tickets — fields the M2.1 trigger, seeded workflow, and engineer
-- SELECT query all reference (trigger §Section 3; FR-119).
ALTER TABLE tickets ADD COLUMN column_slug TEXT NOT NULL DEFAULT 'todo';
ALTER TABLE tickets ADD COLUMN acceptance_criteria TEXT;
ALTER TABLE tickets ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE tickets ADD COLUMN origin TEXT NOT NULL DEFAULT 'sql';

-- Section 2 — read-only role.
CREATE ROLE garrison_agent_ro LOGIN;
GRANT SELECT ON tickets, ticket_transitions, departments, agents TO garrison_agent_ro;

-- Section 3 — trigger rewrite. Emits qualified channels of the form
-- work.ticket.created.<department_slug>.<column_slug>. The payload carries
-- both slugs so the event_outbox row is self-describing on fallback-poll read.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION emit_ticket_created() RETURNS trigger AS $$
DECLARE
  event_id UUID;
  payload JSONB;
  dept_slug TEXT;
  channel TEXT;
BEGIN
  SELECT slug INTO dept_slug FROM departments WHERE id = NEW.department_id;
  IF dept_slug IS NULL THEN
    RAISE EXCEPTION 'emit_ticket_created: department_id % has no row in departments', NEW.department_id;
  END IF;
  channel := 'work.ticket.created.' || dept_slug || '.' || NEW.column_slug;
  payload := jsonb_build_object(
    'ticket_id', NEW.id,
    'department_id', NEW.department_id,
    'department_slug', dept_slug,
    'column_slug', NEW.column_slug,
    'created_at', NEW.created_at
  );
  INSERT INTO event_outbox (channel, payload)
    VALUES (channel, payload)
    RETURNING id INTO event_id;
  PERFORM pg_notify(channel, jsonb_build_object('event_id', event_id)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Section 4 — seed companies + engineering department.
INSERT INTO companies (id, name)
  SELECT gen_random_uuid(), 'Garrison operator'
  WHERE NOT EXISTS (SELECT 1 FROM companies);

INSERT INTO departments (id, company_id, slug, name, workspace_path, concurrency_cap, workflow)
VALUES (
  gen_random_uuid(),
  (SELECT id FROM companies LIMIT 1),
  'engineering',
  'Engineering',
  '/workspaces/engineering',
  1,
  '{"columns":[{"slug":"todo","label":"To do","entry_from":["backlog"]},{"slug":"done","label":"Done","entry_from":["todo"]}],"transitions":{"todo":["done"]}}'::jsonb
);

-- Section 5 — seed engineer agent with a placeholder agent_md. T003 replaces
-- the placeholder with the actual engineer.md file contents via
-- `make seed-engineer-agent`. Keeping the placeholder here lets T001 land
-- independently of T003.
INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
VALUES (
  gen_random_uuid(),
  (SELECT id FROM departments WHERE slug = 'engineering'),
  'engineer',
  'PLACEHOLDER — seed by T003',
  'claude-haiku-4-5-20251001',
  '[]'::jsonb,
  '[]'::jsonb,
  '["work.ticket.created.engineering.todo"]'::jsonb,
  NULL,
  'active'
);

-- +goose Down

-- Role cleanup: DROP ROLE fails while the role has privilege grants. DROP
-- OWNED BY revokes all grants the role holds in this database (SELECT on the
-- four M2.1 tables) before the DROP ROLE call.
DROP OWNED BY garrison_agent_ro;
DROP ROLE IF EXISTS garrison_agent_ro;

-- Seeds, child rows before parents so FKs don't block.
DELETE FROM agents WHERE role_slug = 'engineer' AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');
DELETE FROM departments WHERE slug = 'engineering';
DELETE FROM companies WHERE name = 'Garrison operator';

-- Restore M1's emit_ticket_created function body (unqualified channel).

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION emit_ticket_created() RETURNS trigger AS $$
DECLARE
  event_id UUID;
  payload JSONB;
BEGIN
  payload := jsonb_build_object(
    'ticket_id', NEW.id,
    'department_id', NEW.department_id,
    'created_at', NEW.created_at
  );
  INSERT INTO event_outbox (channel, payload)
    VALUES ('work.ticket.created', payload)
    RETURNING id INTO event_id;
  PERFORM pg_notify('work.ticket.created', jsonb_build_object('event_id', event_id)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Reverse tickets + departments column additions.
ALTER TABLE tickets DROP COLUMN IF EXISTS origin;
ALTER TABLE tickets DROP COLUMN IF EXISTS metadata;
ALTER TABLE tickets DROP COLUMN IF EXISTS acceptance_criteria;
ALTER TABLE tickets DROP COLUMN IF EXISTS column_slug;
ALTER TABLE departments DROP COLUMN IF EXISTS workflow;
ALTER TABLE departments DROP COLUMN IF EXISTS workspace_path;
ALTER TABLE departments DROP COLUMN IF EXISTS company_id;

-- Drop new tables in reverse dependency order.
DROP INDEX IF EXISTS idx_ticket_transitions_by_ticket;
DROP TABLE IF EXISTS ticket_transitions;
DROP INDEX IF EXISTS idx_agents_active_by_dept;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS companies;

-- Finally, the cost column.
ALTER TABLE agent_instances DROP COLUMN IF EXISTS total_cost_usd;
