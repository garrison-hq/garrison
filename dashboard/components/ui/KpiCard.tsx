import type { ReactNode } from 'react';

// KPI strip card on the org-overview surface. Label sits above
// the value; value is the loudest typographic element on the
// page (~40px, semibold, tabular figures so a 6 and a 16 don't
// dance on column resize). Card padding is intentionally tight
// — the card height is set by the value's leading-none, not by
// an arbitrary inner gap.

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
    <div className="bg-surface-1 border border-border-1 rounded px-4 py-3 flex flex-col gap-2">
      <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
        {label}
      </span>
      <span className="text-text-1 text-[40px] leading-none font-mono font-semibold font-tabular">
        {value}
      </span>
      {caption ? (
        <span className="text-text-3 text-xs leading-tight">{caption}</span>
      ) : null}
    </div>
  );
}
