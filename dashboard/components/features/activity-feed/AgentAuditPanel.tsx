import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { EmptyState } from '@/components/ui/EmptyState';
import {
  listAuditByAgentInstance,
  type AgentAnchoredAuditRow,
} from '@/lib/queries/audit';

function outcomeTone(outcome: string): 'ok' | 'warn' | 'accent' | 'neutral' {
  if (outcome === 'success') return 'ok';
  if (outcome === 'validation_failed') return 'warn';
  if (outcome === 'dept_weekly_ticket_budget_exceeded') return 'warn';
  if (outcome === 'failed') return 'warn';
  return 'neutral';
}

function formatTimestamp(value: string): string {
  return new Date(value).toISOString().slice(0, 19).replace('T', ' ');
}

function ticketHref(row: AgentAnchoredAuditRow): string | null {
  if (!row.ticketId) return null;
  return `/tickets/${row.ticketId}`;
}

export async function AgentAuditPanel({ agentInstanceId }: Readonly<{ agentInstanceId: string }>) {
  const rows = await listAuditByAgentInstance(agentInstanceId);
  if (rows.length === 0) {
    return (
      <EmptyState
        description="No audit rows for this agent instance"
        caption="Either the agent has not committed any mutations yet, or the agent_instance_id is wrong."
      />
    );
  }
  const header = rows[0];
  return (
    <section className="space-y-3 border border-border-1 rounded p-4">
      <header className="space-y-1">
        <h2 className="text-text-1 text-sm font-semibold tracking-tight">
          Agent audit ({rows.length})
        </h2>
        <p className="text-text-3 text-xs">
          Agent instance{' '}
          <code className="text-text-2 font-mono">{agentInstanceId.slice(0, 8)}</code>
          {header.roleSlug && (
            <>
              {' '}· role <code className="text-text-2 font-mono">{header.roleSlug}</code>
            </>
          )}
          {header.ticketId && (
            <>
              {' '}·{' '}
              <Link href={`/tickets/${header.ticketId}`} className="hover:underline">
                originating ticket
              </Link>
            </>
          )}
        </p>
      </header>
      <table className="w-full text-sm border border-border-1 rounded" data-testid="agent-audit-table">
        <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
          <tr>
            <th className="text-left px-3 py-2">When</th>
            <th className="text-left px-3 py-2">Verb</th>
            <th className="text-left px-3 py-2">Outcome</th>
            <th className="text-left px-3 py-2">Resource</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const href = ticketHref(r);
            return (
              <tr key={r.id} className="border-t border-border-1">
                <td className="px-3 py-2 text-text-3 text-xs">{formatTimestamp(r.createdAt)}</td>
                <td className="px-3 py-2 text-text-1 font-mono text-xs">{r.verb}</td>
                <td className="px-3 py-2">
                  <Chip tone={outcomeTone(r.outcome)}>{r.outcome}</Chip>
                </td>
                <td className="px-3 py-2 text-text-2 text-xs font-mono">
                  {href ? (
                    <Link href={href} className="hover:underline">
                      {r.affectedResourceType ?? 'ticket'}
                    </Link>
                  ) : (
                    r.affectedResourceType ?? '—'
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
