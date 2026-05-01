'use client';

import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { StatusDot } from '@/components/ui/StatusDot';
import { formatIsoFull } from '@/lib/format/relativeTime';
import type { ActivityEvent } from '@/lib/sse/events';

// Single row in an expanded RunGroup. Visually distinct from the
// rollup row above it — leading status dot (vs chevron), full
// HH:MM:SS time (vs relative), event-type chip (vs "<n> events"
// count). 36px tall with extra horizontal padding so it reads as
// nested under the rollup.
//
// Event-type chip tones — only agent.spawned pulses (live work in
// progress). Everything else uses a quiet neutral / info / warn
// per the operator-facing semantic:
//   ticket.created       → info  (something appeared)
//   ticket.transitioned  → neutral (most common; shouldn't shout)
//   ticket.commented     → neutral
//   agent.spawned        → ok + pulse on the leading dot
//   agent.completed      → ok
//   hygiene.flagged      → warn
//   error.*              → err
//   unknown              → neutral

type ChipTone = 'ok' | 'info' | 'warn' | 'err' | 'neutral';

const KIND_CHIP_TONE: Record<string, ChipTone> = {
  'ticket.created': 'info',
  'ticket.transitioned': 'neutral',
  'ticket.commented': 'neutral',
  'agent.spawned': 'ok',
  'agent.completed': 'ok',
  'hygiene.flagged': 'warn',
  'chat.session_deleted': 'warn',
  unknown: 'neutral',
};

const KIND_DOT_TONE: Record<string, ChipTone> = {
  'ticket.created': 'info',
  'ticket.transitioned': 'neutral',
  'ticket.commented': 'neutral',
  'agent.spawned': 'ok',
  'agent.completed': 'ok',
  'hygiene.flagged': 'warn',
  'chat.session_deleted': 'warn',
  unknown: 'neutral',
};

function chipToneFor(kind: string, hint?: string): ChipTone {
  if (kind === 'unknown' && hint?.startsWith('error')) return 'err';
  return KIND_CHIP_TONE[kind] ?? 'neutral';
}

function dotToneFor(kind: string, hint?: string): ChipTone {
  if (kind === 'unknown' && hint?.startsWith('error')) return 'err';
  return KIND_DOT_TONE[kind] ?? 'neutral';
}

export function EventRow({ event }: Readonly<{ event: ActivityEvent }>) {
  const ts = new Date(event.at).toISOString().slice(11, 19);
  const channelHint = event.kind === 'unknown' ? event.channel : undefined;
  const dotTone = dotToneFor(event.kind, channelHint);
  const chipTone = chipToneFor(event.kind, channelHint);
  const ticketIdShort = 'ticketId' in event && event.ticketId ? event.ticketId.slice(-8) : null;
  return (
    <div
      className="grid items-center gap-3 px-4 pl-12 py-2.5 text-[12px] hover:bg-surface-2/60 transition-colors"
      style={{ gridTemplateColumns: 'auto 76px 130px 1fr 80px' }}
      data-testid="event-row"
    >
      <StatusDot
        tone={dotTone}
        pulse={(event.kind as string) === 'agent.spawned'}
      />
      <span
        className="font-mono font-tabular text-text-3"
        title={formatIsoFull(event.at)}
      >
        {ts}
      </span>
      <span className="min-w-0">
        <Chip tone={chipTone}>{event.kind}</Chip>
      </span>
      <span className="font-mono text-text-2 truncate">
        <EventDescription event={event} />
      </span>
      <span className="text-right">
        {ticketIdShort ? (
          <Link
            href={`/tickets/${(event as { ticketId: string }).ticketId}`}
            className="font-mono text-[11.5px] text-info hover:underline"
          >
            {ticketIdShort}
          </Link>
        ) : (
          <span className="text-text-4 text-[11.5px]">—</span>
        )}
      </span>
    </div>
  );
}

// CHAT_MUTATION_KINDS centralises the kind-set so EventDescription's
// dispatch is a single Set lookup instead of an 8-branch || chain.
const CHAT_MUTATION_KINDS = new Set([
  'chat.ticket.created',
  'chat.ticket.edited',
  'chat.ticket.transitioned',
  'chat.agent.paused',
  'chat.agent.resumed',
  'chat.agent.spawned',
  'chat.agent.config_edited',
  'chat.hiring.proposed',
]);

function shortId(id: string | null | undefined): string {
  return id ? id.slice(-8) : '—';
}

// Inline transition rendering: dept slug at text-2, slash separator
// at text-4, source column at text-3, arrow at text-4, destination
// at text-1 (full contrast). Source dim, destination loud.
function TicketTransitioned({ from, to, dept }: Readonly<{ from: string; to: string; dept?: string }>) {
  return (
    <span className="inline-flex items-center gap-1.5">
      {dept ? <><span className="text-text-2">{dept}</span><span className="text-text-4">/</span></> : null}
      <span className="text-text-3">{from}</span>
      <span className="text-text-4 mx-0.5" aria-hidden>→</span>
      <span className="text-text-1">{to}</span>
    </span>
  );
}

// CHAT_LIFECYCLE_VERBS keeps describeChatLifecycle's per-kind copy in
// one place so the function body stays a single render expression
// (avoids the nested-ternary lint).
const CHAT_LIFECYCLE_VERBS: Record<'chat.session_started' | 'chat.message_sent' | 'chat.session_ended', string> = {
  'chat.session_started': 'started',
  'chat.message_sent': 'message sent',
  'chat.session_ended': 'ended',
};

function describeChatLifecycle(kind: keyof typeof CHAT_LIFECYCLE_VERBS, sessionId: string) {
  const s = shortId(sessionId);
  return <span className="text-text-3">Chat session <span className="text-text-2 font-mono">{s}</span> {CHAT_LIFECYCLE_VERBS[kind]}</span>;
}

function describeChatMutation(event: ActivityEvent): React.ReactNode {
  if (event.kind === 'chat.ticket.transitioned') {
    return (
      <span className="text-text-3">
        Chat transitioned <span className="text-text-2 font-mono">{shortId(event.affectedResourceId)}</span>:{' '}
        <TicketTransitioned from={event.extras.from_column ?? '?'} to={event.extras.to_column ?? '?'} />
      </span>
    );
  }
  // Other chat-mutation kinds share a uniform shape: "Chat <verb> <id>".
  // The kind's tail (e.g. "ticket.created") becomes "ticket created"
  // verbatim — the verb-set is closed and the wording was approved
  // during /speckit.clarify.
  if ('kind' in event && CHAT_MUTATION_KINDS.has(event.kind)) {
    const verb = event.kind.replace('chat.', '').replace('.', ' ');
    const tail = shortId('affectedResourceId' in event ? event.affectedResourceId : null);
    return <span className="text-text-3">Chat {verb} <span className="text-text-2 font-mono">{tail}</span></span>;
  }
  return null;
}

function EventDescription({ event }: Readonly<{ event: ActivityEvent }>) {
  switch (event.kind) {
    case 'ticket.created':
      return <span className="text-text-3">created</span>;
    case 'ticket.transitioned':
      return <TicketTransitioned dept={event.department} from={event.from} to={event.to} />;
    case 'unknown':
      return <span className="text-text-3">unknown channel: {event.channel}</span>;
    case 'chat.session_deleted':
      return (
        <span className="text-text-3">
          Chat thread <span className="text-text-2 font-mono">{shortId(event.chatSessionId)}</span> deleted by operator
        </span>
      );
    case 'chat.session_started':
    case 'chat.message_sent':
    case 'chat.session_ended':
      return describeChatLifecycle(event.kind, event.chatSessionId);
    default: {
      const chat = describeChatMutation(event);
      if (chat) return chat;
      // M4 mutation event variants (ticket.edited / agent.edited /
      // vault.*) — generic kind description until each gets dedicated
      // rendering wired in by their respective tasks.
      return <span className="text-text-3">{event.kind}</span>;
    }
  }
}
