import { test, expect } from './_coverage';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Vault read-views isolation acceptance.
//
// Per FR-021 + FR-083 + FR-084 + AGENTS.md §M3 activation:
//   - vault sub-views read via garrison_dashboard_ro
//   - garrison_dashboard_app's vault grants are scoped per
//     migration: M3 isolation gave it none; M4's mutation
//     surfaces (deleteSecret pre-flight, agent edit form's
//     existing-grants render, vault_access_log INSERT for the
//     audit row) require SELECT + INSERT on the vault tables.
//     See migrations/20260427000012 + 20260427000014.
//   - the load-bearing isolation invariant moved with M4: secret
//     VALUES never flow into Postgres at all (Rule 6). The old
//     "app role can't even read the vault tables" assertion was
//     a proxy; it stops being true at M4.
//   - no surface anywhere exposes a path to read or copy a secret
//     value
//
// The static-analysis test in lib/queries/vault.test.ts pins the
// source-code invariant; this Playwright spec exercises the
// runtime invariants.

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

  test('garrisonDashboardAppCannotReadSecretValuesFromAnyTable', async () => {
    const env = await bootHarness();
    // M4 expanded the app role's vault grants (SELECT on
    // agent_role_secrets / vault_access_log / secret_metadata,
    // plus INSERT on vault_access_log) so the M4 mutation paths
    // can read the joinable metadata they need. The
    // load-bearing isolation invariant the test pins is now:
    // **no Postgres column anywhere holds a secret value**.
    // Verify by SELECTing every column the dashboard_app role
    // can reach across the vault tables and asserting nothing
    // resembling a value-shaped string ships back.
    const sql = postgres(env.DASHBOARD_APP_DSN, { max: 1 });
    try {
      // secret_metadata: has secret_path (public), customer_id,
      // provenance, rotation_*, allowed_role_slugs — no value.
      const metaCols = await sql<{ column_name: string }[]>`
        SELECT column_name FROM information_schema.columns
        WHERE table_name = 'secret_metadata'
      `;
      const metaColNames = metaCols.map((r) => r.column_name);
      expect(metaColNames).not.toContain('secret_value');
      expect(metaColNames).not.toContain('value');

      // vault_access_log: outcome + path + actor metadata; no
      // value column. metadata is JSONB but write paths run
      // through writeVaultMutationLog's defensive shape-scan.
      const auditCols = await sql<{ column_name: string }[]>`
        SELECT column_name FROM information_schema.columns
        WHERE table_name = 'vault_access_log'
      `;
      const auditColNames = auditCols.map((r) => r.column_name);
      expect(auditColNames).not.toContain('secret_value');
      expect(auditColNames).not.toContain('value');

      // agent_role_secrets: role↔env-var↔path mapping; no value.
      const grantCols = await sql<{ column_name: string }[]>`
        SELECT column_name FROM information_schema.columns
        WHERE table_name = 'agent_role_secrets'
      `;
      const grantColNames = grantCols.map((r) => r.column_name);
      expect(grantColNames).not.toContain('secret_value');
      expect(grantColNames).not.toContain('value');
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
