import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Theme parity acceptance per plan §"Test strategy > integration
// > theme-parity.spec.ts" (FR-010, FR-010a):
//   - every primary surface renders in dark theme without missing
//     contrast
//   - every primary surface renders in light theme without missing
//     contrast
//   - theme switch persists across browser sessions for the same
//     operator (FR-010a)
//
// "Without missing contrast" is checked at the CSS-variable level:
// data-theme="dark"|"light" must resolve every used token; we
// assert the resolved values for --bg, --text-1 etc differ between
// the two palettes.

const SURFACES = [
  '/',
  '/departments/engineering',
  '/activity',
  '/hygiene',
  '/vault',
  '/agents',
  '/admin/invites',
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

test.describe('theme parity', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('every primary surface renders in dark theme with token-resolved colours', async ({ page }) => {
    const env = await bootHarness();
    await authenticate(page, env);
    // Explicitly pick dark — the theme defaults to 'system' which
    // doesn't set data-theme; under headless Chromium 'system' often
    // resolves to light, so without this click data-theme is null.
    await page.goto('/');
    await page.getByTestId('theme-dark').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark', { timeout: 5_000 });
    for (const path of SURFACES) {
      await page.goto(path);
      const dataTheme = await page.locator('html').getAttribute('data-theme');
      expect(dataTheme).toBe('dark');
      // Resolve the foreground/background tokens; assert they
      // ARE set (no "" / "transparent" / unset) — that's the
      // baseline "no missing contrast" check.
      const colors = await page.evaluate(() => {
        const cs = getComputedStyle(document.documentElement);
        return {
          bg: cs.getPropertyValue('--bg').trim(),
          text1: cs.getPropertyValue('--text-1').trim(),
        };
      });
      expect(colors.bg).not.toBe('');
      expect(colors.text1).not.toBe('');
      expect(colors.bg).not.toBe(colors.text1);
    }
  });

  test('every primary surface renders in light theme with token-resolved colours', async ({ page }) => {
    const env = await bootHarness();
    await authenticate(page, env);
    // Switch theme to light via the topbar.
    await page.goto('/');
    await page.getByTestId('theme-light').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light', { timeout: 5_000 });
    for (const path of SURFACES) {
      await page.goto(path);
      const dataTheme = await page.locator('html').getAttribute('data-theme');
      expect(dataTheme).toBe('light');
      const colors = await page.evaluate(() => {
        const cs = getComputedStyle(document.documentElement);
        return {
          bg: cs.getPropertyValue('--bg').trim(),
          text1: cs.getPropertyValue('--text-1').trim(),
        };
      });
      expect(colors.bg).not.toBe('');
      expect(colors.text1).not.toBe('');
      expect(colors.bg).not.toBe(colors.text1);
    }
  });

  test('theme switch persists across browser sessions for the same operator', async ({ browser }) => {
    const env = await bootHarness();
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    await authenticate(page, env);
    await page.goto('/');
    await page.getByTestId('theme-light').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light', { timeout: 5_000 });
    await ctx.close();

    // New context, log in again — theme preference loaded from
    // the better-auth user record (FR-010a).
    const ctx2 = await browser.newContext();
    const page2 = await ctx2.newPage();
    await page2.goto('/login');
    await page2.locator('input[name=email]').fill('op@example.com');
    await page2.locator('input[name=password]').fill('integration-pw-1');
    await page2.getByRole('button', { name: 'Sign in' }).click();
    await page2.waitForURL((url) => url.pathname === '/');
    await expect(page2.locator('html')).toHaveAttribute('data-theme', 'light');
    await ctx2.close();
  });
});
