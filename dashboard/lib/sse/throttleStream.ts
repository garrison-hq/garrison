'use client';

// M6 — useThrottleStream hook (plan §"Phase 9 — Dashboard").
//
// Mirrors the M5.x chat-SSE shape: per-route LISTEN, EventSource
// subscribes to a single SSE event name, the hook exposes an
// in-memory buffer + connection-error string. The hygiene table's
// throttle sub-table consumes this hook to live-render
// `work.throttle.event` notifies on top of the SSR'd snapshot.
//
// Buffer policy:
//  - Cap at 100 events.
//  - On overflow, FIFO-drop the oldest (the buffer is a "recent
//    throttle activity" view; older rows live in the SSR'd
//    `listThrottleEvents` snapshot below).
//  - Newest events are PREPENDED so the rendered list reads
//    newest-first without per-render sorting.
//
// The pure store + reducer (`emptyStore`, `pushEvent`) are exported
// for vitest unit coverage; the React hook itself is exercised
// end-to-end by Playwright once the M6.x integration suite lands.
// Mirrors the chatStream.ts test seam ("ChatStreamMachine is the
// pure surface; the hook is browser-coverage-only").

import { useEffect, useReducer, useRef } from 'react';

export interface ThrottleEvent {
  event_id: string;
  company_id: string;
  kind: string;
  fired_at: string;
}

export interface UseThrottleStreamResult {
  events: ThrottleEvent[];
  lastError: string | null;
}

const BUFFER_CAP = 100;

export interface ThrottleStreamStore {
  events: ThrottleEvent[];
  lastError: string | null;
  /** Dedupe by event_id so a reconnect-replay (Last-Event-ID) doesn't
   *  double-render the same row. */
  seenIds: Set<string>;
}

export function emptyStore(): ThrottleStreamStore {
  return { events: [], lastError: null, seenIds: new Set() };
}

export function pushEvent(store: ThrottleStreamStore, raw: string): ThrottleStreamStore {
  let payload: ThrottleEvent;
  try {
    payload = JSON.parse(raw) as ThrottleEvent;
  } catch {
    return store;
  }
  if (!payload.event_id) return store;
  if (store.seenIds.has(payload.event_id)) return store;
  store.seenIds.add(payload.event_id);
  // Prepend; FIFO-drop on overflow.
  const next = [payload, ...store.events];
  if (next.length > BUFFER_CAP) {
    const dropped = next.slice(BUFFER_CAP);
    for (const d of dropped) store.seenIds.delete(d.event_id);
    store.events = next.slice(0, BUFFER_CAP);
  } else {
    store.events = next;
  }
  store.lastError = null;
  return store;
}

export function useThrottleStream(): UseThrottleStreamResult {
  const [, force] = useReducer((x: number) => x + 1, 0);
  const storeRef = useRef<ThrottleStreamStore>(emptyStore());
  const esRef = useRef<EventSource | null>(null);
  const cancelledRef = useRef<boolean>(false);

  useEffect(() => {
    cancelledRef.current = false;
    const store = storeRef.current;

    function notify() {
      if (!cancelledRef.current) force();
    }

    function onThrottleEvent(ev: MessageEvent) {
      pushEvent(store, ev.data as string);
      notify();
    }

    function onError() {
      store.lastError = 'connection_lost';
      notify();
    }

    function onOpen() {
      store.lastError = null;
      notify();
    }

    const es = new EventSource('/api/sse/throttle');
    esRef.current = es;
    es.onopen = onOpen;
    es.onerror = onError;
    es.addEventListener('throttle_event', onThrottleEvent as unknown as EventListener);

    return () => {
      cancelledRef.current = true;
      es.onopen = null;
      es.onerror = null;
      es.close();
      esRef.current = null;
    };
  }, []);

  return {
    events: storeRef.current.events,
    lastError: storeRef.current.lastError,
  };
}
