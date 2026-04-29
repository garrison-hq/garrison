// M5.2 — chatStream state-machine tests (plan §1.5).
//
// Tests target the pure ChatStreamMachine surface (delta dedupe,
// terminal handling, session-ended close, error/backoff transitions,
// idle-grace store survival across remount). The React hook itself
// is exercised end-to-end by the Playwright sub-scenarios in T013–T015
// where a real browser EventSource drives the lifecycle; covering the
// state machine here avoids bringing jsdom into the dev deps.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  ChatStreamMachine,
  computeBackoff,
  emptyStore,
  getOrCreateStore,
  __resetChatStreamCache,
  IDLE_GRACE_MS,
} from './chatStream';

beforeEach(() => {
  __resetChatStreamCache();
});

afterEach(() => {
  __resetChatStreamCache();
});

describe('ChatStreamMachine', () => {
  it('TestChatStreamAccumulatesDeltas', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'hello' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 1, delta_text: ' world' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 2, delta_text: '!' }));
    expect(store.state).toBe('live');
    expect(store.partialDeltas.get('m-1')).toBe('hello world!');
  });

  it('TestChatStreamDedupesOnSeq', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'A' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'A' })); // dedupe
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 1, delta_text: 'B' }));
    expect(store.partialDeltas.get('m-1')).toBe('AB');
  });

  it('TestChatStreamPreservesPartialOnReconnect', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'pre' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 1, delta_text: 'fix' }));
    expect(store.partialDeltas.get('m-1')).toBe('prefix');

    // Disconnect → backoff: partial buffer survives.
    ChatStreamMachine.error(store);
    expect(store.state).toBe('backoff');
    expect(store.partialDeltas.get('m-1')).toBe('prefix');

    // Reconnect: replayed seqs 0 + 1 are deduped, new seq 2 appends.
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'pre' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 1, delta_text: 'fix' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 2, delta_text: 'ed' }));
    expect(store.partialDeltas.get('m-1')).toBe('prefixed');
    expect(store.state).toBe('live');
  });

  it('TestChatStreamFinalisesOnTerminal', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'partial...' }));
    ChatStreamMachine.terminal(
      store,
      JSON.stringify({
        messageId: 'm-1',
        status: 'completed',
        content: 'final content',
        errorKind: null,
        costUsd: '0.0042',
      }),
    );
    expect(store.terminals.get('m-1')?.content).toBe('final content');
    // Per plan §1.5 the renderer prefers terminal but the partial
    // buffer remains for forensic / scroll-back purposes.
    expect(store.partialDeltas.get('m-1')).toBe('partial...');
  });

  it('TestChatStreamClosesOnSessionEnded', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.sessionEnded(store);
    expect(store.sessionEnded).toBe(true);
  });

  it('TestChatStreamClosesOnSessionIdChange', () => {
    // The hook caches stores per sessionId; switching to a fresh
    // sessionId returns a new empty store while the prior store
    // enters idle-grace.
    const a = getOrCreateStore('sess-A');
    ChatStreamMachine.open(a);
    ChatStreamMachine.delta(a, JSON.stringify({ message_id: 'mA', seq: 0, delta_text: 'A' }));
    ChatStreamMachine.enterIdleGrace(a);
    expect(a.state).toBe('idle-grace');

    const b = getOrCreateStore('sess-B');
    expect(b).not.toBe(a);
    expect(b.partialDeltas.size).toBe(0);
    expect(a.partialDeltas.get('mA')).toBe('A'); // prior buffer untouched
  });

  it('TestChatStreamIdleGraceOnUnmount', () => {
    vi.useFakeTimers();
    try {
      const sessionId = 'sess-idle';
      const store = getOrCreateStore(sessionId);
      ChatStreamMachine.open(store);
      ChatStreamMachine.delta(
        store,
        JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'kept' }),
      );
      ChatStreamMachine.enterIdleGrace(store);

      // Re-fetch within idle window: store is still cached.
      const within = getOrCreateStore(sessionId);
      expect(within).toBe(store);
      expect(within.partialDeltas.get('m-1')).toBe('kept');

      // The hook schedules reaping at IDLE_GRACE_MS; manual cleanup
      // through __resetChatStreamCache emulates the timer firing
      // since we don't run the React effect here.
      vi.advanceTimersByTime(IDLE_GRACE_MS + 10);
    } finally {
      vi.useRealTimers();
    }
    // Verify the constant is exposed and matches the documented
    // 60s window.
    expect(IDLE_GRACE_MS).toBe(60_000);
  });

  it('resets the visible buffer when block increments (multi-message_start turn)', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    // Block 0: claude's first message streams "pong" before tool calls.
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', block: 0, seq: 0, delta_text: 'po' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', block: 0, seq: 1, delta_text: 'ng' }));
    expect(store.partialDeltas.get('m-1')).toBe('pong');

    // Block 1: claude moved past message_start (tool_use happened);
    // the new message starts streaming "Yes, MemPalace is up" — the
    // dashboard must DROP the prior "pong" and render only the new
    // message's deltas.
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', block: 1, seq: 2, delta_text: 'Yes, ' }));
    expect(store.partialDeltas.get('m-1')).toBe('Yes, ');
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', block: 1, seq: 3, delta_text: 'MemPalace is up' }));
    expect(store.partialDeltas.get('m-1')).toBe('Yes, MemPalace is up');

    // A late delta from block 0 (out-of-order replay or reconnect
    // race) is dropped — block tracking is monotonic per id, so
    // anything below the last-seen block can't update the visible
    // buffer.
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', block: 0, seq: 4, delta_text: 'STALE' }));
    expect(store.partialDeltas.get('m-1')).toBe('Yes, MemPalace is up');
  });

  it('legacy supervisor without block field still works (defaults to 0)', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 0, delta_text: 'A' }));
    ChatStreamMachine.delta(store, JSON.stringify({ message_id: 'm-1', seq: 1, delta_text: 'B' }));
    expect(store.partialDeltas.get('m-1')).toBe('AB');
  });

  it('TestChatStreamBackoffSchedule', () => {
    expect(computeBackoff(0)).toBe(100);
    expect(computeBackoff(100)).toBe(200);
    expect(computeBackoff(200)).toBe(400);
    expect(computeBackoff(400)).toBe(800);
    expect(computeBackoff(800)).toBe(1600);
    expect(computeBackoff(1600)).toBe(3200);
    expect(computeBackoff(15_000)).toBe(30_000);
    expect(computeBackoff(30_000)).toBe(30_000); // cap
    expect(computeBackoff(60_000)).toBe(30_000); // never above cap
  });

  it('rejects malformed delta JSON without throwing', () => {
    const store = emptyStore();
    ChatStreamMachine.open(store);
    ChatStreamMachine.delta(store, '<not json>');
    expect(store.partialDeltas.size).toBe(0);
  });

  it('rejects terminal payload missing messageId', () => {
    const store = emptyStore();
    ChatStreamMachine.terminal(store, JSON.stringify({ status: 'completed', content: 'x' }));
    expect(store.terminals.size).toBe(0);
  });
});
