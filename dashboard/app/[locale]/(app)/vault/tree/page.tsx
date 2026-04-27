import { fetchSecretsList } from '@/lib/queries/vault';
import { VaultTabs } from '@/components/features/vault/VaultTabs';
import { PathTreeNavigator } from '@/components/features/vault/PathTreeNavigator';
import { EmptyState } from '@/components/ui/EmptyState';

// /vault/tree — hierarchical view of every vault path. Leaves are
// real secrets; internal nodes are prefix-only buckets. M4 surface
// per FR-085 / FR-086.
//
// Click on a leaf → navigate to the secret edit page via the
// PathTreeNavigator client wrapper.

export const dynamic = 'force-dynamic';

export default async function VaultTreePage() {
  const rows = await fetchSecretsList();
  const paths = rows.map((r) => r.secretPath);

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1200px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          Path tree
        </h1>
        <p className="text-text-3 text-xs">
          <span className="font-mono font-tabular">{paths.length}</span>{' '}
          paths organised by prefix. Click a leaf to edit the secret.
        </p>
      </header>
      <VaultTabs active="secrets" />
      <section className="bg-surface-1 border border-border-1 rounded p-4">
        {paths.length === 0 ? (
          <EmptyState description="no secrets to organise" />
        ) : (
          <PathTreeNavigator paths={paths} />
        )}
      </section>
    </div>
  );
}
