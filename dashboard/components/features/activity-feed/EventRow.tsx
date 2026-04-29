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

// Inline transition rendering: dept slug at text-2, slash separator
// at text-4, source column at text-3, arrow at text-4, destination
// at text-1 (full contrast). Source dim, destination loud.
function EventDescription({ event }: Readonly<{ event: ActivityEvent }>) {
  if (event.kind === 'ticket.created') {
    return <span className="text-text-3">created</span>;
  }
  if (event.kind === 'ticket.transitioned') {
    return (
      <span className="inline-flex items-center gap-1.5">
        <span className="text-text-2">{event.department}</span>
        <span className="text-text-4">/</span>
        <span className="text-text-3">{event.from}</span>
        <span className="text-text-4 mx-0.5" aria-hidden>→</span>
        <span className="text-text-1">{event.to}</span>
      </span>
    );
  }
  if (event.kind === 'unknown') {
    return <span className="text-text-3">unknown channel: {event.channel}</span>;
  }
  if (event.kind === 'chat.session_deleted') {
    // M5.2 (FR-322 amended): chat-flavoured concise predicate per the
    // M3 audit-event design language. The actor is named generically
    // "operator" because Garrison is single-operator per Constitution X
    // — multi-operator naming lands post-M5 with the actor lookup.
    const sessionShort = event.chatSessionId ? event.chatSessionId.slice(-8) : '—';
    return (
      <span className="text-text-3">
        Chat thread <span className="text-text-2 font-mono">{sessionShort}</span> deleted by operator
      </span>
    );
  }
  // M4 mutation event variants (ticket.edited / agent.edited /
  // vault.*). Each has dedicated rendering wired in by T011 /
  // T012 / T013. Until those tasks land, render a generic
  // description that names the kind so the operator never sees
  // a blank row.
  return <span className="text-text-3">{event.kind}</span>;
}
