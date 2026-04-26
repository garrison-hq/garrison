// Single source of truth for the workflow-column color palette.
// Used by the kanban column header, the ticket-detail status pill,
// the transition-history "from → to" labels, and the org-overview
// distribution sliver — anywhere a column slug needs a visual tone.
//
// Four canonical states map to the dashboard's existing semantic
// tones:
//
//   todo / backlog / ideas / new / questions  → neutral
//   in_dev / drafting / running / working     → info
//   in_review / review / analyzing            → warn
//   done / ship / closed / written            → ok
//
// Slugs not in the lookup fall back to neutral so unknown
// custom-workflow columns still render.

export type ColumnTone = 'neutral' | 'info' | 'warn' | 'ok';

const TONE: Record<string, ColumnTone> = {
  todo: 'neutral',
  backlog: 'neutral',
  ideas: 'neutral',
  new: 'neutral',
  questions: 'neutral',

  in_dev: 'info',
  drafting: 'info',
  running: 'info',
  working: 'info',

  in_review: 'warn',
  review: 'warn',
  analyzing: 'warn',
  qa: 'warn',

  done: 'ok',
  ship: 'ok',
  closed: 'ok',
  written: 'ok',
};

export function columnTone(slug: string): ColumnTone {
  return TONE[slug] ?? 'neutral';
}

// Tailwind class fragments for each tone, keyed by surface role.
// Centralised so all consumers paint with the same swatches.

const TONE_TEXT: Record<ColumnTone, string> = {
  neutral: 'text-text-3',
  info: 'text-info',
  warn: 'text-warn',
  ok: 'text-ok',
};

const TONE_BG_FILL: Record<ColumnTone, string> = {
  neutral: 'bg-text-3/60',
  info: 'bg-info/80',
  warn: 'bg-warn/80',
  ok: 'bg-ok/80',
};

const TONE_DOT: Record<ColumnTone, 'neutral' | 'info' | 'warn' | 'ok'> = {
  neutral: 'neutral',
  info: 'info',
  warn: 'warn',
  ok: 'ok',
};

export function columnTextClass(slug: string): string {
  return TONE_TEXT[columnTone(slug)];
}

export function columnFillClass(slug: string): string {
  return TONE_BG_FILL[columnTone(slug)];
}

export function columnDotTone(slug: string): 'neutral' | 'info' | 'warn' | 'ok' {
  return TONE_DOT[columnTone(slug)];
}
