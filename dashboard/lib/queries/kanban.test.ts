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

async function seedDept(slug = 'engineering'): Promise<{ deptId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
    // Use jsonb_build_object so the JSONB value lands as a real
    // structured value — postgres-js's tagged template doesn't
    // reliably round-trip a stringified JS object through ::jsonb;
    // the stringified form sometimes lands as a quoted scalar.
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, ${slug}, 'X', 1, '/tmp')
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

describe('lib/queries/kanban', () => {
  it('fetchDepartmentBySlug returns the workflow columns', async () => {
    await seedDept();
    const { fetchDepartmentBySlug } = await import('./kanban');
    const dept = await fetchDepartmentBySlug('engineering');
    expect(dept).not.toBeNull();
    expect(dept!.columns.map((c) => c.slug)).toEqual(['todo', 'in_dev', 'qa_review', 'done']);
  });

  it('fetchDepartmentBySlug returns null for an unknown slug', async () => {
    const { fetchDepartmentBySlug } = await import('./kanban');
    const dept = await fetchDepartmentBySlug('nonexistent');
    expect(dept).toBeNull();
  });

  it('fetchKanban returns all tickets including done, ordered by created_at descending', async () => {
    const { deptId } = await seedDept();
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql`
        INSERT INTO tickets (id, department_id, column_slug, objective, origin, created_at)
        VALUES
          (gen_random_uuid(), ${deptId}, 'todo', 'oldest', 'sql', now() - interval '2 hours'),
          (gen_random_uuid(), ${deptId}, 'in_dev', 'middle', 'sql', now() - interval '1 hour'),
          (gen_random_uuid(), ${deptId}, 'todo', 'newest', 'sql', now()),
          (gen_random_uuid(), ${deptId}, 'done', 'closed', 'sql', now() - interval '30 minutes')
      `;
    } finally {
      await sql.end();
    }
    const { fetchKanban } = await import('./kanban');
    const tickets = await fetchKanban('engineering');
    // Done tickets are now included so the operator sees the full
    // department state on the kanban (M5.4 retro live-stack
    // discovery — operator-reported "ticket disappears when done").
    expect(tickets.map((t) => t.objective)).toEqual(['newest', 'middle', 'closed', 'oldest']);
  });
});
