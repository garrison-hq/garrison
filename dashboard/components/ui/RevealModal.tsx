'use client';

import { useEffect, useId, useState } from 'react';

// RevealModal — secret reveal flow per FR-070 / plan §"Secret
// reveal modal lifecycle":
//
//  state: idle (operator clicks reveal button)
//    → confirm-prompt (single-click confirm in this modal)
//    → fetching-value (server action revealSecret() in flight)
//    → rendered (value visible, 30s auto-hide timer starts)
//    → hidden (auto-hide elapsed OR operator closes modal)
//      → idle
//
// The 30s timer is visible to the operator via a countdown
// displayed next to the value. On modal close the value is
// purged from the React tree immediately.
//
// The audit row (vault_access_log outcome='value_revealed') is
// written by the server-side revealSecret action passed in via
// `fetcher`, NOT by this component. The component just renders
// the modal lifecycle around the fetch.

export const REVEAL_AUTO_HIDE_SECONDS = 30;

export interface RevealModalProps {
  open: boolean;
  /** The secret path the operator is revealing — shown in the
   *  modal heading; the VALUE is never embedded here, only
   *  fetched on confirm. */
  secretPath: string;
  /** Server action: reveals the value, writes the audit row,
   *  returns { value, revealedAt }. Errors propagate to the
   *  parent's onError. */
  fetcher: () => Promise<{ value: string; revealedAt: string }>;
  /** Called when the modal closes (operator action OR auto-hide). */
  onClose: () => void;
  /** Called when fetcher throws. */
  onError?: (err: unknown) => void;
}

type Phase =
  | { kind: 'confirm-prompt' }
  | { kind: 'fetching-value' }
  | { kind: 'rendered'; value: string; expiresAt: number }
  | { kind: 'error'; message: string };

export function RevealModal({ open, secretPath, fetcher, onClose, onError }: Readonly<RevealModalProps>) {
  const [phase, setPhase] = useState<Phase>({ kind: 'confirm-prompt' });
  const [secondsRemaining, setSecondsRemaining] = useState<number>(REVEAL_AUTO_HIDE_SECONDS);
  const headingId = useId();

  // Reset phase when the modal opens.
  useEffect(() => {
    if (open) {
      setPhase({ kind: 'confirm-prompt' });
      setSecondsRemaining(REVEAL_AUTO_HIDE_SECONDS);
    }
  }, [open]);

  // Auto-hide tick: while phase is 'rendered', count down each
  // second and close at zero.
  useEffect(() => {
    if (phase.kind !== 'rendered') return;
    const interval = setInterval(() => {
      const remaining = Math.max(0, Math.ceil((phase.expiresAt - Date.now()) / 1000));
      setSecondsRemaining(remaining);
      if (remaining <= 0) {
        clearInterval(interval);
        onClose();
      }
    }, 250);
    return () => clearInterval(interval);
  }, [phase, onClose]);

  if (!open) return null;

  async function handleConfirm() {
    setPhase({ kind: 'fetching-value' });
    try {
      const { value } = await fetcher();
      setPhase({
        kind: 'rendered',
        value,
        expiresAt: Date.now() + REVEAL_AUTO_HIDE_SECONDS * 1000,
      });
      setSecondsRemaining(REVEAL_AUTO_HIDE_SECONDS);
    } catch (err) {
      onError?.(err);
      setPhase({ kind: 'error', message: err instanceof Error ? err.message : String(err) });
    }
  }

  async function handleCopy(value: string) {
    if (typeof navigator !== 'undefined' && navigator.clipboard) {
      await navigator.clipboard.writeText(value);
    }
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={headingId}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 garrison-fade-in"
    >
      <div className="bg-surface-1 border border-border-1 rounded shadow-lg w-full max-w-lg p-5">
        <h2 id={headingId} className="text-[16px] font-semibold text-text-1">
          Reveal secret value
        </h2>
        <p className="mt-1 text-[12px] text-text-3 font-mono">{secretPath}</p>

        {phase.kind === 'confirm-prompt' && (
          <>
            <p className="mt-4 text-[13px] text-text-2 leading-relaxed">
              The value will be visible for {REVEAL_AUTO_HIDE_SECONDS} seconds, then auto-hide. This
              reveal will be recorded in the vault audit log.
            </p>
            <div className="mt-5 flex justify-end gap-2">
              <button
                type="button"
                onClick={onClose}
                className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={handleConfirm}
                className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90"
              >
                Reveal value
              </button>
            </div>
          </>
        )}

        {phase.kind === 'fetching-value' && (
          <p className="mt-4 text-[13px] text-text-3 italic">fetching value…</p>
        )}

        {phase.kind === 'rendered' && (
          <>
            <div className="mt-4 bg-surface-2 border border-border-1 rounded p-3">
              <pre className="font-mono text-[12px] text-text-1 break-all whitespace-pre-wrap">
                {phase.value}
              </pre>
            </div>
            <div className="mt-3 flex items-center justify-between">
              <span className="text-[11px] uppercase tracking-wide text-text-3">
                auto-hide in {secondsRemaining}s
              </span>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={() => handleCopy(phase.value)}
                  className="px-3 py-1.5 text-[13px] text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
                >
                  Copy
                </button>
                <button
                  type="button"
                  onClick={onClose}
                  className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90"
                >
                  Close
                </button>
              </div>
            </div>
          </>
        )}

        {phase.kind === 'error' && (
          <>
            <p className="mt-4 text-[13px] text-err">{phase.message}</p>
            <div className="mt-5 flex justify-end gap-2">
              <button
                type="button"
                onClick={onClose}
                className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
              >
                Close
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
