import { getTranslations } from 'next-intl/server';
import { Chip } from '@/components/ui/Chip';
import { CostCaveatIcon } from '@/components/ui/CostCaveatIcon';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import { formatShortDateTime, formatIsoFull } from '@/lib/format/relativeTime';
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
  const s = sec % 60;
  return `${min}m ${s}s`;
}

export async function AgentInstancesBlock({
  instances,
}: Readonly<{ instances: AgentInstanceRow[] }>) {
  const t = await getTranslations('ticketDetail');
  return (
    <section className="space-y-2">
      <h3 className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
        {t('agentInstances')}
      </h3>
      {instances.length === 0 ? (
        <div className="rounded border border-border-1 bg-surface-1 px-3 py-3 text-text-3 text-xs">
          {t('noInstances')}
        </div>
      ) : (
        <Tbl>
          <thead>
            <tr>
              <Th>{t('role')}</Th>
              <Th>{t('startedAt')}</Th>
              <Th>{t('duration')}</Th>
              <Th>{t('exitReason')}</Th>
              <Th className="text-right">{t('cost')}</Th>
            </tr>
          </thead>
          <tbody>
            {instances.map((inst) => (
              <tr key={inst.id} data-testid="agent-instance-row">
                <Td>
                  <Chip tone="info">{inst.roleSlug}</Chip>
                </Td>
                <Td>
                  <span
                    className="font-mono text-[12px] text-text-2"
                    title={formatIsoFull(inst.startedAt)}
                  >
                    {formatShortDateTime(inst.startedAt)}
                  </span>
                </Td>
                <Td>
                  <span className="font-mono font-tabular text-[12px] text-text-2">
                    {formatDuration(inst.startedAt, inst.finishedAt)}
                  </span>
                </Td>
                <Td>
                  <span className="font-mono text-[12px] text-text-2">
                    {inst.exitReason ?? '—'}
                  </span>
                </Td>
                <Td className="text-right">
                  <span className="inline-flex items-center justify-end gap-1.5">
                    <span className="font-mono font-tabular text-[12px] text-text-1">
                      {formatCost(inst.totalCostUsd)}
                    </span>
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
