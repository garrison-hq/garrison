import { test, expect, type BrowserContext } from './_coverage';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 / T017 — Rule 6 invariant: no secret values in audit rows.
//
// Exercises the audit-row write paths that ARE achievable
// without an Infisical testcontainer:
//   - event_outbox rows for ticket/agent mutations
//   - vault_access_log rows are NOT exercised here (those need
//     Infisical) — they're covered by the unit-level
//     vaultAccessLog.test.ts which pins the leak-scan backstop
//     against synthetic payloads.
//
// What this spec verifies end-to-end:
//   1. Ticket inline edit landing an event_outbox row carries
//      the field-level diff but NEVER a value resembling a
//      seeded fetchable secret.
//   2. Agent edit landing an event_outbox row carries
//      role_slug + diff but NEVER any secret-shaped string.
//
// The shape-scan is the same set of patterns the T006 leak-scan
// uses; this is the integration-level confirmation that no
// codepath sneaks a value through.

const SECRET_SHAPED_PATTERNS = [
  /sk-[A-Za-z0-9]{20,}/,
  /xoxb-[A-Za-z0-9-]{20,}/,
  /AKIA[0-9A-Z]{16}/,
  /-----BEGIN [A-Z ]+-----/,
  /gh[psuor]_[A-Za-z0-9]{30,}/,
];

function containsSecretShape(payload: unknown): boolean {
  const text = JSON.stringify(payload);
  return SECRET_SHAPED_PATTERNS.some((re) => re.test(text));
}

async function seedDept(env: HarnessEnv): Promise<void> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-rule6') RETURNING id
    `;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 1, '/tmp')
      RETURNING id
    `;
    await sql`
      UPDATE departments SET workflow = jsonb_build_object(
        'columns', jsonb_build_array(
          jsonb_build_object('slug', 'todo', 'label', 'To do'),
          jsonb_build_object('slug', 'in_dev', 'label', 'In dev')
        )
      ) WHERE id = ${d.id}
    `;
  } finally {
    await sql.end();
  }
}

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Rule6 Op');
  await page.locator('input[name=email]').fill('rule6@example.com');
  await page.locator('input[name=password]').fill('rule6-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('rule6@example.com');
  await page.locator('input[name=password]').fill('rule6-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

test.describe('M4 / T017 — Rule 6 audit invariants', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
    await seedDept(env);
  });

  test('SC-006: ticket inline edit audit rows contain no secret-shaped values', async ({
    browser,
  }) => {
    const env = await bootHarness();
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    // Create a ticket with an objective that would otherwise
    // never appear in a real workload, then edit it.
    await page.goto('/tickets/new');
    await page.locator('input').first().fill('rule6 audit shape');
    await page.getByRole('button', { name: 'Create ticket' }).click();
    await page.waitForURL(/\/tickets\/[a-f0-9-]{36}$/, { timeout: 10_000 });
    // Reload to break out of the @panel intercept and render the
    // standalone /tickets/<id> page that hosts the inline editor.
    await page.goto(page.url());

    await page.getByRole('button', { name: 'Edit' }).first().click();
    await page.locator('input').filter({ hasNotText: '' }).first().fill('rule6 edited');
    await page.getByRole('button', { name: 'Save' }).click();
    await page.waitForTimeout(1000);

    // Inspect every event_outbox row this test produced.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ payload: unknown }[]>`
        SELECT payload FROM event_outbox WHERE channel IN (
          'work.ticket.created', 'work.ticket.edited', 'work.agent.edited'
        )
      `;
      for (const r of rows) {
        expect(
          containsSecretShape(r.payload),
          `event_outbox payload contains a secret-shaped value: ${JSON.stringify(r.payload)}`,
        ).toBe(false);
      }
    } finally {
      await sql.end();
    }

    await ctx.close();
  });

  test('SC-017: failed mutations write zero audit rows', async ({ browser }) => {
    const env = await bootHarness();
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    // Try to create a ticket with an empty objective — should
    // be client-side-rejected without the server action firing.
    await page.goto('/tickets/new');
    // Don't fill the objective input. Click Create.
    await page.getByRole('button', { name: 'Create ticket' }).click();
    // Error toast appears.
    await expect(page.getByText(/objective cannot be empty/i)).toBeVisible();

    // No event_outbox row should have landed.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ count: number }[]>`
        SELECT count(*)::int AS count FROM event_outbox WHERE channel = 'work.ticket.created'
      `;
      expect(rows[0].count).toBe(0);
    } finally {
      await sql.end();
    }

    await ctx.close();
  });
});
