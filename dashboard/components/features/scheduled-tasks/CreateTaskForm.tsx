'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { createScheduledTask } from '@/lib/actions/scheduledTasks';

// M9 /admin/recurring-jobs — create form for scheduled tasks
// (T015, plan §8). Follows the M8 RegisterForm shape: uncontrolled
// inputs read through FormData, useTransition pending state, inline
// error rendering from the action's typed result (no throws).
//
// Grammar validation is NOT mirrored here (plan decision 10): the
// createScheduledTask action calls the supervisor's
// POST /schedule/validate, which is the single source of truth for
// the expression grammar + next-fire computation. The form only
// enforces presence.
//
// Per AGENTS.md "Tests for Go only, never frontend": no vitest.

export interface DeptOption {
  id: string;
  slug: string;
  name: string;
}

export function CreateTaskForm({
  departments,
  roleSlugSuggestions,
}: Readonly<{
  departments: ReadonlyArray<DeptOption>;
  roleSlugSuggestions: ReadonlyArray<string>;
}>) {
  const router = useRouter();
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<string | null>(null);

  function onSubmit(formData: FormData) {
    setError(null);
    setCreated(null);
    // FormData.get returns string | File | null; text inputs only
    // emit string entries — narrow explicitly (Sonar S6551).
    const readString = (key: string): string => {
      const raw = formData.get(key);
      return typeof raw === 'string' ? raw.trim() : '';
    };
    const name = readString('name');
    const departmentId = readString('departmentId');
    const roleSlug = readString('roleSlug');
    const mode = readString('mode');
    const scheduleExpr = readString('scheduleExpr');
    // Templates keep their raw value — leading/trailing whitespace
    // can be meaningful in rendered briefs; only emptiness is checked.
    const rawObjective = formData.get('objectiveTemplate');
    const rawAcceptance = formData.get('acceptanceCriteriaTemplate');
    const objectiveTemplate = typeof rawObjective === 'string' ? rawObjective : '';
    const acceptanceCriteriaTemplate = typeof rawAcceptance === 'string' ? rawAcceptance : '';

    startTransition(async () => {
      const res = await createScheduledTask({
        name,
        departmentId,
        roleSlug,
        mode,
        scheduleExpr,
        objectiveTemplate,
        acceptanceCriteriaTemplate,
      });
      if (!res.ok) {
        setError(res.message);
        return;
      }
      setCreated(name);
      router.refresh();
    });
  }

  return (
    <form
      action={onSubmit}
      className="space-y-3 border border-border-1 rounded p-4 max-w-2xl"
      data-testid="scheduled-task-create-form"
    >
      <h2 className="text-text-1 text-sm font-semibold tracking-tight">New recurring job</h2>
      <p className="text-text-3 text-xs">
        The supervisor&apos;s tick loop fires the task at each slot — <code>ticket</code> mode
        inserts a Kanban ticket through the normal flow; <code>oneshot</code> mode spawns an
        agent directly with the rendered brief. Templates may reference{' '}
        <code className="text-text-2">{'{{fire_at}}'}</code> and{' '}
        <code className="text-text-2">{'{{last_fired_at}}'}</code>.
      </p>

      <div className="grid grid-cols-2 gap-3">
        <label className="block">
          <span className="text-text-2 text-xs">Name</span>
          <input
            name="name"
            type="text"
            required
            placeholder="daily-standup"
            className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
          />
        </label>

        <label className="block">
          <span className="text-text-2 text-xs">Schedule (UTC)</span>
          <input
            name="scheduleExpr"
            type="text"
            required
            placeholder="daily@09:00"
            className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
          />
          <span className="mt-0.5 block text-[10.5px] text-text-4 font-mono">
            daily@HH:MM · weekly@{'{mon..sun}'}@HH:MM · every@N{'{m|h}'}
          </span>
        </label>

        <label className="block">
          <span className="text-text-2 text-xs">Department</span>
          <select
            name="departmentId"
            required
            defaultValue={departments[0]?.id ?? ''}
            className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm"
          >
            {departments.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name} ({d.slug})
              </option>
            ))}
          </select>
        </label>

        <label className="block">
          <span className="text-text-2 text-xs">Role slug</span>
          <input
            name="roleSlug"
            type="text"
            required
            list="scheduled-task-role-slugs"
            placeholder="engineer"
            className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
          />
          <datalist id="scheduled-task-role-slugs">
            {roleSlugSuggestions.map((slug) => (
              <option key={slug} value={slug} />
            ))}
          </datalist>
        </label>

        <label className="block">
          <span className="text-text-2 text-xs">Mode</span>
          <select
            name="mode"
            defaultValue="ticket"
            className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm"
          >
            <option value="ticket">ticket — fire into the Kanban flow</option>
            <option value="oneshot">oneshot — spawn an agent directly</option>
          </select>
        </label>
      </div>

      <label className="block">
        <span className="text-text-2 text-xs">Objective template</span>
        <textarea
          name="objectiveTemplate"
          required
          rows={2}
          placeholder="Run the daily standup sweep for {{fire_at}}."
          className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
        />
      </label>

      <label className="block">
        <span className="text-text-2 text-xs">Acceptance-criteria template</span>
        <textarea
          name="acceptanceCriteriaTemplate"
          required
          rows={2}
          placeholder="Summary covers everything since {{last_fired_at}}."
          className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
        />
      </label>

      {error && (
        <p className="text-warn text-xs" role="alert" data-testid="scheduled-task-create-error">
          {error}
        </p>
      )}
      {created && !error && (
        <p className="text-ok text-xs" data-testid="scheduled-task-create-success">
          Created <span className="font-mono">{created}</span>.
        </p>
      )}

      <button
        type="submit"
        disabled={pending}
        className="px-3 py-1 text-sm rounded border border-border-1 bg-surface-2 hover:bg-surface-3 disabled:opacity-50"
      >
        {pending ? 'Creating…' : 'Create job'}
      </button>
    </form>
  );
}
