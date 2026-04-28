'use client';

import type { ReactNode } from 'react';

// DiffView — side-by-side before/after view of a field-level
// diff. Used by:
//
//  - Secret edit form (T007): renders a confirmation diff
//    before commit. For value diffs, `redactValues=true`
//    replaces both sides with [redacted] so the diff shows
//    "value will change" without revealing either side.
//  - Conflict resolution modal (ConflictResolutionModal):
//    renders the operator's draft vs. the server's latest
//    version side-by-side so the operator can decide
//    overwrite / merge / discard.
//
// Diff payload shape matches lib/audit/diff.ts:buildFieldDiff
// output.

export interface DiffViewProps {
  diff: Record<string, { before: unknown; after: unknown }>;
  /** When true, every value is replaced with [redacted]. Use
   *  this for value-bearing fields the operator should not see
   *  in the dialog (vault secret values per Rule 6 / FR-018). */
  redactValues?: boolean;
  /** Optional formatter for individual values; defaults to
   *  JSON-stringify or string-coerce. */
  formatValue?: (value: unknown) => ReactNode;
  /** Optional set of field names that should be redacted even
   *  if redactValues=false. */
  redactFields?: ReadonlySet<string>;
}

const REDACTED = '[redacted]';

function defaultFormat(value: unknown): ReactNode {
  if (value === null) return <span className="text-text-4 italic">null</span>;
  if (value === undefined) return <span className="text-text-4 italic">unset</span>;
  if (typeof value === 'string') return <span className="font-mono text-[12px]">{value}</span>;
  return <span className="font-mono text-[12px]">{JSON.stringify(value)}</span>;
}

export function DiffView({
  diff,
  redactValues = false,
  formatValue = defaultFormat,
  redactFields,
}: Readonly<DiffViewProps>) {
  const entries = Object.entries(diff);
  if (entries.length === 0) {
    return <p className="text-[12px] text-text-3 italic">no changes</p>;
  }
  return (
    <table className="w-full text-[12px]">
      <thead>
        <tr className="text-left text-[10.5px] uppercase tracking-wide text-text-3 border-b border-border-1">
          <th className="py-1 pr-3 font-normal w-1/4">Field</th>
          <th className="py-1 pr-3 font-normal w-3/8">Before</th>
          <th className="py-1 font-normal w-3/8">After</th>
        </tr>
      </thead>
      <tbody>
        {entries.map(([field, { before, after }]) => {
          const fieldRedacted =
            redactValues || (redactFields?.has(field) ?? false);
          return (
            <tr key={field} className="border-b border-border-1/50">
              <td className="py-1.5 pr-3 align-top text-text-2">{field}</td>
              <td className="py-1.5 pr-3 align-top text-text-3">
                {fieldRedacted ? <span className="text-text-4 italic">{REDACTED}</span> : formatValue(before)}
              </td>
              <td className="py-1.5 align-top text-text-1">
                {fieldRedacted ? <span className="text-text-4 italic">{REDACTED}</span> : formatValue(after)}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
