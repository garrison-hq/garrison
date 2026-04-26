-- M3 dev-stack seed data.
--
-- Populates a clean Garrison Postgres with realistic-looking M2-arc
-- rows so the operator dashboard renders something interesting on
-- first boot (org overview KPIs, Kanban with cards across all
-- columns, hygiene rows in three failure-mode buckets, vault
-- audit-log rows, agents registry stats, recent activity events).
--
-- Idempotent: every INSERT uses ON CONFLICT DO NOTHING keyed on a
-- stable predicate. Re-running won't double-seed.
--
-- Run as the migration owner (the postgres superuser the dev-stack
-- script provisions). Roles + grants are created by goose migrations
-- 20260426000010 and 20260426000011 — the dev-stack script applies
-- those before running this file.

BEGIN;

-- =========================================================================
-- 0. Reset M2-arc state.
-- =========================================================================
-- The M2.2 migration (20260423000004_m2_2_mempalace.sql) seeds a default
-- "engineering" department to make the supervisor's first run usable.
-- The dev-stack wants a fully-controlled fixture, so we wipe + re-seed.
-- The dashboard-owned tables (users, sessions, accounts, verifications,
-- operator_invites) are NOT touched — first-run wizard owns those.

TRUNCATE
  vault_access_log,
  agent_role_secrets,
  secret_metadata,
  event_outbox,
  ticket_transitions,
  agent_instances,
  tickets,
  agents,
  departments,
  companies
RESTART IDENTITY CASCADE;

-- =========================================================================
-- 1. Companies + departments
-- =========================================================================

INSERT INTO companies (id, name)
VALUES
  ('00000000-0000-0000-0000-00000000c001', 'Acme Corp')
ON CONFLICT (id) DO NOTHING;

INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path, workflow)
VALUES
  ('00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-00000000c001',
   'engineering', 'Engineering', 3, '/tmp/garrison-dev/engineering',
   '{"columns":[{"slug":"todo","label":"To do"},{"slug":"in_dev","label":"In dev"},{"slug":"in_review","label":"In review"},{"slug":"done","label":"Done"}]}'::jsonb),
  ('00000000-0000-0000-0000-00000000d002', '00000000-0000-0000-0000-00000000c001',
   'qa-engineer', 'QA Engineering', 2, '/tmp/garrison-dev/qa-engineer',
   '{"columns":[{"slug":"todo","label":"To do"},{"slug":"in_dev","label":"In dev"},{"slug":"in_review","label":"In review"},{"slug":"done","label":"Done"}]}'::jsonb),
  ('00000000-0000-0000-0000-00000000d003', '00000000-0000-0000-0000-00000000c001',
   'docs', 'Documentation', 1, '/tmp/garrison-dev/docs',
   '{"columns":[{"slug":"todo","label":"To do"},{"slug":"in_dev","label":"In dev"},{"slug":"in_review","label":"In review"},{"slug":"done","label":"Done"}]}'::jsonb)
ON CONFLICT (id) DO NOTHING;

-- =========================================================================
-- 2. Agents (one engineer per department, plus a QA reviewer)
-- =========================================================================

INSERT INTO agents (id, department_id, role_slug, agent_md, model, listens_for, palace_wing, status)
VALUES
  ('00000000-0000-0000-0000-0000000a0001',
   '00000000-0000-0000-0000-00000000d001',
   'engineer',
   '# Engineer (dev-stack seed)' || chr(10) ||
   'You are the engineering department engineer agent.' || chr(10) ||
   'Pick up the next ticket in `todo`, do the work, finalize.',
   'claude-opus-4-7',
   '["todo"]'::jsonb,
   'wing_engineering',
   'active'),
  ('00000000-0000-0000-0000-0000000a0002',
   '00000000-0000-0000-0000-00000000d002',
   'qa-engineer',
   '# QA Engineer (dev-stack seed)' || chr(10) ||
   'You are the QA reviewer.' || chr(10) ||
   'Pick up `in_review` tickets, run the test plan, finalize.',
   'claude-sonnet-4-6',
   '["in_review"]'::jsonb,
   'wing_qa',
   'active'),
  ('00000000-0000-0000-0000-0000000a0003',
   '00000000-0000-0000-0000-00000000d003',
   'tech-writer',
   '# Tech Writer (dev-stack seed)' || chr(10) ||
   'You are the documentation agent.',
   'claude-haiku-4-5',
   '["todo"]'::jsonb,
   'wing_docs',
   'active')
ON CONFLICT (department_id, role_slug) DO NOTHING;

-- =========================================================================
-- 3. Tickets — one per Kanban column per department + a few extras
-- =========================================================================

-- Engineering: spread across all 4 columns
INSERT INTO tickets (id, department_id, column_slug, objective, acceptance_criteria, origin, created_at)
VALUES
  ('00000000-0000-0000-0000-aaaa00000001', '00000000-0000-0000-0000-00000000d001',
   'todo', 'Add OAuth provider list to /login',
   'Login page renders 3 provider buttons (Google, GitHub, Microsoft).', 'sql',
   now() - interval '2 days'),
  ('00000000-0000-0000-0000-aaaa00000002', '00000000-0000-0000-0000-00000000d001',
   'todo', 'Migrate session storage from cookie to Redis',
   'Sessions persist across dashboard restart.', 'sql',
   now() - interval '5 hours'),
  ('00000000-0000-0000-0000-aaaa00000003', '00000000-0000-0000-0000-00000000d001',
   'in_dev', 'Wire CSRF token rotation to the form library',
   'Every POST form receives a fresh token per render.', 'sql',
   now() - interval '8 hours'),
  ('00000000-0000-0000-0000-aaaa00000004', '00000000-0000-0000-0000-00000000d001',
   'in_review', 'Bcrypt rounds bumped from 10 to 12',
   'Login latency within 250ms p95 on baseline machine.', 'sql',
   now() - interval '1 day'),
  ('00000000-0000-0000-0000-aaaa00000005', '00000000-0000-0000-0000-00000000d001',
   'done', 'Patch CVE-2026-1234 in upstream postgres-js',
   'Patched version pinned in package.json + lock.', 'sql',
   now() - interval '3 days');

-- QA: 2 tickets
INSERT INTO tickets (id, department_id, column_slug, objective, acceptance_criteria, origin, created_at)
VALUES
  ('00000000-0000-0000-0000-aaaa00010001', '00000000-0000-0000-0000-00000000d002',
   'in_review', 'Verify session migration meets latency budget',
   'Run perf suite; p95 under 300ms.', 'sql',
   now() - interval '10 hours'),
  ('00000000-0000-0000-0000-aaaa00010002', '00000000-0000-0000-0000-00000000d002',
   'done', 'Smoke-test M3 dashboard against staging',
   'All eight surfaces render with seed data.', 'sql',
   now() - interval '2 days');

-- Docs: 1 ticket
INSERT INTO tickets (id, department_id, column_slug, objective, acceptance_criteria, origin, created_at)
VALUES
  ('00000000-0000-0000-0000-aaaa00020001', '00000000-0000-0000-0000-00000000d003',
   'todo', 'Document the dashboard role-isolation pattern',
   'New section in ARCHITECTURE.md; reviewed by CTO.', 'sql',
   now() - interval '1 day');

-- =========================================================================
-- 4. Agent instances — terminal rows (no live runs, dev-stack is static)
-- =========================================================================

-- Engineer instances against done + in_review tickets, plus one over the
-- M2.3 sandbox-escape failure mode for hygiene-table demo data.
INSERT INTO agent_instances (id, department_id, ticket_id, role_slug, status, started_at, finished_at, exit_reason, total_cost_usd, wake_up_status)
VALUES
  ('00000000-0000-0000-0000-bbbb00000001',
   '00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-aaaa00000005',
   'engineer', 'completed',
   now() - interval '3 days', now() - interval '3 days' + interval '4 minutes',
   'finalize_ticket called', 0.034210, 'normal'),
  ('00000000-0000-0000-0000-bbbb00000002',
   '00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-aaaa00000004',
   'engineer', 'completed',
   now() - interval '1 day', now() - interval '1 day' + interval '6 minutes',
   'finalize_ticket called', 0.067540, 'normal'),
  ('00000000-0000-0000-0000-bbbb00000003',
   '00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-aaaa00000003',
   'engineer', 'completed',
   now() - interval '8 hours', now() - interval '8 hours' + interval '11 minutes',
   'finalize_ticket called', 0.000000, 'normal'),
  ('00000000-0000-0000-0000-bbbb00000004',
   '00000000-0000-0000-0000-00000000d002', '00000000-0000-0000-0000-aaaa00010002',
   'qa-engineer', 'completed',
   now() - interval '2 days', now() - interval '2 days' + interval '7 minutes',
   'finalize_ticket called', 0.022100, 'normal'),
  ('00000000-0000-0000-0000-bbbb00000005',
   '00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-aaaa00000003',
   'engineer', 'completed',
   now() - interval '14 hours', now() - interval '14 hours' + interval '3 minutes',
   'sandbox_escape', 0.012000, 'normal'),
  -- Two currently-running spawns so the org-overview "Live spawns"
  -- card has something to show. NULL finished_at + status='running'
  -- is the predicate the dashboard's Sidebar + Live spawns query
  -- both filter on. Started a couple minutes ago so the elapsed
  -- column reads "2m 14s" / "47s" rather than "0s".
  ('00000000-0000-0000-0000-bbbb00000006',
   '00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-aaaa00000002',
   'engineer', 'running',
   now() - interval '2 minutes 14 seconds', NULL,
   NULL, NULL, 'normal'),
  ('00000000-0000-0000-0000-bbbb00000007',
   '00000000-0000-0000-0000-00000000d002', '00000000-0000-0000-0000-aaaa00010001',
   'qa-engineer', 'running',
   now() - interval '47 seconds', NULL,
   NULL, NULL, 'normal')
ON CONFLICT (id) DO NOTHING;

-- =========================================================================
-- 5. Ticket transitions — drive the hygiene table
-- =========================================================================
-- One clean per done ticket, plus a few flagged rows to populate the
-- hygiene table's three failure-mode buckets.

INSERT INTO ticket_transitions (ticket_id, from_column, to_column, triggered_by_agent_instance_id, hygiene_status, at)
VALUES
  -- Clean transitions (won't show in /hygiene; will show in activity feed)
  ('00000000-0000-0000-0000-aaaa00000005', 'in_review', 'done',
   '00000000-0000-0000-0000-bbbb00000001', 'clean', now() - interval '3 days' + interval '4 minutes'),
  ('00000000-0000-0000-0000-aaaa00000004', 'in_dev', 'in_review',
   '00000000-0000-0000-0000-bbbb00000002', 'clean', now() - interval '1 day' + interval '6 minutes'),
  ('00000000-0000-0000-0000-aaaa00010002', 'in_review', 'done',
   '00000000-0000-0000-0000-bbbb00000004', 'clean', now() - interval '2 days' + interval '7 minutes'),

  -- Finalize-path failure mode (one of: missing_diary, missing_kg, finalize_never_called)
  ('00000000-0000-0000-0000-aaaa00000003', 'todo', 'in_dev',
   '00000000-0000-0000-0000-bbbb00000003', 'finalize_never_called',
   now() - interval '8 hours' + interval '11 minutes'),

  -- Sandbox-escape failure mode
  ('00000000-0000-0000-0000-aaaa00000003', 'in_dev', 'todo',
   '00000000-0000-0000-0000-bbbb00000005', 'sandbox_escape',
   now() - interval '14 hours' + interval '3 minutes'),

  -- Suspected-secret-emitted failure mode (the M2.3 vault scanner output)
  ('00000000-0000-0000-0000-aaaa00000003', 'in_dev', 'todo',
   '00000000-0000-0000-0000-bbbb00000005', 'suspected_secret_emitted',
   now() - interval '14 hours' + interval '4 minutes');

-- =========================================================================
-- 6. Event outbox — recent events for the activity feed
-- =========================================================================

INSERT INTO event_outbox (channel, payload, created_at)
VALUES
  ('work.ticket.created',
   jsonb_build_object('ticket_id', '00000000-0000-0000-0000-aaaa00000002', 'event_id', 'evt-001'),
   now() - interval '5 hours'),
  ('work.ticket.transitioned.engineering.in_dev.in_review',
   jsonb_build_object(
     'ticket_id', '00000000-0000-0000-0000-aaaa00000004',
     'agent_instance_id', '00000000-0000-0000-0000-bbbb00000002',
     'event_id', 'evt-002'),
   now() - interval '1 day' + interval '6 minutes'),
  ('work.ticket.transitioned.engineering.todo.in_dev',
   jsonb_build_object(
     'ticket_id', '00000000-0000-0000-0000-aaaa00000003',
     'agent_instance_id', '00000000-0000-0000-0000-bbbb00000003',
     'event_id', 'evt-003'),
   now() - interval '8 hours'),
  ('work.ticket.transitioned.engineering.in_review.done',
   jsonb_build_object(
     'ticket_id', '00000000-0000-0000-0000-aaaa00000005',
     'agent_instance_id', '00000000-0000-0000-0000-bbbb00000001',
     'event_id', 'evt-004'),
   now() - interval '3 days' + interval '4 minutes'),
  ('work.ticket.transitioned.qa-engineer.in_review.done',
   jsonb_build_object(
     'ticket_id', '00000000-0000-0000-0000-aaaa00010002',
     'agent_instance_id', '00000000-0000-0000-0000-bbbb00000004',
     'event_id', 'evt-005'),
   now() - interval '2 days' + interval '7 minutes');

-- =========================================================================
-- 7. Vault tables — secrets, role-secret grants, audit-log entries
-- =========================================================================

INSERT INTO secret_metadata (secret_path, customer_id, provenance, rotation_cadence, last_rotated_at, last_accessed_at, allowed_role_slugs)
VALUES
  ('/acme/db/postgres-app',
   '00000000-0000-0000-0000-00000000c001',
   'manual', '90 days',
   now() - interval '12 days', now() - interval '4 hours',
   ARRAY['engineer']),
  ('/acme/api/stripe-prod',
   '00000000-0000-0000-0000-00000000c001',
   'manual', '30 days',
   now() - interval '40 days', now() - interval '1 day',
   ARRAY['engineer']),
  ('/acme/api/sendgrid',
   '00000000-0000-0000-0000-00000000c001',
   'manual', '90 days',
   now() - interval '7 days', NULL,
   ARRAY['tech-writer'])
ON CONFLICT (secret_path, customer_id) DO NOTHING;

INSERT INTO agent_role_secrets (role_slug, secret_path, env_var_name, customer_id, granted_by)
VALUES
  ('engineer', '/acme/db/postgres-app', 'POSTGRES_APP_DSN',
   '00000000-0000-0000-0000-00000000c001', 'operator@dev-stack'),
  ('engineer', '/acme/api/stripe-prod', 'STRIPE_API_KEY',
   '00000000-0000-0000-0000-00000000c001', 'operator@dev-stack'),
  ('tech-writer', '/acme/api/sendgrid', 'SENDGRID_API_KEY',
   '00000000-0000-0000-0000-00000000c001', 'operator@dev-stack')
ON CONFLICT (role_slug, env_var_name, customer_id) DO NOTHING;

INSERT INTO vault_access_log (agent_instance_id, ticket_id, secret_path, customer_id, outcome, "timestamp")
VALUES
  ('00000000-0000-0000-0000-bbbb00000001', '00000000-0000-0000-0000-aaaa00000005',
   '/acme/db/postgres-app', '00000000-0000-0000-0000-00000000c001', 'allowed',
   now() - interval '3 days' + interval '1 minute'),
  ('00000000-0000-0000-0000-bbbb00000002', '00000000-0000-0000-0000-aaaa00000004',
   '/acme/db/postgres-app', '00000000-0000-0000-0000-00000000c001', 'allowed',
   now() - interval '1 day' + interval '2 minutes'),
  ('00000000-0000-0000-0000-bbbb00000003', '00000000-0000-0000-0000-aaaa00000003',
   '/acme/api/stripe-prod', '00000000-0000-0000-0000-00000000c001', 'allowed',
   now() - interval '8 hours'),
  ('00000000-0000-0000-0000-bbbb00000005', '00000000-0000-0000-0000-aaaa00000003',
   '/acme/api/sendgrid', '00000000-0000-0000-0000-00000000c001', 'denied',
   now() - interval '14 hours');

COMMIT;

-- Re-grants for the dashboard roles. The migration grants are on
-- migration-time tables, but the seed inserts above don't change
-- ownership; the existing GRANT on schema public + the role-level
-- grants in 20260426000010 + 20260426000011 still apply. This block
-- exists only to flip the two dashboard roles to LOGIN with passwords
-- the dev-stack script knows.

ALTER ROLE garrison_dashboard_app WITH LOGIN PASSWORD 'apppass';
ALTER ROLE garrison_dashboard_ro  WITH LOGIN PASSWORD 'ropass';
