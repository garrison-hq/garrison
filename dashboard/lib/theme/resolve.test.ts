import { describe, it, expect } from 'vitest';
import { resolveTheme } from './resolve';

describe('lib/theme/resolve', () => {
  it('resolves system preference when operator’s theme_preference is "system"', () => {
    expect(resolveTheme('system', 'dark')).toBe('dark');
    expect(resolveTheme('system', 'light')).toBe('light');
  });

  it('resolves explicit preference over system when operator’s theme_preference is "dark" or "light"', () => {
    expect(resolveTheme('dark', 'light')).toBe('dark');
    expect(resolveTheme('light', 'dark')).toBe('light');
  });
});
