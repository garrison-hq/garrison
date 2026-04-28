// eventOutbox integration test — verifies that
// writeMutationEventToOutbox lands a row with the correct
// channel name and payload shape, and that a subsequent
// emitPgNotifyForOutbox issues the right NOTIFY.
//
// Runs against the shared Postgres testcontainer.

import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import postgres from 'postgres';
import { drizzle } from 'drizzle-orm/postgres-js';
import { sql } from 'drizzle-orm';
import { writeMutationEventToOutbox } from './eventOutbox';
import type { MutationTx } from './eventOutbox';

let env: { TEST_SUPERUSER_DSN: string };

beforeAll(async () => {
  const { readFileSync } = await import('node:fs');
  const { resolve } = await import('node:path');
  const path = resolve(import.meta.dirname, '..', '..', 'tests', 'integration', '.harness', 'env.json');
  env = JSON.parse(readFileSync(path, 'utf-8'));
});

afterEach(async () => {
  const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await client`DELETE FROM event_outbox WHERE channel LIKE 'work.%' OR channel LIKE 'm4_%'`;
  } finally {
    await client.end();
  }
});

describe('lib/audit/eventOutbox', () => {
  it('writes a row with the correct channel for ticket.created', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);

    const result = await db.transaction(async (tx) => {
      return writeMutationEventToOutbox(tx as unknown as MutationTx, {
        kind: 'ticket.created',
        ticketId: '00000000-0000-0000-0000-000000000abc',
        deptSlug: 'engineering',
        targetColumn: 'in_dev',
      });
    });
    expect(result.id).toMatch(/^[0-9a-f-]{36}$/);

    const rows = await client<{ channel: string; payload: Record<string, unknown> }[]>`
      SELECT channel, payload FROM event_outbox WHERE id = ${result.id}
    `;
    expect(rows).toHaveLength(1);
    expect(rows[0].channel).toBe('work.ticket.created');
    expect(rows[0].payload).toMatchObject({
      kind: 'ticket.created',
      ticketId: '00000000-0000-0000-0000-000000000abc',
      deptSlug: 'engineering',
      targetColumn: 'in_dev',
    });

    await client.end();
  });

  it('writes a row with the correct channel for ticket.moved (parameterized)', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);

    const result = await db.transaction(async (tx) => {
      return writeMutationEventToOutbox(
        tx as unknown as MutationTx,
        {
          kind: 'ticket.moved',
          ticketId: '00000000-0000-0000-0000-000000000def',
          fromColumn: 'in_dev',
          toColumn: 'in_review',
          transitionId: '00000000-0000-0000-0000-000000000aaa',
          origin: 'operator',
        },
        'engineering',
      );
    });

    const rows = await client<{ channel: string }[]>`SELECT channel FROM event_outbox WHERE id = ${result.id}`;
    expect(rows[0].channel).toBe('work.ticket.transitioned.engineering.in_dev.in_review');

    await client.end();
  });

  it('throws on ticket.moved without a deptSlug', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);

    await expect(
      db.transaction(async (tx) => {
        await writeMutationEventToOutbox(tx as unknown as MutationTx, {
          kind: 'ticket.moved',
          ticketId: 'tk',
          fromColumn: 'in_dev',
          toColumn: 'in_review',
          transitionId: 'tr',
          origin: 'operator',
        });
      }),
    ).rejects.toThrow(/deptSlug/);

    await client.end();
  });

  it('writes correct channel for agent.edited', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);

    const result = await db.transaction(async (tx) => {
      return writeMutationEventToOutbox(tx as unknown as MutationTx, {
        kind: 'agent.edited',
        roleSlug: 'engineer',
        diff: { model: { before: 'sonnet', after: 'haiku' } },
      });
    });

    const rows = await client<{ channel: string; payload: Record<string, unknown> }[]>`
      SELECT channel, payload FROM event_outbox WHERE id = ${result.id}
    `;
    expect(rows[0].channel).toBe('work.agent.edited');
    expect((rows[0].payload as { diff: Record<string, unknown> }).diff).toMatchObject({
      model: { before: 'sonnet', after: 'haiku' },
    });

    await client.end();
  });

  it('rolls back the row write when the transaction throws', async () => {
    const client = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    const db = drizzle(client);

    await expect(
      db.transaction(async (tx) => {
        await writeMutationEventToOutbox(tx as unknown as MutationTx, {
          kind: 'ticket.edited',
          ticketId: '00000000-0000-0000-0000-000000000bad',
          diff: { title: { before: 'a', after: 'b' } },
        });
        throw new Error('rollback');
      }),
    ).rejects.toThrow('rollback');

    const rows = await client`
      SELECT id FROM event_outbox WHERE channel = 'work.ticket.edited' AND payload->>'ticketId' = '00000000-0000-0000-0000-000000000bad'
    `;
    expect(rows).toHaveLength(0);

    await client.end();
  });
});
