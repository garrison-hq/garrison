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

  it('streaming=true + status=failed does NOT show cursor (short-circuit on terminal status)', () => {
    const html = renderToString(
      <MessageBubble role="assistant" status="failed" content="x" streaming />,
    );
    expect(visible(html)).not.toContain('garrison-cursor');
  });

  it('streaming=true + status=aborted does NOT show cursor', () => {
    const html = renderToString(
      <MessageBubble role="assistant" status="aborted" content="x" streaming />,
    );
    expect(visible(html)).not.toContain('garrison-cursor');
  });

  it('renders error block when supplied', () => {
    const html = renderToString(
      <MessageBubble
        role="assistant"
        status="failed"
        content={null}
        errorBlock={<div data-testid="error-block-stub">err</div>}
      />,
    );
    expect(visible(html)).toContain('error-block-stub');
  });

  it('renders the C avatar only for assistant rows', () => {
    const op = renderToString(
      <MessageBubble role="operator" status="completed" content="x" />,
    );
    const asst = renderToString(
      <MessageBubble role="assistant" status="completed" content="x" />,
    );
    // Operator bubble doesn't render the C avatar wrapper.
    expect(visible(op)).not.toContain('CEO');
    // Assistant bubble does.
    expect(visible(asst)).toContain('CEO');
  });

  it('renders null cost gracefully (operator-style empty footer)', () => {
    const html = renderToString(
      <MessageBubble role="assistant" status="completed" content="x" costUsd={null} />,
    );
    // Footer renders but with empty content (formatPerMessageCost returns null for null).
    expect(visible(html)).toContain('chat-message-cost');
  });

  it('shows the typing dots while pending with no content yet', () => {
    const html = renderToString(
      <MessageBubble role="assistant" status="pending" content={null} streaming />,
    );
    const v = visible(html);
    expect(v).toContain('chat-typing-indicator');
    expect(v).toContain('garrison-typing-dot');
    expect(v).not.toContain('garrison-cursor');
  });

  it('swaps dots for streaming text + cursor once content arrives', () => {
    const html = renderToString(
      <MessageBubble role="assistant" status="streaming" content="hello" streaming />,
    );
    const v = visible(html);
    expect(v).not.toContain('chat-typing-indicator');
    expect(v).toContain('garrison-cursor');
    expect(v).toContain('hello');
  });
});
