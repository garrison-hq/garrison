'use client';

// Recent-threads block — bottom-anchored region of the right-pane
// KnowsPane on chat surfaces. Replaces the M5.2 left-sidebar
// ThreadHistorySubnav so all chat-related navigation lives in one
// pane: KnowsPane tabs above, thread switcher below.
//
// The active row (the thread the operator is currently viewing) is
// highlighted with a surface-2 background; usePathname picks up the
// active /chat/<id> segment client-side so the highlight tracks
// route changes without a server round-trip.

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { relativeTime } from '@/lib/format/relativeTime';

interface ThreadRow {
  id: string;
  threadNumber: number;
  startedAt: string;
}

interface Props {
  threads: ThreadRow[];
}

export function RecentThreadsBlock({ threads }: Readonly<Props>) {
  const pathname = usePathname();

  return (
    <section
      className="border-t border-border-1 bg-surface-1 flex flex-col min-h-0 shrink-0"
      style={{ height: 224 }}
      aria-label="Recent chat threads"
      data-testid="recent-threads-block"
    >
      <header className="px-3 py-2 flex items-center justify-between">
        <span className="text-[10px] uppercase tracking-[0.06em] font-medium text-text-3">
          recent threads
        </span>
        <span className="font-mono font-tabular text-[10px] text-text-4">
          {threads.length}
        </span>
      </header>
      <div className="flex-1 min-h-0 overflow-auto px-2">
        {threads.length === 0 ? (
          <p className="px-1 py-1 text-[11px] text-text-4 italic">No threads yet.</p>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {threads.map((t) => {
              const active = pathname?.endsWith(`/chat/${t.id}`) ?? false;
              return (
                <li key={t.id}>
                  <Link
                    href={`/chat/${t.id}`}
                    className={`flex items-center gap-2 px-2 py-1 rounded text-[12px] ${
                      active
                        ? 'bg-surface-2 text-text-1'
                        : 'text-text-2 hover:bg-surface-2/60 hover:text-text-1'
                    }`}
                    data-testid="thread-history-row"
                    data-active={active ? 'true' : 'false'}
                  >
                    <span className="font-mono text-text-3 text-[11px] w-9 shrink-0">
                      #{t.threadNumber}
                    </span>
                    <span className="flex-1 truncate">thread #{t.threadNumber}</span>
                    <span
                      className="font-mono text-[10.5px] text-text-3 shrink-0"
                      title={t.startedAt}
                      suppressHydrationWarning
                    >
                      {relativeTime(t.startedAt)}
                    </span>
                  </Link>
                </li>
              );
            })}
          </ul>
        )}
      </div>
      <Link
        href="/chat/all"
        className="px-3 py-1.5 text-[10.5px] text-text-3 hover:text-text-1 underline-offset-4 hover:underline"
      >
        view all threads →
      </Link>
    </section>
  );
}
