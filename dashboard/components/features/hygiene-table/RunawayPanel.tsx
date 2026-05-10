import { Chip } from '@/components/ui/Chip';
import { listDeptWeeklyState, type DeptWeeklyState } from '@/lib/queries/throttle';

function formatTimestamp(value: string | null): string {
  if (!value) return '—';
  return new Date(value).toISOString().slice(0, 19).replace('T', ' ');
}

function daysSince(value: string | null): string {
  if (!value) return '';
  const diffMs = Date.now() - new Date(value).getTime();
  const days = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (days <= 0) return ' (today)';
  return ` (${days}d ago)`;
}

function ratioTone(row: DeptWeeklyState): 'ok' | 'warn' | 'accent' | 'neutral' {
  if (row.weeklyTicketBudget == null) return 'neutral';
  if (row.weeklyTicketBudget === 0) return 'warn';
  const ratio = row.currentCount / row.weeklyTicketBudget;
  if (ratio >= 1) return 'warn';
  if (ratio >= 0.8) return 'accent';
  return 'ok';
}

function budgetLabel(row: DeptWeeklyState): string {
  if (row.weeklyTicketBudget == null) return 'unlimited';
  return `${row.currentCount} / ${row.weeklyTicketBudget}`;
}

export async function RunawayPanel() {
  const rows = await listDeptWeeklyState();
  if (rows.length === 0) {
    return null;
  }
  return (
    <section className="space-y-3 border border-border-1 rounded p-4" data-testid="runaway-panel">
      <header className="space-y-1">
        <h2 className="text-text-1 text-sm font-semibold tracking-tight">Runaway control</h2>
        <p className="text-text-3 text-xs">
          Per-department rolling-7d ticket count vs configured weekly budget. NULL budget =
          unlimited (M8 alpha default). The supervisor&apos;s <code>create_ticket</code> verb
          rejects with <code>dept_weekly_ticket_budget_exceeded</code> when count + 1 &gt; budget.
        </p>
      </header>
      <table className="w-full text-sm border border-border-1 rounded">
        <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
          <tr>
            <th className="text-left px-3 py-2">Department</th>
            <th className="text-left px-3 py-2">Count / budget</th>
            <th className="text-left px-3 py-2">Last fired</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.deptId} className="border-t border-border-1">
              <td className="px-3 py-2 text-text-1">{row.deptName}</td>
              <td className="px-3 py-2">
                <Chip tone={ratioTone(row)}>{budgetLabel(row)}</Chip>
              </td>
              <td className="px-3 py-2 text-text-3 text-xs">
                {formatTimestamp(row.lastFiredAt)}
                {daysSince(row.lastFiredAt)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
