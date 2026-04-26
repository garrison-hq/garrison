import { getTranslations } from 'next-intl/server';
import { fetchSecretsList } from '@/lib/queries/vault';
import { SecretsList } from '@/components/features/vault/SecretsList';
import { EmptyState } from '@/components/ui/EmptyState';

export const dynamic = 'force-dynamic';

export default async function VaultLandingPage() {
  const rows = await fetchSecretsList();
  const t = await getTranslations('vault');

  return (
    <main className="p-6 space-y-4">
      <h1 className="text-text-1 text-lg font-semibold">{t('secretsHeading')}</h1>
      <nav className="flex gap-3 text-xs text-text-2">
        <a href="/vault" className="hover:text-text-1 underline">{t('secretsTab')}</a>
        <a href="/vault/audit" className="hover:text-text-1">{t('auditTab')}</a>
        <a href="/vault/matrix" className="hover:text-text-1">{t('matrixTab')}</a>
      </nav>
      {rows.length === 0 ? (
        <EmptyState description={t('emptySecrets')} />
      ) : (
        <SecretsList rows={rows} />
      )}
    </main>
  );
}
