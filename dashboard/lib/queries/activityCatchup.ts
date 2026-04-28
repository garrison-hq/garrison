import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { vaultRoDb } from '@/lib/db/vaultRoClient';
import { parseChannel, isKnownChannel } from '@/lib/sse/channels';
import type { ActivityEvent } from '@/lib/sse/events';

// FR-064 catch-up flow. After a Last-Event-ID reconnect (or the
// SSE route's initial backfill), fetch every event_outbox row
// whose created_at is after the cursor (ASC order; chronological
// replay). Filters out unknown channels at the application layer
// — though the `unknown`-kind ActivityEvent variant lets the UI
// render any leakage rather than mute it silently.
//
// M4 (FR-019): the activity feed is now sourced from TWO tables.
// event_outbox (existing, supervisor + dashboard writers) carries
// ticket / agent mutation events. vault_access_log (M2.3 +
// extended in T001) carries vault mutation events with the
// extended outcome enum from FR-012. fetchEventsAfter merges
// both streams chronologically by timestamp; the consumer sees
// a single ordered list of ActivityEvent variants.
//
// Vault rows use vaultRoDb (the dashboard's vault read role)
// because vault_access_log is in the M3 read role's grant set.
// The activity feed is operator-facing and read-only on this
// table; writes happen inside server actions via appDb (with
// the dashboard's app role's elevated grants on vault_access_log
// for INSERT — added in T007's task scope).

/**
 * Fetch events after a cursor. The cursor is the ISO timestamp of
 * the last delivered event; if undefined, returns the most recent
 * `limit` rows (newest-first), in ASC order so the caller can
 * stream them chronologically.
 *
 * Merges event_outbox + vault_access_log streams by timestamp
 * (ASC). Vault rows are converted into ActivityEvent variants
 * via a vault-specific bridge (mirrors the channel-name shape
 * the lib/sse/channels.ts:parseChannel function expects).
 */
export async function fetchEventsAfter(
  cursorIso: string | null,
  limit = 100,
): Promise<ActivityEvent[]> {
  const cap = Math.max(1, Math.min(500, limit));

  const outboxRows = await appDb.execute<{
    id: string;
    channel: string;
    payload: Record<string, unknown> | null;
    created_at: Date;
  }>(sql`
    SELECT id, channel, payload, created_at
      FROM event_outbox
     ${cursorIso ? sql`WHERE created_at > ${cursorIso}::timestamptz` : sql``}
     ORDER BY created_at ASC, id ASC
     LIMIT ${cap}
  `);

  const vaultRows = await vaultRoDb.execute<{
    id: string;
    outcome: string;
    secret_path: string;
    metadata: Record<string, unknown> | null;
    timestamp: Date;
  }>(sql`
    SELECT id, outcome, secret_path, metadata, timestamp
      FROM vault_access_log
     WHERE outcome IN (
         'secret_created','secret_edited','secret_deleted',
         'grant_added','grant_removed',
         'rotation_initiated','rotation_completed','rotation_failed',
         'value_revealed'
       )
       ${cursorIso ? sql`AND timestamp > ${cursorIso}::timestamptz` : sql``}
     ORDER BY timestamp ASC, id ASC
     LIMIT ${cap}
  `);

  const outboxEvents: Array<{ at: Date; ev: ActivityEvent }> = outboxRows
    .filter((r) => isKnownChannel(r.channel))
    .map((r) => ({
      at: r.created_at instanceof Date ? r.created_at : new Date(r.created_at),
      ev: parseChannel({
        id: r.id,
        channel: r.channel,
        payload: r.payload,
        createdAt: r.created_at,
      }),
    }));

  const vaultEvents: Array<{ at: Date; ev: ActivityEvent }> = vaultRows.map((r) => ({
    at: r.timestamp instanceof Date ? r.timestamp : new Date(r.timestamp),
    ev: vaultRowToActivityEvent(r),
  }));

  // Merge two ASC-sorted streams chronologically.
  const merged: ActivityEvent[] = [];
  let i = 0;
  let j = 0;
  while (i < outboxEvents.length && j < vaultEvents.length) {
    if (outboxEvents[i].at.getTime() <= vaultEvents[j].at.getTime()) {
      merged.push(outboxEvents[i].ev);
      i++;
    } else {
      merged.push(vaultEvents[j].ev);
      j++;
    }
  }
  while (i < outboxEvents.length) merged.push(outboxEvents[i++].ev);
  while (j < vaultEvents.length) merged.push(vaultEvents[j++].ev);
  return merged.slice(0, cap);
}

/**
 * Convert a vault_access_log row into an ActivityEvent. The
 * channel name shape is synthetic — vault rows don't have a
 * channel column, but the lib/sse/channels.ts:parseChannel
 * function expects channel-keyed dispatch. Building a synthetic
 * "work.vault.<outcome>" channel name keeps the rendering layer
 * agnostic about the source table.
 */
function vaultRowToActivityEvent(row: {
  id: string;
  outcome: string;
  secret_path: string;
  metadata: Record<string, unknown> | null;
  timestamp: Date;
}): ActivityEvent {
  return parseChannel({
    id: row.id,
    channel: `work.vault.${row.outcome}`,
    payload: {
      secret_path: row.secret_path,
      ...row.metadata,
    },
    createdAt: row.timestamp,
  });
}
