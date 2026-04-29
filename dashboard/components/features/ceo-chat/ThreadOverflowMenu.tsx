'use client';

// M5.2 — ThreadOverflowMenu (plan §1.16, FR-211).
//
// Items are pinned per FR-211:
//   Archive thread     — always visible
//   Unarchive thread   — visible only when is_archived=true
//   End thread         — visible only when status='active'
//   Delete thread      — always visible
//
// "End thread" and "Delete thread" open a single-click ConfirmDialog
// per FR-237 + FR-245. No "Rename" item in v1 (slate item 28; plan
// §1.11). Each menu item triggers the corresponding T003 server
// action; the parent re-fetches via Next router refresh after the
// action resolves so the new state lands on the next render.

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import {
  endChatSession,
  archiveChatSession,
  unarchiveChatSession,
  deleteChatSession,
} from '@/lib/actions/chat';

interface ThreadOverflowMenuProps {
  sessionId: string;
  status: 'active' | 'ended' | 'aborted' | string;
  isArchived: boolean;
}

type DialogKind = 'end' | 'delete' | null;

export function ThreadOverflowMenu({
  sessionId,
  status,
  isArchived,
}: Readonly<ThreadOverflowMenuProps>) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [dialog, setDialog] = useState<DialogKind>(null);
  const [, startTransition] = useTransition();

  const close = () => setOpen(false);

  const handleArchive = () => {
    close();
    startTransition(async () => {
      await archiveChatSession(sessionId);
      router.refresh();
    });
  };

  const handleUnarchive = () => {
    close();
    startTransition(async () => {
      await unarchiveChatSession(sessionId);
      router.refresh();
    });
  };

  const handleEndConfirm = () => {
    setDialog(null);
    startTransition(async () => {
      await endChatSession(sessionId);
      router.refresh();
    });
  };

  const handleDeleteConfirm = () => {
    setDialog(null);
    startTransition(async () => {
      await deleteChatSession(sessionId);
      router.push('/chat');
    });
  };

  const isActive = status === 'active';

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="px-2 py-1 text-text-3 hover:text-text-1 hover:bg-surface-2 rounded text-[12px]"
        aria-haspopup="menu"
        aria-expanded={open}
        data-testid="thread-overflow-trigger"
      >
        ⋯
      </button>
      {open ? (
        <div
          role="menu"
          className="absolute right-0 top-full mt-1 z-10 bg-surface-1 border border-border-1 rounded shadow-md min-w-[160px] py-1"
          data-testid="thread-overflow-menu"
        >
          {isArchived ? (
            <MenuItem onSelect={handleUnarchive} testId="overflow-unarchive">
              Unarchive thread
            </MenuItem>
          ) : (
            <MenuItem onSelect={handleArchive} testId="overflow-archive">
              Archive thread
            </MenuItem>
          )}
          {isActive ? (
            <MenuItem onSelect={() => { close(); setDialog('end'); }} testId="overflow-end">
              End thread
            </MenuItem>
          ) : null}
          <MenuItem onSelect={() => { close(); setDialog('delete'); }} testId="overflow-delete">
            Delete thread
          </MenuItem>
        </div>
      ) : null}
      <ConfirmDialog
        open={dialog === 'end'}
        tier="single-click"
        title="End thread?"
        body={
          <p className="text-text-2 text-sm">
            Ending this thread closes it for further messages. The transcript stays available.
          </p>
        }
        confirmLabel="End thread"
        intent="primary"
        onConfirm={handleEndConfirm}
        onCancel={() => setDialog(null)}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        tier="single-click"
        title="Delete thread?"
        body={
          <p className="text-text-2 text-sm">
            Deletes this thread and its transcript. This action cannot be undone.
          </p>
        }
        confirmLabel="Delete thread"
        intent="destructive"
        onConfirm={handleDeleteConfirm}
        onCancel={() => setDialog(null)}
      />
    </div>
  );
}

function MenuItem({
  onSelect,
  testId,
  children,
}: Readonly<{ onSelect: () => void; testId: string; children: React.ReactNode }>) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onSelect}
      className="w-full text-left px-3 py-1.5 text-[13px] text-text-2 hover:bg-surface-2 hover:text-text-1"
      data-testid={testId}
    >
      {children}
    </button>
  );
}
