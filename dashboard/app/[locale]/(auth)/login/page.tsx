'use client';

import { Suspense, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { formField } from '@/lib/forms/formField';

// Login form. Posts to better-auth's sign-in/email endpoint via
// fetch (NOT Server Action) so the cookie set in the response
// reaches the browser's cookie jar without a server-side redirect
// dance. better-auth handles the cookie scheme.

export default function LoginPage() {
  return (
    <Suspense
      fallback={
        <main className="min-h-screen flex items-center justify-center p-8">
          <p className="text-text-2 text-sm">…</p>
        </main>
      }
    >
      <LoginForm />
    </Suspense>
  );
}

function LoginForm() {
  const t = useTranslations('auth.signIn');
  const router = useRouter();
  const search = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setPending(true);
    const fd = new FormData(e.currentTarget);
    try {
      const res = await fetch('/api/auth/sign-in/email', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: formField(fd, 'email'),
          password: formField(fd, 'password'),
        }),
        credentials: 'include',
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { message?: string };
        throw new Error(body.message ?? `sign-in failed (HTTP ${res.status})`);
      }
      const redirectTo = search.get('redirect') ?? '/';
      router.push(redirectTo);
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
        <h1 className="text-text-1 text-lg font-semibold">{t('heading')}</h1>

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
