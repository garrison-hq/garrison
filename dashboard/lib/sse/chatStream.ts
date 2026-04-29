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

// M5.3 tool-call observability shapes (FR-450 / FR-451). The hook
// surfaces tool_use + tool_result events as ToolCallEntry rows keyed
// by messageId so the renderer can interleave chips with text deltas
// in arrival order.
export interface ToolUseEvent {
  messageId: string;
  toolUseId: string;
  toolName: string;
  /** Raw tool args from claude's content_block.input. Renderer reads
   *  selectively — read tools render lower-emphasis chip with summary;
   *  mutation tools render verb name + arg highlights. */
  args: unknown;
}

export interface ToolResultEvent {
  messageId: string;
  toolUseId: string;
  isError: boolean;
  /** Synthetic envelope from supervisor — {detail, is_error} for
   *  read-tool results; the chip surface decodes garrison-mutate's
   *  Result shape (success/affected_resource_id/error_kind/message)
   *  from the tool_result block on terminal commit via the row-state-read
   *  reconnect path per M5.2 FR-261. */
  result: unknown;
}

export interface ToolCallEntry {
  toolUseId: string;
  toolName: string;
  args: unknown;
  /** undefined while the tool_use is in flight; populated when the
   *  matching tool_result frame arrives. The chip surface transitions
   *  from pre-call to post-call (or failure) state per FR-441 / FR-444. */
  result?: { isError: boolean; payload: unknown };
}

export interface UseChatStreamResult {
  state: ChatStreamState;
  /** in-flight messageId → accumulated buffer (string). */
  partialDeltas: Map<string, string>;
  /** messageId → terminal payload. */
  terminals: Map<string, ChatTerminalEvent>;
  /** M5.3: messageId → ordered ToolCallEntry list (FR-451). */
  toolCalls: Map<string, ToolCallEntry[]>;
  /** True after `session_ended` event arrives. */
  sessionEnded: boolean;
  /** Last EventSource error string; null when live. */
  lastError: string | null;
}

interface DeltaPayload {
  message_id: string;
  /** Per-message_start counter from the supervisor. Defaults to 0
   *  for legacy supervisors that didn't emit it. The dashboard
   *  resets the visible buffer when this counter increases so
   *  multi-message turns (text → tool_use → text) render only the
   *  current message's deltas while it streams. */
  block?: number;
  seq: number;
  delta_text: string;
  /** When true, this is a directive to clear the visible buffer
   *  for (messageId, block) — claude opened a tool_use content
   *  block and the prior text was preamble that shouldn't linger
   *  in the bubble. */
  scrub?: boolean;
}

// ChatStreamStore is the mutable state container shared between the
// React hook and the underlying state machine. Tests construct one
// directly and drive transitions through ChatStreamMachine.
export interface ChatStreamStore extends UseChatStreamResult {
  /** Internal: dedupe key set keyed on `${messageId}:${block}:${seq}`. */
  seenSeqs: Set<string>;
  /** Internal: highest block seen per messageId. When a delta arrives
   *  with a higher block, the partial buffer for that messageId is
   *  reset (so the visible text reflects only the current claude
   *  message_start window). */
  blocks: Map<string, number>;
  /** Internal: dedupe key set keyed on `${messageId}:${toolUseId}` so
   *  reconnect-replay doesn't double-render M5.3 chips (FR-447). */
  seenToolUseIds: Set<string>;
}

export function emptyStore(): ChatStreamStore {
  return {
    state: 'dormant',
    partialDeltas: new Map(),
    terminals: new Map(),
    toolCalls: new Map(),
    sessionEnded: false,
    lastError: null,
    seenSeqs: new Set(),
    blocks: new Map(),
    seenToolUseIds: new Set(),
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
    const block = payload.block ?? 0;
    const key = `${payload.message_id}:${block}:${payload.seq}`;
    if (store.seenSeqs.has(key)) return store; // dedupe per plan §1.5
    store.seenSeqs.add(key);

    // Scrub directive — claude opened a tool_use content block in the
    // current message; the preamble we may have already streamed is
    // no longer the answer. Wipe the visible buffer for this messageId
    // immediately so the operator doesn't see lingering pre-tool text.
    if (payload.scrub) {
      store.blocks.set(payload.message_id, block);
      const next = new Map(store.partialDeltas);
      next.set(payload.message_id, '');
      store.partialDeltas = next;
      return store;
    }

    // If we see a new (higher) block for this messageId, reset its
    // visible buffer — claude moved past a message_start boundary in
    // the same turn (text → tool_use → text). Without this reset, the
    // dashboard would render prior intermediate text glued onto the
    // current message's stream.
    const lastBlock = store.blocks.get(payload.message_id) ?? -1;
    if (block > lastBlock) {
      store.blocks.set(payload.message_id, block);
      const next = new Map(store.partialDeltas);
      next.set(payload.message_id, payload.delta_text);
      store.partialDeltas = next;
      return store;
    }
    if (block < lastBlock) {
      // Stale delta from an earlier block (out-of-order replay or
      // reconnect race). Drop it — the prior block-reset already
      // overwrote the visible buffer.
      return store;
    }
    const prev = store.partialDeltas.get(payload.message_id) ?? '';
    const next = new Map(store.partialDeltas);
    next.set(payload.message_id, prev + payload.delta_text);
    store.partialDeltas = next;
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
    const next = new Map(store.terminals);
    next.set(payload.messageId, payload);
    store.terminals = next;
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
  // M5.3 tool-call observability transitions (FR-451 / FR-447).
  toolUse(store: ChatStreamStore, raw: string): ChatStreamStore {
    interface ToolUsePayload {
      message_id: string;
      tool_use_id: string;
      tool_name: string;
      args?: unknown;
    }
    let payload: ToolUsePayload;
    try {
      payload = JSON.parse(raw) as ToolUsePayload;
    } catch {
      return store;
    }
    if (!payload.message_id || !payload.tool_use_id) return store;
    const key = `${payload.message_id}:${payload.tool_use_id}`;
    if (store.seenToolUseIds.has(key)) return store; // reconnect dedupe
    store.seenToolUseIds.add(key);

    const existing = store.toolCalls.get(payload.message_id) ?? [];
    const next = new Map(store.toolCalls);
    next.set(payload.message_id, [
      ...existing,
      {
        toolUseId: payload.tool_use_id,
        toolName: payload.tool_name,
        args: payload.args ?? null,
      },
    ]);
    store.toolCalls = next;
    return store;
  },
  toolResult(store: ChatStreamStore, raw: string): ChatStreamStore {
    interface ToolResultPayload {
      message_id: string;
      tool_use_id: string;
      is_error?: boolean;
      result?: unknown;
    }
    let payload: ToolResultPayload;
    try {
      payload = JSON.parse(raw) as ToolResultPayload;
    } catch {
      return store;
    }
    if (!payload.message_id || !payload.tool_use_id) return store;
    const list = store.toolCalls.get(payload.message_id);
    if (!list) return store;
    const updated = list.map((entry) =>
      entry.toolUseId === payload.tool_use_id
        ? { ...entry, result: { isError: !!payload.is_error, payload: payload.result ?? null } }
        : entry,
    );
    const next = new Map(store.toolCalls);
    next.set(payload.message_id, updated);
    store.toolCalls = next;
    return store;
  },
  assistantError(store: ChatStreamStore, raw: string): ChatStreamStore {
    interface AssistantErrorPayload {
      message_id: string;
      error_kind: string;
      message?: string;
    }
    let payload: AssistantErrorPayload;
    try {
      payload = JSON.parse(raw) as AssistantErrorPayload;
    } catch {
      return store;
    }
    store.lastError = payload.error_kind;
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

    // M5.3 tool-call observability listeners (FR-450).
    function onToolUse(ev: MessageEvent) {
      ChatStreamMachine.toolUse(store, ev.data as string);
      notify();
    }
    function onToolResult(ev: MessageEvent) {
      ChatStreamMachine.toolResult(store, ev.data as string);
      notify();
    }
    function onAssistantError(ev: MessageEvent) {
      ChatStreamMachine.assistantError(store, ev.data as string);
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
      es.addEventListener('tool_use', onToolUse as unknown as EventListener);
      es.addEventListener('tool_result', onToolResult as unknown as EventListener);
      es.addEventListener('assistant_error', onAssistantError as unknown as EventListener);
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
    toolCalls: store.toolCalls,
    sessionEnded: store.sessionEnded,
    lastError: store.lastError,
  };
}
