import { describe, it, expect } from 'vitest';
import { reconstructToolCallsFromEnvelope } from './replay';

describe('reconstructToolCallsFromEnvelope', () => {
  it('returns [] for null / non-array envelopes', () => {
    expect(reconstructToolCallsFromEnvelope(null)).toEqual([]);
    expect(reconstructToolCallsFromEnvelope(undefined)).toEqual([]);
    expect(reconstructToolCallsFromEnvelope({})).toEqual([]);
    expect(reconstructToolCallsFromEnvelope('nope')).toEqual([]);
  });

  it('extracts tool_use blocks with no result yet', () => {
    const envelope = [
      {
        type: 'assistant',
        message: {
          content: [
            { type: 'tool_use', id: 'tu-1', name: 'mcp__garrison-mutate__create_ticket', input: { dept: 'eng' } },
          ],
        },
      },
    ];
    const out = reconstructToolCallsFromEnvelope(envelope);
    expect(out).toHaveLength(1);
    expect(out[0].toolUseId).toBe('tu-1');
    expect(out[0].toolName).toBe('mcp__garrison-mutate__create_ticket');
    expect(out[0].args).toEqual({ dept: 'eng' });
    expect(out[0].result).toBeUndefined();
  });

  it('attaches a successful tool_result to its matching tool_use', () => {
    const envelope = [
      {
        type: 'assistant',
        message: { content: [{ type: 'tool_use', id: 'tu-2', name: 'mcp__mempalace__mempalace_search', input: { q: 'x' } }] },
      },
      {
        type: 'user',
        message: {
          content: [
            {
              type: 'tool_result',
              tool_use_id: 'tu-2',
              is_error: false,
              content: [{ type: 'text', text: '{"ok":true}' }],
            },
          ],
        },
      },
    ];
    const out = reconstructToolCallsFromEnvelope(envelope);
    expect(out).toHaveLength(1);
    expect(out[0].result).toEqual({
      isError: false,
      payload: { detail: '{"ok":true}', is_error: false },
    });
  });

  it('attaches a failure tool_result with is_error=true', () => {
    const envelope = [
      {
        type: 'assistant',
        message: { content: [{ type: 'tool_use', id: 'tu-3', name: 'mcp__garrison-mutate__create_ticket', input: {} }] },
      },
      {
        type: 'user',
        message: {
          content: [
            {
              type: 'tool_result',
              tool_use_id: 'tu-3',
              is_error: true,
              content: [{ type: 'text', text: '{"success":false,"error_kind":"validation_failed"}' }],
            },
          ],
        },
      },
    ];
    const out = reconstructToolCallsFromEnvelope(envelope);
    expect(out[0].result?.isError).toBe(true);
    expect(out[0].result?.payload).toEqual({
      detail: '{"success":false,"error_kind":"validation_failed"}',
      is_error: true,
    });
  });

  it('preserves order across multiple tool_uses + results', () => {
    const envelope = [
      { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'a', name: 'mcp__x__a', input: {} }] } },
      { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'b', name: 'mcp__x__b', input: {} }] } },
      { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'b', is_error: false, content: [] }] } },
      { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'a', is_error: true, content: [] }] } },
    ];
    const out = reconstructToolCallsFromEnvelope(envelope);
    expect(out.map((e) => e.toolUseId)).toEqual(['a', 'b']);
    expect(out[0].result?.isError).toBe(true);
    expect(out[1].result?.isError).toBe(false);
  });

  it('tolerates the legacy `{events: [...]}` wrapper shape', () => {
    const envelope = {
      events: [
        { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'tu-4', name: 'x', input: {} }] } },
      ],
    };
    const out = reconstructToolCallsFromEnvelope(envelope);
    expect(out).toHaveLength(1);
    expect(out[0].toolUseId).toBe('tu-4');
  });
});
