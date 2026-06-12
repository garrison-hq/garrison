import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import { OutcomeChip } from './RunHistoryTable';
import type { ScheduledTaskRow } from '@/lib/queries/scheduledTasks';

// M9 — one row of the /admin/recurring-jobs list (T015). Server
// component: a plain <tr> with the columns the task contract names —
// schedule / next fire / mode / state / last outcome — plus the name
// linking through to the detail page.
//
// Per AGENTS.md "Tests for Go only, never frontend": no vitest.

export function TaskRow({ task }: Readonly<{ task: ScheduledTaskRow }>) {
  return (
    <tr className="border-t border-border-1" data-testid={`scheduled-task-row-${task.id}`}>
      <td className="px-3 py-2">
        <Link
          href={`/admin/recurring-jobs/${task.id}`}
          className="font-mono text-text-1 hover:underline"
        >
          {task.name}
        </Link>
        <div className="text-text-3 text-[11px] font-mono">
          {task.departmentSlug} · {task.roleSlug}
        </div>
      </td>
      <td className="px-3 py-2 whitespace-nowrap font-mono text-xs text-text-2">
        {task.scheduleExpr}
      </td>
      <td className="px-3 py-2 whitespace-nowrap">
        {task.paused ? (
          <span className="text-text-4 text-xs">paused</span>
        ) : (
          <span
            className="font-mono font-tabular text-text-2 text-xs"
            title={formatIsoFull(task.nextFireAt)}
            suppressHydrationWarning
          >
            {relativeTime(task.nextFireAt)}
          </span>
        )}
      </td>
      <td className="px-3 py-2 whitespace-nowrap">
        <Chip tone={task.mode === 'oneshot' ? 'info' : 'neutral'}>{task.mode}</Chip>
      </td>
      <td className="px-3 py-2 whitespace-nowrap">
        {task.paused ? <Chip tone="warn">paused</Chip> : <Chip tone="ok">active</Chip>}
      </td>
      <td className="px-3 py-2 whitespace-nowrap">
        {task.lastOutcome ? (
          <OutcomeChip outcome={task.lastOutcome} />
        ) : (
          <span className="text-text-4 text-xs">never claimed</span>
        )}
      </td>
    </tr>
  );
}
