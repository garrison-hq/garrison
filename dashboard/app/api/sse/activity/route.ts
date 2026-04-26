// SSE endpoint for the activity feed (FR-060, FR-064).
//
// Auth-gated. On connect:
//   1. Validate the session — 401 if missing.
//   2. Open a ReadableStream and enqueue an initial 'ping' event.
//   3. Read the Last-Event-ID header (if any) and run the catch-up
//      flow, streaming each historical event as a server-sent
//      event with id=<eventId>.
//   4. Subscribe to the singleton listener; every onEvent callback
//      streams a new SSE message with the live event.
//   5. On client disconnect (request.signal aborts), unsubscribe
//      and close the stream.

import { listener } from '@/lib/sse/listener';
import { fetchEventsAfter } from '@/lib/queries/activityCatchup';
import { getSession } from '@/lib/auth/session';
import type { ActivityEvent } from '@/lib/sse/events';

export const dynamic = 'force-dynamic';

function sseFrame(event: ActivityEvent): string {
  // SSE wire format: "id: <id>\nevent: <kind>\ndata: <json>\n\n".
  // The `id` line lets the browser auto-resume from Last-Event-ID
  // on disconnect.
  const lines = [
    `id: ${event.eventId}`,
    `event: ${event.kind}`,
    `data: ${JSON.stringify(event)}`,
    '',
    '',
  ];
  return lines.join('\n');
}

export async function GET(req: Request) {
  const session = await getSession();
  if (!session) {
    return new Response(JSON.stringify({ error: 'no_session' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  const lastEventId = req.headers.get('last-event-id');
  const lastEventCursor = lastEventId ? await cursorForEventId(lastEventId) : null;

  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    async start(controller) {
      // Initial keep-alive comment so proxies don't kill the
      // connection during the catch-up phase.
      controller.enqueue(encoder.encode(': connected\n\n'));

      // Catch-up replay
      try {
        const backlog = await fetchEventsAfter(lastEventCursor, 200);
        for (const ev of backlog) {
          controller.enqueue(encoder.encode(sseFrame(ev)));
        }
      } catch (err) {
        // If the catch-up query fails, push a synthetic error
        // event but keep the live stream going.
        const note = `: catch-up error: ${err instanceof Error ? err.message : 'unknown'}\n\n`;
        controller.enqueue(encoder.encode(note));
      }

      // Live subscription
      const unsubscribe = listener.subscribe({
        onEvent(event) {
          try {
            controller.enqueue(encoder.encode(sseFrame(event)));
          } catch {
            // controller is already closed — happens during
            // disconnect race; safe to ignore
          }
        },
        onConnectionLost() {
          try {
            controller.enqueue(encoder.encode(': connection-lost\n\n'));
          } catch {
            // ignore
          }
        },
      });

      // Heartbeat every 25s to keep proxies happy (most idle
      // timeouts are 30s+).
      const heartbeat = setInterval(() => {
        try {
          controller.enqueue(encoder.encode(': hb\n\n'));
        } catch {
          // ignore
        }
      }, 25_000);

      const onAbort = () => {
        clearInterval(heartbeat);
        unsubscribe();
        try {
          controller.close();
        } catch {
          // already closed
        }
      };
      req.signal.addEventListener('abort', onAbort);
    },
  });

  return new Response(stream, {
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache, no-transform',
      Connection: 'keep-alive',
      'X-Accel-Buffering': 'no',
    },
  });
}

/**
 * Resolve a Last-Event-ID (an event_outbox uuid) to the ISO
 * timestamp our cursor model expects. Falls back to null on
 * unknown ids — the catch-up returns the trailing N events.
 */
async function cursorForEventId(id: string): Promise<string | null> {
  try {
    const { appDb } = await import('@/lib/db/appClient');
    const { sql } = await import('drizzle-orm');
    const rows = await appDb.execute<{ created_at: Date }>(sql`
      SELECT created_at FROM event_outbox WHERE id = ${id} LIMIT 1
    `);
    return rows[0]?.created_at?.toISOString() ?? null;
  } catch {
    return null;
  }
}
