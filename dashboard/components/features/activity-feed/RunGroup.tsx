'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { Chip } from '@/components/ui/Chip';
import { EventRow } from './EventRow';
import type { ActivityEvent } from '@/lib/sse/events';

// A "run" is a group of events sharing an agent_instance_id (the
// transitioned events from a single agent invocation). Per FR-061,
// each run renders as a one-line collapsed summary by default;
// expanding it reveals every event in chronological order.
//
// Events without an agent_instance_id (manual SQL transitions,
// ticket-created events) render under a synthetic "unattributed"
// run that's always expanded.

export function RunGroup({
  runId,
  events,
}: Readonly<{
  runId: string | null;
  events: ActivityEvent[];
}>) {
  const t = useTranslations('common');
  const [open, setOpen] = useState(runId === null);
  const sorted = [...events].sort(
    (a, b) => new Date(a.at).getTime() - new Date(b.at).getTime(),
  );
  const first = sorted[0];
  const last = sorted.at(-1);
  let summaryDescription: string;
  if (!first) {
    summaryDescription = 'empty run';
  } else if (first.kind === 'ticket.transitioned') {
    const lastTo = last?.kind === 'ticket.transitioned' ? last.to : '?';
    summaryDescription = `${first.department}: ${first.from} → ${lastTo}`;
  } else {
    summaryDescription = 'ticket activity';
  }

  return (
    <div className="border border-border-1 rounded bg-surface-1" data-testid="run-group">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center gap-3 px-3 py-2 text-xs hover:bg-surface-2"
        aria-expanded={open}
      >
        <Chip tone="info">{events.length} events</Chip>
        <span className="font-mono text-text-2 truncate flex-1 text-left">
          {runId ? runId.slice(0, 8) : 'unattributed'}
        </span>
        <span className="text-text-3 truncate">{summaryDescription}</span>
        <span className="text-text-3 font-mono">{open ? t('collapse') : t('expand')}</span>
      </button>
      {open ? (
        <div className="border-t border-border-1 divide-y divide-border-1">
          {sorted.map((ev) => (
            <EventRow key={ev.eventId} event={ev} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
