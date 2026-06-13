-- M11 action-broker queries (plan §sqlc).
-- Used by:
--   - supervisor/internal/garrisonmutate/verbs_actions.go (request_external_action verb:
--     InsertPendingAction + InsertPendingActionOutcome in one transaction).
--   - supervisor/internal/actionbroker/dispatcher.go (dispatcher worker: claim,
--     mark executed/failed, outcome writes).
--   - supervisor/internal/actionbroker/... (GetPendingActionByID for re-read;
--     ListPendingApproveActions + ListPendingActionOutcomes for dashboard API).
--
-- All parameters use sqlc.arg(name) exclusively (M7 retro gotcha).

-- name: InsertPendingAction :one
-- Writes the immutable pending-action row at verb call time (FR-003).
-- The tier is policy-table-assigned by the caller (never agent-supplied);
-- the DB-level pending_actions_floor_is_approve CHECK provides the third
-- enforcement layer (plan D5c). Returns id and tier so the verb handler
-- can include them in the Result message (FR-004).
INSERT INTO pending_actions (
    action_type,
    target,
    rendered_payload,
    agent_instance_id,
    ticket_id,
    tier,
    tier_reason
) VALUES (
    sqlc.arg(action_type)::text,
    sqlc.arg(target)::jsonb,
    sqlc.arg(rendered_payload)::text,
    sqlc.arg(agent_instance_id)::uuid,
    sqlc.arg(ticket_id)::uuid,
    sqlc.arg(tier)::text,
    sqlc.arg(tier_reason)::text
)
RETURNING id, tier;

-- name: InsertPendingActionOutcome :exec
-- Appends one immutable outcome row to the action's history (FR-024).
-- Mirrors M9's scheduled_task_runs append shape: the request row is not
-- mutated in place beyond status transitions; outcomes are appended here.
INSERT INTO pending_action_outcomes (
    pending_action_id,
    agent_instance_id,
    outcome,
    detail,
    structured_outcome
) VALUES (
    sqlc.arg(pending_action_id)::uuid,
    sqlc.arg(agent_instance_id)::uuid,
    sqlc.arg(outcome)::text,
    sqlc.arg(detail)::text,
    sqlc.arg(structured_outcome)::jsonb
);

-- name: ClaimDispatchablePendingAction :one
-- Dispatcher claim (FR-021). FOR UPDATE SKIP LOCKED gives each dispatchable
-- row at most one claimant regardless of concurrent dispatcher instances.
-- The partial index idx_pending_actions_dispatchable serves exactly this
-- predicate (status IN ('pending','approved') AND tier <> 'human_only').
-- LIMIT 1 bounds the claim to one row per Handle call.
-- human_only rows are structurally excluded by the index predicate so they
-- are never claimed through the normal LISTEN path.
SELECT id, action_type, target, rendered_payload, agent_instance_id, ticket_id,
       tier, tier_reason, status, approved_by, dispatched_at, created_at
  FROM pending_actions
 WHERE status IN ('pending', 'approved')
   AND tier <> 'human_only'
 ORDER BY created_at
   FOR UPDATE SKIP LOCKED
 LIMIT 1;

-- name: MarkPendingActionExecuted :exec
-- Terminal success transition: status → executed, dispatched_at set (FR-021,
-- FR-024). Called after the external provider POST returns 2xx. Together with
-- the FOR UPDATE SKIP LOCKED claim this provides the exactly-once guarantee —
-- a re-claimed row in 'executed' status is a no-op (terminal state).
UPDATE pending_actions
   SET status       = 'executed',
       dispatched_at = sqlc.arg(dispatched_at)::timestamptz
 WHERE id = sqlc.arg(id)::uuid;

-- name: MarkPendingActionFailed :exec
-- Terminal failure transition: status → failed (FR-022). Called when the
-- external provider returns a non-recoverable error or vault is unavailable
-- (FR-023). No auto-retry in M11 alpha; the row surfaces in the Outbox for
-- operator-initiated re-request.
UPDATE pending_actions
   SET status = 'failed'
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetPendingActionByID :one
-- Single-row re-read for the dispatcher's Handle implementation: after
-- claiming a row via FOR UPDATE SKIP LOCKED, the dispatcher re-reads it
-- to confirm the status is still in the dispatchable set (a row may
-- transition between the LISTEN notify and the claim — the restart-mid-
-- dispatch guard SC-006).
SELECT id, action_type, target, rendered_payload, agent_instance_id, ticket_id,
       tier, tier_reason, status, approved_by, dispatched_at, created_at
  FROM pending_actions
 WHERE id = sqlc.arg(id)::uuid;

-- name: ListPendingApproveActions :many
-- Outbox approval-queue read (FR-025): approve-tier rows still at
-- status='pending' (awaiting the operator's approve/reject click).
-- Ordered oldest-first so the operator sees the queue in arrival order.
SELECT id, action_type, target, rendered_payload, agent_instance_id, ticket_id,
       tier, tier_reason, status, approved_by, dispatched_at, created_at
  FROM pending_actions
 WHERE tier = 'approve'
   AND status = 'pending'
 ORDER BY created_at;

-- name: ListPendingActionOutcomes :many
-- Full outcome history for a pending_actions row (FR-007, SC-007).
-- Used to reconstruct the audit trail: requested → approved/rejected →
-- executed/failed/done/skipped_human_only. Ordered by created_at so the
-- timeline is in arrival order.
SELECT id, pending_action_id, agent_instance_id, outcome, detail,
       structured_outcome, created_at
  FROM pending_action_outcomes
 WHERE pending_action_id = sqlc.arg(pending_action_id)::uuid
 ORDER BY created_at;
