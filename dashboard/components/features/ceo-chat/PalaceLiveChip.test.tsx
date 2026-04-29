// M5.2 — PalaceLiveChip threshold pins (FR-283).

import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { PalaceLiveChip, classifyAge } from './PalaceLiveChip';

function visible(html: string): string {
  return html.replace(/<!--\s*-->/g, '');
}

describe('PalaceLiveChip threshold', () => {
  it('TestPalaceLiveChipLiveUnder5Min', () => {
    expect(classifyAge(4 * 60_000)).toBe('live');
    const html = renderToString(<PalaceLiveChip ageMs={4 * 60_000} />);
    const v = visible(html);
    expect(v).toContain('palace live');
    expect(v).toContain('data-tone="live"');
  });

  it('TestPalaceLiveChipStaleAt15Min', () => {
    expect(classifyAge(15 * 60_000)).toBe('stale');
    const html = renderToString(<PalaceLiveChip ageMs={15 * 60_000} />);
    const v = visible(html);
    expect(v).toContain('palace stale');
    expect(v).toContain('data-tone="stale"');
  });

  it('TestPalaceLiveChipUnavailableOver30Min', () => {
    expect(classifyAge(31 * 60_000)).toBe('unavailable');
    const html = renderToString(<PalaceLiveChip ageMs={31 * 60_000} />);
    const v = visible(html);
    expect(v).toContain('palace unavailable');
    expect(v).toContain('data-tone="unavailable"');
  });

  it('TestPalaceLiveChipUnavailableNullAge', () => {
    expect(classifyAge(null)).toBe('unavailable');
    const html = renderToString(<PalaceLiveChip ageMs={null} />);
    const v = visible(html);
    expect(v).toContain('palace unavailable');
  });

  it('exact boundary at 5 min still reads live', () => {
    expect(classifyAge(5 * 60_000)).toBe('live');
  });

  it('exact boundary at 30 min still reads stale', () => {
    expect(classifyAge(30 * 60_000)).toBe('stale');
  });
});
