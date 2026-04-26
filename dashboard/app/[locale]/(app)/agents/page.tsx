import { getTranslations } from 'next-intl/server';
import { fetchAgents } from '@/lib/queries/agents';
import { AgentsTable } from '@/components/features/agents-registry/AgentsTable';
import { EmptyState } from '@/components/ui/EmptyState';
import { RefreshButton } from '@/components/features/org-overview/RefreshButton';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';

export const dynamic = 'force-dynamic';

export default async function AgentsPage() {
  const [rows, navT, t] = await Promise.all([
    fetchAgents(),
    getTranslations('nav'),
    getTranslations('agents'),
  ]);
  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-text-1 text-lg font-semibold">{navT('agents')}</h1>
        <RefreshButton />
      </div>
      {rows.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <AgentsTable rows={rows} />
      )}
      <SoftPoll intervalMs={60_000} />
    </div>
  );
}
