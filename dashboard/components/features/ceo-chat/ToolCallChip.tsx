// ToolCallChip — M5.3 inline chip rendered inside an in-flight or
// committed assistant message bubble for every tool_use the chat
// emitted. Branches internally on tool name (read vs mutation) and
// result presence (pre-call vs post-call vs failure) per spec
// FR-440 .. FR-448. Single component, local sub-renderers, no file
// proliferation per plan D7.
//
// Visual structure (M5.4 polish): each chip reads as
//
//     ▸ verb · target · result
//
// where the leading caret + verb sit in --text-3, target in --text-2,
// the separator in --text-4, and the result token carries the chip's
// semantic tone (ok / err / warn / muted neutral).
//
// Read tools (postgres.query, mempalace.*) render at neutral emphasis;
// mutation tools (garrison-mutate.*) at accent emphasis on pre-call,
// ok on success, err on failure. The chip is informative-only per
// FR-445 — no undo / cancel / retry / approve / reject affordances.
// Click target on mutation post-call chips opens the affected resource
// via Next.js Link per FR-446.

import Link from 'next/link';
import type { ReactNode } from 'react';
import type { ToolCallEntry } from '@/lib/sse/chatStream';

// Claude's MCP tool-name wire format is `mcp__<server>__<verb>`
// (double-underscore separators). Earlier the chip renderer pinned
// `garrison-mutate.<verb>` from a pre-MCP draft of the spec; live
// traffic against claude shows the `mcp__garrison-mutate__<verb>`
// shape, so the renderer accepts both — old tests may still emit
// the legacy form, and operators who hit the verbs via a non-MCP
// path would too.
const MUTATION_TOOL_PREFIXES = ['mcp__garrison-mutate__', 'garrison-mutate.'] as const;

function matchMutationPrefix(toolName: string): string | null {
  for (const p of MUTATION_TOOL_PREFIXES) {
    if (toolName.startsWith(p)) return p;
  }
  return null;
}

interface Props {
  /** ToolCallEntry from useChatStream's toolCalls map. */
  entry: ToolCallEntry;
}

type Tone = 'neutral' | 'accent' | 'ok' | 'err';

const RESULT_TONE: Record<Tone, string> = {
  neutral: 'text-text-3',
  accent: 'text-accent',
  ok: 'text-ok',
  err: 'text-err',
};

const BORDER_TONE: Record<Tone, string> = {
  neutral: 'border-border-1',
  accent: 'border-accent/30',
  ok: 'border-ok/30',
  err: 'border-err/30',
};

const BG_TONE: Record<Tone, string> = {
  neutral: 'bg-surface-2',
  accent: 'bg-accent/10',
  ok: 'bg-ok/10',
  err: 'bg-err/10',
};

export function ToolCallChip({ entry }: Readonly<Props>) {
  const isMutation = matchMutationPrefix(entry.toolName) !== null;
  const isFailure = entry.result?.isError === true;
  const isPreCall = entry.result === undefined;

  const { verb, target } = splitToolName(entry.toolName);
  const tone = pickTone({ isMutation, isFailure, isPreCall });
  const result = pickResultLabel({ entry, isFailure, isPreCall });
  const url = !isFailure && !isPreCall ? affectedResourceURL(entry.result?.payload) : null;

  const chipBody = (
    <span
      role={isFailure ? 'alert' : isPreCall ? 'status' : undefined}
      aria-live={isPreCall ? 'polite' : undefined}
      aria-busy={isPreCall ? true : undefined}
      className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border text-[11px] font-mono ${BG_TONE[tone]} ${BORDER_TONE[tone]}`}
      data-testid="toolcall-chip"
      data-tool-name={entry.toolName}
      data-state={chipState({ isMutation, isFailure, isPreCall })}
    >
      <span className="text-text-3" aria-hidden>▸</span>
      <span className="text-text-3">{verb}</span>
      <span className="text-text-2">{target}</span>
      <span className="text-text-4" aria-hidden>·</span>
      <span className={RESULT_TONE[tone]}>{result}</span>
    </span>
  );

  return url ? (
    <Link href={url} className="no-underline">
      {chipBody}
    </Link>
  ) : (
    chipBody as ReactNode
  );
}

// Helpers — kept local so the rendering stays single-file per plan D7.

interface SplitToolName {
  verb: string;
  target: string;
}

function splitToolName(toolName: string): SplitToolName {
  // Mutation verbs come through claude as mcp__garrison-mutate__<verb>
  // — the action-word is "called" and the target is the verb name itself.
  const mutPrefix = matchMutationPrefix(toolName);
  if (mutPrefix) return { verb: 'called', target: toolName.slice(mutPrefix.length) };

  // mcp__<server>__<tool> → choose the verb by server (read tools have
  // server-specific phrasing). Strip the prefix to get a clean target.
  const mcpMatch = /^mcp__([^_]+(?:-[^_]+)*)__(.+)$/.exec(toolName);
  if (mcpMatch) {
    const server = mcpMatch[1];
    const tool = mcpMatch[2];
    return { verb: readVerbForServer(server), target: tool };
  }

  // Legacy bare names (postgres.query, mempalace.search) from the M5.3 draft.
  if (toolName.startsWith('postgres.')) return { verb: 'queried', target: 'postgres' };
  if (toolName.startsWith('mempalace.')) return { verb: 'searched', target: 'palace' };
  return { verb: 'called', target: toolName };
}

function readVerbForServer(server: string): string {
  if (server === 'mempalace') return 'searched';
  if (server === 'postgres') return 'queried';
  return 'called';
}

function pickTone({
  isMutation,
  isFailure,
  isPreCall,
}: Readonly<{ isMutation: boolean; isFailure: boolean; isPreCall: boolean }>): Tone {
  if (isFailure) return 'err';
  if (isPreCall) return isMutation ? 'accent' : 'neutral';
  return isMutation ? 'ok' : 'neutral';
}

function chipState({
  isMutation,
  isFailure,
  isPreCall,
}: Readonly<{ isMutation: boolean; isFailure: boolean; isPreCall: boolean }>): string {
  if (isFailure) return 'failure';
  if (isPreCall) return isMutation ? 'precall-mutate' : 'precall-read';
  return isMutation ? 'postcall-mutate' : 'postcall-read';
}

function pickResultLabel({
  entry,
  isFailure,
  isPreCall,
}: Readonly<{ entry: ToolCallEntry; isFailure: boolean; isPreCall: boolean }>): string {
  if (isFailure) return extractFailureDetail(entry.result?.payload);
  if (isPreCall) return '…';
  return 'ok';
}

function affectedResourceURL(args: unknown): string | null {
  // For garrison-mutate post-call results, the supervisor's tool_result
  // SSE frame carries a {detail, is_error} envelope at M5.3; the full
  // structured Result lives in chat_messages.raw_event_envelope and
  // gets replayed on reconnect. The chip's deep-link target is
  // synthesized from the verb name + (when present) a heuristic on
  // detail. M5.3 ships the simpler shape; richer linking lands as a
  // polish round when raw_event_envelope is wired into the chip.
  if (typeof args !== 'object' || args === null) return null;
  const obj = args as Record<string, unknown>;
  const url = obj['affected_resource_url'];
  return typeof url === 'string' ? url : null;
}

// extractFailureDetail produces a short, operator-readable label for a
// failed tool call. The supervisor's EmitToolResult wraps tr.Detail
// (which itself is the JSON-stringified MCP envelope from the verb)
// inside `{detail, is_error}`. So payload.detail is usually a JSON
// STRING (not an object), e.g. `{"success":false,"error_kind":"validation_failed",...}`.
// Try the cheap object lookups first; if detail is a JSON string,
// re-parse it and pull error_kind / message; fall back to the raw
// string (truncated) or 'failed'.
function extractFailureDetail(payload: unknown): string {
  if (typeof payload !== 'object' || payload === null) return 'failed';
  const obj = payload as Record<string, unknown>;
  const ek = obj['error_kind'];
  if (typeof ek === 'string') return ek;
  const d = obj['detail'];
  if (typeof d !== 'string') return 'failed';
  if (d.trimStart().startsWith('{')) {
    try {
      const parsed = JSON.parse(d) as Record<string, unknown>;
      const innerKind = parsed['error_kind'];
      if (typeof innerKind === 'string') return innerKind;
      const innerMsg = parsed['message'];
      if (typeof innerMsg === 'string') return innerMsg;
    } catch {
      // detail is opaque; fall through to the truncated raw form below
    }
  }
  return d.length > 80 ? `${d.slice(0, 77)}…` : d;
}
