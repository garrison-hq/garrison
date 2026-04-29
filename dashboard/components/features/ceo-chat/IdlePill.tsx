// M5.2 — IdlePill (plan §1.9). Reads chat_sessions.status directly:
//   active   → tone='ok' + label 'active' + pulse
//   ended    → tone='warn' + label 'idle'
//   aborted  → tone='err' + label 'aborted'
//
// Per FR-332 the pill combines a colored StatusDot with a text label
// so colorblind operators can read it without relying on hue alone.
// Per FR-250 the flip is driven by the supervisor-written session
// status, not a client-side timer.

import { StatusDot } from '@/components/ui/StatusDot';

type SessionStatus = 'active' | 'ended' | 'aborted';

const TONE_BY_STATUS = {
  active: { tone: 'ok' as const, label: 'active', pulse: true },
  ended: { tone: 'warn' as const, label: 'idle', pulse: false },
  aborted: { tone: 'err' as const, label: 'aborted', pulse: false },
};

export function IdlePill({ status }: Readonly<{ status: SessionStatus | string }>) {
  const config = TONE_BY_STATUS[status as SessionStatus] ?? {
    tone: 'neutral' as const,
    label: status,
    pulse: false,
  };
  return (
    <span className="inline-flex items-center gap-1.5" data-testid="chat-idle-pill" data-status={status}>
      <StatusDot tone={config.tone} pulse={config.pulse} />
      <span className="text-text-1 text-[11px] font-medium">{config.label}</span>
    </span>
  );
}
