import type { ActivityEvent } from './events';

// Channel allowlist (FR-060).
//
// KNOWN_CHANNELS — literal channel names the supervisor emits via
// pg_notify. The dashboard's LISTEN connection subscribes to each
// of these names verbatim. Postgres LISTEN does NOT support
// wildcards, so parameterized channels (work.ticket.transitioned.
// <dept>.<from>.<to>) cannot be LISTEN-subscribed; they're
// captured via the event_outbox poll plus pattern matching from
// KNOWN_CHANNEL_PATTERNS.
//
// Source verified at task-start by:
//   grep -E "pg_notify\\('([a-z._]+)" supervisor/migrations/*.sql
// Currently emits: work.ticket.created (literal) and
// work.ticket.transitioned.<dept>.<from>.<to> (parameterized).
//
// Adding a new channel here is an explicit code change. The
// dashboard MUST NOT wildcard-subscribe.

export const KNOWN_CHANNELS = ['work.ticket.created'] as const;
export type KnownChannel = (typeof KNOWN_CHANNELS)[number];

export const KNOWN_CHANNEL_PATTERNS: Array<{
  pattern: RegExp;
  /** A short description shown in retro / debug logs. */
  description: string;
}> = [
  {
    pattern: /^work\.ticket\.transitioned\.[a-z0-9_-]+\.[a-z0-9_]+\.[a-z0-9_]+$/,
    description: 'work.ticket.transitioned.<dept>.<from>.<to>',
  },
];

export interface OutboxRow {
  id: string;
  channel: string;
  payload: Record<string, unknown> | null;
  createdAt: Date;
}

/**
 * Parse an event_outbox row into a discriminated ActivityEvent.
 * Returns an `unknown`-kind event for channels that don't match a
 * known pattern — the activity feed renders those rows in a
 * generic shape so the operator never sees the surface mute
 * unexpected events silently.
 */
export function parseChannel(row: OutboxRow): ActivityEvent {
  const at = row.createdAt instanceof Date
    ? row.createdAt.toISOString()
    : new Date(row.createdAt).toISOString();
  const payload = row.payload ?? {};
  const ticketId = String(payload.ticket_id ?? payload.ticketId ?? '');

  if (row.channel === 'work.ticket.created') {
    return { kind: 'ticket.created', eventId: row.id, at, ticketId };
  }

  const transitionMatch = /^work\.ticket\.transitioned\.([a-z0-9_-]+)\.([a-z0-9_]+)\.([a-z0-9_]+)$/.exec(
    row.channel,
  );
  if (transitionMatch) {
    return {
      kind: 'ticket.transitioned',
      eventId: row.id,
      at,
      ticketId,
      department: transitionMatch[1],
      from: transitionMatch[2],
      to: transitionMatch[3],
    };
  }

  return { kind: 'unknown', eventId: row.id, at, channel: row.channel };
}

/**
 * True if the channel name matches a known channel — either a
 * literal in KNOWN_CHANNELS or a pattern in KNOWN_CHANNEL_PATTERNS.
 * Used by the listener to drop notifications outside the allowlist.
 */
export function isKnownChannel(channel: string): boolean {
  if ((KNOWN_CHANNELS as readonly string[]).includes(channel)) return true;
  return KNOWN_CHANNEL_PATTERNS.some(({ pattern }) => pattern.test(channel));
}
