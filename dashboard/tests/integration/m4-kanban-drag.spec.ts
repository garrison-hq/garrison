import { test, expect, type BrowserContext } from './_coverage';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState, type HarnessEnv } from './_harness';

// M4 / coverage spec — drives KanbanBoard's drag-and-drop client
// code. Without a spec that drags, the handleDragStart /
// DragOver / DragLeave / Drop / DragEnd handlers + the optimistic
// setColumnFor call + the conflict-revert path are all
// uncovered (~48 lines of new M4 code).
//
// Playwright's page.dragAndDrop() synthesizes the full HTML5
// drag sequence (mousedown → dragstart → dragover → drop →
// dragend) that KanbanBoard listens to. The moveTicket server
// action runs end-to-end against the testcontainer Postgres.

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Kanban Op');
  await page.locator('input[name=email]').fill('kanban@example.com');
  await page.locator('input[name=password]').fill('m4-kanban-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('kanban@example.com');
  await page.locator('input[name=password]').fill('m4-kanban-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

async function seedDeptAndTicket(env: HarnessEnv): Promise<{ ticketId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-kanban') RETURNING id
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
          jsonb_build_object('slug', 'in_dev', 'label', 'In dev'),
          jsonb_build_object('slug', 'qa_review', 'label', 'QA review'),
          jsonb_build_object('slug', 'done', 'label', 'Done')
        )
      ) WHERE id = ${d.id}
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'todo', 'kanban drag target', 'sql')
      RETURNING id
    `;
    return { ticketId: t.id };
  } finally {
    await sql.end();
  }
}

test.describe('M4 kanban drag-drop', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  test('drag a ticket from todo → in_dev triggers moveTicket + writes operator_initiated transition', async ({
    browser,
  }) => {
    const env = await bootHarness();
    const { ticketId } = await seedDeptAndTicket(env);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    await page.goto('/departments/engineering');
    const card = page.getByTestId(`ticket-card-${ticketId}`);
    const targetColumn = page.getByTestId('column-in_dev');
    await expect(card).toBeVisible();
    await expect(targetColumn).toBeVisible();

    // dragAndDrop synthesizes the full dragstart → dragover →
    // drop sequence; KanbanBoard's handlers fire in order.
    await card.dragTo(targetColumn);

    // The card should now appear inside the in_dev column
    // (optimistic setColumnFor) AND the moveTicket server action
    // should have written a ticket_transitions row.
    await expect
      .poll(async () => {
        const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
        try {
          const r = await sql<{ count: number }[]>`
            SELECT count(*)::int AS count FROM ticket_transitions
            WHERE ticket_id = ${ticketId}
              AND from_column = 'todo'
              AND to_column = 'in_dev'
              AND hygiene_status = 'operator_initiated'
          `;
          return r[0]?.count ?? 0;
        } finally {
          await sql.end();
        }
      }, { timeout: 10_000 })
      .toBeGreaterThan(0);

    await ctx.close();
  });

  test('drop on the same column is a no-op (FR-036; setColumnFor early-return)', async ({
    browser,
  }) => {
    const env = await bootHarness();
    const { ticketId } = await seedDeptAndTicket(env);
    const ctx = await browser.newContext();
    await bootstrapAndSignIn(ctx);
    const page = await ctx.newPage();

    await page.goto('/departments/engineering');
    const card = page.getByTestId(`ticket-card-${ticketId}`);
    const sourceColumn = page.getByTestId('column-todo');

    // Drop on the same column the card came from. KanbanBoard's
    // handleDrop should early-return without firing moveTicket.
    await card.dragTo(sourceColumn);

    // Give any spurious moveTicket call time to land if the
    // early-return is broken.
    await page.waitForTimeout(1_000);

    // No ticket_transitions row should exist.
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const r = await sql<{ count: number }[]>`
        SELECT count(*)::int AS count FROM ticket_transitions
        WHERE ticket_id = ${ticketId}
      `;
      expect(r[0]?.count ?? 0).toBe(0);
    } finally {
      await sql.end();
    }

    await ctx.close();
  });
});
