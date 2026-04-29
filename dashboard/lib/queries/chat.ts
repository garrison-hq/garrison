// M5.1 + M5.2 chat read-side queries. Used by the dashboard's session
// list + per-session detail surfaces (M5.1 ships the helpers; M5.2
// extends listSessionsForUser with archived filter, paginates
// getSessionWithMessages for long threads, and adds
// getMostRecentMempalaceCallAge for the composer's palace-live chip).

import { eq, and, lt, desc, sum } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { chatSessions, chatMessages } from '@/drizzle/schema.supervisor';

type ChatSessionRow = typeof chatSessions.$inferSelect;
type ChatMessageRow = typeof chatMessages.$inferSelect;

export interface ListSessionsForUserOptions {
  archived?: boolean;
  limit?: number;
}

// listSessionsForUser is backwards-compatible with M5.1 callers that
// passed (userId, limit). Two call shapes are supported:
//   listSessionsForUser(userId)
//   listSessionsForUser(userId, 25)                         (M5.1)
//   listSessionsForUser(userId, { archived: true })         (M5.2)
//   listSessionsForUser(userId, { archived: false, limit: 10 })
// Default behaviour: archived=false, limit=50 — the partial index
// idx_chat_sessions_user_active_unarchived (added in T001) makes the
// archived=false path single-index-scan fast.
export async function listSessionsForUser(
  userId: string,
  optionsOrLimit?: ListSessionsForUserOptions | number,
): Promise<ChatSessionRow[]> {
  let archived = false;
  let limit = 50;
  if (typeof optionsOrLimit === 'number') {
    limit = optionsOrLimit;
  } else if (optionsOrLimit) {
    if (typeof optionsOrLimit.archived === 'boolean') {
      archived = optionsOrLimit.archived;
    }
    if (typeof optionsOrLimit.limit === 'number') {
      limit = optionsOrLimit.limit;
    }
  }
  return appDb
    .select()
    .from(chatSessions)
    .where(
      and(
        eq(chatSessions.startedByUserId, userId),
        eq(chatSessions.isArchived, archived),
      ),
    )
    .orderBy(desc(chatSessions.startedAt))
    .limit(limit);
}

export interface GetSessionWithMessagesOptions {
  limit?: number;
  beforeTurnIndex?: number;
}

export interface GetSessionWithMessagesResult {
  session: ChatSessionRow;
  messages: ChatMessageRow[];
  hasMore: boolean;
}

// getSessionWithMessages returns the most recent N messages by
// turn_index DESC then reverses to ASC for render. hasMore is true if
// the session has older messages than the page returned. M5.2 paginates
// long threads (plan §1.8) — long-thread TTI < 2s per SC-221; the
// initial DOM stays bounded to 50 turns even for 100-turn sessions.
export async function getSessionWithMessages(
  sessionId: string,
  options?: GetSessionWithMessagesOptions,
): Promise<GetSessionWithMessagesResult | null> {
  const limit = options?.limit ?? 50;
  const beforeTurnIndex = options?.beforeTurnIndex;

  const [session] = await appDb
    .select()
    .from(chatSessions)
    .where(eq(chatSessions.id, sessionId))
    .limit(1);
  if (!session) return null;

  const whereClause = beforeTurnIndex === undefined
    ? eq(chatMessages.sessionId, sessionId)
    : and(
        eq(chatMessages.sessionId, sessionId),
        lt(chatMessages.turnIndex, beforeTurnIndex),
      );

  // Fetch limit+1 to detect hasMore without an extra COUNT roundtrip.
  const rows = await appDb
    .select()
    .from(chatMessages)
    .where(whereClause)
    .orderBy(desc(chatMessages.turnIndex))
    .limit(limit + 1);

  const hasMore = rows.length > limit;
  const page = hasMore ? rows.slice(0, limit) : rows;
  // Reverse to ASC for render (oldest at top of page).
  const messages = page.slice().reverse();
  return { session, messages, hasMore };
}

export async function getRunningCost(sessionId: string): Promise<number> {
  const [{ total }] = (await appDb
    .select({ total: sum(chatMessages.costUsd) })
    .from(chatMessages)
    .where(eq(chatMessages.sessionId, sessionId))) as Array<{ total: string | null }>;
  if (!total) return 0;
  return Number(total);
}

// getMostRecentMempalaceCallAge powers the composer's PalaceLiveChip
// (FR-283 + plan §1.10). Reads the most recent assistant
// chat_messages.raw_event_envelope for the session and looks for
// successful tool_use/tool_result pairs targeting the mempalace MCP
// server. Returns { ageMs: null } if no successful call has happened
// yet in the session.
//
// Implementation per plan §1.4: SELECT the latest assistant rows then
// parse client-side. Keeps JSONB-parse logic in TypeScript (matches
// M3's "Drizzle returns rows; TS transforms" precedent).
export async function getMostRecentMempalaceCallAge(
  sessionId: string,
): Promise<{ ageMs: number | null }> {
  const rows = await appDb
    .select({
      rawEventEnvelope: chatMessages.rawEventEnvelope,
      terminatedAt: chatMessages.terminatedAt,
      createdAt: chatMessages.createdAt,
    })
    .from(chatMessages)
    .where(
      and(
        eq(chatMessages.sessionId, sessionId),
        eq(chatMessages.role, 'assistant'),
      ),
    )
    .orderBy(desc(chatMessages.turnIndex))
    .limit(5);

  for (const row of rows) {
    const ts = parseSuccessfulMempalaceCallTimestamp(row.rawEventEnvelope, row.terminatedAt ?? row.createdAt);
    if (ts !== null) {
      return { ageMs: Math.max(0, Date.now() - ts) };
    }
  }
  return { ageMs: null };
}

// parseSuccessfulMempalaceCallTimestamp pulls the timestamp of the
// most recent successful mempalace tool call from a raw_event_envelope.
//
// The supervisor stores the envelope as a top-level JSON array of
// stream events (claudeproto.OnStreamEvent accumulator). Each element
// has shape { type, message: { content: [...] } } — the array carries
// `assistant` events whose message.content includes `tool_use` blocks
// (the dispatch) and `user` events whose message.content includes
// `tool_result` blocks (the response).
//
// Detection is two-step:
//   1. Build a Map<tool_use_id, tool_name> from assistant.tool_use blocks
//   2. For each user.tool_result block, check whether the paired
//      tool name starts with `mcp__mempalace__` and is_error is not true
//
// Pre-M5 envelopes / fixtures may carry a `{events: [...]}` shape; we
// fall back to that for parity. Returns null if no successful mempalace
// call is present.
function parseSuccessfulMempalaceCallTimestamp(
  envelope: unknown,
  fallbackAt: string | Date | null,
): number | null {
  if (!envelope) return null;
  const events = extractStreamEvents(envelope);
  const toolUses = collectToolUseNames(events);
  if (!hasSuccessfulMempalaceResult(events, toolUses)) return null;
  return fallbackAt ? new Date(fallbackAt as string | number | Date).getTime() : Date.now();
}

// Pass 1: build tool_use_id → name from assistant.tool_use blocks.
function collectToolUseNames(events: unknown[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const ev of events) {
    if (!isAssistantEvent(ev)) continue;
    for (const block of extractMessageContent(ev)) {
      if (!isToolUseBlock(block)) continue;
      const id = block['id'];
      const name = block['name'];
      if (typeof id === 'string' && typeof name === 'string') {
        out.set(id, name);
      }
    }
  }
  return out;
}

// Pass 2: walk tool_result blocks (user events) in reverse so the
// latest successful mempalace call wins.
function hasSuccessfulMempalaceResult(
  events: unknown[],
  toolUses: Map<string, string>,
): boolean {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i];
    if (!isUserEvent(ev)) continue;
    for (const block of extractMessageContent(ev)) {
      if (!isSuccessfulToolResult(block)) continue;
      const useId = block['tool_use_id'];
      if (typeof useId !== 'string') continue;
      const name = toolUses.get(useId);
      if (name?.startsWith('mcp__mempalace__')) return true;
    }
  }
  return false;
}

function isAssistantEvent(ev: unknown): ev is Record<string, unknown> {
  return isRecord(ev) && ev['type'] === 'assistant';
}

function isUserEvent(ev: unknown): ev is Record<string, unknown> {
  return isRecord(ev) && ev['type'] === 'user';
}

function isToolUseBlock(block: unknown): block is Record<string, unknown> {
  return isRecord(block) && block['type'] === 'tool_use';
}

function isSuccessfulToolResult(block: unknown): block is Record<string, unknown> {
  return isRecord(block) && block['type'] === 'tool_result' && block['is_error'] !== true;
}

function extractStreamEvents(envelope: unknown): unknown[] {
  if (Array.isArray(envelope)) return envelope;
  if (typeof envelope === 'object' && envelope !== null) {
    const e = envelope as { events?: unknown };
    if (Array.isArray(e.events)) return e.events;
  }
  return [];
}

function extractMessageContent(ev: Record<string, unknown>): unknown[] {
  const message = ev['message'];
  if (!isRecord(message)) return [];
  const content = message['content'];
  return Array.isArray(content) ? content : [];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
