'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { editTicket } from '@/lib/actions/tickets';
import { ConflictError } from '@/lib/locks/conflict';

// TicketInlineEditor — inline edit surface for a ticket's
// objective + acceptanceCriteria (FR-031, scope-adapted to the
// M2.1 schema's actual columns; see lib/actions/tickets.ts
// header comment for the schema-vs-spec mapping).
//
// Last-write-wins per FR-034 — no optimistic locking. Two
// concurrent inline edits both succeed in chronological order;
// both audit rows are preserved in event_outbox so the divergence
// is reconstructible from the activity feed.
//
// Renders in two modes: 'view' (the default) shows the current
// values + an Edit button; 'edit' shows the form. Toggle is
// component-local state.

export interface TicketInlineEditorProps {
  ticketId: string;
  initialObjective: string;
  initialAcceptanceCriteria: string | null;
}

export function TicketInlineEditor({
  ticketId,
  initialObjective,
  initialAcceptanceCriteria,
}: Readonly<TicketInlineEditorProps>) {
  const router = useRouter();
  const [mode, setMode] = useState<'view' | 'edit'>('view');
  const [objective, setObjective] = useState(initialObjective);
  const [acceptanceCriteria, setAcceptanceCriteria] = useState(
    initialAcceptanceCriteria ?? '',
  );
  const [error, setError] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSave() {
    setError(null);
    if (objective.trim().length === 0) {
      setError('Objective cannot be empty.');
      return;
    }
    if (objective.length > 500) {
      setError('Objective must be ≤ 500 characters.');
      return;
    }
    const changes: Parameters<typeof editTicket>[0]['changes'] = {};
    if (objective.trim() !== initialObjective) {
      changes.objective = objective.trim();
    }
    if ((acceptanceCriteria || null) !== (initialAcceptanceCriteria || null)) {
      changes.acceptanceCriteria = acceptanceCriteria.trim().length > 0
        ? acceptanceCriteria.trim()
        : null;
    }
    if (Object.keys(changes).length === 0) {
      setMode('view');
      return;
    }
    startTransition(async () => {
      try {
        const result = await editTicket({ ticketId, changes });
        if (result.accepted) {
          setMode('view');
          router.refresh();
        } else {
          setError(`Save rejected: ${result.reason}`);
        }
      } catch (err) {
        if (err instanceof ConflictError) {
          setError(`Save rejected: ${err.message}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  function handleCancel() {
    setObjective(initialObjective);
    setAcceptanceCriteria(initialAcceptanceCriteria ?? '');
    setError(null);
    setMode('view');
  }

  if (mode === 'view') {
    return (
      <div className="flex justify-end">
        <button
          type="button"
          onClick={() => setMode('edit')}
          className="text-[12px] text-accent hover:underline"
        >
          Edit
        </button>
      </div>
    );
  }

  return (
    <div className="space-y-4 rounded border border-border-1 bg-surface-1 p-4">
      <div className="space-y-1.5">
        <label className="block text-[10.5px] uppercase tracking-wide text-text-3 font-mono">
          objective
        </label>
        <input
          type="text"
          value={objective}
          onChange={(e) => setObjective(e.target.value)}
          maxLength={500}
          className="w-full bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
        />
      </div>

      <div className="space-y-1.5">
        <label className="block text-[10.5px] uppercase tracking-wide text-text-3 font-mono">
          acceptance criteria
        </label>
        <textarea
          value={acceptanceCriteria}
          onChange={(e) => setAcceptanceCriteria(e.target.value)}
          rows={6}
          className="w-full font-mono text-[12.5px] bg-surface-2 border border-border-1 rounded p-3 text-text-1 focus:outline-none focus:border-accent"
          spellCheck={false}
        />
      </div>

      {error && (
        <div className="rounded border border-err/40 bg-err/5 px-3 py-2 text-[12.5px] text-err">
          {error}
        </div>
      )}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={handleCancel}
          className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
        >
          Cancel
        </button>
        <button
          type="button"
          disabled={pending}
          onClick={handleSave}
          className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {pending ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  );
}
