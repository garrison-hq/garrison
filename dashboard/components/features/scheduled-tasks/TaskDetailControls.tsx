'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import {
  editScheduledTask,
  pauseScheduledTask,
  resumeScheduledTask,
  deleteScheduledTask,
  type EditScheduledTaskInput,
} from '@/lib/actions/scheduledTasks';
import type { ScheduledTaskRow } from '@/lib/queries/scheduledTasks';

// M9 — edit / pause / resume / delete controls for one scheduled
// task (T015, plan §8). Every mutation goes through the T014 Server
// Actions (typed results, no throws); validation errors render
// inline per the task contract.
//
// Delete is a SOFT delete server-side (FR-502: run history + audit
// rows survive) but it is still Tier 3 — recurring cost-incurring
// state stops existing for the operator — so the confirm dialog uses
// the typed-name tier, matching the vault delete-secret precedent.
//
// Per AGENTS.md "Tests for Go only, never frontend": no vitest.

export function TaskDetailControls({ task }: Readonly<{ task: ScheduledTaskRow }>) {
  const router = useRouter();
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  function run(fn: () => Promise<{ ok: boolean; message?: string }>, after?: () => void) {
    setError(null);
    startTransition(async () => {
      const res = await fn();
      if (!res.ok) {
        setError(res.message ?? 'unknown error');
        return;
      }
      after?.();
      router.refresh();
    });
  }

  function onEditSubmit(formData: FormData) {
    const readString = (key: string): string => {
      const raw = formData.get(key);
      return typeof raw === 'string' ? raw : '';
    };
    // Only send fields that actually changed — the action audits a
    // per-field before/after diff, so an unchanged field should not
    // appear in args_jsonb.
    const patch: EditScheduledTaskInput = {};
    const name = readString('name').trim();
    if (name !== task.name) patch.name = name;
    const roleSlug = readString('roleSlug').trim();
    if (roleSlug !== task.roleSlug) patch.roleSlug = roleSlug;
    const scheduleExpr = readString('scheduleExpr').trim();
    if (scheduleExpr !== task.scheduleExpr) patch.scheduleExpr = scheduleExpr;
    const objectiveTemplate = readString('objectiveTemplate');
    if (objectiveTemplate !== task.objectiveTemplate) patch.objectiveTemplate = objectiveTemplate;
    const acceptanceCriteriaTemplate = readString('acceptanceCriteriaTemplate');
    if (acceptanceCriteriaTemplate !== task.acceptanceCriteriaTemplate) {
      patch.acceptanceCriteriaTemplate = acceptanceCriteriaTemplate;
    }
    if (Object.keys(patch).length === 0) {
      setEditing(false);
      return;
    }
    run(
      () => editScheduledTask(task.id, patch),
      () => setEditing(false),
    );
  }

  return (
    <div className="space-y-3" data-testid="scheduled-task-detail-controls">
      <div className="flex items-center gap-2">
        <button
          type="button"
          disabled={pending}
          onClick={() => {
            setError(null);
            setEditing((v) => !v);
          }}
          className="px-3 py-1 text-sm rounded border border-border-1 bg-surface-2 hover:bg-surface-3 disabled:opacity-50"
          data-testid="scheduled-task-edit-toggle"
        >
          {editing ? 'Cancel edit' : 'Edit'}
        </button>
        {task.paused ? (
          <button
            type="button"
            disabled={pending}
            onClick={() => run(() => resumeScheduledTask(task.id))}
            className="px-3 py-1 text-sm rounded border border-border-1 bg-surface-2 hover:bg-surface-3 disabled:opacity-50"
            data-testid="scheduled-task-resume"
          >
            Resume
          </button>
        ) : (
          <button
            type="button"
            disabled={pending}
            onClick={() => run(() => pauseScheduledTask(task.id))}
            className="px-3 py-1 text-sm rounded border border-border-1 bg-surface-2 hover:bg-surface-3 disabled:opacity-50"
            data-testid="scheduled-task-pause"
          >
            Pause
          </button>
        )}
        <button
          type="button"
          disabled={pending}
          onClick={() => setConfirmingDelete(true)}
          className="px-3 py-1 text-sm rounded border border-err/40 text-err bg-surface-2 hover:bg-err/10 disabled:opacity-50"
          data-testid="scheduled-task-delete"
        >
          Delete
        </button>
      </div>

      {error && (
        <p className="text-warn text-xs" role="alert" data-testid="scheduled-task-action-error">
          {error}
        </p>
      )}

      {editing && (
        <form
          action={onEditSubmit}
          className="space-y-3 border border-border-1 rounded p-4 max-w-2xl"
          data-testid="scheduled-task-edit-form"
        >
          <div className="grid grid-cols-2 gap-3">
            <label className="block">
              <span className="text-text-2 text-xs">Name</span>
              <input
                name="name"
                type="text"
                required
                defaultValue={task.name}
                className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
              />
            </label>
            <label className="block">
              <span className="text-text-2 text-xs">Schedule (UTC)</span>
              <input
                name="scheduleExpr"
                type="text"
                required
                defaultValue={task.scheduleExpr}
                className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
              />
              <span className="mt-0.5 block text-[10.5px] text-text-4 font-mono">
                daily@HH:MM · weekly@{'{mon..sun}'}@HH:MM · every@N{'{m|h}'}
              </span>
            </label>
            <label className="block">
              <span className="text-text-2 text-xs">Role slug</span>
              <input
                name="roleSlug"
                type="text"
                required
                defaultValue={task.roleSlug}
                className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
              />
            </label>
          </div>
          <label className="block">
            <span className="text-text-2 text-xs">Objective template</span>
            <textarea
              name="objectiveTemplate"
              required
              rows={2}
              defaultValue={task.objectiveTemplate}
              className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
            />
          </label>
          <label className="block">
            <span className="text-text-2 text-xs">Acceptance-criteria template</span>
            <textarea
              name="acceptanceCriteriaTemplate"
              required
              rows={2}
              defaultValue={task.acceptanceCriteriaTemplate}
              className="mt-1 w-full px-2 py-1 bg-surface-2 border border-border-1 rounded text-sm font-mono"
            />
          </label>
          <button
            type="submit"
            disabled={pending}
            className="px-3 py-1 text-sm rounded border border-border-1 bg-surface-2 hover:bg-surface-3 disabled:opacity-50"
          >
            {pending ? 'Saving…' : 'Save changes'}
          </button>
        </form>
      )}

      <ConfirmDialog
        open={confirmingDelete}
        tier="typed-name"
        title="Delete recurring job"
        body={
          <p>
            <span className="font-mono">{task.name}</span> stops firing immediately. Run history
            and audit rows survive (soft delete), and the name becomes reusable.
          </p>
        }
        confirmationName={task.name}
        confirmLabel="Delete"
        intent="destructive"
        onConfirm={() => {
          setConfirmingDelete(false);
          run(
            () => deleteScheduledTask(task.id),
            () => router.push('/admin/recurring-jobs'),
          );
        }}
        onCancel={() => setConfirmingDelete(false)}
      />
    </div>
  );
}
