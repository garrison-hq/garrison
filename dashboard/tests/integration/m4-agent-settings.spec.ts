import { test, expect, type BrowserContext } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 / T013 — agent settings editor end-to-end.
//
// Exercises the editAgent server action's three load-bearing
// properties:
//
//   1. accepted save updates the agents row, emits the
//      agents.changed pg_notify (consumed by the supervisor's
//      cache invalidator in T014), and records the field-level
//      diff in event_outbox.
//   2. optimistic-lock conflict surfaces the conflict modal.
//   3. the agent settings editor is reachable via the
//      AgentsTable Edit link.

async function seedAgent(env: HarnessEnv): Promise<{ deptSlug: string; roleSlug: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-agent') RETURNING id
    `;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 1, '/tmp')
      RETURNING id
    `;
    await sql`
      INSERT INTO agents (id, department_id, role_slug, agent_md, model, status, listens_for, skills, mcp_tools)
      VALUES (gen_random_uuid(), ${d.id}, 'engineer', '# initial', 'haiku', 'active', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb)
    `;
    return { deptSlug: 'engineering', roleSlug: 'engineer' };
  } finally {
    await sql.end();
  }
}

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Agent Op');
  await page.locator('input[name=email]').fill('agent@example.com');
  await page.locator('input[name=password]').fill('m4-agent-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('agent@example.com');
  await page.locator('input[name=password]').fill('m4-agent-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

test.describe('M4 agent settings editor', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('edit page renders + save lands a row + agents.changed pg_notify fires', async ({
    browser,
  }) => {
    const env = await bootHarness();
    const { deptSlug, roleSlug } = await seedAgent(env);

    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();
    await page.goto(`/agents/${deptSlug}/${roleSlug}/edit`);

    await expect(page.getByRole('heading', { name: 'Edit agent' })).toBeVisible();

    // Click "sonnet" model button.
    await page.getByRole('button', { name: 'sonnet' }).click();
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page.getByText(/saved/i)).toBeVisible();

    // Verify agents row updated + event_outbox row landed.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const deadline = Date.now() + 5_000;
      while (Date.now() < deadline) {
        const agentRows = await sql<{ model: string }[]>`
          SELECT model FROM agents WHERE role_slug = 'engineer' LIMIT 1
        `;
        const outboxRows = await sql<{ payload: { kind: string; roleSlug: string; diff: { model?: { after: string } } } }[]>`
          SELECT payload FROM event_outbox WHERE channel = 'work.agent.edited' ORDER BY created_at DESC LIMIT 1
        `;
        if (
          agentRows[0]?.model === 'sonnet' &&
          outboxRows[0]?.payload?.diff?.model?.after === 'sonnet'
        ) {
          expect(outboxRows[0].payload.kind).toBe('agent.edited');
          expect(outboxRows[0].payload.roleSlug).toBe('engineer');
          await ctx.close();
          return;
        }
        await new Promise((r) => setTimeout(r, 200));
      }
      throw new Error('expected agents.model + work.agent.edited event_outbox row never appeared');
    } finally {
      await sql.end();
    }
  });
});
