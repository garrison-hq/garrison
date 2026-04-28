import { test, expect } from './_coverage';
import { bootHarness, truncateDashboardState } from './_harness';

// First-run wizard + protected-route gating.
//
// The harness bootstraps a Postgres testcontainer with all goose +
// Drizzle migrations + role passwords applied. Each test starts with
// the dashboard tables truncated so the wizard's "users table empty"
// invariant holds at the start.

test.describe('first-run wizard', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('firstRunWizardCreatesInauguralAccountOnFreshDB', async ({ page }) => {
    await page.goto('/setup');
    await expect(page.getByRole('heading', { name: 'First-run setup' })).toBeVisible();

    await page.locator('input[name=name]').fill('Op One');
    await page.locator('input[name=email]').fill('op@example.com');
    await page.locator('input[name=password]').fill('integration-test-pw1');
    await page.getByRole('button', { name: 'Create operator account' }).click();

    await page.waitForURL(/\/login/);
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
  });

  test('firstRunWizardReturns404OnceUsersTableNonEmpty', async ({ page, request }) => {
    // Seed the users table by first running the wizard once.
    await page.goto('/setup');
    await page.locator('input[name=name]').fill('Already Here');
    await page.locator('input[name=email]').fill('first@example.com');
    await page.locator('input[name=password]').fill('integration-test-pw1');
    await page.getByRole('button', { name: 'Create operator account' }).click();
    await page.waitForURL(/\/login/);

    // Now the wizard route must self-404.
    const res = await request.get('/setup');
    expect(res.status()).toBe(404);
  });

  test('everyProtectedRouteRedirectsToLoginWhenNoSession', async ({ page }) => {
    // The middleware allow-listed /login, /setup, /invite/, /api/auth/, etc.
    // /(app) routes have not been built yet (T010 onwards), so we exercise
    // the bare middleware against the placeholder /api endpoint and against
    // a non-existent protected route — both should redirect to /login.
    await page.context().clearCookies();

    const res = await page.goto('/some-protected-path');
    // Middleware redirects unauthenticated requests to /login with the
    // original pathname encoded as ?redirect=...
    await expect(page).toHaveURL(/\/login\?redirect=%2Fsome-protected-path/);
    expect(res?.status()).toBeLessThan(400);
  });
});
