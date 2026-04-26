import { describe, it, expect } from 'vitest';
import { routing } from './routing';

describe('lib/i18n/routing', () => {
  it('uses localePrefix as-needed so the default locale stays unprefixed', () => {
    expect(routing.localePrefix).toBe('as-needed');
  });

  it('binds the locales list to next-intl', () => {
    expect(routing.locales).toContain('en');
  });

  it('default locale is en', () => {
    expect(routing.defaultLocale).toBe('en');
  });
});
