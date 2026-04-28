'use client';

import { useId, useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { initiateRotation } from '@/lib/actions/vault';
import { VaultError } from '@/lib/vault/errors';

// RotationButton — inline rotate-now affordance per FR-064 / FR-065.
// Renders three different states based on rotation_provider:
//
//  - 'manual_paste' — opens an inline modal accepting the new value;
//    on confirm, calls initiateRotation with newValue.
//  - 'infisical_native' — same modal flow at the SDK layer (per
//    plan.md note that SDK 5.0.2 doesn't expose Infisical-native
//    rotation primitives; both provider types take a value at the
//    SDK level). The provider tag drives label copy only.
//  - 'not_rotatable' — disabled button with a tooltip naming why.

export type RotationProvider = 'infisical_native' | 'manual_paste' | 'not_rotatable';

export interface RotationButtonProps {
  secretPath: string;
  rotationProvider: RotationProvider;
  variant?: 'inline' | 'pill';
}

export function RotationButton({
  secretPath,
  rotationProvider,
  variant = 'inline',
}: Readonly<RotationButtonProps>) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [newValue, setNewValue] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();
  const inputId = useId();

  if (rotationProvider === 'not_rotatable') {
    return (
      <span
        className="text-[12px] text-text-4 cursor-help"
        title="rotation_provider=not_rotatable: this secret is treated as static"
      >
        not rotatable
      </span>
    );
  }

  const className =
    variant === 'inline'
      ? 'text-[12px] text-accent hover:underline'
      : 'px-3 py-1.5 text-[12.5px] rounded bg-accent text-white hover:bg-accent/90';

  function handleRotate() {
    setError(null);
    if (newValue.length === 0) {
      setError('New value cannot be empty.');
      return;
    }
    startTransition(async () => {
      try {
        const result = await initiateRotation({ secretPath, newValue });
        if (result.status === 'completed') {
          setOpen(false);
          setNewValue('');
          router.refresh();
        } else {
          setError(
            `Rotation failed at step "${result.failedStep}": ${result.error}. Recovery: see the activity feed for the rotation_failed audit row.`,
          );
        }
      } catch (err) {
        if (err instanceof VaultError) {
          setError(`Rotation rejected: ${err.kind}`);
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  return (
    <>
      <button type="button" className={className} onClick={() => setOpen(true)}>
        Rotate now
      </button>
      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 garrison-fade-in">
          <dialog
            open
            aria-modal="true"
            aria-label="Rotate secret"
            className="static block m-0 bg-surface-1 border border-border-1 rounded shadow-lg w-full max-w-md p-5 space-y-4 text-text-1"
          >
            <h2 className="text-[16px] font-semibold text-text-1">Rotate secret</h2>
            <p className="text-[12px] text-text-3 font-mono">{secretPath}</p>
            <div className="space-y-1.5">
              <label htmlFor={inputId} className="block text-[10.5px] uppercase tracking-wide text-text-3 font-mono">
                new value
              </label>
              <textarea
                id={inputId}
                value={newValue}
                onChange={(e) => setNewValue(e.target.value)}
                rows={4}
                autoComplete="off"
                spellCheck={false}
                className="w-full font-mono text-[12.5px] bg-surface-2 border border-border-1 rounded p-3 text-text-1 focus:outline-none focus:border-accent"
              />
            </div>
            {error && (
              <div className="rounded border border-err/40 bg-err/5 px-3 py-2 text-[12px] text-err">
                {error}
              </div>
            )}
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => {
                  setOpen(false);
                  setNewValue('');
                  setError(null);
                }}
                className="px-3 py-1.5 text-[13px] text-text-2 hover:text-text-1 border border-border-1 rounded bg-surface-2 hover:bg-surface-3"
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={pending}
                onClick={handleRotate}
                className="px-3 py-1.5 text-[13px] rounded bg-accent text-white hover:bg-accent/90 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {pending ? 'Rotating…' : 'Rotate'}
              </button>
            </div>
          </dialog>
        </div>
      )}
    </>
  );
}
