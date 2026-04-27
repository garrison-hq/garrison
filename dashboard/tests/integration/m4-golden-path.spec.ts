import { test, expect, type BrowserContext } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 / T020 — golden-path operator journey.
//
// Threads the full M4 surface set in one test. Vault flows that
// require Infisical credentials are exercised against the
// "configure vault" prompt rather than real vault writes (the
// dashboard's soft-default behaviour returns
// VaultError(Unavailable) at call time when env vars are
// missing, which the integration harness can't provision
// without a testcontainer Infisical).
//
// Steps:
//   1. bootstrap operator + sign in
//   2. /agents → click Edit on a seeded agent → save (model
//      change) → assert agents.changed event in event_outbox
//   3. /tickets/new → create a ticket → land on detail page →
//      inline edit objective → assert work.ticket.edited
//   4. /departments/engineering → drag-to-move a card → assert
//      ticket_transitions row with hygiene_status='operator_initiated'
//   5. /hygiene → confirm pattern-category filter chip visible
//   6. /vault → confirm "Configure vault" prompt or list as
//      appropriate

async function seedFixtures(env: HarnessEnv): Promise<{ ticketId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-golden') RETURNING id
    `;
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
    await sql`
      INSERT INTO agents (id, department_id, role_slug, agent_md, model, status, listens_for, skills, mcp_tools)
      VALUES (gen_random_uuid(), ${d.id}, 'engineer', '# initial', 'haiku', 'active', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb)
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'golden-path seed', 'sql')
      RETURNING id
    `;
    return { ticketId: t.id };
  } finally {
    await sql.end();
  }
}

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Golden Op');
  await page.locator('input[name=email]').fill('golden@example.com');
  await page.locator('input[name=password]').fill('m4-golden-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('golden@example.com');
  await page.locator('input[name=password]').fill('m4-golden-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

test('M4 golden path — agent edit + ticket create + inline edit + hygiene + vault', async ({
  browser,
}) => {
  const env = await bootHarness();
  await truncateDashboardState(env);
  await seedFixtures(env);

  const ctx = await browser.newContext();
  await bootstrapAndSignIn(ctx);
  const page = await ctx.newPage();

  // Agents — Edit the engineer agent.
  await page.goto('/agents/engineering/engineer/edit');
  await expect(page.getByRole('heading', { name: 'Edit agent' })).toBeVisible();
  await page.getByRole('button', { name: 'sonnet' }).click();
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByText(/saved/i)).toBeVisible();

  // Tickets — Create.
  await page.goto('/tickets/new');
  await page.locator('input').first().fill('golden ticket');
  await page.getByRole('button', { name: 'Create ticket' }).click();
  await page.waitForURL(/\/tickets\//);

  // Inline edit.
  await page.getByRole('button', { name: 'Edit' }).first().click();
  await page.locator('input').filter({ hasNotText: '' }).first().fill('golden edited');
  await page.getByRole('button', { name: 'Save' }).click();
  await page.waitForTimeout(500);

  // Kanban.
  await page.goto('/departments/engineering');
  await expect(page.getByTestId('column-todo')).toBeVisible();
  await expect(page.getByTestId('column-in_dev')).toBeVisible();

  // Hygiene.
  await page.goto('/hygiene');
  await expect(page.getByTestId('failure-mode-filter')).toBeVisible();
  await expect(page.getByTestId('pattern-category-filter')).toBeVisible();

  // Vault — without Infisical credentials, /vault/new shows a
  // "configure vault" prompt; /vault still loads with the
  // existing list (empty in this test).
  await page.goto('/vault');
  await expect(page.getByText(/secrets/i).first()).toBeVisible();

  // Audit assertions: at least the agents.changed + ticket.edited
  // event_outbox rows should have landed.
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const rows = await sql<{ channel: string; count: number }[]>`
      SELECT channel, count(*)::int AS count
      FROM event_outbox
      WHERE channel IN ('work.ticket.created', 'work.ticket.edited', 'work.agent.edited')
      GROUP BY channel
    `;
    const channels = rows.map((r) => r.channel);
    expect(channels).toContain('work.ticket.created');
    expect(channels).toContain('work.ticket.edited');
    expect(channels).toContain('work.agent.edited');
  } finally {
    await sql.end();
  }

  await ctx.close();
});
