// @vitest-environment jsdom

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react';

const getRecentPalaceWritesMock = vi.fn();

vi.mock('@/lib/queries/knowsPane', () => ({
  getRecentPalaceWrites: () => getRecentPalaceWritesMock(),
  // Re-export the types as no-op so the SUT compiles.
}));

import { RecentPalaceWritesTab } from './RecentPalaceWritesTab';

const sampleEntry = (i: number) => ({
  id: `d${i}`,
  drawer_name: `name-${i}`,
  room_name: 'hall_events',
  wing_name: `wing_${i}`,
  written_at: '2026-04-30T10:00:00Z',
  body_preview: `preview ${i}`,
});

describe('RecentPalaceWritesTab', () => {
  beforeEach(() => {
    getRecentPalaceWritesMock.mockReset();
  });
  afterEach(() => cleanup());

  it('TestRecentPalaceWritesTab_RendersList', async () => {
    getRecentPalaceWritesMock.mockResolvedValue({
      writes: [sampleEntry(1), sampleEntry(2), sampleEntry(3)],
      error: null,
    });
    render(<RecentPalaceWritesTab />);
    await waitFor(() => {
      expect(screen.getAllByTestId('palace-write-row')).toHaveLength(3);
    });
  });

  it('TestRecentPalaceWritesTab_RefreshButtonGreysList', async () => {
    getRecentPalaceWritesMock.mockResolvedValueOnce({
      writes: [sampleEntry(1)],
      error: null,
    });
    let resolveSecond: (v: unknown) => void = () => {};
    getRecentPalaceWritesMock.mockReturnValueOnce(
      new Promise((r) => {
        resolveSecond = r;
      }),
    );

    render(<RecentPalaceWritesTab />);
    await waitFor(() => {
      expect(screen.getByText('Refresh')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('palace-refresh'));
    await waitFor(() => {
      expect(screen.getByText('Refreshing…')).toBeTruthy();
    });
    const ul = screen.getByRole('list');
    expect(ul.getAttribute('data-greyed')).toBe('true');

    resolveSecond({ writes: [sampleEntry(1), sampleEntry(2)], error: null });
    await waitFor(() => {
      expect(screen.getAllByTestId('palace-write-row')).toHaveLength(2);
    });
  });

  it('TestRecentPalaceWritesTab_RefreshSuccessRestoresOpacity', async () => {
    getRecentPalaceWritesMock.mockResolvedValue({
      writes: [sampleEntry(1)],
      error: null,
    });
    render(<RecentPalaceWritesTab />);
    await waitFor(() => {
      expect(screen.getByRole('list').getAttribute('data-greyed')).toBe('false');
    });
  });

  it('TestRecentPalaceWritesTab_EmptyState', async () => {
    getRecentPalaceWritesMock.mockResolvedValue({ writes: [], error: null });
    render(<RecentPalaceWritesTab />);
    await waitFor(() => {
      expect(screen.getByText(/No palace writes yet/)).toBeTruthy();
    });
  });

  it('TestRecentPalaceWritesTab_UnreachableShowsTypedError', async () => {
    getRecentPalaceWritesMock.mockResolvedValue({
      writes: [],
      error: 'MempalaceUnreachable',
    });
    render(<RecentPalaceWritesTab />);
    await waitFor(() => {
      expect(screen.getByTestId('palace-error-block')).toBeTruthy();
    });
  });
});
