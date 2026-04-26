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

// "2m 14s" / "1h 32m" — used for the live-spawns elapsed column.
export function formatElapsed(start: Date | string, now: Date = new Date()): string {
  const t = typeof start === 'string' ? new Date(start) : start;
  const delta = Math.max(0, now.getTime() - t.getTime());
  const totalSeconds = Math.floor(delta / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `${minutes}m ${seconds}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

// "14:08:22" — wall-clock time-of-day for transition rows.
export function formatTimeOfDay(when: Date | string): string {
  const t = typeof when === 'string' ? new Date(when) : when;
  return t.toISOString().slice(11, 19);
}

// "Apr 26 17:37" — short calendar+time for transition history
// rows. Used as the visible value; full ISO string sits on the
// element's title attribute as a tooltip.
const SHORT_MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
export function formatShortDateTime(when: Date | string): string {
  const t = typeof when === 'string' ? new Date(when) : when;
  const m = SHORT_MONTHS[t.getUTCMonth()];
  const d = String(t.getUTCDate()).padStart(2, '0');
  const hh = String(t.getUTCHours()).padStart(2, '0');
  const mm = String(t.getUTCMinutes()).padStart(2, '0');
  return `${m} ${d} ${hh}:${mm}`;
}

export function formatIsoFull(when: Date | string): string {
  const t = typeof when === 'string' ? new Date(when) : when;
  return t.toISOString();
}
