import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Golden-path integration test (T021). Drives the full M3 operator
// journey against the testcontainer-backed dashboard:
//
//   first-run wizard → log in → org overview KPIs render →
//   department Kanban → ticket detail (cost-blind-spot caveat +
//   sandbox-escape icon + expandable detail) → hygiene table
//   (3 failure modes filterable) → vault sub-views (3 tabs) →
//   activity feed (10-event seeded run) → admin/invites
//   → invite redemption in second context → both operators
//   navigate org overview simultaneously
//
// The seed shape is documented in tests/fixtures/golden-path-seed.sql.

interface SeedIds {
  costBlindSpotTicket: string;
  sandboxEscapeTicket: string;
  secretEmittedTicket: string;
  finalizeNeverCalledTicket: string;
  agentInstanceForRun: string;
}

async function seedGoldenPath(env: Awaited<ReturnType<typeof bootHarness>>): Promise<SeedIds> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'golden') RETURNING id`;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 2, '/tmp')
      RETURNING id
    `;
    await sql`
      UPDATE departments SET workflow = jsonb_build_object(
        'columns', jsonb_build_array(
          jsonb_build_object('slug', 'todo', 'label', 'To do'),
          jsonb_build_object('slug', 'in_dev', 'label', 'In dev'),
          jsonb_build_object('slug', 'qa_review', 'label', 'QA review'),
          jsonb_build_object('slug', 'done', 'label', 'Done')
        )
      ) WHERE id = ${d.id}
    `;
    // Two agents
    await sql`
      INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
      VALUES
        (gen_random_uuid(), ${d.id}, 'engineer', '#', 'haiku', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, NULL, 'active'),
        (gen_random_uuid(), ${d.id}, 'qa-engineer', '#', 'haiku', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, NULL, 'active')
    `;

    // 4 tickets, one per failure mode
    const [t1] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'cost-blind-spot', 'sql')
      RETURNING id
    `;
    const [t2] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'sandbox-escape', 'sql')
      RETURNING id
    `;
    const [t3] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'secret-emitted', 'sql')
      RETURNING id
    `;
    const [t4] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'finalize-never', 'sql')
      RETURNING id
    `;
    // Hygiene transitions
    await sql`
      INSERT INTO ticket_transitions (ticket_id, from_column, to_column, hygiene_status) VALUES
        (${t2.id}, 'todo', 'in_dev', 'sandbox_escape'),
        (${t3.id}, 'todo', 'in_dev', 'suspected_secret_emitted'),
        (${t4.id}, 'todo', 'in_dev', 'finalize_never_called')
    `;
    // Cost-blind-spot agent_instance
    const [ai] = await sql<{ id: string }[]>`
      INSERT INTO agent_instances (department_id, ticket_id, role_slug, status, exit_reason, total_cost_usd, started_at, finished_at)
      VALUES (${d.id}, ${t1.id}, 'engineer', 'finished', 'finalize_committed', 0, now() - interval '5 minutes', now() - interval '4 minutes')
      RETURNING id
    `;
    // Vault entries
    await sql`
      INSERT INTO secret_metadata (secret_path, customer_id, provenance, last_rotated_at, allowed_role_slugs) VALUES
        ('infra/db', ${c.id}, 'manual', now() - interval '30 days', ARRAY['engineer']),
        ('infra/cache', ${c.id}, 'manual', now() - interval '60 days', ARRAY['engineer','qa-engineer']),
        ('infra/api-token', ${c.id}, 'manual', NULL, ARRAY['engineer'])
    `;
    await sql`
      INSERT INTO agent_role_secrets (role_slug, secret_path, env_var_name, customer_id, granted_by) VALUES
        ('engineer',    'infra/db',        'DB_PASSWORD',   ${c.id}, 'op'),
        ('engineer',    'infra/cache',     'CACHE_TOKEN',   ${c.id}, 'op'),
        ('engineer',    'infra/api-token', 'API_TOKEN',     ${c.id}, 'op'),
        ('qa-engineer', 'infra/cache',     'CACHE_TOKEN',   ${c.id}, 'op')
    `;
    // Vault access events
    await sql`
      INSERT INTO vault_access_log (agent_instance_id, ticket_id, secret_path, customer_id, outcome) VALUES
        (${ai.id}, ${t1.id}, 'infra/db',    ${c.id}, 'granted'),
        (${ai.id}, ${t1.id}, 'infra/cache', ${c.id}, 'granted')
    `;
    // 10-event run in event_outbox
    await sql`
      INSERT INTO event_outbox (channel, payload, created_at) VALUES
        ('work.ticket.created', jsonb_build_object('ticket_id', ${t1.id}::text), now() - interval '10 minutes'),
        ('work.ticket.transitioned.engineering.todo.in_dev',    jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '9 minutes'),
        ('work.ticket.transitioned.engineering.in_dev.in_dev',  jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '8 minutes'),
        ('work.ticket.transitioned.engineering.in_dev.in_dev',  jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '7 minutes'),
        ('work.ticket.transitioned.engineering.in_dev.in_dev',  jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '6 minutes'),
        ('work.ticket.transitioned.engineering.in_dev.in_dev',  jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '5 minutes'),
        ('work.ticket.transitioned.engineering.in_dev.qa_review',  jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '4 minutes'),
        ('work.ticket.transitioned.engineering.qa_review.qa_review', jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '3 minutes'),
        ('work.ticket.transitioned.engineering.qa_review.qa_review', jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '2 minutes'),
        ('work.ticket.transitioned.engineering.qa_review.done',     jsonb_build_object('ticket_id', ${t1.id}::text, 'agent_instance_id', ${ai.id}::text), now() - interval '1 minute')
    `;
    return {
      costBlindSpotTicket: t1.id,
      sandboxEscapeTicket: t2.id,
      secretEmittedTicket: t3.id,
      finalizeNeverCalledTicket: t4.id,
      agentInstanceForRun: ai.id,
    };
  } finally {
    await sql.end();
  }
}

test('M3 golden-path acceptance: full operator journey', async ({ browser }) => {
  test.setTimeout(120_000);
  const env = await bootHarness();
  await truncateDashboardState(env);
  const seed = await seedGoldenPath(env);

  const ctxA = await browser.newContext();
  const pageA = await ctxA.newPage();

  // Step 1: first-run wizard
  await pageA.goto('/setup');
  await pageA.locator('input[name=name]').fill('Op One');
  await pageA.locator('input[name=email]').fill('op-one@example.com');
  await pageA.locator('input[name=password]').fill('integration-pw-1');
  await pageA.getByRole('button', { name: 'Create operator account' }).click();
  await pageA.waitForURL(/\/login/);

  // Step 2: log in
  await pageA.locator('input[name=email]').fill('op-one@example.com');
  await pageA.locator('input[name=password]').fill('integration-pw-1');
  await pageA.getByRole('button', { name: 'Sign in' }).click();
  await pageA.waitForURL((url) => url.pathname === '/');

  // Step 3: org overview
  await expect(pageA.getByRole('heading', { name: 'Org overview' })).toBeVisible();

  // Step 4: department Kanban
  await pageA.goto('/departments/engineering');
  await expect(pageA.getByTestId('column-todo')).toBeVisible();
  await expect(pageA.getByTestId('column-in_dev')).toBeVisible();
  await expect(pageA.getByTestId('column-qa_review')).toBeVisible();
  await expect(pageA.getByTestId('column-done')).toBeVisible();

  // Step 5: ticket detail with cost-blind-spot
  await pageA.goto(`/tickets/${seed.costBlindSpotTicket}`);
  await expect(pageA.getByTestId('cost-caveat-icon')).toBeVisible();

  // Step 6: ticket detail with sandbox-escape
  await pageA.goto(`/tickets/${seed.sandboxEscapeTicket}`);
  await expect(pageA.getByTestId('failure-icon')).toBeVisible();
  await pageA.getByTestId('history-block').getByRole('button').first().click();
  await expect(pageA.getByTestId('sandbox-escape-detail')).toBeVisible();

  // Step 7: hygiene table — all 3 failure modes filterable
  await pageA.goto('/hygiene');
  await expect(pageA.getByTestId('hygiene-row')).toHaveCount(3);
  await pageA.getByTestId('mode-suspected_secret_emitted').click();
  await expect(pageA.getByTestId('hygiene-row')).toHaveCount(1);
  await expect(pageA.getByTestId('pattern-category')).toBeVisible();
  await pageA.getByTestId('mode-sandbox_escape').click();
  await expect(pageA.getByTestId('hygiene-row')).toHaveCount(1);
  await pageA.getByTestId('mode-finalize_path').click();
  await expect(pageA.getByTestId('hygiene-row')).toHaveCount(1);

  // Step 8: vault sub-views — all 3 tabs render via garrison_dashboard_ro
  await pageA.goto('/vault');
  await expect(pageA.getByTestId('secret-row')).toHaveCount(3);
  await pageA.goto('/vault/audit');
  await expect(pageA.getByTestId('audit-row')).toHaveCount(2);
  await pageA.goto('/vault/matrix');
  await expect(pageA.getByTestId('matrix-cell-granted')).toHaveCount(4);

  // Step 9: activity feed — events appear once the SSE catch-up
  // delivers the seeded outbox rows. The exact count depends on
  // how the events bucket by agent_instance_id; the load-bearing
  // assertion is that AT LEAST one run-group is visible and
  // expandable. Detailed event-count checks live in
  // tests/integration/activity-feed.spec.ts.
  await pageA.goto('/activity');
  await expect(pageA.getByTestId('run-group').first()).toBeVisible({ timeout: 15_000 });
  await pageA.getByTestId('run-group').first().getByRole('button').click();
  // Wait briefly for events to render; assert at least one
  // expanded EventRow.
  await expect(pageA.getByTestId('event-row').first()).toBeVisible({ timeout: 5_000 });

  // Step 10: admin/invites — generate invite
  await pageA.goto('/admin/invites');
  await pageA.getByTestId('generate-invite').click();
  await expect(pageA.getByTestId('invite-link').first()).toBeVisible({ timeout: 10_000 });
  const inviteLink = (await pageA.getByTestId('invite-link').first().textContent()) ?? '';

  // Step 11: second operator redeems
  const ctxB = await browser.newContext();
  const pageB = await ctxB.newPage();
  await pageB.goto(inviteLink);
  await pageB.locator('input[name=name]').fill('Op Two');
  await pageB.locator('input[name=email]').fill('op-two@example.com');
  await pageB.locator('input[name=password]').fill('integration-pw-2');
  await pageB.getByRole('button', { name: 'Create account' }).click();
  await pageB.waitForURL((url) => url.pathname === '/');

  // Step 12: both operators navigate org overview simultaneously
  await Promise.all([pageA.goto('/'), pageB.goto('/')]);
  await expect(pageA.getByRole('heading', { name: 'Org overview' })).toBeVisible();
  await expect(pageB.getByRole('heading', { name: 'Org overview' })).toBeVisible();

  await ctxA.close();
  await ctxB.close();
});
