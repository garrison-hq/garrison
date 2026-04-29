'use client';

// M5.2 — client wrapper that wires the NoThreadsEverEmptyState's
// onCreate handler to createEmptyChatSession + router.push. Lives in
// a separate component so the parent /chat page can stay a Server
// Component (auth-gated, force-dynamic).
//
// Two render shapes:
//   default     — full empty state with green "+ New thread" CTA.
//                 Used on the no-threads-ever path.
//   compact     — just the green button. Used inline on the thread
//                 list header so the operator can start a new thread
//                 without leaving the listing.

import { useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { NoThreadsEverEmptyState } from './EmptyStates';
import { createEmptyChatSession } from '@/lib/actions/chat';

export function ChatLandingClient({ compact = false }: Readonly<{ compact?: boolean }>) {
  const router = useRouter();
  const [isPending, startTransition] = useTransition();
  const handleCreate = () => {
    startTransition(async () => {
      const { sessionId } = await createEmptyChatSession();
      router.push(`/chat/${sessionId}`);
    });
  };
  if (compact) {
    return (
      <button
        type="button"
        onClick={handleCreate}
        disabled={isPending}
        className="px-3 py-1.5 text-[12px] font-medium border border-border-2 rounded bg-accent/90 text-white hover:bg-accent disabled:opacity-60"
        data-testid="chat-landing-new-thread"
        data-pending={isPending}
      >
        + New thread
      </button>
    );
  }
  return (
    <div data-testid="chat-landing-client" data-pending={isPending}>
      <NoThreadsEverEmptyState onCreate={handleCreate} />
    </div>
  );
}
