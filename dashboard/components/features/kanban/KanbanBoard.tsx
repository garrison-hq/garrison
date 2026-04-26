import { KanbanColumn } from './KanbanColumn';
import type { DeptInfo, TicketCardRow } from '@/lib/queries/kanban';

export function KanbanBoard({
  dept,
  tickets,
}: Readonly<{
  dept: DeptInfo;
  tickets: TicketCardRow[];
}>) {
  // Group tickets into columns by column_slug. Empty columns are
  // still rendered so the four-column visual cadence is stable
  // regardless of where tickets currently live.
  const byColumn = new Map<string, TicketCardRow[]>();
  for (const c of dept.columns) {
    byColumn.set(c.slug, []);
  }
  for (const t of tickets) {
    if (byColumn.has(t.columnSlug)) {
      byColumn.get(t.columnSlug)!.push(t);
    }
  }
  return (
    <div className="grid gap-3 h-full" style={{ gridTemplateColumns: `repeat(${dept.columns.length}, minmax(220px, 1fr))` }}>
      {dept.columns.map((c) => (
        <KanbanColumn
          key={c.slug}
          slug={c.slug}
          label={c.label}
          tickets={byColumn.get(c.slug) ?? []}
        />
      ))}
    </div>
  );
}
