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
    // Get-or-create the engineering department so multiple calls
    // in the same spec don't violate departments_slug_key.
    const existing = await sql<{ id: string }[]>`
      SELECT id FROM departments WHERE slug = 'engineering' LIMIT 1
    `;
    let dId: string;
    if (existing.length > 0) {
      dId = existing[0].id;
    } else {
      const [c] = await sql<{ id: string }[]>`
        INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-hygiene') RETURNING id
      `;
      const [d] = await sql<{ id: string }[]>`
        INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
        VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 1, '/tmp')
        RETURNING id
      `;
      dId = d.id;
    }
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${dId}, 'in_dev', ${objectiveTag}, 'sql')
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

    // Both seeded rows render in the hygiene table — the page
    // shows the truncated ticket id in the 'ticket' column, not
    // the objective; assert via the pattern-category cell text
    // (FR-117 = the label renders verbatim; FR-118 = NULL → 'unknown').
    await expect(page.getByText('sk_prefix').first()).toBeVisible();
    await expect(page.getByText('unknown').first()).toBeVisible();

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

    // Click sk_prefix; only that row remains. Assert via the
    // hygiene-row count + the surviving row's category cell text.
    // (Looking for 'aws_akia' anywhere on the page would still match
    // the always-rendered filter chip; scope to the data table.)
    await page.getByTestId('category-sk_prefix').click();
    await page.waitForURL(/category=sk_prefix/);
    await expect(page.getByTestId('hygiene-row')).toHaveCount(1);
    const tableText = await page.getByRole('table').textContent();
    expect(tableText).toContain('sk_prefix');
    expect(tableText).not.toContain('aws_akia');
    await ctx.close();
  });
});
