import { headers } from 'next/headers';
import { getTranslations } from 'next-intl/server';
import { listPendingInvites } from '@/lib/auth/invites';
import { PendingInvitesList } from '@/components/features/invites/PendingInvitesList';
import { GenerateInviteForm } from '@/components/features/invites/GenerateInviteForm';

// Operator-side admin surface for inviting an additional operator.
// The middleware (T006) gates this page behind a session cookie;
// the listing query runs as the dashboard's app role.

export const dynamic = 'force-dynamic';

export default async function AdminInvitesPage() {
  const invites = await listPendingInvites();
  const h = await headers();
  const proto = h.get('x-forwarded-proto') ?? 'http';
  const host = h.get('host') ?? 'localhost';
  const baseUrl = `${proto}://${host}`;
  const t = await getTranslations('auth.admin');

  return (
    <div className="max-w-2xl mx-auto p-8 space-y-6">
      <div>
        <h1 className="text-text-1 text-lg font-semibold">{t('heading')}</h1>
        <p className="text-text-3 text-xs">{t('description')}</p>
      </div>
      <GenerateInviteForm />
      <PendingInvitesList invites={invites} baseUrl={baseUrl} />
    </div>
  );
}
