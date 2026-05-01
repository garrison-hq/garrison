// Minimal data-binding test for RecentThreadsBlock — same shape as
// the M5.2 ThreadHistorySubnav test it replaces. renderToString is
// sufficient: the component is static markup, no interaction.

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { RecentThreadsBlock } from './RecentThreadsBlock';

describe('RecentThreadsBlock', () => {
  it('renders the supplied threads with deep-links', () => {
    const threads = [
      { id: '11111111-1111-1111-1111-111111111111', threadNumber: 12 },
      { id: '22222222-2222-2222-2222-222222222222', threadNumber: 11 },
    ];
    const html = renderToString(<RecentThreadsBlock threads={threads} />);
    const visible = html.replace(/<!--\s*-->/g, '');
    expect(visible).toContain('thread #12');
    expect(visible).toContain('thread #11');
    expect(visible).toContain('/chat/11111111-1111-1111-1111-111111111111');
    expect(visible).toContain('/chat/22222222-2222-2222-2222-222222222222');
    expect(visible).toContain('/chat/all');
  });

  it('renders the "no threads yet" copy on empty input', () => {
    const html = renderToString(<RecentThreadsBlock threads={[]} />);
    expect(html).toContain('No threads yet');
  });
});
