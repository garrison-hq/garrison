import Link from 'next/link';
import { StatusDot } from '@/components/ui/StatusDot';
import { relativeTime } from '@/lib/format/relativeTime';
import type { DeptRow } from '@/lib/queries/orgOverview';

// Department card on the org-overview surface. Mirrors the layout
// from .workspace/m3-mocks/garrison-reference/screen-overview.jsx
// (and the operator-supplied screenshot): name + slug + age,
// "X / Y active" with a capacity bar, per-column counts on a
// tight horizontal list, and a thin distribution sliver showing
// how the open work splits across columns.

const COLUMN_LABELS: Record<string, string> = {
  todo: 'Todo',
  in_dev: 'In dev',
  in_review: 'In review',
  qa: 'QA',
  done: 'Done',
  backlog: 'Backlog',
  drafting: 'Drafting',
  review: 'Review',
  ship: 'Ship',
  ideas: 'Ideas',
  running: 'Running',
  analyzing: 'Analyzing',
};

function labelFor(slug: string): string {
  return (
    COLUMN_LABELS[slug] ??
    slug.replaceAll('_', ' ').replace(/\b\w/g, (c) => c.toUpperCase())
  );
}

function statusTone(row: DeptRow): 'ok' | 'warn' | 'err' | 'neutral' {
  if (row.hygieneWarnings >= 3) return 'err';
  if (row.hygieneWarnings >= 1) return 'warn';
  if (row.liveInstances > 0) return 'ok';
  return 'neutral';
}

export function DepartmentRow({ row }: Readonly<{ row: DeptRow }>) {
  const utilization =
    row.agentCap > 0 ? Math.min(1, row.liveInstances / row.agentCap) : 0;
  const counts = Object.entries(row.ticketCounts);
  const total = counts.reduce((acc, [, n]) => acc + n, 0);

  return (
    <Link
      href={`/departments/${row.slug}`}
      className="block bg-surface-1 border border-border-1 rounded p-4 hover:border-border-2 transition-colors"
    >
      <div className="flex items-center gap-2">
        <StatusDot tone={statusTone(row)} pulse={row.liveInstances > 0} />
        <span className="text-text-1 font-semibold text-sm">{row.name}</span>
        <span className="text-text-3 text-xs">·</span>
        <span className="text-text-3 text-xs font-mono">{row.slug}</span>
        <span className="ml-auto text-text-3 text-xs font-mono">
          {relativeTime(row.lastTransitionAt)}
        </span>
      </div>

      <div className="mt-3 flex items-center gap-3">
        <div className="text-text-1 text-sm font-mono whitespace-nowrap">
          {row.liveInstances} / {row.agentCap}{' '}
          <span className="text-text-3 text-xs">active</span>
        </div>
        <div
          className="flex-1 h-1 rounded-full bg-surface-3 overflow-hidden"
          aria-label={`${Math.round(utilization * 100)}% utilization`}
        >
          <div
            className="h-full bg-ok"
            style={{ width: `${utilization * 100}%` }}
          />
        </div>
      </div>

      {counts.length > 0 ? (
        <div className="mt-3 text-text-2 text-xs flex flex-wrap items-center gap-x-2 gap-y-1">
          {counts.map(([col, n], i) => (
            <span key={col} className="flex items-center gap-1">
              {i > 0 ? <span className="text-text-4">·</span> : null}
              <span className="text-text-3">{labelFor(col)}</span>
              <span className="text-text-1 font-mono">{n}</span>
            </span>
          ))}
        </div>
      ) : (
        <div className="mt-3 text-text-3 text-xs">no open tickets</div>
      )}

      {total > 0 ? (
        <div className="mt-2 flex h-1 rounded-full bg-surface-3 overflow-hidden">
          {counts.map(([col, n], i) => (
            <div
              key={col}
              className={
                i % 2 === 0
                  ? 'h-full bg-ok'
                  : 'h-full bg-info'
              }
              style={{ width: `${(n / total) * 100}%` }}
              title={`${labelFor(col)}: ${n}`}
            />
          ))}
        </div>
      ) : null}
    </Link>
  );
}
