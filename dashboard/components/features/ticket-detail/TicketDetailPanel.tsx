'use client';

import type { ReactNode } from 'react';
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
    globalThis.addEventListener('keydown', onKey);
    // Lock body scroll while the drawer is open.
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      globalThis.removeEventListener('keydown', onKey);
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
      {/* Native <dialog open> has UA defaults that override Tailwind's
          `fixed` positioning (position: absolute; left: 0; right: 0;
          margin: auto) and push the underlying surface around. Use
          <aside role="dialog" aria-modal="true"> instead — same a11y
          semantics, no rogue UA styles. */}
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={`Ticket ${ticketIdShort}`}
        className="fixed top-0 right-0 bottom-0 z-50 w-full lg:w-[720px] bg-bg border-l-2 border-border-2 overflow-y-auto garrison-slide-in-right shadow-[-12px_0_32px_-8px_rgba(0,0,0,0.6)]"
      >
        <div className="sticky top-0 z-10 bg-surface-1 border-b border-border-1 px-4 h-12 flex items-center gap-3">
          <button
            type="button"
            onClick={close}
            aria-label="Close ticket panel"
            className="w-7 h-7 grid place-items-center rounded text-text-2 hover:text-text-1 hover:bg-surface-3 border border-border-1 hover:border-border-2 transition-colors"
          >
            <span aria-hidden className="text-base leading-none">←</span>
          </button>
          <span className="text-text-3 text-[11px] font-mono">{departmentSlug}</span>
          <span className="text-text-4" aria-hidden>·</span>
          <span className="text-text-3 text-[11px] font-mono">{ticketIdShort}</span>
          <span className="ml-auto" />
          {/* Plain <a> (not next/link): the user is already at
              /tickets/<id> via the intercept, so an in-shell
              Link to the same href is a no-op. A full anchor
              triggers a hard navigation, which re-resolves the
              URL fresh — the intercept doesn't fire on initial
              page loads, so the full page renders. */}
          <a
            href={`/tickets/${ticketId}`}
            className="text-text-3 hover:text-text-1 hover:bg-surface-3 border border-border-1 hover:border-border-2 rounded text-[11px] font-medium px-2.5 py-1 transition-colors"
          >
            open full →
          </a>
        </div>
        <div className="p-6 space-y-7">{children}</div>
      </aside>
    </>
  );
}
