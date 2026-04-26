'use client';

import { useTranslations } from 'next-intl';
import { StatusDot } from '@/components/ui/StatusDot';

// Compact KPI row above the live feed. Mirrors the per-tile shape
// used on /hygiene meta + the org-overview "X tracked" line: small
// uppercase microcopy label + tabular-mono value, separated by a
// thin vertical rule. Status dot per tile keys off whether the
// count is non-zero — this makes "0 errors" land as idle rather
// than alarming.

type Tone = 'ok' | 'info' | 'warn' | 'err' | 'neutral';

function tileTone(value: number, signalTone: Tone): Tone {
  return value > 0 ? signalTone : 'neutral';
}

function dotTone(t: Tone): 'ok' | 'info' | 'warn' | 'err' | 'neutral' {
  return t;
}

export function ActivityKPIStrip({
  events,
  transitions,
  creates,
  lastHour,
}: Readonly<{
  events: number;
  transitions: number;
  creates: number;
  lastHour: number;
}>) {
  const t = useTranslations('activityMeta.kpi');
  return (
    <div className="bg-surface-1 border border-border-1 rounded flex items-center divide-x divide-border-1">
      <Tile label={t('events')} value={events} tone={tileTone(events, 'ok')} />
      <Tile label={t('lastHour')} value={lastHour} tone={tileTone(lastHour, 'ok')} />
      <Tile
        label={t('transitions')}
        value={transitions}
        tone={tileTone(transitions, 'info')}
      />
      <Tile label={t('creates')} value={creates} tone={tileTone(creates, 'info')} />
    </div>
  );
}

function Tile({
  label,
  value,
  tone,
}: Readonly<{ label: string; value: number; tone: Tone }>) {
  return (
    <div className="flex-1 px-4 py-3 flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5">
        <StatusDot tone={dotTone(tone)} />
        <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
          {label}
        </span>
      </div>
      <span className="text-text-1 text-[24px] leading-none font-mono font-semibold font-tabular">
        {value}
      </span>
    </div>
  );
}
