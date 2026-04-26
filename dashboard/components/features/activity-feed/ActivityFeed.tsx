'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { useVirtualizer } from '@tanstack/react-virtual';
import { RunGroup } from './RunGroup';
import { EmptyState } from '@/components/ui/EmptyState';
import type { ActivityEvent } from '@/lib/sse/events';

// Live activity feed (FR-061 → FR-065).
//
// Opens an EventSource to /api/sse/activity. Buckets incoming
// events by agent_instance_id into RunGroups; rows are
// virtualized via @tanstack/react-virtual so render cost stays
// bounded as event volume grows.
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

  const parentRef = useRef<HTMLDivElement | null>(null);
  const virtualizer = useVirtualizer({
    count: filteredRuns.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 80,
    overscan: 5,
  });

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

  const totalEvents = filteredRuns.reduce(
    (acc, r) => acc + r.events.length,
    0,
  );

  return (
    <section className="bg-surface-1 border border-border-1 rounded h-full flex flex-col min-h-0 overflow-hidden">
      <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
        <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
          {t('feedHeader')}
        </span>
        <span className="text-text-3 text-[11px] font-mono font-tabular">
          <span className="text-text-1">{totalEvents}</span> {t('events')}
        </span>
        <span className="ml-auto inline-flex items-center gap-1.5 text-[11px]">
          <span
            className={`inline-block w-1.5 h-1.5 rounded-full ${statusDotClass}`}
            aria-hidden
          />
          <span data-testid="sse-status" className={`${statusToneClass} font-mono`}>
            {statusLabel}
          </span>
        </span>
      </header>
      {filteredRuns.length === 0 ? (
        <div className="flex-1 grid place-items-center px-6 py-12 text-text-3 text-[12.5px] text-center">
          {t('empty')}
        </div>
      ) : (
        <div ref={parentRef} className="flex-1 overflow-y-auto">
          <div
            style={{
              height: `${virtualizer.getTotalSize()}px`,
              position: 'relative',
            }}
            data-testid="activity-feed-list"
          >
            {virtualizer.getVirtualItems().map((virtualItem) => {
              const run = filteredRuns[virtualItem.index];
              return (
                <div
                  key={run.runId ?? `un_${run.events[0].eventId}`}
                  ref={virtualizer.measureElement}
                  data-index={virtualItem.index}
                  style={{
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    transform: `translateY(${virtualItem.start}px)`,
                  }}
                >
                  <RunGroup runId={run.runId} events={run.events} />
                </div>
              );
            })}
          </div>
        </div>
      )}
    </section>
  );
}
