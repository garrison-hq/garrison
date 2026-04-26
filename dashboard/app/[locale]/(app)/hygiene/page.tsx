import { getTranslations } from 'next-intl/server';
import { fetchHygieneRows, type FailureMode } from '@/lib/queries/hygiene';
import { HygieneTable } from '@/components/features/hygiene-table/HygieneTable';
import { FailureModeFilter } from '@/components/features/hygiene-table/FailureModeFilter';
import { RefreshButton } from '@/components/features/org-overview/RefreshButton';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';

// Hygiene table surface (FR-070 → FR-075). 30s soft-poll, URL-
// persisted filters, links to ticket detail. Read-only.

export const dynamic = 'force-dynamic';

const VALID_MODES: FailureMode[] = ['finalize_path', 'sandbox_escape', 'suspected_secret_emitted'];

function parseMode(raw: string | string[] | undefined): FailureMode | undefined {
  if (typeof raw !== 'string') return undefined;
  return (VALID_MODES as string[]).includes(raw) ? (raw as FailureMode) : undefined;
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

  const [hygiene, t, navT] = await Promise.all([
    fetchHygieneRows({ failureMode, departmentSlug: dept, page, pageSize: 25 }),
    getTranslations('hygiene'),
    getTranslations('nav'),
  ]);

  return (
    <main className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-text-1 text-lg font-semibold">{navT('hygiene')}</h1>
        <RefreshButton />
      </div>
      <FailureModeFilter />
      <HygieneTable rows={hygiene.rows} emptyDescription={t('empty')} />
      <SoftPoll intervalMs={30_000} />
    </main>
  );
}
