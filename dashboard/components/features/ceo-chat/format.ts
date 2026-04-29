// M5.2 — chat-feature formatting helpers (plan §1.9 + §1.11).
//
// Five exports:
//   formatThreadTitle(n)        — "thread #<n>"
//   formatTimeAgo(date)         — concise relative-time
//   formatSessionCost(usd)      — 2-decimal currency for headers
//   formatPerMessageCost(usd)   — 4-decimal currency for bubble footers
//   formatModelBadge(model)     — cosmetic CEO model name display
//
// Currency formatting uses Intl.NumberFormat with explicit decimal
// bounds so the renderer reads $0.00 / $0.0001 in the same shape the
// operator sees in budget dashboards. Both helpers return null-safe
// shapes — operator turns have cost_usd=NULL per M5.1 schema.

/** Cost values arrive as plain numbers (in tests / hot client paths)
 *  or as Drizzle's NUMERIC string ("1.234567") via the database
 *  driver. The helpers below accept either, plus null/undefined. */
export type CostInput = number | string | null | undefined;

export function formatThreadTitle(threadNumber: number): string {
  return `thread #${threadNumber}`;
}

const SESSION_COST_FMT = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

export function formatSessionCost(usd: CostInput): string {
  const n = toNumber(usd);
  if (n === null) return SESSION_COST_FMT.format(0);
  return SESSION_COST_FMT.format(n);
}

const PER_MESSAGE_COST_FMT = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 4,
  maximumFractionDigits: 4,
});

export function formatPerMessageCost(usd: CostInput): string | null {
  const n = toNumber(usd);
  if (n === null) return null;
  return PER_MESSAGE_COST_FMT.format(n);
}

function toNumber(v: CostInput): number | null {
  if (v === null || v === undefined) return null;
  if (typeof v === 'number') return Number.isFinite(v) ? v : null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}

// formatTimeAgo returns short-form English ("2m ago", "3h ago", "5d
// ago"). Avoids the extended M3 relativeTime helper because chat
// surfaces want a tighter visual budget — header subtext, not body
// copy. Falls back to ISO date for >30 days.
export function formatTimeAgo(date: Date | string | number, now: Date | number = Date.now()): string {
  const target = date instanceof Date ? date.getTime() : new Date(date).getTime();
  const reference = typeof now === 'number' ? now : now.getTime();
  const seconds = Math.max(0, Math.floor((reference - target) / 1000));
  if (seconds < 60) return seconds <= 5 ? 'just now' : `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  const iso = new Date(target).toISOString().slice(0, 10);
  return iso;
}

// formatModelBadge displays the chat image's model name. Cosmetic
// display only per FR-225 — clicking the badge is a no-op. Empty /
// null falls back to a placeholder so the header layout stays stable.
export function formatModelBadge(model: string | null | undefined): string {
  if (!model || model.trim().length === 0) return 'model n/a';
  return model.trim();
}
