// Compact concurrency widget for an agent row. Shows
// "<live>/<cap>" in tabular mono followed by a thin proportional
// bar. Bar fills with the accent tone normally, flips to warn
// at >= 80% saturation, and to err at full cap. Empty (0/N)
// renders the bar at zero width with the surface track visible.

type Tone = 'accent' | 'warn' | 'err';

const TONE_FILL: Record<Tone, string> = {
  accent: 'bg-accent',
  warn: 'bg-warn',
  err: 'bg-err',
};

export function ConcurrencyBar({
  live,
  cap,
}: Readonly<{ live: number; cap: number }>) {
  const safeCap = Math.max(1, cap);
  const pct = Math.min(1, live / safeCap);
  const tone: Tone =
    live >= cap ? 'err' : pct >= 0.8 ? 'warn' : 'accent';
  return (
    <div
      className="flex items-center gap-2"
      aria-label={`${live} of ${cap} concurrent`}
    >
      <span className="font-mono font-tabular text-[12px] text-text-2">
        <span className="text-text-1">{live}</span>
        <span className="text-text-4 mx-0.5">/</span>
        {cap}
      </span>
      <div className="flex-1 max-w-12 h-1 rounded-full bg-surface-3 overflow-hidden">
        <div
          className={`h-full ${live > 0 ? TONE_FILL[tone] : ''}`}
          style={{ width: `${pct * 100}%` }}
        />
      </div>
    </div>
  );
}
