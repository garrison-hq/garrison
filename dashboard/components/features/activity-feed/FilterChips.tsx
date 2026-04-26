'use client';

import { useRouter, useSearchParams, usePathname } from 'next/navigation';

const KINDS = ['all', 'ticket.created', 'ticket.transitioned'] as const;
type Kind = (typeof KINDS)[number];

// FR-063: per-event-type filter chips persist in URL query params
// for shareability + back/forward navigation.

export function FilterChips() {
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
    <div className="flex gap-1.5 flex-wrap" data-testid="event-kind-filter">
      {KINDS.map((k) => (
        <button
          key={k}
          type="button"
          onClick={() => setKind(k)}
          data-testid={`kind-${k}`}
          className={`text-xs px-2 py-1 rounded border ${
            active === k
              ? 'bg-surface-3 text-text-1 border-border-2'
              : 'bg-surface-1 text-text-3 border-border-1 hover:text-text-2'
          }`}
        >
          {k}
        </button>
      ))}
    </div>
  );
}
