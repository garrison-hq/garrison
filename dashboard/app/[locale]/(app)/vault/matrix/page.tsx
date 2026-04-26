import { getTranslations } from 'next-intl/server';
import { fetchRoleSecretMatrix } from '@/lib/queries/vault';
import { RoleSecretMatrix } from '@/components/features/vault/RoleSecretMatrix';
import { EmptyState } from '@/components/ui/EmptyState';

export const dynamic = 'force-dynamic';

export default async function VaultMatrixPage() {
  const matrix = await fetchRoleSecretMatrix();
  const t = await getTranslations('vault');

  return (
    <main className="p-6 space-y-4">
      <h1 className="text-text-1 text-lg font-semibold">{t('matrixHeading')}</h1>
      <nav className="flex gap-3 text-xs text-text-2">
        <a href="/vault" className="hover:text-text-1">{t('secretsTab')}</a>
        <a href="/vault/audit" className="hover:text-text-1">{t('auditTab')}</a>
        <a href="/vault/matrix" className="hover:text-text-1 underline">{t('matrixTab')}</a>
      </nav>
      {matrix.cells.length === 0 ? (
        <EmptyState description={t('emptyMatrix')} />
      ) : (
        <RoleSecretMatrix roles={matrix.roles} secrets={matrix.secrets} cells={matrix.cells} />
      )}
    </main>
  );
}
