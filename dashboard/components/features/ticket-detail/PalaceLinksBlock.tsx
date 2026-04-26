import { getTranslations } from 'next-intl/server';

// Placeholder palace-links surface (FR-054). M3 doesn't query
// MemPalace directly — per A-004, the palace MCP wiring is the M2.2
// supervisor concern. M3 surfaces "look in MemPalace for this
// ticket id" rather than embedding the diary excerpt inline. M5+
// wires the read references.

export async function PalaceLinksBlock({ ticketId }: Readonly<{ ticketId: string }>) {
  const t = await getTranslations('ticketDetail');
  return (
    <section className="space-y-2">
      <h3 className="text-text-2 text-xs uppercase tracking-wider">{t('palaceLinks')}</h3>
      <div className="bg-surface-1 border border-border-1 rounded p-3 text-xs space-y-1.5 text-text-2">
        <p>{t('palaceLinksHint')}</p>
        <code className="font-mono text-text-1 block break-all">ticket:{ticketId}</code>
      </div>
    </section>
  );
}
