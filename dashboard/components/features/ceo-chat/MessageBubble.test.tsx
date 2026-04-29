// M5.2 — MessageBubble static rendering pins.

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { MessageBubble } from './MessageBubble';

function visible(html: string): string {
  return html.replace(/<!--\s*-->/g, '');
}

describe('MessageBubble', () => {
  it('renders an operator bubble right-aligned without cost footer', () => {
    const html = renderToString(
      <MessageBubble role="operator" status="completed" content="hello CEO" />,
    );
    const v = visible(html);
    expect(v).toContain('hello CEO');
    expect(v).toContain('justify-end');
    expect(v).not.toContain('chat-message-cost');
  });

  it('TestMessageStreamShowsCursorWhileStreaming', () => {
    const html = renderToString(
      <MessageBubble role="assistant" status="streaming" content="loading" streaming />,
    );
    const v = visible(html);
    expect(v).toContain('garrison-cursor');
    expect(v).toContain('aria-busy');
    expect(v).toContain('aria-live="polite"');
  });

  it('TestMessageStreamHidesCursorAfterTerminal', () => {
    const html = renderToString(
      <MessageBubble
        role="assistant"
        status="completed"
        content="final"
        streaming={false}
        costUsd={0.0042}
      />,
    );
    const v = visible(html);
    expect(v).not.toContain('garrison-cursor');
    expect(v).toContain('$0.0042'); // 4-decimal per-message cost
  });

  it('renders the model badge cosmetically', () => {
    const html = renderToString(
      <MessageBubble
        role="assistant"
        status="completed"
        content="x"
        modelBadge="claude-sonnet-4-6"
      />,
    );
    expect(visible(html)).toContain('claude-sonnet-4-6');
  });
});
