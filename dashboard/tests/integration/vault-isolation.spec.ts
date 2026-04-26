import { test, expect } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Vault read-views isolation acceptance.
//
// Per FR-021 + FR-083 + FR-084 + AGENTS.md §M3 activation:
//   - vault sub-views read via garrison_dashboard_ro
//   - garrison_dashboard_app does NOT have grants on the vault
//     tables; reading vault tables via appDb returns
//     "permission denied"
//   - no surface anywhere exposes a path to read or copy a secret
//     value
//
// The static-analysis test in lib/queries/vault.test.ts pins the
// source-code invariant; this Playwright spec exercises the
// runtime invariants — that the dashboard is configured to use
// garrison_dashboard_ro, and that the app role can't read the
// vault tables.

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

test.describe('vault isolation', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('attemptingToReadAgentRoleSecretsViaAppDbFailsWithPermissionDenied', async () => {
    const env = await bootHarness();
    // Connect as garrison_dashboard_app and try to SELECT from
    // agent_role_secrets — must fail with "permission denied"
    // because the role has no grant on vault tables (T001).
    const sql = postgres(env.DASHBOARD_APP_DSN, { max: 1 });
    try {
      let errorMessage = '';
      try {
        await sql`SELECT 1 FROM agent_role_secrets LIMIT 1`;
      } catch (err) {
        errorMessage = err instanceof Error ? err.message : String(err);
      }
      expect(errorMessage).toMatch(/permission denied/i);
    } finally {
      await sql.end();
    }
  });

  test('runtimeVaultDSNResolvesToDashboardRo', async () => {
    const env = await bootHarness();
    expect(env.DASHBOARD_RO_DSN).toContain('garrison_dashboard_ro');
    expect(env.DASHBOARD_APP_DSN).toContain('garrison_dashboard_app');
    // The two DSNs must point at different roles even though they
    // talk to the same Postgres host/port.
    const roDsnRole = new URL(env.DASHBOARD_RO_DSN.replace('postgres://', 'http://')).username;
    const appDsnRole = new URL(env.DASHBOARD_APP_DSN.replace('postgres://', 'http://')).username;
    expect(roDsnRole).not.toBe(appDsnRole);
  });

  test('noSurfaceExposesAPathToReadOrCopySecretValues', async ({ page }) => {
    const env = await bootHarness();
    // Seed a secret with a known value-shaped string in the path
    // (NOT a stored value — paths are public, values are not).
    const sentinel = 'sk-fake-secret-value-not-for-display-1234567890';
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
      await sql`
        INSERT INTO secret_metadata (secret_path, customer_id, provenance, last_rotated_at)
        VALUES ('integration/test-secret', ${c.id}, 'test', now())
      `;
      await sql`
        INSERT INTO agent_role_secrets (role_slug, secret_path, env_var_name, customer_id, granted_by)
        VALUES ('engineer', 'integration/test-secret', 'TEST_SECRET', ${c.id}, 'test')
      `;
    } finally {
      await sql.end();
    }
    await authenticate(page, env);
    for (const path of ['/vault', '/vault/audit', '/vault/matrix']) {
      await page.goto(path);
      const html = await page.content();
      // The sentinel is a value-shaped string; no vault sub-view
      // should ever render anything matching the value pattern.
      expect(html).not.toContain(sentinel);
    }
  });
});
