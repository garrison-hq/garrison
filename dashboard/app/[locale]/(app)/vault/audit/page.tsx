import { getTranslations } from 'next-intl/server';
import { fetchAuditLog } from '@/lib/queries/vault';
import { AuditLog } from '@/components/features/vault/AuditLog';
import { EmptyState } from '@/components/ui/EmptyState';
import { SoftPoll } from '@/components/features/org-overview/SoftPoll';

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

  return (
    <main className="p-6 space-y-4">
      <h1 className="text-text-1 text-lg font-semibold">{t('auditHeading')}</h1>
      <nav className="flex gap-3 text-xs text-text-2">
        <a href="/vault" className="hover:text-text-1">{t('secretsTab')}</a>
        <a href="/vault/audit" className="hover:text-text-1 underline">{t('auditTab')}</a>
        <a href="/vault/matrix" className="hover:text-text-1">{t('matrixTab')}</a>
      </nav>
      {rows.length === 0 ? (
        <EmptyState description={t('emptyAudit')} />
      ) : (
        <AuditLog rows={rows} />
      )}
      <SoftPoll intervalMs={30_000} />
    </main>
  );
}
