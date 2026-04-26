// Compact relative-time formatter for the org-overview card
// timestamps ("12m ago", "1h ago", "3d ago"). Falls back to ISO
// when the date is older than 30 days.

const SECOND = 1_000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

export function relativeTime(when: Date | string | null, now: Date = new Date()): string {
  if (!when) return '—';
  const t = typeof when === 'string' ? new Date(when) : when;
  const delta = now.getTime() - t.getTime();
  if (delta < 0) return 'just now';
  if (delta < MINUTE) return 'just now';
  if (delta < HOUR) return `${Math.floor(delta / MINUTE)}m ago`;
  if (delta < DAY) return `${Math.floor(delta / HOUR)}h ago`;
  if (delta < 30 * DAY) return `${Math.floor(delta / DAY)}d ago`;
  return t.toISOString().slice(0, 10);
}
