import { getTranslations } from 'next-intl/server';
import { Chip } from '@/components/ui/Chip';
import { CostCaveatIcon } from '@/components/ui/CostCaveatIcon';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import type { AgentInstanceRow } from '@/lib/queries/ticketDetail';

function formatCost(cost: number | null): string {
  if (cost === null) return '—';
  return `$${cost.toFixed(4)}`;
}

function formatDuration(start: Date, end: Date | null): string {
  if (!end) return '—';
  const ms = new Date(end).getTime() - new Date(start).getTime();
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  return `${min}m${sec % 60}s`;
}

export async function AgentInstancesBlock({ instances }: { instances: AgentInstanceRow[] }) {
  const t = await getTranslations('ticketDetail');
  return (
    <section className="space-y-2">
      <h3 className="text-text-2 text-xs uppercase tracking-wider">{t('agentInstances')}</h3>
      {instances.length === 0 ? (
        <p className="text-text-3 text-xs">{t('noInstances')}</p>
      ) : (
        <Tbl>
          <thead>
            <tr>
              <Th>{t('role')}</Th>
              <Th>{t('startedAt')}</Th>
              <Th>{t('duration')}</Th>
              <Th>{t('exitReason')}</Th>
              <Th>{t('cost')}</Th>
            </tr>
          </thead>
          <tbody>
            {instances.map((inst) => (
              <tr key={inst.id} data-testid="agent-instance-row">
                <Td>
                  <Chip tone="info">{inst.roleSlug}</Chip>
                </Td>
                <Td>
                  <span className="font-mono text-xs text-text-2">
                    {new Date(inst.startedAt).toISOString().slice(0, 16)}Z
                  </span>
                </Td>
                <Td>
                  <span className="font-mono text-xs text-text-2">
                    {formatDuration(inst.startedAt, inst.finishedAt)}
                  </span>
                </Td>
                <Td>
                  <span className="font-mono text-xs text-text-2">{inst.exitReason ?? '—'}</span>
                </Td>
                <Td>
                  <span className="inline-flex items-center gap-1">
                    <span className="font-mono text-xs">{formatCost(inst.totalCostUsd)}</span>
                    <CostCaveatIcon matched={inst.costBlindSpot} />
                  </span>
                </Td>
              </tr>
            ))}
          </tbody>
        </Tbl>
      )}
    </section>
  );
}
