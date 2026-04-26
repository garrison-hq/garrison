'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';

// One-button "generate invite" form. Posts to /api/invites and
// reloads the parent page so the new invite appears in the list.
export function GenerateInviteForm() {
  const t = useTranslations('auth.admin');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setPending(true);
    setError(null);
    try {
      const res = await fetch('/api/invites', {
        method: 'POST',
        credentials: 'include',
      });
      if (!res.ok) {
        throw new Error(`generate failed (HTTP ${res.status})`);
      }
      window.location.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-2">
      <button
        type="submit"
        disabled={pending}
        className="bg-accent text-bg rounded px-3 py-1.5 text-sm font-medium disabled:opacity-60"
        data-testid="generate-invite"
      >
        {pending ? t('generating') : t('generate')}
      </button>
      {error ? <p className="text-err text-xs">{error}</p> : null}
    </form>
  );
}
