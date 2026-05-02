'use client';

import { useTranslations } from 'next-intl';

// M6 — three-tab strip on /hygiene (plan §"Phase 9 — Dashboard").
//
// Sentence-case labels per the M5.4 polish round:
//   - "Agent failures" — failure-mode rows from the M2.x scanner
//     output (excludes operator drags + clean rows).
//   - "Operator audit"  — operator_initiated rows only.
//   - "All"            — every non-clean row.
//
// Active = `bg-accent/10 text-accent border-accent/30`; inactive =
// `bg-surface-1 text-text-3 border-border-1 hover:text-text-2
// hover:border-border-2`. Mirrors FailureModeFilter's chip shape
// byte-for-byte so the two segmented rows on /hygiene share an
// active-state vocabulary. Calls `onChange(tab)` on click. The
// parent owns the tab state; this component is presentational only.

export type HygieneTab = 'failures' | 'audit' | 'all';

const TABS: { tab: HygieneTab; labelKey: string }[] = [
  { tab: 'failures', labelKey: 'tabFailures' },
  { tab: 'audit', labelKey: 'tabAudit' },
  { tab: 'all', labelKey: 'tabAll' },
];

export function HygieneTabStrip({
  active,
  onChange,
}: Readonly<{
  active: HygieneTab;
  onChange: (tab: HygieneTab) => void;
}>) {
  const t = useTranslations('hygieneMeta');
  return (
    <div
      role="tablist"
      aria-label={t('tabsLabel')}
      className="flex gap-1.5 flex-wrap"
      data-testid="hygiene-tab-strip"
    >
      {TABS.map((m) => {
        const selected = active === m.tab;
        return (
          <button
            key={m.tab}
            type="button"
            role="tab"
            aria-selected={selected}
            onClick={() => onChange(m.tab)}
            data-testid={`hygiene-tab-${m.tab}`}
            className={`inline-flex items-center px-2.5 py-1 rounded text-[12px] border transition-colors ${
              selected
                ? 'bg-accent/10 text-accent border-accent/30'
                : 'bg-surface-1 text-text-3 border-border-1 hover:text-text-2 hover:border-border-2'
            }`}
          >
            {t(m.labelKey)}
          </button>
        );
      })}
    </div>
  );
}
