import { notFound } from 'next/navigation';
import { fetchSecretForEdit } from '@/lib/queries/vault';
import { SecretEditForm } from '@/components/features/vault-secret-form/SecretEditForm';
import type { SecretProvenance, RotationProvider } from '@/lib/actions/vault';

// Vault secret edit route. Catch-all `[...path]` captures the
// multi-segment secret_path (e.g.
// /<customer_id>/operator/stripe_key) into params.path as an
// array; we re-join with leading slash. Server Component reads
// the current state via fetchSecretForEdit and hydrates the
// SecretEditForm Client Component which calls editSecret.

export const dynamic = 'force-dynamic';

interface RouteParams {
  params: Promise<{ path: string[]; locale: string }>;
}

export default async function VaultEditPage({ params }: Readonly<RouteParams>) {
  const { path } = await params;
  const secretPath = '/' + path.join('/');
  const snapshot = await fetchSecretForEdit(secretPath);
  if (!snapshot) {
    notFound();
  }

  return (
    <div className="px-6 py-5 space-y-5 max-w-[800px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          Edit secret
        </h1>
        <p className="text-text-3 text-[12px]">
          Optimistic locking via <code className="font-mono">updated_at</code>;
          stale saves surface a conflict-resolution modal.
        </p>
      </header>
      <SecretEditForm
        initial={{
          secretPath: snapshot.secretPath,
          provenance: snapshot.provenance as SecretProvenance,
          rotationCadence: snapshot.rotationCadence,
          rotationProvider: snapshot.rotationProvider as RotationProvider,
          updatedAt: snapshot.updatedAt,
        }}
      />
    </div>
  );
}
