import type { ActivityEvent } from './events';

// Channel allowlist (FR-060).
//
// KNOWN_CHANNELS — literal channel names that emit pg_notify.
// The dashboard's LISTEN connection subscribes to each of these
// names verbatim. Postgres LISTEN does NOT support wildcards, so
// parameterized channels (e.g. work.ticket.transitioned.<dept>.
// <from>.<to>, work.vault.<kind>) cannot be LISTEN-subscribed;
// they're captured via the event_outbox / vault_access_log poll
// plus pattern matching from KNOWN_CHANNEL_PATTERNS. Real-time
// (sub-second) latency only applies to literal channels.
//
// Source verified at task-start by:
//   grep -E "pg_notify\\('([a-z._]+)" supervisor/migrations/*.sql
// Supervisor emits: work.ticket.created (literal) and
// work.ticket.transitioned.<dept>.<from>.<to> (parameterized).
//
// M4 dashboard emits: work.ticket.created (same channel as
// supervisor; operator-created tickets join the supervisor's
// stream), work.ticket.edited (literal, new — inline edits),
// work.ticket.transitioned.<dept>.<from>.<to> (parameterized;
// operator drag-to-move uses the same channel agent finalize
// uses per FR-029 clarification), work.agent.edited (literal,
// new — agent settings edits), work.vault.<kind> (parameterized;
// poll-cycle latency, fine for vault frequency).
//
// agents.changed is NOT in this set — it's supervisor-listened
// (T014 cache invalidator), not dashboard-listened. Emitted by
// the dashboard's editAgent server action; consumed by the
// supervisor's internal/agents listener.
//
// Adding a new channel here is an explicit code change.

export const KNOWN_CHANNELS = [
  'work.ticket.created',
  'work.ticket.edited',
  'work.agent.edited',
  'work.chat.session_deleted',
] as const;
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
  {
    pattern: /^work\.vault\.[a-z_]+$/,
    description: 'work.vault.<kind> (M4)',
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
function pickString(...candidates: unknown[]): string {
  for (const c of candidates) {
    if (typeof c === 'string') return c;
  }
  return '';
}

function pickRecord(value: unknown): Record<string, { before: unknown; after: unknown }> {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    return value as Record<string, { before: unknown; after: unknown }>;
  }
  return {};
}

function pickStringArray(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((v) => typeof v === 'string') as string[];
  }
  return [];
}

export function parseChannel(row: OutboxRow): ActivityEvent {
  const at = row.createdAt instanceof Date
    ? row.createdAt.toISOString()
    : new Date(row.createdAt).toISOString();
  const payload = row.payload ?? {};
  const ticketId = pickString(payload.ticket_id, payload.ticketId);

  if (row.channel === 'work.ticket.created') {
    return { kind: 'ticket.created', eventId: row.id, at, ticketId };
  }

  if (row.channel === 'work.ticket.edited') {
    return {
      kind: 'ticket.edited',
      eventId: row.id,
      at,
      ticketId,
      diff: pickRecord(payload.diff),
    };
  }

  if (row.channel === 'work.agent.edited') {
    return {
      kind: 'agent.edited',
      eventId: row.id,
      at,
      roleSlug: pickString(payload.role_slug, payload.roleSlug),
      diff: pickRecord(payload.diff),
    };
  }

  if (row.channel === 'work.chat.session_deleted') {
    return {
      kind: 'chat.session_deleted',
      eventId: row.id,
      at,
      chatSessionId: pickString(payload.chat_session_id, payload.chatSessionId),
      actorUserId: pickString(payload.actor_user_id, payload.actorUserId),
    };
  }

  const transitionMatch = /^work\.ticket\.transitioned\.([a-z0-9_-]+)\.([a-z0-9_]+)\.([a-z0-9_]+)$/.exec(
    row.channel,
  );
  if (transitionMatch) {
    const agentInstanceRaw = payload.agent_instance_id ?? payload.agentInstanceId;
    const hygieneStatusRaw = payload.hygiene_status ?? payload.hygieneStatus;
    return {
      kind: 'ticket.transitioned',
      eventId: row.id,
      at,
      ticketId,
      department: transitionMatch[1],
      from: transitionMatch[2],
      to: transitionMatch[3],
      agentInstanceId: typeof agentInstanceRaw === 'string' ? agentInstanceRaw : null,
      hygieneStatus: typeof hygieneStatusRaw === 'string' ? hygieneStatusRaw : null,
    };
  }

  const vaultMatch = /^work\.vault\.([a-z_]+)$/.exec(row.channel);
  if (vaultMatch) {
    const kind = vaultMatch[1];
    const secretPath = pickString(payload.secret_path, payload.secretPath);
    switch (kind) {
      case 'secret_created':
        return { kind: 'vault.secret_created', eventId: row.id, at, secretPath };
      case 'secret_edited':
        return {
          kind: 'vault.secret_edited',
          eventId: row.id,
          at,
          secretPath,
          changedFields: pickStringArray(payload.changed_fields ?? payload.changedFields),
        };
      case 'secret_deleted':
        return { kind: 'vault.secret_deleted', eventId: row.id, at, secretPath };
      case 'grant_added':
        return {
          kind: 'vault.grant_added',
          eventId: row.id,
          at,
          roleSlug: pickString(payload.role_slug, payload.roleSlug),
          envVarName: pickString(payload.env_var_name, payload.envVarName),
          secretPath,
        };
      case 'grant_removed':
        return {
          kind: 'vault.grant_removed',
          eventId: row.id,
          at,
          roleSlug: pickString(payload.role_slug, payload.roleSlug),
          envVarName: pickString(payload.env_var_name, payload.envVarName),
          secretPath,
        };
      case 'rotation_initiated':
        return { kind: 'vault.rotation_initiated', eventId: row.id, at, secretPath };
      case 'rotation_completed':
        return { kind: 'vault.rotation_completed', eventId: row.id, at, secretPath };
      case 'rotation_failed': {
        const failedStepRaw = payload.failed_step ?? payload.failedStep;
        return {
          kind: 'vault.rotation_failed',
          eventId: row.id,
          at,
          secretPath,
          failedStep: typeof failedStepRaw === 'string' ? failedStepRaw : null,
        };
      }
      case 'value_revealed':
        return { kind: 'vault.value_revealed', eventId: row.id, at, secretPath };
      default:
        return { kind: 'unknown', eventId: row.id, at, channel: row.channel };
    }
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
