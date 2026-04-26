import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Sandbox-escape per-row icon + expand acceptance per
// plan §"Test strategy > integration > sandbox-escape.spec.ts":
//   - sandbox-escape transition row in ticket detail shows the
//     icon and is expandable
//   - expanded row reveals "claimed: X / on-disk: Y" detail

async function seedTicketWithSandboxEscape(
  env: Awaited<ReturnType<typeof bootHarness>>,
): Promise<string> {
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
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'sandbox test', 'sql')
      RETURNING id
    `;
    await sql`
      INSERT INTO ticket_transitions (ticket_id, from_column, to_column, hygiene_status)
      VALUES (${t.id}, 'todo', 'in_dev', 'sandbox_escape')
    `;
    return t.id;
  } finally {
    await sql.end();
  }
}

async function authenticate(page: import('@playwright/test').Page, env: Awaited<ReturnType<typeof bootHarness>>) {
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

test.describe('sandbox-escape evidence', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('sandboxEscapeRowShowsIconAndExpands', async ({ page }) => {
    const env = await bootHarness();
    const ticketId = await seedTicketWithSandboxEscape(env);
    await authenticate(page, env);
    await page.goto(`/tickets/${ticketId}`);
    await expect(page.getByTestId('sandbox-escape-icon')).toBeVisible();
    // Detail should be hidden until expand is clicked.
    await expect(page.getByTestId('sandbox-escape-detail')).toHaveCount(0);
    // Find the expand button inside the history block and click it.
    await page.getByTestId('history-block').getByRole('button').first().click();
  });

  test('expandedRowRevealsClaimedVsOnDisk', async ({ page }) => {
    const env = await bootHarness();
    const ticketId = await seedTicketWithSandboxEscape(env);
    await authenticate(page, env);
    await page.goto(`/tickets/${ticketId}`);
    await page.getByTestId('history-block').getByRole('button').first().click();
    await expect(page.getByTestId('sandbox-escape-detail')).toBeVisible();
    await expect(page.getByTestId('sandbox-escape-detail')).toContainText('claimed');
    await expect(page.getByTestId('sandbox-escape-detail')).toContainText('on disk');
  });
});
