import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Responsive acceptance per plan §"Test strategy > integration >
// responsive.spec.ts" (FR-011 + SC-003):
//   - every primary surface renders without horizontal page
//     scroll at 768px
//   - same at 1024px
//   - same at 1280px+
//
// We use Playwright's project config (playwright.config.ts) to run
// each viewport as a separate run — but for THIS spec we set the
// viewport explicitly per test for clarity.

const SURFACES = [
  '/',
  '/departments/engineering',
  '/activity',
  '/hygiene',
  '/vault',
  '/agents',
  '/admin/invites',
];

const VIEWPORTS = [
  { name: '768px', width: 768, height: 1024 },
  { name: '1024px', width: 1024, height: 768 },
  { name: '1280px', width: 1280, height: 800 },
];

async function authenticate(
  page: import('@playwright/test').Page,
  env: Awaited<ReturnType<typeof bootHarness>>,
) {
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

test.describe('responsive', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  for (const viewport of VIEWPORTS) {
    test(`every primary surface renders without horizontal page scroll at ${viewport.name}`, async ({ page }) => {
      const env = await bootHarness();
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await authenticate(page, env);
      for (const path of SURFACES) {
        await page.goto(path);
        const overflow = await page.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }));
        // Allow a 1px rounding tolerance.
        expect(
          overflow.scrollWidth,
          `${path} at ${overflow.clientWidth}px viewport overflowed (scrollWidth=${overflow.scrollWidth})`,
        ).toBeLessThanOrEqual(overflow.clientWidth + 1);
      }
    });
  }
});
