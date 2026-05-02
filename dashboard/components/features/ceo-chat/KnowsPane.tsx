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
//
// The lower-right region is the recent-threads block (formerly the
// left-sidebar ThreadHistorySubnav at M5.2). ChatShell is the only
// caller; it fetches the seed list server-side and passes it here.

import { useState } from 'react';
import { CompanyMDTab } from './CompanyMDTab';
import { RecentPalaceWritesTab } from './RecentPalaceWritesTab';
import { KGRecentFactsTab } from './KGRecentFactsTab';
import { RecentThreadsBlock } from './RecentThreadsBlock';

type Tab = 'company' | 'palace' | 'kg';

// Sentence-case labels per M5.4 polish: the prior all-caps wall
// (`COMPANY.MD / RECENT PALACE WRITES / KG RECENT FACTS`) read as a
// banner row instead of a tab strip. Matches the screen-ceo reference.
const TABS: ReadonlyArray<{ id: Tab; label: string }> = [
  { id: 'company', label: 'Company.md' },
  { id: 'palace', label: 'Recent palace writes' },
  { id: 'kg', label: 'KG recent facts' },
];

interface ThreadRow {
  id: string;
  threadNumber: number;
  startedAt: string;
}

interface Props {
  threads: ThreadRow[];
}

export function KnowsPane({ threads }: Readonly<Props>) {
  const [active, setActive] = useState<Tab>('company');

  return (
    <aside
      className="border-l border-border-1 bg-surface-1 hidden lg:flex flex-col min-w-0"
      style={{ width: 360 }}
      aria-label="What the CEO knows"
      data-testid="knows-pane"
      data-active-tab={active}
    >
      {/* Header strip — labels the pane the way screen-overview labels
          its right rail. Matches the chat thread header's 40px-ish
          density so the right column doesn't look top-heavy. */}
      <header className="flex items-center justify-between border-b border-border-1 px-4 py-2.5">
        <h3 className="text-text-1 text-[12.5px] font-medium">What the CEO knows</h3>
        <span className="text-text-3 text-[10.5px] font-mono font-tabular">context</span>
      </header>
      <div
        role="tablist"
        aria-label="Knowledge-base sections"
        className="flex border-b border-border-1 px-3"
      >
        {TABS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={active === tab.id}
            data-testid={`knows-tab-${tab.id}`}
            className={
              'px-3 py-2 text-[12px] font-medium transition-colors -mb-px ' +
              (active === tab.id
                ? 'text-text-1 border-b-[1.5px] border-accent'
                : 'text-text-3 border-b-[1.5px] border-transparent hover:text-text-2')
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

      <RecentThreadsBlock threads={threads} />
    </aside>
  );
}
