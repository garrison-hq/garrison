-- M4 — relax vault_access_log.agent_instance_id to NULLable.
--
-- M2.3 designed vault_access_log for agent-driven access (the
-- supervisor reads secrets at spawn time on behalf of an agent;
-- agent_instance_id is always known and never null).
--
-- M4 introduces operator-driven vault mutations through the
-- dashboard. Those rows have no agent_instance_id — the operator
-- is the actor, identified via the dashboard better-auth session.
-- Per FR-012 / FR-013, the operator identity lives in the
-- metadata JSONB column (added in T001), keyed as
-- metadata.actor_user_id. This avoids a cross-domain FK from
-- supervisor-owned vault_access_log to dashboard-owned users.
--
-- The plan's "Open question for plan execution" item 4
-- (event_outbox writers) implicitly assumed this kind of
-- schema relaxation would be needed for the parallel audit
-- table; the T001 plan body did not anticipate the NOT NULL
-- constraint specifically. This migration corrects that without
-- changing any other M2.3 invariant — all M2.3 supervisor
-- writes still populate agent_instance_id.
--
-- Threat model Rule 6 is unchanged: every vault access still
-- writes one audit row, the row still records the actor, the row
-- still carries no secret values. M4's actor identity is the
-- operator user uuid; M2.3's actor identity is the agent
-- instance uuid. Both are auditable and triangulate to a real
-- principal.

-- +goose Up
ALTER TABLE vault_access_log
    ALTER COLUMN agent_instance_id DROP NOT NULL;

-- +goose Down
-- Reversing requires every row to have a non-null agent_instance_id;
-- delete operator-actor rows (those have agent_instance_id NULL) before
-- re-applying the NOT NULL constraint, then re-apply.
DELETE FROM vault_access_log WHERE agent_instance_id IS NULL;
ALTER TABLE vault_access_log
    ALTER COLUMN agent_instance_id SET NOT NULL;
