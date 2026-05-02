// Reconstruct the ToolCallEntry list for a committed assistant message
// from chat_messages.raw_event_envelope. Used by MessageStream so the
// chip strip survives a page reload — live SSE only delivers tool_use /
// tool_result frames during the in-flight turn; on reconnect the
// envelope is the canonical source per M5.2 FR-261.
//
// Envelope shape (from supervisor's claudeproto accumulator):
//   - top-level array of stream events
//   - assistant event: { type: 'assistant', message: { content: [{type:'tool_use', id, name, input}, ...] } }
//   - user event:      { type: 'user',      message: { content: [{type:'tool_result', tool_use_id, is_error, content}, ...] } }
//
// The replay shape mirrors the live SSE shape: each ToolCallEntry's
// result.payload is wrapped as { detail, is_error } so ToolCallChip's
// FailureChip detail-extraction works identically across live + replay.

import type { ToolCallEntry } from './chatStream';

export function reconstructToolCallsFromEnvelope(envelope: unknown): ToolCallEntry[] {
  const events = extractStreamEvents(envelope);
  if (events.length === 0) return [];

  const calls = new Map<string, ToolCallEntry>();
  collectToolUses(events, calls);
  attachToolResults(events, calls);
  return [...calls.values()];
}

function collectToolUses(events: unknown[], calls: Map<string, ToolCallEntry>): void {
  for (const ev of events) {
    if (!isAssistantEvent(ev)) continue;
    for (const block of extractMessageContent(ev)) {
      if (!isToolUseBlock(block)) continue;
      const id = block['id'];
      const name = block['name'];
      if (typeof id !== 'string' || typeof name !== 'string') continue;
      calls.set(id, {
        toolUseId: id,
        toolName: name,
        args: block['input'] ?? null,
      });
    }
  }
}

function attachToolResults(events: unknown[], calls: Map<string, ToolCallEntry>): void {
  for (const ev of events) {
    if (!isUserEvent(ev)) continue;
    for (const block of extractMessageContent(ev)) {
      if (!isRecord(block) || block['type'] !== 'tool_result') continue;
      const useId = block['tool_use_id'];
      if (typeof useId !== 'string') continue;
      const entry = calls.get(useId);
      if (!entry) continue;
      const isError = block['is_error'] === true;
      entry.result = {
        isError,
        payload: { detail: extractToolResultDetail(block['content']), is_error: isError },
      };
    }
  }
}

function extractToolResultDetail(content: unknown): string {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  // claude tool_result content is typically [{type: 'text', text: '...'}].
  return content
    .filter((p): p is Record<string, unknown> => isRecord(p) && p['type'] === 'text')
    .map((p) => (typeof p['text'] === 'string' ? p['text'] : ''))
    .join('');
}

function extractStreamEvents(envelope: unknown): unknown[] {
  if (Array.isArray(envelope)) return envelope;
  if (isRecord(envelope) && Array.isArray(envelope['events'])) {
    return envelope['events'];
  }
  return [];
}

function extractMessageContent(ev: Record<string, unknown>): unknown[] {
  const message = ev['message'];
  if (!isRecord(message)) return [];
  const content = message['content'];
  return Array.isArray(content) ? content : [];
}

function isAssistantEvent(ev: unknown): ev is Record<string, unknown> {
  return isRecord(ev) && ev['type'] === 'assistant';
}

function isUserEvent(ev: unknown): ev is Record<string, unknown> {
  return isRecord(ev) && ev['type'] === 'user';
}

function isToolUseBlock(block: unknown): block is Record<string, unknown> {
  return isRecord(block) && block['type'] === 'tool_use';
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null;
}
