// M5.2 — three-pane chat shell (plan §1.1). Composes the center pane
// (children) alongside the right-pane KnowsPane (M5.4). The left
// rail is the global Sidebar from (app)/layout.tsx — ChatShell does
// not render it. The right pane collapses below 1024px per FR-202;
// CSS handles the breakpoint so SSR + CSR layouts agree without a
// JS hydration delay.
//
// Recent-threads list lives at the bottom of the right pane (was the
// left-sidebar subnav at M5.2; relocated for M5.4 so all chat
// navigation stays in one column). Fetched server-side here and
// passed as a serializable prop into KnowsPane.

import type { ReactNode } from 'react';
import { getRecentThreadsForCurrentUser } from '@/lib/actions/chat';
import { getSession } from '@/lib/auth/session';
import { KnowsPane } from './KnowsPane';

export async function ChatShell({ children }: Readonly<{ children: ReactNode }>) {
  // Soft-fail to [] (matches the prior Sidebar behaviour) so the shell
  // still mounts on environments where the chat schema migration hasn't
  // run yet. The KnowsPane block renders the empty-state copy.
  const session = await getSession();
  const recentThreads = session
    ? await getRecentThreadsForCurrentUser(10).catch(() => [])
    : [];
  const threads = recentThreads.map((r) => ({ id: r.id, threadNumber: r.threadNumber }));

  // (app) layout's <main> is now a flex-col container with min-h-0 and
  // overflow-auto, so flex-1 here resolves cleanly: the shell consumes
  // whatever vertical space is left under the global topbar, and the
  // inner section uses overflow-hidden so MessageStream is the only
  // scrolling surface — the page itself never grows past the viewport.
  return (
    <div className="flex flex-1 min-h-0 min-w-0 overflow-x-hidden">
      <section className="flex-1 flex flex-col min-w-0 overflow-hidden">{children}</section>
      <KnowsPane threads={threads} />
    </div>
  );
}
