'use client';

import { useState, useTransition, useEffect, type DragEvent } from 'react';
import { useRouter } from 'next/navigation';
import { TicketCard } from './TicketCard';
import { columnDotTone } from '@/lib/format/columnTone';
import { StatusDot } from '@/components/ui/StatusDot';
import { moveTicket } from '@/lib/actions/tickets';
import { ConflictError } from '@/lib/locks/conflict';
import type { DeptInfo, TicketCardRow } from '@/lib/queries/kanban';

// KanbanBoard — extends the M3 read view with M4 drag-to-move per
// FR-027 / FR-029 (clarified: same channel as agent finalize) /
// FR-035 / FR-036 / FR-042 / FR-043.
//
// Optimistic UI: cards move on drop immediately, then the
// moveTicket server action runs. On failure (the supervisor
// finalize path raced us, the column doesn't exist, the
// fromColumn doesn't match the row's actual column anymore),
// we revert the card to its original column and surface an
// err-tone toast. router.refresh() pulls the canonical state
// after the server action succeeds.
//
// Inline KanbanColumn rendering (was a separate Server Component
// in M3) so the drag handlers live in one place — moving across
// the Client/Server boundary cleanly.

export function KanbanBoard({
  dept,
  tickets,
}: Readonly<{
  dept: DeptInfo;
  tickets: TicketCardRow[];
}>) {
  const router = useRouter();
  const [optimisticTickets, setOptimisticTickets] = useState<TicketCardRow[]>(tickets);
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [hoveredColumn, setHoveredColumn] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [, startTransition] = useTransition();

  // Reconcile optimistic state when the parent passes new data
  // (M3 60s soft-poll, SSE-driven refresh, post-mutation refresh).
  useEffect(() => {
    setOptimisticTickets(tickets);
  }, [tickets]);

  function handleDragStart(e: DragEvent, ticketId: string) {
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', ticketId);
    setDraggingId(ticketId);
  }

  function handleDragOver(e: DragEvent, columnSlug: string) {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    if (hoveredColumn !== columnSlug) {
      setHoveredColumn(columnSlug);
    }
  }

  function handleDragLeave(columnSlug: string) {
    if (hoveredColumn === columnSlug) {
      setHoveredColumn(null);
    }
  }

  function handleDrop(e: DragEvent, toColumn: string) {
    e.preventDefault();
    setHoveredColumn(null);
    const ticketId = e.dataTransfer.getData('text/plain');
    setDraggingId(null);
    if (!ticketId) return;

    const ticket = optimisticTickets.find((t) => t.id === ticketId);
    if (!ticket) return;

    const fromColumn = ticket.columnSlug;
    if (fromColumn === toColumn) {
      // FR-036: no-op move; nothing to do, no audit row.
      return;
    }

    // Optimistic update.
    setColumnFor(ticketId, toColumn);
    setError(null);

    startTransition(async () => {
      try {
        await moveTicket({ ticketId, fromColumn, toColumn });
        router.refresh();
      } catch (err) {
        // Revert per FR-042.
        setColumnFor(ticketId, fromColumn);
        if (err instanceof ConflictError) {
          setError(`Could not move: ${err.message}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  function setColumnFor(ticketId: string, column: string): void {
    setOptimisticTickets((prev) =>
      prev.map((t) => (t.id === ticketId ? { ...t, columnSlug: column } : t)),
    );
  }

  function handleDragEnd() {
    setDraggingId(null);
    setHoveredColumn(null);
  }

  const byColumn = new Map<string, TicketCardRow[]>();
  for (const c of dept.columns) {
    byColumn.set(c.slug, []);
  }
  for (const t of optimisticTickets) {
    if (byColumn.has(t.columnSlug)) {
      byColumn.get(t.columnSlug)!.push(t);
    }
  }

  return (
    <div className="space-y-2">
      {error && (
        <div className="rounded border border-err/40 bg-err/5 px-3 py-2 text-[12.5px] text-err">
          {error}
        </div>
      )}
      <div
        className="grid gap-3 h-full"
        style={{
          gridTemplateColumns: `repeat(${dept.columns.length}, minmax(220px, 1fr))`,
        }}
      >
        {dept.columns.map((c) => {
          const colTickets = byColumn.get(c.slug) ?? [];
          const isHovered = hoveredColumn === c.slug;
          return (
            <section
              key={c.slug}
              aria-label={`${c.label} column drop target`}
              onDragOver={(e) => handleDragOver(e, c.slug)}
              onDragLeave={() => handleDragLeave(c.slug)}
              onDrop={(e) => handleDrop(e, c.slug)}
              className={`bg-surface-1 border rounded-md flex flex-col min-h-0 transition-colors ${
                isHovered ? 'border-accent/50 ring-1 ring-accent/20' : 'border-border-1'
              }`}
            >
              <div className="px-3 py-2.5 border-b border-border-1 flex items-center gap-2">
                <StatusDot tone={columnDotTone(c.slug)} />
                <span className="text-text-2 text-[10.5px] font-medium uppercase tracking-[0.08em]">
                  {c.label}
                </span>
                <span className="ml-auto inline-flex items-center justify-center min-w-5 h-5 px-1.5 rounded bg-surface-3 text-text-1 text-[10.5px] font-mono font-tabular">
                  {colTickets.length}
                </span>
              </div>
              <div
                className="p-2 space-y-2 flex-1 overflow-y-auto min-h-[120px]"
                data-testid={`column-${c.slug}`}
              >
                {colTickets.length === 0 ? (
                  <p className="text-text-4 text-[11px] text-center pt-6 select-none">
                    no tickets in {c.label.toLowerCase()}
                  </p>
                ) : (
                  colTickets.map((t) => (
                    <div
                      key={t.id}
                      role="button"
                      tabIndex={0}
                      aria-label={`Drag ${t.id}`}
                      draggable
                      onDragStart={(e) => handleDragStart(e, t.id)}
                      onDragEnd={handleDragEnd}
                      className={`cursor-grab transition-opacity ${
                        draggingId === t.id ? 'opacity-40' : ''
                      }`}
                      data-testid={`ticket-card-${t.id}`}
                    >
                      <TicketCard ticket={t} />
                    </div>
                  ))
                )}
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
}
