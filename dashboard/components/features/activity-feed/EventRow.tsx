'use client';

import { Chip } from '@/components/ui/Chip';
import type { ActivityEvent } from '@/lib/sse/events';

export function EventRow({ event }: Readonly<{ event: ActivityEvent }>) {
  const ts = new Date(event.at).toISOString().slice(11, 19) + 'Z';
  return (
    <div className="grid grid-cols-12 gap-2 px-3 py-1 items-center text-xs" data-testid="event-row">
      <span className="col-span-2 font-mono text-text-3">{ts}</span>
      <span className="col-span-3">
        <Chip tone={tone(event.kind)}>{event.kind}</Chip>
      </span>
      <span className="col-span-7 font-mono text-text-2 truncate">{describe(event)}</span>
    </div>
  );
}

function tone(kind: ActivityEvent['kind']): 'info' | 'ok' | 'neutral' {
  if (kind === 'ticket.created') return 'info';
  if (kind === 'ticket.transitioned') return 'ok';
  return 'neutral';
}

function describe(event: ActivityEvent): string {
  if (event.kind === 'ticket.created') {
    return `ticket ${event.ticketId.slice(0, 8)} created`;
  }
  if (event.kind === 'ticket.transitioned') {
    return `${event.department}: ${event.from} → ${event.to} (ticket ${event.ticketId.slice(0, 8)})`;
  }
  return `unknown channel: ${event.channel}`;
}
