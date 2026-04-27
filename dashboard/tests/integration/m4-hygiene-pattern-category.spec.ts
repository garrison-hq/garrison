import { test, expect, type BrowserContext } from '@playwright/test';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 / T015 — hygiene pattern-category column + filter chip.
//
// Server side (commit f0c3bc8): scanAndRedactPayload writes the
// matched supervisor pattern label to
// ticket_transitions.suspected_secret_pattern_category. Dashboard
// query layer (commit e53ffad): fetchHygieneRows reads that
// column and renders 'unknown' for pre-M4 (NULL) rows per
// FR-118. Filter chip (commit 251d12c): URL-state ?category=...
// narrows the table; setting a category implies the
// suspected_secret_emitted bucket.

async function seedSecretEmittedRow(
  env: HarnessEnv,
  patternCategory: string | null,
  objectiveTag: string,
): Promise<void> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-hygiene') RETURNING id
    `;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 1, '/tmp')
      RETURNING id
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', ${objectiveTag}, 'sql')
      RETURNING id
    `;
    await sql`
      INSERT INTO ticket_transitions (
        ticket_id, from_column, to_column, hygiene_status,
        suspected_secret_pattern_category
      )
      VALUES (
        ${t.id}, 'todo', 'in_dev', 'suspected_secret_emitted',
        ${patternCategory}
      )
    `;
  } finally {
    await sql.end();
  }
}

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('M4 Hygiene Op');
  await page.locator('input[name=email]').fill('hygiene@example.com');
  await page.locator('input[name=password]').fill('m4-hygiene-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('hygiene@example.com');
  await page.locator('input[name=password]').fill('m4-hygiene-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

test.describe('M4 hygiene pattern-category filter', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('FR-117 + FR-118: known category renders as the label, NULL renders as "unknown"', async ({
    browser,
  }) => {
    const env = await bootHarness();
    await seedSecretEmittedRow(env, 'sk_prefix', 'has-category');
    await seedSecretEmittedRow(env, null, 'pre-m4-row');

    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();
    await page.goto('/hygiene');

    // Both rows are visible.
    await expect(page.getByText('has-category')).toBeVisible();
    await expect(page.getByText('pre-m4-row')).toBeVisible();

    // Filter chip exists for the suspected_secret_emitted view
    // (rendering only when failure-mode is unset OR set to that
    // bucket).
    await expect(page.getByTestId('pattern-category-filter')).toBeVisible();
    await ctx.close();
  });

  test('filter chip narrows by category and persists in URL', async ({ browser }) => {
    const env = await bootHarness();
    await seedSecretEmittedRow(env, 'sk_prefix', 'sk-row');
    await seedSecretEmittedRow(env, 'aws_akia', 'aws-row');

    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();
    await page.goto('/hygiene');

    // Click sk_prefix; only that row remains.
    await page.getByTestId('category-sk_prefix').click();
    await page.waitForURL(/category=sk_prefix/);
    await expect(page.getByText('sk-row')).toBeVisible();
    await expect(page.getByText('aws-row')).not.toBeVisible();
    await ctx.close();
  });
});
