'use client';

import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { columnTextClass } from '@/lib/format/columnTone';
import { formatIsoFull } from '@/lib/format/relativeTime';
import type { ActivityEvent } from '@/lib/sse/events';

// Single row in an expanded RunGroup. Layout mirrors the hygiene
// table density (mono timestamps + tone-coded transition labels +
// a ticket link on the right), but at one indent level deeper so
// the row reads as a child of the run header above.

const EVENT_KIND_TONE: Record<ActivityEvent['kind'], 'info' | 'ok' | 'neutral'> = {
  'ticket.created': 'info',
  'ticket.transitioned': 'ok',
  unknown: 'neutral',
};

export function EventRow({ event }: Readonly<{ event: ActivityEvent }>) {
  const ts = new Date(event.at).toISOString().slice(11, 19);
  return (
    <div
      className="grid items-center gap-3 px-4 pl-12 py-2 text-[12px] hover:bg-surface-2/60 transition-colors"
      style={{ gridTemplateColumns: '90px 130px 1fr 90px' }}
      data-testid="event-row"
    >
      <span
        className="font-mono font-tabular text-text-3"
        title={formatIsoFull(event.at)}
      >
        {ts}
      </span>
      <span>
        <Chip tone={EVENT_KIND_TONE[event.kind]}>{event.kind}</Chip>
      </span>
      <span className="font-mono text-text-2 truncate">
        <EventDescription event={event} />
      </span>
      <span className="text-right">
        {'ticketId' in event && event.ticketId ? (
          <Link
            href={`/tickets/${event.ticketId}`}
            className="font-mono text-[11.5px] text-info hover:underline"
          >
            {event.ticketId.slice(0, 8)}
          </Link>
        ) : (
          <span className="text-text-3 text-[11.5px]">—</span>
        )}
      </span>
    </div>
  );
}

function EventDescription({ event }: Readonly<{ event: ActivityEvent }>) {
  if (event.kind === 'ticket.created') {
    return <span>created</span>;
  }
  if (event.kind === 'ticket.transitioned') {
    return (
      <span className="inline-flex items-center gap-1.5">
        <span className="text-text-2">{event.department}</span>
        <span className="text-text-4">·</span>
        <span className={columnTextClass(event.from)}>{event.from}</span>
        <span className="text-text-4" aria-hidden>→</span>
        <span className={columnTextClass(event.to)}>{event.to}</span>
      </span>
    );
  }
  return <span className="text-text-3">unknown channel: {event.channel}</span>;
}
