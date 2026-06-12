'use client';

// M11 OutboxControls — client-side action buttons for approve / reject /
// mark-as-done. Calls the Server Actions in actions.ts and calls
// router.refresh() on success so the Server Component re-fetches the
// updated row list (the M9 TaskDetailControls router.refresh() pattern).
//
// Per AGENTS.md "Tests for Go only, never frontend": no vitest.

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { approveAction, rejectAction, markActionDone } from './actions';

type OutboxControlsMode = 'approve' | 'human_only';

export function OutboxControls({
  id,
  mode,
}: Readonly<{ id: string; mode: OutboxControlsMode }>) {
  const router = useRouter();
  const [pending, startTransition] = useTransition();
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState('');

  function run(fn: () => Promise<{ ok: boolean; message?: string }>) {
    setError(null);
    startTransition(async () => {
      const res = await fn();
      if (!res.ok) {
        setError((res as { ok: false; message: string }).message ?? 'unknown error');
        return;
      }
      router.refresh();
    });
  }

  if (mode === 'approve') {
    return (
      <div className="flex items-center gap-3 flex-wrap">
        <button
          type="button"
          disabled={pending}
          onClick={() => run(() => approveAction(id))}
          className="rounded bg-accent-1 px-4 py-1.5 text-[13px] font-medium text-surface-1 hover:opacity-90 disabled:opacity-50"
          data-testid="outbox-approve-button"
        >
          {pending ? 'Approving…' : 'Approve'}
        </button>
        <button
          type="button"
          disabled={pending}
          onClick={() => run(() => rejectAction(id))}
          className="rounded border border-border-1 px-4 py-1.5 text-[13px] font-medium text-text-1 hover:bg-surface-2 disabled:opacity-50"
          data-testid="outbox-reject-button"
        >
          {pending ? 'Rejecting…' : 'Reject'}
        </button>
        {error ? (
          <span className="text-err text-xs" role="alert" data-testid="outbox-action-error">
            {error}
          </span>
        ) : null}
      </div>
    );
  }

  // human_only — mark-as-done with optional note
  return (
    <div className="space-y-2">
      <div className="flex items-start gap-3 flex-wrap">
        <div className="flex-1 min-w-0 space-y-1.5">
          <label
            htmlFor={`outbox-note-${id}`}
            className="block text-text-3 text-[12px]"
          >
            Note (optional — what was actually done)
          </label>
          <textarea
            id={`outbox-note-${id}`}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
            className="w-full rounded border border-border-1 bg-surface-2 p-2 text-[13px] text-text-1"
            placeholder="describe what you performed…"
            disabled={pending}
            data-testid="outbox-mark-done-note"
          />
        </div>
        <button
          type="button"
          disabled={pending}
          onClick={() => run(() => markActionDone(id, note))}
          className="mt-6 rounded border border-border-1 px-4 py-1.5 text-[13px] font-medium text-text-1 hover:bg-surface-2 disabled:opacity-50"
          data-testid="outbox-mark-done-button"
        >
          {pending ? 'Marking done…' : 'Mark as done'}
        </button>
      </div>
      {error ? (
        <span className="text-err text-xs" role="alert" data-testid="outbox-action-error">
          {error}
        </span>
      ) : null}
    </div>
  );
}
