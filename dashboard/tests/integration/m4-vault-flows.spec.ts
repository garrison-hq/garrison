import { test, expect, type BrowserContext } from './_coverage';
import postgres from 'postgres';
import {
  bootHarness,
  ensureInfisicalFolder,
  truncateDashboardState,
  type HarnessEnv,
} from './_harness';

// M4 / post-ship coverage spec — exercises the M4 vault Client
// Components that the existing specs left uncovered:
//
//   SecretRowActions  — Reveal + Delete row buttons + ConfirmDialog
//                       typed-name confirm
//   RevealModal       — value fetched + auto-hide + DOM purge
//   SecretEditForm    — value + provenance + cadence edit
//   GrantEditor       — add grant + remove grant + ConfirmDialog
//                       single-click confirm
//   RotationButton    — manual_paste rotate flow
//
// Each flow runs against a real Infisical testcontainer (the same
// one /vault/new uses; we seed an extra Infisical-side secret +
// secret_metadata row up front so Reveal/Edit/Delete have
// something to act on without going through the create-secret
// form again).

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Vault Op');
  await page.locator('input[name=email]').fill('vault@example.com');
  await page.locator('input[name=password]').fill('m4-vault-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('vault@example.com');
  await page.locator('input[name=password]').fill('m4-vault-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

async function seedCompanyAndAgent(env: HarnessEnv): Promise<{ customerId: string; secretPath: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-vault') RETURNING id
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
    return { customerId: c.id, secretPath: `/${c.id}/operator/SEEDED_KEY` };
  } finally {
    await sql.end();
  }
}

/**
 * Inject a secret straight into Infisical + secret_metadata so the
 * subsequent flows have a row to Reveal / Edit / Delete without
 * routing through the create-secret form (that's already covered
 * by m4-golden-path).
 */
async function seedSecretInBoth(env: HarnessEnv, customerId: string): Promise<void> {
  // Pre-create the folder structure + the secret in Infisical.
  await ensureInfisicalFolder(env, `/${customerId}/operator`);
  const loginRes = await fetch(`${env.INFISICAL_SITE_URL}/api/v1/auth/universal-auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      clientId: env.INFISICAL_DASHBOARD_ML_CLIENT_ID,
      clientSecret: env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET,
    }),
  });
  const { accessToken } = (await loginRes.json()) as { accessToken: string };
  await fetch(`${env.INFISICAL_SITE_URL}/api/v3/secrets/raw/SEEDED_KEY`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${accessToken}`,
    },
    body: JSON.stringify({
      workspaceId: env.INFISICAL_DASHBOARD_PROJECT_ID,
      environment: env.INFISICAL_DASHBOARD_ENVIRONMENT,
      secretPath: `/${customerId}/operator`,
      secretValue: 'seeded-secret-value-not-leaked-anywhere',
    }),
  });
  // Mirror in secret_metadata so the dashboard's read paths see it.
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await sql`
      INSERT INTO secret_metadata
        (secret_path, customer_id, provenance, rotation_cadence, rotation_provider, allowed_role_slugs)
      VALUES
        (${`/${customerId}/operator/SEEDED_KEY`}, ${customerId}, 'operator_entered',
         '90 days', 'manual_paste', '{}'::text[])
    `;
  } finally {
    await sql.end();
  }
}

test.describe('M4 vault flows — coverage for under-covered Client Components', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('reveal + delete flow exercises SecretRowActions + RevealModal + ConfirmDialog typed-name', async ({
    browser,
  }) => {
    const env = await bootHarness();
    const { customerId, secretPath } = await seedCompanyAndAgent(env);
    await seedSecretInBoth(env, customerId);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    // --- Reveal ---
    await page.goto('/vault');
    const row = page.getByTestId('secret-row').first();
    await expect(row).toBeVisible();
    await row.getByRole('button', { name: 'Reveal' }).click();
    // The modal opens in confirm-prompt phase; click through to
    // trigger the fetch.
    await page.getByRole('button', { name: 'Reveal value' }).click();
    // After the fetch resolves, the value renders in a <pre>.
    await expect(page.getByText('seeded-secret-value-not-leaked-anywhere')).toBeVisible({
      timeout: 10_000,
    });
    // Close via the modal's Close button — the value should leave the DOM.
    await page.getByRole('button', { name: 'Close' }).click();
    await expect(page.getByText('seeded-secret-value-not-leaked-anywhere')).toHaveCount(0);

    // --- Delete (typed-name confirm) ---
    await row.getByRole('button', { name: 'Delete' }).click();
    // ConfirmDialog tier='typed-name' renders an input labeled
    // "Type <path> to confirm". Type the path, click Delete.
    const typedInput = page.locator('input[type=text]').last();
    await typedInput.fill(secretPath);
    // Scope to the dialog so we don't match the row's Delete button.
    await page.getByLabel('Delete secret').getByRole('button', { name: 'Delete' }).click();

    // Verify deletion landed.
    await expect
      .poll(async () => {
        const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
        try {
          const r = await sql<{ count: number }[]>`
            SELECT count(*)::int AS count FROM secret_metadata
            WHERE secret_path = ${secretPath}
          `;
          return r[0]?.count ?? 0;
        } finally {
          await sql.end();
        }
      }, { timeout: 10_000 })
      .toBe(0);

    await ctx.close();
  });

  test('edit secret exercises SecretEditForm value + cadence + DiffView path', async ({ browser }) => {
    const env = await bootHarness();
    const { customerId, secretPath } = await seedCompanyAndAgent(env);
    await seedSecretInBoth(env, customerId);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    // The edit route is /vault/edit/<path...>; the path in the URL
    // is the secret path with leading / stripped.
    await page.goto(`/vault/edit${secretPath}`);
    await expect(page.getByRole('heading', { name: 'Edit secret' })).toBeVisible();
    // Change the cadence (a metadata-only edit; no Infisical write).
    const cadenceField = page.locator('input[type=number]').first();
    await cadenceField.fill('45');
    await page.getByRole('button', { name: 'Save changes' }).click();

    await expect
      .poll(async () => {
        const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
        try {
          const r = await sql<{ rotation_cadence: string }[]>`
            SELECT rotation_cadence::text AS rotation_cadence
            FROM secret_metadata WHERE secret_path = ${secretPath}
          `;
          return r[0]?.rotation_cadence ?? '';
        } finally {
          await sql.end();
        }
      }, { timeout: 10_000 })
      .toMatch(/^45 days?$/);

    await ctx.close();
  });

  test('grant add + remove exercises GrantEditor + ConfirmDialog single-click', async ({
    browser,
  }) => {
    const env = await bootHarness();
    const { customerId } = await seedCompanyAndAgent(env);
    await seedSecretInBoth(env, customerId);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    await page.goto('/vault/matrix');
    await expect(page.getByRole('heading', { name: /role.*secret/i })).toBeVisible();
    // The role dropdown is the first <select>; engineer is the
    // only seeded agent role, so it's the first option.
    await page.locator('select').first().selectOption('engineer');
    await page.locator('input[placeholder=ENV_VAR_NAME]').fill('SEEDED_VAR');
    // Path dropdown — pick the seeded secret.
    await page.locator('select').last().selectOption({ index: 0 });
    await page.getByRole('button', { name: 'Add grant' }).click();
    // The new grant lands in the table below.
    await expect(page.getByText('SEEDED_VAR')).toBeVisible({ timeout: 5_000 });

    // Remove it. Click the row's Remove button → ConfirmDialog
    // tier='single-click' renders → Remove (no typed input).
    await page.getByRole('button', { name: 'Remove' }).first().click();
    await page
      .getByRole('button', { name: 'Remove', exact: true })
      .last()
      .click();
    await expect
      .poll(async () => {
        const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
        try {
          const r = await sql<{ count: number }[]>`
            SELECT count(*)::int AS count FROM agent_role_secrets
            WHERE env_var_name = 'SEEDED_VAR'
          `;
          return r[0]?.count ?? 0;
        } finally {
          await sql.end();
        }
      }, { timeout: 10_000 })
      .toBe(0);

    await ctx.close();
  });

  test('manual-paste rotation exercises RotationButton modal', async ({ browser }) => {
    const env = await bootHarness();
    const { customerId, secretPath } = await seedCompanyAndAgent(env);
    await seedSecretInBoth(env, customerId);

    // Backdate the seeded secret so the rotation page surfaces it
    // as overdue (rotation_cadence = 90 days, last_rotated_at >
    // 90 days ago).
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql`
        UPDATE secret_metadata
        SET last_rotated_at = now() - interval '120 days'
        WHERE secret_path = ${secretPath}
      `;
    } finally {
      await sql.end();
    }

    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    await page.goto('/vault/rotation');
    // Click Rotate on the overdue row → manual-paste modal opens
    // → paste a new value → confirm.
    await page.getByRole('button', { name: 'Rotate' }).first().click();
    await page.locator('textarea').fill('rotated-value-via-spec');
    await page.getByRole('button', { name: /rotate/i }).last().click();

    await expect
      .poll(async () => {
        const c = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
        try {
          const r = await c<{ count: number }[]>`
            SELECT count(*)::int AS count FROM vault_access_log
            WHERE secret_path = ${secretPath}
              AND outcome = 'rotation_completed'
          `;
          return r[0]?.count ?? 0;
        } finally {
          await c.end();
        }
      }, { timeout: 15_000 })
      .toBeGreaterThan(0);

    await ctx.close();
  });
});
