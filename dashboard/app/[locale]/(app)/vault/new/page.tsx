import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { SecretCreateForm } from '@/components/features/vault-secret-form/SecretCreateForm';
import { isVaultConfigured } from '@/lib/vault/infisicalClient';

// Vault create-secret route. Server Component resolves the
// operating entity's customer_id (single-tenant default) so
// SecretCreateForm can prefill a Rule-4-compliant path prefix.
// Falls back to a "configure vault" prompt when Infisical
// credentials are missing per the soft-default behaviour wired
// in lib/vault/infisicalClient.ts.

export const dynamic = 'force-dynamic';

export default async function VaultCreatePage() {
  if (!isVaultConfigured()) {
    return (
      <div className="px-6 py-5 max-w-[800px] mx-auto">
        <header className="space-y-1 mb-5">
          <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Create secret</h1>
        </header>
        <div className="rounded border border-warn/40 bg-warn/5 px-4 py-3 text-[12.5px] text-warn">
          The dashboard&apos;s Infisical credentials are not configured. See
          <code className="font-mono mx-1">docs/ops-checklist.md</code>
          M4 section to provision the
          <code className="font-mono mx-1">garrison-dashboard</code>
          Machine Identity, then set
          <code className="font-mono mx-1">INFISICAL_DASHBOARD_ML_CLIENT_ID</code> /
          <code className="font-mono mx-1">_SECRET</code> /
          <code className="font-mono mx-1">_PROJECT_ID</code> on the dashboard
          runtime and reload.
        </div>
      </div>
    );
  }

  // Resolve the single-tenant default customer id (matches the
  // server action's resolveCustomerId).
  const rows = await appDb.execute<{ id: string }>(sql`
    SELECT id FROM companies LIMIT 1
  `);
  const customerId = rows[0]?.id ?? '00000000-0000-0000-0000-000000000000';

  return (
    <div className="px-6 py-5 space-y-5 max-w-[800px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">Create secret</h1>
        <p className="text-text-3 text-[12px]">
          The value is sent to Infisical via the dashboard Machine Identity.
          The path is composed as <span className="font-mono">prefix/name</span>.
        </p>
      </header>
      <SecretCreateForm customerId={customerId} />
    </div>
  );
}
