-- M5.3 — chat-driven mutations under autonomous-execution posture.
--
-- Three schema additions:
--
--   1. chat_mutation_audit table — forensic record of every
--      garrison-mutate verb call (success and failure). Every successful
--      verb writes one row in the same transaction as the data write;
--      failed verbs write a separate audit-only transaction (the
--      data-side ROLLBACK invalidates a same-tx audit INSERT). Carries
--      args_jsonb (full args including operator-typed text), outcome,
--      reversibility_class, and a single discriminated affected_resource
--      reference (no FK constraint — mirrors vault_access_log.secret_path
--      opacity precedent).
--
--   2. hiring_proposals table — minimum-viable column set for M5.3's
--      propose_hire verb + the read-only stopgap page. M7 ADDs review/
--      approve/spawn columns; M7 MUST NOT rename or remove any M5.3
--      column.
--
--   3. tickets.created_via_chat_session_id — nullable FK simplifying
--      forensic queries ("which tickets did chat session #X create"
--      without needing to join chat_mutation_audit). ON DELETE SET NULL
--      so chat session deletion doesn't cascade-destroy the ticket.
--
-- New chat-namespaced pg_notify channels follow the work.chat.<entity>.<action>
-- shape (mirrors the M5.1/M5.2 work.chat.session_* precedent):
--   work.chat.ticket.{created,edited,transitioned}
--   work.chat.agent.{paused,resumed,spawned,config_edited}
--   work.chat.hiring.proposed
--
-- Grants: garrison_dashboard_app gets SELECT on chat_mutation_audit
-- (read-only — supervisor writes via its own role) and SELECT, INSERT
-- on hiring_proposals (dashboard reads for the stopgap page; M7 will
-- add UPDATE).
--
-- See docs/security/chat-threat-model.md (M5.3) for the threat model
-- this migration ships under.

-- +goose Up

CREATE TABLE chat_mutation_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_session_id UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    chat_message_id UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    verb TEXT NOT NULL CHECK (verb IN (
        'create_ticket', 'edit_ticket', 'transition_ticket',
        'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
        'propose_hire'
    )),
    args_jsonb JSONB NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN (
        'success', 'validation_failed', 'leak_scan_failed', 'ticket_state_changed',
        'concurrency_cap_full', 'invalid_transition', 'resource_not_found',
        'tool_call_ceiling_reached'
    )),
    reversibility_class SMALLINT NOT NULL CHECK (reversibility_class IN (1, 2, 3)),
    affected_resource_id TEXT,
    affected_resource_type TEXT CHECK (affected_resource_type IN ('ticket', 'agent_role', 'hiring_proposal')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cma_session ON chat_mutation_audit (chat_session_id, created_at DESC);
CREATE INDEX idx_cma_resource ON chat_mutation_audit (affected_resource_type, affected_resource_id);

CREATE TABLE hiring_proposals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    role_title TEXT NOT NULL,
    department_slug TEXT NOT NULL REFERENCES departments(slug) ON DELETE RESTRICT,
    justification_md TEXT NOT NULL,
    skills_summary_md TEXT,
    proposed_via TEXT NOT NULL CHECK (proposed_via IN ('ceo_chat', 'dashboard', 'agent')),
    proposed_by_chat_session_id UUID REFERENCES chat_sessions(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'superseded')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_hp_status_dept ON hiring_proposals (status, department_slug, created_at DESC);
CREATE INDEX idx_hp_chat_session ON hiring_proposals (proposed_by_chat_session_id) WHERE proposed_by_chat_session_id IS NOT NULL;

ALTER TABLE tickets
    ADD COLUMN created_via_chat_session_id UUID REFERENCES chat_sessions(id) ON DELETE SET NULL;
CREATE INDEX idx_tickets_chat_session ON tickets (created_via_chat_session_id) WHERE created_via_chat_session_id IS NOT NULL;

GRANT SELECT ON chat_mutation_audit TO garrison_dashboard_app;
GRANT SELECT, INSERT ON hiring_proposals TO garrison_dashboard_app;

-- +goose Down

REVOKE SELECT, INSERT ON hiring_proposals FROM garrison_dashboard_app;
REVOKE SELECT ON chat_mutation_audit FROM garrison_dashboard_app;

DROP INDEX IF EXISTS idx_tickets_chat_session;
ALTER TABLE tickets DROP COLUMN IF EXISTS created_via_chat_session_id;

DROP INDEX IF EXISTS idx_hp_chat_session;
DROP INDEX IF EXISTS idx_hp_status_dept;
DROP TABLE IF EXISTS hiring_proposals;

DROP INDEX IF EXISTS idx_cma_resource;
DROP INDEX IF EXISTS idx_cma_session;
DROP TABLE IF EXISTS chat_mutation_audit;
