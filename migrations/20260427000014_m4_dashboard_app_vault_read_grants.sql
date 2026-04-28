-- +goose Up
-- M4 follow-up: dashboard_app role grants the M4 mutation paths
-- actually need.
--
-- 1. SELECT on agent_role_secrets / vault_access_log /
--    secret_metadata. The M3 split granted SELECT only to
--    garrison_dashboard_ro; M4's server actions execute through
--    the app role and also read these tables (deleteSecret
--    pre-flight grant check, agent edit form joins
--    agent_role_secrets to render existing grants, vault.ts
--    editSecret reads secret_metadata customer_id).
-- 2. UPDATE on agents. The M4 editAgent action mutates the
--    agent_md / model / skills / listens_for fields plus the
--    optimistic-lock updated_at column. M3 granted only SELECT
--    on agents.
-- 3. INSERT/UPDATE on tickets. The M4 createTicket / editTicket
--    actions mutate tickets; M3 granted only SELECT.
-- 4. INSERT on ticket_transitions. The M4 moveTicket action
--    writes a transition row.
--
-- Without these grants the agent edit page UPDATE throws
-- permission_denied, the ticket create / edit / move actions
-- throw permission_denied, and deleteSecret fails the pre-flight
-- grant check at the SELECT step. Caught by the M4 vault
-- golden-path Playwright spec when the testcontainer-Infisical
-- wiring landed.

GRANT SELECT ON agent_role_secrets, vault_access_log, secret_metadata
  TO garrison_dashboard_app;
GRANT UPDATE ON agents TO garrison_dashboard_app;
GRANT INSERT, UPDATE ON tickets TO garrison_dashboard_app;
GRANT INSERT ON ticket_transitions TO garrison_dashboard_app;
GRANT INSERT ON event_outbox TO garrison_dashboard_app;
-- The M4 grant migration covered INSERT/UPDATE/DELETE on the
-- vault tables but not vault_access_log — the audit row writes
-- need INSERT.
GRANT INSERT ON vault_access_log TO garrison_dashboard_app;

-- +goose Down
REVOKE INSERT ON vault_access_log FROM garrison_dashboard_app;
REVOKE INSERT ON event_outbox FROM garrison_dashboard_app;
REVOKE INSERT ON ticket_transitions FROM garrison_dashboard_app;
REVOKE INSERT, UPDATE ON tickets FROM garrison_dashboard_app;
REVOKE UPDATE ON agents FROM garrison_dashboard_app;
REVOKE SELECT ON agent_role_secrets, vault_access_log, secret_metadata
  FROM garrison_dashboard_app;
