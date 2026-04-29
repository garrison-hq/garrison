'use client';

// M5.2 — useChatStream(sessionId) hook (plan §1.5).
//
// A chat-specific SSE consumer that mirrors the M3 listener's 5-state
// machine (dormant/connecting/live/backoff/idle-grace) without modifying
// the M3 lib/sse/listener.ts singleton — per plan §R1, the chat
// listener is a separate instance, not a parameterised fork.
//
// Lifecycle:
//   dormant   — initial mount before subscribe()
//   connecting — opening EventSource
//   live      — open, accumulating deltas
//   backoff   — error mid-stream; waiting before reconnect
//   idle-grace — hook unmounted; holding 60s in case operator
//                re-subscribes (back-button, tab-switch)
//
// Backoff: 100ms → 200 → 400 → … → 30000ms cap (matches M3 listener).
//
// Event-source contract (matches app/api/sse/chat/route.ts):
//   delta        { message_id, seq, delta_text }
//   terminal     { messageId, status, content, errorKind, costUsd }
//   session_ended { chat_session_id, status }
//
// Dedupe: the hook keeps a Set<string> of (messageId:seq) keys; replayed
// deltas (the SSE route streams Last-Event-ID-keyed events; the browser
// re-sends Last-Event-ID after a reconnect by default) are dropped
// silently rather than re-rendered.
//
// Partial buffer survival: on disconnect the partialDeltas Map keeps
// the in-flight buffer; on reconnect the consumer relies on the SSE
// route's row-state-read mechanism (see app/api/sse/chat/route.ts
// terminal branch which reads getSessionWithMessages on the work.chat
// .message_sent notify) to receive any committed terminal it missed.
// Partial deltas themselves are NOT replayed — that's per amended FR-261.
//
// The state machine is extracted into a pure ChatStreamMachine object so
// unit tests can exercise dedupe, terminal handling, idle-grace, and
// backoff schedules without booting a DOM. The React hook is a thin
// adapter: useEffect drives the EventSource lifecycle and forwards
// events into the same machine.

import { useEffect, useReducer, useRef } from 'react';

export type ChatStreamState =
  | 'dormant'
  | 'connecting'
  | 'live'
  | 'backoff'
  | 'idle-grace';

export interface ChatTerminalEvent {
  messageId: string;
  /** Terminal chat_messages.status — typically 'completed' | 'failed' | 'aborted'. */
  status: string;
  content: string | null;
  errorKind: string | null;
  costUsd: string | null;
}

export interface UseChatStreamResult {
  state: ChatStreamState;
  /** in-flight messageId → accumulated buffer (string). */
  partialDeltas: Map<string, string>;
  /** messageId → terminal payload. */
  terminals: Map<string, ChatTerminalEvent>;
  /** True after `session_ended` event arrives. */
  sessionEnded: boolean;
  /** Last EventSource error string; null when live. */
  lastError: string | null;
}

interface DeltaPayload {
  message_id: string;
  seq: number;
  delta_text: string;
}

// ChatStreamStore is the mutable state container shared between the
// React hook and the underlying state machine. Tests construct one
// directly and drive transitions through ChatStreamMachine.
export interface ChatStreamStore extends UseChatStreamResult {
  /** Internal: dedupe key set keyed on `${messageId}:${seq}`. */
  seenSeqs: Set<string>;
}

export function emptyStore(): ChatStreamStore {
  return {
    state: 'dormant',
    partialDeltas: new Map(),
    terminals: new Map(),
    sessionEnded: false,
    lastError: null,
    seenSeqs: new Set(),
  };
}

// Backoff schedule per plan §1.5: 100ms doubling, capped at 30s.
export function computeBackoff(prevMs: number): number {
  if (prevMs <= 0) return 100;
  return Math.min(prevMs * 2, 30_000);
}

// ChatStreamMachine — pure state transitions on a ChatStreamStore.
// Tests construct a store + drive transitions directly without bringing
// up jsdom or rendering React. Each method mutates the store in place
// and returns the store for chaining.
export const ChatStreamMachine = {
  open(store: ChatStreamStore): ChatStreamStore {
    store.state = 'live';
    store.lastError = null;
    return store;
  },
  connecting(store: ChatStreamStore): ChatStreamStore {
    store.state = 'connecting';
    return store;
  },
  delta(store: ChatStreamStore, raw: string): ChatStreamStore {
    let payload: DeltaPayload;
    try {
      payload = JSON.parse(raw) as DeltaPayload;
    } catch {
      return store;
    }
    const key = `${payload.message_id}:${payload.seq}`;
    if (store.seenSeqs.has(key)) return store; // dedupe per plan §1.5
    store.seenSeqs.add(key);
    const prev = store.partialDeltas.get(payload.message_id) ?? '';
    store.partialDeltas.set(payload.message_id, prev + payload.delta_text);
    return store;
  },
  terminal(store: ChatStreamStore, raw: string): ChatStreamStore {
    let payload: ChatTerminalEvent;
    try {
      payload = JSON.parse(raw) as ChatTerminalEvent;
    } catch {
      return store;
    }
    if (!payload.messageId) return store;
    store.terminals.set(payload.messageId, payload);
    // Per plan §1.5 the renderer prefers terminal.content over
    // partialDeltas.get(messageId); we leave the partial buffer alone.
    return store;
  },
  sessionEnded(store: ChatStreamStore): ChatStreamStore {
    store.sessionEnded = true;
    return store;
  },
  error(store: ChatStreamStore, message = 'connection_lost'): ChatStreamStore {
    store.state = 'backoff';
    store.lastError = message;
    return store;
  },
  enterIdleGrace(store: ChatStreamStore): ChatStreamStore {
    store.state = 'idle-grace';
    return store;
  },
};

// Cache keyed by sessionId so re-mounts within idle-grace inherit
// accumulated state (no flash-of-stale-content per plan §1.16).
// Module-level so the cache survives component unmount → remount.
const sessionStores = new Map<string, ChatStreamStore>();
const idleTimers = new Map<string, ReturnType<typeof setTimeout>>();

export const IDLE_GRACE_MS = 60_000;

export function getOrCreateStore(sessionId: string): ChatStreamStore {
  let store = sessionStores.get(sessionId);
  if (!store) {
    store = emptyStore();
    sessionStores.set(sessionId, store);
  }
  return store;
}

function clearIdleTimer(sessionId: string): void {
  const t = idleTimers.get(sessionId);
  if (t) {
    clearTimeout(t);
    idleTimers.delete(sessionId);
  }
}

function scheduleIdleGrace(sessionId: string): void {
  clearIdleTimer(sessionId);
  const t = setTimeout(() => {
    sessionStores.delete(sessionId);
    idleTimers.delete(sessionId);
  }, IDLE_GRACE_MS);
  idleTimers.set(sessionId, t);
}

// Test-only: reset module state between specs. Not exported through
// the public API.
export function __resetChatStreamCache(): void {
  for (const t of idleTimers.values()) clearTimeout(t);
  idleTimers.clear();
  sessionStores.clear();
}

// EventSource factory injection so unit tests can pass a mock.
export type EventSourceFactory = (url: string) => EventSource;

let eventSourceFactory: EventSourceFactory | null = null;

export function __setEventSourceFactory(f: EventSourceFactory | null): void {
  eventSourceFactory = f;
}

function makeEventSource(url: string): EventSource {
  if (eventSourceFactory) return eventSourceFactory(url);
  return new EventSource(url);
}

export function useChatStream(sessionId: string): UseChatStreamResult {
  const [, force] = useReducer((x: number) => x + 1, 0);
  const esRef = useRef<EventSource | null>(null);
  const backoffRef = useRef<number>(0);
  const backoffTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelledRef = useRef<boolean>(false);

  useEffect(() => {
    if (!sessionId) return;
    cancelledRef.current = false;
    clearIdleTimer(sessionId);
    const store = getOrCreateStore(sessionId);
    if (store.state === 'idle-grace') {
      ChatStreamMachine.connecting(store);
    }

    function notify() {
      if (!cancelledRef.current) force();
    }

    function onDelta(ev: MessageEvent) {
      ChatStreamMachine.delta(store, ev.data as string);
      notify();
    }

    function onTerminal(ev: MessageEvent) {
      ChatStreamMachine.terminal(store, ev.data as string);
      notify();
    }

    function onSessionEnded(_ev: MessageEvent) {
      ChatStreamMachine.sessionEnded(store);
      closeAndStop();
      notify();
    }

    function clearBackoffTimer() {
      if (backoffTimerRef.current) {
        clearTimeout(backoffTimerRef.current);
        backoffTimerRef.current = null;
      }
    }

    function closeEventSource() {
      const es = esRef.current;
      if (es) {
        es.onopen = null;
        es.onerror = null;
        es.onmessage = null;
        es.close();
        esRef.current = null;
      }
    }

    function closeAndStop() {
      closeEventSource();
      clearBackoffTimer();
    }

    function onError() {
      closeEventSource();
      ChatStreamMachine.error(store);
      const next = computeBackoff(backoffRef.current);
      backoffRef.current = next;
      clearBackoffTimer();
      backoffTimerRef.current = setTimeout(() => {
        if (cancelledRef.current) return;
        connect();
      }, next);
      notify();
    }

    function onOpen() {
      backoffRef.current = 0;
      ChatStreamMachine.open(store);
      notify();
    }

    function connect() {
      if (cancelledRef.current) return;
      ChatStreamMachine.connecting(store);
      notify();
      const es = makeEventSource(`/api/sse/chat?session_id=${encodeURIComponent(sessionId)}`);
      esRef.current = es;
      es.onopen = onOpen;
      es.onerror = onError;
      es.addEventListener('delta', onDelta as unknown as EventListener);
      es.addEventListener('terminal', onTerminal as unknown as EventListener);
      es.addEventListener('session_ended', onSessionEnded as unknown as EventListener);
    }

    connect();

    return () => {
      cancelledRef.current = true;
      closeAndStop();
      ChatStreamMachine.enterIdleGrace(store);
      scheduleIdleGrace(sessionId);
    };
  }, [sessionId]);

  const store = sessionStores.get(sessionId) ?? emptyStore();
  return {
    state: store.state,
    partialDeltas: store.partialDeltas,
    terminals: store.terminals,
    sessionEnded: store.sessionEnded,
    lastError: store.lastError,
  };
}
