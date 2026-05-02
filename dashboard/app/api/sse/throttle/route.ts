// M6 — SSE endpoint for live throttle events (plan §"Phase 9 — Dashboard").
//
// GET /api/sse/throttle
//
// On connect:
//  1. Validate the better-auth session (401 on missing).
//  2. Open a per-route LISTEN connection that subscribes to
//     `work.throttle.event`, the channel the supervisor's
//     `internal/throttle` package fires on every budget defer or
//     rate-limit pause (see throttle.emitNotify, T004).
//  3. Forward each notify as an SSE event named `throttle_event`
//     carrying the parsed JSON payload `{event_id, company_id,
//     kind, fired_at}`.
//  4. Heartbeat every 25s.
//  5. On client disconnect (req.signal.abort) close the LISTEN
//     connection.
//
// Mirrors the per-route LISTEN shape of /api/sse/chat — one
// connection per browser EventSource, no shared singleton; the
// dashboard's hygiene tab is the only consumer for now and it
// stays mounted while the operator is on /hygiene.

import postgres from 'postgres';
import { getSession } from '@/lib/auth/session';

export const dynamic = 'force-dynamic';

const CHANNEL = 'work.throttle.event';

function frame(eventName: string, data: unknown, id?: string): string {
  const lines = [
    ...(id ? [`id: ${id}`] : []),
    `event: ${eventName}`,
    `data: ${JSON.stringify(data)}`,
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

  const dsn = process.env.DASHBOARD_APP_DSN;
  if (!dsn) {
    return new Response(JSON.stringify({ error: 'dsn_unset' }), {
      status: 500,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    async start(controller) {
      controller.enqueue(encoder.encode(': connected\n\n'));

      const sql = postgres(dsn, { max: 1 });
      let closed = false;

      const enqueue = (chunk: string) => {
        if (closed) return;
        try {
          controller.enqueue(encoder.encode(chunk));
        } catch {
          // controller closed
        }
      };

      const close = async () => {
        if (closed) return;
        closed = true;
        try { await sql.end({ timeout: 1 }); } catch { /* ignore */ }
        try { controller.close(); } catch { /* ignore */ }
      };

      try {
        await sql.listen(CHANNEL, (payloadRaw) => {
          try {
            const p = JSON.parse(payloadRaw) as {
              event_id?: string;
              company_id?: string;
              kind?: string;
              fired_at?: string;
            };
            const id = p.event_id ? `throttle:${p.event_id}` : undefined;
            enqueue(frame('throttle_event', p, id));
          } catch {
            // malformed payload; drop
          }
        });
      } catch (err) {
        enqueue(`: listen error: ${err instanceof Error ? err.message : 'unknown'}\n\n`);
        await close();
        return;
      }

      // Heartbeat keeps proxies happy.
      const hb = setInterval(() => enqueue(': hb\n\n'), 25_000);

      const onAbort = () => {
        clearInterval(hb);
        void close();
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
