'use client';

// M5.2 — chat-only topbar strip (plan §1.1, resolved Q2: dual-strip
// posture). Rendered above ChatShell; carries the breadcrumb (left),
// IdlePill slot (center-right), and "+ New thread" button (right).
//
// Per resolved Q2 the global Topbar.tsx is NOT modified — chat affords
// itself to its own strip. The strip lives at the top of the center
// pane so the right-pane KnowsPanePlaceholder isn't pushed below it.

import { useTransition } from 'react';
import { useRouter } from 'next/navigation';
import type { ReactNode } from 'react';
import { createEmptyChatSession } from '@/lib/actions/chat';

interface ChatTopbarStripProps {
  /** Optional breadcrumb suffix — e.g. the active thread number. */
  breadcrumbSuffix?: ReactNode;
  /** Slot for IdlePill so the layout doesn't depend on this component
   *  knowing the live session row shape. */
  idlePill?: ReactNode;
}

export function ChatTopbarStrip({ breadcrumbSuffix, idlePill }: Readonly<ChatTopbarStripProps>) {
  const router = useRouter();
  const [isPending, startTransition] = useTransition();

  const handleNewThread = () => {
    startTransition(async () => {
      const { sessionId } = await createEmptyChatSession();
      router.push(`/chat/${sessionId}`);
    });
  };

  return (
    <div className="border-b border-border-1 bg-surface-1 px-4 py-2 flex items-center gap-3 text-sm min-w-0" data-testid="chat-topbar-strip">
      <nav aria-label="Chat breadcrumb" className="flex items-center gap-1.5 text-text-3 text-[12px] min-w-0 truncate">
        <span className="shrink-0">CEO chat</span>
        {breadcrumbSuffix ? (
          <>
            <span className="text-text-4 shrink-0">/</span>
            <span className="text-text-2 font-mono truncate">{breadcrumbSuffix}</span>
          </>
        ) : null}
      </nav>
      <div className="flex-1" />
      {idlePill ? <div className="text-[11px] shrink-0">{idlePill}</div> : null}
      <button
        type="button"
        onClick={handleNewThread}
        disabled={isPending}
        className="px-3 py-1.5 text-[12px] font-medium border border-border-2 rounded bg-surface-2 hover:bg-surface-3 disabled:opacity-60 disabled:cursor-not-allowed transition-colors shrink-0 whitespace-nowrap"
        data-testid="chat-new-thread"
      >
        + New thread
      </button>
    </div>
  );
}
