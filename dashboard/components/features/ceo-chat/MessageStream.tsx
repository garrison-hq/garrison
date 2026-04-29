'use client';

// M5.2 — MessageStream (plan §1.7 + §1.8).
//
// Renders chat_messages rows in (turn_index, role) order, merging the
// initial server-fetched list with the live partial deltas + terminal
// events from useChatStream. The renderer prefers terminals[messageId]
// .content when present; otherwise falls back to partialDeltas
// .get(messageId) for in-flight bubbles.
//
// Sticky-bottom auto-scroll: useStickyBottom auto-scrolls on every new
// content arrival when the operator is within 40px of the bottom.
// Scrolling up >100px disengages stickiness and surfaces a "↓ N new"
// pill counting terminal commits since scroll-up.
//
// Pagination: server-rendered initial fetch returns 50 most-recent
// turns. When hasMore=true the stream renders a "Load earlier" button
// at the top that requests more history via the parent's loadEarlier
// callback. Each "Load earlier" call appends to the top of the stream.

import { useEffect, useMemo, useRef, useState } from 'react';
import { MessageBubble } from './MessageBubble';
import { useStickyBottom } from './useStickyBottom';
import { useChatStream } from '@/lib/sse/chatStream';

interface ServerMessage {
  id: string;
  turnIndex: number;
  role: 'operator' | 'assistant' | string;
  status: string;
  content: string | null;
  costUsd: string | number | null;
  errorKind: string | null;
  rawEventEnvelope: unknown;
}

interface MessageStreamProps {
  sessionId: string;
  initialMessages: ServerMessage[];
  hasMore: boolean;
  loadEarlier?: (beforeTurnIndex: number) => Promise<ServerMessage[]>;
  /** Optional renderer for the inline error block; T011 wires the
   *  CHAT_ERROR_DISPLAY table through here. */
  renderErrorBlock?: (errorKind: string) => React.ReactNode;
  /** Stub renderer for the "thread ended" empty state below the
   *  stream. T011 swaps this in. */
  endedFooter?: React.ReactNode;
  /** Cosmetic CEO model name (passed straight to MessageBubble). */
  modelBadge?: string | null;
}

const SCROLL_UP_PILL_THRESHOLD_PX = 100;

export function MessageStream({
  sessionId,
  initialMessages,
  hasMore: hasMoreInitial,
  loadEarlier,
  renderErrorBlock,
  endedFooter,
  modelBadge,
}: Readonly<MessageStreamProps>) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const { isStuck, scrollToBottom } = useStickyBottom(containerRef);
  const stream = useChatStream(sessionId);
  const [messages, setMessages] = useState<ServerMessage[]>(initialMessages);
  const [hasMore, setHasMore] = useState(hasMoreInitial);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [newPillCount, setNewPillCount] = useState(0);
  const lastTerminalsSize = useRef<number>(0);
  const lastDistanceFromBottom = useRef<number>(0);

  // Reset local state when sessionId changes — switching threads
  // wipes the in-component buffer; useChatStream handles its own
  // EventSource cleanup.
  useEffect(() => {
    setMessages(initialMessages);
    setHasMore(hasMoreInitial);
    setNewPillCount(0);
    lastTerminalsSize.current = 0;
    // Initial server-render arrives at the top of the stream; jump to
    // bottom on mount so the operator sees the most recent turns.
    queueMicrotask(() => scrollToBottom());
  }, [sessionId, initialMessages, hasMoreInitial, scrollToBottom]);

  // Track "new since scroll-up" via terminal-commit count.
  useEffect(() => {
    const newTerminals = stream.terminals.size;
    if (newTerminals > lastTerminalsSize.current) {
      const delta = newTerminals - lastTerminalsSize.current;
      lastTerminalsSize.current = newTerminals;
      if (!isStuck) {
        // Only count when operator is scrolled up — when stuck we
        // auto-scroll, no pill needed.
        setNewPillCount((c) => c + delta);
      }
    }
    // When stuck, every new content arrival re-pins to bottom.
    if (isStuck) {
      const el = containerRef.current;
      if (el) el.scrollTo({ top: el.scrollHeight });
    }
  }, [stream.terminals, stream.partialDeltas, isStuck]);

  // Track scroll distance for pill visibility threshold (>100px).
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onScroll = () => {
      lastDistanceFromBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight;
    };
    el.addEventListener('scroll', onScroll, { passive: true });
    return () => el.removeEventListener('scroll', onScroll);
  }, []);

  const handleLoadEarlier = async () => {
    if (!loadEarlier || loadingEarlier || messages.length === 0) return;
    const oldest = messages[0].turnIndex;
    setLoadingEarlier(true);
    try {
      const earlier = await loadEarlier(oldest);
      if (earlier.length > 0) {
        setMessages((prev) => [...earlier, ...prev]);
      }
      if (earlier.length < 50) setHasMore(false);
    } finally {
      setLoadingEarlier(false);
    }
  };

  const handleNewPillClick = () => {
    setNewPillCount(0);
    scrollToBottom();
  };

  // Project messages into render rows: each row carries the merged
  // content (terminal preferred, partial fallback).
  const rows = useMemo(() => {
    return [...messages].sort((a, b) => a.turnIndex - b.turnIndex).map((m) => {
      const terminal = stream.terminals.get(m.id);
      const partial = stream.partialDeltas.get(m.id);
      const mergedContent = terminal?.content ?? m.content ?? partial ?? null;
      const mergedStatus = terminal?.status ?? m.status;
      const isStreaming = m.role === 'assistant' && (mergedStatus === 'streaming' || (partial !== undefined && terminal === undefined));
      const errorKindFinal = terminal?.errorKind ?? m.errorKind;
      return { ...m, mergedContent, mergedStatus, isStreaming, errorKindFinal };
    });
  }, [messages, stream.terminals, stream.partialDeltas]);

  const showNewPill = !isStuck && newPillCount > 0 && lastDistanceFromBottom.current > SCROLL_UP_PILL_THRESHOLD_PX;

  return (
    <div className="flex-1 flex flex-col min-h-0 relative">
      <div
        ref={containerRef}
        className="flex-1 overflow-auto px-4 py-4 space-y-3"
        data-testid="chat-message-stream"
      >
        {hasMore ? (
          <div className="flex justify-center">
            <button
              type="button"
              onClick={handleLoadEarlier}
              disabled={loadingEarlier}
              className="px-3 py-1 text-[11px] text-text-3 hover:text-text-1 underline-offset-4 hover:underline disabled:opacity-60"
              data-testid="chat-load-earlier"
            >
              {loadingEarlier ? 'loading…' : 'Load earlier'}
            </button>
          </div>
        ) : null}

        {rows.map((row) => (
          <MessageBubble
            key={row.id}
            role={row.role as 'operator' | 'assistant'}
            status={row.mergedStatus}
            content={row.mergedContent}
            costUsd={row.role === 'assistant' ? row.costUsd : undefined}
            modelBadge={modelBadge}
            streaming={row.isStreaming}
            errorBlock={
              row.errorKindFinal && renderErrorBlock
                ? renderErrorBlock(row.errorKindFinal)
                : undefined
            }
          />
        ))}

        {endedFooter ? <div className="pt-4">{endedFooter}</div> : null}
      </div>

      {showNewPill ? (
        <button
          type="button"
          onClick={handleNewPillClick}
          className="absolute bottom-3 right-4 z-10 bg-surface-3 border border-border-2 rounded-full px-3 py-1 text-[11px] text-text-1 shadow-md hover:bg-surface-2"
          data-testid="chat-new-messages-pill"
        >
          ↓ {newPillCount} new
        </button>
      ) : null}
    </div>
  );
}
