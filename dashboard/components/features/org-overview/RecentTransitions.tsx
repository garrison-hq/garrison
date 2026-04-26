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

export function RecentTransitions({
  rows,
}: Readonly<{ rows: RecentTransitionRow[] }>) {
  return (
    <section className="bg-surface-1 border border-border-1 rounded">
      <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3 text-xs">
        <span className="text-text-1 font-semibold text-sm">Recent transitions</span>
        <span className="text-text-3">
          last {rows.length} · across all depts
        </span>
        <Link
          href="/activity"
          className="ml-auto text-text-3 hover:text-text-1"
        >
          view all →
        </Link>
      </header>
      {rows.length === 0 ? (
        <div className="p-6 text-text-3 text-xs text-center">
          no transitions yet
        </div>
      ) : (
        <ul className="divide-y divide-border-1">
          {rows.map((r) => (
            <li key={r.id}>
              <Link
                href={`/tickets/${r.ticketId}`}
                className="grid grid-cols-12 gap-3 items-center px-4 py-2 text-xs hover:bg-surface-2"
              >
                <span className="col-span-2 font-mono text-text-3">
                  {formatTimeOfDay(r.at)}
                </span>
                <span className="col-span-2 font-mono text-text-2">
                  {r.departmentSlug}
                </span>
                <span className="col-span-5 text-text-1 truncate">
                  {r.ticketObjective}
                </span>
                <span className="col-span-3 font-mono text-text-3 truncate">
                  {r.fromColumn ?? '—'}{' '}
                  <span className="text-text-4">→</span> {r.toColumn}
                  {r.hygieneStatus && r.hygieneStatus !== 'clean' ? (
                    <span className="ml-2 inline-block">
                      <Chip tone={hygieneTone(r.hygieneStatus)}>
                        {r.hygieneStatus}
                      </Chip>
                    </span>
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
