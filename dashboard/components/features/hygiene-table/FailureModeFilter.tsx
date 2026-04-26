'use client';

import { useRouter, useSearchParams, usePathname } from 'next/navigation';
import { Chip } from '@/components/ui/Chip';
import type { FailureMode } from '@/lib/queries/hygiene';

const MODES: { mode: FailureMode | 'all'; label: string }[] = [
  { mode: 'all', label: 'all' },
  { mode: 'finalize_path', label: 'finalize-path' },
  { mode: 'sandbox_escape', label: 'sandbox-escape' },
  { mode: 'suspected_secret_emitted', label: 'suspected-secret' },
];

export function FailureModeFilter() {
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

  return (
    <div className="flex gap-1.5 flex-wrap" data-testid="failure-mode-filter">
      {MODES.map((m) => (
        <button
          key={m.mode}
          type="button"
          onClick={() => setMode(m.mode)}
          data-testid={`mode-${m.mode}`}
          className={`text-xs px-2 py-1 rounded border ${
            active === m.mode
              ? 'bg-surface-3 text-text-1 border-border-2'
              : 'bg-surface-1 text-text-3 border-border-1 hover:text-text-2'
          }`}
        >
          {m.label}
        </button>
      ))}
    </div>
  );
}
