import { notFound } from 'next/navigation';
import { ChatTopbarStrip } from '@/components/features/ceo-chat/ChatTopbarStrip';
import { ChatSessionView } from '@/components/features/ceo-chat/ChatSessionView';
import { EmptyState } from '@/components/ui/EmptyState';
import {
  getSessionWithMessages,
  getMostRecentMempalaceCallAge,
} from '@/lib/queries/chat';
import { getRecentThreadsForCurrentUser } from '@/lib/actions/chat';
import { getSession } from '@/lib/auth/session';

// M5.2 — /chat/<sessionId>. Server-renders the initial transcript +
// hands off the live stream to the client-side ChatSessionView (which
// owns useChatStream + MessageStream + Composer). The page itself is
// a Server Component so the initial transcript fetch is uncached and
// authentication-gated.

export const dynamic = 'force-dynamic';

export default async function ChatSessionPage({
  params,
}: Readonly<{ params: Promise<{ sessionId: string }> }>) {
  const { sessionId } = await params;

  if (!sessionId) {
    return (
      <>
        <ChatTopbarStrip />
        <div className="flex-1 flex items-center justify-center p-8">
          <EmptyState description="Pick or create a thread." />
        </div>
      </>
    );
  }

  const me = await getSession();
  if (!me) return notFound();

  const detail = await getSessionWithMessages(sessionId);
  if (!detail) return notFound();

  // Defensive ownership check — server actions enforce this for writes;
  // we mirror it on reads so a guessed UUID can't sniff transcripts.
  if (detail.session.startedByUserId !== me.user.id) return notFound();

  // Per-operator thread number — matched against the recent-threads
  // list. If the session is older than the cached top-N we fall back
  // to the short-uuid suffix; the FR-211 visibility matrix doesn't
  // require thread #N to be authoritative everywhere, only on the
  // active row.
  const recent = await getRecentThreadsForCurrentUser(50).catch(() => []);
  const numbered = recent.find((r) => r.id === sessionId);
  const threadNumber = numbered?.threadNumber ?? null;

  const palaceAge = await getMostRecentMempalaceCallAge(sessionId).catch(() => ({
    ageMs: null,
  }));

  // Most recent assistant message's model name for the cosmetic badge.
  const lastAssistantWithModel = [...detail.messages]
    .reverse()
    .find((m) => m.role === 'assistant' && m.rawEventEnvelope);
  const modelBadge = extractModelFromEnvelope(lastAssistantWithModel?.rawEventEnvelope);

  return (
    <ChatSessionView
      sessionId={sessionId}
      session={{
        id: detail.session.id,
        status: detail.session.status,
        isArchived: detail.session.isArchived,
        startedAt: detail.session.startedAt,
        totalCostUsd: detail.session.totalCostUsd,
      }}
      threadNumber={threadNumber}
      initialMessages={detail.messages.map((m) => ({
        id: m.id,
        turnIndex: m.turnIndex,
        role: m.role,
        status: m.status,
        content: m.content,
        costUsd: m.costUsd,
        errorKind: m.errorKind,
        rawEventEnvelope: m.rawEventEnvelope,
      }))}
      hasMore={detail.hasMore}
      palaceAgeMs={palaceAge.ageMs}
      modelBadge={modelBadge}
    />
  );
}

// Pulls the model name (e.g. "claude-sonnet-4-6") out of the
// supervisor-stored stream envelope. The envelope is a top-level
// JSON array of stream events; assistant events carry the model
// under ev.message.model. Falls back to a legacy {events:[…]} shape
// for fixtures that pre-date the array commit.
function extractModelFromEnvelope(envelope: unknown): string | null {
  const events = collectEvents(envelope);
  for (const ev of events) {
    if (!isRecord(ev)) continue;
    if (ev['type'] !== 'assistant') continue;
    const message = ev['message'];
    if (!isRecord(message)) continue;
    const model = message['model'];
    if (typeof model === 'string' && model.length > 0) return model;
  }
  return null;
}

function collectEvents(envelope: unknown): unknown[] {
  if (Array.isArray(envelope)) return envelope;
  if (isRecord(envelope) && Array.isArray(envelope['events'])) {
    return envelope['events'];
  }
  return [];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
