'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import type { InviteRow } from '@/lib/auth/invites';

interface Props {
  invites: InviteRow[];
  baseUrl: string;
}

// Renders the pending-invites table. Each row shows the share-able
// link, the expiration timestamp, and a revoke button. Revoke calls
// the API and refreshes the page.
export function PendingInvitesList({ invites, baseUrl }: Readonly<Props>) {
  const t = useTranslations('auth.admin');
  const [busyId, setBusyId] = useState<string | null>(null);

  async function revoke(id: string) {
    setBusyId(id);
    try {
      await fetch(`/api/invites/${id}/revoke`, {
        method: 'POST',
        credentials: 'include',
      });
      // Force a server-component refresh of the parent route so the
      // newly-revoked invite drops out of the list.
      globalThis.location.reload();
    } finally {
      setBusyId(null);
    }
  }

  if (invites.length === 0) {
    return <p className="text-text-3 text-xs">{t('noPending')}</p>;
  }

  return (
    <ul className="divide-y divide-border-1 border border-border-1 rounded">
      {invites.map((inv) => {
        const link = `${baseUrl}/invite/${inv.token}`;
        return (
          <li key={inv.id} className="p-3 flex items-center gap-3 text-sm">
            <code className="font-mono text-xs text-text-2 truncate flex-1" data-testid="invite-link">
              {link}
            </code>
            <span className="text-text-3 text-xs whitespace-nowrap">
              {t('expires', { when: new Date(inv.expiresAt).toISOString().slice(0, 16) + 'Z' })}
            </span>
            <button
              type="button"
              onClick={() => revoke(inv.id)}
              disabled={busyId === inv.id}
              className="text-err text-xs px-2 py-1 border border-border-1 rounded disabled:opacity-60"
              data-testid="revoke-invite"
            >
              {busyId === inv.id ? t('revoking') : t('revoke')}
            </button>
          </li>
        );
      })}
    </ul>
  );
}
