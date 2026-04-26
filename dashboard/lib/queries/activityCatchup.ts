import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { parseChannel, isKnownChannel } from '@/lib/sse/channels';
import type { ActivityEvent } from '@/lib/sse/events';

// FR-064 catch-up flow. After a Last-Event-ID reconnect (or the
// SSE route's initial backfill), fetch every event_outbox row
// whose created_at is after the cursor (ASC order; chronological
// replay). Filters out unknown channels at the application layer
// — though the `unknown`-kind ActivityEvent variant lets the UI
// render any leakage rather than mute it silently.

/**
 * Fetch events after a cursor. The cursor is the ISO timestamp of
 * the last delivered event; if undefined, returns the most recent
 * `limit` rows (newest-first), in ASC order so the caller can
 * stream them chronologically.
 */
export async function fetchEventsAfter(
  cursorIso: string | null,
  limit = 100,
): Promise<ActivityEvent[]> {
  const cap = Math.max(1, Math.min(500, limit));
  const rows = await appDb.execute<{
    id: string;
    channel: string;
    payload: Record<string, unknown> | null;
    created_at: Date;
  }>(sql`
    SELECT id, channel, payload, created_at
      FROM event_outbox
     ${cursorIso ? sql`WHERE created_at > ${new Date(cursorIso)}` : sql``}
     ORDER BY created_at ASC, id ASC
     LIMIT ${cap}
  `);
  return rows
    .filter((r) => isKnownChannel(r.channel))
    .map((r) =>
      parseChannel({
        id: r.id,
        channel: r.channel,
        payload: r.payload,
        createdAt: r.created_at,
      }),
    );
}
