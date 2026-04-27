import { test, expect, type BrowserContext } from './_coverage';
import postgres from 'postgres';
import {
  bootHarness,
  ensureInfisicalFolder,
  truncateDashboardState,
  type HarnessEnv,
} from './_harness';

// M4 / T020 — golden-path operator journey.
//
// Threads the M4 surface set in one test. Vault flows now run
// against a real Infisical testcontainer (Postgres + Redis +
// Infisical v0.159.22) provisioned by _harness.ts; the dashboard
// process is started with INFISICAL_DASHBOARD_ML_* env vars and
// authenticates as a freshly-minted Universal-Auth machine
// identity with admin access to the test workspace.
//
// Steps:
//   1. bootstrap operator + sign in
//   2. /agents → click Edit on a seeded agent → save (model
//      change) → assert agents.changed event in event_outbox
//   3. /tickets/new → create a ticket → assert
//      work.ticket.created in event_outbox
//   4. /departments/engineering → kanban columns visible
//   5. /hygiene → pattern-category filter chip visible
//   6. /vault/new → create a secret → assert secret_metadata +
//      vault_access_log rows landed AND that the secret value
//      does NOT appear anywhere in the audit row payload (Rule 6).
//
// The inline-edit / drag-to-move surfaces are exercised by the
// dedicated m4-ticket-mutations.spec.ts; this spec is the
// vault-wiring smoke + cross-surface audit-row pin.

async function seedFixtures(env: HarnessEnv): Promise<{ ticketId: string; customerId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm4-golden') RETURNING id
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
    await sql`
      INSERT INTO agents (id, department_id, role_slug, agent_md, model, status, listens_for, skills, mcp_tools)
      VALUES (gen_random_uuid(), ${d.id}, 'engineer', '# initial', 'haiku', 'active', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb)
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'golden-path seed', 'sql')
      RETURNING id
    `;
    return { ticketId: t.id, customerId: c.id };
  } finally {
    await sql.end();
  }
}

async function bootstrapAndSignIn(ctx: BrowserContext): Promise<void> {
  const page = await ctx.newPage();
  await page.goto('/setup');
  await page.locator('input[name=name]').fill('Golden Op');
  await page.locator('input[name=email]').fill('golden@example.com');
  await page.locator('input[name=password]').fill('m4-golden-pw1234');
  await page.getByRole('button', { name: 'Create operator account' }).click();
  await page.waitForURL(/\/login/);
  await page.locator('input[name=email]').fill('golden@example.com');
  await page.locator('input[name=password]').fill('m4-golden-pw1234');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
  await page.close();
}

test('M4 golden path — agent edit + ticket create + inline edit + hygiene + vault', async ({
  browser,
}) => {
  const env = await bootHarness();
  await truncateDashboardState(env);
  const { customerId } = await seedFixtures(env);
  // Pre-create the Infisical folder structure for this customer.
  // The dashboard's createSecret server action targets
  // /<customer_id>/operator and Infisical does not auto-create
  // missing folders.
  await ensureInfisicalFolder(env, `/${customerId}/operator`);

  const ctx = await browser.newContext();
  await bootstrapAndSignIn(ctx);
  const page = await ctx.newPage();

  // Agents — Edit the engineer agent.
  await page.goto('/agents/engineering/engineer/edit');
  await expect(page.getByRole('heading', { name: 'Edit agent' })).toBeVisible();
  await page.getByRole('button', { name: 'sonnet' }).click();
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByText(/saved/i)).toBeVisible();

  // Tickets — Create.
  await page.goto('/tickets/new');
  await page.locator('input').first().fill('golden ticket');
  await page.getByRole('button', { name: 'Create ticket' }).click();
  // Wait for redirect to /tickets/<uuid> — the createTicket
  // server action redirects via router.push.
  await page.waitForURL(/\/tickets\/[a-f0-9-]{36}$/, { timeout: 10_000 });

  // Kanban.
  await page.goto('/departments/engineering');
  await expect(page.getByTestId('column-todo')).toBeVisible();
  await expect(page.getByTestId('column-in_dev')).toBeVisible();

  // Hygiene.
  await page.goto('/hygiene');
  await expect(page.getByTestId('failure-mode-filter')).toBeVisible();
  await expect(page.getByTestId('pattern-category-filter')).toBeVisible();

  // Vault — exercise create-secret end-to-end against the Infisical
  // testcontainer. The form's path-prefix prefill resolves the
  // operating entity's customer_id server-side; matching default is
  // /<customer_id>/operator which createSecret accepts under Rule 4.
  const SECRET_VALUE = 'sk_test_golden_path_value_DO_NOT_LEAK_xyz123';
  await page.goto('/vault/new');
  await expect(page.getByRole('heading', { name: 'Create secret' })).toBeVisible();
  await page.locator('input').first().fill('GOLDEN_API_KEY');
  await page.locator('textarea').fill(SECRET_VALUE);
  await page.getByRole('button', { name: 'Create secret' }).click();
  // The form's router.push('/vault') + router.refresh() race
  // sometimes leaves the URL pinned at /vault/new from
  // Playwright's perspective even though the server action
  // succeeded. Poll the DB directly for the secret_metadata row
  // — that row's existence proves the createSecret server action
  // completed end-to-end (Infisical write + Postgres write).
  await expect
    .poll(
      async () => {
        const c = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
        try {
          const r = await c<{ count: number }[]>`
            SELECT count(*)::int AS count FROM secret_metadata
            WHERE secret_path LIKE '%/GOLDEN_API_KEY'
          `;
          return r[0]?.count ?? 0;
        } finally {
          await c.end();
        }
      },
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);

  // Audit assertions:
  //  - event_outbox rows landed for the ticket + agent
  //    mutations (those go through writeMutationEventToOutbox).
  //  - vault mutations go through vault_access_log only (FR-073
  //    audit log is the dedicated vault audit trail; vault
  //    pg_notify carries the secret path as payload directly).
  //  - vault_access_log row landed with outcome='secret_created'
  //    AND the secret value never appears in any audit-side
  //    column (Rule 6 — values must not flow into Postgres).
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const rows = await sql<{ channel: string; count: number }[]>`
      SELECT channel, count(*)::int AS count
      FROM event_outbox
      WHERE channel IN ('work.ticket.created', 'work.agent.edited')
      GROUP BY channel
    `;
    const channels = rows.map((r) => r.channel);
    expect(channels).toContain('work.ticket.created');
    expect(channels).toContain('work.agent.edited');

    const secretRows = await sql<{ secret_path: string; provenance: string }[]>`
      SELECT secret_path, provenance FROM secret_metadata
      WHERE secret_path LIKE '%/GOLDEN_API_KEY'
    `;
    expect(secretRows).toHaveLength(1);
    expect(secretRows[0].provenance).toBe('operator_entered');

    const auditRows = await sql<{ outcome: string; secret_path: string; metadata: unknown }[]>`
      SELECT outcome, secret_path, metadata FROM vault_access_log
      WHERE secret_path LIKE '%/GOLDEN_API_KEY'
    `;
    expect(auditRows).toHaveLength(1);
    expect(auditRows[0].outcome).toBe('secret_created');
    // Rule 6: the secret value MUST NOT appear anywhere in the
    // audit row, including the JSONB metadata.
    const audit_json = JSON.stringify(auditRows[0]);
    expect(audit_json).not.toContain(SECRET_VALUE);
  } finally {
    await sql.end();
  }

  await ctx.close();
});
