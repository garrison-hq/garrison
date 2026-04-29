// M5.2 — inline error variant rendered inside MessageBubble when
// chat_messages.error_kind is non-null (plan §1.15 + FR-271 + FR-272).
// Uses CHAT_ERROR_DISPLAY so every M5.1 ErrorKind has guaranteed copy.

import Link from 'next/link';
import { resolveChatErrorDisplay } from './errorMessages';

export function ChatErrorBlock({ errorKind }: Readonly<{ errorKind: string }>) {
  const d = resolveChatErrorDisplay(errorKind);
  return (
    <div
      className="border border-err/40 bg-err/10 rounded px-2.5 py-1.5 text-[12px]"
      role="alert"
      data-testid="chat-error-block"
      data-error-kind={errorKind}
    >
      <p className="text-err font-medium">{d.headline}</p>
      <p className="text-text-2 mt-0.5">{d.body}</p>
      {d.deepLinkHref ? (
        <Link
          href={d.deepLinkHref}
          className="text-info hover:underline mt-1 inline-block"
        >
          {d.deepLinkLabel ?? 'Open'}
        </Link>
      ) : null}
    </div>
  );
}
