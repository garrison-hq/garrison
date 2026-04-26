import { notFound } from 'next/navigation';
import { fetchTicketDetail } from '@/lib/queries/ticketDetail';
import { MetadataBlock } from '@/components/features/ticket-detail/MetadataBlock';
import { HistoryBlock } from '@/components/features/ticket-detail/HistoryBlock';
import { AgentInstancesBlock } from '@/components/features/ticket-detail/AgentInstancesBlock';
import { PalaceLinksBlock } from '@/components/features/ticket-detail/PalaceLinksBlock';

export const dynamic = 'force-dynamic';

export default async function TicketDetailPage({
  params,
}: Readonly<{
  params: Promise<{ id: string }>;
}>) {
  const { id } = await params;
  const detail = await fetchTicketDetail(id);
  if (!detail) {
    notFound();
  }
  return (
    <main className="p-6 space-y-6 max-w-4xl">
      <MetadataBlock metadata={detail.metadata} />
      <HistoryBlock history={detail.history} />
      <AgentInstancesBlock instances={detail.instances} />
      <PalaceLinksBlock ticketId={detail.metadata.id} />
    </main>
  );
}
