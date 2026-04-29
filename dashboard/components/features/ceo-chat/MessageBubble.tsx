// M5.2 — MessageBubble (plan §1.7).
//
// Operator messages right-aligned in a darker bubble. Assistant
// messages left-aligned with a "C  CEO · <model>" header + a
// per-message cost footer at 4 decimals. Streaming variant carries a
// trailing ▍ cursor (ARIA-hidden) styled by the .garrison-cursor
// keyframe in app/globals.css.

import type { ReactNode } from 'react';
import { formatPerMessageCost, formatModelBadge } from './format';

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
  errorBlock,
}: Readonly<MessageBubbleProps>) {
  const isOperator = role === 'operator';
  const inFlight = streaming && status !== 'completed' && status !== 'failed' && status !== 'aborted';
  const ariaLive = inFlight ? 'polite' : undefined;
  const ariaBusy = inFlight ? true : undefined;

  return (
    <article
      className={`flex ${isOperator ? 'justify-end' : 'justify-start'} gap-2`}
      data-testid="chat-message-bubble"
      data-role={role}
      data-status={status}
      aria-live={ariaLive}
      aria-busy={ariaBusy}
    >
      {isOperator ? null : (
        <div className="w-7 h-7 rounded bg-surface-3 text-text-1 grid place-items-center font-mono font-semibold text-[10px] shrink-0 mt-1">
          C
        </div>
      )}
      <div
        className={
          isOperator
            ? 'max-w-[70%] bg-surface-3 text-text-1 rounded px-3 py-2 text-sm whitespace-pre-wrap break-words'
            : 'max-w-[80%] bg-surface-2 text-text-1 rounded px-3 py-2 text-sm whitespace-pre-wrap break-words'
        }
      >
        {isOperator ? null : (
          <header className="flex items-center gap-1.5 mb-1 text-text-3 text-[10.5px] uppercase tracking-[0.06em]">
            <span>CEO</span>
            <span className="text-text-4">·</span>
            <span className="font-mono normal-case tracking-normal" aria-label="model" data-testid="chat-model-badge">
              {formatModelBadge(modelBadge)}
            </span>
          </header>
        )}
        {errorBlock ? <div className="mb-1">{errorBlock}</div> : null}
        <div data-testid="chat-message-body">
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
              {content ?? ''}
              {inFlight ? (
                <span className="garrison-cursor inline-block ml-0.5 text-text-3" aria-hidden>
                  ▍
                </span>
              ) : null}
            </>
          )}
        </div>
        {isOperator || costUsd === undefined ? null : (
          <footer className="mt-1 text-text-3 text-[10px] font-mono font-tabular text-right" data-testid="chat-message-cost">
            {formatPerMessageCost(costUsd) ?? ''}
          </footer>
        )}
      </div>
    </article>
  );
}
