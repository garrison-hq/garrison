'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { createTicket } from '@/lib/actions/tickets';
import { ConflictError } from '@/lib/locks/conflict';

// TicketCreateForm — operator-driven ticket creation per FR-025 /
// FR-049. Wires the createTicket server action.
//
// The schema (M2.1 tickets) carries `objective` (~= title; required,
// short imperative) and `acceptanceCriteria` (~= description;
// optional, multi-line). FR-025's "title/description/priority/
// assigned-agent" naming is rendered onto the schema's actual
// columns. priority + assigned-agent fields are deferred until the
// schema gains those columns (post-M4).
//
// targetColumn defaults to 'todo' per FR-025.

export interface TicketCreateFormProps {
  /** List of (slug, name) pairs for the department dropdown.
   *  Resolved server-side at page load via lib/queries/
   *  orgOverview.ts:fetchDepartmentRows. */
  departments: ReadonlyArray<{ slug: string; name: string }>;
  /** Pre-selected department from the URL or referrer. Falls
   *  back to the first department when omitted. */
  initialDeptSlug?: string;
}

export function TicketCreateForm({
  departments,
  initialDeptSlug,
}: Readonly<TicketCreateFormProps>) {
  const router = useRouter();
  const [objective, setObjective] = useState('');
  const [acceptanceCriteria, setAcceptanceCriteria] = useState('');
  const [deptSlug, setDeptSlug] = useState(
    initialDeptSlug ?? departments[0]?.slug ?? '',
  );
  const [error, setError] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  function handleSubmit() {
    setError(null);
    if (objective.trim().length === 0) {
      setError('Objective cannot be empty.');
      return;
    }
    if (objective.length > 500) {
      setError('Objective must be ≤ 500 characters.');
      return;
    }
    if (deptSlug.length === 0) {
      setError('Pick a department.');
      return;
    }
    startTransition(async () => {
      try {
        const result = await createTicket({
          objective: objective.trim(),
          acceptanceCriteria: acceptanceCriteria.trim() || undefined,
          deptSlug,
        });
        router.push(`/tickets/${result.ticketId}`);
      } catch (err) {
        if (err instanceof ConflictError) {
          setError(`Create rejected: ${err.message}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  return (
    <div className="space-y-5">
      <FieldBlock label="objective" hint="One imperative sentence — what should the ticket accomplish?">
        <input
          type="text"
          value={objective}
          onChange={(e) => setObjective(e.target.value)}
          maxLength={500}
          className="w-full bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
        />
        <p className="text-[11px] text-text-3 text-right tabular-nums font-mono">
          {objective.length}/500
        </p>
      </FieldBlock>

      <FieldBlock label="acceptance criteria" hint="Optional. Multi-line; markdown supported.">
        <textarea
          value={acceptanceCriteria}
          onChange={(e) => setAcceptanceCriteria(e.target.value)}
          rows={8}
          className="w-full font-mono text-[12.5px] bg-surface-2 border border-border-1 rounded p-3 text-text-1 focus:outline-none focus:border-accent"
          spellCheck={false}
        />
      </FieldBlock>

      <FieldBlock label="department">
        <select
          value={deptSlug}
          onChange={(e) => setDeptSlug(e.target.value)}
          className="w-full bg-surface-2 border border-border-1 rounded px-3 py-2 text-[13px] text-text-1 focus:outline-none focus:border-accent"
        >
          {departments.map((d) => (
            <option key={d.slug} value={d.slug}>
              {d.name} ({d.slug})
            </option>
          ))}
        </select>
      </FieldBlock>

      {error && (
        <div className="rounded border border-err/40 bg-err/5 px-4 py-2.5 text-[12.5px] text-err">
          {error}
        </div>
      )}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={() => router.back()}
          className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
        >
          Cancel
        </button>
        <button
          type="button"
          disabled={pending}
          onClick={handleSubmit}
          className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {pending ? 'Creating…' : 'Create ticket'}
        </button>
      </div>
    </div>
  );
}

function FieldBlock({
  label,
  hint,
  children,
}: Readonly<{ label: string; hint?: string; children: React.ReactNode }>) {
  return (
    <div className="space-y-1.5">
      <label className="block text-[10.5px] uppercase tracking-wide text-text-3 font-mono">
        {label}
      </label>
      {children}
      {hint && <p className="text-[11px] text-text-3">{hint}</p>}
    </div>
  );
}
