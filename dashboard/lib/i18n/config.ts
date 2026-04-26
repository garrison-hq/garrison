// Locale list for the dashboard. M3 ships English only — adding a
// second locale is a follow-up that requires only a translation
// catalog file under messages/, not component changes (FR-013).
//
// In test mode (GARRISON_TEST_MODE=1) we additionally enable a stub
// `zz` locale (catalog at tests/fixtures/i18n/zz.json, copied to
// messages/zz.json by the i18n integration test). It exercises the
// "swap the active locale and re-render every surface" code path
// without baking a second real translation into production
// builds.

const isTestMode = process.env.GARRISON_TEST_MODE === '1';

export const locales = (isTestMode ? (['en', 'zz'] as const) : (['en'] as const)) as readonly (
  | 'en'
  | 'zz'
)[];
export const defaultLocale = 'en' as const;
export type Locale = (typeof locales)[number];
