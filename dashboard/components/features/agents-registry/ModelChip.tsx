// Tier-coded model chip. Family (haiku/sonnet/opus) drives the
// chip's visual weight; the version suffix sits in muted text-3
// so the family pops.
//
//   haiku  → neutral chip (smallest, cheapest, least loud)
//   sonnet → info chip (mid tier)
//   opus   → accent-outline chip (premium tier)

type Tier = 'haiku' | 'sonnet' | 'opus' | 'other';

function tierFor(model: string): Tier {
  if (model.includes('opus')) return 'opus';
  if (model.includes('sonnet')) return 'sonnet';
  if (model.includes('haiku')) return 'haiku';
  return 'other';
}

const TIER_CLASS: Record<Tier, string> = {
  haiku: 'bg-surface-2 text-text-2 border-border-1',
  sonnet: 'bg-info/10 text-info border-info/30',
  opus: 'bg-transparent text-accent border-accent/40',
  other: 'bg-surface-2 text-text-2 border-border-1',
};

function splitFamilyVersion(model: string): { family: string; version: string } {
  // claude-opus-4-7 → family="claude-opus", version="-4-7"
  // claude-sonnet-4-6 → family="claude-sonnet", version="-4-6"
  const m = /^([a-z-]+?)(-\d[\d-]*)$/.exec(model);
  if (!m) return { family: model, version: '' };
  return { family: m[1], version: m[2] };
}

export function ModelChip({ model }: Readonly<{ model: string }>) {
  const tier = tierFor(model);
  const { family, version } = splitFamilyVersion(model);
  return (
    <span
      className={`inline-flex items-center px-2 py-0.5 rounded text-[10.5px] font-mono border ${TIER_CLASS[tier]}`}
    >
      <span>{family}</span>
      {version ? <span className="text-text-3">{version}</span> : null}
    </span>
  );
}
