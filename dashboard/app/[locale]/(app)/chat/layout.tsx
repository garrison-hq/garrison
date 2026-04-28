import type { ReactNode } from 'react';
import { ChatShell } from '@/components/features/ceo-chat/ChatShell';

// M5.2 — chat-route layout. Composes inside the existing (app)/layout.tsx
// shell (sidebar + global topbar). Adds the three-pane chat composition;
// individual pages render the center-pane content (page.tsx empty state,
// [[...sessionId]]/page.tsx active session view, all/page.tsx full thread
// list view).
//
// The ChatTopbarStrip is rendered by each page rather than the layout
// because the strip's content (breadcrumb suffix, idle pill) depends on
// whether a session is open — pushing that to the layout would force a
// client component at the layout boundary, which would defeat the
// server-rendered initial transcript fetch (plan §1.16).

export default function ChatLayout({ children }: Readonly<{ children: ReactNode }>) {
  return <ChatShell>{children}</ChatShell>;
}
