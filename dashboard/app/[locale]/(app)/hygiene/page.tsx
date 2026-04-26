import { getTranslations } from 'next-intl/server';
import {
  fetchHygieneRows,
  fetchHygieneCounts,
  type FailureMode,
} from '@/lib/queries/hygiene';
import { HygieneTable } from '@/components/features/hygiene-table/HygieneTable';
import { FailureModeFilter } from '@/components/features/hygiene-table/FailureModeFilter';
import { RefreshButton } from '@/components/features/org-overview/RefreshButton';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';

// Hygiene table surface (FR-070 → FR-075). 30s soft-poll, URL-
// persisted filters, links to ticket detail. Read-only.

export const dynamic = 'force-dynamic';

const VALID_MODES: FailureMode[] = [
  'finalize_path',
  'sandbox_escape',
  'suspected_secret_emitted',
];

function parseMode(
  raw: string | string[] | undefined,
): FailureMode | undefined {
  if (typeof raw !== 'string') return undefined;
  return (VALID_MODES as string[]).includes(raw)
    ? (raw as FailureMode)
    : undefined;
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

  const [hygiene, counts, t, navT, metaT] = await Promise.all([
    fetchHygieneRows({ failureMode, departmentSlug: dept, page, pageSize: 25 }),
    fetchHygieneCounts(),
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

      <FailureModeFilter counts={counts.byMode} total={counts.total} />

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
      <SoftPoll intervalMs={30_000} />
    </div>
  );
}
