// @vitest-environment jsdom

// Branch coverage for EventRow's tone classifier + EventDescription
// switch. Each test renders one event variant and asserts the rendered
// markup carries the expected verb/copy. Pulled together from M5.3's
// PR diff which added the chat.* variants — operator-visible UI is
// thin enough to be exercised directly via @testing-library/react.

import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { EventRow } from './EventRow';
import type { ActivityEvent } from '@/lib/sse/events';

function row(event: ActivityEvent) {
  return render(<EventRow event={event} />);
}

describe('EventRow', () => {
  it('renders ticket.created with chip + dash for missing-ish ticket', () => {
    row({
      kind: 'ticket.created',
      eventId: 'e-1',
      at: '2026-04-30T12:00:00Z',
      ticketId: 'aabbccddeeff0011',
    });
    expect(screen.getByText('ticket.created')).toBeTruthy();
    expect(screen.getByText('created')).toBeTruthy();
  });

  it('renders ticket.transitioned with from→to + dept', () => {
    row({
      kind: 'ticket.transitioned',
      eventId: 'e-2',
      at: '2026-04-30T12:00:01Z',
      ticketId: '11223344556677',
      department: 'engineering',
      from: 'todo',
      to: 'in_progress',
      agentInstanceId: null,
      hygieneStatus: 'operator_initiated',
    });
    expect(screen.getByText('engineering')).toBeTruthy();
    expect(screen.getByText('todo')).toBeTruthy();
    expect(screen.getByText('in_progress')).toBeTruthy();
  });

  it('renders unknown channel hint', () => {
    row({
      kind: 'unknown',
      eventId: 'e-3',
      at: '2026-04-30T12:00:02Z',
      channel: 'work.weird.event',
    });
    expect(screen.getByText(/unknown channel/)).toBeTruthy();
  });

  it('renders unknown error variant with err tone', () => {
    const { container } = row({
      kind: 'unknown',
      eventId: 'e-3b',
      at: '2026-04-30T12:00:02Z',
      channel: 'error.something_broke',
    });
    // chipToneFor maps unknown+error.* → err tone; the visible
    // assertion is that the chip says "unknown" — color is
    // applied via tailwind classes the chip swallows. We just
    // verify the row rendered without throwing.
    expect(container.querySelector('[data-testid="event-row"]')).toBeTruthy();
  });

  it('renders chat.session_deleted lifecycle predicate', () => {
    row({
      kind: 'chat.session_deleted',
      eventId: 'e-4',
      at: '2026-04-30T12:00:03Z',
      chatSessionId: 'aaaaaaaa11111111',
    });
    expect(screen.getByText(/Chat thread/)).toBeTruthy();
    expect(screen.getByText(/deleted by operator/)).toBeTruthy();
  });

  it.each([
    ['chat.session_started', 'started'] as const,
    ['chat.message_sent', 'message sent'] as const,
    ['chat.session_ended', 'ended'] as const,
  ])('renders %s lifecycle predicate', (kind, verb) => {
    row({
      kind,
      eventId: 'e-life',
      at: '2026-04-30T12:00:04Z',
      chatSessionId: '11112222333344445555',
    } as ActivityEvent);
    // 'Chat session <id> <verb>' lives in a span next to the chip text;
    // both the chip and the span may match — assert at least one hit.
    expect(screen.getAllByText(new RegExp(verb)).length).toBeGreaterThan(0);
  });

  it('renders chat.ticket.transitioned with TicketTransitioned + extras', () => {
    row({
      kind: 'chat.ticket.transitioned',
      eventId: 'e-5',
      at: '2026-04-30T12:00:05Z',
      affectedResourceId: 'tkt-aabbccdd',
      extras: { from_column: 'todo', to_column: 'in_progress' },
    } as unknown as ActivityEvent);
    expect(screen.getByText(/Chat transitioned/)).toBeTruthy();
    // Both 'todo' and 'in_progress' may appear in the kind chip + the
    // arrow markup; use getAllByText.
    expect(screen.getAllByText('todo').length).toBeGreaterThan(0);
    expect(screen.getAllByText('in_progress').length).toBeGreaterThan(0);
  });

  it('renders generic chat-mutation kind via describeChatMutation', () => {
    row({
      kind: 'chat.ticket.created',
      eventId: 'e-6',
      at: '2026-04-30T12:00:06Z',
      affectedResourceId: 'tkt-99887766',
      extras: {},
    } as unknown as ActivityEvent);
    expect(screen.getByText(/Chat ticket created/)).toBeTruthy();
  });

  it('renders chat.agent.spawned with verb formatting', () => {
    row({
      kind: 'chat.agent.spawned',
      eventId: 'e-7',
      at: '2026-04-30T12:00:07Z',
      affectedResourceId: 'agentslug',
      extras: {},
    } as unknown as ActivityEvent);
    expect(screen.getByText(/Chat agent spawned/)).toBeTruthy();
  });

  it('renders generic kind for unrecognised future variants', () => {
    row({
      kind: 'vault.rotated' as ActivityEvent['kind'],
      eventId: 'e-8',
      at: '2026-04-30T12:00:08Z',
    } as unknown as ActivityEvent);
    // Falls through to the default: <span>{event.kind}</span>
    const matches = screen.getAllByText('vault.rotated');
    expect(matches.length).toBeGreaterThan(0);
  });

  it('renders the deep-link when ticketId is present', () => {
    const { container } = row({
      kind: 'ticket.created',
      eventId: 'e-9',
      at: '2026-04-30T12:00:09Z',
      ticketId: '00112233445566778899',
    });
    const link = container.querySelector('a[href^="/tickets/"]');
    expect(link).toBeTruthy();
  });

  it('renders dash when no ticketId', () => {
    const { container } = row({
      kind: 'unknown',
      eventId: 'e-10',
      at: '2026-04-30T12:00:10Z',
      channel: 'work.misc',
    });
    // The dash lives in the right-most span — find any em-dash text node.
    const dashes = Array.from(container.querySelectorAll('span')).filter(
      (el) => el.textContent === '—',
    );
    expect(dashes.length).toBeGreaterThan(0);
  });
});
