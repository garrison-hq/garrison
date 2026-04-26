import { describe, it, expect, vi } from 'vitest';
import { makeListener } from './listener';
import type { ListenClient } from '@/lib/db/listenClient';

// The listener tests use an injected fake clientFactory so we can
// drive the state machine without a real Postgres LISTEN
// connection. The fake records subscribe calls so we can assert
// the LISTEN was set up correctly; real connection lifecycle is
// covered by the integration tests in T016.

function fakeClient() {
  const listened: string[] = [];
  return {
    listen: async (channel: string) => {
      listened.push(channel);
    },
    end: async () => {},
    listened,
  };
}

describe('lib/sse/listener', () => {
  it('listener starts in dormant and transitions to live on first subscriber', async () => {
    const fake = fakeClient();
    const listener = makeListener({
      clientFactory: () => fake as unknown as ListenClient,
    });
    expect(listener.state()).toBe('dormant');
    listener.subscribe({ onEvent: () => {} });
    // Connection is async — wait a microtask flush.
    await new Promise((r) => setTimeout(r, 5));
    expect(listener.state()).toBe('live');
    expect(fake.listened).toContain('work.ticket.created');
    await listener.shutdown();
  });

  it('listener fans out a notification to multiple subscribers in subscription order', async () => {
    const fake = fakeClient();
    const listener = makeListener({
      clientFactory: () => fake as unknown as ListenClient,
    });
    const seen: string[] = [];
    listener.subscribe({ onEvent: (e) => seen.push('A:' + e.kind) });
    listener.subscribe({ onEvent: (e) => seen.push('B:' + e.kind) });
    await new Promise((r) => setTimeout(r, 5));
    await listener.__testInject(
      'work.ticket.created',
      JSON.stringify({ event_id: 'evt-1', ticket_id: 'tk-1' }),
    );
    expect(seen).toEqual(['A:ticket.created', 'B:ticket.created']);
    await listener.shutdown();
  });

  it('listener drops notifications on channels outside the allowlist', async () => {
    const fake = fakeClient();
    const listener = makeListener({
      clientFactory: () => fake as unknown as ListenClient,
    });
    const seen: string[] = [];
    listener.subscribe({ onEvent: (e) => seen.push(e.kind) });
    await new Promise((r) => setTimeout(r, 5));
    await listener.__testInject(
      'totally.unrelated.channel',
      JSON.stringify({ event_id: 'evt-x' }),
    );
    expect(seen).toEqual([]);
    await listener.shutdown();
  });

  it('listener notifies subscribers on simulated connection-lost', async () => {
    const fake = fakeClient();
    const listener = makeListener({
      clientFactory: () => fake as unknown as ListenClient,
    });
    const lost = vi.fn();
    listener.subscribe({ onEvent: () => {}, onConnectionLost: lost });
    await new Promise((r) => setTimeout(r, 5));
    listener.__testConnectionLost();
    expect(lost).toHaveBeenCalledTimes(1);
    await listener.shutdown();
  });

  it('listener returns to idle-grace after the last subscriber disconnects', async () => {
    const fake = fakeClient();
    const listener = makeListener({
      clientFactory: () => fake as unknown as ListenClient,
      idleGraceMs: 500,
    });
    const unsubscribe = listener.subscribe({ onEvent: () => {} });
    await new Promise((r) => setTimeout(r, 5));
    expect(listener.state()).toBe('live');
    unsubscribe();
    expect(listener.state()).toBe('idle-grace');
    await listener.shutdown();
  });
});
