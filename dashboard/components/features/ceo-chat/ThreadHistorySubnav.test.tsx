// M5.2 — minimal data-binding test for ThreadHistorySubnav.
//
// The component renders a static <details>/<summary> tree from a
// supplied threads array. We don't bring up jsdom for this — a
// renderToString pass is sufficient to verify the data binding (the
// actual interactive behaviour is exercised by the Playwright
// sub-scenarios in T013–T015).

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { ThreadHistorySubnav } from './ThreadHistorySubnav';

describe('ThreadHistorySubnav', () => {
  it('TestThreadHistorySubnavRendersUserOwnedThreads', () => {
    const threads = [
      { id: '11111111-1111-1111-1111-111111111111', threadNumber: 12 },
      { id: '22222222-2222-2222-2222-222222222222', threadNumber: 11 },
    ];
    const html = renderToString(<ThreadHistorySubnav threads={threads} />);
    // React inserts comment nodes between literal text + dynamic
    // values; strip them before matching so the test asserts on the
    // visible-string shape rather than the SSR markup.
    const visible = html.replace(/<!--\s*-->/g, '');
    expect(visible).toContain('thread #12');
    expect(visible).toContain('thread #11');
    expect(visible).toContain('/chat/11111111-1111-1111-1111-111111111111');
    expect(visible).toContain('/chat/22222222-2222-2222-2222-222222222222');
    expect(visible).toContain('/chat/all');
  });

  it('renders the "no threads yet" copy on empty input', () => {
    const html = renderToString(<ThreadHistorySubnav threads={[]} />);
    expect(html).toContain('No threads yet');
  });
});
