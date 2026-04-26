import type { ReactNode } from 'react';

// KPI strip card — used by the org-overview surface. Renders a
// label, a primary value, and optional captioning.

export function KpiCard({
  label,
  value,
  caption,
}: Readonly<{
  label: ReactNode;
  value: ReactNode;
  caption?: ReactNode;
}>) {
  return (
    <div className="bg-surface-1 border border-border-1 rounded p-4 flex flex-col gap-1">
      <span className="text-text-3 text-[11px] uppercase tracking-wider">{label}</span>
      <span className="text-text-1 text-2xl font-semibold font-mono">{value}</span>
      {caption ? <span className="text-text-3 text-xs">{caption}</span> : null}
    </div>
  );
}
