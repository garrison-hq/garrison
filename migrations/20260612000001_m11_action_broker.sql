-- M11 — Action Broker (outbound external actions, gated by policy).
--
-- Lands the full M11 schema in one migration (plan §Data model):
--
-- 1. pending_actions — one immutable row per outbound action request
--    (FR-003, FR-013). Attributes: action_type, target JSONB (e.g. the
--    GitHub issue owner/repo/number for github_issue_comment), the
--    agent-rendered payload, the requesting agent_instance_id, the
--    serving ticket_id (nullable — oneshot agents have no ticket), the
--    policy-assigned tier (never agent-supplied), tier_reason, status,
--    and the operator who approved it (approve-tier only). Anchored to
--    agent_instances.id per the M8 audit posture. Distinct from M1's
--    event_outbox (m11-context §Scope-mismatch #2).
--
--    The permanent-Approve floor is enforced by a CHECK:
--    pending_actions_floor_is_approve — action_type IN ('github_issue_comment')
--    implies tier = 'approve'. The DB-level floor is the third enforcement
--    layer (plan D5c), alongside the Classify floor-first lookup and the
--    TestFloorCannotBeLowered unit test.
--
--    Dispatchable partial index (status pending/approved, tier != human_only)
--    serves the ClaimDispatchablePendingAction FOR UPDATE SKIP LOCKED path.
--
-- 2. pending_action_outcomes — append-only immutable outcome history for
--    every pending_actions row (FR-024). Mirrors the M9
--    scheduled_task_runs → scheduled_tasks shape: the request row is not
--    mutated in place beyond status; outcomes are appended. One row per
--    transition: requested, approved, rejected, executed, failed,
--    notified, done, skipped_human_only. Note: 'classified' is NOT in
--    the CHECK — tier classification is a column on pending_actions, not
--    an outcome row.
--
-- 3. chat_mutation_audit CHECK amendments — verb CHECK gains
--    request_external_action, approve_action, reject_action,
--    mark_action_done; affected_resource_type CHECK gains pending_action.
--    Land in the migration Up (M8 retro lesson: land these on day one,
--    not at first integration-test failure).
--
-- 4. Dashboard grants — GRANT SELECT, UPDATE ON pending_actions TO
--    garrison_dashboard_app (Outbox reads + approve/reject transitions);
--    GRANT SELECT, INSERT ON pending_action_outcomes (read history +
--    write outcomes from Server Actions).
--
-- Migration version: 20260612000001 (today 2026-06-12; advances past
-- M10's 20260612000000_m10_ingress_connectors.sql on the same date;
-- no collision — verified at plan time, M9 gotcha 5 / plan decision 13).

-- +goose Up

-- 1. pending_actions
CREATE TABLE pending_actions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action_type TEXT NOT NULL,
    target JSONB NOT NULL,
    rendered_payload TEXT NOT NULL,
    agent_instance_id UUID NOT NULL REFERENCES agent_instances(id),
    ticket_id UUID NULL REFERENCES tickets(id),
    tier TEXT NOT NULL CHECK (tier IN ('auto', 'notify', 'approve', 'human_only')),
    tier_reason TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending', 'approved', 'rejected', 'executed', 'failed', 'done'
    )),
    approved_by TEXT NULL,
    dispatched_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(rendered_payload) > 0),
    -- Permanent-Approve floor (SC-003, FR-014, plan D5c):
    -- github_issue_comment is public-facing and must always be approve-tier.
    -- An INSERT/UPDATE that sets tier != 'approve' for these action types
    -- fires this CHECK, providing the third enforcement layer below the
    -- Classify floor-first lookup and the TestFloorCannotBeLowered unit test.
    CONSTRAINT pending_actions_floor_is_approve
        CHECK (action_type NOT IN ('github_issue_comment') OR tier = 'approve')
);

-- Dispatchable partial index: ClaimDispatchablePendingAction reads only
-- pending/approved rows that are not human_only. Ordered by created_at
-- for FIFO claim semantics.
CREATE INDEX idx_pending_actions_dispatchable
    ON pending_actions (created_at)
    WHERE status IN ('pending', 'approved') AND tier <> 'human_only';

-- 2. pending_action_outcomes
CREATE TABLE pending_action_outcomes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pending_action_id UUID NOT NULL REFERENCES pending_actions(id),
    agent_instance_id UUID NOT NULL REFERENCES agent_instances(id),
    outcome TEXT NOT NULL CHECK (outcome IN (
        'requested', 'approved', 'rejected', 'executed', 'failed',
        'notified', 'done', 'skipped_human_only'
    )),
    detail TEXT NULL,
    structured_outcome JSONB NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pending_action_outcomes_action
    ON pending_action_outcomes (pending_action_id, created_at);

-- 3. chat_mutation_audit CHECK amendments (M8/M9 pattern: land on day one).
--    The Down section deletes M11-era rows before restoring the pre-M11 CHECKs.

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
    -- M9 verbs (11th verb: create_scheduled_task; four Server Action verbs)
    'create_scheduled_task', 'edit_scheduled_task',
    'pause_scheduled_task', 'resume_scheduled_task',
    'delete_scheduled_task',
    -- M11: request_external_action is the 12th sealed verb (agent-callers only,
    -- ReversibilityClass 3). The three approve/reject/done transitions are
    -- operator Server Action verbs (disjoint from the agent verb set).
    'request_external_action',
    'approve_action', 'reject_action', 'mark_action_done'
  ));

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_affected_resource_type_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_affected_resource_type_check
  CHECK (affected_resource_type IS NULL OR affected_resource_type IN (
    'ticket', 'agent_role', 'hiring_proposal', 'mcp_server',
    -- M9 resource type
    'scheduled_task',
    -- M11 resource type for pending-action verb audit rows
    'pending_action'
  ));

-- 4. Dashboard grants
GRANT SELECT, UPDATE ON pending_actions TO garrison_dashboard_app;
GRANT SELECT, INSERT ON pending_action_outcomes TO garrison_dashboard_app;

-- +goose Down

-- 3. Delete M11-era chat_mutation_audit rows before restoring the pre-M11 CHECKs
--    (M8/M9 Down precedent: clear rows before reverting a CHECK constraint).
DELETE FROM chat_mutation_audit
  WHERE verb IN ('request_external_action', 'approve_action', 'reject_action', 'mark_action_done')
     OR affected_resource_type = 'pending_action';

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_affected_resource_type_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_affected_resource_type_check
  CHECK (affected_resource_type IS NULL OR affected_resource_type IN (
    'ticket', 'agent_role', 'hiring_proposal', 'mcp_server',
    'scheduled_task'
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
    'register_mcp_server',
    'create_scheduled_task', 'edit_scheduled_task',
    'pause_scheduled_task', 'resume_scheduled_task',
    'delete_scheduled_task'
  ));

-- 4. Revoke dashboard grants
REVOKE SELECT, UPDATE ON pending_actions FROM garrison_dashboard_app;
REVOKE SELECT, INSERT ON pending_action_outcomes FROM garrison_dashboard_app;

-- 2. Drop pending_action_outcomes before pending_actions (FK order)
DROP INDEX IF EXISTS idx_pending_action_outcomes_action;
DROP TABLE IF EXISTS pending_action_outcomes;

-- 1. Drop pending_actions
DROP INDEX IF EXISTS idx_pending_actions_dispatchable;
DROP TABLE IF EXISTS pending_actions;
