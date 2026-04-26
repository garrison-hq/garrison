import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Cost-blind-spot caveat acceptance.
// Two scenarios per plan §"Test strategy > integration >
// cost-blind-spot.spec.ts":
//   - clean-finalize zero-cost row in ticket detail shows the
//     caveat icon
//   - non-zero cost rows do not show the caveat icon

async function seedTicket(env: Awaited<ReturnType<typeof bootHarness>>, opts: {
  cost: number | null;
  exitReason: string;
}): Promise<string> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'eng', 'Eng', 1, '/tmp')
      RETURNING id
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'cost test', 'sql')
      RETURNING id
    `;
    await sql`
      INSERT INTO agent_instances (department_id, ticket_id, role_slug, status, exit_reason, total_cost_usd, started_at, finished_at)
      VALUES (${d.id}, ${t.id}, 'engineer', 'finished', ${opts.exitReason}, ${opts.cost}, now() - interval '1 minute', now())
    `;
    return t.id;
  } finally {
    await sql.end();
  }
}

async function authenticate(page: import('@playwright/test').Page, env: Awaited<ReturnType<typeof bootHarness>>) {
  // Seed an inaugural operator and sign in via the UI.
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const usersCount = await sql<{ count: string }[]>`SELECT count(*) FROM users`;
    if (Number(usersCount[0].count) === 0) {
      await fetch(`${env.BETTER_AUTH_URL}/api/auth/sign-up/email`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Origin: env.BETTER_AUTH_URL },
        body: JSON.stringify({ email: 'op@example.com', password: 'integration-pw-1', name: 'Op' }),
      });
    }
  } finally {
    await sql.end();
  }
  await page.goto('/login');
  await page.locator('input[name=email]').fill('op@example.com');
  await page.locator('input[name=password]').fill('integration-pw-1');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
}

test.describe('cost-blind-spot caveat', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('cleanFinalizeZeroCostShowsCaveat', async ({ page }) => {
    const env = await bootHarness();
    const ticketId = await seedTicket(env, { cost: 0, exitReason: 'finalize_committed' });
    await authenticate(page, env);
    await page.goto(`/tickets/${ticketId}`);
    await expect(page.getByTestId('cost-caveat-icon')).toBeVisible();
  });

  test('nonZeroCostRowsDoNotShowCaveat', async ({ page }) => {
    const env = await bootHarness();
    const ticketId = await seedTicket(env, { cost: 0.0123, exitReason: 'finalize_committed' });
    await authenticate(page, env);
    await page.goto(`/tickets/${ticketId}`);
    await expect(page.getByTestId('agent-instance-row')).toBeVisible();
    await expect(page.getByTestId('cost-caveat-icon')).toHaveCount(0);
  });
});
