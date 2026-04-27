'use client';

import { useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { revealSecret, deleteSecret } from '@/lib/actions/vault';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { RevealModal } from '@/components/ui/RevealModal';
import { VaultError } from '@/lib/vault/errors';

// SecretRowActions — per-row action surface for reveal + delete.
// Lives as a sibling to SecretsList's path/roles/rotation columns;
// rendered as the rightmost column.
//
// Reveal flow uses the M4 RevealModal primitive (T005) wrapped
// around the revealSecret server action — modal handles the
// confirm-prompt → fetching → rendered → auto-hide lifecycle and
// the value-purge-on-close per FR-070.
//
// Delete flow uses ConfirmDialog with tier='typed-name' per
// FR-059, the deleteSecret server action enforces the same
// invariant server-side as defense-in-depth.

export interface SecretRowActionsProps {
  secretPath: string;
}

export function SecretRowActions({ secretPath }: SecretRowActionsProps) {
  const router = useRouter();
  const [revealOpen, setRevealOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  function handleRevealFetch() {
    return revealSecret({ secretPath });
  }

  function handleDeleteConfirm() {
    setError(null);
    setDeleteOpen(false);
    startTransition(async () => {
      try {
        await deleteSecret({ secretPath, confirmationName: secretPath });
        router.refresh();
      } catch (err) {
        if (err instanceof VaultError) {
          const detail = (err.detail?.roleSlugs as string[] | undefined)?.join(', ');
          if (detail) {
            setError(
              `Delete rejected: secret is granted to roles: ${detail}. Remove the grants first.`,
            );
          } else {
            setError(`Delete rejected: ${err.kind}`);
          }
        } else {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      }
    });
  }

  return (
    <span className="inline-flex items-center gap-2">
      <button
        type="button"
        onClick={() => setRevealOpen(true)}
        disabled={pending}
        className="text-[12px] text-accent hover:underline disabled:opacity-50"
      >
        Reveal
      </button>
      <button
        type="button"
        onClick={() => setDeleteOpen(true)}
        disabled={pending}
        className="text-[12px] text-err hover:underline disabled:opacity-50"
      >
        Delete
      </button>

      {error && (
        <span
          className="text-[11px] text-err truncate max-w-[280px]"
          title={error}
        >
          {error}
        </span>
      )}

      <RevealModal
        open={revealOpen}
        secretPath={secretPath}
        fetcher={handleRevealFetch}
        onClose={() => setRevealOpen(false)}
        onError={(err) => {
          setRevealOpen(false);
          setError(err instanceof Error ? err.message : 'reveal failed');
        }}
      />

      <ConfirmDialog
        open={deleteOpen}
        tier="typed-name"
        intent="destructive"
        title="Delete secret"
        body={
          <span>
            Type the secret path below to confirm deletion. The secret will be
            removed from Infisical and from <code className="font-mono">secret_metadata</code>;
            this is not reversible from the dashboard.
          </span>
        }
        confirmationName={secretPath}
        confirmLabel="Delete"
        onConfirm={handleDeleteConfirm}
        onCancel={() => setDeleteOpen(false)}
      />
    </span>
  );
}
