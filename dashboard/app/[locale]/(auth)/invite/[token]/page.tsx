'use client';

import { use, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { formField } from '@/lib/forms/formField';

// Invite-redemption form. Public route (allow-listed in middleware).
// Posts to /api/invites/redeem; on success, the response carries a
// Set-Cookie that logs the new operator in, and we redirect to the
// org overview at /.
//
// The token is read from the URL pathname; the operator never types
// it. better-auth's session cookie is set by the redeem route's
// forwarded headers.

export default function RedeemInvitePage({
  params,
}: Readonly<{
  params: Promise<{ token: string }>;
}>) {
  const { token } = use(params);
  const t = useTranslations('auth.invite');
  const router = useRouter();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setPending(true);
    const fd = new FormData(e.currentTarget);
    try {
      const res = await fetch('/api/invites/redeem', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          token,
          name: formField(fd, 'name'),
          email: formField(fd, 'email'),
          password: formField(fd, 'password'),
        }),
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { error?: string };
        throw new Error(body.error ?? `redeem failed (HTTP ${res.status})`);
      }
      router.push('/');
      router.refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="min-h-screen flex items-center justify-center p-8">
      <form
        onSubmit={onSubmit}
        className="w-full max-w-sm space-y-4 bg-surface-1 border border-border-1 rounded p-6"
      >
        <div className="space-y-1">
          <h1 className="text-text-1 text-lg font-semibold">{t('heading')}</h1>
          <p className="text-text-3 text-xs">{t('description')}</p>
        </div>

        <label className="block space-y-1">
          <span className="text-text-2 text-xs">{t('name')}</span>
          <input
            name="name"
            type="text"
            required
            className="w-full bg-surface-2 border border-border-1 rounded px-2 py-1.5 text-text-1"
          />
        </label>

        <label className="block space-y-1">
          <span className="text-text-2 text-xs">{t('email')}</span>
          <input
            name="email"
            type="email"
            required
            className="w-full bg-surface-2 border border-border-1 rounded px-2 py-1.5 text-text-1"
          />
        </label>

        <label className="block space-y-1">
          <span className="text-text-2 text-xs">{t('password')}</span>
          <input
            name="password"
            type="password"
            required
            minLength={8}
            className="w-full bg-surface-2 border border-border-1 rounded px-2 py-1.5 text-text-1"
          />
        </label>

        {error ? <p className="text-err text-xs">{error}</p> : null}

        <button
          type="submit"
          disabled={pending}
          className="w-full bg-accent text-bg rounded px-3 py-2 font-medium disabled:opacity-60"
        >
          {pending ? t('submitting') : t('submit')}
        </button>
      </form>
    </main>
  );
}
