// M5.2 — ThreadHeader (plan §1.9). Renders per-operator thread title,
// started-time, turn count, and per-session cost badge. Right side
// carries the ThreadOverflowMenu.
//
// Server-rendered; the cost badge updates whenever the parent
// re-fetches the session row (after a terminal commit, after an
// overflow action). No client-side ticker.

import {
  formatThreadTitle,
  formatTimeAgo,
  formatSessionCost,
} from './format';
import { ThreadOverflowMenu } from './ThreadOverflowMenu';

interface ThreadHeaderProps {
  sessionId: string;
  threadNumber: number;
  startedAt: string | Date;
  turnCount: number;
  totalCostUsd: number | string | null;
  status: 'active' | 'ended' | 'aborted' | string;
  isArchived: boolean;
}

export function ThreadHeader({
  sessionId,
  threadNumber,
  startedAt,
  turnCount,
  totalCostUsd,
  status,
  isArchived,
}: Readonly<ThreadHeaderProps>) {
  return (
    <header
      className="flex items-center gap-3 border-b border-border-1 bg-surface-1 px-4 py-2 min-w-0"
      data-testid="chat-thread-header"
    >
      <div className="flex flex-col gap-0.5 min-w-0 flex-1">
        <h2 className="text-text-1 text-sm font-medium truncate" data-testid="chat-thread-title">
          {formatThreadTitle(threadNumber)}
        </h2>
        <p className="text-text-3 text-[11px] flex items-center gap-2 flex-wrap">
          <span className="shrink-0">started {formatTimeAgo(startedAt)}</span>
          <span className="text-text-4 shrink-0">·</span>
          <span className="font-mono font-tabular shrink-0">{turnCount} turns</span>
          <span className="text-text-4 shrink-0">·</span>
          <CostBadge value={totalCostUsd} />
        </p>
      </div>
      <ThreadOverflowMenu sessionId={sessionId} status={status} isArchived={isArchived} />
    </header>
  );
}

export function CostBadge({
  value,
  precision = 2,
}: Readonly<{ value: number | string | null | undefined; precision?: 2 | 4 }>) {
  if (value === null || value === undefined) {
    return <span className="font-mono font-tabular text-text-2 text-[12px]">$0.00</span>;
  }
  if (precision === 4) {
    // Per-message badge variant (rare on the header but kept for a
    // shared call shape with MessageBubble's per-message footer).
    const fmt = new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: 'USD',
      minimumFractionDigits: 4,
      maximumFractionDigits: 4,
    });
    const n = typeof value === 'number' ? value : Number(value);
    return (
      <span className="font-mono font-tabular text-text-2 text-[12px]" data-testid="chat-cost-badge">
        {fmt.format(Number.isFinite(n) ? n : 0)}
      </span>
    );
  }
  return (
    <span className="font-mono font-tabular text-text-2 text-[12px]" data-testid="chat-cost-badge">
      {formatSessionCost(value)}
    </span>
  );
}
