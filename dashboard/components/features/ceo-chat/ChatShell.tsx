// M5.2 — three-pane chat shell (plan §1.1). Composes the center pane
// (children) alongside the right-pane KnowsPanePlaceholder. The left
// rail is the global Sidebar from (app)/layout.tsx — ChatShell does
// not render it. The right pane collapses below 1024px per FR-202;
// CSS handles the breakpoint so SSR + CSR layouts agree without a
// JS hydration delay.

import type { ReactNode } from 'react';
import { KnowsPanePlaceholder } from './KnowsPanePlaceholder';

export function ChatShell({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div className="flex flex-1 min-h-0 min-w-0">
      <section className="flex-1 flex flex-col min-w-0 overflow-hidden">{children}</section>
      <KnowsPanePlaceholder />
    </div>
  );
}
