'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { EventRow } from './EventRow';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { ActivityEvent } from '@/lib/sse/events';

// A "run" is a group of events sharing an agent_instance_id (the
// transitioned events from a single agent invocation). Per FR-061,
// each run renders as a one-line collapsed summary by default;
// expanding it reveals every event in chronological order.
//
// Two visual treatments:
//
//   - **Rollup row** (this component): chevron + "<n> events" mono
//     count + dept / src → dst transition + relative time + short
//     run id. 32px tall, hover-tints the surface. Click toggles.
//   - **Individual event row** (EventRow): status dot + HH:MM:SS +
//     event-type chip + transition + ticket link. Renders below
//     the rollup row when expanded.
//
// Self-loops (first.from === last.to over 2+ events): the agent
// re-attempted the same column move and ended where it started.
// Don't render as a transition arrow — show a "heartbeat" label
// in muted text-3 + idle dot. Operator can still click to expand.

const SHORT_ID_LEN = 8;

function shortId(id: string): string {
  // Real production agent_instance ids are gen_random_uuid()
  // strings; the seed uses fixed ids whose first 8 chars are
  // all '0'. Slicing from the END gives meaningful identifiers
  // for both ('aaaa00000001' → 'aaa00001' rather than '00000000').
  return id.slice(-SHORT_ID_LEN);
}

type Transition = { department: string; fromCol: string; toCol: string };

function readTransition(
  first: ActivityEvent | undefined,
  last: ActivityEvent | undefined,
): Transition | null {
  if (first?.kind !== 'ticket.transitioned') return null;
  const toCol = last?.kind === 'ticket.transitioned' ? last.to : first.to;
  return { department: first.department, fromCol: first.from, toCol };
}

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

  const transition = readTransition(first, last);
  const heartbeat = transition !== null && transition.fromCol === transition.toCol;
  const latestAt = last?.at ?? first?.at;

  return (
    <div className="border-b border-border-1 last:border-b-0" data-testid="run-group">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full grid items-center gap-3 px-4 h-8 text-left hover:bg-surface-2/60 transition-colors"
        style={{ gridTemplateColumns: 'auto 84px 1fr auto auto' }}
        aria-expanded={open}
      >
        <span
          className="text-text-3 text-[10px] font-mono w-3 inline-block transition-transform"
          style={{ transform: open ? 'rotate(90deg)' : 'rotate(0deg)' }}
          aria-hidden
        >
          ▸
        </span>
        <span className="text-text-3 text-[11px] font-mono font-tabular border-r border-border-1 pr-3">
          {events.length} {events.length === 1 ? 'event' : 'events'}
        </span>
        <RunSummary
          runId={runId}
          transition={transition}
          heartbeat={heartbeat}
          heartbeatLabel={t('heartbeat')}
          unattributedLabel={t('unattributed')}
          activityLabel={t('ticketActivity')}
        />
        <span
          className="text-text-3 text-[10.5px] font-mono font-tabular"
          title={latestAt ? formatIsoFull(latestAt) : undefined}
        >
          {latestAt ? relativeTime(latestAt) : ''}
        </span>
        <span className="text-text-4 text-[10.5px] font-mono">
          {runId ? shortId(runId) : '—'}
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

function RunSummary({
  runId,
  transition,
  heartbeat,
  heartbeatLabel,
  unattributedLabel,
  activityLabel,
}: Readonly<{
  runId: string | null;
  transition: Transition | null;
  heartbeat: boolean;
  heartbeatLabel: string;
  unattributedLabel: string;
  activityLabel: string;
}>) {
  if (heartbeat && transition) {
    return (
      <span className="font-mono text-[11.5px] flex items-center gap-1.5 truncate">
        <span className="inline-block w-1.5 h-1.5 rounded-full bg-text-4" aria-hidden />
        <span className="text-text-2">{transition.department}</span>
        <span className="text-text-4">/</span>
        <span className="text-text-3">{transition.toCol}</span>
        <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] ml-2">
          {heartbeatLabel}
        </span>
      </span>
    );
  }
  if (transition) {
    return (
      <span className="font-mono text-[11.5px] flex items-center gap-2 truncate">
        <span className="text-text-2">{transition.department}</span>
        <span className="text-text-4">/</span>
        <span className="text-text-3">{transition.fromCol}</span>
        <span className="text-text-4 mx-0.5" aria-hidden>→</span>
        <span className="text-text-1">{transition.toCol}</span>
      </span>
    );
  }
  return (
    <span className="font-mono text-[11.5px] text-text-3 truncate">
      {runId === null ? unattributedLabel : activityLabel}
    </span>
  );
}
