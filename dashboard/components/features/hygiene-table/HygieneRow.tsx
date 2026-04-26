import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import type { HygieneRow as Row } from '@/lib/queries/hygiene';

function statusTone(failureMode: Row['failureMode']): 'warn' | 'err' {
  if (failureMode === 'suspected_secret_emitted') return 'err';
  return 'warn';
}

export function HygieneRowItem({ row }: { row: Row }) {
  const ts = new Date(row.at).toISOString().slice(0, 16) + 'Z';
  return (
    <Link
      href={`/tickets/${row.ticketId}`}
      className="grid grid-cols-12 gap-3 px-3 py-2 items-center hover:bg-surface-2 text-xs border-b border-border-1 last:border-b-0"
      data-testid="hygiene-row"
    >
      <span className="col-span-2 text-text-3 font-mono">{ts}</span>
      <span className="col-span-2 font-mono text-text-1">{row.departmentSlug}</span>
      <span className="col-span-2 font-mono text-text-2">
        {row.fromColumn ?? '—'} → {row.toColumn}
      </span>
      <span className="col-span-3">
        <Chip tone={statusTone(row.failureMode)}>{row.hygieneStatus}</Chip>
      </span>
      <span className="col-span-2 text-text-2 font-mono">
        {row.failureMode === 'suspected_secret_emitted' && row.patternCategory ? (
          <span data-testid="pattern-category">{row.patternCategory}</span>
        ) : row.exitReason ? (
          <span>{row.exitReason}</span>
        ) : (
          <span className="text-text-3">—</span>
        )}
      </span>
      <span className="col-span-1 text-right text-text-3 font-mono">
        {row.ticketId.slice(0, 8)}
      </span>
    </Link>
  );
}
