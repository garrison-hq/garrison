'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { EventRow } from './EventRow';
import { columnTextClass } from '@/lib/format/columnTone';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { ActivityEvent } from '@/lib/sse/events';

// A "run" is a group of events sharing an agent_instance_id (the
// transitioned events from a single agent invocation). Per FR-061,
// each run renders as a one-line collapsed summary by default;
// expanding it reveals every event in chronological order.
//
// Events without an agent_instance_id (manual SQL transitions,
// ticket-created events) render under a synthetic "unattributed"
// run that's expanded by default.

export function RunGroup({
  runId,
  events,
}: Readonly<{
  runId: string | null;
  events: ActivityEvent[];
}>) {
  const t = useTranslations('activityMeta');
  const [open, setOpen] = useState(runId === null);
  const sorted = [...events].sort(
    (a, b) => new Date(a.at).getTime() - new Date(b.at).getTime(),
  );
  const first = sorted[0];
  const last = sorted.at(-1);

  let department: string | null = null;
  let fromCol: string | null = null;
  let toCol: string | null = null;
  if (first?.kind === 'ticket.transitioned') {
    department = first.department;
    fromCol = first.from;
    toCol = last?.kind === 'ticket.transitioned' ? last.to : first.to;
  }

  const latestAt = last?.at ?? first?.at;

  return (
    <div className="border-b border-border-1 last:border-b-0" data-testid="run-group">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full grid items-center gap-3 px-4 py-2.5 text-left hover:bg-surface-2/60 transition-colors"
        style={{ gridTemplateColumns: 'auto 100px 1fr auto auto' }}
        aria-expanded={open}
      >
        <span
          className="text-text-3 text-[10px] font-mono w-4 inline-block transition-transform"
          style={{ transform: open ? 'rotate(90deg)' : 'rotate(0deg)' }}
          aria-hidden
        >
          ▸
        </span>
        <span className="text-text-3 text-[10.5px] font-mono font-tabular">
          {events.length} {events.length === 1 ? 'event' : 'events'}
        </span>
        {department && fromCol && toCol ? (
          <span className="font-mono text-[12px] flex items-center gap-1.5 truncate">
            <span className="text-text-2">{department}</span>
            <span className="text-text-4">·</span>
            <span className={columnTextClass(fromCol)}>{fromCol}</span>
            <span className="text-text-4" aria-hidden>→</span>
            <span className={columnTextClass(toCol)}>{toCol}</span>
          </span>
        ) : (
          <span className="font-mono text-[12px] text-text-3 truncate">
            {runId === null ? t('unattributed') : t('ticketActivity')}
          </span>
        )}
        <span
          className="text-text-3 text-[10.5px] font-mono font-tabular"
          title={latestAt ? formatIsoFull(latestAt) : undefined}
        >
          {latestAt ? relativeTime(latestAt) : ''}
        </span>
        <span className="text-text-3 text-[10.5px] font-mono">
          {runId ? runId.slice(0, 8) : ''}
        </span>
      </button>
      {open ? (
        <div className="bg-surface-2/40 border-t border-border-1 divide-y divide-border-1">
          {sorted.map((ev) => (
            <EventRow key={ev.eventId} event={ev} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
