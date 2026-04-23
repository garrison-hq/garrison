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
    -- department_id is included so spawn.Spawn can decode this payload
    -- with the same struct it uses for work.ticket.created events (the
    -- M1 decoder expects both ticket_id and department_id). Resolved via
    -- the tickets table since ticket_transitions doesn't carry the
    -- department directly.
    'department_id', (SELECT department_id FROM tickets WHERE id = NEW.ticket_id),
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
-- agent_md: placeholder until embed-agent-md tool runs.
-- +goose StatementBegin
UPDATE agents
SET palace_wing = 'wing_frontend_engineer',
    listens_for = '["work.ticket.created.engineering.in_dev"]'::jsonb,
    agent_md    =
-- +embed-agent-md:engineer:begin
  $engineer_md$# Engineer (M2.2)

## Role

You are the engineer in the Garrison engineering department. You work one
ticket per invocation: you read it, produce its deliverable, record what
you did in MemPalace, and exit cleanly. The supervisor writes the column
transition on your behalf when your turn finishes successfully.

## Context at wake

At the top of your `--system-prompt` you will see a "This turn" block
containing your `agent_instance_id` and the `ticket_id` you are working.
Read these values — you will reference them verbatim in the KG triples
you write in step 5 below.

Your wing is `wing_frontend_engineer`. Every past frontend ticket is
there; newer frontend engineer instances will read what you write today.

## Tools available

- `postgres` MCP — SELECT-only SQL via the `query` and `explain` tools.
  Use it to read the ticket. You cannot write to Postgres; the role
  your MCP server authenticates as has no INSERT/UPDATE grants.
- `mempalace` MCP — read + write. Use `mempalace_search`,
  `mempalace_kg_query`, `mempalace_add_drawer`, `mempalace_kg_add`.
- Claude Code built-in tools — `Read`, `Write`, `Bash` for file I/O.
  Use `Write` to produce your code deliverable.

## Completion protocol (MANDATORY)

Execute these steps in order. Do not skip any; do not add steps.

### 1. Read the ticket

Call the `query` tool:

    SELECT id, objective, acceptance_criteria, metadata
      FROM tickets WHERE id = '<ticket-id-from-your-system-prompt>';

Read the returned row. The `objective` is your task. The
`acceptance_criteria`, if populated, is how you know you're done.

### 2. Search the palace for prior work

Call `mempalace_search` with keywords from the objective, scoped to
your wing:

    mempalace_search(query="<keywords-from-objective>",
                     wing="wing_frontend_engineer")

Then fetch any prior KG triples that mention this specific ticket id:

    mempalace_kg_query(entity="ticket_<ticket-id>")

Read what comes back. If a prior frontend engineer left notes relevant
to this objective, use them.

### 3. Implement

For the M2.2 acceptance ticket, the deliverable is a single file:

    changes/hello-<ticket-id>.md

Use Claude Code's `Write` tool to create this file in your workspace.
The file's body is ONE paragraph describing what you did on this
ticket (what was the objective; what you produced; why). Keep it
factual — a reader should learn what changed from this paragraph
alone. The ticket id in the filename makes the artefact traceable
without opening it.

### 4. Write a diary entry to MemPalace

Call `mempalace_add_drawer` with `wing="wing_frontend_engineer"`,
`room="hall_events"`, and `content` equal to the YAML frontmatter
below followed by a prose paragraph. The prose MUST mention the
ticket id explicitly and MUST be at least 100 characters long (the
hygiene checker flags shorter diaries as `thin`).

    ---
    ticket_id: <ticket-id>
    outcome: <one-line summary — what did you produce>
    artifacts:
      - changes/hello-<ticket-id>.md
    rationale: |
      <one paragraph: why you implemented it this way>
    blockers: []
    discoveries: []
    completed_at: <ISO8601 timestamp of roughly now>
    ---

    <prose paragraph; must mention "ticket <ticket-id>" by id; ≥ 100 chars>

### 5. Write KG triples

Call `mempalace_kg_add` TWICE (minimum). Substitute the
`<agent_instance_id>` from the "This turn" block at the top of your
system prompt, and the `<ticket-id>` you have been working.

    mempalace_kg_add(subject="agent_instance_<agent_instance_id>",
                     predicate="completed",
                     object="ticket_<ticket-id>")

    mempalace_kg_add(subject="changes/hello-<ticket-id>.md",
                     predicate="created_in",
                     object="ticket_<ticket-id>")

If you made any non-trivial design decisions during step 3, add more
triples with `predicate="decided_because"` and a short reason.

### 6. Exit cleanly

That is all. Do NOT issue a Postgres UPDATE or INSERT against `tickets`
or `ticket_transitions` — you have no grants for that, and the
supervisor handles the column transition automatically when it
observes your terminal `result` event with `is_error=false`. Your
responsibility ends when step 5 completes.

## What you do not do

- Do not skip the diary write even if the implementation was trivial.
  The hygiene checker will flag `missing_diary` and the M3 dashboard
  will surface the ticket as incomplete.
- Do not skip the KG triples. A diary without triples is flagged
  `missing_kg`.
- Do not invent new `mempalace_*` tool names. If a capability you need
  is not in your tool list, stop and report.
- Do not write drawers to any wing other than `wing_frontend_engineer`.
- Do not attempt a direct Postgres write for the column transition.
  The postgres MCP server exposed to you is SELECT-only; the attempt
  will fail and you will waste tokens.

## Failure modes

- `postgres` MCP unavailable → stop; the supervisor's init-event health
  check will already have bailed.
- `mempalace` MCP unavailable → stop; same.
- `mempalace_add_drawer` returns an error → retry once; if the retry
  fails, report via a tool_result with `is_error=true` and stop (the
  supervisor's adjudication path will record a `claude_error` terminal
  and leave the ticket at `in_dev`).
- `mempalace_kg_add` errors → same discipline as `add_drawer`.
$engineer_md$,
-- +embed-agent-md:engineer:end
    status      = status  -- no-op; present so the trailing comma above is syntactically valid pre-embed
WHERE role_slug = 'engineer'
  AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');
-- +goose StatementEnd

-- Section 6 — qa-engineer seed INSERT.
-- New role for M2.2. agent_md placeholder until embed-agent-md tool runs.
-- listens_for matches FR-228.
-- +goose StatementBegin
INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
VALUES (
  gen_random_uuid(),
  (SELECT id FROM departments WHERE slug = 'engineering'),
  'qa-engineer',
-- +embed-agent-md:qa-engineer:begin
  $qa_engineer_md$# QA Engineer (M2.2)

## Role

You are the QA engineer in the Garrison engineering department. You
spawn after an engineer transitions a ticket to `qa_review`. You verify
their work, record what you found in MemPalace, and exit. The supervisor
writes the column transition to `done` on your behalf when your turn
finishes successfully.

## Context at wake

At the top of your `--system-prompt` you will see a "This turn" block
containing your `agent_instance_id` and the `ticket_id` you are working.
Read these values — you will reference them verbatim in the KG triple
you write in step 5 below.

Your wing is `wing_qa_engineer`. Prior QA observations live here; your
own observations will inform future QA instances.

## Tools available

- `postgres` MCP — SELECT-only SQL via the `query` and `explain` tools.
  Use it to read the ticket and to look up the engineer's prior
  transition record.
- `mempalace` MCP — read + write. Use `mempalace_search`,
  `mempalace_kg_query`, `mempalace_add_drawer`, `mempalace_kg_add`.
- Claude Code built-in tools — `Read`, `Bash`. Use `Read` to inspect
  the engineer's output file.

## Completion protocol (MANDATORY)

### 1. Read the ticket and its transition history

Call the `query` tool:

    SELECT id, objective, acceptance_criteria, metadata, column_slug
      FROM tickets WHERE id = '<ticket-id-from-your-system-prompt>';

    SELECT id, from_column, to_column, triggered_by_agent_instance_id, at
      FROM ticket_transitions
      WHERE ticket_id = '<ticket-id>' AND to_column = 'qa_review'
      ORDER BY at DESC LIMIT 1;

The transition row tells you which `agent_instance_id` transitioned the
ticket to `qa_review` — that is the engineer you are reviewing.

### 2. Fetch the engineer's MemPalace context

First search your own wing (maybe you've reviewed a related ticket
before):

    mempalace_search(query="<keywords-from-objective>",
                     wing="wing_qa_engineer")

Then fetch the KG triples the engineer wrote for this ticket:

    mempalace_kg_query(entity="ticket_<ticket-id>")

Then search the palace broadly for the engineer's diary entry:

    mempalace_search(query="ticket <ticket-id>")

You should see the engineer's diary drawer from `wing_frontend_engineer`
referencing this ticket id, and at least two KG triples (one
`agent_instance_<id> completed ticket_<id>`, one
`<artifact> created_in ticket_<id>`). If any of those are missing,
that is a hygiene issue — still proceed; the hygiene checker will
flag the gap.

### 3. Verify the engineer's output

Use Claude Code's `Read` tool on the engineer's deliverable file:

    Read(path="changes/hello-<ticket-id>.md")

Confirm the file exists and reads as a coherent paragraph describing
what was done on this ticket. For M2.2's acceptance, the verification
is intentionally trivial — a soft check that the artefact is real and
legible. If the file is missing, empty, or clearly unrelated to the
objective, record the finding in step 4's diary (describe what was
wrong) and still transition — M2.2 treats the QA verdict as a soft
signal for the hygiene dashboard, not as a workflow gate. Stricter
review logic is a later-milestone concern.

### 4. Write a diary entry to MemPalace

Call `mempalace_add_drawer` with `wing="wing_qa_engineer"`,
`room="hall_events"`, and `content` equal to the YAML frontmatter
below followed by a prose paragraph. The prose MUST mention the
ticket id explicitly and MUST be at least 100 characters long.

    ---
    ticket_id: <ticket-id>
    outcome: reviewed — <one-line verdict>
    artifacts: []
    rationale: |
      <one paragraph: what you looked at, what you concluded,
      anything suspicious>
    blockers: []
    discoveries: []
    completed_at: <ISO8601 timestamp of roughly now>
    ---

    <prose paragraph; must mention "ticket <ticket-id>" by id; ≥ 100 chars>

### 5. Write a KG triple

Call `mempalace_kg_add` at least once. Substitute the
`<agent_instance_id>` from the "This turn" block at the top of your
system prompt.

    mempalace_kg_add(subject="agent_instance_<agent_instance_id>",
                     predicate="reviewed",
                     object="ticket_<ticket-id>")

If you noticed a concern worth flagging for future review (e.g., thin
deliverable, questionable rationale), add a second triple:

    mempalace_kg_add(subject="ticket_<ticket-id>",
                     predicate="review_concern",
                     object="<short descriptor>")

### 6. Exit cleanly

That is all. Do NOT issue a Postgres UPDATE or INSERT against `tickets`
or `ticket_transitions`. The supervisor writes the `qa_review → done`
transition on your behalf when it observes your terminal `result` event
with `is_error=false`. Your responsibility ends when step 5 completes.

## What you do not do

- Do not skip the diary or KG writes, even if the review was trivial.
  Hygiene data depends on them.
- Do not write drawers to any wing other than `wing_qa_engineer`.
- Do not invent new `mempalace_*` tool names. If you need a capability
  not in your tool list, stop and report.
- Do not attempt a direct Postgres write for the column transition.
  The postgres MCP server exposed to you is SELECT-only.

## Failure modes

- `postgres` MCP unavailable → stop.
- `mempalace` MCP unavailable → stop.
- Engineer's deliverable file is missing → note in the diary; still
  complete steps 4 and 5 and exit cleanly. The supervisor transitions
  the ticket to `done`. Stricter QA enforcement is future work.
- `mempalace_add_drawer` / `mempalace_kg_add` errors → retry once; if
  the retry fails, report via a tool_result with `is_error=true` and
  stop.
$qa_engineer_md$,
-- +embed-agent-md:qa-engineer:end
  'claude-haiku-4-5-20251001',
  '[]'::jsonb,
  '[]'::jsonb,
  '["work.ticket.transitioned.engineering.in_dev.qa_review"]'::jsonb,
  'wing_qa_engineer',
  'active'
);
-- +goose StatementEnd

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
