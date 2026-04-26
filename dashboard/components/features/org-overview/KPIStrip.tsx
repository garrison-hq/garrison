'use client';

import { useTranslations } from 'next-intl';
import { KpiCard } from '@/components/ui/KpiCard';
import type { OrgKPIs } from '@/lib/queries/orgOverview';

export function KPIStrip({ kpis }: Readonly<{ kpis: OrgKPIs }>) {
  const t = useTranslations('orgOverview');
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
      <KpiCard label={t('openTickets')} value={kpis.openTickets} />
      <KpiCard label={t('activeAgents')} value={kpis.activeAgents} />
      <KpiCard label={t('transitions24h')} value={kpis.transitions24h} />
      <KpiCard label={t('hygieneWarnings')} value={kpis.hygieneWarnings} />
    </div>
  );
}
