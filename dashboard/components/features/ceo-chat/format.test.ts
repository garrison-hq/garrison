// M5.2 — format.ts unit tests (plan §1.9 + §1.11).

import { describe, it, expect } from 'vitest';
import {
  formatThreadTitle,
  formatSessionCost,
  formatPerMessageCost,
  formatTimeAgo,
  formatModelBadge,
} from './format';

describe('formatThreadTitle', () => {
  it('formats per-operator counter as "thread #N"', () => {
    expect(formatThreadTitle(1)).toBe('thread #1');
    expect(formatThreadTitle(142)).toBe('thread #142');
  });
});

describe('formatSessionCost', () => {
  it('renders 2-decimal currency for plain numbers', () => {
    expect(formatSessionCost(0.1432)).toBe('$0.14');
    expect(formatSessionCost(2.5)).toBe('$2.50');
  });

  it('accepts NUMERIC string (Drizzle returns NUMERIC as string)', () => {
    expect(formatSessionCost('1.234567')).toBe('$1.23');
  });

  it('renders $0.00 for null / undefined / NaN', () => {
    expect(formatSessionCost(null)).toBe('$0.00');
    expect(formatSessionCost(undefined)).toBe('$0.00');
    expect(formatSessionCost('not a number')).toBe('$0.00');
  });
});

describe('formatPerMessageCost', () => {
  it('renders 4-decimal currency', () => {
    expect(formatPerMessageCost(0.0234)).toBe('$0.0234');
    expect(formatPerMessageCost('0.001')).toBe('$0.0010');
  });

  it('returns null for null / undefined (operator rows have NULL cost)', () => {
    expect(formatPerMessageCost(null)).toBeNull();
    expect(formatPerMessageCost(undefined)).toBeNull();
  });

  it('rounds to 4 decimals — small turns can read as $0.0000', () => {
    expect(formatPerMessageCost(0.000004)).toBe('$0.0000');
  });
});

describe('formatTimeAgo', () => {
  const now = new Date('2026-04-29T12:00:00Z').getTime();

  it('renders "just now" for <=5s old', () => {
    expect(formatTimeAgo(new Date(now - 1000), now)).toBe('just now');
  });

  it('renders Ns ago for <60s', () => {
    expect(formatTimeAgo(new Date(now - 30_000), now)).toBe('30s ago');
  });

  it('renders Nm ago for <60m', () => {
    expect(formatTimeAgo(new Date(now - 5 * 60_000), now)).toBe('5m ago');
  });

  it('renders Nh ago for <24h', () => {
    expect(formatTimeAgo(new Date(now - 3 * 60 * 60_000), now)).toBe('3h ago');
  });

  it('renders Nd ago for <30d', () => {
    expect(formatTimeAgo(new Date(now - 5 * 24 * 60 * 60_000), now)).toBe('5d ago');
  });

  it('falls back to ISO date for >30d', () => {
    expect(formatTimeAgo(new Date(now - 60 * 24 * 60 * 60_000), now)).toBe('2026-02-28');
  });
});

describe('formatModelBadge', () => {
  it('returns the model name verbatim', () => {
    expect(formatModelBadge('claude-sonnet-4-6')).toBe('claude-sonnet-4-6');
  });

  it('returns "model n/a" for null / empty', () => {
    expect(formatModelBadge(null)).toBe('model n/a');
    expect(formatModelBadge('')).toBe('model n/a');
    expect(formatModelBadge('   ')).toBe('model n/a');
  });

  it('returns "model n/a" for undefined', () => {
    expect(formatModelBadge(undefined)).toBe('model n/a');
  });

  it('trims surrounding whitespace from the model name', () => {
    expect(formatModelBadge('  claude-haiku  ')).toBe('claude-haiku');
  });
});

describe('formatTimeAgo edge cases', () => {
  it('treats negative deltas as "just now" (clock skew tolerance)', () => {
    const now = new Date('2026-04-29T12:00:00Z').getTime();
    expect(formatTimeAgo(new Date(now + 5_000), now)).toBe('just now');
  });

  it('accepts string + number inputs equivalently', () => {
    const now = new Date('2026-04-29T12:00:00Z').getTime();
    const past = new Date(now - 30_000);
    expect(formatTimeAgo(past, now)).toBe('30s ago');
    expect(formatTimeAgo(past.toISOString(), now)).toBe('30s ago');
    expect(formatTimeAgo(past.getTime(), now)).toBe('30s ago');
  });
});
