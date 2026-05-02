// @vitest-environment jsdom

// Minimal data-binding test for RecentThreadsBlock — same shape as
// the M5.2 ThreadHistorySubnav test it replaces.

import { describe, it, expect, vi, afterEach } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';

vi.mock('next/navigation', () => ({
  usePathname: () => '/chat/22222222-2222-2222-2222-222222222222',
}));

import { RecentThreadsBlock } from './RecentThreadsBlock';

describe('RecentThreadsBlock', () => {
  afterEach(() => cleanup());

  it('renders the supplied threads with deep-links and marks the active row', () => {
    const threads = [
      { id: '11111111-1111-1111-1111-111111111111', threadNumber: 12, startedAt: new Date(Date.now() - 60_000).toISOString() },
      { id: '22222222-2222-2222-2222-222222222222', threadNumber: 11, startedAt: new Date(Date.now() - 3_600_000).toISOString() },
    ];
    render(<RecentThreadsBlock threads={threads} />);
    const rows = screen.getAllByTestId('thread-history-row');
    expect(rows).toHaveLength(2);
    expect(rows[0].getAttribute('href')).toBe('/chat/11111111-1111-1111-1111-111111111111');
    expect(rows[0].getAttribute('data-active')).toBe('false');
    expect(rows[1].getAttribute('href')).toBe('/chat/22222222-2222-2222-2222-222222222222');
    expect(rows[1].getAttribute('data-active')).toBe('true');
    expect(screen.getByText('view all threads →')).toBeTruthy();
  });

  it('renders the empty-state copy when no threads exist', () => {
    render(<RecentThreadsBlock threads={[]} />);
    expect(screen.getByText('No threads yet.')).toBeTruthy();
  });
});
