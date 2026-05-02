// MutationEvent — discriminated union of mutation events the
// dashboard writes to the audit stream. Distinct from
// lib/sse/events.ts:ActivityEvent (which is the UI render
// contract): MutationEvent is the WRITE contract used by
// lib/audit/eventOutbox.ts:writeMutationEventToOutbox and the
// server actions in lib/actions/*.
//
// FR-014 (event_outbox payloads with field-level diffs):
// ticket.* and agent.* variants carry `diff` records.
// FR-012 (vault outcomes): vault mutations land in
// vault_access_log with extended outcome enum, NOT here. Vault
// mutations are written via lib/audit/vaultAccessLog.ts and
// emit pg_notify on work.vault.<kind> channels separately.
//
// MutationEvent and ActivityEvent are siblings; lib/sse/channels.
// ts:parseChannel bridges between event_outbox row payloads and
// ActivityEvent variants.

export type TicketCreatedEvent = {
  kind: 'ticket.created';
  ticketId: string;
  deptSlug: string;
  targetColumn: string;
  /** M6 / T017 — parent ticket id for chat-driven decomposition.
   *  Null for top-level (operator-initiated) creates. Surfaces in
   *  the activity feed and in the per-ticket audit trail so a
   *  decomposition is traceable from kanban → activity. */
  parentTicketId?: string | null;
};

export type TicketMovedEvent = {
  kind: 'ticket.moved';
  ticketId: string;
  fromColumn: string;
  toColumn: string;
  /** New ticket_transitions row id — surfaces in the audit
   *  history block on the ticket detail panel. */
  transitionId: string;
  /** 'agent' for finalize-driven transitions, 'operator' for
   *  drag-to-move per FR-027. The ticket_transitions row's
   *  hygiene_status carries 'operator_initiated' for the
   *  operator path. */
  origin: 'agent' | 'operator';
};

export type TicketEditedEvent = {
  kind: 'ticket.edited';
  ticketId: string;
  /** Field-level diff per FR-014. Keys are column names; values
   *  are { before, after }. Inline edits write a single event
   *  per save with all changed fields in one diff record. */
  diff: Record<string, { before: unknown; after: unknown }>;
};

export type AgentEditedEvent = {
  kind: 'agent.edited';
  roleSlug: string;
  diff: Record<string, { before: unknown; after: unknown }>;
};

export type MutationEvent =
  | TicketCreatedEvent
  | TicketMovedEvent
  | TicketEditedEvent
  | AgentEditedEvent;

/**
 * Channel name a given mutation event publishes on. Matches the
 * lib/sse/channels.ts allowlist. ticket.moved uses the
 * parameterized work.ticket.transitioned.<dept>.<from>.<to>
 * channel agent finalize uses (FR-029 clarification); other
 * mutations use literal channels for real-time visibility per
 * FR-015.
 */
export function channelForEvent(event: MutationEvent, deptSlug?: string): string {
  switch (event.kind) {
    case 'ticket.created':
      return 'work.ticket.created';
    case 'ticket.moved': {
      // Caller must pass deptSlug for moved events because the
      // channel name carries the department. Throw eagerly on
      // misuse rather than emit on a malformed channel.
      if (!deptSlug) {
        throw new Error('channelForEvent: ticket.moved requires deptSlug');
      }
      return `work.ticket.transitioned.${deptSlug}.${event.fromColumn}.${event.toColumn}`;
    }
    case 'ticket.edited':
      return 'work.ticket.edited';
    case 'agent.edited':
      return 'work.agent.edited';
  }
}
