import { test, expect, type BrowserContext } from './_coverage';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 / coverage spec — exercises the broader AgentSettingsForm
// edit cycle (model + listens_for + skills) and the
// TicketInlineEditor cancel + save edge cases. Bottom-of-list
// uncovered lines: 46/67 (AgentSettingsForm) + 35/48
// (TicketInlineEditor).

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Edge2 Op');
  await page.locator('input[name=email]').fill('edge2@example.com');
  await page.locator('input[name=password]').fill('m4-edge2-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('edge2@example.com');
  await page.locator('input[name=password]').fill('m4-edge2-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

async function seedDeptAndAgent(env: HarnessEnv): Promise<{ deptId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-edge2') RETURNING id
    `;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 1, '/tmp')
      RETURNING id
    `;
    await sql`
      UPDATE departments SET workflow = jsonb_build_object(
        'columns', jsonb_build_array(
          jsonb_build_object('slug', 'todo', 'label', 'To do'),
          jsonb_build_object('slug', 'in_dev', 'label', 'In dev')
        )
      ) WHERE id = ${d.id}
    `;
    await sql`
      INSERT INTO agents (id, department_id, role_slug, agent_md, model, status, listens_for, skills, mcp_tools)
      VALUES (gen_random_uuid(), ${d.id}, 'engineer', '# initial', 'haiku', 'active',
              '[]'::jsonb, '[]'::jsonb, '[]'::jsonb)
    `;
    return { deptId: d.id };
  } finally {
    await sql.end();
  }
}

test.describe('M4 agent + ticket edge cases', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('AgentSettingsForm full edit cycle: model + listens_for + skills + agent_md', async ({
    browser,
  }) => {
    const env = await bootHarness();
    await seedDeptAndAgent(env);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    await page.goto('/agents/engineering/engineer/edit');
    await expect(page.getByRole('heading', { name: 'Edit agent' })).toBeVisible();

    // Edit each field. Different fields exercise different
    // branches in handleSave's buildChanges + the JSON-array
    // splitters for listens_for + skills.
    await page.locator('textarea').first().fill('# updated agent.md\n\nNew instructions.');
    await page.getByRole('button', { name: 'sonnet' }).click();
    await page.locator('textarea').nth(1).fill('work.ticket.created.engineering.todo\nwork.ticket.transitioned.engineering.*');
    await page.locator('textarea').nth(2).fill('typescript-coding\nplaywright-dev');
    await page.getByRole('button', { name: 'Save changes' }).click();

    await expect(page.getByText(/saved/i)).toBeVisible({ timeout: 5_000 });

    // Verify the row landed.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const r = await sql<{ model: string; listens_for: string[]; skills: string[] }[]>`
        SELECT model, listens_for, skills FROM agents WHERE role_slug = 'engineer'
      `;
      expect(r[0]?.model).toBe('sonnet');
      expect(r[0]?.listens_for).toContain('work.ticket.created.engineering.todo');
      expect(r[0]?.skills).toContain('typescript-coding');
    } finally {
      await sql.end();
    }

    await ctx.close();
  });

  test('TicketInlineEditor cancel preserves the original objective', async ({ browser }) => {
    const env = await bootHarness();
    await seedDeptAndAgent(env);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    // Create a ticket via the form so the inline editor has
    // something to act on.
    await page.goto('/tickets/new');
    await page.locator('input').first().fill('original objective');
    await page.getByRole('button', { name: 'Create ticket' }).click();
    await page.waitForURL(/\/tickets\/[a-f0-9-]{36}$/, { timeout: 10_000 });
    await page.goto(page.url());

    // Enter edit mode → type a new objective → click Cancel.
    await page.getByRole('button', { name: 'Edit' }).first().click();
    const objectiveInput = page.locator('input').filter({ hasNotText: '' }).first();
    await objectiveInput.fill('cancelled change');
    await page.getByRole('button', { name: /cancel/i }).click();

    // Verify the row in the database still carries the original.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const r = await sql<{ objective: string }[]>`
        SELECT objective FROM tickets WHERE objective = 'original objective'
      `;
      expect(r).toHaveLength(1);
    } finally {
      await sql.end();
    }

    await ctx.close();
  });

  test('TicketCreateForm rejects empty objective + Cancel returns to previous page', async ({
    browser,
  }) => {
    const env = await bootHarness();
    await seedDeptAndAgent(env);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    // First: navigate via the Cancel target. We come from / so
    // Cancel returns there.
    await page.goto('/');
    await page.goto('/tickets/new');
    // Empty submit → "Objective cannot be empty" client-side branch.
    await page.getByRole('button', { name: 'Create ticket' }).click();
    await expect(page.getByText(/objective cannot be empty/i)).toBeVisible({ timeout: 5_000 });

    // Cancel → router.back() returns us to /.
    await page.getByRole('button', { name: 'Cancel' }).click();
    await page.waitForURL((url) => url.pathname === '/');

    await ctx.close();
  });
});
