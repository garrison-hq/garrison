'use client';

import { useId, useState, type ReactNode } from 'react';

// ConfirmDialog — operator-facing confirmation primitive with
// two tiers per FR-004:
//
//   tier 'single-click' — "are you sure?" → operator clicks
//                         confirm. Used for destructive but
//                         recoverable actions (rotation, agent
//                         settings save with running-instances
//                         warning).
//   tier 'typed-name' — operator types a literal string (the
//                       entity name) to enable confirm. Used for
//                       high-blast destructive actions
//                       (delete-secret, delete-grant, agent.md
//                       "clear all", multi-secret path move).
//
// Returns through `onConfirm` / `onCancel` callbacks. The parent
// owns open/close state; this component renders nothing when
// `open` is false.

export type ConfirmTier = 'single-click' | 'typed-name';

export interface ConfirmDialogProps {
  open: boolean;
  tier: ConfirmTier;
  title: string;
  /** Body copy. Can include rich content (lists of affected
   *  rows, etc.). */
  body: ReactNode;
  /** Required when tier='typed-name'. The literal value the
   *  operator must type to enable confirm. */
  confirmationName?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Visual treatment for confirm action. 'destructive' = err
   *  tone; 'primary' = accent tone. Default 'primary'. */
  intent?: 'primary' | 'destructive';
  onConfirm: () => void;
  onCancel: () => void;
}

/** Pure helper: typed-name input matches the expected name? */
export function matchesConfirmation(input: string, expected: string): boolean {
  return input === expected;
}

export function ConfirmDialog({
  open,
  tier,
  title,
  body,
  confirmationName,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  intent = 'primary',
  onConfirm,
  onCancel,
}: Readonly<ConfirmDialogProps>) {
  const [typed, setTyped] = useState('');
  const headingId = useId();
  const bodyId = useId();

  if (!open) return null;

  const isTypedTier = tier === 'typed-name';
  const canConfirm =
    !isTypedTier || (confirmationName !== undefined && matchesConfirmation(typed, confirmationName));

  const intentClass =
    intent === 'destructive'
      ? 'bg-err text-white hover:bg-err/90'
      : 'bg-accent text-white hover:bg-accent/90';

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={headingId}
      aria-describedby={bodyId}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 garrison-fade-in"
    >
      <div className="bg-surface-1 border border-border-1 rounded shadow-lg w-full max-w-md p-5">
        <h2 id={headingId} className="text-[16px] font-semibold text-text-1">
          {title}
        </h2>
        <div id={bodyId} className="mt-3 text-[13px] text-text-2 leading-relaxed">
          {body}
        </div>
        {isTypedTier && confirmationName !== undefined && (
          <div className="mt-4">
            <label className="block text-[11px] uppercase tracking-wide text-text-3 mb-1.5">
              Type{' '}
              <span className="font-mono text-text-1 normal-case tracking-normal">
                {confirmationName}
              </span>{' '}
              to confirm
            </label>
            <input
              type="text"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              autoCorrect="off"
              spellCheck={false}
              className="w-full bg-surface-2 border border-border-1 rounded px-2 py-1.5 text-[13px] text-text-1 font-mono focus:outline-none focus:border-accent"
            />
          </div>
        )}
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            disabled={!canConfirm}
            onClick={onConfirm}
            className={`px-3 py-1.5 text-[13px] rounded ${intentClass} disabled:opacity-50 disabled:cursor-not-allowed`}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
