// M5.3 stopgap hiring proposals page (FR-490 .. FR-494). Read-only
// table of hiring_proposals rows; M7 extends with review/approve/spawn
// flow. Without this stopgap, garrison-mutate.propose_hire would write
// to a black hole until M7 ships (Open Q 9 default lean — operator
// needs visibility before the full hiring flow lands).

import { EmptyState } from '@/components/ui/EmptyState';
import { Chip } from '@/components/ui/Chip';
import { getProposalsForCurrentUser } from '@/lib/queries/hiring';

export const dynamic = 'force-dynamic';

function statusTone(status: string): 'neutral' | 'accent' | 'ok' | 'warn' {
  switch (status) {
    case 'approved':
      return 'ok';
    case 'rejected':
      return 'warn';
    case 'superseded':
      return 'neutral';
    case 'pending':
    default:
      return 'accent';
  }
}

function formatTimestamp(value: string | Date): string {
  const d = typeof value === 'string' ? new Date(value) : value;
  return d.toISOString().slice(0, 19).replace('T', ' ');
}

export default async function HiringProposalsPage() {
  const rows = await getProposalsForCurrentUser();

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1400px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Hiring proposals</h1>
        <p className="text-text-3 text-sm">
          Hiring requests originated by the CEO via chat. Approve / reject affordances
          arrive in M7; this surface is read-only for now.
        </p>
      </header>

      {rows.length === 0 ? (
        <EmptyState
          description="No hiring proposals yet"
          caption="The CEO can propose hires through chat (propose_hire verb). Proposals appear here as soon as the chat call commits."
        />
      ) : (
        <table
          className="w-full text-sm border border-border-1 rounded"
          data-testid="hiring-proposals-table"
        >
          <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
            <tr>
              <th className="text-left px-3 py-2">Role</th>
              <th className="text-left px-3 py-2">Department</th>
              <th className="text-left px-3 py-2">Origin</th>
              <th className="text-left px-3 py-2">Created</th>
              <th className="text-left px-3 py-2">Status</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className="border-t border-border-1" data-testid="hiring-proposal-row">
                <td className="px-3 py-2 text-text-1">{row.roleTitle}</td>
                <td className="px-3 py-2 text-text-2 font-mono text-[12px]">{row.departmentSlug}</td>
                <td className="px-3 py-2 text-text-3 font-mono text-[12px]">{row.proposedVia}</td>
                <td className="px-3 py-2 text-text-3 font-mono text-[12px]">{formatTimestamp(row.createdAt)}</td>
                <td className="px-3 py-2">
                  <Chip tone={statusTone(row.status)}>{row.status}</Chip>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
