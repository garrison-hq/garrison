'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { RunGroup } from './RunGroup';
import { ActivityKPIStrip } from './ActivityKPIStrip';
import type { ActivityEvent } from '@/lib/sse/events';

// Live activity feed (FR-061 → FR-065).
//
// Opens an EventSource to /api/sse/activity. Buckets incoming
// events by agent_instance_id into RunGroups; rows render as a
// flat list with day separators between days. Virtualization was
// removed in the polish round — at expected event volume (a few
// dozen runs in the operator's session-active window) the layout
// cost is negligible and dropping the virtualizer simplifies the
// day-separator logic.
//
// FR-064: the EventSource auto-reconnects on disconnect. The SSE
// route's catch-up flow uses Last-Event-ID, which the browser
// sets automatically from the `id:` line on each frame.

interface RunBucket {
  runId: string | null;
  events: ActivityEvent[];
  /** Most recent event timestamp; used for reverse-chronological ordering. */
  latestAt: number;
}

function bucketEvent(buckets: Map<string, RunBucket>, ev: ActivityEvent): Map<string, RunBucket> {
  // Pure: returns a new Map with a new bucket value where the event
  // belongs. React StrictMode runs setState updaters twice in dev to
  // catch impure mutations — pushing into existing.events directly
  // would land the same event in the bucket twice and the EventRow
  // list would crash with duplicate React keys. Also dedupes against
  // existing eventIds in the bucket as a defense-in-depth check.
  const runId =
    ev.kind === 'ticket.transitioned' ? (ev.agentInstanceId ?? null) : null;
  const key = runId ?? `__unattributed_${ev.eventId}`;
  const next = new Map(buckets);
  const existing = next.get(key);
  if (existing) {
    if (existing.events.some((e) => e.eventId === ev.eventId)) return next;
    const ts = new Date(ev.at).getTime();
    next.set(key, {
      runId,
      events: [...existing.events, ev],
      latestAt: Math.max(ts, existing.latestAt),
    });
  } else {
    next.set(key, {
      runId,
      events: [ev],
      latestAt: new Date(ev.at).getTime(),
    });
  }
  return next;
}

function dayKey(ms: number): string {
  // YYYY-MM-DD in UTC, used both as the React key for the day
  // separator and as the visible label after relabel-today.
  return new Date(ms).toISOString().slice(0, 10);
}

const DAY_LABEL_FORMATTER_MONTHS = [
  'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
  'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
];
function formatDayLabel(key: string, today: string): string {
  if (key === today) return 'Today';
  const yest = new Date(Date.now() - 86_400_000).toISOString().slice(0, 10);
  if (key === yest) return 'Yesterday';
  const d = new Date(`${key}T00:00:00Z`);
  return `${DAY_LABEL_FORMATTER_MONTHS[d.getUTCMonth()]} ${String(d.getUTCDate()).padStart(2, '0')}`;
}

export function ActivityFeed() {
  const t = useTranslations('activityMeta');
  const search = useSearchParams();
  const [buckets, setBuckets] = useState<Map<string, RunBucket>>(new Map());
  const [status, setStatus] = useState<'connecting' | 'live' | 'reconnecting'>('connecting');
  const seenRef = useRef<Set<string>>(new Set());
  const filterKind = search.get('kind') ?? 'all';

  useEffect(() => {
    const es = new EventSource('/api/sse/activity', { withCredentials: true });
    setStatus('connecting');

    function handleEvent(this: EventSource, msg: MessageEvent) {
      try {
        const event = JSON.parse(msg.data) as ActivityEvent;
        if (seenRef.current.has(event.eventId)) return;
        seenRef.current.add(event.eventId);
        setBuckets((prev) => bucketEvent(prev, event));
      } catch {
        // ignore malformed frames
      }
    }

    es.addEventListener('open', () => setStatus('live'));
    es.addEventListener('error', () => setStatus('reconnecting'));
    es.addEventListener('ticket.created', handleEvent as EventListener);
    es.addEventListener('ticket.transitioned', handleEvent as EventListener);
    es.addEventListener('unknown', handleEvent as EventListener);

    return () => {
      es.close();
    };
  }, []);

  const filteredRuns = useMemo(() => {
    const runs = Array.from(buckets.values());
    const filtered = filterKind === 'all'
      ? runs
      : runs
          .map((r) => ({ ...r, events: r.events.filter((e) => e.kind === filterKind) }))
          .filter((r) => r.events.length > 0);
    return filtered.sort((a, b) => b.latestAt - a.latestAt);
  }, [buckets, filterKind]);

  // KPI counts across the visible filtered window.
  const counts = useMemo(() => {
    const all = filteredRuns.flatMap((r) => r.events);
    const lastHourMs = Date.now() - 60 * 60 * 1000;
    return {
      events: all.length,
      transitions: all.filter((e) => e.kind === 'ticket.transitioned').length,
      creates: all.filter((e) => e.kind === 'ticket.created').length,
      lastHour: all.filter((e) => new Date(e.at).getTime() >= lastHourMs).length,
    };
  }, [filteredRuns]);

  const today = dayKey(Date.now());

  // Status indicator classes — three states.
  const statusToneClass =
    status === 'live'
      ? 'text-ok'
      : status === 'reconnecting'
        ? 'text-warn'
        : 'text-text-3';
  const statusDotClass =
    status === 'live'
      ? 'bg-ok animate-pulse'
      : status === 'reconnecting'
        ? 'bg-warn'
        : 'bg-text-3';
  const statusLabel = t.has(status) ? t(status) : status;

  return (
    <div className="h-full flex flex-col min-h-0 gap-4">
      <ActivityKPIStrip
        events={counts.events}
        transitions={counts.transitions}
        creates={counts.creates}
        lastHour={counts.lastHour}
      />
      <section className="bg-surface-1 border border-border-1 rounded flex-1 flex flex-col min-h-0 overflow-hidden">
        <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
          <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
            {t('feedHeader')}
          </span>
          <span className="text-text-3 text-[11px] font-mono font-tabular">
            <span className="text-text-1">{counts.events}</span> {t('events')}
          </span>
          <span className="ml-auto inline-flex items-center gap-1.5 text-[11px]">
            <span
              className={`inline-block w-1.5 h-1.5 rounded-full ${statusDotClass}`}
              aria-hidden
            />
            <span data-testid="sse-status" className={`${statusToneClass} font-mono uppercase tracking-[0.08em] text-[10.5px]`}>
              {statusLabel}
            </span>
          </span>
        </header>
        {filteredRuns.length === 0 ? (
          <div className="flex-1 grid place-items-center px-6 py-12 text-text-3 text-[12.5px] text-center">
            {t('empty')}
          </div>
        ) : (
          <div className="flex-1 overflow-y-auto" data-testid="activity-feed-list">
            {(() => {
              // Walk the sorted runs and emit a day separator each
              // time the latestAt's day changes. The list is in
              // reverse-chronological order (newest first).
              const out: React.ReactNode[] = [];
              let lastDay: string | null = null;
              for (const run of filteredRuns) {
                const d = dayKey(run.latestAt);
                if (d !== lastDay) {
                  out.push(
                    <DaySeparator
                      key={`day-${d}`}
                      label={formatDayLabel(d, today)}
                    />,
                  );
                  lastDay = d;
                }
                out.push(
                  <RunGroup
                    key={run.runId ?? `un_${run.events[0].eventId}`}
                    runId={run.runId}
                    events={run.events}
                  />,
                );
              }
              return out;
            })()}
          </div>
        )}
      </section>
    </div>
  );
}

function DaySeparator({ label }: Readonly<{ label: string }>) {
  return (
    <div className="px-4 py-1.5 bg-surface-2/50 border-b border-border-1 text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
      {label}
    </div>
  );
}
