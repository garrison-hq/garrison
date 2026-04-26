// Tiny inline bar sparkline. Renders an array of non-negative
// integers as a vertical-bar chart inside a fixed SVG box. No
// axes, no labels — pure shape so the eye picks up the trend
// next to a numeric total.
//
// `tone` selects the bar fill from the existing token set
// (accent / info / warn / err / neutral). Bars sit on the
// bottom; their heights are proportional to max(values, 1).

type Tone = 'accent' | 'info' | 'warn' | 'err' | 'neutral';

const TONE_FILL: Record<Tone, string> = {
  accent: 'fill-accent',
  info: 'fill-info',
  warn: 'fill-warn',
  err: 'fill-err',
  neutral: 'fill-text-3',
};

export function Sparkline({
  values,
  tone = 'accent',
  width = 56,
  height = 16,
  ariaLabel,
}: Readonly<{
  values: number[];
  tone?: Tone;
  width?: number;
  height?: number;
  ariaLabel?: string;
}>) {
  if (values.length === 0) {
    return <span className="text-text-4 text-[10.5px] font-mono">—</span>;
  }
  const max = Math.max(1, ...values);
  const gap = 1;
  const barWidth = (width - gap * (values.length - 1)) / values.length;
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={ariaLabel ?? `7-day spawn trend, ${values.join(', ')}`}
      className="shrink-0"
    >
      {values.map((v, i) => {
        const barHeight = v === 0 ? 1 : Math.max(1, (v / max) * height);
        const x = i * (barWidth + gap);
        const y = height - barHeight;
        return (
          <rect
            key={i}
            x={x}
            y={y}
            width={barWidth}
            height={barHeight}
            className={v === 0 ? TONE_FILL.neutral : TONE_FILL[tone]}
            opacity={v === 0 ? 0.35 : 1}
          />
        );
      })}
    </svg>
  );
}
