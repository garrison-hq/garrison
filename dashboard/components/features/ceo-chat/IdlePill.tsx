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

const TONE_BY_STATUS: Record<string, { tone: 'ok' | 'warn' | 'err'; label: string; pulse: boolean }> = {
  active: { tone: 'ok', label: 'active', pulse: true },
  ended: { tone: 'warn', label: 'idle', pulse: false },
  aborted: { tone: 'err', label: 'aborted', pulse: false },
};

export function IdlePill({ status }: Readonly<{ status: string }>) {
  const config = TONE_BY_STATUS[status] ?? {
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
