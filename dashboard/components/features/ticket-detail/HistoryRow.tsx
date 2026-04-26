'use client';

import { useTranslations } from 'next-intl';
import { Chip } from '@/components/ui/Chip';
import { ExpandRow } from '@/components/ui/ExpandRow';
import { SandboxEscapeDetail } from './SandboxEscapeDetail';
import type { TransitionRow } from '@/lib/queries/ticketDetail';

// Single ticket-transition history row. Sandbox-escape rows render
// with a distinct icon + tooltip and an expand control that
// reveals SandboxEscapeDetail per FR-055; non-escape rows render
// plainly.

function statusTone(status: string | null): 'neutral' | 'warn' | 'err' | 'ok' {
  if (!status || status === 'clean') return 'ok';
  if (status === 'finalize_committed' || status === 'committed') return 'ok';
  if (status.startsWith('suspected_')) return 'err';
  if (status.startsWith('finalize_')) return 'warn';
  return 'warn';
}

export function HistoryRow({ row }: Readonly<{ row: TransitionRow }>) {
  const t = useTranslations('ticketDetail');
  const errT = useTranslations('errors');
  const summary = (
    <div className="flex items-center gap-3 text-xs">
      <span className="font-mono text-text-3 w-32">
        {new Date(row.at).toISOString().slice(0, 16)}Z
      </span>
      <span className="font-mono text-text-2">
        {row.fromColumn ?? '—'} → {row.toColumn}
      </span>
      <Chip tone={statusTone(row.hygieneStatus)}>{row.hygieneStatus ?? 'clean'}</Chip>
      {row.sandboxEscape ? (
        <span
          className="text-warn cursor-help"
          title={errT.has('sandbox_escape.tooltip') ? errT('sandbox_escape.tooltip') : t('sandboxEscape')}
          data-testid="sandbox-escape-icon"
          aria-label="sandbox-escape"
        >
          ⚠
        </span>
      ) : null}
    </div>
  );

  if (row.sandboxEscape && row.sandboxEscapeDetail) {
    return (
      <ExpandRow
        summary={summary}
        detail={
          <SandboxEscapeDetail
            claimedPath={row.sandboxEscapeDetail.claimedPath}
            onDiskPath={row.sandboxEscapeDetail.onDiskPath}
          />
        }
      />
    );
  }
  return (
    <div className="px-3 py-2 border border-border-1 rounded bg-surface-1">{summary}</div>
  );
}
