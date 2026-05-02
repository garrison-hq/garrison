// M6 — ThrottleEventsTable pure-helper tests.
//
// The component itself wires `useThrottleStream` (a React hook with
// EventSource side effects) into a render pipeline; the pure logic
// lives in three exported helpers (kindTone, previewPayload,
// mergeRows). Tests target those — same shape as the M5.x chatStream
// "test the pure surface, not the hook" pattern. The hook + JSX
// rendering are exercised end-to-end by the M6.x Playwright suite
// when it lands.

import { describe, it, expect, vi } from 'vitest';
import { renderToString } from 'react-dom/server';

vi.mock('next-intl', () => ({
  useTranslations: () => (key: string) => key,
}));

import {
  kindTone,
  previewPayload,
  mergeRows,
  ThrottleEventsTable,
} from './ThrottleEventsTable';
import type { ThrottleEventRow } from '@/lib/queries/throttle';
import type { ThrottleEvent } from '@/lib/sse/throttleStream';

describe('ThrottleEventsTable.kindTone', () => {
  it('TestKindToneRateLimitPauseIsWarn', () => {
    expect(kindTone('rate_limit_pause')).toBe('warn');
  });
  it('TestKindToneCompanyBudgetExceededIsErr', () => {
    expect(kindTone('company_budget_exceeded')).toBe('err');
  });
  it('TestKindToneUnknownIsNeutral', () => {
    expect(kindTone('something_else')).toBe('neutral');
  });
});

describe('ThrottleEventsTable.previewPayload', () => {
  it('TestPreviewPayloadShortStringPassesThrough', () => {
    expect(previewPayload({ k: 'v' })).toBe('{"k":"v"}');
  });
  it('TestPreviewPayloadTruncatesAt80Chars', () => {
    const big = { description: 'x'.repeat(200) };
    const out = previewPayload(big);
    expect(out.length).toBeLessThanOrEqual(81); // 80 + the ellipsis
    expect(out.endsWith('…')).toBe(true);
  });
  it('TestPreviewPayloadHandlesNullAsEmptyObject', () => {
    expect(previewPayload(null)).toBe('{}');
  });
  it('TestPreviewPayloadStringPayloadEchoes', () => {
    expect(previewPayload('plain string')).toBe('plain string');
  });
});

describe('ThrottleEventsTable.mergeRows', () => {
  const initial: ThrottleEventRow[] = [
    {
      eventId: 'r-1',
      companyId: 'co-1',
      companyName: 'Acme',
      kind: 'company_budget_exceeded',
      firedAt: new Date('2026-05-02T10:00:00Z'),
      payload: { current_24h_usd: 0.95 },
    },
  ];
  const live: ThrottleEvent[] = [
    {
      event_id: 'r-2',
      company_id: 'co-1',
      kind: 'rate_limit_pause',
      fired_at: '2026-05-02T11:00:00Z',
    },
  ];

  it('TestMergeRowsLiveEventsPrepend', () => {
    const merged = mergeRows(initial, live, new Map([['co-1', 'Acme']]));
    expect(merged).toHaveLength(2);
    expect(merged[0].eventId).toBe('r-2');
    expect(merged[1].eventId).toBe('r-1');
  });

  it('TestMergeRowsDedupesByEventID', () => {
    // Same eventId in live + initial → live wins (live entries come first
    // and `seen` blocks the initial duplicate).
    const dup: ThrottleEventRow[] = [
      { ...initial[0], eventId: 'r-2', companyName: 'OldName' },
    ];
    const merged = mergeRows(dup, live, new Map([['co-1', 'NewName']]));
    expect(merged).toHaveLength(1);
    expect(merged[0].companyName).toBe('NewName');
  });

  it('TestMergeRowsFallsBackToCompanyIDPrefix', () => {
    const merged = mergeRows([], live, new Map());
    expect(merged[0].companyName).toBe('co-1'.slice(0, 8));
  });

  it('TestMergeRowsParsesFiredAtToDate', () => {
    const merged = mergeRows([], live, new Map());
    expect(merged[0].firedAt).toBeInstanceOf(Date);
  });
});

describe('ThrottleEventsTable render', () => {
  it('TestRendersEmptyStateWhenNoRowsPresent', () => {
    const html = renderToString(<ThrottleEventsTable initialRows={[]} />);
    // Empty branch surfaces the throttleEventsEmpty translation key
    // (the next-intl mock echoes keys verbatim).
    expect(html).toContain('throttleEventsEmpty');
    // Header row count = 0.
    expect(html).toContain('data-testid="throttle-events-table"');
    // No throttle-event-row tbody entries.
    expect(html).not.toContain('data-testid="throttle-event-row"');
  });

  it('TestRendersOneRowFromInitialSnapshot', () => {
    const initial: ThrottleEventRow[] = [
      {
        eventId: 'r-1',
        companyId: 'co-1',
        companyName: 'Acme Aerospace',
        kind: 'company_budget_exceeded',
        firedAt: new Date('2026-05-02T10:00:00Z'),
        payload: { current_24h_usd: 0.95 },
      },
    ];
    const html = renderToString(<ThrottleEventsTable initialRows={initial} />);
    // Non-empty branch: the table body renders + the row carries
    // the company name + the kind chip.
    expect(html).toContain('data-testid="throttle-event-row"');
    expect(html).toContain('Acme Aerospace');
    expect(html).toContain('company_budget_exceeded');
  });

  it('TestRendersTwoRowsFromInitialSnapshot', () => {
    const initial: ThrottleEventRow[] = [
      {
        eventId: 'r-1',
        companyId: 'co-1',
        companyName: 'Acme',
        kind: 'rate_limit_pause',
        firedAt: new Date('2026-05-02T10:00:00Z'),
        payload: {},
      },
      {
        eventId: 'r-2',
        companyId: 'co-2',
        companyName: 'Beta',
        kind: 'company_budget_exceeded',
        firedAt: new Date('2026-05-02T11:00:00Z'),
        payload: {},
      },
    ];
    const html = renderToString(<ThrottleEventsTable initialRows={initial} />);
    // Both rows render with their distinct company names.
    expect(html).toContain('Acme');
    expect(html).toContain('Beta');
    // Both kinds render.
    expect(html).toContain('rate_limit_pause');
    expect(html).toContain('company_budget_exceeded');
  });

  it('TestRendersTableHeadersWhenRowsPresent', () => {
    const initial: ThrottleEventRow[] = [
      {
        eventId: 'r-1',
        companyId: 'co-1',
        companyName: 'Acme',
        kind: 'rate_limit_pause',
        firedAt: new Date('2026-05-02T10:00:00Z'),
        payload: {},
      },
    ];
    const html = renderToString(<ThrottleEventsTable initialRows={initial} />);
    // The header row pulls headers.{time, company, kind, payload}
    // from next-intl; the mock echoes the key. Headers only render
    // in the non-empty branch.
    expect(html).toContain('headers.time');
    expect(html).toContain('headers.company');
    expect(html).toContain('headers.kind');
    expect(html).toContain('headers.payload');
  });
});
