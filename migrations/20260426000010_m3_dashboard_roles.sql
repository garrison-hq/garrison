-- M3 — cross-boundary roles for the dashboard.
--
-- The dashboard runs as a fourth container alongside supervisor +
-- mempalace + socket-proxy. It reads M2-arc data via two distinct
-- Postgres roles, neither of which is an agent-facing role:
--
--   garrison_dashboard_app — operational reads (org overview, Kanban,
--     ticket detail, hygiene table, agents registry, activity feed
--     catch-up). Also the role better-auth uses against its own
--     dashboard-side tables (users, sessions, accounts, verifications,
--     operator_invites). Drizzle migrations grant the write privileges
--     on those tables in their own SQL output (see
--     dashboard/drizzle/migrations).
--
--   garrison_dashboard_ro — vault read views only. The vault sub-views
--     (secrets list, audit log, role-secret matrix) connect via this
--     role; every other dashboard query connects via the app role.
--     Per spec FR-021, the role also gets SELECT on the joinable read
--     tables (tickets, ticket_transitions, agents, agent_instances)
--     so the audit-log filters by ticket/role and the role-secret
--     matrix render can join without bouncing back through the app
--     role.
--
-- Both roles ship as NOINHERIT NOLOGIN. Operators set passwords +
-- ALTER ROLE ... LOGIN at deployment time per docs/ops-checklist.md
-- M3 section, mirroring the M2.2 garrison_agent_mempalace pattern.
--
-- M3 does NOT use:
--   - garrison_agent_ro (sealed M2.1 / M2.2.1 surface)
--   - garrison_agent_mempalace (sealed M2.2 surface)
--   - any future agent-facing role
-- per AGENTS.md §M3 activation and the M2.3 vault threat model.

-- +goose Up
CREATE ROLE garrison_dashboard_app NOINHERIT NOLOGIN;
CREATE ROLE garrison_dashboard_ro NOINHERIT NOLOGIN;

-- App role: read-only on M2-arc tables. event_outbox is included so
-- the SSE catch-up flow (lib/queries/activityCatchup.ts) can replay
-- events with id > Last-Event-ID after a connection drop.
GRANT SELECT ON tickets, ticket_transitions, agents, agent_instances, event_outbox
  TO garrison_dashboard_app;

-- Vault read-only role: vault tables + the joinable read tables.
-- The joinable tables are required for the audit-log "filter by
-- ticket / by role" surface (FR-081) and the role-secret matrix
-- render (FR-082). Spec FR-021 enumerates exactly this set.
GRANT SELECT ON agent_role_secrets, vault_access_log, secret_metadata,
                tickets, ticket_transitions, agents, agent_instances
  TO garrison_dashboard_ro;

-- +goose Down
REVOKE SELECT ON agent_role_secrets, vault_access_log, secret_metadata,
                 tickets, ticket_transitions, agents, agent_instances
  FROM garrison_dashboard_ro;
REVOKE SELECT ON tickets, ticket_transitions, agents, agent_instances, event_outbox
  FROM garrison_dashboard_app;
DROP ROLE garrison_dashboard_ro;
DROP ROLE garrison_dashboard_app;
