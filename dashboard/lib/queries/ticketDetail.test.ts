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

async function seedTicket(): Promise<string> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [c] = await sql<{ id: string }[]>`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 't') RETURNING id`;
    const [d] = await sql<{ id: string }[]>`
      INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
      VALUES (gen_random_uuid(), ${c.id}, 'eng', 'Eng', 1, '/tmp')
      RETURNING id
    `;
    const [t] = await sql<{ id: string }[]>`
      INSERT INTO tickets (id, department_id, column_slug, objective, origin)
      VALUES (gen_random_uuid(), ${d.id}, 'in_dev', 'fix the thing', 'sql')
      RETURNING id
    `;
    return t.id;
  } finally {
    await sql.end();
  }
}

describe('lib/queries/ticketDetail', () => {
  it('returns null for an unknown ticket id', async () => {
    const { fetchTicketDetail } = await import('./ticketDetail');
    const detail = await fetchTicketDetail('00000000-0000-0000-0000-000000000000');
    expect(detail).toBeNull();
  });

  it('returns metadata + history + instances for a ticket with multiple transitions', async () => {
    const ticketId = await seedTicket();
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      // Two transitions; second has hygiene_status='clean'.
      await sql`
        INSERT INTO ticket_transitions (ticket_id, from_column, to_column, hygiene_status, at)
        VALUES
          (${ticketId}, NULL, 'todo', NULL, now() - interval '2 hours'),
          (${ticketId}, 'todo', 'in_dev', 'clean', now() - interval '1 hour')
      `;
      // One agent_instance.
      const [d] = await sql<{ id: string }[]>`SELECT department_id AS id FROM tickets WHERE id = ${ticketId}`;
      await sql`
        INSERT INTO agent_instances (department_id, ticket_id, role_slug, status, exit_reason, total_cost_usd, started_at, finished_at)
        VALUES (${d.id}, ${ticketId}, 'engineer', 'finished', 'finalize_committed', 0, now() - interval '1 hour', now())
      `;
    } finally {
      await sql.end();
    }
    const { fetchTicketDetail } = await import('./ticketDetail');
    const detail = await fetchTicketDetail(ticketId);
    expect(detail).not.toBeNull();
    expect(detail!.metadata.objective).toBe('fix the thing');
    expect(detail!.history).toHaveLength(2);
    expect(detail!.history[0].toColumn).toBe('todo');
    expect(detail!.history[1].toColumn).toBe('in_dev');
    expect(detail!.instances).toHaveLength(1);
    expect(detail!.instances[0].costBlindSpot).toBe(true);
  });

  it('flags sandbox-escape on transitions whose hygiene_status names the failure mode', async () => {
    const ticketId = await seedTicket();
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql`
        INSERT INTO ticket_transitions (ticket_id, from_column, to_column, hygiene_status)
        VALUES (${ticketId}, 'todo', 'in_dev', 'sandbox_escape')
      `;
    } finally {
      await sql.end();
    }
    const { fetchTicketDetail } = await import('./ticketDetail');
    const detail = await fetchTicketDetail(ticketId);
    expect(detail!.history[0].sandboxEscape).toBe(true);
    expect(detail!.history[0].sandboxEscapeDetail).not.toBeNull();
  });

  it('does NOT flag costBlindSpot for non-zero cost rows', async () => {
    const ticketId = await seedTicket();
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const [d] = await sql<{ id: string }[]>`SELECT department_id AS id FROM tickets WHERE id = ${ticketId}`;
      await sql`
        INSERT INTO agent_instances (department_id, ticket_id, role_slug, status, exit_reason, total_cost_usd)
        VALUES (${d.id}, ${ticketId}, 'engineer', 'finished', 'finalize_committed', 0.0123)
      `;
    } finally {
      await sql.end();
    }
    const { fetchTicketDetail } = await import('./ticketDetail');
    const detail = await fetchTicketDetail(ticketId);
    expect(detail!.instances[0].costBlindSpot).toBe(false);
    expect(detail!.instances[0].totalCostUsd).toBeCloseTo(0.0123);
  });
});
