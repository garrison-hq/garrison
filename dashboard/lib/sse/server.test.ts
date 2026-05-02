// M6 — server-side SSE scaffolding tests.
//
// Covers the pure helpers (frame + sseHeaders) extracted in M6 to
// dedupe the chat + throttle SSE routes. The full sseRoute() helper
// runs in the Node route-handler context (postgres + better-auth);
// the route-handler integration is exercised by the Playwright
// suite. Frame() is the externally-visible wire shape both routes
// produce, so it gets unit-pinned here.

import { describe, it, expect } from 'vitest';
import { frame, sseHeaders } from './server';

describe('lib/sse/server.frame', () => {
  it('TestFrameProducesNamedEventBlock', () => {
    const out = frame('throttle_event', { x: 1 });
    // SSE is line-delimited; each frame ends with a blank line.
    const lines = out.split('\n');
    expect(lines).toContain('event: throttle_event');
    expect(lines).toContain('data: {"x":1}');
    // Blank line terminator.
    expect(out.endsWith('\n\n')).toBe(true);
  });

  it('TestFrameIncludesIdWhenSupplied', () => {
    const out = frame('terminal', { ok: true }, 'sess:123');
    expect(out.split('\n')).toContain('id: sess:123');
  });

  it('TestFrameOmitsIdWhenAbsent', () => {
    const out = frame('delta', { seq: 0 });
    expect(out.split('\n').some((l) => l.startsWith('id:'))).toBe(false);
  });

  it('TestFrameSerializesArbitraryJSON', () => {
    const out = frame('throttle_event', {
      event_id: 'e1',
      kind: 'rate_limit_pause',
      payload: { back_off_seconds: 60 },
    });
    expect(out).toContain('"event_id":"e1"');
    expect(out).toContain('"back_off_seconds":60');
  });
});

describe('lib/sse/server.sseHeaders', () => {
  it('TestSSEHeadersCarryEventStreamContentType', () => {
    expect(sseHeaders['Content-Type']).toBe('text/event-stream');
  });

  it('TestSSEHeadersDisableCache', () => {
    expect(sseHeaders['Cache-Control']).toContain('no-cache');
  });

  it('TestSSEHeadersDisableProxyBuffering', () => {
    expect(sseHeaders['X-Accel-Buffering']).toBe('no');
  });
});
