// M7 /admin/hires — operator review surface for hiring proposals
// per spec FR-104..FR-110a + plan §6 + threat-model hiring Rule 9.
// Lists every hiring_proposals row; clicking into a row opens the
// detail page where the operator approves / rejects.
//
// Per AGENTS.md "Tests for Go only, never frontend" memory rule: this
// surface ships without vitest coverage; the Go-side approve.go +
// integration tests cover the data shape.

import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { EmptyState } from '@/components/ui/EmptyState';
import { getProposalsForCurrentUser } from '@/lib/queries/hiring';

export const dynamic = 'force-dynamic';

function statusTone(status: string): 'neutral' | 'accent' | 'ok' | 'warn' {
  switch (status) {
    case 'approved':
    case 'installed':
      return 'ok';
    case 'rejected':
    case 'install_failed':
      return 'warn';
    case 'superseded':
      return 'neutral';
    case 'pending':
    case 'install_in_progress':
    default:
      return 'accent';
  }
}

function formatTimestamp(value: string | Date): string {
  const d = typeof value === 'string' ? new Date(value) : value;
  return d.toISOString().slice(0, 19).replace('T', ' ');
}

function proposalTypeLabel(t: string): string {
  switch (t) {
    case 'new_agent':
      return 'New agent';
    case 'skill_change':
      return 'Skill change';
    case 'version_bump':
      return 'Version bump';
    default:
      return t;
  }
}

export default async function AdminHiresPage() {
  const rows = await getProposalsForCurrentUser(200);

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1400px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Hiring proposals</h1>
        <p className="text-text-3 text-sm">
          Operator-side review surface for new agent hires, skill changes, and version bumps.
          Click into a proposal to review the diff + approve or reject.
        </p>
      </header>

      {rows.length === 0 ? (
        <EmptyState
          description="No hiring proposals yet"
          caption="The CEO can propose hires through chat (propose_hire / propose_skill_change / bump_skill_version). Proposals appear here as soon as the chat call commits."
        />
      ) : (
        <table
          className="w-full text-sm border border-border-1 rounded"
          data-testid="admin-hires-table"
        >
          <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
            <tr>
              <th className="text-left px-3 py-2">Type</th>
              <th className="text-left px-3 py-2">Target</th>
              <th className="text-left px-3 py-2">Department</th>
              <th className="text-left px-3 py-2">Origin</th>
              <th className="text-left px-3 py-2">Created</th>
              <th className="text-left px-3 py-2">Status</th>
              <th className="text-right px-3 py-2">Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr
                key={row.id}
                className="border-t border-border-1"
                data-testid="admin-hires-row"
              >
                <td className="px-3 py-2 text-text-2">{proposalTypeLabel(row.proposalType)}</td>
                <td className="px-3 py-2 text-text-1 font-mono text-[12px]">{row.roleTitle}</td>
                <td className="px-3 py-2 text-text-2 font-mono text-[12px]">{row.departmentSlug}</td>
                <td className="px-3 py-2 text-text-3 font-mono text-[12px]">{row.proposedVia}</td>
                <td className="px-3 py-2 text-text-3 font-mono text-[12px]">
                  {formatTimestamp(row.createdAt)}
                </td>
                <td className="px-3 py-2">
                  <Chip tone={statusTone(row.status)}>{row.status}</Chip>
                </td>
                <td className="px-3 py-2 text-right">
                  <Link
                    href={`/admin/hires/${row.id}`}
                    className="text-accent-1 hover:underline text-[12px]"
                  >
                    Review →
                  </Link>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
