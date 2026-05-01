'use client';

// M5.4 — replaces M5.2's KnowsPanePlaceholder. Tab strip + per-tab
// content router for the three knowledge-base surfaces:
//
//   1. Company.md             — CEO-editable, MinIO-backed
//   2. Recent palace writes   — read-only, MemPalace drawer entries
//   3. KG recent facts        — read-only, MemPalace KG triples
//
// Tab state is local React useState (NOT URL-routed per FR-602) so
// switching threads doesn't reset the active tab and switching tabs
// doesn't dirty the URL. Inactive tabs stay mounted (display:none) so
// each tab's local state (e.g. CompanyMDTab edit buffer, palace
// loaded list) persists across switches.

import { useState } from 'react';
import { CompanyMDTab } from './CompanyMDTab';
import { RecentPalaceWritesTab } from './RecentPalaceWritesTab';
import { KGRecentFactsTab } from './KGRecentFactsTab';

type Tab = 'company' | 'palace' | 'kg';

const TABS: ReadonlyArray<{ id: Tab; label: string }> = [
  { id: 'company', label: 'Company.md' },
  { id: 'palace', label: 'Recent palace writes' },
  { id: 'kg', label: 'KG recent facts' },
];

export function KnowsPane() {
  const [active, setActive] = useState<Tab>('company');

  return (
    <aside
      className="border-l border-border-1 bg-surface-1 hidden lg:flex flex-col min-w-0"
      style={{ width: 360 }}
      aria-label="What the CEO knows"
      data-testid="knows-pane"
      data-active-tab={active}
    >
      <div
        role="tablist"
        aria-label="Knowledge-base sections"
        className="flex border-b border-border-1"
      >
        {TABS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={active === tab.id}
            data-testid={`knows-tab-${tab.id}`}
            className={
              'flex-1 px-3 py-2 text-[11.5px] uppercase tracking-[0.06em] font-medium border-b-2 transition-colors ' +
              (active === tab.id
                ? 'text-text-1 border-info'
                : 'text-text-3 border-transparent hover:text-text-2')
            }
            onClick={() => setActive(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {/* Inactive tabs stay mounted so per-tab state persists. */}
      <div className="flex-1 min-h-0 overflow-hidden">
        <div className={active === 'company' ? 'h-full' : 'hidden'}>
          <CompanyMDTab />
        </div>
        <div className={active === 'palace' ? 'h-full' : 'hidden'}>
          <RecentPalaceWritesTab />
        </div>
        <div className={active === 'kg' ? 'h-full' : 'hidden'}>
          <KGRecentFactsTab />
        </div>
      </div>
    </aside>
  );
}
