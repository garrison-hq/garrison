import { getTranslations } from 'next-intl/server';
import { CopyButton } from '@/components/ui/CopyButton';

// Placeholder palace-links surface (FR-054). M3 doesn't query
// MemPalace directly — per A-004, the palace MCP wiring is the
// M2.2 supervisor concern. M3 surfaces the search lookup string
// and a copy button so the operator can paste it into MemPalace's
// search. M5+ will wire the read references inline.

export async function PalaceLinksBlock({
  ticketId,
}: Readonly<{ ticketId: string }>) {
  const t = await getTranslations('ticketDetail');
  const lookup = `ticket:${ticketId}`;
  return (
    <section className="space-y-2">
      <h3 className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
        {t('palaceLinks')}
      </h3>
      <p className="text-text-2 text-[13px] leading-[1.55]">
        {t('palaceLinksHint')}
      </p>
      <div className="flex items-center justify-between gap-2 rounded border border-border-1 bg-surface-1 px-3 py-2">
        <code className="font-mono text-[12px] text-text-1 truncate">{lookup}</code>
        <CopyButton value={lookup} />
      </div>
    </section>
  );
}
