import { getTranslations } from 'next-intl/server';
import { fetchAgents, fetchAgentRegistryStats } from '@/lib/queries/agents';
import { AgentsTable } from '@/components/features/agents-registry/AgentsTable';
import { AgentsKPIStrip } from '@/components/features/agents-registry/AgentsKPIStrip';
import { EmptyState } from '@/components/ui/EmptyState';
import { RefreshButton } from '@/components/features/org-overview/RefreshButton';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';

export const dynamic = 'force-dynamic';

export default async function AgentsPage() {
  const [rows, stats, navT, t, meta] = await Promise.all([
    fetchAgents(),
    fetchAgentRegistryStats(),
    getTranslations('nav'),
    getTranslations('agents'),
    getTranslations('agentsMeta'),
  ]);

  const saturationPct =
    stats.totalCap > 0
      ? Math.round((stats.totalLive / stats.totalCap) * 100)
      : 0;

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto">
      <header className="space-y-1">
        <div className="flex items-center justify-between">
          <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
            {navT('agents')}
          </h1>
          <RefreshButton />
        </div>
        <p className="text-text-3 text-xs">
          <span className="font-mono font-tabular">{stats.totalAgents}</span>{' '}
          {meta('subtitleAgents')}
          {' · '}
          <span className="font-mono font-tabular">{stats.liveAgents}</span>{' '}
          {meta('subtitleLive')}
          {' · '}
          {meta('subtitleSaturation')}{' '}
          <span className="font-mono font-tabular">{saturationPct}%</span>
        </p>
      </header>
      <AgentsKPIStrip stats={stats} />
      {rows.length === 0 ? (
        <section className="bg-surface-1 border border-border-1 rounded">
          <EmptyState description={t('empty')} />
        </section>
      ) : (
        <AgentsTable rows={rows} />
      )}
      <SoftPoll intervalMs={60_000} />
    </div>
  );
}
