import { notFound } from 'next/navigation';
import { getTranslations } from 'next-intl/server';
import { fetchDepartmentBySlug, fetchKanban } from '@/lib/queries/kanban';
import { KanbanBoard } from '@/components/features/kanban/KanbanBoard';
import { EmptyState } from '@/components/ui/EmptyState';

export const dynamic = 'force-dynamic';

export default async function DepartmentPage({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  const { slug } = await params;
  const dept = await fetchDepartmentBySlug(slug);
  if (!dept) {
    notFound();
  }
  const tickets = await fetchKanban(slug);
  const t = await getTranslations('kanban');

  return (
    <main className="p-6 space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="text-text-1 text-lg font-semibold font-mono">{dept.slug}</h1>
        <span className="text-text-3 text-sm">{dept.name}</span>
      </div>
      {tickets.length === 0 && dept.columns.length > 0 ? (
        <EmptyState description={t('noTickets')} />
      ) : (
        <KanbanBoard dept={dept} tickets={tickets} />
      )}
    </main>
  );
}
