import type { ReactNode } from 'react';
import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import { columnTextClass } from '@/lib/format/columnTone';
import { formatIsoFull } from '@/lib/format/relativeTime';
import type { HygieneRow as Row } from '@/lib/queries/hygiene';

// Single hygiene-flag row.
//
// Tone mapping (operator-supplied design notes):
//   finalize_*  → warn (procedural smell, not breach)
//   sandbox_*   → err
//   suspected_* → err  (real risk)
//   anything else with a known prefix → warn
//
// Row is non-clickable as a whole; only the ticket cell is the
// link (mono, text-info, hover-underline). Hover on the row tints
// the surface so the click affordance for the ticket cell still
// reads as part of the row.

function flagTone(failureMode: Row['failureMode'], status: string): 'warn' | 'err' | 'info' {
  if (failureMode === 'sandbox_escape') return 'err';
  if (failureMode === 'suspected_secret_emitted') return 'err';
  if (failureMode === 'finalize_path') return 'warn';
  if (status.endsWith('_info')) return 'info';
  return 'warn';
}

export function HygieneRowItem({ row }: Readonly<{ row: Row }>) {
  const tsIso = new Date(row.at).toISOString().slice(0, 19) + 'Z';

  let detailCell: ReactNode;
  if (row.failureMode === 'suspected_secret_emitted' && row.patternCategory) {
    detailCell = (
      <span data-testid="pattern-category" className="text-text-2">
        {row.patternCategory}
      </span>
    );
  } else if (row.exitReason) {
    detailCell = <span className="text-text-2">{row.exitReason}</span>;
  } else {
    detailCell = <span className="text-text-3">—</span>;
  }

  return (
    <tr className="hover:bg-surface-2/60 transition-colors" data-testid="hygiene-row">
      <Td>
        <span
          className="font-mono font-tabular text-[12px] text-text-3"
          title={formatIsoFull(row.at)}
        >
          {tsIso}
        </span>
      </Td>
      <Td>
        <span className="text-[12.5px] text-text-2">{row.departmentSlug}</span>
      </Td>
      <Td>
        <span className="font-mono text-[12px] flex items-center gap-1.5">
          <span className={row.fromColumn ? columnTextClass(row.fromColumn) : 'text-text-3'}>
            {row.fromColumn ?? '—'}
          </span>
          <span className="text-text-4" aria-hidden>→</span>
          <span className={columnTextClass(row.toColumn)}>{row.toColumn}</span>
        </span>
      </Td>
      <Td>
        <Chip tone={flagTone(row.failureMode, row.hygieneStatus)}>
          {row.hygieneStatus}
        </Chip>
      </Td>
      <Td className="truncate max-w-0">
        <span className="text-[12.5px]">{detailCell}</span>
      </Td>
      <Td className="text-right">
        <Link
          href={`/tickets/${row.ticketId}`}
          className="font-mono text-[11.5px] text-info hover:underline"
        >
          {row.ticketId.slice(0, 8)}
        </Link>
      </Td>
    </tr>
  );
}

function Td({
  children,
  className = '',
}: Readonly<{ children: ReactNode; className?: string }>) {
  return <td className={`px-3 py-2.5 align-middle ${className}`}>{children}</td>;
}
