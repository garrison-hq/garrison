// M9 /admin/recurring-jobs — list + create surface for scheduled
// tasks (T015, plan §8). Lists every live (non-deleted) task with
// schedule / next fire / mode / state / last outcome and renders the
// CreateTaskForm at the top.
//
// force-dynamic + 60s SoftPoll per the task contract: the supervisor
// advances next_fire_at and appends runs server-side; the poll nudges
// the Server Component to re-run its queries. Width container is
// max-w-[1400px] mx-auto + w-full per the M6 width-collapse lesson.
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage; the Go-side T016/T017 integration suites pin the
// row shapes read here.

import { asc } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { departments, agents } from '@/drizzle/schema.supervisor';
import { EmptyState } from '@/components/ui/EmptyState';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';
import { CreateTaskForm, type DeptOption } from '@/components/features/scheduled-tasks/CreateTaskForm';
import { TaskRow } from '@/components/features/scheduled-tasks/TaskRow';
import { listScheduledTasks } from '@/lib/queries/scheduledTasks';

export const dynamic = 'force-dynamic';

async function fetchDepartmentOptions(): Promise<DeptOption[]> {
  return appDb
    .select({ id: departments.id, slug: departments.slug, name: departments.name })
    .from(departments)
    .orderBy(asc(departments.slug));
}

// Role-slug suggestions for the create form's datalist — free entry
// stays allowed; the supervisor-side validation owns role existence.
async function fetchRoleSlugSuggestions(): Promise<string[]> {
  const rows = await appDb
    .selectDistinct({ roleSlug: agents.roleSlug })
    .from(agents)
    .orderBy(asc(agents.roleSlug));
  return rows.map((r) => r.roleSlug);
}

export default async function RecurringJobsPage() {
  const [tasks, deptOptions, roleSlugSuggestions] = await Promise.all([
    listScheduledTasks(),
    fetchDepartmentOptions(),
    fetchRoleSlugSuggestions(),
  ]);

  return (
    <div className="px-6 py-5 space-y-6 max-w-[1400px] mx-auto w-full">
      <SoftPoll intervalMs={60_000} />
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Recurring jobs</h1>
        <p className="text-text-3 text-sm">
          Scheduled wake-ups fired by the supervisor&apos;s tick loop. <code>ticket</code> mode
          inserts a Kanban ticket; <code>oneshot</code> mode spawns an agent directly. Missed
          slots collapse to the single next future slot — no backfill.
        </p>
      </header>

      <CreateTaskForm departments={deptOptions} roleSlugSuggestions={roleSlugSuggestions} />

      {tasks.length === 0 ? (
        <EmptyState
          description="No recurring jobs yet"
          caption="Create one above, or ask the CEO chat to schedule recurring work."
        />
      ) : (
        <table
          className="w-full text-sm border border-border-1 rounded"
          data-testid="scheduled-tasks-table"
        >
          <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
            <tr>
              <th className="text-left px-3 py-2">Name</th>
              <th className="text-left px-3 py-2">Schedule</th>
              <th className="text-left px-3 py-2">Next fire</th>
              <th className="text-left px-3 py-2">Mode</th>
              <th className="text-left px-3 py-2">State</th>
              <th className="text-left px-3 py-2">Last outcome</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((task) => (
              <TaskRow key={task.id} task={task} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
