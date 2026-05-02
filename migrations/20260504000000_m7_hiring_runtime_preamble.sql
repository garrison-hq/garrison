-- M7 — First custom agent end-to-end (hiring + per-agent runtime + immutable preamble).
--
-- Schema additions across the three M7 threads. Every change is additive;
-- no destructive drops on existing data, no row updates required.
-- See specs/017-m7-hiring-runtime-preamble/ {spec,plan,tasks}.md.
--
-- Schema deviation from spec FR-214a (documented here, not silently resolved):
-- the spec text says install steps land in chat_mutation_audit with
-- kind='install_step:<step>'. The existing chat_mutation_audit table uses
-- a `verb` column (not `kind`), and its semantic surface is "operator-
-- authorized chat mutation." Install-step events are supervisor-driven
-- audit rows, not chat mutations. Forcing them into chat_mutation_audit
-- requires either bending `verb` to non-verb identifiers OR adding a
-- second-purpose column. Both are uglier than a separate journal table.
-- M7 introduces `agent_install_journal` as the install-step audit
-- surface; the supervisor crash-recovery query (skillinstall.recover)
-- reads from this table instead. Spec FR-214a's intent (per-step audit
-- + recovery) is preserved; only the table location changes.
--
-- Extensions in this migration:
--
-- 1. agents — six additive columns supporting the per-agent container
--    runtime and the hiring lifecycle:
--      image_digest TEXT NULL — RepoDigest of the per-agent container
--        image at activation; FR-213, FR-304, decision #22.
--      runtime_caps JSONB NULL — per-role overrides for memory/cpus/
--        pids-limit; FR-207, decision #31.
--      egress_grant_jsonb JSONB NULL — operator-approved per-agent
--        egress grants (extra networks the per-agent container joins
--        beyond the default sidecars); FR-402, decision #20.
--      mcp_servers_jsonb JSONB NULL — operator-approved MCP server
--        list at hire time; FR-403, decision #16.
--      last_grandfathered_at TIMESTAMPTZ NULL — populated by migrate7
--        for M2.x-seeded agents at the M7 cutover; FR-112, decision #6.
--      host_uid INT NULL — sequentially-allocated UID within the
--        per-customer range; FR-206a / decision #30.
--    Plus an index on host_uid (partial; only non-NULL rows) to speed
--    the MAX(host_uid)+1 allocator.
--
-- 2. hiring_proposals — extend the M5.3 stopgap table with the columns
--    M7 needs to carry the full proposal lifecycle (FR-101):
--      target_agent_id UUID NULL — FK to agents.id, populated for
--        skill-change and version-bump proposals (NULL for new-agent
--        proposals); HR-1, FR-101.
--      proposal_type TEXT — CHECK in {new_agent, skill_change,
--        version_bump}; default 'new_agent' so existing rows stay
--        valid; FR-101.
--      skill_diff_jsonb JSONB NULL — for skill-change proposals,
--        carries the {add[], remove[], bump[]} diff; FR-101.
--      proposal_snapshot_jsonb JSONB NULL — full proposal content
--        captured at propose time; HR-9. NULL on the M5.3-era rows;
--        T009 propose-handler populates for new rows.
--      skill_digest_at_propose TEXT NULL — propose-time SHA-256;
--        HR-7, FR-106.
--      approved_at TIMESTAMPTZ NULL, approved_by UUID NULL — populated
--        on approval; HR-2.
--      rejected_at TIMESTAMPTZ NULL, rejected_reason TEXT NULL —
--        populated on rejection (incl. supersession via FR-110a);
--        HR-3.
--    Existing status CHECK (M5.3 included 'superseded' as a value;
--    FR-110a's supersession write reuses it). New status values
--    install_in_progress, installed, install_failed are added by
--    extending the CHECK constraint.
--
-- 3. agent_container_events — new audit table for the per-agent
--    container lifecycle (FR-211, AS-10). Append-only; rows tagged
--    by kind for fan-out and reconciliation. retention_class column
--    is FR-404's schema-only stub — no enforcement at M7.
--
-- 4. agent_install_journal — new audit table for install pipeline
--    steps (FR-214a, deviation noted at top of file). Each material
--    install step writes a row; supervisor crash recovery
--    (skillinstall.Resume) reads the latest row per proposal_id.
--    Append-only; no UPDATE path.
--
-- 5. agent_instances — four additive columns supporting forensic
--    reconstruction of every spawn (FR-213, FR-303–FR-305, AS-10):
--      image_digest TEXT NOT NULL DEFAULT '' — populated at spawn
--        from the persistent container's recorded digest.
--      preamble_hash TEXT NOT NULL DEFAULT '' — populated from
--        agentpolicy.Hash() at spawn.
--      claude_md_hash TEXT NULL — populated at spawn from cwd
--        CLAUDE.md hash; NULL when claude is invoked with --bare.
--      originating_audit_id UUID NULL — FK to chat_mutation_audit.id;
--        populated for hired agents linking the spawn to the
--        approval that authorised the agent.
--
-- 6. chat_mutation_audit — extend verb CHECK to include the M7 verb
--    additions; add retention_class column (FR-404). The CHECK list
--    grows to 16 values; behaviour-preserving for M5.3 callers.

-- +goose Up

-- 1. agents extensions
ALTER TABLE agents
  ADD COLUMN image_digest TEXT NULL,
  ADD COLUMN runtime_caps JSONB NULL,
  ADD COLUMN egress_grant_jsonb JSONB NULL,
  ADD COLUMN mcp_servers_jsonb JSONB NULL,
  ADD COLUMN last_grandfathered_at TIMESTAMPTZ NULL,
  ADD COLUMN host_uid INT NULL;

CREATE INDEX idx_agents_host_uid
  ON agents (host_uid)
  WHERE host_uid IS NOT NULL;

-- 2. hiring_proposals extensions
ALTER TABLE hiring_proposals
  ADD COLUMN target_agent_id UUID NULL REFERENCES agents(id),
  ADD COLUMN proposal_type TEXT NOT NULL DEFAULT 'new_agent'
    CHECK (proposal_type IN ('new_agent', 'skill_change', 'version_bump')),
  ADD COLUMN skill_diff_jsonb JSONB NULL,
  ADD COLUMN proposal_snapshot_jsonb JSONB NULL,
  ADD COLUMN skill_digest_at_propose TEXT NULL,
  ADD COLUMN approved_at TIMESTAMPTZ NULL,
  ADD COLUMN approved_by UUID NULL,
  ADD COLUMN rejected_at TIMESTAMPTZ NULL,
  ADD COLUMN rejected_reason TEXT NULL;

-- Extend the status CHECK constraint with install lifecycle states.
-- M5.3 shipped: ('pending', 'approved', 'rejected', 'superseded').
-- M7 adds: install_in_progress, installed, install_failed.
ALTER TABLE hiring_proposals DROP CONSTRAINT IF EXISTS hiring_proposals_status_check;
ALTER TABLE hiring_proposals
  ADD CONSTRAINT hiring_proposals_status_check
  CHECK (status IN (
    'pending', 'approved', 'rejected', 'superseded',
    'install_in_progress', 'installed', 'install_failed'
  ));

CREATE INDEX idx_hp_target_agent_pending
  ON hiring_proposals (target_agent_id, proposal_type)
  WHERE status = 'pending' AND target_agent_id IS NOT NULL;

-- 3. agent_container_events
CREATE TABLE agent_container_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN (
    'created', 'started', 'stopped', 'removed', 'migrated',
    'oom_killed', 'crashed',
    'image_digest_drift_detected', 'reconciled_on_supervisor_restart'
  )),
  image_digest TEXT,
  started_at TIMESTAMPTZ,
  stopped_at TIMESTAMPTZ,
  stop_reason TEXT,
  cgroup_caps_jsonb JSONB,
  retention_class TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_container_events_agent_created
  ON agent_container_events (agent_id, created_at DESC);

GRANT SELECT ON agent_container_events TO garrison_dashboard_app;

-- 4. agent_install_journal
CREATE TABLE agent_install_journal (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  proposal_id UUID NOT NULL REFERENCES hiring_proposals(id) ON DELETE CASCADE,
  step TEXT NOT NULL CHECK (step IN (
    'download', 'verify_digest', 'extract', 'mount',
    'container_create', 'container_start'
  )),
  outcome TEXT NOT NULL CHECK (outcome IN (
    'success', 'failed', 'interrupted'
  )),
  error_kind TEXT,
  payload_jsonb JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_agent_install_journal_proposal_created
  ON agent_install_journal (proposal_id, created_at DESC);

GRANT SELECT ON agent_install_journal TO garrison_dashboard_app;

-- 5. agent_instances extensions
ALTER TABLE agent_instances
  ADD COLUMN image_digest TEXT NOT NULL DEFAULT '',
  ADD COLUMN preamble_hash TEXT NOT NULL DEFAULT '',
  ADD COLUMN claude_md_hash TEXT NULL,
  ADD COLUMN originating_audit_id UUID NULL REFERENCES chat_mutation_audit(id) ON DELETE SET NULL;

-- 6. chat_mutation_audit extensions
ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_verb_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_verb_check
  CHECK (verb IN (
    -- M5.3 chat verbs (preserved)
    'create_ticket', 'edit_ticket', 'transition_ticket',
    'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
    'propose_hire',
    -- M7 chat verbs
    'propose_skill_change', 'bump_skill_version',
    -- M7 Server-Action-driven audit events
    'approve_hire', 'reject_hire',
    'approve_skill_change', 'reject_skill_change',
    'approve_version_bump', 'reject_version_bump',
    'update_agent_md',
    'grandfathered_at_m7'
  ));

ALTER TABLE chat_mutation_audit
  ADD COLUMN retention_class TEXT NULL;

-- +goose Down

-- chat_mutation_audit
ALTER TABLE chat_mutation_audit DROP COLUMN IF EXISTS retention_class;
ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_verb_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_verb_check
  CHECK (verb IN (
    'create_ticket', 'edit_ticket', 'transition_ticket',
    'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
    'propose_hire'
  ));

-- agent_instances
ALTER TABLE agent_instances DROP COLUMN IF EXISTS originating_audit_id;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS claude_md_hash;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS preamble_hash;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS image_digest;

-- agent_install_journal
REVOKE SELECT ON agent_install_journal FROM garrison_dashboard_app;
DROP INDEX IF EXISTS idx_agent_install_journal_proposal_created;
DROP TABLE IF EXISTS agent_install_journal;

-- agent_container_events
REVOKE SELECT ON agent_container_events FROM garrison_dashboard_app;
DROP INDEX IF EXISTS idx_agent_container_events_agent_created;
DROP TABLE IF EXISTS agent_container_events;

-- hiring_proposals
DROP INDEX IF EXISTS idx_hp_target_agent_pending;
ALTER TABLE hiring_proposals DROP CONSTRAINT IF EXISTS hiring_proposals_status_check;
ALTER TABLE hiring_proposals
  ADD CONSTRAINT hiring_proposals_status_check
  CHECK (status IN ('pending', 'approved', 'rejected', 'superseded'));
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS rejected_reason;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS rejected_at;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS approved_by;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS approved_at;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS skill_digest_at_propose;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS proposal_snapshot_jsonb;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS skill_diff_jsonb;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS proposal_type;
ALTER TABLE hiring_proposals DROP COLUMN IF EXISTS target_agent_id;

-- agents
DROP INDEX IF EXISTS idx_agents_host_uid;
ALTER TABLE agents DROP COLUMN IF EXISTS host_uid;
ALTER TABLE agents DROP COLUMN IF EXISTS last_grandfathered_at;
ALTER TABLE agents DROP COLUMN IF EXISTS mcp_servers_jsonb;
ALTER TABLE agents DROP COLUMN IF EXISTS egress_grant_jsonb;
ALTER TABLE agents DROP COLUMN IF EXISTS runtime_caps;
ALTER TABLE agents DROP COLUMN IF EXISTS image_digest;
