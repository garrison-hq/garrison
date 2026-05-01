// ToolCallChip — M5.3 inline chip rendered inside an in-flight or
// committed assistant message bubble for every tool_use the chat
// emitted. Branches internally on tool name (read vs mutation) and
// result presence (pre-call vs post-call vs failure) per spec
// FR-440 .. FR-448. Single component, local sub-renderers, no file
// proliferation per plan D7.
//
// Read tools (postgres.query, mempalace.search, etc.) render
// lower-emphasis (neutral tone, single-line summary). Mutation tools
// (garrison-mutate.<verb>) render higher-emphasis (accent tone, verb
// label + arg highlights, deep-link on post-call). Failures use the
// M5.2 error palette via Chip tone='err'.
//
// The chip is informative-only per FR-445 — no undo / cancel / retry /
// approve / reject affordances. Click target on mutation post-call
// chips opens the affected resource via Next.js Link per FR-446.

import Link from 'next/link';
import type { ToolCallEntry } from '@/lib/sse/chatStream';
import { Chip } from '@/components/ui/Chip';

const MUTATION_TOOL_PREFIX = 'garrison-mutate.';

interface Props {
  /** ToolCallEntry from useChatStream's toolCalls map. */
  entry: ToolCallEntry;
}

export function ToolCallChip({ entry }: Readonly<Props>) {
  const isMutation = entry.toolName.startsWith(MUTATION_TOOL_PREFIX);
  const isFailure = entry.result?.isError === true;
  const isPreCall = entry.result === undefined;

  if (isFailure) {
    return <FailureChip entry={entry} isMutation={isMutation} />;
  }
  if (isPreCall) {
    return isMutation ? <MutateChipPreCall entry={entry} /> : <ReadChipPreCall entry={entry} />;
  }
  return isMutation ? <MutateChipPostCall entry={entry} /> : <ReadChipPostCall entry={entry} />;
}

// Helpers — kept local so the rendering stays single-file per plan D7.

function shortVerbName(toolName: string): string {
  return toolName.startsWith(MUTATION_TOOL_PREFIX)
    ? toolName.slice(MUTATION_TOOL_PREFIX.length)
    : toolName;
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

function readToolSummary(toolName: string): string {
  if (toolName === 'postgres.query') return 'queried postgres';
  if (toolName === 'mempalace.search') return 'searched palace';
  return `called ${toolName}`;
}

// --- read tool variants (low emphasis) ---

function ReadChipPreCall({ entry }: Readonly<Props>) {
  return (
    <span
      role="status"
      aria-live="polite"
      aria-busy="true"
      className="my-1 inline-flex items-center gap-1.5 text-[11px] text-text-3 font-tabular"
      data-testid="toolcall-chip"
      data-tool-name={entry.toolName}
      data-state="precall-read"
    >
      <Chip tone="neutral">{readToolSummary(entry.toolName)}…</Chip>
    </span>
  );
}

function ReadChipPostCall({ entry }: Readonly<Props>) {
  return (
    <span
      className="my-1 inline-flex items-center gap-1.5 text-[11px] text-text-3 font-tabular"
      data-testid="toolcall-chip"
      data-tool-name={entry.toolName}
      data-state="postcall-read"
    >
      <Chip tone="neutral">{readToolSummary(entry.toolName)}</Chip>
    </span>
  );
}

// --- mutation tool variants (higher emphasis) ---

function MutateChipPreCall({ entry }: Readonly<Props>) {
  return (
    <span
      role="status"
      aria-live="polite"
      aria-busy="true"
      className="my-1 inline-flex items-center gap-1.5 text-[12px] text-text-1"
      data-testid="toolcall-chip"
      data-tool-name={entry.toolName}
      data-state="precall-mutate"
    >
      <Chip tone="accent">
        {shortVerbName(entry.toolName)} <span className="font-tabular">…</span>
      </Chip>
    </span>
  );
}

function MutateChipPostCall({ entry }: Readonly<Props>) {
  const url = affectedResourceURL(entry.result?.payload);
  const label = `${shortVerbName(entry.toolName)} ✓`;
  return (
    <span
      className="my-1 inline-flex items-center gap-1.5 text-[12px] text-text-1"
      data-testid="toolcall-chip"
      data-tool-name={entry.toolName}
      data-state="postcall-mutate"
    >
      {url ? (
        <Link href={url} className="no-underline">
          <Chip tone="ok">{label}</Chip>
        </Link>
      ) : (
        <Chip tone="ok">{label}</Chip>
      )}
    </span>
  );
}

// --- failure variant (M5.2 error palette) ---

function FailureChip({ entry, isMutation }: Readonly<{ entry: ToolCallEntry; isMutation: boolean }>) {
  const detail = (() => {
    const payload = entry.result?.payload;
    if (typeof payload === 'object' && payload !== null) {
      const obj = payload as Record<string, unknown>;
      const ek = obj['error_kind'];
      if (typeof ek === 'string') return ek;
      const d = obj['detail'];
      if (typeof d === 'string') return d;
    }
    return 'failed';
  })();
  const label = `${isMutation ? shortVerbName(entry.toolName) : entry.toolName} — ${detail}`;
  return (
    <span
      role="alert"
      className="my-1 inline-flex items-center gap-1.5 text-[12px]"
      data-testid="toolcall-chip"
      data-tool-name={entry.toolName}
      data-state="failure"
    >
      <Chip tone="err">{label}</Chip>
    </span>
  );
}
