import Link from 'next/link';
import { ChatTopbarStrip } from '@/components/features/ceo-chat/ChatTopbarStrip';
import { ChatLandingClient } from '@/components/features/ceo-chat/ChatLandingClient';
import { Chip } from '@/components/ui/Chip';
import { getRecentThreadsForCurrentUser } from '@/lib/actions/chat';
import { listSessionsForUser } from '@/lib/queries/chat';
import { getSession } from '@/lib/auth/session';

// /chat — CEO chat landing page.
//
// Shows the operator's threads, latest on top, so they can resume any
// conversation in one click. The "Pick a thread from the sidebar"
// prompt was removed because it shipped a dead-end interaction —
// landing on /chat after clicking the nav item gave the operator no
// way forward without scanning the sidebar.
//
// If the operator has never opened a thread (active + archived both
// empty), we render the NoThreadsEverEmptyState which carries the
// green "+ New thread" CTA.

export const dynamic = 'force-dynamic';

const STATUS_TONE: Record<string, 'ok' | 'info' | 'neutral' | 'err'> = {
  active: 'ok',
  ended: 'neutral',
  aborted: 'err',
};

export default async function ChatLandingPage() {
  const session = await getSession();
  if (!session) return null;

  const [threads, archived] = await Promise.all([
    getRecentThreadsForCurrentUser(200).catch(() => []),
    listSessionsForUser(session.user.id, { archived: true, limit: 1 }).catch(() => []),
  ]);

  const everHadThread = threads.length > 0 || archived.length > 0;

  return (
    <>
      <ChatTopbarStrip />
      {everHadThread ? (
        <div className="flex-1 overflow-auto p-6 max-w-[1200px] w-full mx-auto">
          <header className="flex items-center justify-between mb-4">
            <div className="space-y-1">
              <h1 className="text-text-1 text-xl font-semibold tracking-tight">CEO chat</h1>
              <p className="text-text-3 text-[12px]">
                Pick a thread to resume, or open a new one. Latest on top.
              </p>
            </div>
            <ChatLandingClient compact />
          </header>
          <ul className="border border-border-1 rounded divide-y divide-border-1" data-testid="chat-thread-list">
            {threads.map((t) => (
              <li key={t.id}>
                <Link
                  href={`/chat/${t.id}`}
                  className="flex items-center justify-between gap-4 px-4 py-2.5 text-sm hover:bg-surface-2/60 transition-colors"
                >
                  <span className="text-text-1 font-medium">thread #{t.threadNumber}</span>
                  <span className="text-text-3 text-[11px] flex-1 text-right font-mono">
                    {new Date(t.startedAt).toISOString().slice(0, 16).replace('T', ' ')}
                  </span>
                  <Chip tone={STATUS_TONE[t.status] ?? 'neutral'}>
                    <span className="text-[10.5px] uppercase tracking-[0.06em]">{t.status}</span>
                  </Chip>
                </Link>
              </li>
            ))}
          </ul>
          {archived.length > 0 ? (
            <div className="mt-3 text-[11.5px] text-text-3">
              <Link href="/chat/all?archived=true" className="hover:text-text-2 underline underline-offset-4">
                View archived threads
              </Link>
            </div>
          ) : null}
        </div>
      ) : (
        <div className="flex-1 flex items-center justify-center p-8">
          <ChatLandingClient />
        </div>
      )}
    </>
  );
}
