import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { ToolCallChip } from './ToolCallChip';
import type { ToolCallEntry } from '@/lib/sse/chatStream';

function chip(entry: ToolCallEntry): string {
  return renderToString(<ToolCallChip entry={entry} />);
}

// Strip HTML tags + comment markers so assertions can match the
// visible text shape (e.g. "▸ queried · postgres · …") regardless
// of the per-span markup the chip emits.
function visibleText(html: string): string {
  return html
    .replace(/<!--\s*-->/g, '')
    .replace(/<[^>]+>/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

describe('ToolCallChip', () => {
  it('renders a pre-call read chip with the queried-server result placeholder', () => {
    const html = chip({ toolUseId: 'tu_1', toolName: 'postgres.query', args: {} });
    expect(html).toContain('data-state="precall-read"');
    expect(html).toContain('aria-busy="true"');
    expect(visibleText(html)).toMatch(/queried\s*postgres\s*·\s*…/);
  });

  it('renders a post-call read chip with the ok result label', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'postgres.query',
      args: {},
      result: { isError: false, payload: { detail: '4 rows', is_error: false } },
    });
    expect(html).toContain('data-state="postcall-read"');
    expect(html).not.toContain('aria-busy="true"');
    expect(visibleText(html)).toMatch(/queried\s*postgres\s*·\s*ok/);
  });

  it('renders a pre-call mutation chip with the verb-name target', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.create_ticket',
      args: { objective: 'Fix kanban' },
    });
    expect(html).toContain('data-state="precall-mutate"');
    expect(html).toContain('aria-busy="true"');
    expect(visibleText(html)).toMatch(/called\s*create_ticket\s*·\s*…/);
  });

  it('renders a post-call mutation chip with ok result', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.create_ticket',
      args: {},
      result: { isError: false, payload: { detail: 'created', is_error: false } },
    });
    expect(html).toContain('data-state="postcall-mutate"');
    expect(visibleText(html)).toMatch(/called\s*create_ticket\s*·\s*ok/);
  });

  it('renders a failure chip with the error_kind as the result token', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.transition_ticket',
      args: {},
      result: { isError: true, payload: { error_kind: 'ticket_state_changed', is_error: true } },
    });
    expect(html).toContain('data-state="failure"');
    expect(html).toContain('role="alert"');
    expect(visibleText(html)).toMatch(/called\s*transition_ticket\s*·\s*ticket_state_changed/);
  });

  it('mutation post-call chips with affected_resource_url include a deep-link', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.create_ticket',
      args: {},
      result: {
        isError: false,
        payload: {
          detail: 'created',
          is_error: false,
          affected_resource_url: '/tickets/abc-123',
        },
      },
    });
    expect(html).toContain('href="/tickets/abc-123"');
  });

  it('chip is informative-only — no buttons rendered', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.transition_ticket',
      args: {},
      result: { isError: false, payload: { detail: 'ok', is_error: false } },
    });
    expect(html).not.toContain('<button');
  });

  it('failure chip on a non-mutation tool has role=alert for screen-reader announcement', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'postgres.query',
      args: {},
      result: { isError: true, payload: { detail: 'connection refused', is_error: true } },
    });
    expect(html).toContain('role="alert"');
  });

  it('chips carry data-testid="toolcall-chip" for Playwright targeting', () => {
    const html = chip({ toolUseId: 'tu_1', toolName: 'postgres.query', args: {} });
    expect(html).toContain('data-testid="toolcall-chip"');
  });

  it('renders mempalace pre-call as searched palace', () => {
    const html = chip({ toolUseId: 'tu_2', toolName: 'mempalace.search', args: {} });
    expect(visibleText(html)).toMatch(/searched\s*palace\s*·\s*…/);
  });

  it('renders mempalace post-call with ok result', () => {
    const html = chip({
      toolUseId: 'tu_2',
      toolName: 'mempalace.search',
      args: {},
      result: { isError: false, payload: { detail: 'hits', is_error: false } },
    });
    expect(html).toContain('postcall-read');
    expect(visibleText(html)).toMatch(/searched\s*palace\s*·\s*ok/);
  });

  it('falls back to "called <toolname>" for unknown tool families', () => {
    const html = chip({ toolUseId: 'tu_3', toolName: 'docker.inspect', args: {} });
    expect(visibleText(html)).toMatch(/called\s*docker\.inspect\s*·\s*…/);
  });

  it('failure chip falls back to "failed" when payload has no error_kind or detail', () => {
    const html = chip({
      toolUseId: 'tu_4',
      toolName: 'garrison-mutate.spawn_agent',
      args: {},
      result: { isError: true, payload: { is_error: true } },
    });
    expect(visibleText(html)).toMatch(/spawn_agent\s*·\s*failed/);
  });

  it('failure chip uses detail when error_kind is missing', () => {
    const html = chip({
      toolUseId: 'tu_5',
      toolName: 'postgres.query',
      args: {},
      result: { isError: true, payload: { detail: 'connection refused', is_error: true } },
    });
    expect(visibleText(html)).toMatch(/queried\s*postgres\s*·\s*connection refused/);
  });

  it('failure chip handles non-object payloads gracefully', () => {
    const html = chip({
      toolUseId: 'tu_6',
      toolName: 'postgres.query',
      args: {},
      result: { isError: true, payload: 'string-payload' },
    });
    expect(visibleText(html)).toMatch(/queried\s*postgres\s*·\s*failed/);
  });

  it('mutation post-call chips render without a deep-link when no affected_resource_url', () => {
    const html = chip({
      toolUseId: 'tu_7',
      toolName: 'garrison-mutate.edit_ticket',
      args: {},
      result: { isError: false, payload: { detail: 'updated', is_error: false } },
    });
    expect(html).toContain('postcall-mutate');
    expect(visibleText(html)).toMatch(/called\s*edit_ticket\s*·\s*ok/);
    expect(html).not.toContain('<a ');
  });

  it('affectedResourceURL ignores non-string url values', () => {
    const html = chip({
      toolUseId: 'tu_8',
      toolName: 'garrison-mutate.create_ticket',
      args: {},
      result: {
        isError: false,
        payload: { affected_resource_url: 12345, detail: 'ok', is_error: false },
      },
    });
    expect(html).not.toContain('<a ');
    expect(visibleText(html)).toMatch(/called\s*create_ticket\s*·\s*ok/);
  });

  it('failure chip on a non-mutation tool surfaces the server-specific verb', () => {
    const html = chip({
      toolUseId: 'tu_9',
      toolName: 'mempalace.search',
      args: {},
      result: { isError: true, payload: { error_kind: 'timeout', is_error: true } },
    });
    expect(visibleText(html)).toMatch(/searched\s*palace\s*·\s*timeout/);
  });
});
