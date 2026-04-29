// M5.2 — server action tests for chat session housekeeping.
//
// Boots the Postgres testcontainer once via the vitest globalSetup
// hook + reuses the harness env file for DSN. Each test opens its own
// auth-mock context (via vi.mock on @/lib/auth/session) so ownership
// gating exercises both same-user and foreign-user paths without
// bringing the better-auth machinery into the loop.
//
// pg_notify assertions use a dedicated postgres LISTEN connection so
// the test verifies the channel + payload shape end-to-end.

import { describe, it, expect, beforeAll, beforeEach, afterAll, vi } from 'vitest';
import postgres from 'postgres';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

let env: { DASHBOARD_APP_DSN: string; TEST_SUPERUSER_DSN: string };
let mockUserId = '11111111-1111-1111-1111-111111111111';

// Mock the auth session BEFORE the action module is imported. Each
// test rewrites mockUserId to switch between owner and non-owner
// contexts.
vi.mock('@/lib/auth/session', () => ({
  getSession: () =>
    Promise.resolve({
      user: { id: mockUserId, email: 'op@local', emailVerified: true },
      session: { id: 'sess', token: 't', userId: mockUserId, expiresAt: new Date(Date.now() + 3600_000) },
    }),
}));

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
  mockUserId = '11111111-1111-1111-1111-111111111111';
});

afterAll(async () => {
  // Allow open connections to drain so the Postgres container
  // doesn't hold the test process open after the suite.
  await new Promise((r) => setTimeout(r, 50));
});

async function seedSession(opts: { userId: string; status?: string; isArchived?: boolean }): Promise<string> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const status = opts.status ?? 'active';
    const isArchived = opts.isArchived ?? false;
    const [row] = await sql<{ id: string }[]>`
      INSERT INTO chat_sessions (started_by_user_id, status, is_archived)
      VALUES (${opts.userId}, ${status}, ${isArchived})
      RETURNING id
    `;
    return row.id;
  } finally {
    await sql.end();
  }
}

async function readSession(sessionId: string): Promise<{ status: string; isArchived: boolean; endedAt: Date | null } | null> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const rows = await sql<{ status: string; is_archived: boolean; ended_at: Date | null }[]>`
      SELECT status, is_archived, ended_at FROM chat_sessions WHERE id = ${sessionId}
    `;
    if (rows.length === 0) return null;
    return { status: rows[0].status, isArchived: rows[0].is_archived, endedAt: rows[0].ended_at };
  } finally {
    await sql.end();
  }
}

async function captureNotify<T>(channel: string, runWhileListening: () => Promise<T>): Promise<{ result: T; payloads: string[] }> {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  const payloads: string[] = [];
  await sql.listen(channel, (payload) => {
    payloads.push(payload);
  });
  // Give the LISTEN connection a moment to register at the
  // backend side before any NOTIFY fires.
  await new Promise((r) => setTimeout(r, 50));
  try {
    const result = await runWhileListening();
    // Drain any in-flight notifications (postgres-js pumps them
    // through the connection inline, but not all of them have
    // delivered by the time the call returns).
    await new Promise((r) => setTimeout(r, 100));
    return { result, payloads };
  } finally {
    await sql.end();
  }
}

describe('chat actions — endChatSession', () => {
  it('TestEndChatSessionMarksEnded', async () => {
    const sessionId = await seedSession({ userId: mockUserId, status: 'active' });
    const { endChatSession } = await import('./chat');

    const { payloads } = await captureNotify('work.chat.session_ended', async () => {
      await endChatSession(sessionId);
    });

    const row = await readSession(sessionId);
    expect(row?.status).toBe('ended');
    expect(row?.endedAt).not.toBeNull();
    expect(payloads.length).toBeGreaterThanOrEqual(1);
    const payload = JSON.parse(payloads[0]);
    expect(payload).toEqual(expect.objectContaining({ chat_session_id: sessionId, status: 'ended' }));
  });

  it('TestEndChatSessionRejectsForeignSession', async () => {
    const ownerId = '22222222-2222-2222-2222-222222222222';
    const sessionId = await seedSession({ userId: ownerId, status: 'active' });
    mockUserId = '33333333-3333-3333-3333-333333333333';
    const { endChatSession } = await import('./chat');
    const { ChatError, ChatErrorKind } = await import('./chat.errors');

    await expect(endChatSession(sessionId)).rejects.toThrow(ChatError);
    try {
      await endChatSession(sessionId);
    } catch (err: unknown) {
      expect((err as { kind: string }).kind).toBe(ChatErrorKind.SessionNotFound);
    }
  });

  it('TestEndChatSessionIdempotentForEnded', async () => {
    const sessionId = await seedSession({ userId: mockUserId, status: 'ended' });
    const { endChatSession } = await import('./chat');

    const { payloads } = await captureNotify('work.chat.session_ended', async () => {
      await endChatSession(sessionId);
    });
    expect(payloads.length).toBe(0);
    const row = await readSession(sessionId);
    expect(row?.status).toBe('ended');
  });

  it('TestEndChatSessionRejectsAborted', async () => {
    const sessionId = await seedSession({ userId: mockUserId, status: 'aborted' });
    const { endChatSession } = await import('./chat');
    const { ChatErrorKind } = await import('./chat.errors');

    try {
      await endChatSession(sessionId);
      throw new Error('expected throw');
    } catch (err: unknown) {
      expect((err as { kind: string }).kind).toBe(ChatErrorKind.SessionEnded);
    }
  });
});

describe('chat actions — archive / unarchive', () => {
  it('TestArchiveChatSessionFlipsFlag', async () => {
    const sessionId = await seedSession({ userId: mockUserId });
    const { archiveChatSession } = await import('./chat');

    const { payloads } = await captureNotify('work.chat.session_ended', async () => {
      await archiveChatSession(sessionId);
    });
    expect(payloads.length).toBe(0);
    const row = await readSession(sessionId);
    expect(row?.isArchived).toBe(true);
  });

  it('TestUnarchiveChatSessionFlipsFlag', async () => {
    const sessionId = await seedSession({ userId: mockUserId, isArchived: true });
    const { unarchiveChatSession } = await import('./chat');

    await unarchiveChatSession(sessionId);
    const row = await readSession(sessionId);
    expect(row?.isArchived).toBe(false);
  });
});

describe('chat actions — deleteChatSession', () => {
  it('TestDeleteChatSessionRemovesRow', async () => {
    const sessionId = await seedSession({ userId: mockUserId });
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      for (let i = 0; i < 3; i++) {
        await sql`
          INSERT INTO chat_messages (session_id, turn_index, role, status, content)
          VALUES (${sessionId}, ${i}, 'operator', 'completed', ${'turn ' + i})
        `;
      }
    } finally {
      await sql.end();
    }

    const { deleteChatSession } = await import('./chat');
    await deleteChatSession(sessionId);
    expect(await readSession(sessionId)).toBeNull();

    const verify = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await verify<{ count: string }[]>`SELECT COUNT(*)::text AS count FROM chat_messages WHERE session_id = ${sessionId}`;
      expect(rows[0].count).toBe('0');
    } finally {
      await verify.end();
    }
  });

  it('TestDeleteChatSessionEmitsNotify', async () => {
    const sessionId = await seedSession({ userId: mockUserId });
    const { deleteChatSession } = await import('./chat');

    const { payloads } = await captureNotify('work.chat.session_deleted', async () => {
      await deleteChatSession(sessionId);
    });
    expect(payloads.length).toBeGreaterThanOrEqual(1);
    const payload = JSON.parse(payloads[0]);
    expect(payload).toEqual(
      expect.objectContaining({ chat_session_id: sessionId, actor_user_id: mockUserId }),
    );
    // Rule 6: payload must NOT carry message content keys.
    expect(payload.content).toBeUndefined();
    expect(payload.message_id).toBeUndefined();
  });

  it('TestDeleteChatSessionPreservesVaultLog', async () => {
    const sessionId = await seedSession({ userId: mockUserId });
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      // Seed a vault_access_log row referencing the session via JSONB
      // metadata. The FK is JSON-deep, not a Postgres FK, so the
      // session DELETE must not cascade to it (FR-236). Build the
      // metadata via jsonb_build_object so the cast lands on
      // server-side construction (avoids the parameter-type-inference
      // pitfall postgres-js hits with bare string-to-jsonb casts).
      await sql`
        INSERT INTO vault_access_log (secret_path, customer_id, outcome, agent_instance_id, metadata)
        VALUES (
          '/test/SECRET',
          '00000000-0000-0000-0000-000000000001'::uuid,
          'success', NULL,
          jsonb_build_object('chat_session_id', ${sessionId}::text, 'actor_user_id', ${mockUserId}::text)
        )
      `;
    } finally {
      await sql.end();
    }

    const { deleteChatSession } = await import('./chat');
    await deleteChatSession(sessionId);

    const verify = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await verify<{ id: string }[]>`
        SELECT id FROM vault_access_log
         WHERE metadata->>'chat_session_id' = ${sessionId}
      `;
      expect(rows.length).toBe(1);
    } finally {
      await verify.end();
    }
  });
});

describe('chat actions — createEmptyChatSession', () => {
  it('TestCreateEmptyChatSessionInsertsRowOnly', async () => {
    const { createEmptyChatSession } = await import('./chat');
    const { payloads } = await captureNotify('work.chat.session_started', async () => {
      const { sessionId } = await createEmptyChatSession();
      const row = await readSession(sessionId);
      expect(row?.status).toBe('active');
    });
    expect(payloads.length).toBe(0);

    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ count: string }[]>`SELECT COUNT(*)::text AS count FROM chat_messages`;
      expect(rows[0].count).toBe('0');
    } finally {
      await sql.end();
    }
  });

  it('TestCreateEmptyChatSessionRapidCalls', async () => {
    const { createEmptyChatSession } = await import('./chat');
    const ids = await Promise.all(Array.from({ length: 5 }, () => createEmptyChatSession()));
    const distinct = new Set(ids.map((x) => x.sessionId));
    expect(distinct.size).toBe(5);

    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    try {
      const rows = await sql<{ count: string }[]>`SELECT COUNT(*)::text AS count FROM chat_sessions WHERE started_by_user_id = ${mockUserId}`;
      expect(rows[0].count).toBe('5');
    } finally {
      await sql.end();
    }
  });
});

describe('chat actions — requireSessionOwner ownership gate', () => {
  it('TestRequireSessionOwnerCollapsesNonOwnerToNotFound', async () => {
    const ownerId = '99999999-9999-9999-9999-999999999999';
    const sessionId = await seedSession({ userId: ownerId });
    mockUserId = '88888888-8888-8888-8888-888888888888';
    const { archiveChatSession } = await import('./chat');
    const { ChatErrorKind } = await import('./chat.errors');

    try {
      await archiveChatSession(sessionId);
      throw new Error('expected throw');
    } catch (err: unknown) {
      expect((err as { kind: string }).kind).toBe(ChatErrorKind.SessionNotFound);
    }
  });
});
