import type { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { hasLocale, NextIntlClientProvider } from 'next-intl';
import { setRequestLocale } from 'next-intl/server';
import { routing } from '@/lib/i18n/routing';
import { LocaleSync } from './LocaleSync';

export const metadata: Metadata = {
  title: 'Garrison',
  description: 'Operator console for the Garrison agent orchestration system.',
};

// Pre-render every locale segment for static-route detection.
export function generateStaticParams() {
  return routing.locales.map((locale) => ({ locale }));
}

// Locale-aware layout. The root layout (app/layout.tsx) owns the
// <html data-theme=...> shell; this layer wraps children in the
// next-intl provider and synchronises <html lang> via a tiny
// client island so it tracks the active locale.
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
    <NextIntlClientProvider>
      <LocaleSync locale={locale} />
      {children}
    </NextIntlClientProvider>
  );
}
