// M5.2 — PalaceLiveChip (plan §1.10).
//
// Reads the operator-supplied ageMs (parent fetches via
// getMostRecentMempalaceCallAge on every terminal commit) and renders
// one of three states:
//
//   ageMs <= 5min  → 'live'         (ok, green)
//   ageMs > 30min  → 'unavailable'  (err, red)
//   else           → 'idle'         (info, muted)
//
// "Idle" covers both (a) no mempalace call has happened in this thread
// yet (ageMs === null) and (b) the most recent call is in the 5-30min
// range. In both cases the palace itself is fine, it just hasn't been
// touched recently — the muted info tone says "nothing wrong, nothing
// active" without the warn-yellow alarm. "Unavailable" / err is
// reserved for the >30min cold path that's likely an actual problem.
//
// Per FR-332 the chip combines a colored StatusDot with a text label
// — colorblind operators read the label.

import { Chip } from '@/components/ui/Chip';
import { StatusDot } from '@/components/ui/StatusDot';

const FIVE_MIN_MS = 5 * 60_000;
const THIRTY_MIN_MS = 30 * 60_000;

export type PalaceLiveTone = 'live' | 'idle' | 'unavailable';

export function classifyAge(ageMs: number | null): PalaceLiveTone {
  if (ageMs === null) return 'idle';
  if (ageMs <= FIVE_MIN_MS) return 'live';
  if (ageMs <= THIRTY_MIN_MS) return 'idle';
  return 'unavailable';
}

const TONE_DOT: Record<PalaceLiveTone, 'ok' | 'err' | 'info'> = {
  live: 'ok',
  unavailable: 'err',
  idle: 'info',
};

const TONE_LABEL: Record<PalaceLiveTone, string> = {
  live: 'palace live',
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
