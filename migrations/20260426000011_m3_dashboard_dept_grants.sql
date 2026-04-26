-- M3 follow-up: extend dashboard SELECT grants to departments + companies.
--
-- T001 (20260426000010_m3_dashboard_roles.sql) granted SELECT on the
-- M2-arc tables that ship the operational read surface (tickets,
-- ticket_transitions, agents, agent_instances, event_outbox), but
-- the org-overview surface (T010) and the Kanban surface (T011)
-- additionally need departments + companies for the per-department
-- row strip and the company-scoped landing screen.
--
-- Both roles get the same SELECT — garrison_dashboard_ro needs them
-- too because the role-secret matrix (T013) joins agent_role_secrets
-- against the agent role + department for display, and that resolves
-- through departments.

-- +goose Up
GRANT SELECT ON departments, companies TO garrison_dashboard_app;
GRANT SELECT ON departments, companies TO garrison_dashboard_ro;

-- +goose Down
REVOKE SELECT ON departments, companies FROM garrison_dashboard_ro;
REVOKE SELECT ON departments, companies FROM garrison_dashboard_app;
