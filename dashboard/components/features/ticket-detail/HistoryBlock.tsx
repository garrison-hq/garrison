import { getTranslations } from 'next-intl/server';
import { HistoryRow } from './HistoryRow';
import type { TransitionRow } from '@/lib/queries/ticketDetail';

export async function HistoryBlock({ history }: { history: TransitionRow[] }) {
  const t = await getTranslations('ticketDetail');
  return (
    <section className="space-y-2">
      <h3 className="text-text-2 text-xs uppercase tracking-wider">{t('history')}</h3>
      <div className="space-y-2" data-testid="history-block">
        {history.length === 0 ? (
          <p className="text-text-3 text-xs">{t('noHistory')}</p>
        ) : (
          history.map((row) => <HistoryRow key={row.id} row={row} />)
        )}
      </div>
    </section>
  );
}
