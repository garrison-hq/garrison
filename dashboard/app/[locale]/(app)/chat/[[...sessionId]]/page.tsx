import { notFound } from 'next/navigation';
import { ChatTopbarStrip } from '@/components/features/ceo-chat/ChatTopbarStrip';
import { EmptyState } from '@/components/ui/EmptyState';
import { getSessionWithMessages } from '@/lib/queries/chat';
import { getSession } from '@/lib/auth/session';

// M5.2 — /chat/<sessionId>. Server-renders the initial transcript +
// hands off the live stream to the client-side useChatStream hook (T009
// MessageStream / T010 Composer wire those in). T006 ships the route
// shell only — the per-pane components plug in via T008–T011.

export const dynamic = 'force-dynamic';

export default async function ChatSessionPage({
  params,
}: Readonly<{ params: Promise<{ sessionId?: string[] }> }>) {
  const { sessionId: sessionIdParts } = await params;
  const sessionId = Array.isArray(sessionIdParts) ? sessionIdParts[0] : undefined;

  if (!sessionId) {
    return (
      <>
        <ChatTopbarStrip />
        <div className="flex-1 flex items-center justify-center p-8">
          <EmptyState description="Pick or create a thread." />
        </div>
      </>
    );
  }

  const me = await getSession();
  if (!me) return notFound();

  const detail = await getSessionWithMessages(sessionId);
  if (!detail) return notFound();

  // Defensive ownership check — server actions enforce this for writes;
  // we mirror it on reads so a guessed UUID can't sniff transcripts.
  if (detail.session.startedByUserId !== me.user.id) return notFound();

  // Stub thread number — T008 wires the per-operator counter via
  // ROW_NUMBER() OVER (PARTITION BY ...). Until then, the breadcrumb
  // shows the short uuid suffix so the layout reads as live.
  const breadcrumbSuffix = `thread ${detail.session.id.slice(-6)}`;

  return (
    <>
      <ChatTopbarStrip breadcrumbSuffix={breadcrumbSuffix} />
      <div className="flex-1 flex flex-col min-h-0 p-6 gap-3">
        <header className="text-text-2 text-sm" data-testid="chat-thread-header-stub">
          {breadcrumbSuffix}
        </header>
        <section
          className="flex-1 overflow-auto border border-border-1 rounded p-4 text-text-3 text-sm"
          data-testid="chat-message-stream-stub"
        >
          {detail.messages.length === 0 ? (
            <EmptyState description="Ask the CEO anything" />
          ) : (
            <ul className="space-y-2 font-mono text-[12px]">
              {detail.messages.map((m) => (
                <li key={m.id} className="text-text-2">
                  <span className="text-text-4">[{m.role}]</span> {m.content ?? ''}
                </li>
              ))}
            </ul>
          )}
        </section>
        <footer
          className="border border-border-1 rounded p-3 text-text-4 text-xs"
          data-testid="chat-composer-stub"
        >
          Composer lands in T010 — ⌘↵ to send.
        </footer>
      </div>
    </>
  );
}
