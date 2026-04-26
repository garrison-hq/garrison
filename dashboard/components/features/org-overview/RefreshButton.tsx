'use client';

import { useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';

// Manual refresh — bumps the Next router's cache so the
// Server Component re-runs its DB queries. Doesn't reuse TanStack
// Query because the org-overview surface is rendered server-side
// (T010 keeps it as Server Component); for purely client-side
// surfaces, the manual refresh path goes through query.refetch().

export function RefreshButton() {
  const t = useTranslations('common');
  const router = useRouter();
  const [pending, start] = useTransition();
  return (
    <button
      type="button"
      disabled={pending}
      onClick={() => start(() => router.refresh())}
      className="text-text-2 text-xs px-2 py-1 border border-border-1 rounded hover:bg-surface-2 disabled:opacity-60"
      data-testid="refresh"
    >
      {pending ? t('loading') : t('refresh')}
    </button>
  );
}
