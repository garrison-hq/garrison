import { getTranslations } from 'next-intl/server';
import { StatusDot } from '@/components/ui/StatusDot';
import type { AgentRegistryStats } from '@/lib/queries/agents';

// 4-tile KPI strip on /agents. Same shape as ActivityKPIStrip and
// the org-overview KpiCard row: status dot + uppercase microcopy
// label + tabular-mono value, separated by a thin vertical rule.
//
// Concurrency tile carries a sub-label "<pct>% of cap"; saturation
// flips the dot to warn at >= 80% of cap.

type Tone = 'ok' | 'info' | 'warn' | 'err' | 'neutral';

export async function AgentsKPIStrip({
  stats,
}: Readonly<{ stats: AgentRegistryStats }>) {
  const t = await getTranslations('agentsMeta.kpi');
  const concurrencyPct =
    stats.totalCap > 0
      ? Math.round((stats.totalLive / stats.totalCap) * 100)
      : 0;
  const concurrencyTone: Tone =
    concurrencyPct >= 90 ? 'err' : concurrencyPct >= 80 ? 'warn' : 'ok';

  return (
    <div className="bg-surface-1 border border-border-1 rounded grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 divide-y lg:divide-y-0 lg:divide-x divide-border-1">
      <Tile
        label={t('agents')}
        value={stats.totalAgents}
        sub={`${stats.liveAgents} ${t('liveAgents')}`}
        tone={stats.liveAgents > 0 ? 'ok' : 'neutral'}
      />
      <Tile
        label={t('concurrency')}
        value={`${stats.totalLive} / ${stats.totalCap}`}
        sub={`${concurrencyPct}${t('pctOfCap')}`}
        tone={concurrencyTone}
      />
      <Tile
        label={t('spawns24h')}
        value={stats.spawns24h}
        sub={t('rolling24h')}
        tone={stats.spawns24h > 0 ? 'info' : 'neutral'}
      />
      <Tile
        label={t('idleAgents')}
        value={stats.idleAgents}
        sub={t('zeroLiveInstances')}
        tone="neutral"
      />
    </div>
  );
}

function Tile({
  label,
  value,
  sub,
  tone,
}: Readonly<{
  label: string;
  value: number | string;
  sub: string;
  tone: Tone;
}>) {
  return (
    <div className="flex-1 px-4 py-3 flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5">
        <StatusDot tone={tone} />
        <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
          {label}
        </span>
      </div>
      <span className="text-text-1 text-[28px] leading-none font-mono font-semibold font-tabular">
        {value}
      </span>
      <span className="text-text-3 text-[11px]">{sub}</span>
    </div>
  );
}
