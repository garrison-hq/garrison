import Link from 'next/link';
import { ChatTopbarStrip } from '@/components/features/ceo-chat/ChatTopbarStrip';
import { EmptyState } from '@/components/ui/EmptyState';
import { listSessionsForUser } from '@/lib/queries/chat';
import { getSession } from '@/lib/auth/session';

// M5.2 — /chat/all. Full thread-list page (active + archived sub-tabs).
// Server-rendered table; archived tab is a stub link (?archived=true).
// Tab state lives in URL — keeps SSR cheap, no client state.

export const dynamic = 'force-dynamic';

interface AllChatsPageProps {
  searchParams: Promise<{ archived?: string }>;
}

export default async function AllChatsPage({ searchParams }: Readonly<AllChatsPageProps>) {
  const { archived: archivedRaw } = await searchParams;
  const archived = archivedRaw === 'true';
  const me = await getSession();
  if (!me) return null;
  const sessions = await listSessionsForUser(me.user.id, { archived, limit: 200 });

  return (
    <>
      <ChatTopbarStrip breadcrumbSuffix="all threads" />
      <div className="flex-1 overflow-auto p-6 max-w-[1200px] w-full mx-auto">
        <header className="space-y-1 mb-4">
          <h1 className="text-text-1 text-xl font-semibold tracking-tight">All chat threads</h1>
          <nav className="flex gap-3 text-[12px]" aria-label="Thread filter">
            <Link
              href="/chat/all"
              className={
                archived
                  ? 'text-text-3 hover:text-text-2'
                  : 'text-text-1 underline underline-offset-4'
              }
            >
              Active
            </Link>
            <Link
              href="/chat/all?archived=true"
              className={
                archived
                  ? 'text-text-1 underline underline-offset-4'
                  : 'text-text-3 hover:text-text-2'
              }
            >
              Archived
            </Link>
          </nav>
        </header>
        {sessions.length === 0 ? (
          <EmptyState
            description={archived ? 'No archived threads.' : 'No threads yet.'}
            caption={archived ? 'Archive a thread from its overflow menu.' : 'Open a new thread to get started.'}
          />
        ) : (
          <ul className="border border-border-1 rounded divide-y divide-border-1">
            {sessions.map((s) => (
              <li key={s.id}>
                <Link
                  href={`/chat/${s.id}`}
                  className="flex items-center justify-between px-4 py-2.5 text-sm hover:bg-surface-2/60 transition-colors"
                >
                  <span className="font-mono text-text-2 text-[12px]">
                    {s.id.slice(-8)}
                  </span>
                  <span className="text-text-3 text-[11px]">
                    {new Date(s.startedAt).toISOString().slice(0, 16).replace('T', ' ')}
                  </span>
                  <span className="text-text-3 text-[11px] uppercase tracking-[0.06em]">
                    {s.status}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
    </>
  );
}
