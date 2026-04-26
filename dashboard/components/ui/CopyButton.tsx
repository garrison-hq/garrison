'use client';

import { useState } from 'react';

// Tiny clipboard helper used by the palace-links block + anywhere
// else we surface a copy-able token (ticket id, audit row id, etc).
// 1.5s "copied" feedback then snaps back to the default label.

export function CopyButton({
  value,
  label = 'copy',
  copiedLabel = 'copied',
}: Readonly<{ value: string; label?: string; copiedLabel?: string }>) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          globalThis.setTimeout(() => setCopied(false), 1500);
        } catch {
          // Clipboard write blocked (e.g. permission denied).
          // Surface no UI for it — the operator can fall back to
          // selecting the text manually.
        }
      }}
      className="text-text-3 hover:text-text-1 hover:bg-surface-3 border border-border-1 hover:border-border-2 rounded text-[10.5px] font-mono px-2 py-0.5 transition-colors"
    >
      {copied ? copiedLabel : label}
    </button>
  );
}
