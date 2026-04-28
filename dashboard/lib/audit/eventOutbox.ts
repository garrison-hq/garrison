// writeMutationEventToOutbox — write a MutationEvent to the
// supervisor-domain event_outbox table.
//
// FR-011 / FR-014 / Phase 0 research item 4: the dashboard joins
// the supervisor as a second writer on event_outbox. The PK is
// uuid v4 (collision-resistant across writers); the supervisor's
// existing readers (M3 SSE catch-up, M3 retro §"SSE activity
// feed") are unaffected because they read by id ordering, not by
// writer identity.
//
// The write happens inside a Drizzle transaction passed in by the
// caller. The caller is responsible for transaction lifecycle;
// this helper only INSERTs. The pg_notify is issued separately
// via lib/audit/pgNotify.ts:emitPgNotify so the order
// (INSERT then NOTIFY then COMMIT) is explicit.

import { sql, type ExtractTablesWithRelations } from 'drizzle-orm';
import type { PgTransaction, PgQueryResultHKT } from 'drizzle-orm/pg-core';
import { eventOutbox } from '@/drizzle/schema.supervisor';
import type { MutationEvent } from './events';
import { channelForEvent } from './events';

/** Drizzle transaction handle used by mutation server actions. */
export type MutationTx = PgTransaction<
  PgQueryResultHKT,
  Record<string, never>,
  ExtractTablesWithRelations<Record<string, never>>
>;

/**
 * Write the mutation event to event_outbox and return the new row id.
 *
 * Caller passes deptSlug for ticket.moved events (the channel name
 * encodes the department). Other event kinds ignore deptSlug.
 *
 * The payload is stored as JSONB. Per Rule 6 / FR-018, no field of
 * the payload may carry a secret value; the caller's leak-scan
 * discipline (FR-017) is the source of that property.
 */
export async function writeMutationEventToOutbox(
  tx: MutationTx,
  event: MutationEvent,
  deptSlug?: string,
): Promise<{ id: string }> {
  const channel = channelForEvent(event, deptSlug);
  const rows = await tx
    .insert(eventOutbox)
    .values({
      channel,
      payload: event,
    })
    .returning({ id: eventOutbox.id });
  return { id: rows[0].id };
}

/**
 * Convenience: emit a typed pg_notify referencing an event_outbox
 * row id. The caller passes the same tx used to write the row;
 * the NOTIFY is enqueued for the transaction's COMMIT.
 *
 * Why a separate helper: pg_notify is a `SELECT pg_notify(channel,
 * payload)` SQL call, not a Drizzle INSERT. Keeping the call site
 * compact is worth the extra function.
 */
export async function emitPgNotifyForOutbox(
  tx: MutationTx,
  channel: string,
  outboxRowId: string,
): Promise<void> {
  await tx.execute(sql`SELECT pg_notify(${channel}, ${outboxRowId})`);
}
