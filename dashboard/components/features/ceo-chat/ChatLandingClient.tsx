'use client';

// M5.2 — client wrapper that wires the NoThreadsEverEmptyState's
// onCreate handler to createEmptyChatSession + router.push. Lives in
// a separate component so the parent /chat page can stay a Server
// Component (auth-gated, force-dynamic).

import { useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { NoThreadsEverEmptyState } from './EmptyStates';
import { createEmptyChatSession } from '@/lib/actions/chat';

export function ChatLandingClient() {
  const router = useRouter();
  const [isPending, startTransition] = useTransition();
  const handleCreate = () => {
    startTransition(async () => {
      const { sessionId } = await createEmptyChatSession();
      router.push(`/chat/${sessionId}`);
    });
  };
  return (
    <div data-testid="chat-landing-client" data-pending={isPending}>
      <NoThreadsEverEmptyState onCreate={handleCreate} />
    </div>
  );
}
