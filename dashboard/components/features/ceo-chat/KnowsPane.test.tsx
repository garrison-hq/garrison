// @vitest-environment jsdom

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup, fireEvent } from '@testing-library/react';

// Mock the data-fetching child components since they each call their
// query module on mount; we only care about the tab-strip behaviour
// here.
vi.mock('@/lib/actions/companyMD', () => ({
  getCompanyMD: vi.fn().mockResolvedValue({ content: '', etag: null, error: null }),
  saveCompanyMD: vi.fn(),
}));
vi.mock('@/lib/queries/knowsPane', () => ({
  getRecentPalaceWrites: vi.fn().mockResolvedValue({ writes: [], error: null }),
  getRecentKGFacts: vi.fn().mockResolvedValue({ facts: [], error: null }),
}));

import { KnowsPane } from './KnowsPane';

describe('KnowsPane', () => {
  afterEach(() => cleanup());

  it('TestKnowsPane_RendersThreeTabs', () => {
    render(<KnowsPane />);
    expect(screen.getByTestId('knows-tab-company')).toBeTruthy();
    expect(screen.getByTestId('knows-tab-palace')).toBeTruthy();
    expect(screen.getByTestId('knows-tab-kg')).toBeTruthy();
    // Order check.
    const tabs = screen.getAllByRole('tab');
    expect(tabs[0].getAttribute('data-testid')).toBe('knows-tab-company');
    expect(tabs[1].getAttribute('data-testid')).toBe('knows-tab-palace');
    expect(tabs[2].getAttribute('data-testid')).toBe('knows-tab-kg');
  });

  it('TestKnowsPane_DefaultsToCompanyMDActive', () => {
    render(<KnowsPane />);
    const pane = screen.getByTestId('knows-pane');
    expect(pane.getAttribute('data-active-tab')).toBe('company');
    expect(screen.getByTestId('knows-tab-company').getAttribute('aria-selected')).toBe('true');
  });

  it('TestKnowsPane_SwitchesTabsOnClick', () => {
    render(<KnowsPane />);
    fireEvent.click(screen.getByTestId('knows-tab-palace'));
    const pane = screen.getByTestId('knows-pane');
    expect(pane.getAttribute('data-active-tab')).toBe('palace');
    expect(screen.getByTestId('knows-tab-palace').getAttribute('aria-selected')).toBe('true');
    expect(screen.getByTestId('knows-tab-company').getAttribute('aria-selected')).toBe('false');

    fireEvent.click(screen.getByTestId('knows-tab-kg'));
    expect(pane.getAttribute('data-active-tab')).toBe('kg');
  });
});
