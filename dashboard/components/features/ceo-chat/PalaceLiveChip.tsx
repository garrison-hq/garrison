// M5.2 — PalaceLiveChip (plan §1.10).
//
// Reads the operator-supplied ageMs (parent fetches via
// getMostRecentMempalaceCallAge on every terminal commit) and renders
// one of three states per FR-283:
//
//   ageMs <= 5min       → 'live'         (ok)
//   5min < ageMs <= 30m → 'stale'        (warn)
//   ageMs > 30m or null → 'unavailable'  (err)
//
// Per FR-332 the chip combines a colored StatusDot with a text label
// — colorblind operators read the label.

import { Chip } from '@/components/ui/Chip';
import { StatusDot } from '@/components/ui/StatusDot';

const FIVE_MIN_MS = 5 * 60_000;
const THIRTY_MIN_MS = 30 * 60_000;

export type PalaceLiveTone = 'live' | 'stale' | 'unavailable';

export function classifyAge(ageMs: number | null): PalaceLiveTone {
  if (ageMs === null) return 'unavailable';
  if (ageMs <= FIVE_MIN_MS) return 'live';
  if (ageMs <= THIRTY_MIN_MS) return 'stale';
  return 'unavailable';
}

export function PalaceLiveChip({ ageMs }: Readonly<{ ageMs: number | null }>) {
  const tone = classifyAge(ageMs);
  const dot = tone === 'live' ? 'ok' : tone === 'stale' ? 'warn' : 'err';
  const label =
    tone === 'live'
      ? 'palace live'
      : tone === 'stale'
      ? 'palace stale'
      : 'palace unavailable';
  return (
    <Chip tone={dot}>
      <span className="inline-flex items-center gap-1.5" data-testid="palace-live-chip" data-tone={tone}>
        <StatusDot tone={dot} />
        <span>{label}</span>
      </span>
    </Chip>
  );
}
