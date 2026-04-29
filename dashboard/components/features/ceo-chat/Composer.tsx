'use client';

// M5.2 — Composer (plan §1.6).
//
// State machine:
//   idle | typing | submitting | streaming | disabled-ended | disabled-cost-cap
//
// 'aborted' is NOT a disabled state — per FR-215 the composer stays
// enabled when chat_sessions.status='aborted' so the operator's next
// turn proceeds as a new operator INSERT in the same session.
//
// Submit binding: (event.metaKey || event.ctrlKey) && event.key === 'Enter'.
// Plain Enter inserts a newline. Send button disabled for empty/whitespace-only
// drafts (FR-281). Loading state on send button between submit and SSE first
// delta (FR-282).
//
// Max input: 10240 bytes UTF-8. Paste exceeding the limit truncates with
// a non-blocking warning toast (we surface a small in-line caption rather
// than wiring a global toast primitive — keeps the surface self-contained).
//
// Auto-focus on session-open + after each terminal commit (FR-218).

import { useEffect, useRef, useState, useTransition } from 'react';
import { Kbd } from '@/components/ui/Kbd';
import { PalaceLiveChip } from './PalaceLiveChip';
import { startChatSession, sendChatMessage } from '@/lib/actions/chat';

const MAX_BYTES = 10 * 1024;

interface ComposerProps {
  /** Current chat_sessions row. Composer derives its disabled state
   *  from session.status + the latest assistant errorKind. Pass undefined
   *  for the "no session yet" case (the first message creates one). */
  sessionId?: string;
  status?: 'active' | 'ended' | 'aborted' | string;
  /** Whether the latest assistant turn is currently streaming — drives
   *  the disabled-during-streaming gate. */
  isStreaming?: boolean;
  /** Latest assistant error_kind, used to detect cost-cap. */
  latestErrorKind?: string | null;
  /** Age of the most recent successful mempalace tool call (for the
   *  PalaceLiveChip). null = unavailable. */
  palaceAgeMs?: number | null;
  /** Trigger key the parent bumps to force a re-focus after each
   *  terminal commit (per FR-218). */
  refocusKey?: number;
  /** Optional override of the action endpoints — tests pass mocks. */
  actions?: {
    startChatSession: typeof startChatSession;
    sendChatMessage: typeof sendChatMessage;
  };
  /** Called after a successful send so the parent can refresh queries
   *  / advance state. */
  onSent?: (info: { sessionId: string; messageId: string }) => void;
}

export function Composer({
  sessionId,
  status,
  isStreaming,
  latestErrorKind,
  palaceAgeMs = null,
  refocusKey,
  actions,
  onSent,
}: Readonly<ComposerProps>) {
  const [draft, setDraft] = useState('');
  const [warning, setWarning] = useState<string | null>(null);
  const [isPending, startTransition] = useTransition();
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  const startFn = actions?.startChatSession ?? startChatSession;
  const sendFn = actions?.sendChatMessage ?? sendChatMessage;

  const trimmed = draft.trim();
  const isCostCapped = latestErrorKind === 'session_cost_cap_reached';
  const isEnded = status === 'ended';
  const disabled = isEnded || isCostCapped || isStreaming === true;

  useEffect(() => {
    if (!disabled) textareaRef.current?.focus();
  }, [sessionId, refocusKey, disabled]);

  const handleSend = () => {
    if (disabled) return;
    if (trimmed.length === 0) return;
    if (Buffer.byteLength(trimmed, 'utf-8') > MAX_BYTES) {
      setWarning('Message exceeds the 10KB limit. Send shorter or split it up.');
      return;
    }
    const content = trimmed;
    setDraft('');
    setWarning(null);
    startTransition(async () => {
      try {
        if (!sessionId) {
          const result = await startFn(content);
          onSent?.({ sessionId: result.sessionId, messageId: result.messageId });
        } else {
          const result = await sendFn(sessionId, content);
          onSent?.({ sessionId, messageId: result.messageId });
        }
      } catch (err) {
        setWarning(err instanceof Error ? err.message : 'Send failed.');
      }
    });
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault();
      handleSend();
    }
  };

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const pasted = e.clipboardData.getData('text');
    if (!pasted) return;
    const next = draft + pasted;
    if (Buffer.byteLength(next, 'utf-8') > MAX_BYTES) {
      e.preventDefault();
      // Truncate to the byte limit (UTF-8 boundary safe by slicing
      // codepoints, not bytes — paste content is rarely beyond ASCII
      // for chat purposes; this is a defensive cap).
      const room = MAX_BYTES - Buffer.byteLength(draft, 'utf-8');
      const allowed = sliceByBytes(pasted, Math.max(0, room));
      setDraft(draft + allowed);
      setWarning('Pasted content was truncated to 10KB. Send what you have or break it up across messages.');
    }
  };

  const captionText = isEnded
    ? 'This thread is ended. Open a new one to keep chatting.'
    : isCostCapped
    ? 'This thread hit its cost cap. Start a new thread to keep chatting.'
    : isStreaming
    ? 'CEO is responding…'
    : 'CEO will be summoned when you send. Each message spawns a fresh process.';

  const sendDisabled = disabled || isPending || trimmed.length === 0;

  return (
    <footer
      className="border-t border-border-1 bg-surface-1 px-4 py-3 flex flex-col gap-2 min-w-0"
      data-testid="chat-composer"
    >
      <textarea
        ref={textareaRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        placeholder={sessionId ? 'Message the CEO…' : 'Ask the CEO anything to start a thread…'}
        rows={3}
        aria-keyshortcuts="Meta+Enter"
        disabled={disabled}
        data-testid="chat-composer-textarea"
        className="w-full min-w-0 resize-y min-h-[64px] max-h-[280px] bg-surface-2 border border-border-1 rounded px-3 py-2 text-sm font-mono outline-none focus:ring-2 focus:ring-accent/50 disabled:opacity-60 disabled:cursor-not-allowed"
      />
      <div className="flex items-center gap-3 text-[11px] text-text-3 min-w-0">
        <span className="shrink-0">
          <PalaceLiveChip ageMs={palaceAgeMs} />
        </span>
        <span className="flex-1 min-w-0 truncate">{captionText}</span>
        <button
          type="button"
          onClick={handleSend}
          disabled={sendDisabled}
          className="px-3 py-1.5 text-[12px] font-medium border border-border-2 rounded bg-accent/90 text-white hover:bg-accent disabled:opacity-60 disabled:cursor-not-allowed flex items-center gap-2 shrink-0 whitespace-nowrap"
          data-testid="chat-composer-send"
          aria-label="send"
        >
          <span>{isPending ? 'sending…' : 'send'}</span>
          <Kbd>⌘↵</Kbd>
        </button>
      </div>
      {warning ? (
        <p className="text-[11px] text-warn" role="status" data-testid="chat-composer-warning">
          {warning}
        </p>
      ) : null}
    </footer>
  );
}

// sliceByBytes returns a substring whose UTF-8 byte length is <= the
// supplied budget. Walks codepoints from the start to avoid mid-rune
// truncation. Defensive — paste content is usually plain ASCII at the
// chat scale, but Greek letters in math symbols would otherwise leave
// a corrupt byte tail.
function sliceByBytes(s: string, byteBudget: number): string {
  if (byteBudget <= 0) return '';
  let used = 0;
  const out: string[] = [];
  for (const ch of s) {
    const w = Buffer.byteLength(ch, 'utf-8');
    if (used + w > byteBudget) break;
    used += w;
    out.push(ch);
  }
  return out.join('');
}
