'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { Chip } from '@/components/ui/Chip';
import { SandboxEscapeDetail } from './SandboxEscapeDetail';
import { columnTextClass } from '@/lib/format/columnTone';
import { relativeTime, formatIsoFull, formatShortDateTime } from '@/lib/format/relativeTime';
import type { TransitionRow } from '@/lib/queries/ticketDetail';

// Single ticket-transition history row. Renders inside the
// HistoryBlock's bordered container; rows are siblings divided by
// the parent's divide-y rule, so this component intentionally has
// no border of its own.
//
// Failure rows (anything where hygieneStatus is non-clean) ALL get
// the warning glyph — operator feedback was that the inconsistent
// icon read as decoration. If a row also has sandbox-escape detail,
// the chevron at the end toggles a SandboxEscapeDetail panel.

function statusTone(status: string | null): 'neutral' | 'warn' | 'err' | 'ok' {
  if (!status || status === 'clean') return 'ok';
  if (status === 'finalize_committed' || status === 'committed') return 'ok';
  if (status.startsWith('suspected_')) return 'err';
  if (status.startsWith('finalize_')) return 'warn';
  return 'warn';
}

function isFailure(status: string | null): boolean {
  if (!status || status === 'clean') return false;
  if (status === 'finalize_committed' || status === 'committed') return false;
  return true;
}

export function HistoryRow({ row }: Readonly<{ row: TransitionRow }>) {
  const t = useTranslations('ticketDetail');
  const commonT = useTranslations('common');
  const errT = useTranslations('errors');
  const [open, setOpen] = useState(false);
  const fromCls = row.fromColumn ? columnTextClass(row.fromColumn) : 'text-text-3';
  const toCls = columnTextClass(row.toColumn);
  const failure = isFailure(row.hygieneStatus);
  const expandable = row.sandboxEscape && row.sandboxEscapeDetail;

  return (
    <div>
      <div
        className="grid items-center gap-3 px-3 py-2.5 text-[12px]"
        style={{ gridTemplateColumns: '110px 1fr auto auto' }}
      >
        <span
          className="font-mono font-tabular text-text-3"
          title={formatIsoFull(row.at)}
        >
          {relativeTime(row.at)}
        </span>
        <span className="font-mono flex items-center gap-1.5 truncate">
          <span className={fromCls}>{row.fromColumn ?? '—'}</span>
          <span className="text-text-4" aria-hidden>→</span>
          <span className={toCls}>{row.toColumn}</span>
        </span>
        <span className="flex items-center gap-1.5">
          {failure ? (
            <span
              className="text-warn"
              title={errT.has('sandbox_escape.tooltip') ? errT('sandbox_escape.tooltip') : t('sandboxEscape')}
              data-testid="failure-icon"
              aria-label="failure"
            >
              ⚠
            </span>
          ) : null}
          <Chip tone={statusTone(row.hygieneStatus)}>
            {row.hygieneStatus ?? 'clean'}
          </Chip>
        </span>
        {expandable ? (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="text-text-3 hover:text-text-1 text-xs px-1 leading-none"
            aria-expanded={open}
            aria-label={open ? commonT('collapse') : commonT('expand')}
            title={formatShortDateTime(row.at)}
          >
            <span className={`inline-block transition-transform ${open ? 'rotate-90' : ''}`}>
              ▸
            </span>
          </button>
        ) : (
          <span className="w-4" aria-hidden />
        )}
      </div>
      {expandable && open && row.sandboxEscapeDetail ? (
        <div className="px-3 pb-3 pt-1 border-t border-border-1 bg-surface-2">
          <SandboxEscapeDetail
            claimedPath={row.sandboxEscapeDetail.claimedPath}
            onDiskPath={row.sandboxEscapeDetail.onDiskPath}
          />
        </div>
      ) : null}
    </div>
  );
}
