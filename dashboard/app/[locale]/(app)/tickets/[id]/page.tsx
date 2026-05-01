import { notFound } from 'next/navigation';
import { fetchTicketDetail } from '@/lib/queries/ticketDetail';
import { MetadataBlock } from '@/components/features/ticket-detail/MetadataBlock';
import { HistoryBlock } from '@/components/features/ticket-detail/HistoryBlock';
import { AgentInstancesBlock } from '@/components/features/ticket-detail/AgentInstancesBlock';
import { PalaceLinksBlock } from '@/components/features/ticket-detail/PalaceLinksBlock';
import { TicketInlineEditor } from '@/components/features/ticket-inline-edit/TicketInlineEditor';

export const dynamic = 'force-dynamic';

// UUID guard — Next 16's static `tickets/new/page.tsx` should take
// priority but the [id] dynamic catches any non-UUID slug if a
// future static peer page slips. Skip the DB call and 404 cleanly.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export default async function TicketDetailPage({
  params,
}: Readonly<{
  params: Promise<{ id: string }>;
}>) {
  const { id } = await params;
  if (!UUID_RE.test(id)) notFound();
  const detail = await fetchTicketDetail(id);
  if (!detail) {
    notFound();
  }
  return (
    <div className="p-6 space-y-6 max-w-4xl">
      <MetadataBlock metadata={detail.metadata} />
      <TicketInlineEditor
        ticketId={detail.metadata.id}
        initialObjective={detail.metadata.objective}
        initialAcceptanceCriteria={detail.metadata.acceptanceCriteria}
      />
      <HistoryBlock history={detail.history} />
      <AgentInstancesBlock instances={detail.instances} />
      <PalaceLinksBlock ticketId={detail.metadata.id} />
    </div>
  );
}
