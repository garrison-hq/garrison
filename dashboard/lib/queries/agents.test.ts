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

describe('lib/queries/agents', () => {
  it('returns an empty list when no agents are configured', async () => {
    const { fetchAgents } = await import('./agents');
    const rows = await fetchAgents();
    expect(rows).toEqual([]);
  });

  it('returns agents with computed last_spawned_at and spawns_this_week stats', async () => {
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    let deptId: string;
    try {
      const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
      const [d] = await sql<{ id: string }[]>`
        INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
        VALUES (gen_random_uuid(), ${c.id}, 'eng', 'X', 2, '/tmp')
        RETURNING id
      `;
      deptId = d.id;
      await sql`
        INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
        VALUES (gen_random_uuid(), ${deptId}, 'engineer', '#', 'haiku', '[]'::jsonb, '[]'::jsonb, '["work.ticket.created"]'::jsonb, NULL, 'active')
      `;
      const [t1] = await sql<{ id: string }[]>`
        INSERT INTO tickets (id, department_id, column_slug, objective, origin)
        VALUES (gen_random_uuid(), ${deptId}, 'todo', 'agents-test', 'sql')
        RETURNING id
      `;
      // Two spawns in the last week, one a month ago.
      await sql`
        INSERT INTO agent_instances (department_id, ticket_id, role_slug, status, started_at)
        VALUES
          (${deptId}, ${t1.id}, 'engineer', 'finished', now() - interval '1 hour'),
          (${deptId}, ${t1.id}, 'engineer', 'finished', now() - interval '2 days'),
          (${deptId}, ${t1.id}, 'engineer', 'finished', now() - interval '30 days')
      `;
    } finally {
      await sql.end();
    }
    const { fetchAgents } = await import('./agents');
    const rows = await fetchAgents();
    expect(rows).toHaveLength(1);
    expect(rows[0].roleSlug).toBe('engineer');
    expect(rows[0].concurrencyCap).toBe(2);
    expect(rows[0].spawnsThisWeek).toBe(2);
    expect(rows[0].lastSpawnedAt).not.toBeNull();
  });

  it('returns spawnsThisWeek=0 and lastSpawnedAt=null for an agent with no instances', async () => {
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
      const [d] = await sql<{ id: string }[]>`
        INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
        VALUES (gen_random_uuid(), ${c.id}, 'eng', 'X', 1, '/tmp')
        RETURNING id
      `;
      await sql`
        INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
        VALUES (gen_random_uuid(), ${d.id}, 'engineer', '#', 'm', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, NULL, 'active')
      `;
    } finally {
      await sql.end();
    }
    const { fetchAgents } = await import('./agents');
    const rows = await fetchAgents();
    expect(rows[0].spawnsThisWeek).toBe(0);
    expect(rows[0].lastSpawnedAt).toBeNull();
  });
});
