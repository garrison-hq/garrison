// M5.1 chat read-side queries. Used by the dashboard's session list +
// per-session detail surfaces (M5.2 ships the UI; M5.1 ships these
// helpers so the SSE route + tests have a typed lookup path).

import { eq, desc, asc, sum } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { chatSessions, chatMessages } from '@/drizzle/schema.supervisor';

export async function listSessionsForUser(userId: string, limit = 50) {
  return appDb
    .select()
    .from(chatSessions)
    .where(eq(chatSessions.startedByUserId, userId))
    .orderBy(desc(chatSessions.startedAt))
    .limit(limit);
}

export async function getSessionWithMessages(sessionId: string) {
  const [session] = await appDb
    .select()
    .from(chatSessions)
    .where(eq(chatSessions.id, sessionId))
    .limit(1);
  if (!session) return null;
  const messages = await appDb
    .select()
    .from(chatMessages)
    .where(eq(chatMessages.sessionId, sessionId))
    .orderBy(asc(chatMessages.turnIndex));
  return { session, messages };
}

export async function getRunningCost(sessionId: string): Promise<number> {
  const [{ total }] = (await appDb
    .select({ total: sum(chatMessages.costUsd) })
    .from(chatMessages)
    .where(eq(chatMessages.sessionId, sessionId))) as Array<{ total: string | null }>;
  if (!total) return 0;
  return Number(total);
}
