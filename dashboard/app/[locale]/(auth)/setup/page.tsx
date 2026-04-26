import { notFound, redirect } from 'next/navigation';
import { count } from 'drizzle-orm';
import { getTranslations } from 'next-intl/server';
import { appDb } from '@/lib/db/appClient';
import { users } from '@/drizzle/schema.dashboard';
import { auth } from '@/lib/auth';

// The empty-table check must run at request time, not build time —
// otherwise the static prerender attempts a Postgres connection
// during `next build` and fails when the DB isn't reachable.
export const dynamic = 'force-dynamic';

async function createInauguralOperator(formData: FormData) {
  'use server';

  // Re-verify the empty-table invariant inside the action so a
  // concurrent first-run race can't produce a second account.
  const [{ value }] = await appDb.select({ value: count() }).from(users);
  if (value > 0) {
    notFound();
  }

  const email = String(formData.get('email') ?? '').trim();
  const name = String(formData.get('name') ?? '').trim();
  const password = String(formData.get('password') ?? '');

  if (!email || !name || password.length < 8) {
    throw new Error('email, name, and an 8+ character password are required');
  }

  await auth.api.signUpEmail({
    body: { email, name, password },
  });

  redirect('/login');
}

export default async function SetupPage() {
  const [{ value }] = await appDb.select({ value: count() }).from(users);
  if (value > 0) {
    notFound();
  }
  const t = await getTranslations('auth.setup');

  return (
    <main className="min-h-screen flex items-center justify-center p-8">
      <form
        action={createInauguralOperator}
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

        <button
          type="submit"
          className="w-full bg-accent text-bg rounded px-3 py-2 font-medium"
        >
          {t('submit')}
        </button>
      </form>
    </main>
  );
}
