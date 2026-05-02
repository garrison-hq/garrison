// M6 — TicketCard parent-chip render tests.
//
// The card itself is a Server-renderable Link wrapper; M5.x tests
// for kanban hovering / click semantics live in the Playwright suite.
// This file pins the M6 T017 addition: the parent-chip surface
// (rendered when ticket.parentTicketId is non-null, omitted when null).

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { TicketCard } from './TicketCard';
import type { TicketCardRow } from '@/lib/queries/kanban';

const baseTicket: TicketCardRow = {
  id: '11111111-1111-1111-1111-111111111111',
  objective: 'Build the kanban thing',
  columnSlug: 'in_dev',
  createdAt: new Date('2026-05-02T10:00:00Z'),
  assignedAgentRoleSlug: null,
  parentTicketId: null,
};

describe('TicketCard', () => {
  it('TestTicketCardOmitsParentChipWhenNull', () => {
    const html = renderToString(<TicketCard ticket={baseTicket} />);
    expect(html).not.toContain('data-testid="ticket-parent-chip"');
    expect(html).not.toContain('parent:');
  });

  it('TestTicketCardRendersParentChipWhenSet', () => {
    const ticket: TicketCardRow = {
      ...baseTicket,
      parentTicketId: '22222222-2222-2222-2222-222222222222',
    };
    const html = renderToString(<TicketCard ticket={ticket} />);
    expect(html).toContain('data-testid="ticket-parent-chip"');
    // React inserts a comment between text nodes; strip them before
    // asserting the rendered prefix.
    const visible = html.replace(/<!--\s*-->/g, '');
    expect(visible).toContain('parent: 22222222');
    // Chip is a Link whose href targets the parent's detail page.
    expect(html).toMatch(/href="\/tickets\/22222222-2222-2222-2222-222222222222"/);
  });

  it('TestTicketCardRendersTicketIDPrefix', () => {
    const html = renderToString(<TicketCard ticket={baseTicket} />);
    // The card surfaces the first 8 chars of the ticket id, mono.
    expect(html).toContain('11111111');
  });

  it('TestTicketCardLinksToTicketDetail', () => {
    const html = renderToString(<TicketCard ticket={baseTicket} />);
    expect(html).toMatch(/href="\/tickets\/11111111-1111-1111-1111-111111111111"/);
  });
});
