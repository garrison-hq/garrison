// M6 — throttleStream pure-store tests.
//
// Mirrors the M5.2 chatStream.test.ts pattern: the React hook itself
// is exercised end-to-end by Playwright (M6.x integration suite once
// it lands); this file covers the pure store/reducer surface
// (emptyStore + pushEvent) without bringing jsdom into the dev deps.

import { describe, it, expect } from 'vitest';
import { emptyStore, pushEvent } from './throttleStream';

function notify(eventID: string, kind = 'company_budget_exceeded'): string {
  return JSON.stringify({
    event_id: eventID,
    company_id: 'co-1',
    kind,
    fired_at: '2026-05-02T12:00:00Z',
  });
}

describe('throttleStream.pushEvent', () => {
  it('TestPushEventAppendsToEmptyStore', () => {
    const store = emptyStore();
    pushEvent(store, notify('e1'));
    expect(store.events).toHaveLength(1);
    expect(store.events[0].event_id).toBe('e1');
    expect(store.lastError).toBeNull();
  });

  it('TestPushEventPrependsNewest', () => {
    const store = emptyStore();
    pushEvent(store, notify('e1'));
    pushEvent(store, notify('e2'));
    expect(store.events.map((e) => e.event_id)).toEqual(['e2', 'e1']);
  });

  it('TestPushEventDedupesByEventID', () => {
    const store = emptyStore();
    pushEvent(store, notify('e1'));
    pushEvent(store, notify('e1')); // dedupe
    pushEvent(store, notify('e2'));
    expect(store.events).toHaveLength(2);
    expect(store.events.map((e) => e.event_id)).toEqual(['e2', 'e1']);
  });

  it('TestPushEventDropsMalformedJSON', () => {
    const store = emptyStore();
    pushEvent(store, '{not json');
    expect(store.events).toHaveLength(0);
  });

  it('TestPushEventDropsMissingEventID', () => {
    const store = emptyStore();
    pushEvent(
      store,
      JSON.stringify({ company_id: 'co-1', kind: 'rate_limit_pause', fired_at: 'x' }),
    );
    expect(store.events).toHaveLength(0);
  });

  it('TestPushEventEnforcesBufferCap', () => {
    const store = emptyStore();
    for (let i = 0; i < 105; i++) pushEvent(store, notify(`e${i}`));
    // Cap at 100; oldest 5 dropped from the tail.
    expect(store.events).toHaveLength(100);
    // Newest first — last push (e104) sits at index 0.
    expect(store.events[0].event_id).toBe('e104');
    // Oldest surviving is e5 (e0..e4 dropped).
    expect(store.events[store.events.length - 1].event_id).toBe('e5');
    // seenIds tracks survivors only — pushing the dropped e0 again is
    // treated as a fresh event (re-prepended at index 0, e104 shifts).
    pushEvent(store, notify('e0'));
    expect(store.events[0].event_id).toBe('e0');
  });

  it('TestPushEventClearsLastErrorOnSuccess', () => {
    const store = emptyStore();
    store.lastError = 'connection_lost';
    pushEvent(store, notify('e1'));
    expect(store.lastError).toBeNull();
  });

  it('TestPushEventLeavesLastErrorAloneOnDrop', () => {
    const store = emptyStore();
    store.lastError = 'connection_lost';
    pushEvent(store, '{malformed');
    expect(store.lastError).toBe('connection_lost');
  });
});
