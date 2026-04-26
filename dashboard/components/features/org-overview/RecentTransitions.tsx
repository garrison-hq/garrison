import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { formatTimeOfDay } from '@/lib/format/relativeTime';
import type { RecentTransitionRow } from '@/lib/queries/orgOverview';

// "Recent transitions" panel on the org overview. Last N
// ticket_transitions across all departments, newest first. Each
// row links into the ticket — clicking the row from the (app)
// shell triggers the @panel intercept, sliding in the ticket
// detail without leaving the overview.

function hygieneTone(status: string | null): 'warn' | 'err' | 'neutral' {
  if (!status || status === 'clean') return 'neutral';
  if (status === 'suspected_secret_emitted' || status === 'sandbox_escape') return 'err';
  return 'warn';
}

// Explicit column widths so timestamps and status arrows align
// across rows regardless of objective length. Two right-side
// columns for the transition text and the (optional) hygiene
// chip — splitting them keeps the transition arrow at the same
// x-position regardless of whether a chip is present.
//
//   time       80px      font-mono tabular
//   dept       96px      font-mono
//   objective  flex      text-1
//   transition 160px     font-mono right-aligned
//   hygiene    180px     chip slot (always reserved)
const ROW_TEMPLATE = '80px 96px minmax(0,1fr) 160px 180px';

export function RecentTransitions({
  rows,
}: Readonly<{ rows: RecentTransitionRow[] }>) {
  return (
    <section className="bg-surface-1 border border-border-1 rounded">
      <header className="px-4 py-3 border-b border-border-1 flex items-center gap-3">
        <h2 className="text-text-1 font-semibold text-base tracking-tight">
          Recent transitions
        </h2>
        <span className="text-text-3 text-xs font-tabular">
          last {rows.length} · across all depts
        </span>
        <Link
          href="/activity"
          className="ml-auto text-text-3 hover:text-text-1 text-xs"
        >
          view all →
        </Link>
      </header>
      {rows.length === 0 ? (
        <div className="p-8 text-text-3 text-xs text-center">
          no transitions yet
        </div>
      ) : (
        <ul className="divide-y divide-border-1">
          {rows.map((r) => (
            <li key={r.id}>
              <Link
                href={`/tickets/${r.ticketId}`}
                className="grid items-center gap-3 px-4 py-2.5 text-xs hover:bg-surface-2/60 transition-colors"
                style={{ gridTemplateColumns: ROW_TEMPLATE }}
              >
                <span className="font-mono font-tabular text-text-3">
                  {formatTimeOfDay(r.at)}
                </span>
                <span className="font-mono text-text-2 truncate">
                  {r.departmentSlug}
                </span>
                <span className="text-text-1 truncate">{r.ticketObjective}</span>
                <span className="font-mono text-text-3 flex items-center justify-end gap-1.5 truncate">
                  <span className="truncate">{r.fromColumn ?? '—'}</span>
                  <span className="text-text-4">→</span>
                  <span className="text-text-2 truncate">{r.toColumn}</span>
                </span>
                <span className="flex justify-end">
                  {r.hygieneStatus && r.hygieneStatus !== 'clean' ? (
                    <Chip tone={hygieneTone(r.hygieneStatus)}>
                      {r.hygieneStatus}
                    </Chip>
                  ) : null}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
