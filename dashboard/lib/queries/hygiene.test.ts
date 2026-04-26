import { describe, it, expect, beforeAll, beforeEach } from 'vitest';
import postgres from 'postgres';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

let env: { DASHBOARD_APP_DSN: string; TEST_SUPERUSER_DSN: string };

beforeAll(async () => {
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  env = JSON.parse(readFileSync(path, 'utf-8'));
  process.env.DASHBOARD_APP_DSN = env.DASHBOARD_APP_DSN;
  process.env.DASHBOARD_RO_DSN = env.DASHBOARD_APP_DSN;
  process.env.BETTER_AUTH_SECRET = 'unit_test_secret_long_enough_xxxxxxxxxxxxxxxxxxxx';
});

beforeEach(async () => {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await sql`TRUNCATE companies, departments, tickets, ticket_transitions, agent_instances, agents RESTART IDENTITY CASCADE`;
  } finally {
    await sql.end();
  }
});

async function seedTransitions(statuses: string[]): Promise<{ deptId: string; ticketId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'eng', 'X', 1, '/tmp')
      RETURNING id
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'h-test', 'sql')
      RETURNING id
    `;
    for (const status of statuses) {
      await sql`
        INSERT INTO ticket_transitions (ticket_id, from_column, to_column, hygiene_status)
        VALUES (${t.id}, 'todo', 'in_dev', ${status})
      `;
    }
    return { deptId: d.id, ticketId: t.id };
  } finally {
    await sql.end();
  }
}

describe('lib/queries/hygiene', () => {
  it('returns empty against an empty database', async () => {
    const { fetchHygieneRows } = await import('./hygiene');
    const result = await fetchHygieneRows();
    expect(result.rows).toEqual([]);
    expect(result.total).toBe(0);
  });

  it('returns only non-clean rows', async () => {
    await seedTransitions(['clean', 'finalize_never_called', 'sandbox_escape', 'suspected_secret_emitted']);
    const { fetchHygieneRows } = await import('./hygiene');
    const result = await fetchHygieneRows();
    expect(result.total).toBe(3);
    expect(result.rows.map((r) => r.hygieneStatus).sort()).toEqual([
      'finalize_never_called',
      'sandbox_escape',
      'suspected_secret_emitted',
    ]);
  });

  it('classifies rows into the three failure-mode buckets', async () => {
    await seedTransitions(['finalize_never_called', 'sandbox_escape', 'suspected_secret_emitted']);
    const { fetchHygieneRows } = await import('./hygiene');
    const result = await fetchHygieneRows();
    const modes = result.rows.map((r) => r.failureMode).sort();
    expect(modes).toEqual(['finalize_path', 'sandbox_escape', 'suspected_secret_emitted']);
  });

  it('filters by failure mode', async () => {
    await seedTransitions(['finalize_never_called', 'sandbox_escape', 'suspected_secret_emitted']);
    const { fetchHygieneRows } = await import('./hygiene');
    const onlySecret = await fetchHygieneRows({ failureMode: 'suspected_secret_emitted' });
    expect(onlySecret.rows).toHaveLength(1);
    expect(onlySecret.rows[0].hygieneStatus).toBe('suspected_secret_emitted');
  });

  it('flags suspected_secret_emitted rows with a pattern category, never the matched value', async () => {
    await seedTransitions(['suspected_secret_emitted']);
    const { fetchHygieneRows } = await import('./hygiene');
    const result = await fetchHygieneRows({ failureMode: 'suspected_secret_emitted' });
    expect(result.rows[0].patternCategory).toBe('secret-shape');
    // No raw secret values should be reachable from the row
    // (the schema doesn't carry them; pinning the contract here).
    expect(JSON.stringify(result.rows[0])).not.toMatch(/sk-/);
  });
});
