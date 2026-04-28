import { describe, it, expect } from 'vitest';
import { scanForLeaks, hasLeak } from './leakScan';

describe('lib/vault/leakScan', () => {
  it('detects all 10 shape patterns', () => {
    const samples = [
      ['sk_prefix', 'sk-' + 'a'.repeat(30)],
      ['xoxb_prefix', 'xoxb-' + 'a'.repeat(30) + '-token'],
      ['aws_akia', 'AKIAIOSFODNN7EXAMPLE'],
      ['pem_header', '-----BEGIN PRIVATE KEY-----'],
      ['github_pat', 'ghp_' + 'a'.repeat(40)],
      ['github_app', 'gho_' + 'a'.repeat(40)],
      ['github_user', 'ghu_' + 'a'.repeat(40)],
      ['github_server', 'ghs_' + 'a'.repeat(40)],
      ['github_refresh', 'ghr_' + 'a'.repeat(40)],
      ['bearer_shape', 'Authorization: Bearer abc.def.ghi=='],
    ] as const;

    for (const [expectedLabel, sample] of samples) {
      const result = scanForLeaks(sample);
      expect(result.length, `expected at least one match for ${expectedLabel}`).toBeGreaterThan(0);
      expect(result.some((m) => m.label === expectedLabel)).toBe(true);
    }
  });

  it('detects verbatim secret values from fetchableValues regardless of shape', () => {
    // A value that doesn't match any shape pattern (no prefix, no
    // dashes, just an opaque token).
    const value = 'opaque-customer-secret-1234567890';
    const content = `Here is some context\n${value}\nmore context`;
    const result = scanForLeaks(content, [value]);
    expect(result).toEqual([
      {
        label: 'verbatim',
        offset: content.indexOf(value),
        length: value.length,
      },
    ]);
  });

  it('returns empty array for clean content', () => {
    const result = scanForLeaks('# Engineer agent\n\nUse $STRIPE_KEY to charge customers.');
    expect(result).toEqual([]);
  });

  it('tolerates UTF-8 / unicode boundary cases without crashing', () => {
    const content = '# Engineer 🛠️\n\n这是中文内容 ​ sk-' + 'a'.repeat(30) + ' ​';
    const result = scanForLeaks(content);
    expect(result.some((m) => m.label === 'sk_prefix')).toBe(true);
  });

  it('skips short fetchableValues to avoid over-matching', () => {
    const content = 'abcd appears twice: abcd';
    // 'abcd' is < 8 chars; should not match.
    const result = scanForLeaks(content, ['abcd']);
    expect(result).toEqual([]);
  });

  it('hasLeak returns true on any match, false on clean content', () => {
    expect(hasLeak('# clean')).toBe(false);
    expect(hasLeak('AKIAIOSFODNN7EXAMPLE')).toBe(true);
  });
});
