// M9 /admin/recurring-jobs/[id] — detail view for one scheduled task
// (T015, plan §8): edit / pause / resume / delete controls + the run
// history table with outcome chips. Soft-deleted tasks 404 here (the
// list query filters them too); their run history remains readable
// through the audit surfaces (FR-502).
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage.

import Link from 'next/link';
import { notFound } from 'next/navigation';
import { Chip } from '@/components/ui/Chip';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';
import { TaskDetailControls } from '@/components/features/scheduled-tasks/TaskDetailControls';
import { RunHistoryTable } from '@/components/features/scheduled-tasks/RunHistoryTable';
import { getScheduledTaskById, getTaskRunHistory } from '@/lib/queries/scheduledTasks';
import { formatIsoFull } from '@/lib/format/relativeTime';

export const dynamic = 'force-dynamic';

export default async function RecurringJobDetailPage({
  params,
}: Readonly<{ params: Promise<{ id: string }> }>) {
  const { id } = await params;
  const task = await getScheduledTaskById(id);
  if (!task) {
    notFound();
  }
  const runs = await getTaskRunHistory(task.id);

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1100px] mx-auto w-full">
      <SoftPoll intervalMs={60_000} />
      <nav className="text-text-3 text-xs">
        <Link href="/admin/recurring-jobs" className="hover:underline">
          ← Recurring jobs
        </Link>
      </nav>

      <header className="space-y-2">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight font-mono">
          {task.name}
        </h1>
        <div className="flex items-center gap-2 text-sm">
          <Chip tone={task.mode === 'oneshot' ? 'info' : 'neutral'}>{task.mode}</Chip>
          {task.paused ? <Chip tone="warn">paused</Chip> : <Chip tone="ok">active</Chip>}
          <Chip tone="neutral">{task.scheduleExpr}</Chip>
        </div>
      </header>

      <dl className="grid grid-cols-[220px_1fr] gap-y-2 text-sm">
        <dt className="text-text-3">Department / role</dt>
        <dd className="text-text-1 font-mono">
          {task.departmentSlug} / {task.roleSlug}
        </dd>

        <dt className="text-text-3">Next fire (UTC)</dt>
        <dd className="text-text-1 font-mono">
          {task.paused ? '— (paused)' : formatIsoFull(task.nextFireAt)}
        </dd>

        <dt className="text-text-3">Last fired</dt>
        <dd className="text-text-1 font-mono">
          {task.lastFiredAt ? formatIsoFull(task.lastFiredAt) : 'never'}
        </dd>

        <dt className="text-text-3">Objective template</dt>
        <dd className="text-text-1 whitespace-pre-wrap">{task.objectiveTemplate}</dd>

        <dt className="text-text-3">Acceptance-criteria template</dt>
        <dd className="text-text-1 whitespace-pre-wrap">{task.acceptanceCriteriaTemplate}</dd>

        <dt className="text-text-3">Created</dt>
        <dd className="text-text-1">{formatIsoFull(task.createdAt)}</dd>
      </dl>

      <TaskDetailControls task={task} />

      <section className="space-y-2">
        <h2 className="text-text-2 text-sm font-semibold tracking-tight">
          Run history ({runs.length})
        </h2>
        <RunHistoryTable runs={runs} />
      </section>
    </div>
  );
}
