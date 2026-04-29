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

  it('TestPalaceLiveChipIdleAt15Min', () => {
    // 5-30min range maps to 'idle' (was 'stale' pre-Apr-29 polish).
    expect(classifyAge(15 * 60_000)).toBe('idle');
    const html = renderToString(<PalaceLiveChip ageMs={15 * 60_000} />);
    const v = visible(html);
    expect(v).toContain('palace idle');
    expect(v).toContain('data-tone="idle"');
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

  it('exact boundary at 30 min still reads idle', () => {
    expect(classifyAge(30 * 60_000)).toBe('idle');
  });

  it('renders all three label/tone combinations distinctly', () => {
    const live = visible(renderToString(<PalaceLiveChip ageMs={1_000} />));
    const idleRecent = visible(renderToString(<PalaceLiveChip ageMs={10 * 60_000} />));
    const idleNull = visible(renderToString(<PalaceLiveChip ageMs={null} />));
    const unavailable = visible(renderToString(<PalaceLiveChip ageMs={31 * 60_000} />));
    expect(live).toContain('palace live');
    expect(idleRecent).toContain('palace idle');
    expect(idleNull).toContain('palace idle');
    expect(unavailable).toContain('palace unavailable');
    // StatusDot tones via Chip wrapping.
    expect(live.toLowerCase()).toContain('ok');
    expect(idleRecent.toLowerCase()).toContain('info');
    expect(idleNull.toLowerCase()).toContain('info');
    expect(unavailable.toLowerCase()).toContain('err');
  });
});
