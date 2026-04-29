'use client';

// M5.2 — sidebar thread-history subnav (plan §1.2).
//
// Collapsible list of the operator's most recent active threads.
// Uses native <details>/<summary> for built-in keyboard support
// (Enter/Space toggles, Tab traverses, Enter activates a row).
// On <768px the subnav inherits the existing M3 sidebar collapse
// pattern.
//
// Server-rendered initial state — the parent passes the seeded
// thread list from getRecentThreadsForCurrentUser(); this component
// just renders. Live-updates on route navigation are out of scope
// for M5.2 (subnav refetches on next load; SSE-driven refresh is
// future polish).

import Link from 'next/link';

interface ThreadRow {
  id: string;
  threadNumber: number;
}

interface ThreadHistorySubnavProps {
  threads: ThreadRow[];
}

export function ThreadHistorySubnav({ threads }: Readonly<ThreadHistorySubnavProps>) {
  return (
    <details
      className="px-2 mt-0.5 text-text-3 text-xs"
      open
      data-testid="thread-history-subnav"
    >
      <summary className="cursor-pointer select-none px-3 py-1 flex items-center justify-between text-[10px] uppercase tracking-wider font-medium hover:text-text-2">
        <span>recent threads</span>
        <span className="font-mono font-tabular text-text-4">{threads.length}</span>
      </summary>
      {threads.length === 0 ? (
        <p className="pl-9 pr-3 py-1 text-[11px] text-text-4 italic">No threads yet.</p>
      ) : (
        <ul className="flex flex-col gap-0.5 mt-0.5">
          {threads.map((t) => (
            <li key={t.id}>
              <Link
                href={`/chat/${t.id}`}
                className="block pl-9 pr-3 py-1 rounded text-text-2 hover:bg-surface-2 hover:text-text-1 text-[11.5px] font-mono"
                data-testid="thread-history-row"
              >
                thread #{t.threadNumber}
              </Link>
            </li>
          ))}
        </ul>
      )}
      <Link
        href="/chat/all"
        className="block pl-9 pr-3 py-1 mt-0.5 text-[10.5px] text-text-3 hover:text-text-1 underline-offset-4 hover:underline"
      >
        view all threads
      </Link>
    </details>
  );
}
