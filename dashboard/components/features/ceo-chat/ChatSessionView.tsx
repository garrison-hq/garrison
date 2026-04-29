'use client';

// M5.2 — ChatSessionView (plan §1.16). Client component that composes
// the per-session pieces:
//   ChatTopbarStrip (with IdlePill slot + thread breadcrumb)
//   ThreadHeader
//   MessageStream
//   Composer
//
// All live state (status, terminals, partials) flows through
// useChatStream + the supplied initial server-fetched session row.
// After each terminal commit the parent page re-fetches via Next
// router.refresh() so the supervisor-side session.status flip + total
// cost rollup land on the next render.

import { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { ChatTopbarStrip } from './ChatTopbarStrip';
import { ThreadHeader } from './ThreadHeader';
import { IdlePill } from './IdlePill';
import { MessageStream } from './MessageStream';
import { Composer } from './Composer';
import { ChatErrorBlock } from './ChatErrorBlock';
import {
  EmptyCurrentThreadHint,
  EndedThreadEmptyState,
} from './EmptyStates';
import { createEmptyChatSession } from '@/lib/actions/chat';
import { useChatStream } from '@/lib/sse/chatStream';

interface ChatSessionViewProps {
  sessionId: string;
  session: {
    id: string;
    status: string;
    isArchived: boolean;
    startedAt: string;
    totalCostUsd: string | number;
  };
  threadNumber: number | null;
  initialMessages: Array<{
    id: string;
    turnIndex: number;
    role: string;
    status: string;
    content: string | null;
    costUsd: string | number | null;
    errorKind: string | null;
    rawEventEnvelope: unknown;
  }>;
  hasMore: boolean;
  palaceAgeMs: number | null;
  modelBadge: string | null;
}

export function ChatSessionView({
  sessionId,
  session,
  threadNumber,
  initialMessages,
  hasMore,
  palaceAgeMs,
  modelBadge,
}: Readonly<ChatSessionViewProps>) {
  const router = useRouter();
  const stream = useChatStream(sessionId);
  const [refocusKey, setRefocusKey] = useState(0);

  // Each terminal commit triggers a server re-render so the session
  // row + cost rollup are fresh; also bumps the refocus key for
  // Composer's auto-focus per FR-218.
  useEffect(() => {
    if (stream.terminals.size === 0) return;
    setRefocusKey((k) => k + 1);
    router.refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stream.terminals.size]);

  const turnCount = useMemo(
    () => new Set(initialMessages.map((m) => m.turnIndex)).size + Math.max(0, stream.terminals.size - countAssistantTerminalsInInitial(initialMessages)),
    [initialMessages, stream.terminals.size],
  );

  // Latest assistant errorKind drives Composer disabled-cost-cap
  // gating (T010 + T011). Take the most recent assistant row, prefer
  // the live terminal state if one has landed.
  const latestErrorKind = useMemo(() => {
    const assistantRows = [...initialMessages]
      .filter((m) => m.role === 'assistant')
      .sort((a, b) => a.turnIndex - b.turnIndex);
    const last = assistantRows.at(-1);
    if (!last) return null;
    const terminal = stream.terminals.get(last.id);
    return terminal?.errorKind ?? last.errorKind;
  }, [initialMessages, stream.terminals]);

  // Streaming gate — disable composer if there's an in-flight assistant.
  // partialDeltas survive after the terminal arrives by design (renderer
  // prefers terminal but keeps the partial for forensic / scroll-back per
  // plan §1.5). So a non-empty partialDeltas alone doesn't mean
  // "streaming" — we need at least one partial whose messageId has NOT
  // received a terminal yet. Otherwise the composer locks forever once a
  // turn has streamed.
  const isStreaming = useMemo(() => {
    if (initialMessages.some(
      (m) => m.role === 'assistant' && (m.status === 'pending' || m.status === 'streaming'),
    )) return true;
    for (const messageId of stream.partialDeltas.keys()) {
      if (!stream.terminals.has(messageId)) return true;
    }
    return false;
  }, [initialMessages, stream.partialDeltas, stream.terminals]);

  const handleCreateNew = async () => {
    const { sessionId: nextId } = await createEmptyChatSession();
    router.push(`/chat/${nextId}`);
  };

  const idlePill = <IdlePill status={session.status} />;
  const threadLabel = threadNumber === null ? `thread ${sessionId.slice(-6)}` : `thread #${threadNumber}`;

  return (
    <>
      <ChatTopbarStrip breadcrumbSuffix={threadLabel} idlePill={idlePill} />
      <ThreadHeader
        sessionId={sessionId}
        threadNumber={threadNumber ?? 0}
        startedAt={session.startedAt}
        turnCount={turnCount}
        totalCostUsd={session.totalCostUsd}
        status={session.status}
        isArchived={session.isArchived}
      />
      {initialMessages.length === 0 ? (
        <div className="flex-1 flex items-center justify-center p-8">
          <EmptyCurrentThreadHint />
        </div>
      ) : (
        <MessageStream
          sessionId={sessionId}
          initialMessages={initialMessages.map((m) => ({
            ...m,
            role: m.role as 'operator' | 'assistant',
          }))}
          hasMore={hasMore}
          renderErrorBlock={(kind: string) => <ChatErrorBlock errorKind={kind} />}
          endedFooter={
            session.status === 'ended' ? (
              <EndedThreadEmptyState onCreateNew={() => void handleCreateNew()} />
            ) : null
          }
          modelBadge={modelBadge}
        />
      )}
      <Composer
        sessionId={sessionId}
        status={session.status}
        isStreaming={isStreaming}
        latestErrorKind={latestErrorKind}
        palaceAgeMs={palaceAgeMs}
        refocusKey={refocusKey}
        onSent={() => router.refresh()}
      />
    </>
  );
}

function countAssistantTerminalsInInitial(
  msgs: Array<{ role: string; status: string }>,
): number {
  return msgs.filter(
    (m) => m.role === 'assistant' && (m.status === 'completed' || m.status === 'failed' || m.status === 'aborted'),
  ).length;
}
