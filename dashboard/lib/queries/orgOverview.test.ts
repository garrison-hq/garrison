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

// Truncate before each test (not after) so the first test sees an
// empty database — the M2 migrations seed default agents (engineer +
// qa-engineer) that an afterEach hook wouldn't have wiped yet.
beforeEach(async () => {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await sql`TRUNCATE companies, departments, tickets, ticket_transitions, agent_instances, agents RESTART IDENTITY CASCADE`;
  } finally {
    await sql.end();
  }
});

async function seedDept(): Promise<{ companyId: string; deptId: string }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [company] = await sql<{ id: string }[]>`
      INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'test') RETURNING id
    `;
    const workflow = JSON.stringify({
      columns: [
        { slug: 'todo', label: 'To do', entry_from: ['backlog'] },
        { slug: 'in_dev', label: 'In dev', entry_from: ['todo'] },
        { slug: 'qa_review', label: 'QA review', entry_from: ['in_dev'] },
        { slug: 'done', label: 'Done', entry_from: ['qa_review'] },
      ],
      transitions: { todo: ['in_dev'], in_dev: ['qa_review'], qa_review: ['done'] },
    });
    const [dept] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path, workflow)
      VALUES (gen_random_uuid(), ${company.id}, 'engineering', 'Engineering', 2, '/tmp', ${workflow}::jsonb)
      RETURNING id
    `;
    return { companyId: company.id, deptId: dept.id };
  } finally {
    await sql.end();
  }
}

describe('lib/queries/orgOverview', () => {
  it('fetchOrgKPIs returns zeros against an empty database', async () => {
    const { fetchOrgKPIs } = await import('./orgOverview');
    const kpis = await fetchOrgKPIs();
    expect(kpis).toMatchObject({
      openTickets: 0,
      activeAgents: 0,
      transitions24h: 0,
      hygieneWarnings: 0,
    });
  });

  it('fetchOrgKPIs counts open tickets, active agents, recent transitions, and hygiene warnings', async () => {
    const { deptId } = await seedDept();
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      // 2 open tickets in todo, 1 done; 1 active agent.
      await sql`
        INSERT INTO tickets (id, department_id, column_slug, objective, origin)
        VALUES
          (gen_random_uuid(), ${deptId}, 'todo', 'open ticket 1', 'sql'),
          (gen_random_uuid(), ${deptId}, 'todo', 'open ticket 2', 'sql'),
          (gen_random_uuid(), ${deptId}, 'done', 'closed ticket', 'sql')
      `;
      await sql`
        INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
        VALUES (gen_random_uuid(), ${deptId}, 'engineer', '#', 'm', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, NULL, 'active')
      `;
      const [t1] = await sql<{ id: string }[]>`SELECT id FROM tickets WHERE column_slug='todo' LIMIT 1`;
      // 2 transitions: 1 with non-clean hygiene_status, 1 clean.
      await sql`
        INSERT INTO ticket_transitions (ticket_id, from_column, to_column, hygiene_status)
        VALUES
          (${t1.id}, 'todo', 'in_dev', 'finalize_never_called'),
          (${t1.id}, 'in_dev', 'qa_review', 'clean')
      `;
    } finally {
      await sql.end();
    }
    const { fetchOrgKPIs } = await import('./orgOverview');
    const kpis = await fetchOrgKPIs();
    expect(kpis.openTickets).toBe(2);
    expect(kpis.activeAgents).toBe(1);
    expect(kpis.transitions24h).toBe(2);
    expect(kpis.hygieneWarnings).toBe(1);
  });

  it('fetchDepartmentRows returns one row per seeded department with ticket counts + last transition', async () => {
    const { deptId } = await seedDept();
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql`
        INSERT INTO tickets (id, department_id, column_slug, objective, origin)
        VALUES
          (gen_random_uuid(), ${deptId}, 'todo', 'tk1', 'sql'),
          (gen_random_uuid(), ${deptId}, 'todo', 'tk2', 'sql'),
          (gen_random_uuid(), ${deptId}, 'in_dev', 'tk3', 'sql')
      `;
      await sql`
        INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
        VALUES (gen_random_uuid(), ${deptId}, 'engineer', '#', 'm', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, NULL, 'active')
      `;
    } finally {
      await sql.end();
    }
    const { fetchDepartmentRows } = await import('./orgOverview');
    const rows = await fetchDepartmentRows();
    expect(rows).toHaveLength(1);
    expect(rows[0].slug).toBe('engineering');
    expect(rows[0].agentCount).toBe(1);
    expect(rows[0].agentCap).toBe(2);
    expect(rows[0].ticketCounts).toMatchObject({ todo: 2, in_dev: 1 });
  });
});
