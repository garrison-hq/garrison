'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';

// 60s soft-poll for the org-overview surface (FR-032). Calls
// router.refresh() on a timer so the Server Component re-runs its
// DB queries. Pauses when the tab is hidden.
//
// We don't use TanStack Query for the poll directly because the
// surface IS server-rendered — the server component owns the
// query. The poll just nudges Next to re-run it. This keeps the
// component tree mostly server-side and aligns with React 19's
// recommended pattern for refresh-on-interval reads.

export function SoftPoll({ intervalMs = 60_000 }: { intervalMs?: number }) {
  const router = useRouter();
  useEffect(() => {
    let id: ReturnType<typeof setInterval> | null = null;
    function start() {
      if (document.hidden) return;
      id = setInterval(() => router.refresh(), intervalMs);
    }
    function stop() {
      if (id !== null) {
        clearInterval(id);
        id = null;
      }
    }
    function onVis() {
      stop();
      start();
    }
    document.addEventListener('visibilitychange', onVis);
    start();
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVis);
    };
  }, [router, intervalMs]);
  return null;
}
