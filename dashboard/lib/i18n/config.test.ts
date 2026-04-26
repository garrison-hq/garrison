import { describe, it, expect } from 'vitest';
import { locales, defaultLocale } from './config';

describe('lib/i18n/config', () => {
  it('exposes en as the default locale', () => {
    expect(defaultLocale).toBe('en');
  });

  it('locales list always contains en (M3 ships English-only by spec)', () => {
    expect(locales).toContain('en');
  });
});
