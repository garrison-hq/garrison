// M5.2 — right-pane placeholder for the M5.4 "WHAT THE CEO KNOWS"
// knowledge-base view. Static; the pane is rendered so the three-pane
// layout is stable, but the content lands in M5.4. Per FR-202 the right
// pane collapses to a header strip on viewports < 1024px (handled by
// ChatShell.tsx; this component renders unconditionally — the parent
// decides whether to show it).

import { EmptyState } from '@/components/ui/EmptyState';

export function KnowsPanePlaceholder() {
  return (
    <aside
      className="border-l border-border-1 bg-surface-1 hidden lg:flex flex-col min-w-0"
      style={{ width: 360 }}
      aria-label="What the CEO knows (placeholder)"
    >
      <header className="border-b border-border-1 px-4 py-2.5">
        <h2 className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
          What the CEO knows
        </h2>
      </header>
      <div className="flex-1 overflow-auto p-4">
        <EmptyState
          description="Knowledge-base context lands in M5.4."
          caption="For now this pane is reserved."
        />
      </div>
    </aside>
  );
}
