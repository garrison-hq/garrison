import { getTranslations } from 'next-intl/server';
import { HygieneRowItem } from './HygieneRow';
import type { HygieneRow as Row } from '@/lib/queries/hygiene';

export async function HygieneTable({
  rows,
  emptyDescription,
}: Readonly<{
  rows: Row[];
  emptyDescription: string;
}>) {
  if (rows.length === 0) {
    // min-h matches roughly the height of 6 populated rows so the
    // page doesn't resize when the operator switches between
    // failure-mode chips (a high-row mode collapsing to zero would
    // otherwise jump the layout up by hundreds of px).
    return (
      <div className="px-4 py-10 text-center text-text-3 text-[12.5px] min-h-[240px] flex items-center justify-center">
        {emptyDescription}
      </div>
    );
  }
  const t = await getTranslations('hygieneMeta.headers');
  return (
    <div className="overflow-x-auto min-h-[240px]">
      <table className="w-full">
        <colgroup>
          <col style={{ width: 180 }} />
          <col style={{ width: 120 }} />
          <col style={{ width: 180 }} />
          <col style={{ width: 180 }} />
          <col />
          <col style={{ width: 110 }} />
        </colgroup>
        <thead>
          <tr className="bg-surface-2 border-b border-border-1">
            <Th>{t('time')}</Th>
            <Th>{t('dept')}</Th>
            <Th>{t('transition')}</Th>
            <Th>{t('flag')}</Th>
            <Th>{t('detail')}</Th>
            <Th align="right">{t('ticket')}</Th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border-1">
          {rows.map((r) => (
            <HygieneRowItem key={r.transitionId} row={r} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Th({
  children,
  align = 'left',
}: Readonly<{ children: React.ReactNode; align?: 'left' | 'right' }>) {
  return (
    <th
      className={`px-3 py-2 text-text-3 font-medium text-[10.5px] uppercase tracking-[0.08em] ${
        align === 'right' ? 'text-right' : 'text-left'
      }`}
    >
      {children}
    </th>
  );
}
