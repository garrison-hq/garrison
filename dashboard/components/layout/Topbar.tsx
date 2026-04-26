import { getSession } from '@/lib/auth/session';
import { getTranslations } from 'next-intl/server';
import { ThemeSwitcher } from '@/components/ui/ThemeSwitcher';
import { LocaleSwitcher } from '@/components/ui/LocaleSwitcher';
import type { ThemePreference } from '@/lib/theme/resolve';
import { LogOutButton } from '@/components/layout/LogOutButton';

// Topbar — current operator's email, theme switcher, locale
// switcher, log-out button. Server-rendered so the operator email
// + saved theme preference are present on first paint.

function isThemePref(v: unknown): v is ThemePreference {
  return v === 'dark' || v === 'light' || v === 'system';
}

export async function Topbar() {
  const session = await getSession();
  const t = await getTranslations('common');
  const themePref: ThemePreference = isThemePref(
    (session?.user as { themePreference?: unknown } | undefined)?.themePreference,
  )
    ? ((session!.user as { themePreference: ThemePreference }).themePreference)
    : 'system';
  const email = session?.user.email ?? '';

  return (
    <header className="h-12 bg-surface-1 border-b border-border-1 flex items-center px-4 gap-3 text-sm">
      <div className="flex-1" />
      <ThemeSwitcher initial={themePref} />
      <LocaleSwitcher />
      {email ? <span className="text-text-2 text-xs font-mono">{email}</span> : null}
      <LogOutButton label={t('logOut')} />
    </header>
  );
}
