'use client';

import type { MouseEvent } from 'react';
import Link from 'next/link';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';
import type { TicketCardRow } from '@/lib/queries/kanban';

// Read-only ticket card. Per FR-042 there is NO drag handle, NO
// inline edit affordance — the card is a clickable summary that
// opens the ticket detail.
//
// Layout decisions from the polish round:
//  - Removed the left-edge accent bar (its meaning was unclear).
//  - ID + age sit on a single header row (mono, both at the same
//    text-3 contrast level so neither out-shouts the title).
//  - Title gets explicit line-height and a min-height so cards
//    don't collapse into squat strips when the objective is short.
//  - Assignee (when present) renders below the title as muted
//    mono text — every card carries the same shape; if no agent
//    is assigned the row is omitted rather than showing an em-dash.

export function TicketCard({ ticket }: Readonly<{ ticket: TicketCardRow }>) {
  return (
    <Link
      href={`/tickets/${ticket.id}`}
      className="block bg-surface-2 hover:bg-surface-3 border border-border-1 hover:border-border-2 rounded-md p-3 min-h-[88px] transition-colors"
      data-testid="ticket-card"
    >
      <div className="flex items-center justify-between gap-2 mb-1.5">
        <div className="flex items-center gap-2 min-w-0">
          <span className="font-mono text-[10.5px] text-text-3 tracking-tight">
            {ticket.id.slice(0, 8)}
          </span>
          {ticket.parentTicketId ? (
            <Link
              href={`/tickets/${ticket.parentTicketId}`}
              onClick={(e: MouseEvent<HTMLAnchorElement>) => e.stopPropagation()}
              className="font-mono text-[10.5px] text-text-3 hover:text-text-1 tracking-tight"
              data-testid="ticket-parent-chip"
            >
              parent: {ticket.parentTicketId.slice(0, 8)}
            </Link>
          ) : null}
        </div>
        <span
          className="font-mono text-[10.5px] text-text-3 font-tabular"
          title={`updated ${formatIsoFull(ticket.createdAt)}`}
          // Relative time computed against Date.now() — SSR + client
          // can read different "now" values when the minute ticks
          // over between render and hydrate (e.g. server says
          // "24m ago", client says "25m ago"). suppressHydrationWarning
          // tells React this specific subtree is intentionally
          // non-deterministic; the client's value wins after hydrate.
          suppressHydrationWarning
        >
          {relativeTime(ticket.createdAt)}
        </span>
      </div>
      <div className="text-text-1 text-[13px] leading-[1.4] line-clamp-3">
        {ticket.objective}
      </div>
      {ticket.assignedAgentRoleSlug ? (
        <div className="mt-2 text-[11px] text-text-3 font-mono truncate">
          {ticket.assignedAgentRoleSlug}
        </div>
      ) : null}
    </Link>
  );
}
