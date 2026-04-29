'use client';

// M5.2 — chat empty-state primitives (plan §1.15).
//
// Three named exports per spec:
//   NoThreadsEverEmptyState({onCreate})  — first-time operator
//   EmptyCurrentThreadHint()             — just clicked + New thread
//   EndedThreadEmptyState({onCreateNew}) — session.status='ended'
//
// Composed on top of the M3 EmptyState primitive. Copy is verbatim
// from plan §1.15 — no localisation in M5.2 per slate item 42 (English-
// only literal strings; future polish migrates to messages/en.json).

import { EmptyState } from '@/components/ui/EmptyState';
import { ChatIcon } from '@/components/ui/icons';

export function NoThreadsEverEmptyState({ onCreate }: Readonly<{ onCreate: () => void }>) {
  return (
    <div className="flex flex-col items-center gap-4 py-10" data-testid="empty-state-no-threads">
      <EmptyState
        icon={<ChatIcon />}
        description="Start a thread with the CEO"
        caption="Ask anything — the CEO summons fresh on every message."
      />
      <button
        type="button"
        onClick={onCreate}
        className="px-3 py-1.5 text-[12px] font-medium border border-border-2 rounded bg-accent/90 text-white hover:bg-accent"
      >
        + New thread
      </button>
    </div>
  );
}

export function EmptyCurrentThreadHint() {
  return (
    <p className="text-text-3 text-sm pb-3" data-testid="empty-current-thread-hint">
      Ask the CEO anything
    </p>
  );
}

export function EndedThreadEmptyState({ onCreateNew }: Readonly<{ onCreateNew: () => void }>) {
  return (
    <div className="flex flex-col items-center gap-4 py-10" data-testid="empty-state-ended">
      <EmptyState
        description="This thread is ended."
        caption="Start a new one to keep talking."
      />
      <button
        type="button"
        onClick={onCreateNew}
        className="px-3 py-1.5 text-[12px] font-medium border border-border-2 rounded bg-surface-2 hover:bg-surface-3"
      >
        Start a new thread
      </button>
    </div>
  );
}
