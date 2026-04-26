import { HygieneRowItem } from './HygieneRow';
import { EmptyState } from '@/components/ui/EmptyState';
import type { HygieneRow as Row } from '@/lib/queries/hygiene';

export function HygieneTable({
  rows,
  emptyDescription,
}: {
  rows: Row[];
  emptyDescription: string;
}) {
  if (rows.length === 0) {
    return <EmptyState description={emptyDescription} />;
  }
  return (
    <div className="border border-border-1 rounded bg-surface-1 divide-y divide-border-1">
      {rows.map((r) => (
        <HygieneRowItem key={r.transitionId} row={r} />
      ))}
    </div>
  );
}
