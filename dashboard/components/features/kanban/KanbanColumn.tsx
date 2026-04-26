import { TicketCard } from './TicketCard';
import { columnDotTone } from '@/lib/format/columnTone';
import { StatusDot } from '@/components/ui/StatusDot';
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
    <div className="bg-surface-1 border border-border-1 rounded-md flex flex-col min-h-0">
      <div className="px-3 py-2.5 border-b border-border-1 flex items-center gap-2">
        <StatusDot tone={columnDotTone(slug)} />
        <span className="text-text-2 text-[10.5px] font-medium uppercase tracking-[0.08em]">
          {label}
        </span>
        <span className="ml-auto inline-flex items-center justify-center min-w-5 h-5 px-1.5 rounded bg-surface-3 text-text-1 text-[10.5px] font-mono font-tabular">
          {tickets.length}
        </span>
      </div>
      <div
        className="p-2 space-y-2 flex-1 overflow-y-auto min-h-[120px]"
        data-testid={`column-${slug}`}
      >
        {tickets.length === 0 ? (
          <p className="text-text-4 text-[11px] text-center pt-6 select-none">
            no tickets in {label.toLowerCase()}
          </p>
        ) : (
          tickets.map((t) => <TicketCard key={t.id} ticket={t} />)
        )}
      </div>
    </div>
  );
}
