import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { ToolCallChip } from './ToolCallChip';
import type { ToolCallEntry } from '@/lib/sse/chatStream';

function chip(entry: ToolCallEntry): string {
  return renderToString(<ToolCallChip entry={entry} />);
}

describe('ToolCallChip', () => {
  it('renders ReadChipPreCall for postgres.query without result', () => {
    const html = chip({ toolUseId: 'tu_1', toolName: 'postgres.query', args: {} });
    expect(html).toContain('data-state="precall-read"');
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('queried postgres');
  });

  it('renders ReadChipPostCall for postgres.query with result', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'postgres.query',
      args: {},
      result: { isError: false, payload: { detail: '4 rows', is_error: false } },
    });
    expect(html).toContain('data-state="postcall-read"');
    expect(html).not.toContain('aria-busy="true"');
  });

  it('renders MutateChipPreCall for garrison-mutate.create_ticket without result', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.create_ticket',
      args: { objective: 'Fix kanban' },
    });
    expect(html).toContain('data-state="precall-mutate"');
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('create_ticket'); // verb stripped of namespace prefix
  });

  it('renders MutateChipPostCall for garrison-mutate.create_ticket with result', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.create_ticket',
      args: {},
      result: { isError: false, payload: { detail: 'created', is_error: false } },
    });
    expect(html).toContain('data-state="postcall-mutate"');
    expect(html).toContain('create_ticket ✓');
  });

  it('renders FailureChip for tool_result.isError=true', () => {
    const html = chip({
      toolUseId: 'tu_1',
      toolName: 'garrison-mutate.transition_ticket',
      args: {},
      result: { isError: true, payload: { error_kind: 'ticket_state_changed', is_error: true } },
    });
    expect(html).toContain('data-state="failure"');
    expect(html).toContain('role="alert"');
    expect(html).toContain('ticket_state_changed');
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

  it('failure chip has role=alert for screen-reader announcement', () => {
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
});
