# Contract: pg_notify channel `work.ticket.created`

**Source of truth**: [`specs/_context/m1-context.md`](../../_context/m1-context.md) §"pg_notify contract". This file is a verbatim restatement for agents consuming the contract at implementation time.

## Channel

| Name | `work.ticket.created` |
|------|------------------------|
| Scope | M1 — the only channel the supervisor LISTENs on |
| Transport | Postgres `LISTEN`/`NOTIFY` on a dedicated `*pgx.Conn` (never pooled) |

## Producer

The `ticket_created_emit` trigger on `AFTER INSERT` of `tickets` calls `emit_ticket_created()`, which:

1. Inserts a row into `event_outbox` with `channel='work.ticket.created'` and a full JSONB payload.
2. Issues `pg_notify('work.ticket.created', json_build_object('event_id', <new_event_id>)::text)` in the same transaction.

Producer rule: nobody issues `NOTIFY work.ticket.created` outside the trigger. Inserting a ticket is the only way an event fires.

## Notification payload

Sent via `pg_notify`:

```json
{"event_id": "<uuid>"}
```

Strict shape — exactly one field, exactly one UUID. No other fields are emitted and consumers MUST NOT read any other field. Rationale: Postgres' 8 KiB NOTIFY payload limit discourages carrying the full payload; consumers fetch it via `GetEventByID`.

## Full event payload (`event_outbox.payload`)

Stored JSONB, written by the trigger, fetched by the consumer:

```json
{
  "ticket_id": "<uuid>",
  "department_id": "<uuid>",
  "created_at": "<iso8601>"
}
```

## Consumer contract

The supervisor (single consumer per database, enforced by FR-018) MUST:

1. Open a dedicated `*pgx.Conn` outside the pool.
2. Acquire `pg_try_advisory_lock(0x6761727269736f6e)` on that connection.
3. Run the FR-011 recovery query.
4. Run one fallback poll over `event_outbox WHERE processed_at IS NULL`.
5. Execute `LISTEN work.ticket.created`.
6. Loop: `WaitForNotification(ctx)`, parse `{"event_id": ...}`, dispatch.

For each notification:
1. Fetch the full row via `GetEventByID`.
2. Apply idempotency: if `processed_at IS NOT NULL`, no-op.
3. Apply concurrency cap check (`CheckCap`).
4. Spawn or defer.
5. On spawn completion, update `event_outbox.processed_at = NOW()` in the same transaction as the terminal `agent_instances` update.

## Failure modes and their handling

| Condition | Handling |
|-----------|----------|
| LISTEN connection drops | NFR-002 exponential backoff (100ms → 30s), on reconnect run fallback poll before re-issuing LISTEN. |
| Notification payload malformed | Log at `error` level with raw payload; drop; fallback poll will pick up the underlying row. |
| `event_outbox` row referenced by a notification doesn't exist | Should be impossible (trigger + notify share a transaction). Log at error level if observed and continue. |
| Duplicate notification (LISTEN + poll race) | Second handler observes `processed_at IS NOT NULL` via `LockEventForProcessing`; no-op. |

## Missed-event recovery

`event_outbox.processed_at` is the durable marker for "has this event been handled". A row with `processed_at IS NULL` is a live piece of work regardless of whether its NOTIFY was received. The fallback poll (`SELECT … WHERE processed_at IS NULL ORDER BY created_at LIMIT 100`) runs at `cfg.PollInterval` (default 5s) and immediately before LISTEN on initial connect and reconnect.

## What the consumer MUST NOT do

- Consume `pg_notify` via a pooled connection.
- Read fields other than `event_id` from the notification payload.
- Acknowledge events by any mechanism other than setting `processed_at`.
- Spawn for an event whose `processed_at` is already set.
- Release the FR-018 advisory lock explicitly (let the connection close release it).
