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

function bucketEvent(buckets: Map<string, RunBucket>, ev: ActivityEvent): void {
  const runId =
    ev.kind === 'ticket.transitioned' ? (ev.agentInstanceId ?? null) : null;
  const key = runId ?? `__unattributed_${ev.eventId}`;
  const existing = buckets.get(key);
  if (existing) {
    existing.events.push(ev);
    const ts = new Date(ev.at).getTime();
    if (ts > existing.latestAt) existing.latestAt = ts;
  } else {
    buckets.set(key, {
      runId,
      events: [ev],
      latestAt: new Date(ev.at).getTime(),
    });
  }
}

export function ActivityFeed() {
  const t = useTranslations('activity');
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
        setBuckets((prev) => {
          const next = new Map(prev);
          bucketEvent(next, event);
          return next;
        });
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

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3 text-xs">
        <span data-testid="sse-status" className={
          status === 'live' ? 'text-ok' : status === 'reconnecting' ? 'text-warn' : 'text-text-3'
        }>
          {status === 'live' ? '● live' : status === 'reconnecting' ? '↻ reconnecting…' : 'connecting…'}
        </span>
        <span className="text-text-3">
          {filteredRuns.reduce((acc, r) => acc + r.events.length, 0)} events
        </span>
      </div>
      {filteredRuns.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <div ref={parentRef} className="h-[70vh] overflow-y-auto">
          <div
            style={{ height: `${virtualizer.getTotalSize()}px`, position: 'relative' }}
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
                  className="pb-2"
                >
                  <RunGroup runId={run.runId} events={run.events} />
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
