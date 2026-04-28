// emitPgNotify — typed pg_notify emission inside a mutation
// transaction.
//
// FR-015 / SC-005 / Phase 0 research item 2: pg_notify issued
// inside a Drizzle transaction (postgres-js underneath) only
// becomes visible to LISTEN subscribers when the transaction
// COMMITS. This is the load-bearing property — the activity
// feed must see committed state, never speculative reads.
// Verified by lib/audit/pgNotify.test.ts.
//
// The runtime contract: caller passes its own active transaction
// handle. Caller is responsible for the transaction lifecycle;
// this helper only enqueues the NOTIFY. The NOTIFY fires at
// COMMIT time per Postgres semantics.

import { sql } from 'drizzle-orm';
import type { MutationTx } from './eventOutbox';

/**
 * Emit a pg_notify on `channel` with the given payload, inside
 * the passed transaction. Postgres delivers the notification at
 * COMMIT time, not at execute time.
 *
 * The payload is a single TEXT argument. For structured payloads,
 * stringify upstream (the activity feed bridges through
 * lib/sse/channels.ts:parseChannel which fetches the
 * event_outbox row by id; the typical pattern is to pass the
 * row id as the payload here).
 */
export async function emitPgNotify(
  tx: MutationTx,
  channel: string,
  payload: string,
): Promise<void> {
  await tx.execute(sql`SELECT pg_notify(${channel}, ${payload})`);
}
