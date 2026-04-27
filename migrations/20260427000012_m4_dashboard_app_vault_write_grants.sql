-- M4 — extend garrison_dashboard_app grants for vault writes.
--
-- M3's role split kept garrison_dashboard_app off the vault
-- tables entirely; vault reads go through garrison_dashboard_ro
-- which has SELECT only. M4 introduces operator-driven vault
-- mutations through the dashboard, so garrison_dashboard_app
-- needs INSERT (vault_access_log audit rows; new secret_metadata
-- + agent_role_secrets rows on create/grant), UPDATE
-- (secret_metadata on edit/rotate; both have updated_at +
-- last_rotated_at columns the supervisor's existing trigger
-- relies on — we keep those triggers intact), and DELETE
-- (secret_metadata on delete; agent_role_secrets on grant
-- removal).
--
-- garrison_dashboard_ro is unchanged — it remains SELECT-only
-- on every table, including the vault set. The activity feed's
-- vault catch-up read continues to use garrison_dashboard_ro
-- (lib/queries/activityCatchup.ts T003 extension).
--
-- Threat model Rule 6 invariant ("audit everything, log no
-- values") is preserved: the GRANTs are at the SQL layer,
-- secret values are kept out of these tables by the application
-- layer (FR-017 TS discipline + M2.3 supervisor's vaultlog
-- analyzer). Garrison's customer_id-keyed schema (M2.3) is
-- already single-tenant; no row-level security adds at this
-- milestone.

-- +goose Up
GRANT INSERT, UPDATE, DELETE ON vault_access_log TO garrison_dashboard_app;
GRANT INSERT, UPDATE, DELETE ON secret_metadata TO garrison_dashboard_app;
GRANT INSERT, UPDATE, DELETE ON agent_role_secrets TO garrison_dashboard_app;

-- +goose Down
REVOKE INSERT, UPDATE, DELETE ON agent_role_secrets FROM garrison_dashboard_app;
REVOKE INSERT, UPDATE, DELETE ON secret_metadata FROM garrison_dashboard_app;
REVOKE INSERT, UPDATE, DELETE ON vault_access_log FROM garrison_dashboard_app;
