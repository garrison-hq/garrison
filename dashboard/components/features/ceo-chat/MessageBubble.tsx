// M5.2 — MessageBubble (plan §1.7).
//
// Operator messages right-aligned in a darker bubble. Assistant
// messages left-aligned WITHOUT a bubble (M5.4 polish): the CEO
// reply reads as a document, not a chat ribbon. A small mono header
// row carries `[C] CEO · <model>`; the per-message cost lives below
// the message body + tools (outside any panel) as muted mono.
//
// Streaming variant carries a trailing ▍ cursor (ARIA-hidden) styled
// by the .garrison-cursor keyframe in app/globals.css.

import type { ReactNode } from 'react';
import { formatPerMessageCost, formatModelBadge } from './format';
import { ToolCallChip } from './ToolCallChip';
import { AssistantMarkdown } from './AssistantMarkdown';
import type { ToolCallEntry } from '@/lib/sse/chatStream';

interface MessageBubbleProps {
  role: 'operator' | 'assistant';
  /** Free-form status string from chat_messages.status. Common values:
   *  'pending' | 'streaming' | 'completed' | 'failed' | 'aborted'. */
  status: string;
  content: string | null;
  costUsd?: number | string | null;
  modelBadge?: string | null;
  /** True while this assistant turn is streaming a delta buffer; the
   *  bubble shows the cursor. */
  streaming?: boolean;
  /** M5.3 inline tool-call chips — rendered after the text content
   *  in arrival order. Empty list = no chips. */
  toolCalls?: ToolCallEntry[];
  /** Optional inline error variant — when assistant row's errorKind
   *  is non-null the bubble renders an error block (T011 wires the
   *  CHAT_ERROR_DISPLAY table). For now this prop is reserved. */
  errorBlock?: ReactNode;
}

export function MessageBubble({
  role,
  status,
  content,
  costUsd,
  modelBadge,
  streaming = false,
  toolCalls,
  errorBlock,
}: Readonly<MessageBubbleProps>) {
  const isOperator = role === 'operator';
  const inFlight = streaming && status !== 'completed' && status !== 'failed' && status !== 'aborted';
  const ariaLive = inFlight ? 'polite' : undefined;
  const ariaBusy = inFlight ? true : undefined;

  if (isOperator) {
    return (
      <article
        className="flex justify-end"
        data-testid="chat-message-bubble"
        data-role={role}
        data-status={status}
        aria-live={ariaLive}
        aria-busy={ariaBusy}
      >
        <div className="max-w-[70%] bg-surface-3 text-text-1 rounded-lg rounded-br-sm px-3 py-2 text-[13.5px] leading-[1.55] whitespace-pre-wrap break-words">
          <div data-testid="chat-message-body">{content ?? ''}</div>
        </div>
      </article>
    );
  }

  return (
    <article
      className="flex flex-col items-start gap-1.5 max-w-[80%]"
      data-testid="chat-message-bubble"
      data-role={role}
      data-status={status}
      aria-live={ariaLive}
      aria-busy={ariaBusy}
    >
      <header className="flex items-center gap-1.5 text-text-3 text-[10.5px] uppercase tracking-[0.06em]">
        <span
          aria-hidden
          className="w-3.5 h-3.5 rounded-sm font-mono text-[9px] grid place-items-center bg-accent/15 text-accent"
        >
          C
        </span>
        <span>CEO</span>
        <span className="text-text-4">·</span>
        <span className="font-mono normal-case tracking-normal" aria-label="model" data-testid="chat-model-badge">
          {formatModelBadge(modelBadge)}
        </span>
      </header>
      {errorBlock ? <div className="w-full">{errorBlock}</div> : null}
      <div className="text-text-1 text-[13.5px] leading-[1.55] break-words" data-testid="chat-message-body">
        {inFlight && !content ? (
          <span
            className="inline-flex items-center gap-1 py-1"
            aria-label="CEO is responding"
            data-testid="chat-typing-indicator"
          >
            <span className="garrison-typing-dot inline-block w-1.5 h-1.5 rounded-full bg-text-3" aria-hidden />
            <span className="garrison-typing-dot inline-block w-1.5 h-1.5 rounded-full bg-text-3" aria-hidden />
            <span className="garrison-typing-dot inline-block w-1.5 h-1.5 rounded-full bg-text-3" aria-hidden />
          </span>
        ) : (
          <>
            {content ? <AssistantMarkdown content={content} /> : null}
            {inFlight ? (
              <span className="garrison-cursor inline-block ml-0.5 text-text-3" aria-hidden>
                ▍
              </span>
            ) : null}
          </>
        )}
      </div>
      {toolCalls && toolCalls.length > 0 ? (
        <div className="flex flex-col items-start gap-1" data-testid="toolcall-chip-list">
          {toolCalls.map((entry) => (
            <ToolCallChip key={entry.toolUseId} entry={entry} />
          ))}
        </div>
      ) : null}
      {costUsd === undefined ? null : (
        <footer
          className="text-text-3 text-[10.5px] font-mono font-tabular"
          data-testid="chat-message-cost"
        >
          {formatPerMessageCost(costUsd) ?? ''}
        </footer>
      )}
    </article>
  );
}
