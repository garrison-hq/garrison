'use client';

import { useRouter, useSearchParams, usePathname } from 'next/navigation';
import { HygieneTabStrip, type HygieneTab } from './HygieneTabStrip';

// M6 — URL-driving wrapper around the presentational HygieneTabStrip.
//
// Reads the active tab from `?tab=`; on change, replaces the URL
// (clearing `page` so pagination resets when the row-shape changes).
// Mirrors the URL-driving pattern of FailureModeFilter so a reload
// or browser-back keeps the tab.

const VALID_TABS: HygieneTab[] = ['failures', 'audit', 'all'];

export function parseTab(raw: string | null): HygieneTab {
  if (raw && (VALID_TABS as string[]).includes(raw)) return raw as HygieneTab;
  return 'failures';
}

export function HygieneTabStripClient() {
  const router = useRouter();
  const pathname = usePathname();
  const search = useSearchParams();
  const active = parseTab(search.get('tab'));

  function onChange(tab: HygieneTab) {
    const params = new URLSearchParams(search.toString());
    if (tab === 'failures') {
      // Default — keep URL clean.
      params.delete('tab');
    } else {
      params.set('tab', tab);
    }
    params.delete('page');
    const qs = params.toString();
    router.push(qs ? `${pathname}?${qs}` : pathname);
  }

  return <HygieneTabStrip active={active} onChange={onChange} />;
}
