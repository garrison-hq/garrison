// M5.2 — three-pane chat shell (plan §1.1). Composes the center pane
// (children) alongside the right-pane KnowsPanePlaceholder. The left
// rail is the global Sidebar from (app)/layout.tsx — ChatShell does
// not render it. The right pane collapses below 1024px per FR-202;
// CSS handles the breakpoint so SSR + CSR layouts agree without a
// JS hydration delay.

import type { ReactNode } from 'react';
import { KnowsPanePlaceholder } from './KnowsPanePlaceholder';

export function ChatShell({ children }: Readonly<{ children: ReactNode }>) {
  // h-full instead of flex-1 because the (app) layout's <main> is a
  // block element (overflow-auto), not a flex container — flex-1 would
  // be a no-op. h-full makes the shell occupy the full main viewport
  // height so the inner flex column actually distributes space:
  // ChatTopbarStrip + ThreadHeader land at natural height, MessageStream
  // (flex-1 internally) fills the middle, Composer sticks at the bottom.
  return (
    <div className="flex h-full min-h-0 min-w-0 overflow-x-hidden">
      <section className="flex-1 flex flex-col min-w-0 overflow-hidden">{children}</section>
      <KnowsPanePlaceholder />
    </div>
  );
}
