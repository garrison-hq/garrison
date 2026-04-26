import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { hasLocale, NextIntlClientProvider } from 'next-intl';
import { setRequestLocale } from 'next-intl/server';
import { routing } from '@/lib/i18n/routing';

export const metadata: Metadata = {
  title: 'Garrison',
  description: 'Operator console for the Garrison agent orchestration system.',
};

// Pre-render every locale segment for static-route detection.
export function generateStaticParams() {
  return routing.locales.map((locale) => ({ locale }));
}

// Locale-aware shell. Sets the html lang to the active locale and
// loads the catalog for Client Components via NextIntlClientProvider.
// T009 will replace data-theme="dark" with the operator's saved
// theme preference (resolved via getSession()); for T008 the
// fallback dark default is sufficient.
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

  return (
    <html lang={locale} data-theme="dark">
      <body>
        <NextIntlClientProvider>{children}</NextIntlClientProvider>
      </body>
    </html>
  );
}
