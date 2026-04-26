import Link from 'next/link';
import { StatusDot } from '@/components/ui/StatusDot';
import { formatElapsed } from '@/lib/format/relativeTime';
import type { LiveSpawnRow } from '@/lib/queries/orgOverview';

// "Live spawns" card on the org overview. Currently-running
// agent_instances with their dept, role, ticket id, and elapsed
// time since started_at. Header right-aligns the live count and
// total cap so the operator can see "7 / 14 cap" at a glance.

export function LiveSpawns({
  rows,
  totalCapacity,
}: Readonly<{ rows: LiveSpawnRow[]; totalCapacity: number }>) {
  return (
    <section className="bg-surface-1 border border-border-1 rounded">
      <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-2 text-xs">
        <StatusDot tone="ok" pulse={rows.length > 0} />
        <span className="text-text-1 font-semibold text-sm">Live spawns</span>
        <span className="ml-auto text-text-3 font-mono">
          {rows.length} / {totalCapacity} cap
        </span>
      </header>
      {rows.length === 0 ? (
        <div className="p-6 text-text-3 text-xs text-center">
          no agents running
        </div>
      ) : (
        <ul className="divide-y divide-border-1">
          {rows.map((r) => (
            <li key={r.id}>
              {r.ticketId ? (
                <Link
                  href={`/tickets/${r.ticketId}`}
                  className="grid grid-cols-12 gap-2 items-center px-4 py-2 text-xs hover:bg-surface-2"
                >
                  <SpawnRow row={r} />
                </Link>
              ) : (
                <div className="grid grid-cols-12 gap-2 items-center px-4 py-2 text-xs">
                  <SpawnRow row={r} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function SpawnRow({ row }: Readonly<{ row: LiveSpawnRow }>) {
  return (
    <>
      <span className="col-span-1 flex items-center">
        <StatusDot tone="ok" />
      </span>
      <span className="col-span-3 font-mono text-text-2">
        {row.departmentSlug}
      </span>
      <span className="col-span-4 font-mono text-text-1 truncate">
        {row.roleSlug}
      </span>
      <span className="col-span-2 font-mono text-text-3 truncate">
        {row.ticketIdShort ?? '—'}
      </span>
      <span className="col-span-2 font-mono text-text-3 text-right">
        {formatElapsed(row.startedAt)}
      </span>
    </>
  );
}
