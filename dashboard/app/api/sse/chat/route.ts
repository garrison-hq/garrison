// SSE endpoint for the M5.1 chat backend (FR-052).
//
// GET /api/sse/chat?session_id=<uuid>
//
// On connect:
//  1. Validate the better-auth session (401 on missing).
//  2. Open a per-route LISTEN connection that subscribes to:
//       - chat.assistant.delta       (per-batch token deltas)
//       - work.chat.message_sent     (assistant turn terminal commit)
//       - work.chat.session_ended    (session lifecycle close)
//  3. Forward each notify whose payload references the requested
//     session_id as an SSE event:
//       - delta        (relay payload verbatim — message_id + seq + delta_text)
//       - terminal     (read terminal chat_messages row + emit content + cost)
//       - session_ended (close the SSE stream)
//  4. Heartbeat every 25s.
//  5. On client disconnect (req.signal.abort) close the LISTEN
//     connection.

import postgres from 'postgres';
import { getSession } from '@/lib/auth/session';
import { getSessionWithMessages } from '@/lib/queries/chat';

export const dynamic = 'force-dynamic';

const CHANNELS = {
  delta: 'chat.assistant.delta',
  workMessageSent: 'work.chat.message_sent',
  workSessionEnded: 'work.chat.session_ended',
} as const;

interface DeltaPayload {
  message_id: string;
  seq: number;
  delta_text: string;
}

interface WorkMessagePayload {
  chat_session_id: string;
  chat_message_id: string;
}

interface WorkSessionEndedPayload {
  chat_session_id: string;
  status: string;
}

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

  const url = new URL(req.url);
  const sessionId = url.searchParams.get('session_id');
  if (!sessionId) {
    return new Response(JSON.stringify({ error: 'missing_session_id' }), {
      status: 400,
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

      // Subscribe to the three channels.
      try {
        await sql.listen(CHANNELS.delta, (payloadRaw) => {
          try {
            const p = JSON.parse(payloadRaw) as DeltaPayload;
            enqueue(frame('delta', p, `${sessionId}:${p.seq}`));
          } catch {
            // malformed payload; drop
          }
        });
        await sql.listen(CHANNELS.workMessageSent, async (payloadRaw) => {
          try {
            const p = JSON.parse(payloadRaw) as WorkMessagePayload;
            if (p.chat_session_id !== sessionId) return;
            // Read the terminal row state.
            const detail = await getSessionWithMessages(sessionId);
            if (!detail) return;
            const terminal = detail.messages.find(m => m.id === p.chat_message_id);
            if (!terminal) return;
            enqueue(frame('terminal', {
              messageId: terminal.id,
              status: terminal.status,
              content: terminal.content,
              errorKind: terminal.errorKind,
              costUsd: terminal.costUsd,
            }, `${sessionId}:terminal:${terminal.id}`));
          } catch {
            // ignore
          }
        });
        await sql.listen(CHANNELS.workSessionEnded, (payloadRaw) => {
          try {
            const p = JSON.parse(payloadRaw) as WorkSessionEndedPayload;
            if (p.chat_session_id !== sessionId) return;
            enqueue(frame('session_ended', p, `${sessionId}:ended`));
            void close();
          } catch {
            // ignore
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
