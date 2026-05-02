import { getTranslations } from 'next-intl/server';
import {
  fetchHygieneRows,
  fetchHygieneCounts,
  type FailureMode,
  type HygieneTabFilter,
} from '@/lib/queries/hygiene';
import { listThrottleEvents } from '@/lib/queries/throttle';
import { HygieneTable } from '@/components/features/hygiene-table/HygieneTable';
import { HygieneTabStripClient } from '@/components/features/hygiene-table/HygieneTabStripClient';
import { ThrottleEventsTable } from '@/components/features/hygiene-table/ThrottleEventsTable';
import { FailureModeFilter } from '@/components/features/hygiene-table/FailureModeFilter';
import { PatternCategoryFilter } from '@/components/features/hygiene-table/PatternCategoryFilter';
import { RefreshButton } from '@/components/features/org-overview/RefreshButton';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';

// Hygiene table surface (FR-070 → FR-075). 30s soft-poll, URL-
// persisted filters, links to ticket detail. Read-only.
//
// M6 / T016 — three-tab strip + throttle-events sub-table.
// The tab defaults to 'failures' (agent failure-mode rows minus
// operator drags); 'audit' surfaces operator_initiated rows only;
// 'all' replicates the pre-M6 behaviour.

export const dynamic = 'force-dynamic';

const VALID_MODES: FailureMode[] = [
  'finalize_path',
  'sandbox_escape',
  'suspected_secret_emitted',
];

const VALID_TABS: HygieneTabFilter[] = ['failures', 'audit', 'all'];

function parseMode(
  raw: string | string[] | undefined,
): FailureMode | undefined {
  if (typeof raw !== 'string') return undefined;
  return (VALID_MODES as string[]).includes(raw)
    ? (raw as FailureMode)
    : undefined;
}

function parseTab(raw: string | string[] | undefined): HygieneTabFilter {
  if (typeof raw === 'string' && (VALID_TABS as string[]).includes(raw)) {
    return raw as HygieneTabFilter;
  }
  return 'failures';
}

export default async function HygienePage({
  searchParams,
}: Readonly<{
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}>) {
  const sp = await searchParams;
  const failureMode = parseMode(sp.mode);
  const dept = typeof sp.dept === 'string' ? sp.dept : undefined;
  const page = typeof sp.page === 'string' ? Number(sp.page) || 1 : 1;
  const patternCategory = typeof sp.category === 'string' ? sp.category : undefined;
  const tab = parseTab(sp.tab);

  const [hygiene, counts, throttleRows, t, navT, metaT] = await Promise.all([
    fetchHygieneRows({
      failureMode,
      departmentSlug: dept,
      patternCategory,
      tab,
      page,
      pageSize: 25,
    }),
    fetchHygieneCounts(),
    listThrottleEvents(50),
    getTranslations('hygiene'),
    getTranslations('nav'),
    getTranslations('hygieneMeta'),
  ]);

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto">
      <header className="space-y-1">
        <div className="flex items-center justify-between">
          <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
            {navT('hygiene')}
          </h1>
          <RefreshButton />
        </div>
        <p className="text-text-3 text-xs">
          <span className="font-mono font-tabular">{counts.total}</span>{' '}
          {metaT('open')}
          {' · '}
          <span className="font-mono font-tabular">{counts.newToday}</span>{' '}
          {metaT('newToday')}
          {' · '}
          {metaT('gate')}:{' '}
          <span className="font-mono text-warn">{metaT('gateWarnOnly')}</span>
        </p>
      </header>

      <HygieneTabStripClient />
      <FailureModeFilter counts={counts.byMode} total={counts.total} />
      <PatternCategoryFilter />

      <section className="bg-surface-1 border border-border-1 rounded overflow-hidden">
        <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
          <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
            {metaT('openFlags')}
          </span>
          <span className="text-text-3 text-[11px] font-mono font-tabular">
            {hygiene.rows.length}
          </span>
        </header>
        <HygieneTable rows={hygiene.rows} emptyDescription={t('empty')} />
      </section>

      <ThrottleEventsTable initialRows={throttleRows} />

      <SoftPoll intervalMs={30_000} />
    </div>
  );
}
