import { getTranslations } from 'next-intl/server';
import { fetchAuditLog } from '@/lib/queries/vault';
import { AuditLog } from '@/components/features/vault/AuditLog';
import { VaultTabs } from '@/components/features/vault/VaultTabs';
import { EmptyState } from '@/components/ui/EmptyState';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';

export const dynamic = 'force-dynamic';

export default async function VaultAuditPage({
  searchParams,
}: Readonly<{
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}>) {
  const sp = await searchParams;
  const roleSlug = typeof sp.role === 'string' ? sp.role : undefined;
  const ticketId = typeof sp.ticket === 'string' ? sp.ticket : undefined;
  const cursor = typeof sp.cursor === 'string' ? sp.cursor : undefined;

  const [{ rows }, t] = await Promise.all([
    fetchAuditLog({ roleSlug, ticketId, cursor }),
    getTranslations('vault'),
  ]);

  const lastEvent = rows.length > 0 ? rows[0].timestamp : null;

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          {t('auditHeading')}
        </h1>
        <p className="text-text-3 text-xs">
          <span className="font-mono font-tabular">{rows.length}</span>{' '}
          {t('events')}
          {lastEvent ? (
            <>
              {' · '}
              {t('lastEvent')}{' '}
              <span className="font-mono" title={formatIsoFull(lastEvent)}>
                {relativeTime(lastEvent)}
              </span>
            </>
          ) : null}
        </p>
      </header>
      <VaultTabs active="audit" />
      <section className="bg-surface-1 border border-border-1 rounded overflow-hidden">
        <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
          <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
            {t('auditTab')}
          </span>
          <span className="text-text-3 text-[11px] font-mono font-tabular">
            {rows.length}
          </span>
        </header>
        {rows.length === 0 ? (
          <EmptyState description={t('emptyAudit')} />
        ) : (
          <AuditLog rows={rows} />
        )}
      </section>
      <SoftPoll intervalMs={30_000} />
    </div>
  );
}
