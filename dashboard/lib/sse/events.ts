// Discriminated-union for events the activity feed renders. Each
// variant maps to a known render path in the UI; adding a variant
// is an explicit code change (FR-060: "the dashboard MUST NOT
// wildcard-subscribe").
//
// FR-064: every event carries an `eventId` (the event_outbox uuid
// or vault_access_log uuid) and an `at` timestamp. The
// Last-Event-ID cursor on the SSE stream is the eventId of the
// most recently delivered event.
//
// M4 (FR-010 / FR-015 / FR-076): mutation variants extend M3's
// supervisor-emitted variants. Tickets / agents flow through
// event_outbox; vault flows through vault_access_log (different
// source, same merge in lib/queries/activityCatchup.ts).

export type ActivityEvent =
  | {
      kind: 'ticket.created';
      eventId: string;
      at: string;
      ticketId: string;
    }
  | {
      kind: 'ticket.transitioned';
      eventId: string;
      at: string;
      ticketId: string;
      department: string;
      from: string;
      to: string;
      /** Run grouping key (FR-061). May be null for transitions not
       *  triggered by an agent (manual / SQL-direct moves, or M4
       *  operator-initiated drags per FR-027). */
      agentInstanceId: string | null;
      /** M4 (FR-027). Distinguishes agent-finalize transitions from
       *  operator drag-to-move transitions. Comes from the
       *  `hygiene_status` column on `ticket_transitions`. The full
       *  M2.x vocabulary plus M4's `'operator_initiated'` value
       *  flows through here verbatim; UI maps to a distinct
       *  visual treatment per FR-016. */
      hygieneStatus: string | null;
    }
  | {
      kind: 'ticket.edited';
      eventId: string;
      at: string;
      ticketId: string;
      /** Field-level diff per FR-014. Keys are column names from
       *  the `tickets` table; values are { before, after }. */
      diff: Record<string, { before: unknown; after: unknown }>;
    }
  | {
      kind: 'agent.edited';
      eventId: string;
      at: string;
      roleSlug: string;
      diff: Record<string, { before: unknown; after: unknown }>;
    }
  | {
      kind: 'vault.secret_created';
      eventId: string;
      at: string;
      secretPath: string;
    }
  | {
      kind: 'vault.secret_edited';
      eventId: string;
      at: string;
      secretPath: string;
      changedFields: string[];
    }
  | {
      kind: 'vault.secret_deleted';
      eventId: string;
      at: string;
      secretPath: string;
    }
  | {
      kind: 'vault.grant_added';
      eventId: string;
      at: string;
      roleSlug: string;
      envVarName: string;
      secretPath: string;
    }
  | {
      kind: 'vault.grant_removed';
      eventId: string;
      at: string;
      roleSlug: string;
      envVarName: string;
      secretPath: string;
    }
  | {
      kind: 'vault.rotation_initiated';
      eventId: string;
      at: string;
      secretPath: string;
    }
  | {
      kind: 'vault.rotation_completed';
      eventId: string;
      at: string;
      secretPath: string;
    }
  | {
      kind: 'vault.rotation_failed';
      eventId: string;
      at: string;
      secretPath: string;
      /** Which step of the multi-step rotation failed. From
       *  vault_access_log.metadata.failed_step (FR-094). */
      failedStep: string | null;
    }
  | {
      kind: 'vault.value_revealed';
      eventId: string;
      at: string;
      secretPath: string;
    }
  | {
      kind: 'chat.session_deleted';
      eventId: string;
      at: string;
      chatSessionId: string;
      actorUserId: string;
    }
  // M5.3 chat-driven mutation events (FR-461 + FR-463 — IDs only,
  // never raw chat content text). All eight share the same payload
  // shape with `extras` for verb-specific extras (from_column /
  // to_column for transition_ticket, agent_role_slug for agent verbs,
  // etc.).
  | ChatMutationEvent
  // M5.3 closes the M5.2 retro carryover for chat lifecycle channels.
  | { kind: 'chat.session_started'; eventId: string; at: string; chatSessionId: string }
  | { kind: 'chat.message_sent'; eventId: string; at: string; chatSessionId: string; chatMessageId: string }
  | { kind: 'chat.session_ended'; eventId: string; at: string; chatSessionId: string }
  | {
      kind: 'unknown';
      eventId: string;
      at: string;
      channel: string;
    };

// Shared shape for all eight M5.3 chat-mutation events. The kind
// discriminator names the verb's <entity>.<action>; `extras` is a
// flat string map with verb-specific fields (Rule 6 backstop: no
// chat content, only IDs / enum-values / role-slugs / column-slugs).
export type ChatMutationKind =
  | 'chat.ticket.created'
  | 'chat.ticket.edited'
  | 'chat.ticket.transitioned'
  | 'chat.agent.paused'
  | 'chat.agent.resumed'
  | 'chat.agent.spawned'
  | 'chat.agent.config_edited'
  | 'chat.hiring.proposed';

export interface ChatMutationEvent {
  kind: ChatMutationKind;
  eventId: string;
  at: string;
  chatSessionId: string;
  chatMessageId: string;
  affectedResourceId: string;
  affectedResourceType: string;
  /** Verb-specific extras as a flat string map. Rule 6 backstop:
   *  payloads carry only IDs / enum values / role-slugs / column-slugs
   *  — never raw chat content. Typed string-only so consumers don't
   *  need defensive String() coercion at every read site. */
  extras: Record<string, string>;
}
