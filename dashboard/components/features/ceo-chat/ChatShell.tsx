// M5.2 — three-pane chat shell (plan §1.1). Composes the center pane
// (children) alongside the right-pane KnowsPanePlaceholder. The left
// rail is the global Sidebar from (app)/layout.tsx — ChatShell does
// not render it. The right pane collapses below 1024px per FR-202;
// CSS handles the breakpoint so SSR + CSR layouts agree without a
// JS hydration delay.

import type { ReactNode } from 'react';
import { KnowsPanePlaceholder } from './KnowsPanePlaceholder';

export function ChatShell({ children }: Readonly<{ children: ReactNode }>) {
  // (app) layout's <main> is now a flex-col container with min-h-0 and
  // overflow-auto, so flex-1 here resolves cleanly: the shell consumes
  // whatever vertical space is left under the global topbar, and the
  // inner section uses overflow-hidden so MessageStream is the only
  // scrolling surface — the page itself never grows past the viewport.
  return (
    <div className="flex flex-1 min-h-0 min-w-0 overflow-x-hidden">
      <section className="flex-1 flex flex-col min-w-0 overflow-hidden">{children}</section>
      <KnowsPanePlaceholder />
    </div>
  );
}
