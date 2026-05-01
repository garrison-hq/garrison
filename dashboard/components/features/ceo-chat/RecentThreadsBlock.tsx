// Recent-threads block — bottom-anchored region of the right-pane
// KnowsPane on chat surfaces. Replaces the M5.2 left-sidebar
// ThreadHistorySubnav so all chat-related navigation lives in one
// pane: KnowsPane tabs above, thread switcher below.
//
// Server-renderable: receives the seeded thread list from ChatShell
// and renders static markup. Live-updates on next route change
// (same cadence as the prior subnav).

import Link from 'next/link';

interface ThreadRow {
  id: string;
  threadNumber: number;
}

interface Props {
  threads: ThreadRow[];
}

export function RecentThreadsBlock({ threads }: Readonly<Props>) {
  return (
    <section
      className="border-t border-border-1 bg-surface-1 flex flex-col min-h-0 shrink-0"
      style={{ height: 224 }}
      aria-label="Recent chat threads"
      data-testid="recent-threads-block"
    >
      <header className="px-3 py-2 flex items-center justify-between border-b border-border-1">
        <span className="text-[10px] uppercase tracking-[0.06em] font-medium text-text-3">
          recent threads
        </span>
        <span className="font-mono font-tabular text-[10px] text-text-4">
          {threads.length}
        </span>
      </header>
      <div className="flex-1 min-h-0 overflow-auto">
        {threads.length === 0 ? (
          <p className="px-3 py-2 text-[11px] text-text-4 italic">No threads yet.</p>
        ) : (
          <ul className="flex flex-col py-1">
            {threads.map((t) => (
              <li key={t.id}>
                <Link
                  href={`/chat/${t.id}`}
                  className="block px-3 py-1 text-[11.5px] font-mono text-text-2 hover:bg-surface-2 hover:text-text-1"
                  data-testid="thread-history-row"
                >
                  thread #{t.threadNumber}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
      <Link
        href="/chat/all"
        className="px-3 py-1.5 text-[10.5px] text-text-3 hover:text-text-1 underline-offset-4 hover:underline border-t border-border-1"
      >
        view all threads
      </Link>
    </section>
  );
}
