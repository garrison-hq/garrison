'use client';

import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import type { DeptRow } from '@/lib/queries/orgOverview';

export function DepartmentRow({ row }: Readonly<{ row: DeptRow }>) {
  const lastTs = row.lastTransitionAt
    ? new Date(row.lastTransitionAt).toISOString().slice(0, 16) + 'Z'
    : '—';
  return (
    <Link
      href={`/departments/${row.slug}`}
      className="grid grid-cols-12 gap-3 items-center px-3 py-2 hover:bg-surface-2 text-sm"
    >
      <span className="col-span-3 font-mono text-text-1">{row.slug}</span>
      <span className="col-span-4 text-text-2 text-xs flex flex-wrap gap-1">
        {Object.entries(row.ticketCounts).map(([col, count]) => (
          <Chip key={col} tone="neutral">
            {col}: {count}
          </Chip>
        ))}
      </span>
      <span className="col-span-2 text-text-2 text-xs font-mono">
        {row.agentCount}/{row.agentCap}
      </span>
      <span className="col-span-2 text-text-3 text-xs font-mono">{lastTs}</span>
      <span className="col-span-1 text-right">
        {row.hygieneWarnings > 0 ? (
          <Chip tone="warn">{row.hygieneWarnings}</Chip>
        ) : (
          <span className="text-text-3 text-xs">—</span>
        )}
      </span>
    </Link>
  );
}
