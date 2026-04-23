-- M2.2 migration: MemPalace wiring schema additions, role, trigger, workflow,
-- seed updates. Builds on the M1 + M2.1 schema. Adds:
--   - agent_instances.wake_up_status  (FR-209)
--   - agent_instances.role_slug       (plan §"Data model", hygiene join needs it)
--   - garrison_agent_mempalace role with SELECT-only grants (FR-221)
--   - emit_ticket_transitioned trigger on ticket_transitions (FR-227 infra)
--   - engineering department workflow JSONB updated to 4-column shape (FR-221)
--   - engineer seed row UPDATE: palace_wing + listens_for + agent_md placeholder
--   - qa-engineer seed row INSERT: new role with palace_wing='wing_qa_engineer'
--
-- The garrison_agent_mempalace role is created with LOGIN but no password
-- (same discipline as M2.1's garrison_agent_ro). Operators run
-- `ALTER ROLE garrison_agent_mempalace PASSWORD '...'` post-migrate per
-- docs/ops-checklist.md §"M2.2".
--
-- Session 2026-04-23 clarifications govern the bootstrap strategy (F1:
-- unconditional init) and the flag shape for wake-up (F2: no --max-tokens).
-- Those belong to the supervisor's runtime, not the migration; the migration
-- is purely schema.

-- +goose Up

-- Section 1 — schema changes

-- (1a) Wake-up status capture (FR-209).
ALTER TABLE agent_instances ADD COLUMN wake_up_status TEXT;
-- Permitted values: 'ok', 'failed', 'skipped'. Enforced at the application
-- layer; no CHECK constraint here to keep future M2.2+ values evolvable.

-- (1b) Role slug on agent_instances (plan §"Data model").
-- Needed so the hygiene checker can resolve palace_wing via the (dept, role)
-- key in the agents table. Back-fill all existing M2.1 rows to 'engineer'.
ALTER TABLE agent_instances ADD COLUMN role_slug TEXT NOT NULL DEFAULT 'engineer';

-- Section 2 — Postgres role for the hygiene checker.
CREATE ROLE garrison_agent_mempalace LOGIN;

-- Hygiene checker reads: ticket_transitions (to know what to evaluate),
-- agent_instances (to resolve run window + role_slug), tickets (ticket
-- metadata), agents (to resolve palace_wing via role_slug).
GRANT SELECT ON ticket_transitions, agent_instances, tickets, agents
  TO garrison_agent_mempalace;
-- No INSERT/UPDATE/DELETE grants. Per Clarify 2026-04-23 Q5 resolution
-- (and FR-221), hygiene checker is strictly read-only against Postgres;
-- MemPalace state writes go through the MCP server from agents, not
-- from the hygiene checker.

-- Section 3 — emit_ticket_transitioned trigger (FR-227 supporting infra).
-- Emits pg_notify on channel work.ticket.transitioned.<dept>.<from>.<to>
-- when a ticket_transitions row is INSERTed with a non-null from_column.
-- Silent no-op when from_column IS NULL (test-fixture direct inserts and
-- future flows that don't carry a from state).

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION emit_ticket_transitioned() RETURNS trigger AS $$
DECLARE
  event_id UUID;
  payload JSONB;
  dept_slug TEXT;
  channel TEXT;
BEGIN
  -- from_column is nullable (entry into the board). Skip emit on NULL
  -- so entry-into-first-column doesn't fire a spurious transition notify.
  IF NEW.from_column IS NULL THEN
    RETURN NEW;
  END IF;

  -- Resolve department slug via the tickets table.
  SELECT d.slug INTO dept_slug
  FROM tickets t JOIN departments d ON d.id = t.department_id
  WHERE t.id = NEW.ticket_id;

  IF dept_slug IS NULL THEN
    RAISE EXCEPTION 'emit_ticket_transitioned: ticket % has no department', NEW.ticket_id;
  END IF;

  channel := 'work.ticket.transitioned.' || dept_slug || '.' || NEW.from_column || '.' || NEW.to_column;
  payload := jsonb_build_object(
    'transition_id', NEW.id,
    'ticket_id', NEW.ticket_id,
    'agent_instance_id', NEW.triggered_by_agent_instance_id,
    'from_column', NEW.from_column,
    'to_column', NEW.to_column,
    'at', NEW.at
  );

  INSERT INTO event_outbox (channel, payload)
    VALUES (channel, payload) RETURNING id INTO event_id;
  PERFORM pg_notify(channel, jsonb_build_object('event_id', event_id)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER ticket_transitioned_emit
  AFTER INSERT ON ticket_transitions
  FOR EACH ROW EXECUTE FUNCTION emit_ticket_transitioned();

-- Section 4 — engineering department workflow update.
-- From M2.1's 2-column shape (todo → done) to M2.2's 4-column shape
-- (todo → in_dev → qa_review → done).
UPDATE departments
SET workflow = jsonb_build_object(
  'columns', jsonb_build_array(
    jsonb_build_object('slug','todo',      'label','To do',     'entry_from', jsonb_build_array('backlog')),
    jsonb_build_object('slug','in_dev',    'label','In dev',    'entry_from', jsonb_build_array('todo')),
    jsonb_build_object('slug','qa_review', 'label','QA review', 'entry_from', jsonb_build_array('in_dev')),
    jsonb_build_object('slug','done',      'label','Done',      'entry_from', jsonb_build_array('qa_review'))
  ),
  'transitions', jsonb_build_object(
    'todo',      jsonb_build_array('in_dev'),
    'in_dev',    jsonb_build_array('qa_review'),
    'qa_review', jsonb_build_array('done')
  )
)
WHERE slug = 'engineering';

-- Section 5 — engineer seed UPDATE.
-- palace_wing: newly consumed by the spawn path and the hygiene checker.
-- listens_for: shifts from created.engineering.todo (M2.1) to
--              created.engineering.in_dev (per Session 2026-04-23 clarification;
--              tickets inserted directly at in_dev for M2.2 acceptance).
-- agent_md: placeholder here; T005 will overwrite via +embed-agent-md tooling.
UPDATE agents
SET palace_wing = 'wing_frontend_engineer',
    listens_for = '["work.ticket.created.engineering.in_dev"]'::jsonb,
    agent_md    = 'PLACEHOLDER — seeded by T005 via make seed-engineer-agent'
WHERE role_slug = 'engineer'
  AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');

-- Section 6 — qa-engineer seed INSERT.
-- New role for M2.2. agent_md is a placeholder; T005 overwrites via
-- make seed-qa-engineer-agent. listens_for matches FR-228.
INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
VALUES (
  gen_random_uuid(),
  (SELECT id FROM departments WHERE slug = 'engineering'),
  'qa-engineer',
  'PLACEHOLDER — seeded by T005 via make seed-qa-engineer-agent',
  'claude-haiku-4-5-20251001',
  '[]'::jsonb,
  '[]'::jsonb,
  '["work.ticket.transitioned.engineering.in_dev.qa_review"]'::jsonb,
  'wing_qa_engineer',
  'active'
);

-- +goose Down

-- Reverse in dependency order. Role grants revoked first, then role,
-- then trigger + function, then seed rollback (qa-engineer INSERT, then
-- engineer UPDATE reset), then workflow reset, then column drops.

DROP OWNED BY garrison_agent_mempalace;
DROP ROLE IF EXISTS garrison_agent_mempalace;

DROP TRIGGER IF EXISTS ticket_transitioned_emit ON ticket_transitions;
DROP FUNCTION IF EXISTS emit_ticket_transitioned();

-- Remove the qa-engineer seed row.
DELETE FROM agents
WHERE role_slug = 'qa-engineer'
  AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');

-- Reset the engineer row to M2.1 state as best-effort. agent_md reverts to
-- the M2.1 placeholder; operators reconciling forward/back need to re-run
-- the M2.1 make target to get real content. This matches M2.1's own down-
-- migration stance (best-effort for disaster recovery, not perfect).
UPDATE agents
SET palace_wing = NULL,
    listens_for = '["work.ticket.created.engineering.todo"]'::jsonb,
    agent_md    = 'PLACEHOLDER — seed by T003'
WHERE role_slug = 'engineer'
  AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');

-- Revert workflow to M2.1's 2-column shape.
UPDATE departments
SET workflow = '{"columns":[{"slug":"todo","label":"To do","entry_from":["backlog"]},{"slug":"done","label":"Done","entry_from":["todo"]}],"transitions":{"todo":["done"]}}'::jsonb
WHERE slug = 'engineering';

-- Finally, drop the columns we added.
ALTER TABLE agent_instances DROP COLUMN IF EXISTS role_slug;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS wake_up_status;
