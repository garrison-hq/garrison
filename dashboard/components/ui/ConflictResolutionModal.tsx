'use client';

import { useId, type ReactNode } from 'react';

// ConflictResolutionModal — surfaces an optimistic-locking
// conflict (lib/locks/version.ts:checkAndUpdate returning
// { accepted: false, serverState }). Operator picks one of
// three actions per FR-101 / plan §"Optimistic-locking conflict
// resolution lifecycle":
//
//  - overwrite: re-submit with the new versionToken from the
//               serverState (caller wires this).
//  - merge-manually: load server version into the form for
//                    manual reconciliation; operator submits
//                    again with the merged content.
//  - discard: close the form without saving.

export interface ConflictResolutionModalProps {
  open: boolean;
  /** Heading copy explaining what conflicted. e.g. "Another
   *  operator changed this secret since you opened the form." */
  title: string;
  /** Side-by-side diff comparing the operator's draft vs. the
   *  current server version. Caller passes a rendered DiffView
   *  (or any ReactNode if richer rendering is needed). */
  diff: ReactNode;
  onOverwrite: () => void;
  onMergeManually: () => void;
  onDiscard: () => void;
}

export function ConflictResolutionModal({
  open,
  title,
  diff,
  onOverwrite,
  onMergeManually,
  onDiscard,
}: ConflictResolutionModalProps) {
  const headingId = useId();
  const diffId = useId();

  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={headingId}
      aria-describedby={diffId}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 garrison-fade-in"
    >
      <div className="bg-surface-1 border border-border-1 rounded shadow-lg w-full max-w-2xl p-5">
        <h2 id={headingId} className="text-[16px] font-semibold text-text-1">
          {title}
        </h2>
        <p className="mt-2 text-[12px] text-text-3">
          Choose to overwrite the server version, merge manually, or discard your changes.
        </p>
        <div id={diffId} className="mt-4 max-h-[60vh] overflow-auto bg-surface-2 rounded border border-border-1 p-3">
          {diff}
        </div>
        <div className="mt-5 flex flex-wrap justify-end gap-2">
          <button
            type="button"
            onClick={onDiscard}
            className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
          >
            Discard my changes
          </button>
          <button
            type="button"
            onClick={onMergeManually}
            className="px-3 py-1.5 text-[13px] text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
          >
            Merge manually
          </button>
          <button
            type="button"
            onClick={onOverwrite}
            className="px-3 py-1.5 text-[13px] rounded bg-warn text-white hover:bg-warn/90"
          >
            Overwrite server version
          </button>
        </div>
      </div>
    </div>
  );
}
