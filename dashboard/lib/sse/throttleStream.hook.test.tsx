// @vitest-environment jsdom
//
// M6 — useThrottleStream hook lifecycle smoke test.
//
// Pure-store coverage lives in throttleStream.test.ts. This file
// covers the React hook surface (mount → EventSource open + listen
// → unmount → close) by stubbing the global EventSource constructor
// so the test never opens a real connection. Mirrors the M5.x
// chatStream pattern of "test the pure surface without bringing
// jsdom in" — but for the hook half, jsdom is required to give us
// the React reconciler.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, cleanup } from '@testing-library/react';

import { useThrottleStream } from './throttleStream';

interface FakeEventSource {
  url: string;
  closed: boolean;
  listeners: Map<string, Array<(ev: MessageEvent) => void>>;
  onopen: (() => void) | null;
  onerror: (() => void) | null;
}

let fakes: FakeEventSource[] = [];

beforeEach(() => {
  fakes = [];

  class FakeES implements FakeEventSource {
    url: string;
    closed = false;
    listeners = new Map<string, Array<(ev: MessageEvent) => void>>();
    onopen: (() => void) | null = null;
    onerror: (() => void) | null = null;
    constructor(url: string) {
      this.url = url;
      fakes.push(this);
    }
    addEventListener(name: string, fn: (ev: MessageEvent) => void) {
      const list = this.listeners.get(name) ?? [];
      list.push(fn);
      this.listeners.set(name, list);
    }
    close() {
      this.closed = true;
    }
  }

  // Inject the fake into the jsdom global so the hook's
  // `new EventSource(...)` resolves to FakeES.
  (globalThis as unknown as { EventSource: typeof FakeES }).EventSource = FakeES;
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('useThrottleStream', () => {
  it('TestUseThrottleStreamOpensEventSource', () => {
    const { result } = renderHook(() => useThrottleStream());
    expect(result.current.events).toEqual([]);
    expect(result.current.lastError).toBeNull();
    // The hook's useEffect runs synchronously in renderHook's
    // strict-mode-aware wrapper.
    expect(fakes).toHaveLength(1);
    expect(fakes[0].url).toBe('/api/sse/throttle');
  });

  it('TestUseThrottleStreamRegistersThrottleEventListener', () => {
    renderHook(() => useThrottleStream());
    const fake = fakes[0];
    expect(fake.listeners.has('throttle_event')).toBe(true);
    const handlers = fake.listeners.get('throttle_event')!;
    expect(handlers).toHaveLength(1);
  });

  it('TestUseThrottleStreamClosesOnUnmount', () => {
    const { unmount } = renderHook(() => useThrottleStream());
    expect(fakes[0].closed).toBe(false);
    unmount();
    expect(fakes[0].closed).toBe(true);
  });
});
