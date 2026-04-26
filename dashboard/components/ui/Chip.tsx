import type { ReactNode } from 'react';

// Compact label/badge primitive lifted from the mocks' visual
// vocabulary. Tone selects the semantic color via Tailwind utility
// names that resolve through the CSS-variable token system in
// app/globals.css.

type Tone = 'neutral' | 'accent' | 'info' | 'warn' | 'err' | 'ok';

const toneClass: Record<Tone, string> = {
  neutral: 'bg-surface-2 text-text-2 border-border-1',
  accent: 'bg-accent/10 text-accent border-accent/30',
  info: 'bg-info/10 text-info border-info/30',
  warn: 'bg-warn/10 text-warn border-warn/30',
  err: 'bg-err/10 text-err border-err/30',
  ok: 'bg-ok/10 text-ok border-ok/30',
};

export function Chip({ children, tone = 'neutral' }: Readonly<{ children: ReactNode; tone?: Tone }>) {
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium border ${toneClass[tone]}`}
    >
      {children}
    </span>
  );
}
