import { describe, it, expect } from 'vitest';
import { matchesConfirmation } from './ConfirmDialog';

describe('components/ui/ConfirmDialog', () => {
  it('typedNameConfirmRequiresExactMatch', () => {
    expect(matchesConfirmation('foo', 'foo')).toBe(true);
    expect(matchesConfirmation('foo ', 'foo')).toBe(false);
    expect(matchesConfirmation('Foo', 'foo')).toBe(false);
    expect(matchesConfirmation('', 'foo')).toBe(false);
    expect(matchesConfirmation('/cust/operator/api_key', '/cust/operator/api_key')).toBe(true);
  });
});
