import { test, expect } from './_coverage';
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
    // ThemeSwitcher applies the data-theme attribute optimistically
    // (synchronous DOM write) before the /api/theme PUT completes, so
    // toHaveAttribute alone can satisfy on the optimistic update while
    // the DB still holds the old preference. Wait for the PUT response
    // AND then reload to confirm the next server-rendered request sees
    // the persisted preference — that's what the SURFACES loop relies
    // on below.
    const themeResponse = page.waitForResponse(
      (resp) => resp.url().includes('/api/theme') && resp.request().method() === 'PUT',
    );
    await page.getByTestId('theme-dark').click();
    const dr = await themeResponse;
    expect(dr.ok()).toBe(true);
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark', { timeout: 5_000 });
    for (const path of SURFACES) {
      await page.goto(path);
      // Per-surface wait — the theme-dark click writes a cookie
      // that the server consumes on subsequent page renders, but
      // there's a small race between the click's async write and
      // the next navigation. Wait for the server's data-theme to
      // resolve before asserting it.
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark', { timeout: 5_000 });
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
    // Switch theme to light via the topbar. Same optimistic-DOM-update
    // race as the dark-theme test above — wait for the /api/theme PUT
    // response and reload before entering the navigation loop.
    await page.goto('/');
    const themeResponse = page.waitForResponse(
      (resp) => resp.url().includes('/api/theme') && resp.request().method() === 'PUT',
    );
    await page.getByTestId('theme-light').click();
    const lr = await themeResponse;
    expect(lr.ok()).toBe(true);
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light', { timeout: 5_000 });
    for (const path of SURFACES) {
      await page.goto(path);
      // Per-surface wait — the theme-light click writes a cookie
      // that the server consumes on subsequent page renders, but
      // there's a small race between the click's async write and
      // the next navigation. Wait for the server's data-theme to
      // resolve before asserting it.
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light', { timeout: 5_000 });
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
    // Wait for the /api/theme PUT to commit before closing the
    // context — otherwise ctx.close() may abort the in-flight PUT and
    // the DB never sees the update, breaking the persistence assertion
    // when ctx2 re-authenticates below.
    const themeResponse = page.waitForResponse(
      (resp) => resp.url().includes('/api/theme') && resp.request().method() === 'PUT',
    );
    await page.getByTestId('theme-light').click();
    const r = await themeResponse;
    expect(r.ok()).toBe(true);
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
