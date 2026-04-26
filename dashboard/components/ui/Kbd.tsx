import type { ReactNode } from 'react';

// Inline keyboard-shortcut indicator. Mono font, faint surface.
export function Kbd({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <kbd className="inline-flex items-center px-1.5 py-0.5 rounded text-[11px] font-mono text-text-3 bg-surface-3 border border-border-1">
      {children}
    </kbd>
  );
}
