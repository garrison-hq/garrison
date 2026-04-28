import { sql } from 'drizzle-orm';
import { getTranslations } from 'next-intl/server';
import {
  fetchRoleSecretMatrix,
  fetchAllGrants,
  fetchSecretsList,
} from '@/lib/queries/vault';
import { appDb } from '@/lib/db/appClient';
import { RoleSecretMatrix } from '@/components/features/vault/RoleSecretMatrix';
import { VaultTabs } from '@/components/features/vault/VaultTabs';
import { GrantEditor } from '@/components/features/vault-grant-edit/GrantEditor';
import { EmptyState } from '@/components/ui/EmptyState';

export const dynamic = 'force-dynamic';

export default async function VaultMatrixPage() {
  const [matrix, grants, secrets, t] = await Promise.all([
    fetchRoleSecretMatrix(),
    fetchAllGrants(),
    fetchSecretsList(),
    getTranslations('vault'),
  ]);

  // Distinct role slugs come from agents — the operator may want
  // to grant to a role that doesn't yet have any grants. Pull
  // the active-agent role list so the dropdown covers all
  // configured roles.
  const roleRows = await appDb.execute<{ role_slug: string }>(sql`
    SELECT DISTINCT role_slug FROM agents WHERE status = 'active' ORDER BY role_slug
  `);
  const roleSlugs = roleRows.map((r) => r.role_slug);
  const secretPaths = secrets.map((s) => s.secretPath);

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1600px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          {t('matrixHeading')}
        </h1>
        <p className="text-text-3 text-xs font-tabular">
          <span className="font-mono">{matrix.roles.length}</span>{' '}
          {t('rolesCount')}
          {' · '}
          <span className="font-mono">{matrix.secrets.length}</span>{' '}
          {t('secretsCount')}
          {' · '}
          <span className="font-mono">{matrix.cells.length}</span>{' '}
          {t('grantsCount')}
        </p>
      </header>
      <VaultTabs active="matrix" />
      {matrix.cells.length === 0 ? (
        <section className="bg-surface-1 border border-border-1 rounded">
          <EmptyState description={t('emptyMatrix')} />
        </section>
      ) : (
        <RoleSecretMatrix
          roles={matrix.roles}
          secrets={matrix.secrets}
          cells={matrix.cells}
        />
      )}
      <GrantEditor
        grants={grants}
        roleSlugs={roleSlugs}
        secretPaths={secretPaths}
      />
    </div>
  );
}
