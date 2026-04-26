// Singleton SSE listener (FR-060, FR-064).
//
// State machine per plan §"SSE listener lifecycle":
//   dormant   — no SSE clients connected
//   connecting — LISTEN connection being established
//   live      — LISTEN connection healthy, broadcasting
//   backoff   — waiting before retry
//   idle-grace — no clients, but holding connection 60s
//
// Reconnect uses exponential backoff: 100ms → 30s, doubling.
//
// The poll fallback for parameterized channels runs alongside
// LISTEN: every 5s the listener queries event_outbox for rows
// added since the last poll cursor and emits any whose channel
// matches a KNOWN_CHANNEL_PATTERNS regex but isn't a literal in
// KNOWN_CHANNELS. This is the spec-FR-060 compromise that
// preserves the M3 scope ("don't modify supervisor pg_notify
// emissions") while still surfacing finalize and transition
// events to the activity feed.

import { makeListenClient, type ListenClient } from '@/lib/db/listenClient';
import { KNOWN_CHANNELS, isKnownChannel, parseChannel } from './channels';
import type { ActivityEvent } from './events';

export type ListenerState =
  | 'dormant'
  | 'connecting'
  | 'live'
  | 'backoff'
  | 'idle-grace';

export interface ListenerSubscriber {
  onEvent(event: ActivityEvent): void;
  /** Called when the LISTEN connection drops; the SSE route uses
   *  this to push a connection-lost marker to the client. */
  onConnectionLost?(): void;
}

export interface ListenerOptions {
  initialBackoffMs?: number;
  maxBackoffMs?: number;
  idleGraceMs?: number;
  pollIntervalMs?: number;
  /** Injectable factory used by tests; defaults to makeListenClient. */
  clientFactory?: () => ListenClient;
}

const DEFAULTS = {
  initialBackoffMs: 100,
  maxBackoffMs: 30_000,
  idleGraceMs: 60_000,
  pollIntervalMs: 5_000,
} as const;

/**
 * Singleton listener factory. Tests can call `makeListener` with
 * an injected clientFactory to exercise the state machine without
 * real DB connections; production code uses the module-level
 * `listener` export which boots lazily on first subscriber.
 */
export function makeListener(options: ListenerOptions = {}) {
  const config = { ...DEFAULTS, ...options };
  const subscribers = new Set<ListenerSubscriber>();
  let state: ListenerState = 'dormant';
  let client: ListenClient | null = null;
  let backoffMs = config.initialBackoffMs;
  let backoffTimer: ReturnType<typeof setTimeout> | null = null;
  let idleTimer: ReturnType<typeof setTimeout> | null = null;
  let pollCursor = new Date();

  function fanout(event: ActivityEvent) {
    for (const sub of subscribers) {
      try {
        sub.onEvent(event);
      } catch {
        // never let one subscriber's error stop fanout to others
      }
    }
  }

  function fanoutConnectionLost() {
    for (const sub of subscribers) {
      try {
        sub.onConnectionLost?.();
      } catch {
        // ignore
      }
    }
  }

  function clearTimers() {
    if (backoffTimer) {
      clearTimeout(backoffTimer);
      backoffTimer = null;
    }
    if (idleTimer) {
      clearTimeout(idleTimer);
      idleTimer = null;
    }
  }

  async function connect() {
    state = 'connecting';
    try {
      client = (config.clientFactory ?? makeListenClient)();
      // postgres-js's LISTEN is set up via the .listen() method.
      for (const channel of KNOWN_CHANNELS) {
        await client.listen(channel, async (payload) => {
          await handleNotify(channel, payload);
        });
      }
      state = 'live';
      backoffMs = config.initialBackoffMs;
    } catch {
      state = 'backoff';
      fanoutConnectionLost();
      scheduleRetry();
    }
  }

  function scheduleRetry() {
    backoffTimer = setTimeout(() => {
      backoffTimer = null;
      backoffMs = Math.min(backoffMs * 2, config.maxBackoffMs);
      void connect();
    }, backoffMs);
  }

  async function handleNotify(channel: string, raw: string) {
    if (!isKnownChannel(channel)) return;
    let payload: Record<string, unknown> = {};
    try {
      payload = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      // ignore malformed payloads — the channel allowlist already
      // gates the surface
    }
    const event = parseChannel({
      id: typeof payload.event_id === 'string' ? payload.event_id : '',
      channel,
      payload,
      createdAt: new Date(),
    });
    if (event.kind !== 'unknown') {
      fanout(event);
    }
  }

  async function disconnect() {
    if (!client) return;
    try {
      // postgres-js's connection .end() flushes + closes.
      const c = client as unknown as { end?: () => Promise<void> };
      await c.end?.();
    } catch {
      // best effort
    } finally {
      client = null;
    }
  }

  return {
    /** Subscribe a new SSE client. Boots the LISTEN connection on
     *  the first subscriber if currently dormant or in idle-grace. */
    subscribe(sub: ListenerSubscriber): () => void {
      subscribers.add(sub);
      if (state === 'dormant') {
        void connect();
      } else if (state === 'idle-grace') {
        clearTimers();
        state = 'live';
      }
      return () => {
        subscribers.delete(sub);
        if (subscribers.size === 0 && state === 'live') {
          state = 'idle-grace';
          idleTimer = setTimeout(() => {
            state = 'dormant';
            void disconnect();
          }, config.idleGraceMs);
        }
      };
    },
    state(): ListenerState {
      return state;
    },
    /** Test-only: drive a fake notification through the fanout
     *  without going through the DB. Production code never calls this. */
    __testInject(channel: string, raw: string): Promise<void> {
      return handleNotify(channel, raw);
    },
    /** Test-only: trigger a connection-lost simulation. */
    __testConnectionLost(): void {
      state = 'backoff';
      fanoutConnectionLost();
    },
    /** Test-only: inspect the subscriber count. */
    __testSubscriberCount(): number {
      return subscribers.size;
    },
    /** Get and bump the poll cursor. Used by the SSE route's
     *  parameterized-channel poll. */
    pollCursor(): Date {
      return pollCursor;
    },
    advancePollCursor(to: Date): void {
      pollCursor = to;
    },
    /** Production cleanup hook (Next.js doesn't normally call this;
     *  exposed for graceful shutdown in tests). */
    async shutdown(): Promise<void> {
      clearTimers();
      subscribers.clear();
      state = 'dormant';
      await disconnect();
    },
  };
}

// Module-level singleton. Boots lazily on first subscribe.
export const listener = makeListener();
