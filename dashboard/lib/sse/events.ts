// Discriminated-union for events the activity feed renders. Each
// variant maps to a known render path in the UI; adding a variant
// is an explicit code change (FR-060: "the dashboard MUST NOT
// wildcard-subscribe").
//
// FR-064: every event carries an `eventId` (the event_outbox uuid)
// and an `at` timestamp. The Last-Event-ID cursor on the SSE
// stream is the eventId of the most recently delivered event.

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
       *  triggered by an agent (manual / SQL-direct moves). */
      agentInstanceId: string | null;
    }
  | {
      kind: 'unknown';
      eventId: string;
      at: string;
      channel: string;
    };
