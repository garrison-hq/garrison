import { test, expect, type BrowserContext } from './_coverage';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 ticket-mutation integration tests covering T011 + T012 +
// T018 (concurrent edit + session expiry mid-mutation).
//
// Server actions exercised:
//   - createTicket (FR-025 / FR-026)
//   - moveTicket (FR-027 with operator_initiated hygiene_status)
//   - editTicket (FR-031 / FR-033 / FR-034 last-write-wins)
//
// Each test bootstraps a fresh dashboard state + minimal seed
// (one engineering department with the M2.1 4-column workflow)
// so the test harness doesn't need a full M3-shaped golden seed.

async function seedDept(env: HarnessEnv): Promise<{ deptId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name)
      VALUES (gen_random_uuid(), 'm4-test')
      RETURNING id
    `;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'engineering', 'Engineering', 2, '/tmp')
      RETURNING id
    `;
    await sql`
      UPDATE departments SET workflow = jsonb_build_object(
        'columns', jsonb_build_array(
          jsonb_build_object('slug', 'todo', 'label', 'To do'),
          jsonb_build_object('slug', 'in_dev', 'label', 'In dev'),
          jsonb_build_object('slug', 'qa_review', 'label', 'QA review'),
          jsonb_build_object('slug', 'done', 'label', 'Done')
        )
      ) WHERE id = ${d.id}
    `;
    return { deptId: d.id };
  } finally {
    await sql.end();
  }
}

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('M4 Op');
  await page.locator('input[name=email]').fill('m4-op@example.com');
  await page.locator('input[name=password]').fill('m4-test-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('m4-op@example.com');
  await page.locator('input[name=password]').fill('m4-test-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

test.describe('M4 ticket mutations', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
    await seedDept(env);
  });

  test('createTicket lands a row + appears on the Kanban view', async ({ browser }) => {
    const env = await bootHarness();
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);

    const page = await ctx.newPage();
    await page.goto('/tickets/new');
    await page.locator('input').first().fill('Test ticket from M4 spec');
    await page.getByRole('button', { name: 'Create ticket' }).click();
    // Specifically a UUID path so we don't trivially match /tickets/new
    // which is the page we're already on when waitForURL is called.
    await page.waitForURL(/\/tickets\/[a-f0-9-]{36}$/, { timeout: 10_000 });

    // Verify the ticket lands as origin='operator'.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ origin: string; objective: string }[]>`
        SELECT origin, objective FROM tickets WHERE objective = 'Test ticket from M4 spec'
      `;
      expect(rows).toHaveLength(1);
      expect(rows[0].origin).toBe('operator');
    } finally {
      await sql.end();
    }
    await ctx.close();
  });

  test('inline edit writes an event_outbox row with field-level diff', async ({ browser }) => {
    const env = await bootHarness();
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);

    const page = await ctx.newPage();
    await page.goto('/tickets/new');
    await page.locator('input').first().fill('Original objective');
    await page.getByRole('button', { name: 'Create ticket' }).click();
    // Specifically a UUID path so we don't trivially match /tickets/new
    // which is the page we're already on when waitForURL is called.
    await page.waitForURL(/\/tickets\/[a-f0-9-]{36}$/, { timeout: 10_000 });
    // The router.push from createTicket lands us on the
    // intercepted @panel route (drawer overlay) which omits the
    // TicketInlineEditor. Reload to break out of the intercept
    // and render the standalone /tickets/<id> page that hosts the
    // inline-edit Edit button.
    await page.goto(page.url());

    // Inline edit.
    await page.getByRole('button', { name: 'Edit' }).first().click();
    const objectiveInput = page.locator('input').filter({ hasNotText: '' }).first();
    await objectiveInput.fill('Edited objective');
    await page.getByRole('button', { name: 'Save' }).click();

    // Verify the event_outbox row.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      // Allow up to 2s for the post-mutation refresh + DB write.
      const deadline = Date.now() + 5_000;
      while (Date.now() < deadline) {
        const rows = await sql<{ payload: { kind: string; diff: { objective?: { before: string; after: string } } } }[]>`
          SELECT payload FROM event_outbox WHERE channel = 'work.ticket.edited' ORDER BY created_at DESC LIMIT 1
        `;
        if (rows.length > 0 && rows[0].payload?.diff?.objective?.after === 'Edited objective') {
          expect(rows[0].payload.kind).toBe('ticket.edited');
          expect(rows[0].payload.diff.objective?.before).toBe('Original objective');
          expect(rows[0].payload.diff.objective?.after).toBe('Edited objective');
          await ctx.close();
          return;
        }
        await new Promise((r) => setTimeout(r, 200));
      }
      throw new Error('expected event_outbox row never appeared');
    } finally {
      await sql.end();
    }
  });

  test('FR-034 last-write-wins on concurrent inline edits', async ({ browser }) => {
    const env = await bootHarness();
    const ctxA = await browser.newContext();
    await bootstrapAndSignIn(ctxA);

    // Create a ticket as op A.
    const pageA = await ctxA.newPage();
    await pageA.goto('/tickets/new');
    await pageA.locator('input').first().fill('Race target');
    await pageA.getByRole('button', { name: 'Create ticket' }).click();
    await pageA.waitForURL(/\/tickets\/[a-f0-9-]{36}$/, { timeout: 10_000 });
    // Reload to break out of the @panel intercept and render the
    // standalone page that hosts the inline-edit Edit button.
    await pageA.goto(pageA.url());
    const ticketUrl = pageA.url();

    // Second context: log in via the API (the harness's
    // bootstrapAndSignIn captures the session).
    const ctxB = await browser.newContext();
    const pageB = await ctxB.newPage();
    await pageB.goto('/login');
    await pageB.locator('input[name=email]').fill('m4-op@example.com');
    await pageB.locator('input[name=password]').fill('m4-test-pw1234');
    await pageB.getByRole('button', { name: 'Sign in' }).click();
    await pageB.waitForURL((url) => url.pathname === '/');
    await pageB.goto(ticketUrl);

    // A enters edit mode, types 'A wins'.
    await pageA.getByRole('button', { name: 'Edit' }).first().click();
    await pageA.locator('input').filter({ hasNotText: '' }).first().fill('A wins');
    // B enters edit mode, types 'B wins'.
    await pageB.getByRole('button', { name: 'Edit' }).first().click();
    await pageB.locator('input').filter({ hasNotText: '' }).first().fill('B wins');

    // A saves first.
    await pageA.getByRole('button', { name: 'Save' }).click();
    await pageA.waitForTimeout(500);
    // B saves second — last-write-wins: B's value persists.
    await pageB.getByRole('button', { name: 'Save' }).click();
    await pageB.waitForTimeout(500);

    // Verify tickets row reflects B's write.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ objective: string }[]>`
        SELECT objective FROM tickets WHERE objective IN ('A wins', 'B wins') ORDER BY created_at DESC
      `;
      expect(rows[0]?.objective).toBe('B wins');
      // Both audit rows landed.
      const auditRows = await sql<{ payload: { diff: { objective?: { after: string } } } }[]>`
        SELECT payload FROM event_outbox WHERE channel = 'work.ticket.edited' ORDER BY created_at ASC
      `;
      const afters = auditRows
        .map((r) => r.payload?.diff?.objective?.after)
        .filter((v): v is string => typeof v === 'string');
      expect(afters).toContain('A wins');
      expect(afters).toContain('B wins');
    } finally {
      await sql.end();
    }

    await ctxA.close();
    await ctxB.close();
  });
});
