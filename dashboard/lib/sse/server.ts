// Shared SSE server-side scaffolding.
//
// Both /api/sse/chat and /api/sse/throttle (M6 T015) follow the same
// shape: validate session → open per-route LISTEN connection → forward
// notifies as SSE events → 25s heartbeat → close on req.signal.abort.
// Keeping the common machinery here avoids the SonarCloud duplicated-
// code finding without forcing both routes through a single rigid
// channel-registration API.

import postgres from 'postgres';
import { getSession } from '@/lib/auth/session';

/** SSE response headers — matches the chat-bridge shape that's already
 * proxied correctly in front of garrison's deployment topology. */
export const sseHeaders = {
  'Content-Type': 'text/event-stream',
  'Cache-Control': 'no-cache, no-transform',
  Connection: 'keep-alive',
  'X-Accel-Buffering': 'no',
} as const;

/** Format an SSE frame with optional id + named event + JSON-stringified
 * data payload. The trailing blank line terminates the frame. */
export function frame(eventName: string, data: unknown, id?: string): string {
  const lines = [
    ...(id ? [`id: ${id}`] : []),
    `event: ${eventName}`,
    `data: ${JSON.stringify(data)}`,
    '',
    '',
  ];
  return lines.join('\n');
}

/** Per-stream context the registerListeners callback receives. enqueue
 * is no-op-after-close (safe to call from late-arriving notifies);
 * close is idempotent. sql is the postgres client the caller registers
 * LISTEN handlers against. */
export interface SSEContext {
  sql: ReturnType<typeof postgres>;
  enqueue: (chunk: string) => void;
  close: () => Promise<void>;
}

/** registerListeners is called once per browser EventSource connect.
 * Implementations call sql.listen(channel, handler) and use ctx.enqueue
 * to forward frames. Any thrown error is surfaced as a `: listen error`
 * SSE comment + closes the stream (matches the pre-extraction shape). */
export type RegisterListeners = (ctx: SSEContext) => Promise<void>;

interface JsonErrorOpts {
  status: number;
  body: Record<string, unknown>;
}

function jsonError({ status, body }: JsonErrorOpts): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** sseRoute is the high-level helper both /api/sse/chat and
 * /api/sse/throttle use. It validates the better-auth session, opens
 * the postgres LISTEN connection, drives the heartbeat + abort
 * teardown, and delegates the channel-registration to the caller's
 * registerListeners closure.
 *
 * Returns a Response carrying the ReadableStream, or a JSON error
 * response on missing session / unset DSN. */
export async function sseRoute(
  req: Request,
  registerListeners: RegisterListeners,
): Promise<Response> {
  const session = await getSession();
  if (!session) {
    return jsonError({ status: 401, body: { error: 'no_session' } });
  }

  const dsn = process.env.DASHBOARD_APP_DSN;
  if (!dsn) {
    return jsonError({ status: 500, body: { error: 'dsn_unset' } });
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
        await registerListeners({ sql, enqueue, close });
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

  return new Response(stream, { headers: sseHeaders });
}
