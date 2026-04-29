// M5.2 — query tests for the dashboard's chat read-side helpers.
//
// Boots the Postgres testcontainer once via vitest globalSetup and
// reuses the harness env file for DSN. Truncates chat tables between
// tests so seeded fixtures don't leak.

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
    await sql`TRUNCATE chat_sessions, chat_messages RESTART IDENTITY CASCADE`;
  } finally {
    await sql.end();
  }
});

async function seedSession(userId: string, opts: { isArchived?: boolean; status?: string }): Promise<string> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const [row] = await sql<{ id: string }[]>`
      INSERT INTO chat_sessions (started_by_user_id, status, is_archived)
      VALUES (${userId}, ${opts.status ?? 'active'}, ${opts.isArchived ?? false})
      RETURNING id
    `;
    return row.id;
  } finally {
    await sql.end();
  }
}

async function seedMessages(sessionId: string, count: number, role: 'operator' | 'assistant'): Promise<void> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    for (let i = 0; i < count; i++) {
      await sql`
        INSERT INTO chat_messages (session_id, turn_index, role, status, content)
        VALUES (${sessionId}, ${i}, ${role}, 'completed', ${'msg ' + i})
      `;
    }
  } finally {
    await sql.end();
  }
}

describe('listSessionsForUser', () => {
  it('TestListSessionsForUserFiltersArchivedDefault', async () => {
    const userId = '11111111-1111-1111-1111-111111111111';
    for (let i = 0; i < 3; i++) await seedSession(userId, {});
    for (let i = 0; i < 2; i++) await seedSession(userId, { isArchived: true });

    const { listSessionsForUser } = await import('./chat');
    const rows = await listSessionsForUser(userId);
    expect(rows.length).toBe(3);
    for (const r of rows) expect(r.isArchived).toBe(false);
  });

  it('TestListSessionsForUserCanIncludeArchived', async () => {
    const userId = '22222222-2222-2222-2222-222222222222';
    for (let i = 0; i < 3; i++) await seedSession(userId, {});
    for (let i = 0; i < 2; i++) await seedSession(userId, { isArchived: true });

    const { listSessionsForUser } = await import('./chat');
    const rows = await listSessionsForUser(userId, { archived: true });
    expect(rows.length).toBe(2);
    for (const r of rows) expect(r.isArchived).toBe(true);
  });
});

describe('getSessionWithMessages', () => {
  it('TestGetSessionWithMessagesPaginates', async () => {
    const sessionId = await seedSession('33333333-3333-3333-3333-333333333333', {});
    await seedMessages(sessionId, 100, 'operator');

    const { getSessionWithMessages } = await import('./chat');
    const result = await getSessionWithMessages(sessionId);
    expect(result).not.toBeNull();
    expect(result!.messages.length).toBe(50);
    expect(result!.hasMore).toBe(true);
    // Default page returns the most recent 50, reverse-sorted to ASC.
    expect(result!.messages[0].turnIndex).toBe(50);
    expect(result!.messages.at(-1)?.turnIndex).toBe(99);
  });

  it('TestGetSessionWithMessagesLoadsEarlier', async () => {
    const sessionId = await seedSession('44444444-4444-4444-4444-444444444444', {});
    await seedMessages(sessionId, 100, 'operator');

    const { getSessionWithMessages } = await import('./chat');
    const result = await getSessionWithMessages(sessionId, { beforeTurnIndex: 50 });
    expect(result).not.toBeNull();
    expect(result!.messages.length).toBe(50);
    expect(result!.hasMore).toBe(false);
    expect(result!.messages[0].turnIndex).toBe(0);
    expect(result!.messages.at(-1)?.turnIndex).toBe(49);
  });
});

describe('getMostRecentMempalaceCallAge', () => {
  it('TestGetMostRecentMempalaceCallAgeReturnsAge', async () => {
    const sessionId = await seedSession('55555555-5555-5555-5555-555555555555', {});
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      // Seed an assistant row whose raw_event_envelope carries a
      // mempalace tool_use + matching tool_result with no is_error.
      // This mirrors the supervisor's actual envelope shape: a
      // top-level array of stream events; assistant carries the
      // tool_use; user carries the tool_result; tool_use_id ties them.
      await sql`
        INSERT INTO chat_messages (session_id, turn_index, role, status, content, raw_event_envelope, terminated_at)
        VALUES (
          ${sessionId}, 1, 'assistant', 'completed',
          'response',
          ${JSON.stringify([
            {
              type: 'assistant',
              message: {
                content: [
                  { type: 'tool_use', id: 'toolu_test_001', name: 'mcp__mempalace__mempalace_search' },
                ],
              },
            },
            {
              type: 'user',
              message: {
                content: [
                  { type: 'tool_result', tool_use_id: 'toolu_test_001', content: [{ type: 'text', text: 'ok' }] },
                ],
              },
            },
          ])}::jsonb,
          NOW()
        )
      `;
    } finally {
      await sql.end();
    }

    const { getMostRecentMempalaceCallAge } = await import('./chat');
    const { ageMs } = await getMostRecentMempalaceCallAge(sessionId);
    expect(ageMs).not.toBeNull();
    expect(ageMs).toBeGreaterThanOrEqual(0);
    expect(ageMs).toBeLessThan(60_000);
  });

  it('TestGetMostRecentMempalaceCallAgeReturnsNullForNone', async () => {
    const sessionId = await seedSession('66666666-6666-6666-6666-666666666666', {});
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      // Assistant row with no MCP calls in the envelope.
      await sql`
        INSERT INTO chat_messages (session_id, turn_index, role, status, content, raw_event_envelope, terminated_at)
        VALUES (
          ${sessionId}, 1, 'assistant', 'completed',
          'response',
          ${JSON.stringify({ events: [{ type: 'text', text: 'plain' }] })}::jsonb,
          NOW()
        )
      `;
    } finally {
      await sql.end();
    }

    const { getMostRecentMempalaceCallAge } = await import('./chat');
    expect(await getMostRecentMempalaceCallAge(sessionId)).toEqual({ ageMs: null });
  });

  it('TestGetMostRecentMempalaceCallAgeIgnoresFailedCalls', async () => {
    const sessionId = await seedSession('77777777-7777-7777-7777-777777777777', {});
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      await sql`
        INSERT INTO chat_messages (session_id, turn_index, role, status, content, raw_event_envelope, terminated_at)
        VALUES (
          ${sessionId}, 1, 'assistant', 'completed',
          'response',
          ${JSON.stringify([
            {
              type: 'assistant',
              message: {
                content: [
                  { type: 'tool_use', id: 'toolu_test_002', name: 'mcp__mempalace__mempalace_search' },
                ],
              },
            },
            {
              type: 'user',
              message: {
                content: [
                  { type: 'tool_result', tool_use_id: 'toolu_test_002', is_error: true, content: [{ type: 'text', text: 'boom' }] },
                ],
              },
            },
          ])}::jsonb,
          NOW()
        )
      `;
    } finally {
      await sql.end();
    }

    const { getMostRecentMempalaceCallAge } = await import('./chat');
    expect(await getMostRecentMempalaceCallAge(sessionId)).toEqual({ ageMs: null });
  });
});
