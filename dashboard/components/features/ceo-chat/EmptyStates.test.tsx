// M5.2 — EmptyStates SSR pins (plan §1.15).

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import {
  NoThreadsEverEmptyState,
  EmptyCurrentThreadHint,
  EndedThreadEmptyState,
} from './EmptyStates';

function visible(html: string): string {
  return html.replace(/<!--\s*-->/g, '');
}

describe('EmptyStates', () => {
  it('TestNoThreadsEverEmptyStateRendersCTA', () => {
    const html = renderToString(<NoThreadsEverEmptyState onCreate={() => {}} />);
    const v = visible(html);
    expect(v).toContain('Start a thread with the CEO');
    expect(v).toContain('+ New thread');
    expect(v).toContain('empty-state-no-threads');
  });

  it('TestEndedThreadEmptyStateRendersCopy', () => {
    const html = renderToString(<EndedThreadEmptyState onCreateNew={() => {}} />);
    const v = visible(html);
    expect(v).toContain('This thread is ended');
    expect(v).toContain('Start a new thread');
    expect(v).toContain('empty-state-ended');
  });

  it('EmptyCurrentThreadHint reads "Ask the CEO anything"', () => {
    const html = renderToString(<EmptyCurrentThreadHint />);
    expect(visible(html)).toContain('Ask the CEO anything');
  });
});
