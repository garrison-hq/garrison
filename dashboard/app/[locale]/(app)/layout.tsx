import { Sidebar } from '@/components/layout/Sidebar';
import { Topbar } from '@/components/layout/Topbar';

// Authenticated shell: sidebar + topbar wrap every (app) route.
// Server Component so the topbar can read the session + the
// sidebar can read translation catalogs without a client round-trip.
//
// Theme application happens at the [locale]/layout.tsx level via
// the data-theme attribute on <html>; that layout reads the
// operator's saved preference from the session.

export default function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-screen flex flex-col">
      <div className="flex flex-1 min-h-0">
        <Sidebar />
        <div className="flex-1 flex flex-col min-h-0">
          <Topbar />
          <div className="flex-1 overflow-auto">{children}</div>
        </div>
      </div>
    </div>
  );
}
