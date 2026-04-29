// M5.2 — ChatErrorBlock SSR rendering test (FR-272 + FR-273).

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { ChatErrorBlock } from './ChatErrorBlock';

function visible(html: string): string {
  return html.replace(/<!--\s*-->/g, '');
}

describe('ChatErrorBlock', () => {
  it('TestMessageBubbleRendersErrorVariant', () => {
    const html = renderToString(<ChatErrorBlock errorKind="container_crashed" />);
    const v = visible(html);
    expect(v).toContain('Chat container crashed');
    expect(v).toContain('chat-error-block');
    expect(v).toContain('data-error-kind="container_crashed"');
  });

  it('renders the deep-link for token_expired', () => {
    const html = renderToString(<ChatErrorBlock errorKind="token_expired" />);
    const v = visible(html);
    expect(v).toContain('Rotate token');
    expect(v).toContain('CLAUDE_CODE_OAUTH_TOKEN');
  });

  it('renders the dynamic mcp_<server>_<status> shape', () => {
    const html = renderToString(<ChatErrorBlock errorKind="mcp_postgres_failed" />);
    const v = visible(html);
    expect(v.toLowerCase()).toContain('postgres');
  });
});
