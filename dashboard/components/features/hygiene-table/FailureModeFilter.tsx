'use client';

import { useTranslations } from 'next-intl';
import { useRouter, useSearchParams, usePathname } from 'next/navigation';
import type { FailureMode } from '@/lib/queries/hygiene';

// Segmented filter chips ("all 14 / finalize-path 6 / sandbox-escape
// 2 / suspected-secret 6"). Each chip is a real button with an
// active state — neutral surface for inactive, accent-tinted with
// border for active. Per-mode counts live in the URL-driven
// counts prop so a click never refetches counts.

const MODES: { mode: FailureMode | 'all'; labelKey: string }[] = [
  { mode: 'all', labelKey: 'all' },
  { mode: 'finalize_path', labelKey: 'finalizePath' },
  { mode: 'sandbox_escape', labelKey: 'sandboxEscape' },
  { mode: 'suspected_secret_emitted', labelKey: 'suspectedSecret' },
];

export function FailureModeFilter({
  counts,
  total,
}: Readonly<{
  counts: Record<FailureMode, number>;
  total: number;
}>) {
  const t = useTranslations('hygieneMeta');
  const router = useRouter();
  const pathname = usePathname();
  const search = useSearchParams();
  const active = search.get('mode') ?? 'all';

  function setMode(mode: FailureMode | 'all') {
    const params = new URLSearchParams(search.toString());
    if (mode === 'all') {
      params.delete('mode');
    } else {
      params.set('mode', mode);
    }
    params.delete('page');
    const qs = params.toString();
    router.push(qs ? `${pathname}?${qs}` : pathname);
  }

  function countFor(mode: FailureMode | 'all'): number {
    return mode === 'all' ? total : counts[mode];
  }

  return (
    <div
      role="radiogroup"
      aria-label={t('all')}
      className="flex gap-1.5 flex-wrap"
      data-testid="failure-mode-filter"
    >
      {MODES.map((m) => {
        const selected = active === m.mode;
        return (
          <button
            key={m.mode}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => setMode(m.mode)}
            data-testid={`mode-${m.mode}`}
            className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-[12px] border transition-colors ${
              selected
                ? 'bg-accent/10 text-accent border-accent/30'
                : 'bg-surface-1 text-text-3 border-border-1 hover:text-text-2 hover:border-border-2'
            }`}
          >
            <span>{t(m.labelKey)}</span>
            <span
              className={`font-mono font-tabular text-[10.5px] ${
                selected ? 'text-accent/80' : 'text-text-3'
              }`}
            >
              {countFor(m.mode)}
            </span>
          </button>
        );
      })}
    </div>
  );
}
