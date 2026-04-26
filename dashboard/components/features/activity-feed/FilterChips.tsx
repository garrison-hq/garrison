'use client';

import { useTranslations } from 'next-intl';
import { useRouter, useSearchParams, usePathname } from 'next/navigation';

const KINDS = ['all', 'ticket.created', 'ticket.transitioned'] as const;
type Kind = (typeof KINDS)[number];

const KIND_LABELS: Record<Kind, 'filterAll' | 'filterCreated' | 'filterTransitioned'> = {
  all: 'filterAll',
  'ticket.created': 'filterCreated',
  'ticket.transitioned': 'filterTransitioned',
};

// Disabled-future placeholders so the filter strip lays out the way
// the production surface will once the supervisor starts emitting
// these channels. They render as muted chips with pointer-events
// disabled — the operator gets visual continuity but can't click
// into a filter that has no data yet.
const FUTURE_KINDS = [
  'ticket.commented',
  'agent.spawned',
  'agent.completed',
  'hygiene.flagged',
] as const;

// FR-063: per-event-type filter chips persist in URL query params
// for shareability + back/forward navigation. Same segmented-chip
// shape as FailureModeFilter on /hygiene.

export function FilterChips() {
  const t = useTranslations('activityMeta');
  const router = useRouter();
  const pathname = usePathname();
  const search = useSearchParams();
  const active: Kind = (KINDS as readonly string[]).includes(search.get('kind') ?? '')
    ? ((search.get('kind') ?? 'all') as Kind)
    : 'all';

  function setKind(kind: Kind) {
    const params = new URLSearchParams(search.toString());
    if (kind === 'all') params.delete('kind');
    else params.set('kind', kind);
    const qs = params.toString();
    router.push(qs ? `${pathname}?${qs}` : pathname);
  }

  return (
    <div
      role="radiogroup"
      aria-label={t('filterAll')}
      className="flex gap-1.5 flex-wrap"
      data-testid="event-kind-filter"
    >
      {KINDS.map((k) => {
        const selected = active === k;
        return (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => setKind(k)}
            data-testid={`kind-${k}`}
            className={`inline-flex items-center px-2.5 py-1 rounded text-[12px] border transition-colors font-mono ${
              selected
                ? 'bg-accent/10 text-accent border-accent/30'
                : 'bg-surface-1 text-text-3 border-border-1 hover:text-text-2 hover:border-border-2'
            }`}
          >
            {t(KIND_LABELS[k])}
          </button>
        );
      })}
      {FUTURE_KINDS.map((k) => (
        <span
          key={k}
          aria-disabled="true"
          className="inline-flex items-center px-2.5 py-1 rounded text-[12px] border border-border-1 bg-surface-1 text-text-4 font-mono opacity-50 select-none cursor-not-allowed"
          title="Available once the supervisor emits this channel (M4+)"
        >
          {k}
        </span>
      ))}
    </div>
  );
}
