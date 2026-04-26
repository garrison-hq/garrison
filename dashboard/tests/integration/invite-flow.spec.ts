import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// End-to-end invite flow.
//
// Each test starts with the dashboard tables truncated, an inaugural
// operator already created (so the wizard is locked), and the
// inaugural operator logged into a Playwright browser context. From
// there, the test exercises invite generation, redemption, revoke,
// expiry, and the concurrent-redemption race.

async function seedInauguralOperator(env: Awaited<ReturnType<typeof bootHarness>>): Promise<{
  email: string;
  password: string;
}> {
  const credentials = {
    email: 'op-one@example.com',
    password: 'integration-pw-1',
  };
  const res = await fetch(`${env.BETTER_AUTH_URL}/api/auth/sign-up/email`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      // Origin header so better-auth's trusted-origin check
      // accepts the request — Node fetch doesn't set one by default.
      Origin: env.BETTER_AUTH_URL,
    },
    body: JSON.stringify({ ...credentials, name: 'Op One' }),
  });
  if (!res.ok) {
    throw new Error(`seed inaugural operator failed: HTTP ${res.status}`);
  }
  return credentials;
}

test.describe('invite flow', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
    await seedInauguralOperator(env);
  });

  async function login(page: import('@playwright/test').Page, email: string, password: string) {
    await page.goto('/login');
    await page.locator('input[name=email]').fill(email);
    await page.locator('input[name=password]').fill(password);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await page.waitForURL((url) => url.pathname === '/');
  }

  test('authenticated operator can generate an invite link from /admin/invites', async ({ page }) => {
    await login(page, 'op-one@example.com', 'integration-pw-1');
    await page.goto('/admin/invites');
    await page.getByTestId('generate-invite').click();
    // Page reloads after generate; the new invite link appears.
    await expect(page.getByTestId('invite-link').first()).toBeVisible({ timeout: 10_000 });
  });

  test('pending invite appears in the operator’s invite list', async ({ page }) => {
    await login(page, 'op-one@example.com', 'integration-pw-1');
    await page.goto('/admin/invites');
    await page.getByTestId('generate-invite').click();
    await expect(page.getByTestId('invite-link').first()).toBeVisible({ timeout: 10_000 });
    const link = await page.getByTestId('invite-link').first().textContent();
    expect(link).toContain('/invite/');
  });

  test('invitee can redeem the link and lands logged in as a peer operator', async ({
    browser,
  }) => {
    // Operator A: log in, generate invite, capture URL.
    const ctxA = await browser.newContext();
    const pageA = await ctxA.newPage();
    await pageA.goto('/login');
    await pageA.locator('input[name=email]').fill('op-one@example.com');
    await pageA.locator('input[name=password]').fill('integration-pw-1');
    await pageA.getByRole('button', { name: 'Sign in' }).click();
    await pageA.waitForURL((url) => url.pathname === '/');
    await pageA.goto('/admin/invites');
    await pageA.getByTestId('generate-invite').click();
    await expect(pageA.getByTestId('invite-link').first()).toBeVisible({ timeout: 10_000 });
    const link = (await pageA.getByTestId('invite-link').first().textContent()) ?? '';
    expect(link).toContain('/invite/');

    // Operator B: clean context, redeem the link, expect to land on /.
    const ctxB = await browser.newContext();
    const pageB = await ctxB.newPage();
    await pageB.goto(link);
    await pageB.locator('input[name=name]').fill('Op Two');
    await pageB.locator('input[name=email]').fill('op-two@example.com');
    await pageB.locator('input[name=password]').fill('integration-pw-2');
    await pageB.getByRole('button', { name: 'Create account' }).click();
    await pageB.waitForURL((url) => url.pathname === '/');

    await ctxA.close();
    await ctxB.close();
  });

  test('revoked invite link returns InviteRevoked on redemption attempt', async ({ page, browser }) => {
    await login(page, 'op-one@example.com', 'integration-pw-1');
    await page.goto('/admin/invites');
    await page.getByTestId('generate-invite').click();
    await expect(page.getByTestId('invite-link').first()).toBeVisible({ timeout: 10_000 });
    const link = (await page.getByTestId('invite-link').first().textContent()) ?? '';
    await page.getByTestId('revoke-invite').first().click();
    await expect(page.getByTestId('invite-link')).toHaveCount(0, { timeout: 10_000 });

    // Attempt redemption with the now-revoked link via API.
    const env = await bootHarness();
    const res = await fetch(`${env.BETTER_AUTH_URL}/api/invites/redeem`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Origin: env.BETTER_AUTH_URL,
      },
      body: JSON.stringify({
        token: link.split('/invite/')[1],
        email: 'should-fail@example.com',
        name: 'Nope',
        password: 'integration-pw-3',
      }),
    });
    expect(res.status).toBe(410);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe('invite_revoked');
  });

  test('expired invite link returns InviteExpired on redemption attempt', async () => {
    const env = await bootHarness();
    // Insert an already-expired invite directly.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    let token: string;
    try {
      // Need an inviter user_id; the inaugural operator already exists.
      const inviter = await sql<{ id: string }[]>`SELECT id FROM users LIMIT 1`;
      const created = await sql<{ token: string }[]>`
        INSERT INTO operator_invites (token, created_by_user_id, expires_at)
        VALUES ('expired-token-' || gen_random_uuid(), ${inviter[0].id},
                now() - interval '1 hour')
        RETURNING token
      `;
      token = created[0].token;
    } finally {
      await sql.end();
    }

    const res = await fetch(`${env.BETTER_AUTH_URL}/api/invites/redeem`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Origin: env.BETTER_AUTH_URL,
      },
      body: JSON.stringify({
        token,
        email: 'expired-test@example.com',
        name: 'Nope',
        password: 'integration-pw-3',
      }),
    });
    expect(res.status).toBe(410);
    const body = (await res.json()) as { error?: string };
    expect(body.error).toBe('invite_expired');
  });

  test('concurrent redemptions of the same invite produce exactly one successful account', async () => {
    const env = await bootHarness();
    // Generate an invite directly via the in-process helper so the
    // test doesn't have to drive the UI for setup.
    const { generateInvite } = await import('@/lib/auth/invites');
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    let inviterId: string;
    try {
      const rows = await sql<{ id: string }[]>`SELECT id FROM users LIMIT 1`;
      inviterId = rows[0].id;
    } finally {
      await sql.end();
    }
    const { token } = await generateInvite(inviterId);

    const callRedeem = (suffix: string) =>
      fetch(`${env.BETTER_AUTH_URL}/api/invites/redeem`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Origin: env.BETTER_AUTH_URL,
        },
        body: JSON.stringify({
          token,
          email: `race-${suffix}@example.com`,
          name: `Race ${suffix}`,
          password: 'integration-pw-3',
        }),
      });

    const [resA, resB] = await Promise.all([callRedeem('a'), callRedeem('b')]);
    const statuses = [resA.status, resB.status].sort();
    expect(statuses).toEqual([200, 410]);
  });
});
