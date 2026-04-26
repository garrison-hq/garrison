import { getTranslations } from 'next-intl/server';
import { fetchSecretsList } from '@/lib/queries/vault';
import { SecretsList } from '@/components/features/vault/SecretsList';
import { VaultTabs } from '@/components/features/vault/VaultTabs';
import { EmptyState } from '@/components/ui/EmptyState';
import { relativeTime, formatIsoFull } from '@/lib/format/relativeTime';

export const dynamic = 'force-dynamic';

export default async function VaultLandingPage() {
  const rows = await fetchSecretsList();
  const t = await getTranslations('vault');

  // Latest rotation timestamp across all tracked secrets — drives
  // the meta line under the page title ("X tracked · last rotation
  // sweep <relative>"). Same shape as the org-overview and kanban
  // header subtitles.
  const lastRotation = rows
    .map((r) => r.lastRotatedAt)
    .filter((d): d is Date => Boolean(d))
    .reduce<Date | null>((acc, d) => (!acc || d > acc ? d : acc), null);

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          {t('secretsHeading')}
        </h1>
        <p className="text-text-3 text-xs">
          <span className="font-mono font-tabular">{rows.length}</span>{' '}
          {t('tracked')}
          {lastRotation ? (
            <>
              {' · '}
              {t('lastRotationSweep')}{' '}
              <span className="font-mono" title={formatIsoFull(lastRotation)}>
                {relativeTime(lastRotation)}
              </span>
            </>
          ) : null}
        </p>
      </header>
      <VaultTabs active="secrets" />
      <section className="bg-surface-1 border border-border-1 rounded overflow-hidden">
        <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
          <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
            {t('secretsTab')}
          </span>
          <span className="text-text-3 text-[11px] font-mono font-tabular">
            {rows.length}
          </span>
        </header>
        {rows.length === 0 ? (
          <EmptyState description={t('emptySecrets')} />
        ) : (
          <SecretsList rows={rows} />
        )}
      </section>
    </div>
  );
}
