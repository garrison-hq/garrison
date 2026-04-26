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
    slug.replaceAll('_', ' ').replaceAll(/\b\w/g, (c) => c.toUpperCase())
  );
}

function statusTone(row: DeptRow): 'ok' | 'warn' | 'err' | 'neutral' {
  if (row.hygieneWarnings >= 3) return 'err';
  if (row.hygieneWarnings >= 1) return 'warn';
  if (row.liveInstances > 0) return 'ok';
  return 'neutral';
}

// Distribution-bar fill per workflow column. Earlier-stage columns
// (todo / backlog / ideas) read as muted text-3, mid columns as
// text-2, and the terminal column (done / ship / closed) as the
// brand accent — the operator's eye lands on "what's done" without
// the strip taking over the card.
function columnTrack(slug: string): string {
  if (slug === 'todo' || slug === 'backlog' || slug === 'ideas' || slug === 'questions' || slug === 'new') {
    return 'bg-text-4';
  }
  if (slug === 'done' || slug === 'ship' || slug === 'closed' || slug === 'written') {
    return 'bg-accent/70';
  }
  return 'bg-text-3/70';
}

export function DepartmentRow({ row }: Readonly<{ row: DeptRow }>) {
  const utilization =
    row.agentCap > 0 ? Math.min(1, row.liveInstances / row.agentCap) : 0;
  const counts = Object.entries(row.ticketCounts);
  const total = counts.reduce((acc, [, n]) => acc + n, 0);

  return (
    <Link
      href={`/departments/${row.slug}`}
      className="block bg-surface-1 border border-border-1 rounded-md px-4 py-4 hover:border-border-2 hover:bg-surface-2/40 transition-colors"
    >
      <div className="flex items-center gap-2 min-w-0">
        <StatusDot tone={statusTone(row)} pulse={row.liveInstances > 0} />
        <span className="text-text-1 font-semibold text-sm truncate">
          {row.name}
        </span>
        <span className="text-text-4">·</span>
        <span className="text-text-3 text-xs font-mono truncate">
          {row.slug}
        </span>
        <span className="ml-auto text-text-3 text-xs font-mono font-tabular shrink-0">
          {relativeTime(row.lastTransitionAt)}
        </span>
      </div>

      <div className="mt-3.5 flex items-center gap-3">
        <div className="text-text-1 text-sm font-mono font-tabular whitespace-nowrap">
          {row.liveInstances} / {row.agentCap}{' '}
          <span className="text-text-3 text-[11px]">active</span>
        </div>
        <div
          className="flex-1 h-[3px] rounded-full bg-surface-3 overflow-hidden"
          aria-label={`${Math.round(utilization * 100)}% utilization`}
        >
          <div
            className="h-full bg-text-2/70"
            style={{ width: `${utilization * 100}%` }}
          />
        </div>
      </div>

      {counts.length > 0 ? (
        <div className="mt-4 text-text-2 text-xs flex flex-wrap items-center gap-x-2.5 gap-y-1">
          {counts.map(([col, n], i) => (
            <span key={col} className="flex items-center gap-1.5">
              {i > 0 ? <span className="text-text-4">·</span> : null}
              <span className="text-text-3">{labelFor(col)}</span>
              <span className="text-text-1 font-mono font-tabular">{n}</span>
            </span>
          ))}
        </div>
      ) : (
        <div className="mt-4 text-text-3 text-xs">no open tickets</div>
      )}

      {total > 0 ? (
        <div className="mt-3 flex h-[3px] rounded-full bg-surface-3 overflow-hidden">
          {counts.map(([col, n]) => (
            <div
              key={col}
              className={`h-full ${columnTrack(col)}`}
              style={{ width: `${(n / total) * 100}%` }}
              title={`${labelFor(col)}: ${n}`}
            />
          ))}
        </div>
      ) : null}
    </Link>
  );
}
