'use client';

import { useState, type ReactNode } from 'react';
import { useTranslations } from 'next-intl';

// A collapsible row used by ticket-detail history rows + activity-
// feed run groups. Renders the summary always, the detail when
// expanded. The expand toggle is keyboard-accessible.
//
// T011's sandbox-escape detail (claimed: X / on-disk: Y) and T016's
// activity-feed run grouping both reuse this primitive.

export function ExpandRow({
  summary,
  detail,
  defaultExpanded = false,
}: {
  summary: ReactNode;
  detail: ReactNode;
  defaultExpanded?: boolean;
}) {
  const t = useTranslations('common');
  const [open, setOpen] = useState(defaultExpanded);
  return (
    <div className="border border-border-1 rounded">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center justify-between gap-2 px-3 py-2 text-left bg-surface-1 hover:bg-surface-2"
        aria-expanded={open}
      >
        <span className="flex-1">{summary}</span>
        <span className="text-text-3 text-xs font-mono">
          {open ? t('collapse') : t('expand')}
        </span>
      </button>
      {open ? <div className="p-3 border-t border-border-1 bg-surface-2">{detail}</div> : null}
    </div>
  );
}
