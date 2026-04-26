import { describe, it, expect, beforeAll, beforeEach } from 'vitest';
import postgres from 'postgres';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

let env: { DASHBOARD_APP_DSN: string; TEST_SUPERUSER_DSN: string };

beforeAll(() => {
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  env = JSON.parse(readFileSync(path, 'utf-8'));
  process.env.DASHBOARD_APP_DSN = env.DASHBOARD_APP_DSN;
  process.env.DASHBOARD_RO_DSN = env.DASHBOARD_APP_DSN;
  process.env.BETTER_AUTH_SECRET = 'unit_test_secret_long_enough_xxxxxxxxxxxxxxxxxxxx';
});

beforeEach(async () => {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await sql`TRUNCATE event_outbox RESTART IDENTITY CASCADE`;
  } finally {
    await sql.end();
  }
});

type JsonScalar = string | number | boolean | null;
type JsonPayload = { [key: string]: JsonScalar | JsonPayload };

async function seedEvent(channel: string, payload: JsonPayload, atIso: string) {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    // postgres-js sends `${JSON.stringify(p)}::jsonb` as a JSON
    // scalar string (the entire object as a quoted string), not as
    // a JSON object. Use sql.json to round-trip jsonb correctly.
    await sql`
      INSERT INTO event_outbox (channel, payload, created_at)
      VALUES (${channel}, ${sql.json(payload)}, ${atIso}::timestamptz)
    `;
  } finally {
    await sql.end();
  }
}

describe('lib/queries/activityCatchup', () => {
  it('returns empty when there are no events', async () => {
    const { fetchEventsAfter } = await import('./activityCatchup');
    const result = await fetchEventsAfter(null);
    expect(result).toEqual([]);
  });

  it('returns events after the cursor in ASC order', async () => {
    await seedEvent('work.ticket.created', { ticket_id: 'aaa' }, '2026-04-26T10:00:00Z');
    await seedEvent('work.ticket.created', { ticket_id: 'bbb' }, '2026-04-26T10:00:01Z');
    await seedEvent('work.ticket.created', { ticket_id: 'ccc' }, '2026-04-26T10:00:02Z');

    const { fetchEventsAfter } = await import('./activityCatchup');
    const result = await fetchEventsAfter('2026-04-26T10:00:00Z');

    expect(result).toHaveLength(2);
    expect(result[0]).toMatchObject({ kind: 'ticket.created', ticketId: 'bbb' });
    expect(result[1]).toMatchObject({ kind: 'ticket.created', ticketId: 'ccc' });
  });

  it('drops events on unknown channels', async () => {
    await seedEvent('work.ticket.created', { ticket_id: 'aaa' }, '2026-04-26T10:00:00Z');
    await seedEvent('chat.message.received', { msg: 'noise' }, '2026-04-26T10:00:01Z');

    const { fetchEventsAfter } = await import('./activityCatchup');
    const result = await fetchEventsAfter(null);

    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ kind: 'ticket.created', ticketId: 'aaa' });
  });

  it('caps the result set with the limit parameter', async () => {
    for (let i = 0; i < 5; i++) {
      await seedEvent(
        'work.ticket.created',
        { ticket_id: `t${i}` },
        `2026-04-26T10:00:0${i}Z`,
      );
    }

    const { fetchEventsAfter } = await import('./activityCatchup');
    const result = await fetchEventsAfter(null, 2);

    expect(result).toHaveLength(2);
  });

  it('parses parameterized transition channels', async () => {
    await seedEvent(
      'work.ticket.transitioned.eng.todo.in_dev',
      { ticket_id: 'tx', agent_instance_id: 'ai-1' },
      '2026-04-26T10:00:00Z',
    );

    const { fetchEventsAfter } = await import('./activityCatchup');
    const result = await fetchEventsAfter(null);

    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({
      kind: 'ticket.transitioned',
      ticketId: 'tx',
      department: 'eng',
      from: 'todo',
      to: 'in_dev',
      agentInstanceId: 'ai-1',
    });
  });
});
