// Tiny coloured dot used in nav rows and ticket status indicators.
// Color resolves through the same token set Chip uses.

type Tone = 'neutral' | 'accent' | 'info' | 'warn' | 'err' | 'ok';

const toneClass: Record<Tone, string> = {
  neutral: 'bg-text-3',
  accent: 'bg-accent',
  info: 'bg-info',
  warn: 'bg-warn',
  err: 'bg-err',
  ok: 'bg-ok',
};

export function StatusDot({ tone = 'neutral' }: { tone?: Tone }) {
  return <span className={`inline-block w-1.5 h-1.5 rounded-full ${toneClass[tone]}`} />;
}
