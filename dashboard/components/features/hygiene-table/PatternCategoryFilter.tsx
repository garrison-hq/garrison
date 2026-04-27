'use client';

import { useRouter, useSearchParams, usePathname } from 'next/navigation';
import { PATTERN_CATEGORIES, type PatternCategory } from '@/lib/hygiene/categories';

// PatternCategoryFilter — M4 / FR-117 chip strip filtering by
// suspected_secret_pattern_category. Renders only when the
// failure-mode filter is unset OR set to 'suspected_secret_emitted'
// (other failure modes have no category to filter on). The chip
// strip mirrors FailureModeFilter visually for consistency.
//
// 'all' clears the category filter; the 11 categories drive the
// remaining chips (10 supervisor labels + 'unknown' for pre-M4
// rows per FR-118). Selection persists in the URL as
// ?category=<slug>.

export function PatternCategoryFilter() {
  const router = useRouter();
  const pathname = usePathname();
  const search = useSearchParams();
  const active = search.get('category') ?? 'all';
  const mode = search.get('mode');

  // Hide the strip unless the failure-mode filter would surface
  // suspected_secret_emitted rows. The hygiene query layer
  // also returns rows from other modes to NULL category, but
  // filtering by category only makes sense when the filter
  // narrows to the secret-emitted bucket.
  if (mode && mode !== 'suspected_secret_emitted') {
    return null;
  }

  function setCategory(category: PatternCategory | 'all') {
    const params = new URLSearchParams(search.toString());
    if (category === 'all') {
      params.delete('category');
    } else {
      params.set('category', category);
      // Setting a category implies the suspected_secret_emitted
      // bucket; sync the mode filter too so the row count
      // chip-strip doesn't show stale numbers.
      if (!params.get('mode')) {
        params.set('mode', 'suspected_secret_emitted');
      }
    }
    params.delete('page');
    const qs = params.toString();
    router.push(qs ? `${pathname}?${qs}` : pathname);
  }

  return (
    <div
      role="radiogroup"
      aria-label="Pattern category filter"
      className="flex gap-1.5 flex-wrap"
      data-testid="pattern-category-filter"
    >
      <CategoryChip label="all categories" value="all" active={active === 'all'} onClick={() => setCategory('all')} />
      {PATTERN_CATEGORIES.map((c) => (
        <CategoryChip
          key={c}
          label={c}
          value={c}
          active={active === c}
          onClick={() => setCategory(c)}
        />
      ))}
    </div>
  );
}

function CategoryChip({
  label,
  value,
  active,
  onClick,
}: Readonly<{
  label: string;
  value: string;
  active: boolean;
  onClick: () => void;
}>) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      data-testid={`category-${value}`}
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-[11.5px] border transition-colors font-mono ${
        active
          ? 'bg-accent/10 text-accent border-accent/30'
          : 'bg-surface-1 text-text-3 border-border-1 hover:text-text-2 hover:border-border-2'
      }`}
    >
      {label}
    </button>
  );
}
