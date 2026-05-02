'use client';

import { useMemo } from 'react';
import { useTranslations } from 'next-intl';
import { Chip } from '@/components/ui/Chip';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import {
  useThrottleStream,
  type ThrottleEvent,
} from '@/lib/sse/throttleStream';
import type { ThrottleEventRow } from '@/lib/queries/throttle';

// M6 — throttle events sub-table on /hygiene (plan §"Phase 9").
//
// Renders a list of recent throttle_events rows below the existing
// HygieneTable. The initial rows come from the SSR'd snapshot
// (`listThrottleEvents`); newly-arrived `throttle_event` SSE
// events from `useThrottleStream()` are merged in at the top by
// event_id (dedupe).
//
// Column shape:
//   - fired_at        — relative time; ISO tooltip on hover
//   - company         — joined company name
//   - kind            — Chip; tone='warn' for rate_limit_pause,
//                       tone='err' for company_budget_exceeded
//   - payload preview — first 80 chars of JSON-stringified payload
//                       (truncated with an ellipsis when over)

const PAYLOAD_PREVIEW_LIMIT = 80;

export interface MergedRow {
  eventId: string;
  companyId: string;
  companyName: string;
  kind: string;
  firedAt: Date;
  payload: unknown;
}

export function kindTone(kind: string): 'warn' | 'err' | 'neutral' {
  if (kind === 'rate_limit_pause') return 'warn';
  if (kind === 'company_budget_exceeded') return 'err';
  return 'neutral';
}

export function previewPayload(payload: unknown): string {
  let text: string;
  try {
    text = typeof payload === 'string' ? payload : JSON.stringify(payload ?? {});
  } catch {
    text = '{}';
  }
  if (text.length <= PAYLOAD_PREVIEW_LIMIT) return text;
  return text.slice(0, PAYLOAD_PREVIEW_LIMIT) + '…';
}

export function mergeRows(
  initial: ThrottleEventRow[],
  liveEvents: ThrottleEvent[],
  companyNameById: Map<string, string>,
): MergedRow[] {
  const seen = new Set<string>();
  const merged: MergedRow[] = [];
  for (const ev of liveEvents) {
    if (seen.has(ev.event_id)) continue;
    seen.add(ev.event_id);
    merged.push({
      eventId: ev.event_id,
      companyId: ev.company_id,
      companyName: companyNameById.get(ev.company_id) ?? ev.company_id.slice(0, 8),
      kind: ev.kind,
      firedAt: new Date(ev.fired_at),
      payload: {},
    });
  }
  for (const r of initial) {
    if (seen.has(r.eventId)) continue;
    seen.add(r.eventId);
    merged.push({
      eventId: r.eventId,
      companyId: r.companyId,
      companyName: r.companyName,
      kind: r.kind,
      firedAt: r.firedAt instanceof Date ? r.firedAt : new Date(r.firedAt),
      payload: r.payload,
    });
  }
  // Already in newest-first order: live events were prepended,
  // initial rows come from a DESC-sorted query.
  return merged;
}

export function ThrottleEventsTable({
  initialRows,
}: Readonly<{ initialRows: ThrottleEventRow[] }>) {
  const t = useTranslations('hygieneMeta');
  const { events, lastError } = useThrottleStream();

  const companyNameById = useMemo(() => {
    const m = new Map<string, string>();
    for (const r of initialRows) m.set(r.companyId, r.companyName);
    return m;
  }, [initialRows]);

  const rows = useMemo(
    () => mergeRows(initialRows, events, companyNameById),
    [initialRows, events, companyNameById],
  );

  return (
    <section
      className="bg-surface-1 border border-border-1 rounded overflow-hidden"
      data-testid="throttle-events-table"
    >
      <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
        <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
          {t('throttleEventsHeader')}
        </span>
        <Chip>{rows.length}</Chip>
        {lastError ? (
          <span className="text-warn text-[10.5px] font-mono ml-auto">
            {lastError}
          </span>
        ) : null}
      </header>
      {rows.length === 0 ? (
        <div className="px-4 py-5 text-center text-text-3 text-[12px]">
          {t('throttleEventsEmpty')}
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full">
            <colgroup>
              <col style={{ width: 160 }} />
              <col style={{ width: 200 }} />
              <col style={{ width: 200 }} />
              <col />
            </colgroup>
            <thead>
              <tr className="bg-surface-2 border-b border-border-1">
                <Th>{t('headers.time')}</Th>
                <Th>{t('headers.company')}</Th>
                <Th>{t('headers.kind')}</Th>
                <Th>{t('headers.payload')}</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border-1">
              {rows.map((r) => (
                <tr
                  key={r.eventId}
                  className="hover:bg-surface-2/60 transition-colors"
                  data-testid="throttle-event-row"
                >
                  <Td>
                    <span
                      className="font-mono font-tabular text-[12px] text-text-3"
                      title={formatIsoFull(r.firedAt)}
                      suppressHydrationWarning
                    >
                      {relativeTime(r.firedAt)}
                    </span>
                  </Td>
                  <Td>
                    <span className="text-[12.5px] text-text-2">
                      {r.companyName}
                    </span>
                  </Td>
                  <Td>
                    <Chip tone={kindTone(r.kind)}>{r.kind}</Chip>
                  </Td>
                  <Td>
                    <span className="font-mono text-[11.5px] text-text-3 break-all">
                      {previewPayload(r.payload)}
                    </span>
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Th({
  children,
  align = 'left',
}: Readonly<{ children: React.ReactNode; align?: 'left' | 'right' }>) {
  return (
    <th
      className={`px-3 py-2 text-text-3 font-medium text-[10.5px] uppercase tracking-[0.08em] ${
        align === 'right' ? 'text-right' : 'text-left'
      }`}
    >
      {children}
    </th>
  );
}

function Td({
  children,
  className = '',
}: Readonly<{ children: React.ReactNode; className?: string }>) {
  return <td className={`px-3 py-2.5 align-middle ${className}`}>{children}</td>;
}
