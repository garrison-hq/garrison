import { getTranslations } from 'next-intl/server';
import { Sidebar } from '@/components/layout/Sidebar';
import { Topbar } from '@/components/layout/Topbar';

// Authenticated shell: sidebar + topbar wrap every (app) route.
// Server Component so the topbar can read the session + the
// sidebar can read translation catalogs without a client round-trip.
//
// Theme application happens at the [locale]/layout.tsx level via
// the data-theme attribute on <html>; that layout reads the
// operator's saved preference from the session.

export default async function AppLayout({
  children,
  panel,
}: Readonly<{ children: React.ReactNode; panel: React.ReactNode }>) {
  // `panel` is the @panel parallel slot — empty when no intercepted
  // route is active, otherwise renders the slide-in drawer
  // overlaying `children`. The two slots are siblings inside the
  // same scrollable column so the drawer's fixed positioning
  // anchors to the viewport, not to the children container.
  const t = await getTranslations('a11y');
  return (
    <div className="min-h-screen flex flex-col">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:bg-surface-2 focus:text-text-1 focus:border focus:border-border-2 focus:rounded focus:px-3 focus:py-2 focus:text-sm"
      >
        {t('skipToContent')}
      </a>
      <div className="flex flex-1 min-h-0">
        <Sidebar />
        <div className="flex-1 flex flex-col min-h-0">
          <Topbar />
          <main id="main-content" className="flex-1 overflow-auto">{children}</main>
        </div>
      </div>
      {panel}
    </div>
  );
}
