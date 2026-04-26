import { defineRouting } from 'next-intl/routing';
import { locales, defaultLocale } from './config';

// localePrefix: 'as-needed' means the default locale (en) renders
// at unprefixed URLs (`/login`, `/setup`, `/admin/invites`) while
// any future locale renders prefixed (`/zz/login`). Keeps the M3
// English-only deploy clean and the path layout matches what T006
// + T007 already shipped without any redirect dance.
export const routing = defineRouting({
  locales,
  defaultLocale,
  localePrefix: 'as-needed',
});
