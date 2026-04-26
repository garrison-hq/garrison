'use client';

import type { ReactNode } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useEffect } from 'react';

// Slide-in drawer that hosts the ticket-detail blocks when the
// route was reached via an in-shell <Link> (intercepted into the
// @panel parallel slot). Closes via the back arrow, the backdrop,
// or Escape. The "open full →" link breaks out of the intercept
// to /tickets/[id] for share-by-URL or full-screen reading.
//
// Responsive shape:
//   - ≥ lg: 720px panel anchored to the right edge.
//   - < lg: full-viewport overlay (mobile equivalent of the
//     full-page route, animation still gives a "drawer" feel).

export function TicketDetailPanel({
  ticketId,
  ticketIdShort,
  departmentSlug,
  children,
}: Readonly<{
  ticketId: string;
  ticketIdShort: string;
  departmentSlug: string;
  children: ReactNode;
}>) {
  const router = useRouter();
  const close = () => router.back();

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') router.back();
    }
    window.addEventListener('keydown', onKey);
    // Lock body scroll while the drawer is open.
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = previousOverflow;
    };
  }, [router]);

  return (
    <>
      <button
        type="button"
        onClick={close}
        aria-label="Close ticket panel"
        className="fixed inset-0 z-40 bg-black/50 garrison-fade-in"
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={`Ticket ${ticketIdShort}`}
        className="fixed top-0 right-0 bottom-0 z-50 w-full lg:w-[720px] bg-bg border-l border-border-1 overflow-y-auto garrison-slide-in-right shadow-2xl"
      >
        <div className="sticky top-0 z-10 bg-surface-1 border-b border-border-1 px-4 py-2.5 flex items-center justify-between">
          <button
            type="button"
            onClick={close}
            className="text-text-2 hover:text-text-1 text-sm flex items-center gap-1.5"
          >
            <span aria-hidden>←</span> back
          </button>
          <div className="flex items-center gap-3 text-xs">
            <span className="text-text-3 font-mono">{departmentSlug}</span>
            <Link
              href={`/tickets/${ticketId}`}
              className="text-text-3 hover:text-text-1"
              prefetch={false}
            >
              open full →
            </Link>
          </div>
        </div>
        <div className="p-6 space-y-6">{children}</div>
      </aside>
    </>
  );
}
