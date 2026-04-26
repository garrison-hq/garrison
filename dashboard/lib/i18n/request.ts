import { getRequestConfig } from 'next-intl/server';
import { hasLocale } from 'next-intl';
import { routing } from './routing';

// Server-side locale resolver. next-intl's plugin generates a
// `next-intl/server` module that imports this file to load the
// catalog for the active locale on every Server Component render.
//
// Catalog source: `messages/<locale>.json`. Missing-key fallback
// to English happens at the component level via next-intl's
// fallback chain.
export default getRequestConfig(async ({ requestLocale }) => {
  const requested = await requestLocale;
  const locale = hasLocale(routing.locales, requested) ? requested : routing.defaultLocale;
  const messages = (await import(`../../messages/${locale}.json`)).default;
  return { locale, messages };
});
