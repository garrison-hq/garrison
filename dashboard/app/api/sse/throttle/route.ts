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
// The shared scaffolding (auth, dsn, ReadableStream, heartbeat,
// abort cleanup) lives in lib/sse/server.ts so this route is just
// the channel-registration logic.

import { sseRoute, frame } from '@/lib/sse/server';

export const dynamic = 'force-dynamic';

const CHANNEL = 'work.throttle.event';

interface ThrottleEventPayload {
  event_id?: string;
  company_id?: string;
  kind?: string;
  fired_at?: string;
}

export async function GET(req: Request) {
  return sseRoute(req, async ({ sql, enqueue }) => {
    await sql.listen(CHANNEL, (payloadRaw) => {
      try {
        const p = JSON.parse(payloadRaw) as ThrottleEventPayload;
        const id = p.event_id ? `throttle:${p.event_id}` : undefined;
        enqueue(frame('throttle_event', p, id));
      } catch {
        // malformed payload; drop
      }
    });
  });
}
