-- M8 — Agent-spawned tickets, cross-department dependencies, runaway control,
-- MCP-server registry. Closes the event-driven zero-human loop on the M7
-- runtime substrate.
--
-- All four threads land in one migration:
--
-- 1. Agent-spawned tickets — `chat_mutation_audit.agent_instance_id` carries
--    the agent caller's anchor (NULL for chat-CEO callers; M7-era Server
--    Action audits remain unaffected because both anchors NULL is already a
--    valid shape post-M7). The `verb` CHECK gains `register_mcp_server`; the
--    `outcome` CHECK gains `dept_weekly_ticket_budget_exceeded`.
--
-- 2. Cross-department dependencies — `tickets.depends_on_ticket_id UUID NULL`
--    with self-reference CHECK + partial index. `departments.dependency_-
--    satisfaction_columns JSONB` controls per-dept which `column_slug` values
--    satisfy a dependency (default ["qa_review","done"]).
--
-- 3. Runaway control — `departments.weekly_ticket_budget INT NULL` (NULL =
--    unlimited; M8 alpha default). `throttle_events.kind` CHECK gains
--    `dept_weekly_ticket_budget_exceeded`. Reuses M6's throttle_events table
--    + work.throttle.event pg_notify channel verbatim.
--
-- 4. MCP-server registry (MCPJungle) — new `mcp_servers` table tracks
--    Garrison-side server records with status (pending → registered |
--    failed); the INSERT trigger emits work.mcp_server.registration_requested
--    so the supervisor's mcpserverwork worker picks up the request reactively
--    and calls MCPJungle's HTTP API. `agent_role_secrets.agent_id` is added
--    (nullable; non-NULL = per-agent scope) so MCPJungle bearer tokens can
--    live in agent-scoped vault grants alongside the M2.3 role-scoped grants.
--    `agent_install_journal.step` CHECK gains `mcpjungle_client_create` +
--    `mcpjungle_allowlist_apply` for the post-T005-reconciler installation
--    journal entries.
--
-- Customer-slug primitive — `companies.customer_slug TEXT UNIQUE NOT NULL`
-- with DEFAULT 'garrison' enabling beta-time per-customer MCPJungle instance
-- lookup (Option A from the multi-tenant analysis). M8 alpha seeds a single
-- row (the existing companies row picks up the default).
--
-- See specs/_context/m8-context.md for the full constraint chain; see
-- specs/018-m8-zero-human-loop/spec.md FR-001..FR-502 for the per-thread
-- requirements; see docs/research/m8-mcpjungle-spike.md for the MCPJungle
-- ACL findings backing the customer-prefix naming convention.

-- +goose Up

-- 1. chat_mutation_audit — agent_instance_id column + CHECK extensions
ALTER TABLE chat_mutation_audit
  ADD COLUMN agent_instance_id UUID NULL REFERENCES agent_instances(id) ON DELETE SET NULL;

CREATE INDEX idx_chat_mutation_audit_agent_instance
  ON chat_mutation_audit (agent_instance_id, created_at DESC)
  WHERE agent_instance_id IS NOT NULL;

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
    -- M8 Server Action verb (register_mcp_server only; create_ticket
    -- gains the agent caller path but keeps the existing verb name).
    'register_mcp_server'
  ));

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_outcome_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_outcome_check
  CHECK (outcome IN (
    'success', 'validation_failed', 'leak_scan_failed', 'ticket_state_changed',
    'concurrency_cap_full', 'invalid_transition', 'resource_not_found',
    'tool_call_ceiling_reached',
    -- M8 runaway-control outcome.
    'dept_weekly_ticket_budget_exceeded'
  ));

-- 2. tickets — depends_on_ticket_id column + self-reference CHECK + partial index
ALTER TABLE tickets
  ADD COLUMN depends_on_ticket_id UUID NULL REFERENCES tickets(id) ON DELETE SET NULL;

ALTER TABLE tickets
  ADD CONSTRAINT tickets_depends_on_not_self
  CHECK (depends_on_ticket_id IS NULL OR depends_on_ticket_id <> id);

CREATE INDEX idx_tickets_depends_on
  ON tickets (depends_on_ticket_id)
  WHERE depends_on_ticket_id IS NOT NULL;

-- 3. departments — dependency_satisfaction_columns + weekly_ticket_budget
ALTER TABLE departments
  ADD COLUMN dependency_satisfaction_columns JSONB NULL DEFAULT '["qa_review", "done"]'::jsonb,
  ADD COLUMN weekly_ticket_budget INT NULL;

-- 4. throttle_events — kind CHECK extension
ALTER TABLE throttle_events DROP CONSTRAINT IF EXISTS throttle_events_kind_check;
ALTER TABLE throttle_events
  ADD CONSTRAINT throttle_events_kind_check
  CHECK (kind IN (
    'company_budget_exceeded',
    'rate_limit_pause',
    -- M8 per-department weekly ticket-creation budget gate fire.
    'dept_weekly_ticket_budget_exceeded'
  ));

-- 5. companies — customer_slug primitive
ALTER TABLE companies
  ADD COLUMN customer_slug TEXT NOT NULL DEFAULT 'garrison';

ALTER TABLE companies
  ADD CONSTRAINT companies_customer_slug_unique UNIQUE (customer_slug);

-- 6. agent_role_secrets — agent_id discriminator + PK reshape.
--    The existing M2.3 PK (role_slug, env_var_name, customer_id) can't
--    accommodate a per-agent discriminator because composite PKs can't
--    contain nullable columns. Swap to a synthetic id PK + two partial
--    unique indexes preserving the role-scoped and adding the agent-
--    scoped uniqueness. Existing callers reference (role_slug,
--    env_var_name, customer_id) via WHERE clauses, not the PK directly,
--    so this is transparent to them. The trigger on agent_role_secrets
--    that maintains secret_metadata.allowed_role_slugs continues to fire
--    on INSERT/UPDATE/DELETE — DISTINCT in its aggregate dedupes the
--    role_slug if both role-scoped and agent-scoped rows exist for the
--    same role.
ALTER TABLE agent_role_secrets DROP CONSTRAINT IF EXISTS agent_role_secrets_pkey;
ALTER TABLE agent_role_secrets ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE agent_role_secrets ADD PRIMARY KEY (id);

ALTER TABLE agent_role_secrets
  ADD COLUMN agent_id UUID NULL REFERENCES agents(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX uq_agent_role_secrets_role_scope
  ON agent_role_secrets (role_slug, env_var_name, customer_id)
  WHERE agent_id IS NULL;

CREATE UNIQUE INDEX uq_agent_role_secrets_agent_scope
  ON agent_role_secrets (agent_id, env_var_name, customer_id)
  WHERE agent_id IS NOT NULL;

CREATE INDEX idx_agent_role_secrets_agent_id
  ON agent_role_secrets (agent_id)
  WHERE agent_id IS NOT NULL;

-- 7. agent_install_journal — extend step CHECK with M8 MCPJungle steps
ALTER TABLE agent_install_journal DROP CONSTRAINT IF EXISTS agent_install_journal_step_check;
ALTER TABLE agent_install_journal
  ADD CONSTRAINT agent_install_journal_step_check
  CHECK (step IN (
    -- M7 steps
    'download', 'verify_digest', 'extract', 'mount',
    'container_create', 'container_start',
    -- M8 MCPJungle steps (run by the supervisor's mcpjungle reconciler
    -- after container_start).
    'mcpjungle_client_create', 'mcpjungle_allowlist_apply'
  ));

-- 8. mcp_servers — new Garrison-side registry of MCP servers
CREATE TABLE mcp_servers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  customer_slug TEXT NOT NULL REFERENCES companies(customer_slug),
  name TEXT NOT NULL,
  transport TEXT NOT NULL CHECK (transport IN ('http', 'stdio', 'sse')),
  url TEXT,
  bearer_token_path TEXT,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'registered', 'failed', 'deregistered')),
  failure_reason TEXT,
  registered_by UUID NULL,
  registered_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (customer_slug, name)
);

CREATE INDEX idx_mcp_servers_status_pending
  ON mcp_servers (created_at)
  WHERE status = 'pending';

GRANT SELECT, INSERT, UPDATE ON mcp_servers TO garrison_dashboard_app;

-- 9. mcp_servers INSERT trigger — emit work.mcp_server.registration_requested
--    Per M1's reactive event-loop pattern; the supervisor's mcpserverwork
--    worker LISTENs on this channel and performs the MCPJungle API call
--    reactively (operator's dashboard write → DB row + trigger → pg_notify
--    → worker pickup → MCPJungle round-trip → UPDATE status + audit row).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION emit_mcp_server_registration_request() RETURNS TRIGGER AS $$
DECLARE
  payload JSONB;
BEGIN
  IF NEW.status = 'pending' THEN
    payload := jsonb_build_object(
      'mcp_server_id', NEW.id,
      'customer_slug', NEW.customer_slug,
      'name', NEW.name,
      'transport', NEW.transport,
      'url', NEW.url,
      'bearer_token_path', NEW.bearer_token_path
    );
    PERFORM pg_notify('work.mcp_server.registration_requested', payload::text);
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER mcp_servers_emit_registration_request
  AFTER INSERT ON mcp_servers
  FOR EACH ROW EXECUTE FUNCTION emit_mcp_server_registration_request();

-- +goose Down

-- mcp_servers
DROP TRIGGER IF EXISTS mcp_servers_emit_registration_request ON mcp_servers;
DROP FUNCTION IF EXISTS emit_mcp_server_registration_request();
REVOKE SELECT, INSERT, UPDATE ON mcp_servers FROM garrison_dashboard_app;
DROP INDEX IF EXISTS idx_mcp_servers_status_pending;
DROP TABLE IF EXISTS mcp_servers;

-- agent_install_journal — restore M7 CHECK
ALTER TABLE agent_install_journal DROP CONSTRAINT IF EXISTS agent_install_journal_step_check;
ALTER TABLE agent_install_journal
  ADD CONSTRAINT agent_install_journal_step_check
  CHECK (step IN (
    'download', 'verify_digest', 'extract', 'mount',
    'container_create', 'container_start'
  ));

-- agent_role_secrets — restore M2.3 PK shape, drop agent_id + partial indexes.
-- Delete agent-scoped rows first; they'd violate the restored composite PK.
DELETE FROM agent_role_secrets WHERE agent_id IS NOT NULL;
DROP INDEX IF EXISTS idx_agent_role_secrets_agent_id;
DROP INDEX IF EXISTS uq_agent_role_secrets_agent_scope;
DROP INDEX IF EXISTS uq_agent_role_secrets_role_scope;
ALTER TABLE agent_role_secrets DROP COLUMN IF EXISTS agent_id;
ALTER TABLE agent_role_secrets DROP CONSTRAINT IF EXISTS agent_role_secrets_pkey;
ALTER TABLE agent_role_secrets DROP COLUMN IF EXISTS id;
ALTER TABLE agent_role_secrets ADD PRIMARY KEY (role_slug, env_var_name, customer_id);

-- companies
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_customer_slug_unique;
ALTER TABLE companies DROP COLUMN IF EXISTS customer_slug;

-- throttle_events — restore M6 CHECK
ALTER TABLE throttle_events DROP CONSTRAINT IF EXISTS throttle_events_kind_check;
ALTER TABLE throttle_events
  ADD CONSTRAINT throttle_events_kind_check
  CHECK (kind IN ('company_budget_exceeded', 'rate_limit_pause'));

-- departments
ALTER TABLE departments DROP COLUMN IF EXISTS weekly_ticket_budget;
ALTER TABLE departments DROP COLUMN IF EXISTS dependency_satisfaction_columns;

-- tickets
DROP INDEX IF EXISTS idx_tickets_depends_on;
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_depends_on_not_self;
ALTER TABLE tickets DROP COLUMN IF EXISTS depends_on_ticket_id;

-- chat_mutation_audit — delete M8-era rows before reverting the CHECKs
DELETE FROM chat_mutation_audit
  WHERE verb = 'register_mcp_server'
     OR outcome = 'dept_weekly_ticket_budget_exceeded';

ALTER TABLE chat_mutation_audit DROP CONSTRAINT IF EXISTS chat_mutation_audit_outcome_check;
ALTER TABLE chat_mutation_audit
  ADD CONSTRAINT chat_mutation_audit_outcome_check
  CHECK (outcome IN (
    'success', 'validation_failed', 'leak_scan_failed', 'ticket_state_changed',
    'concurrency_cap_full', 'invalid_transition', 'resource_not_found',
    'tool_call_ceiling_reached'
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
    'grandfathered_at_m7'
  ));

DROP INDEX IF EXISTS idx_chat_mutation_audit_agent_instance;
ALTER TABLE chat_mutation_audit DROP COLUMN IF EXISTS agent_instance_id;
