import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { hasLocale, NextIntlClientProvider } from 'next-intl';
import { setRequestLocale } from 'next-intl/server';
import { routing } from '@/lib/i18n/routing';
import { getSession } from '@/lib/auth/session';
import { resolveTheme, type ThemePreference, type ResolvedTheme } from '@/lib/theme/resolve';

export const metadata: Metadata = {
  title: 'Garrison',
  description: 'Operator console for the Garrison agent orchestration system.',
};

// Pre-render every locale segment for static-route detection.
export function generateStaticParams() {
  return routing.locales.map((locale) => ({ locale }));
}

function isThemePref(v: unknown): v is ThemePreference {
  return v === 'dark' || v === 'light' || v === 'system';
}

// Locale-aware shell. Sets the html lang to the active locale and
// the data-theme attribute to the operator's resolved preference.
// Loads the catalog for Client Components via NextIntlClientProvider.
//
// System-pref detection: when the operator's saved value is
// 'system', we default to 'dark' on the server (matches the
// resolved-default in app/globals.css). The client-side
// ThemeSwitcher's optimistic update flips the attribute live when
// the operator changes preference.
export default async function LocaleLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ locale: string }>;
}) {
  const { locale } = await params;
  if (!hasLocale(routing.locales, locale)) {
    notFound();
  }
  setRequestLocale(locale);

  const session = await getSession();
  const stored = (session?.user as { themePreference?: unknown } | undefined)?.themePreference;
  const operatorPref: ThemePreference = isThemePref(stored) ? stored : 'system';
  const systemFallback: ResolvedTheme = 'dark';
  const dataTheme = resolveTheme(operatorPref, systemFallback);

  return (
    <html lang={locale} data-theme={dataTheme}>
      <body>
        <NextIntlClientProvider>{children}</NextIntlClientProvider>
      </body>
    </html>
  );
}
