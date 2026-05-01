// @vitest-environment jsdom

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react';

const getRecentKGFactsMock = vi.fn();

vi.mock('@/lib/queries/knowsPane', () => ({
  getRecentKGFacts: () => getRecentKGFactsMock(),
}));

import { KGRecentFactsTab } from './KGRecentFactsTab';

const sampleTriple = (i: number, withTicket = false) => ({
  id: `t${i}`,
  subject: `subj-${i}`,
  predicate: 'created',
  object: `obj-${i}`,
  written_at: '2026-04-30T10:00:00Z',
  ...(withTicket ? { source_ticket_id: `ticket-${i}` } : {}),
});

describe('KGRecentFactsTab', () => {
  beforeEach(() => {
    getRecentKGFactsMock.mockReset();
  });
  afterEach(() => cleanup());

  it('TestKGRecentFactsTab_RendersList', async () => {
    getRecentKGFactsMock.mockResolvedValue({
      facts: [sampleTriple(1), sampleTriple(2, true)],
      error: null,
    });
    render(<KGRecentFactsTab />);
    await waitFor(() => {
      expect(screen.getAllByTestId('kg-fact-row')).toHaveLength(2);
    });
    // The second triple has a source_ticket deep-link.
    const link = screen.getByText('ticket') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('/tickets/ticket-2');
  });

  it('TestKGRecentFactsTab_RefreshButtonGreysList', async () => {
    getRecentKGFactsMock.mockResolvedValueOnce({
      facts: [sampleTriple(1)],
      error: null,
    });
    let resolveSecond: (v: unknown) => void = () => {};
    getRecentKGFactsMock.mockReturnValueOnce(
      new Promise((r) => {
        resolveSecond = r;
      }),
    );

    render(<KGRecentFactsTab />);
    await waitFor(() => {
      expect(screen.getByText('Refresh')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('kg-refresh'));
    await waitFor(() => {
      expect(screen.getByText('Refreshing…')).toBeTruthy();
    });
    expect(screen.getByRole('list').getAttribute('data-greyed')).toBe('true');
    resolveSecond({ facts: [sampleTriple(1), sampleTriple(2)], error: null });
    await waitFor(() => {
      expect(screen.getAllByTestId('kg-fact-row')).toHaveLength(2);
    });
  });

  it('TestKGRecentFactsTab_RefreshSuccessRestoresOpacity', async () => {
    getRecentKGFactsMock.mockResolvedValue({
      facts: [sampleTriple(1)],
      error: null,
    });
    render(<KGRecentFactsTab />);
    await waitFor(() => {
      expect(screen.getByRole('list').getAttribute('data-greyed')).toBe('false');
    });
  });

  it('TestKGRecentFactsTab_EmptyState', async () => {
    getRecentKGFactsMock.mockResolvedValue({ facts: [], error: null });
    render(<KGRecentFactsTab />);
    await waitFor(() => {
      expect(screen.getByText(/No KG facts yet/)).toBeTruthy();
    });
  });

  it('TestKGRecentFactsTab_UnreachableShowsTypedError', async () => {
    getRecentKGFactsMock.mockResolvedValue({
      facts: [],
      error: 'MempalaceUnreachable',
    });
    render(<KGRecentFactsTab />);
    await waitFor(() => {
      expect(screen.getByTestId('kg-error-block')).toBeTruthy();
    });
  });
});
