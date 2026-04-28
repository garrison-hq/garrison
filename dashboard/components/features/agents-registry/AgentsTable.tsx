import { getTranslations } from 'next-intl/server';
import { Chip } from '@/components/ui/Chip';
import { StatusDot } from '@/components/ui/StatusDot';
import { Sparkline } from '@/components/ui/Sparkline';
import { ModelChip } from './ModelChip';
import { ConcurrencyBar } from './ConcurrencyBar';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { AgentRow } from '@/lib/queries/agents';

// Department-grouped agents registry. One outer card; each
// department renders as its own block (header row + table) so a
// 10-plus row registry doesn't read as one undifferentiated list.
//
// Per-row shape:
//   agent          — status dot (pulsing if live) + role-slug
//                    + skills strip below
//   role           — neutral chip (identifier, not status)
//   model          — tier-coded chip via ModelChip
//   concurrency    — live / cap + horizontal bar via ConcurrencyBar
//   listens for    — chip group, one per kanban column
//   last spawn     — live indicator OR relative time
//   spawns / 7d    — inline sparkline + count, right-aligned

interface DeptGroup {
  slug: string;
  name: string;
  rows: AgentRow[];
  liveAgents: number;
  totalLive: number;
  totalCap: number;
}

function groupByDept(rows: AgentRow[]): DeptGroup[] {
  const groups = new Map<string, DeptGroup>();
  for (const r of rows) {
    let g = groups.get(r.departmentSlug);
    if (!g) {
      g = {
        slug: r.departmentSlug,
        name: r.departmentName,
        rows: [],
        liveAgents: 0,
        totalLive: 0,
        totalCap: r.concurrencyCap,
      };
      groups.set(r.departmentSlug, g);
    }
    g.rows.push(r);
    if (r.liveInstances > 0) g.liveAgents += 1;
    g.totalLive += r.liveInstances;
  }
  return Array.from(groups.values());
}

export async function AgentsTable({ rows }: Readonly<{ rows: AgentRow[] }>) {
  const t = await getTranslations('agentsMeta.headers');
  const meta = await getTranslations('agentsMeta');
  const groups = groupByDept(rows);

  return (
    <section className="bg-surface-1 border border-border-1 rounded overflow-hidden">
      {groups.map((g, i) => (
        <div key={g.slug}>
          <header
            className={`flex items-center gap-3 px-4 py-2.5 ${
              i > 0 ? 'border-t border-border-1' : ''
            } bg-surface-2/50`}
          >
            <span className="text-text-1 text-sm font-semibold tracking-tight">
              {g.name}
            </span>
            <span className="text-text-3 text-[11px] font-mono">{g.slug}</span>
            <span className="ml-auto text-text-3 text-[11px] font-mono font-tabular">
              <span className="text-text-1">{g.rows.length}</span>{' '}
              {meta('subtitleAgents')}
              {' · '}
              <span className="text-text-1">{g.totalLive}</span> / {g.totalCap}{' '}
              {meta('subtitleLive')}
            </span>
          </header>
          {g.rows.length === 0 ? (
            <div className="px-4 py-6 text-text-3 text-[12.5px]">
              {meta('noAgentsInDept')}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full">
                <colgroup>
                  <col style={{ width: 220 }} />
                  <col style={{ width: 140 }} />
                  <col style={{ width: 170 }} />
                  <col style={{ width: 140 }} />
                  <col />
                  <col style={{ width: 110 }} />
                  <col style={{ width: 140 }} />
                  <col style={{ width: 60 }} />
                </colgroup>
                <thead>
                  <tr className="bg-surface-2 border-b border-border-1">
                    <Th>{t('agent')}</Th>
                    <Th>{t('role')}</Th>
                    <Th>{t('model')}</Th>
                    <Th>{t('concurrency')}</Th>
                    <Th>{t('listensFor')}</Th>
                    <Th>{t('lastSpawn')}</Th>
                    <Th align="right">{t('spawns7d')}</Th>
                    <Th align="right">{' '}</Th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border-1">
                  {g.rows.map((r) => (
                    <AgentRowItem
                      key={r.id}
                      row={r}
                      runningLabel={meta('running')}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      ))}
    </section>
  );
}

function Th({
  children,
  align = 'left',
}: Readonly<{ children: React.ReactNode; align?: 'left' | 'right' }>) {
  return (
    <th
      className={`px-3 py-2 text-text-3 font-medium text-[10.5px] uppercase tracking-[0.08em] ${
        align === 'right' ? 'text-right' : 'text-left'
      }`}
    >
      {children}
    </th>
  );
}

function AgentRowItem({
  row,
  runningLabel,
}: Readonly<{ row: AgentRow; runningLabel: string }>) {
  const live = row.liveInstances > 0;
  return (
    <tr className="hover:bg-surface-2/50 transition-colors" data-testid="agent-row">
      <Td>
        <div className="flex items-center gap-2">
          <StatusDot tone={live ? 'ok' : 'neutral'} pulse={live} />
          <span className="font-mono text-[12.5px] text-text-1 truncate">
            {row.roleSlug}
          </span>
        </div>
        <div className="mt-1.5">
          <SkillStrip skills={row.skills} />
        </div>
      </Td>
      <Td>
        <Chip tone="neutral">{row.roleSlug}</Chip>
      </Td>
      <Td>
        <ModelChip model={row.model} />
      </Td>
      <Td>
        <ConcurrencyBar live={row.liveInstances} cap={row.concurrencyCap} />
      </Td>
      <Td>
        {row.listensFor.length === 0 ? (
          <span className="text-text-4 text-xs">—</span>
        ) : (
          <div className="flex gap-1 flex-wrap">
            {row.listensFor.map((c) => (
              <Chip key={c} tone="neutral">
                {c}
              </Chip>
            ))}
          </div>
        )}
      </Td>
      <Td>
        {live ? (
          <span className="inline-flex items-center gap-1.5 text-[11.5px] font-mono text-ok">
            <StatusDot tone="ok" pulse />
            {runningLabel}
          </span>
        ) : (
          <span
            className="font-mono font-tabular text-[11.5px] text-text-3"
            title={
              row.lastSpawnedAt ? formatIsoFull(row.lastSpawnedAt) : undefined
            }
          >
            {row.lastSpawnedAt ? relativeTime(row.lastSpawnedAt) : '—'}
          </span>
        )}
      </Td>
      <Td className="text-right">
        <span className="inline-flex items-center justify-end gap-2">
          <Sparkline values={row.spawnsByDay} tone="accent" />
          <span className="font-mono font-tabular text-[12px] text-text-1">
            {row.spawnsThisWeek}
          </span>
        </span>
      </Td>
      <Td className="text-right">
        <a
          href={`/agents/${row.departmentSlug}/${row.roleSlug}/edit`}
          className="text-[12px] text-accent hover:underline"
        >
          Edit
        </a>
      </Td>
    </tr>
  );
}

function Td({
  children,
  className = '',
}: Readonly<{ children: React.ReactNode; className?: string }>) {
  return <td className={`px-3 py-2.5 align-middle ${className}`}>{children}</td>;
}

// Up to 3 skill chips + a "+N" overflow indicator. The overflow
// chip carries the full list as a title attribute so the operator
// can hover to see what's hidden.
function SkillStrip({ skills }: Readonly<{ skills: string[] }>) {
  if (skills.length === 0) return null;
  const visible = skills.slice(0, 3);
  const rest = skills.length - visible.length;
  return (
    <span className="inline-flex flex-wrap gap-1 items-center">
      {visible.map((s) => (
        <span
          key={s}
          className="inline-flex items-center px-1.5 py-px rounded text-[10.5px] font-mono text-text-3 border border-border-1"
        >
          {s}
        </span>
      ))}
      {rest > 0 ? (
        <span
          className="inline-flex items-center px-1.5 py-px rounded text-[10.5px] font-mono text-text-3 border border-border-1"
          title={skills.slice(3).join(', ')}
        >
          +{rest}
        </span>
      ) : null}
    </span>
  );
}
