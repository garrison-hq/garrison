import { ChatTopbarStrip } from '@/components/features/ceo-chat/ChatTopbarStrip';
import { ChatLandingClient } from '@/components/features/ceo-chat/ChatLandingClient';
import { EmptyState } from '@/components/ui/EmptyState';
import { listSessionsForUser } from '@/lib/queries/chat';
import { getSession } from '@/lib/auth/session';

// M5.2 — /chat (no thread selected). Shows either:
//   - the NoThreadsEverEmptyState (with "+ New thread" CTA) if the
//     operator has never opened a thread
//   - a "Pick a thread" prompt otherwise

export const dynamic = 'force-dynamic';

export default async function ChatLandingPage() {
  const session = await getSession();
  if (!session) return null;
  const sessions = await listSessionsForUser(session.user.id, { limit: 1 });
  const archived = await listSessionsForUser(session.user.id, { archived: true, limit: 1 });
  const hasAny = sessions.length > 0 || archived.length > 0;
  return (
    <>
      <ChatTopbarStrip />
      <div className="flex-1 flex items-center justify-center p-8">
        {hasAny ? (
          <EmptyState
            description="Pick a thread from the sidebar or open a new one."
            caption="Chats stream live; the CEO is summoned per message."
          />
        ) : (
          <ChatLandingClient />
        )}
      </div>
    </>
  );
}
