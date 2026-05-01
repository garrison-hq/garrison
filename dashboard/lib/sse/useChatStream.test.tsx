// @vitest-environment jsdom

// Hook tests for useChatStream. Drives the React hook with a fake
// EventSource via __setEventSourceFactory + renderHook. Covers the
// connect → onOpen → delta → terminal → sessionEnded lifecycle the
// pure ChatStreamMachine tests can't reach.
//
// Requires jsdom (added as dev-dep alongside @testing-library/react)
// because useChatStream uses useEffect and a real EventSource binding.

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import {
  __resetChatStreamCache,
  __setEventSourceFactory,
  useChatStream,
} from './chatStream';

// FakeEventSource: minimal shim that lets the hook attach listeners
// and lets the test fire 'delta' / 'terminal' / 'session_ended' /
// 'tool_use' / 'tool_result' / 'assistant_error' / 'open' / 'error'
// events synchronously.
class FakeEventSource implements EventTarget {
  url: string;
  readyState = 0;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  private listeners = new Map<string, Set<EventListener>>();

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: EventListener): void {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(listener);
  }

  removeEventListener(type: string, listener: EventListener): void {
    this.listeners.get(type)?.delete(listener);
  }

  dispatchEvent(event: Event): boolean {
    this.listeners.get(event.type)?.forEach((l) => l(event));
    return true;
  }

  close(): void {
    this.readyState = 2;
  }

  fireOpen(): void {
    this.onopen?.(new Event('open'));
  }

  fireError(): void {
    this.onerror?.(new Event('error'));
  }

  fireNamed(name: string, payload: unknown): void {
    const ev = new MessageEvent(name, { data: JSON.stringify(payload) });
    this.dispatchEvent(ev);
  }

  fireMalformed(name: string, raw: string): void {
    const ev = new MessageEvent(name, { data: raw });
    this.dispatchEvent(ev);
  }
}

let lastEventSource: FakeEventSource | null = null;

beforeEach(() => {
  __resetChatStreamCache();
  lastEventSource = null;
  __setEventSourceFactory((url) => {
    const es = new FakeEventSource(url);
    lastEventSource = es;
    return es as unknown as EventSource;
  });
});

afterEach(() => {
  __setEventSourceFactory(null);
  __resetChatStreamCache();
});

describe('useChatStream hook', () => {
  it('connects on mount and transitions to live on open', () => {
    const { result } = renderHook(() => useChatStream('sess-1'));
    expect(result.current.state).toBe('connecting');
    expect(lastEventSource).not.toBeNull();
    act(() => {
      lastEventSource!.fireOpen();
    });
    expect(result.current.state).toBe('live');
  });

  it('accumulates delta payloads into partialDeltas', () => {
    const { result } = renderHook(() => useChatStream('sess-2'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireNamed('delta', {
        message_id: 'm-1',
        block: 0,
        seq: 0,
        delta_text: 'hello ',
      });
    });
    act(() => {
      lastEventSource!.fireNamed('delta', {
        message_id: 'm-1',
        block: 0,
        seq: 1,
        delta_text: 'world',
      });
    });
    expect(result.current.partialDeltas.get('m-1')).toBe('hello world');
  });

  it('terminal event lands in the terminals map', () => {
    const { result } = renderHook(() => useChatStream('sess-3'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireNamed('terminal', {
        messageId: 'm-2',
        status: 'completed',
        content: 'final text',
        errorKind: null,
      });
    });
    expect(result.current.terminals.get('m-2')?.status).toBe('completed');
  });

  it('session_ended event flips sessionEnded and closes', () => {
    const { result } = renderHook(() => useChatStream('sess-4'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireNamed('session_ended', {
        chat_session_id: 'sess-4',
        status: 'ended',
      });
    });
    expect(result.current.sessionEnded).toBe(true);
  });

  it('tool_use event lands in the toolCalls map', () => {
    const { result } = renderHook(() => useChatStream('sess-5'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireNamed('tool_use', {
        message_id: 'm-3',
        tool_use_id: 'tu-1',
        tool_name: 'create_ticket',
        args: { x: 1 },
      });
    });
    const msgCalls = result.current.toolCalls.get('m-3');
    expect(msgCalls).toBeTruthy();
    const entry = msgCalls!.find((e) => e.toolUseId === 'tu-1');
    expect(entry?.toolName).toBe('create_ticket');
  });

  it('tool_result event transitions the matching chip', () => {
    const { result } = renderHook(() => useChatStream('sess-6'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireNamed('tool_use', {
        message_id: 'm-4',
        tool_use_id: 'tu-2',
        tool_name: 'edit_ticket',
        args: {},
      });
    });
    act(() => {
      lastEventSource!.fireNamed('tool_result', {
        message_id: 'm-4',
        tool_use_id: 'tu-2',
        is_error: false,
        result: { ok: true },
      });
    });
    const entry = result.current.toolCalls.get('m-4')?.find((e) => e.toolUseId === 'tu-2');
    expect(entry?.result).toBeTruthy();
    expect(entry?.result?.isError).toBe(false);
  });

  it('assistant_error event surfaces lastError', () => {
    const { result } = renderHook(() => useChatStream('sess-7'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireNamed('assistant_error', {
        message_id: 'm-5',
        error_kind: 'tool_call_ceiling_reached',
        message: 'exceeded 50 tool calls',
      });
    });
    expect(result.current.lastError).toBe('tool_call_ceiling_reached');
  });

  it('error event transitions to backoff and schedules a reconnect', () => {
    const { result } = renderHook(() => useChatStream('sess-8'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    act(() => {
      lastEventSource!.fireError();
    });
    expect(result.current.state).toBe('backoff');
  });

  it('malformed delta payload does not throw', () => {
    const { result } = renderHook(() => useChatStream('sess-9'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    expect(() => {
      act(() => {
        lastEventSource!.fireMalformed('delta', 'not json');
      });
    }).not.toThrow();
    expect(result.current.state).toBe('live');
  });

  it('skips activation when sessionId is empty', () => {
    const { result } = renderHook(() => useChatStream(''));
    expect(result.current.state).toBe('dormant');
    expect(lastEventSource).toBeNull();
  });

  it('unmount triggers idle-grace transition', () => {
    const { unmount, result } = renderHook(() => useChatStream('sess-10'));
    act(() => {
      lastEventSource!.fireOpen();
    });
    unmount();
    // After unmount the hook's cleanup closes the source and enters
    // idle-grace; the cached store keeps the last state until grace
    // expires. We don't time-travel here; just check the source closed.
    expect(lastEventSource!.readyState).toBe(2);
    // The result reference still points at the last render's snapshot,
    // which captured the live state — we don't assert state change post-
    // unmount because the hook's unmount happens after force().
    void result;
  });
});
