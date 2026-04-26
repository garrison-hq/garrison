import Link from 'next/link';
import { Chip } from '@/components/ui/Chip';
import type { TicketCardRow } from '@/lib/queries/kanban';

// Read-only ticket card. Per FR-042 there is NO drag handle, NO
// inline edit affordance — the card is just a clickable summary
// linking to ticket detail. Priority indicator on the left edge
// is a constant accent color (M3 doesn't ship a priority field;
// the visual is reserved for M4 when priority enters the schema).

export function TicketCard({ ticket }: { ticket: TicketCardRow }) {
  const ageDays = Math.floor(
    (Date.now() - new Date(ticket.createdAt).getTime()) / (1000 * 60 * 60 * 24),
  );
  return (
    <Link
      href={`/tickets/${ticket.id}`}
      className="block bg-surface-2 hover:bg-surface-3 border border-border-1 rounded p-2 text-xs space-y-1.5"
      data-testid="ticket-card"
    >
      <div className="flex items-start gap-2">
        <span className="w-0.5 self-stretch bg-accent rounded-full" aria-hidden />
        <div className="flex-1 min-w-0">
          <div className="font-mono text-[10px] text-text-3 truncate">{ticket.id.slice(0, 8)}</div>
          <div className="text-text-1 line-clamp-2">{ticket.objective}</div>
        </div>
      </div>
      <div className="flex items-center justify-between gap-2 text-[11px]">
        {ticket.assignedAgentRoleSlug ? (
          <Chip tone="info">{ticket.assignedAgentRoleSlug}</Chip>
        ) : (
          <span className="text-text-3">—</span>
        )}
        <span className="text-text-3 font-mono">{ageDays}d</span>
      </div>
    </Link>
  );
}
