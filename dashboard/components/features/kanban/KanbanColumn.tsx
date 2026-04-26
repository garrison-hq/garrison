import { TicketCard } from './TicketCard';
import type { TicketCardRow } from '@/lib/queries/kanban';

export function KanbanColumn({
  slug,
  label,
  tickets,
}: Readonly<{
  slug: string;
  label: string;
  tickets: TicketCardRow[];
}>) {
  return (
    <div className="flex-1 min-w-[200px] bg-surface-1 border border-border-1 rounded flex flex-col">
      <div className="px-3 py-2 border-b border-border-1 flex items-center justify-between">
        <span className="text-text-1 text-xs font-medium uppercase tracking-wider">{label}</span>
        <span className="text-text-3 text-[11px] font-mono">{tickets.length}</span>
      </div>
      <div className="p-2 space-y-2 min-h-[100px]" data-testid={`column-${slug}`}>
        {tickets.map((t) => (
          <TicketCard key={t.id} ticket={t} />
        ))}
      </div>
    </div>
  );
}
