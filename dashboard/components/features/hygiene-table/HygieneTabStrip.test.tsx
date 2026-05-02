// M6 — HygieneTabStrip presentational tests.
//
// Renders via renderToString (matches the M5.2 IdlePill / EventRow
// pattern); next-intl's useTranslations is stubbed to echo the
// label key so tests pin the structure (data-testid + aria-selected
// + role) without coupling to the production locale strings.

import { describe, it, expect, vi } from 'vitest';
import { renderToString } from 'react-dom/server';

vi.mock('next-intl', () => ({
  useTranslations: () => (key: string) => key,
}));

import { HygieneTabStrip, type HygieneTab } from './HygieneTabStrip';

function noop(_: HygieneTab) {
  // captured-tab assertion target; `_` silences unused-arg lint.
  void _;
}

describe('HygieneTabStrip', () => {
  it('TestHygieneTabStripRendersAllThreeTabs', () => {
    const html = renderToString(<HygieneTabStrip active="failures" onChange={noop} />);
    expect(html).toContain('data-testid="hygiene-tab-failures"');
    expect(html).toContain('data-testid="hygiene-tab-audit"');
    expect(html).toContain('data-testid="hygiene-tab-all"');
  });

  it('TestHygieneTabStripMarksActiveTabSelected', () => {
    const html = renderToString(<HygieneTabStrip active="audit" onChange={noop} />);
    // Each tab is a button with role="tab"; the active one carries
    // aria-selected="true".
    expect(html).toMatch(
      /data-testid="hygiene-tab-audit"[^>]*aria-selected="true"|aria-selected="true"[^>]*data-testid="hygiene-tab-audit"/,
    );
    // Inactive tab carries aria-selected="false".
    expect(html).toMatch(
      /data-testid="hygiene-tab-failures"[^>]*aria-selected="false"|aria-selected="false"[^>]*data-testid="hygiene-tab-failures"/,
    );
  });

  it('TestHygieneTabStripActiveTabHasAccentClass', () => {
    const html = renderToString(<HygieneTabStrip active="all" onChange={noop} />);
    // Active chip mirrors FailureModeFilter's shape:
    // bg-accent/10 + text-accent + border-accent/30. Loose match
    // tolerates Tailwind class-ordering changes.
    expect(html).toContain('bg-accent/10');
    expect(html).toContain('border-accent/30');
  });

  it('TestHygieneTabStripRendersAsTablist', () => {
    const html = renderToString(<HygieneTabStrip active="failures" onChange={noop} />);
    expect(html).toContain('role="tablist"');
  });

  it('TestHygieneTabStripExposesLabelViaTranslationKey', () => {
    // Stub useTranslations echoes the key, so the strip carries the
    // labelKey for each tab.
    const html = renderToString(<HygieneTabStrip active="failures" onChange={noop} />);
    expect(html).toContain('tabFailures');
    expect(html).toContain('tabAudit');
    expect(html).toContain('tabAll');
  });
});
