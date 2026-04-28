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

import { eq, sql } from 'drizzle-orm';
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
