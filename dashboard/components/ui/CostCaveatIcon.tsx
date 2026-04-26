'use client';

import { useTranslations } from 'next-intl';

// FR-053 + spec edge case "Cost telemetry zero on clean finalize":
// every display position of `total_cost_usd` carries this icon
// when (exit_reason='finalize_committed' AND total_cost_usd=0).
// Hover tooltip names the issue and links to the open issue under
// docs/issues/cost-telemetry-blind-spot.md.

export function CostCaveatIcon({ matched }: Readonly<{ matched: boolean }>) {
  const t = useTranslations('errors');
  if (!matched) return null;
  // The tooltip key (errors.cost_blind_spot.tooltip) is added in
  // T011 alongside the cost-blind-spot test fixtures. For T009 we
  // fall back gracefully — useTranslations returns the key path
  // when missing, but T011 will install the value before the icon
  // renders against real data.
  return (
    <span
      title={t.has('cost_blind_spot.tooltip') ? t('cost_blind_spot.tooltip') : 'Cost telemetry caveat'}
      data-testid="cost-caveat-icon"
      className="inline-flex items-center text-warn cursor-help"
      aria-label="cost-blind-spot caveat"
    >
      <a
        href="https://github.com/garrison-hq/garrison/blob/main/docs/issues/cost-telemetry-blind-spot.md"
        target="_blank"
        rel="noreferrer"
        className="px-1 text-warn no-underline"
      >
        ⚠
      </a>
    </span>
  );
}
