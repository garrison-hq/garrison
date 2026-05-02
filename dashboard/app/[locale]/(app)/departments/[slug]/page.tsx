import { notFound } from 'next/navigation';
import { fetchDepartmentBySlug, fetchKanban } from '@/lib/queries/kanban';
import { KanbanBoard } from '@/components/features/kanban/KanbanBoard';

export const dynamic = 'force-dynamic';

export default async function DepartmentPage({
  params,
}: Readonly<{
  params: Promise<{ slug: string }>;
}>) {
  const { slug } = await params;
  const dept = await fetchDepartmentBySlug(slug);
  if (!dept) {
    notFound();
  }
  const tickets = await fetchKanban(slug);

  // Per-column counts for the header strip — exposed up here so
  // the header reads as a primary nav cue, not a chrome detail.
  const counts: Record<string, number> = {};
  for (const c of dept.columns) counts[c.slug] = 0;
  for (const t of tickets) {
    if (counts[t.columnSlug] !== undefined) counts[t.columnSlug] += 1;
  }

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto h-full flex flex-col">
      <header className="space-y-2">
        <div className="flex items-baseline gap-3">
          <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
            {dept.name}
          </h1>
          <span className="text-text-3 text-xs font-mono">{dept.slug}</span>
          {/* Department-scoped ticket creation: prefills the
              `dept` query param so the form lands with the
              right workflow already selected. Plain <a> (not
              next/link) so the navigation bypasses the @panel
              intercepting route — see Sidebar's "New ticket"
              for the same pattern. */}
          <a
            href={`/tickets/new?dept=${encodeURIComponent(dept.slug)}`}
            className="ml-auto inline-flex items-center gap-1.5 px-3 py-1.5 text-[12.5px] rounded bg-accent text-white hover:bg-accent/90"
            data-testid="new-ticket-in-dept"
          >
            <span aria-hidden>+</span>
            <span>New ticket</span>
          </a>
        </div>
        <div className="flex items-center gap-5 text-[11px]">
          {dept.columns.map((c) => (
            <span key={c.slug} className="flex items-center gap-1.5">
              <span className="text-text-3 uppercase tracking-[0.08em] text-[10.5px]">
                {c.label}
              </span>
              <span className="text-text-1 font-mono font-tabular">
                {counts[c.slug] ?? 0}
              </span>
            </span>
          ))}
        </div>
      </header>
      <div className="flex-1 min-h-0">
        <KanbanBoard dept={dept} tickets={tickets} />
      </div>
    </div>
  );
}
