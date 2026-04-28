'use server';

// M5.1 chat server actions per plan §"Public interfaces > dashboard/
// lib/actions/chat.ts". Two entry points:
//
//   startChatSession(content) — creates a fresh chat_sessions row +
//     the first chat_messages row (turn_index=0, role='operator',
//     status='completed') in one transaction, then emits
//     pg_notify('chat.message.sent', message_id) so the supervisor
//     listener wakes the chat worker.
//
//   sendChatMessage(sessionId, content) — INSERTs a subsequent
//     operator chat_messages row with monotonic turn_index assignment
//     (COALESCE(MAX, -1) + 1 inside the tx); UNIQUE(session_id,
//     turn_index) collisions retry once.
//
// Both validate the better-auth session + content shape (non-empty,
// ≤100KB defensive bound). Session must be status='active' for
// sendChatMessage.

import { eq, and, sql, desc } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { chatSessions, chatMessages } from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';

const MAX_CONTENT_BYTES = 100 * 1024;

export class ChatError extends Error {
  constructor(public readonly kind: ChatErrorKind, message?: string) {
    super(message ?? kind);
    this.name = 'ChatError';
  }
}
export const ChatErrorKind = {
  EmptyContent: 'empty_content',
  ContentTooLarge: 'content_too_large',
  SessionEnded: 'session_ended',
  SessionNotFound: 'session_not_found',
  TurnIndexCollision: 'turn_index_collision',
} as const;
export type ChatErrorKind = (typeof ChatErrorKind)[keyof typeof ChatErrorKind];

function validateContent(content: string): void {
  if (!content || content.length === 0) {
    throw new ChatError(ChatErrorKind.EmptyContent);
  }
  if (Buffer.byteLength(content, 'utf-8') > MAX_CONTENT_BYTES) {
    throw new ChatError(ChatErrorKind.ContentTooLarge);
  }
}

async function requireUserId(): Promise<string> {
  const session = await getSession();
  if (!session) throw new AuthError(AuthErrorKind.NoSession);
  return session.user.id;
}

export async function startChatSession(
  content: string,
): Promise<{ sessionId: string; messageId: string }> {
  validateContent(content);
  const userId = await requireUserId();

  const result = await appDb.transaction(async (tx) => {
    const [sess] = await tx
      .insert(chatSessions)
      .values({ startedByUserId: userId })
      .returning({ id: chatSessions.id });

    const [msg] = await tx
      .insert(chatMessages)
      .values({
        sessionId: sess.id,
        turnIndex: 0,
        role: 'operator',
        status: 'completed',
        content,
      })
      .returning({ id: chatMessages.id });

    await tx.execute(sql`SELECT pg_notify('chat.message.sent', ${msg.id})`);
    return { sessionId: sess.id, messageId: msg.id };
  });

  return result;
}

export async function sendChatMessage(
  sessionId: string,
  content: string,
): Promise<{ messageId: string }> {
  validateContent(content);
  await requireUserId();

  // Validate session active.
  const sessRows = await appDb
    .select({ status: chatSessions.status })
    .from(chatSessions)
    .where(eq(chatSessions.id, sessionId))
    .limit(1);
  if (sessRows.length === 0) {
    throw new ChatError(ChatErrorKind.SessionNotFound);
  }
  if (sessRows[0].status !== 'active') {
    throw new ChatError(ChatErrorKind.SessionEnded);
  }

  // INSERT with atomic turn_index = max+1; one retry on UNIQUE
  // collision (single-operator low-contention).
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const messageId = await appDb.transaction(async (tx) => {
        const [{ next }] = await tx.execute<{ next: number }>(sql`
          SELECT COALESCE(MAX(turn_index), -1) + 1 AS next
            FROM chat_messages
           WHERE session_id = ${sessionId}
        `) as unknown as Array<{ next: number }>;

        const [msg] = await tx
          .insert(chatMessages)
          .values({
            sessionId,
            turnIndex: next,
            role: 'operator',
            status: 'completed',
            content,
          })
          .returning({ id: chatMessages.id });

        await tx.execute(sql`SELECT pg_notify('chat.message.sent', ${msg.id})`);
        return msg.id;
      });
      return { messageId };
    } catch (err) {
      if (
        attempt === 0 &&
        err instanceof Error &&
        err.message.includes('chat_messages_session_id_turn_index_key')
      ) {
        continue;
      }
      throw err;
    }
  }
  throw new ChatError(ChatErrorKind.TurnIndexCollision);
}

// ────────────────────────────────────────────────────────────────────
// M5.2 — operator-driven session housekeeping (plan §1.3).
//
// Five new mutating actions live here alongside startChatSession +
// sendChatMessage:
//
//   createEmptyChatSession()           — "+ New thread" CTA. Inserts a
//                                        chat_sessions row only; no
//                                        operator message; no spawn.
//   endChatSession(sessionId)          — operator-driven close (FR-082).
//   archiveChatSession(sessionId)      — flag for thread housekeeping.
//   unarchiveChatSession(sessionId)    — reverses archive.
//   deleteChatSession(sessionId)       — hard delete; FK CASCADE wipes
//                                        chat_messages; vault_access_log
//                                        rows referencing the session
//                                        via JSONB metadata survive
//                                        (FR-236).
//
// Plus one user-facing query wrapper:
//
//   getRecentThreadsForCurrentUser(limit?)  — used by ThreadHistorySubnav.
//
// Every mutating action verifies the caller owns the session via
// requireSessionOwner() before any write. Non-owner collapses to
// ChatError(SessionNotFound) per plan §1.3 — no separate NotOwner kind
// (enumeration-resistance, slate item 11).
// ────────────────────────────────────────────────────────────────────

type ChatSessionRow = typeof chatSessions.$inferSelect;

// requireSessionOwner is the single ownership-gate helper reused by all
// four mutating actions. Both "session does not exist" and "session
// exists but is not owned by current user" collapse to SessionNotFound
// per plan §1.3 to avoid an enumeration vector.
async function requireSessionOwner(
  sessionId: string,
): Promise<{ userId: string; session: ChatSessionRow }> {
  const userId = await requireUserId();
  const rows = await appDb
    .select()
    .from(chatSessions)
    .where(eq(chatSessions.id, sessionId))
    .limit(1);
  if (rows.length === 0 || rows[0].startedByUserId !== userId) {
    throw new ChatError(ChatErrorKind.SessionNotFound);
  }
  return { userId, session: rows[0] };
}

// createEmptyChatSession opens a fresh thread without an initial
// operator message. Used by the "+ New thread" CTA. No pg_notify.
// No cost. Per FR-061 carryover the cost cap is reactive at next-spawn
// time, so empty rows churn cheaply if the operator clicks rapidly.
export async function createEmptyChatSession(): Promise<{ sessionId: string }> {
  const userId = await requireUserId();
  const [sess] = await appDb
    .insert(chatSessions)
    .values({ startedByUserId: userId })
    .returning({ id: chatSessions.id });
  return { sessionId: sess.id };
}

// endChatSession closes an active thread (FR-082 follow-through). Idempotent
// against already-ended sessions; rejects aborted sessions (operator's
// intent of "close it" is satisfied by the existing aborted state per
// plan §1.3). The supervisor doesn't observe this UPDATE in real time —
// any in-flight assistant turn finishes its terminal write naturally per
// FR-244, and subsequent operator INSERTs on the now-ended session bounce
// with error_kind='session_ended' per M5.1 FR-081.
export async function endChatSession(
  sessionId: string,
): Promise<{ session: ChatSessionRow }> {
  const { session } = await requireSessionOwner(sessionId);

  if (session.status === 'aborted') {
    throw new ChatError(ChatErrorKind.SessionEnded);
  }

  // Idempotent for already-ended sessions: no UPDATE, no notify.
  if (session.status === 'ended') {
    return { session };
  }

  const result = await appDb.transaction(async (tx) => {
    const updated = await tx
      .update(chatSessions)
      .set({ status: 'ended', endedAt: sql`NOW()` })
      .where(and(eq(chatSessions.id, sessionId), eq(chatSessions.status, 'active')))
      .returning();
    if (updated.length === 0) {
      // Race: another writer flipped status between requireSessionOwner
      // and this UPDATE. Re-read and return the current row without
      // emitting a duplicate notify.
      const [current] = await tx
        .select()
        .from(chatSessions)
        .where(eq(chatSessions.id, sessionId))
        .limit(1);
      return current;
    }
    await tx.execute(sql`SELECT pg_notify(
      'work.chat.session_ended',
      json_build_object('chat_session_id', ${sessionId}, 'status', 'ended')::text
    )`);
    return updated[0];
  });
  return { session: result };
}

// archiveChatSession flips chat_sessions.is_archived=true. No pg_notify
// per FR-234 — archive is a display flag, not an activity-feed event.
// Idempotent against any session state.
export async function archiveChatSession(sessionId: string): Promise<void> {
  await requireSessionOwner(sessionId);
  await appDb
    .update(chatSessions)
    .set({ isArchived: true })
    .where(eq(chatSessions.id, sessionId));
}

// unarchiveChatSession is the inverse of archiveChatSession.
export async function unarchiveChatSession(sessionId: string): Promise<void> {
  await requireSessionOwner(sessionId);
  await appDb
    .update(chatSessions)
    .set({ isArchived: false })
    .where(eq(chatSessions.id, sessionId));
}

// deleteChatSession hard-deletes the session. The FK on chat_messages
// recreated in T001 cascades the transcript. vault_access_log rows
// referencing the session via metadata.chat_session_id survive
// (FR-236) — their FK is JSON-deep, not a Postgres FK, so cascade does
// not reach there.
//
// Emits pg_notify('work.chat.session_deleted', { chat_session_id, actor_user_id })
// — IDs only, no message content (FR-321 + Rule 6 chat-audit discipline).
export async function deleteChatSession(sessionId: string): Promise<void> {
  const { userId } = await requireSessionOwner(sessionId);
  await appDb.transaction(async (tx) => {
    await tx.delete(chatSessions).where(eq(chatSessions.id, sessionId));
    await tx.execute(sql`SELECT pg_notify(
      'work.chat.session_deleted',
      json_build_object('chat_session_id', ${sessionId}, 'actor_user_id', ${userId})::text
    )`);
  });
}

// getRecentThreadsForCurrentUser is the action wrapper used by the
// ThreadHistorySubnav for its server-rendered initial state. Reads
// the auth session to derive userId — kept in lib/actions/chat.ts
// rather than lib/queries/chat.ts because actions can call queries
// but queries are pure-data helpers per M3 convention.
export async function getRecentThreadsForCurrentUser(
  limit = 10,
): Promise<ChatSessionRow[]> {
  const userId = await requireUserId();
  return appDb
    .select()
    .from(chatSessions)
    .where(
      and(
        eq(chatSessions.startedByUserId, userId),
        eq(chatSessions.isArchived, false),
      ),
    )
    .orderBy(desc(chatSessions.startedAt))
    .limit(limit);
}
