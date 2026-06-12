-- M9 — Scheduled / triggered wake-ups (heartbeat).
--
-- Lands the full M9 schema in one migration (plan §Data model):
--
-- 1. scheduled_tasks — concrete named recurring jobs (FR-100). Identity
--    (name, department, role), schedule expression (grammar validated
--    Go-side, FR-103), computed next_fire_at, firing mode, templated
--    objective + acceptance criteria (non-empty CHECKed — "wake up and
--    look for work" rows are structurally impossible), paused state.
--    Deletion is a SOFT delete (deleted_at; FR-502 — run history is
--    immutable, no cascade); name uniqueness holds among live rows only
--    via a partial unique index, so a deleted task's name is reusable.
--
-- 2. scheduled_task_runs — one row per firing attempt (FR-108) with a
--    typed outcome (fired | skipped_overlap | gate_deferred | failed).
--    No ON DELETE CASCADE anywhere: tasks soft-delete, runs survive.
--    structured_outcome JSONB carries the finalize_oneshot payload
--    commit; oneshot completion reads through the joined
--    agent_instances.status (plan decision 5).
--
-- 3. agent_instances origin reshape — ticket_id goes nullable and
--    scheduled_task_run_id arrives so oneshot spawns anchor to their
--    run record instead of a ticket. The exactly-one-origin CHECK keeps
--    every instance anchored to precisely one of the two. All pre-M9
--    rows have ticket_id NOT NULL, so they satisfy the new CHECK with
--    scheduled_task_run_id NULL.
--
-- 4. chat_mutation_audit CHECK extensions, up front per the M8 retro
--    (the affected_resource_type / outcome gaps were found at first
--    integration-test run last time): five M9 verbs, the
--    scheduled_task resource type, and the per-turn creation-ceiling
--    outcome.
--
-- 5. Dashboard grants — CRUD on scheduled_tasks (dashboard CRUD is
--    drizzle-direct per plan decision 11); SELECT only on
--    scheduled_task_runs (runs are supervisor-written only).
--
-- Version note: the plan names this file 20260610000000_m9_scheduled_
-- wakeups.sql, but that version number was already taken by the M7.1-era
-- agents_changed_trigger migration (goose rejects duplicate versions),
-- so this file carries the next free version on the same date.

-- +goose Up

-- 1. scheduled_tasks
CREATE TABLE scheduled_tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,                              -- operator-facing identity
    department_id UUID NOT NULL REFERENCES departments(id),
    role_slug TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('ticket','oneshot')),
    schedule_expr TEXT NOT NULL,                     -- grammar validated Go-side
    next_fire_at TIMESTAMPTZ NOT NULL,
    objective_template TEXT NOT NULL,                -- non-empty enforced Go-side + CHECK length > 0
    acceptance_criteria_template TEXT NOT NULL,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ NULL,                     -- soft delete (FR-502: run history survives)
    last_fired_at TIMESTAMPTZ NULL,                  -- set ONLY on outcome='fired' (FR-107 semantics)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(objective_template) > 0),
    CHECK (length(acceptance_criteria_template) > 0)
);

-- name uniqueness only among live tasks, so a deleted task's name is reusable
CREATE UNIQUE INDEX idx_scheduled_tasks_name_live ON scheduled_tasks (name) WHERE deleted_at IS NULL;
CREATE INDEX idx_scheduled_tasks_due ON scheduled_tasks (next_fire_at) WHERE NOT paused AND deleted_at IS NULL;

-- 2. scheduled_task_runs
CREATE TABLE scheduled_task_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- no ON DELETE CASCADE: tasks soft-delete, runs are immutable history
    scheduled_task_id UUID NOT NULL REFERENCES scheduled_tasks(id),
    slot_at TIMESTAMPTZ NOT NULL,                    -- the slot this run answers for
    fired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    outcome TEXT NOT NULL CHECK (outcome IN ('fired','skipped_overlap','gate_deferred','failed')),
    detail TEXT NULL,                                -- human-readable reason for non-fired outcomes
    ticket_id UUID NULL REFERENCES tickets(id),      -- ticket-mode firings
    agent_instance_id UUID NULL REFERENCES agent_instances(id),  -- oneshot firings (backfilled at spawn)
    structured_outcome JSONB NULL                    -- finalize_oneshot payload commit
);

CREATE INDEX idx_scheduled_task_runs_task ON scheduled_task_runs (scheduled_task_id, fired_at DESC);

-- 3. agent_instances — origin reshape (exactly one of ticket / run)
ALTER TABLE agent_instances ALTER COLUMN ticket_id DROP NOT NULL;
ALTER TABLE agent_instances ADD COLUMN scheduled_task_run_id UUID NULL REFERENCES scheduled_task_runs(id);
ALTER TABLE agent_instances ADD CONSTRAINT agent_instances_exactly_one_origin
    CHECK ((ticket_id IS NOT NULL)::int + (scheduled_task_run_id IS NOT NULL)::int = 1);

-- 4. chat_mutation_audit — CHECK extensions (M8-retro lesson: land these
--    in the migration on day one, not at first integration-test failure)
ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_verb_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_verb_check
  CHECK (verb IN (
    -- M5.3 chat verbs
    'create_ticket', 'edit_ticket', 'transition_ticket',
    'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
    'propose_hire',
    -- M7 chat verbs + Server Action verbs
    'propose_skill_change', 'bump_skill_version',
    'approve_hire', 'reject_hire',
    'approve_skill_change', 'reject_skill_change',
    'approve_version_bump', 'reject_version_bump',
    'update_agent_md',
    'grandfathered_at_m7',
    -- M8 Server Action verb
    'register_mcp_server',
    -- M9: create_scheduled_task is the 11th chat verb (Tier 3); the
    -- other four are dashboard Server Action verbs (FR-501, disjoint
    -- from the chat verb set per the M8 precedent).
    'create_scheduled_task', 'edit_scheduled_task',
    'pause_scheduled_task', 'resume_scheduled_task',
    'delete_scheduled_task'
  ));

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_outcome_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_outcome_check
  CHECK (outcome IN (
    'success', 'validation_failed', 'leak_scan_failed', 'ticket_state_changed',
    'concurrency_cap_full', 'invalid_transition', 'resource_not_found',
    'tool_call_ceiling_reached',
    -- M8 runaway-control outcome.
    'dept_weekly_ticket_budget_exceeded',
    -- M8 register_mcp_server worker outcome.
    'failed',
    -- M9 per-turn scheduled-task creation ceiling (FR-602; mirrors the
    -- M6 tool-call ceiling mechanism).
    'scheduled_task_creation_ceiling_reached'
  ));

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_affected_resource_type_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_affected_resource_type_check
  CHECK (affected_resource_type IS NULL OR affected_resource_type IN (
    'ticket', 'agent_role', 'hiring_proposal', 'mcp_server',
    -- M9 resource type for the five scheduled-task verbs.
    'scheduled_task'
  ));

-- 5. Dashboard grants. CRUD on scheduled_tasks is drizzle-direct (plan
--    decision 11); scheduled_task_runs rows are supervisor-written only,
--    so the dashboard reads run history but never writes it.
GRANT SELECT, INSERT, UPDATE, DELETE ON scheduled_tasks TO garrison_dashboard_app;
GRANT SELECT ON scheduled_task_runs TO garrison_dashboard_app;

-- +goose Down

-- chat_mutation_audit — delete M9-era rows before reverting the CHECKs
DELETE FROM chat_mutation_audit
  WHERE verb IN (
    'create_scheduled_task', 'edit_scheduled_task',
    'pause_scheduled_task', 'resume_scheduled_task',
    'delete_scheduled_task'
  )
     OR outcome = 'scheduled_task_creation_ceiling_reached'
     OR affected_resource_type = 'scheduled_task';

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_affected_resource_type_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_affected_resource_type_check
  CHECK (affected_resource_type IS NULL OR affected_resource_type IN (
    'ticket', 'agent_role', 'hiring_proposal', 'mcp_server'
  ));

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_outcome_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_outcome_check
  CHECK (outcome IN (
    'success', 'validation_failed', 'leak_scan_failed', 'ticket_state_changed',
    'concurrency_cap_full', 'invalid_transition', 'resource_not_found',
    'tool_call_ceiling_reached',
    'dept_weekly_ticket_budget_exceeded',
    'failed'
  ));

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_verb_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_verb_check
  CHECK (verb IN (
    'create_ticket', 'edit_ticket', 'transition_ticket',
    'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
    'propose_hire',
    'propose_skill_change', 'bump_skill_version',
    'approve_hire', 'reject_hire',
    'approve_skill_change', 'reject_skill_change',
    'approve_version_bump', 'reject_version_bump',
    'update_agent_md',
    'grandfathered_at_m7',
    'register_mcp_server'
  ));

-- Grants
REVOKE SELECT ON scheduled_task_runs FROM garrison_dashboard_app;
REVOKE SELECT, INSERT, UPDATE, DELETE ON scheduled_tasks FROM garrison_dashboard_app;

-- agent_instances — the CHECK drop precedes re-adding NOT NULL. The
-- SET NOT NULL fails BY DESIGN if oneshot rows (ticket_id NULL) exist —
-- same posture as M8's agent_role_secrets PK reshape: rolling back past
-- M9 with oneshot history in place requires operator surgery first.
ALTER TABLE agent_instances DROP CONSTRAINT IF EXISTS agent_instances_exactly_one_origin;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS scheduled_task_run_id;
ALTER TABLE agent_instances ALTER COLUMN ticket_id SET NOT NULL;

-- scheduled_task_runs (drop before scheduled_tasks — FK)
DROP INDEX IF EXISTS idx_scheduled_task_runs_task;
DROP TABLE IF EXISTS scheduled_task_runs;

-- scheduled_tasks
DROP INDEX IF EXISTS idx_scheduled_tasks_due;
DROP INDEX IF EXISTS idx_scheduled_tasks_name_live;
DROP TABLE IF EXISTS scheduled_tasks;
