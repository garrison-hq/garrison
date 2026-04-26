import { getTranslations } from 'next-intl/server';
import { fetchOrgKPIs, fetchDepartmentRows } from '@/lib/queries/orgOverview';
import { KPIStrip } from '@/components/features/org-overview/KPIStrip';
import { DepartmentRow } from '@/components/features/org-overview/DepartmentRow';
import { RefreshButton } from '@/components/features/org-overview/RefreshButton';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';
import { EmptyState } from '@/components/ui/EmptyState';

// Org overview surface (FR-030 → FR-033).
//
// Server Component. Reads KPIs + per-department rows on every
// render; the SoftPoll client island calls router.refresh() every
// 60s so the data refreshes without losing the rest of the
// rendered shell. Manual refresh via the RefreshButton is the
// operator's escape hatch.

export const dynamic = 'force-dynamic';

export default async function Home() {
  const [kpis, departments, navT, ovT] = await Promise.all([
    fetchOrgKPIs(),
    fetchDepartmentRows(),
    getTranslations('nav'),
    getTranslations('orgOverview'),
  ]);

  return (
    <main className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-text-1 text-lg font-semibold">{navT('orgOverview')}</h1>
        <RefreshButton />
      </div>

      <KPIStrip kpis={kpis} />

      <section className="space-y-3">
        <div className="flex items-baseline gap-3">
          <h2 className="text-text-1 text-sm font-semibold">
            {ovT('departmentsHeading')}
          </h2>
          <span className="text-text-3 text-xs">
            {departments.length} configured
          </span>
        </div>
        {departments.length === 0 ? (
          <EmptyState description={ovT('noDepartments')} />
        ) : (
          <div className="grid gap-3 grid-cols-1 md:grid-cols-2 xl:grid-cols-3">
            {departments.map((row) => (
              <DepartmentRow key={row.slug} row={row} />
            ))}
          </div>
        )}
      </section>

      <SoftPoll intervalMs={60_000} />
    </main>
  );
}
