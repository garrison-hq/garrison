// M5.2 — PalaceLiveChip (plan §1.10).
//
// Reads the operator-supplied ageMs (parent fetches via
// getMostRecentMempalaceCallAge on every terminal commit) and renders
// one of four states (FR-283 + Apr-29 polish):
//
//   ageMs <= 5min       → 'live'         (ok)
//   5min < ageMs <= 30m → 'stale'        (warn)
//   ageMs > 30m         → 'unavailable'  (err)
//   ageMs == null       → 'idle'         (info)
//
// "Unavailable" / err is now reserved for the genuinely-broken path
// (palace had a successful call recently but it's gone cold). The
// initial empty state — operator just opened a thread, no mempalace
// call has happened yet — renders as muted "idle" so the chip doesn't
// look like the surface is broken before anything's been tried.
//
// Per FR-332 the chip combines a colored StatusDot with a text label
// — colorblind operators read the label.

import { Chip } from '@/components/ui/Chip';
import { StatusDot } from '@/components/ui/StatusDot';

const FIVE_MIN_MS = 5 * 60_000;
const THIRTY_MIN_MS = 30 * 60_000;

export type PalaceLiveTone = 'live' | 'stale' | 'unavailable' | 'idle';

export function classifyAge(ageMs: number | null): PalaceLiveTone {
  if (ageMs === null) return 'idle';
  if (ageMs <= FIVE_MIN_MS) return 'live';
  if (ageMs <= THIRTY_MIN_MS) return 'stale';
  return 'unavailable';
}

const TONE_DOT: Record<PalaceLiveTone, 'ok' | 'warn' | 'err' | 'info'> = {
  live: 'ok',
  stale: 'warn',
  unavailable: 'err',
  idle: 'info',
};

const TONE_LABEL: Record<PalaceLiveTone, string> = {
  live: 'palace live',
  stale: 'palace stale',
  unavailable: 'palace unavailable',
  idle: 'palace idle',
};

export function PalaceLiveChip({ ageMs }: Readonly<{ ageMs: number | null }>) {
  const tone = classifyAge(ageMs);
  const dot = TONE_DOT[tone];
  const label = TONE_LABEL[tone];
  return (
    <Chip tone={dot}>
      <span className="inline-flex items-center gap-1.5" data-testid="palace-live-chip" data-tone={tone}>
        <StatusDot tone={dot} />
        <span>{label}</span>
      </span>
    </Chip>
  );
}
