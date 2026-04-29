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
    // Unavailable now reserved for the genuinely-cold path — a
    // mempalace call DID happen but it was over 30 minutes ago.
    expect(classifyAge(31 * 60_000)).toBe('unavailable');
    const html = renderToString(<PalaceLiveChip ageMs={31 * 60_000} />);
    const v = visible(html);
    expect(v).toContain('palace unavailable');
    expect(v).toContain('data-tone="unavailable"');
  });

  it('TestPalaceLiveChipIdleNullAge', () => {
    // Null age = no mempalace call has happened yet in this thread.
    // Renders as muted "idle" rather than the harsh err-toned
    // "unavailable" — the surface isn't broken, the tool just hasn't
    // been invoked yet.
    expect(classifyAge(null)).toBe('idle');
    const html = renderToString(<PalaceLiveChip ageMs={null} />);
    const v = visible(html);
    expect(v).toContain('palace idle');
    expect(v).toContain('data-tone="idle"');
  });

  it('exact boundary at 5 min still reads live', () => {
    expect(classifyAge(5 * 60_000)).toBe('live');
  });

  it('exact boundary at 30 min still reads stale', () => {
    expect(classifyAge(30 * 60_000)).toBe('stale');
  });

  it('renders all four label/tone combinations distinctly', () => {
    const live = visible(renderToString(<PalaceLiveChip ageMs={1_000} />));
    const stale = visible(renderToString(<PalaceLiveChip ageMs={10 * 60_000} />));
    const unavailable = visible(renderToString(<PalaceLiveChip ageMs={31 * 60_000} />));
    const idle = visible(renderToString(<PalaceLiveChip ageMs={null} />));
    // Each variant ends up with a distinct label.
    expect(live).toContain('palace live');
    expect(stale).toContain('palace stale');
    expect(unavailable).toContain('palace unavailable');
    expect(idle).toContain('palace idle');
    // And distinct StatusDot tones via Chip wrapping.
    expect(live.toLowerCase()).toContain('ok');
    expect(stale.toLowerCase()).toContain('warn');
    expect(unavailable.toLowerCase()).toContain('err');
    expect(idle.toLowerCase()).toContain('info');
  });
});
