// listenClient — dedicated postgres-js client for the SSE bridge's
// LISTEN connection. Bound to DASHBOARD_APP_DSN (the LISTEN slot
// can ride on the same role as operational reads; the supervisor
// pattern uses a separate dedicated *pgx.Conn for the same reason
// — see AGENTS.md §M1 activation).
//
// Why separate from appDb:
//   - postgres-js LISTEN claims an entire connection. Sharing the
//     pool would block normal queries on the same connection.
//   - max: 1 + prepare: false matches the supervisor's M1 LISTEN
//     setup — one slot, no statement cache, reconnect on close.
//
// T015 owns the listener state machine (dormant → connecting →
// live → backoff/idle-grace) and channel subscription logic; this
// module exposes only the raw client factory.

import postgres from 'postgres';

const dsn = process.env.DASHBOARD_APP_DSN;

/**
 * makeListenClient — returns a fresh postgres-js client configured
 * for a single dedicated LISTEN connection. The caller (T015's
 * lib/sse/listener.ts singleton) owns the lifecycle: open on first
 * SSE subscriber, close after the idle-grace window.
 */
export function makeListenClient() {
  if (!dsn) {
    throw new Error(
      'DASHBOARD_APP_DSN is unset. The SSE bridge requires it for the LISTEN ' +
        'connection (see T015 + ops-checklist M3 section).',
    );
  }
  return postgres(dsn, {
    max: 1,
    prepare: false,
  });
}

export type ListenClient = ReturnType<typeof makeListenClient>;
