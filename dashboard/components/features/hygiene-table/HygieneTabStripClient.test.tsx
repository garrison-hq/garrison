// M6 — HygieneTabStripClient parseTab tests.
//
// The wrapper itself drives `useRouter`/`useSearchParams` and is
// exercised end-to-end by the M6.x Playwright suite (when it lands).
// The pure URL-shape parsing helper that decides which tab to
// activate from `?tab=` is exported and tested here.

import { describe, it, expect } from 'vitest';
import { parseTab } from './HygieneTabStripClient';

describe('parseTab', () => {
  it('TestParseTabFailures', () => {
    expect(parseTab('failures')).toBe('failures');
  });
  it('TestParseTabAudit', () => {
    expect(parseTab('audit')).toBe('audit');
  });
  it('TestParseTabAll', () => {
    expect(parseTab('all')).toBe('all');
  });
  it('TestParseTabNullDefaultsToFailures', () => {
    expect(parseTab(null)).toBe('failures');
  });
  it('TestParseTabUnknownDefaultsToFailures', () => {
    expect(parseTab('exotic')).toBe('failures');
    expect(parseTab('')).toBe('failures');
  });
});
