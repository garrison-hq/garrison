import { notFound } from 'next/navigation';
import { fetchTicketDetail } from '@/lib/queries/ticketDetail';
import { MetadataBlock } from '@/components/features/ticket-detail/MetadataBlock';
import { HistoryBlock } from '@/components/features/ticket-detail/HistoryBlock';
import { AgentInstancesBlock } from '@/components/features/ticket-detail/AgentInstancesBlock';
import { PalaceLinksBlock } from '@/components/features/ticket-detail/PalaceLinksBlock';
import { TicketDetailPanel } from '@/components/features/ticket-detail/TicketDetailPanel';

// Intercepted route: when a Link inside the (app) shell navigates
// to /tickets/[id], Next.js routes it into the @panel slot via
// this page rather than swapping `children`. The user sees a
// slide-in drawer while the previous surface stays mounted
// underneath. Direct navigation, refresh, or share-link visits
// hit (app)/tickets/[id]/page.tsx instead — full-page render.
//
// Architecture note: TicketDetailPanel is a Client Component
// (needs router.back, ESC handler, body scroll-lock). The
// content blocks below are Server Components that pull
// translations + DB rows. Compose them as `children` of the
// panel so the SC tree is kept on the server side and only the
// drawer chrome ships to the client.

export const dynamic = 'force-dynamic';

// UUID-shape guard for the intercepting route. Next 16's `[id]`
// dynamic segment matches ANY non-empty path component including
// non-UUID strings like "new" (the create form path). Returning
// null for non-UUID slugs leaves the @panel slot empty so the
// static /tickets/new page renders via the children slot —
// notFound() here would render the 404 boundary inside the panel
// instead of falling through.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export default async function InterceptedTicketPage({
  params,
}: Readonly<{
  params: Promise<{ id: string }>;
}>) {
  const { id } = await params;
  if (!UUID_RE.test(id)) return null;
  const detail = await fetchTicketDetail(id);
  if (!detail) notFound();
  return (
    <TicketDetailPanel
      ticketId={detail.metadata.id}
      ticketIdShort={detail.metadata.id.slice(0, 8)}
      departmentSlug={detail.metadata.departmentSlug}
    >
      <MetadataBlock metadata={detail.metadata} />
      <HistoryBlock history={detail.history} />
      <AgentInstancesBlock instances={detail.instances} />
      <PalaceLinksBlock ticketId={detail.metadata.id} />
    </TicketDetailPanel>
  );
}
