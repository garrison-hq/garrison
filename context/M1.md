# M1 Context — Event Bus + Supervisor Core

This is the context for the M1 specify-cli spec. It contains only the architectural decisions, constraints, and rationale relevant to M1. Later milestones (dashboard, CEO chat, hiring, MemPalace integration) are intentionally excluded.

**Do not re-litigate decisions in this document. Spec the implementation within these constraints.**

---

## What M1 is

M1 builds the event bus and supervisor core. Nothing else.

**Deliverable**: a Go binary that:
1. Listens on Postgres `pg_notify` channels via a dedicated connection
2. Parses event payloads into typed structs
3. Checks concurrency caps before spawning
4. Spawns a subprocess (a stand-in for Claude Code — for M1, this is a dummy command like `echo "hello from ticket $TICKET_ID"`)
5. Tracks live subprocesses in Postgres (pid, started_at, ticket_id, department_id, status)
6. Handles subprocess timeout, graceful cancellation via `context.Context`, and cleanup on exit
7. Reconnects on Postgres disconnect with backoff, and catches missed events via a `processed_at` fallback poll
8. Handles SIGTERM/SIGINT for graceful shutdown of all in-flight work

**Non-goals for M1** (explicitly out of scope):
- No Claude Code invocation (the spawn is fake)
- No MemPalace integration
- No web UI
- No workflow engine (no Kanban column transitions)
- No hiring flow
- No CEO
- No skills.sh

M1 proves the plumbing. M2 swaps the fake spawn for real Claude Code.

---

## Relevant core principles

1. **Postgres is the source of truth for current state.** Everything durable lives there. pg_notify is the event bus.
2. **Agents are ephemeral processes.** Spawned on events, run to completion, die. Zero idle token cost.
3. **Per-department concurrency caps.** Parallelism is bounded by a `concurrency_cap` on each department row (default 3).

---

## The stack (locked)

- **Language**: Go 1.23+
- **Postgres driver**: `jackc/pgx/v5` — not `lib/pq`, not `database/sql`
- **Query layer**: `sqlc` for generating typed query code from SQL migrations
- **Logging**: stdlib `log/slog` for structured logging — no logrus, no zap
- **Concurrency primitives**: `golang.org/x/sync/errgroup` for "run N subsystems, cancel all if one fails"
- **Config**: environment variables + a typed config struct (no Viper unless genuinely needed)
- **Subprocess management**: stdlib `os/exec` with `exec.CommandContext`
- **Testing**: stdlib `testing` + `stretchr/testify` for assertions + `testcontainers-go` for Postgres integration tests
- **Migrations**: `goose` or `tern` — pick one, stick to it
- **Build**: single static binary via `CGO_ENABLED=0 go build`. Dockerfile is 6 lines.

**No additional dependencies without an explicit proposal.** The allowed-dependency list above is closed. Agents implementing M1 cannot add libraries without prior approval. If a dependency seems necessary, it goes in the spec's open-questions section, not the imports.

---

## Concurrency model (non-negotiable)

These patterns must hold throughout the M1 code:

1. Every goroutine accepts a `context.Context` and respects cancellation. No bare `go func()`.
2. The supervisor's `main` owns a root context. Subsystems (pg listener, subprocess manager, fallback poller, HTTP health endpoint if any) get derived contexts. Graceful shutdown cancels the root.
3. Use `errgroup.WithContext` for the "run N subsystems, cancel all if one fails" pattern at the top level.
4. Every spawned subprocess is launched with `exec.CommandContext(ctx, ...)`. The derived context has a timeout. When the context is cancelled, the subprocess is killed.
5. Channels: always specify sender vs receiver responsibility. Close channels from the sender side only. Buffered channels only with a clear reason documented in a comment.

---

## pg_notify contract

**Connection discipline**: the LISTEN connection is a *dedicated* connection held outside the pool. Pool connections cycle; a LISTEN connection must stay alive. In pgx this is a plain `*pgx.Conn` acquired once, or a `*pgxpool.Conn` manually hijacked — either works, but it cannot come out of a round-robin pool.

**Channel naming**: channels are namespaced by event domain. For M1, the only channel is:

```
work.ticket.created
```

Payload (JSON):
```json
{
  "ticket_id": "<uuid>",
  "department_id": "<uuid>",
  "created_at": "<iso8601>"
}
```

Channels are dot-delimited. Future milestones will add more (e.g. `work.ticket.transitioned.<department_slug>.<from_column>.<to_column>`), but M1 handles only `work.ticket.created`.

**Missed event handling**: LISTEN is the fast path. Notifications are lost during reconnects and under some failure modes. The tables include a `processed_at TIMESTAMPTZ` column (NULL = unprocessed). The supervisor runs a fallback poll every N seconds:

```sql
SELECT id, ticket_id, department_id, created_at
FROM event_outbox
WHERE processed_at IS NULL
ORDER BY created_at
LIMIT 100;
```

The poll interval should be configurable but default to 5 seconds. The supervisor deduplicates: if an event arrives via LISTEN and is already processed, skip it. Idempotency is ensured by marking `processed_at` in the same transaction that handles the event.

**Reconnect backoff**: on pg_notify connection drop, reconnect with exponential backoff (100ms → 200ms → 400ms → ... → capped at 30s). On reconnect, immediately run the fallback poll before LISTENing again, to catch events missed during the disconnect.

---

## Data model for M1

Minimal schema. Later milestones will extend this.

```sql
-- Schema: org

CREATE TABLE departments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  concurrency_cap INT NOT NULL DEFAULT 3,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Schema: work

CREATE TABLE tickets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  department_id UUID NOT NULL REFERENCES departments(id),
  objective TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE event_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  channel TEXT NOT NULL,            -- e.g. 'work.ticket.created'
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  processed_at TIMESTAMPTZ          -- NULL until handled
);

CREATE INDEX idx_event_outbox_unprocessed
  ON event_outbox (created_at)
  WHERE processed_at IS NULL;

CREATE TABLE agent_instances (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  department_id UUID NOT NULL REFERENCES departments(id),
  ticket_id UUID NOT NULL REFERENCES tickets(id),
  pid INT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at TIMESTAMPTZ,
  status TEXT NOT NULL,             -- 'running', 'succeeded', 'failed', 'timeout'
  exit_reason TEXT
);

CREATE INDEX idx_agent_instances_running
  ON agent_instances (department_id)
  WHERE status = 'running';
```

**Trigger for event_outbox**: INSERT on `tickets` must atomically insert a row into `event_outbox` and fire `pg_notify`. Implement as a Postgres trigger function so the event is guaranteed to fire in the same transaction as the state change.

```sql
CREATE OR REPLACE FUNCTION emit_ticket_created() RETURNS trigger AS $$
DECLARE
  event_id UUID;
  payload JSONB;
BEGIN
  payload := jsonb_build_object(
    'ticket_id', NEW.id,
    'department_id', NEW.department_id,
    'created_at', NEW.created_at
  );
  INSERT INTO event_outbox (channel, payload)
    VALUES ('work.ticket.created', payload)
    RETURNING id INTO event_id;
  PERFORM pg_notify('work.ticket.created', jsonb_build_object('event_id', event_id)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ticket_created_emit
  AFTER INSERT ON tickets
  FOR EACH ROW
  EXECUTE FUNCTION emit_ticket_created();
```

The notification payload is just the `event_id` — the supervisor fetches the full payload from `event_outbox` by id. This keeps notification size small (Postgres has an 8KB NOTIFY payload limit) and ensures the row exists before the supervisor tries to process it.

---

## Concurrency accounting

Before spawning a subprocess for a ticket:

```sql
SELECT COUNT(*)
FROM agent_instances
WHERE department_id = $1 AND status = 'running';
```

Compare to the department's `concurrency_cap`. If at cap, the event is *not* marked processed; it's deferred. The fallback poll will pick it up again on the next cycle. (Simpler than a priority queue for M1; revisit if deferred events starve under real load.)

**Race condition note**: two concurrent spawns could both see count < cap and both spawn, exceeding the cap by one. For M1 this is acceptable — the supervisor is single-process, and the spawn loop is sequential per incoming event (events are processed one at a time per goroutine). If M2+ introduces parallel event processing, the accounting moves to a SELECT FOR UPDATE on the department row or an advisory lock.

---

## Subprocess contract (M1 placeholder)

For M1, the "agent" is a fake command configurable via env var:

```
ORG_OS_FAKE_AGENT_CMD="sh -c 'echo hello from $TICKET_ID; sleep 2'"
```

The supervisor:
1. Substitutes `$TICKET_ID` and `$DEPARTMENT_ID` in the command string
2. Launches via `exec.CommandContext` with a configurable timeout (default 60s)
3. Captures stdout and stderr, logs them via slog with `ticket_id` and `agent_instance_id` as structured fields
4. On exit: updates `agent_instances.status` to `succeeded`, `failed`, or `timeout`; sets `finished_at`, `exit_reason`
5. On context cancellation (shutdown): sends SIGTERM, waits 5s, then SIGKILL

The spawn function has a clear signature that will be swappable in M2 for real Claude Code invocation. The fake-spawn vs real-spawn decision lives behind an interface.

---

## Graceful shutdown

On SIGTERM/SIGINT:
1. Stop accepting new events (stop the LISTEN loop and the fallback poller)
2. Cancel the root context, which propagates to all in-flight subprocesses
3. Wait up to N seconds (default 30) for subprocesses to exit cleanly
4. Log any still-running subprocesses with their pids, then exit anyway

All `agent_instances` rows with `status = 'running'` at shutdown should be left as-is. On startup, the supervisor runs a recovery query:

```sql
UPDATE agent_instances
SET status = 'failed', exit_reason = 'supervisor_restarted', finished_at = NOW()
WHERE status = 'running' AND started_at < NOW() - INTERVAL '5 minutes';
```

This marks abandoned instances as failed. The `5 minutes` grace period is a safety buffer; tune based on real behavior.

---

## Testing requirements

The spec should produce tests covering:

1. **Unit tests**: concurrency accounting logic, event payload parsing, command template substitution, subprocess lifecycle state transitions
2. **Integration tests** (via `testcontainers-go` Postgres): end-to-end event flow — insert a ticket, verify the trigger fires, verify the supervisor receives the event, verify a subprocess spawns, verify `agent_instances` gets the right row
3. **Chaos tests**: kill the Postgres connection mid-flight, verify reconnect + fallback poll catches missed events; kill a subprocess with SIGKILL, verify the supervisor records `status = failed`

Test coverage target: meaningful coverage on the concurrency and pg_notify paths. Don't chase coverage numbers on boilerplate.

---

## Observability

Minimum for M1:
- Structured logs via slog with `ticket_id`, `department_id`, `agent_instance_id`, `event_id` as fields
- A `/health` HTTP endpoint returning 200 if the pg LISTEN connection is alive and the fallback poller has run within the last 2× poll interval
- A `/metrics` endpoint is out of scope for M1 — add in M2 if needed

---

## What the spec must answer

Things the spec must make concrete (the architecture doc is deliberately vague on these):

1. Exact command-line interface of the binary (flags, env vars, config file support?)
2. Exact Postgres migrations file layout and naming
3. Exact Go package/module structure (`cmd/supervisor`, `internal/...`)
4. Exact `sqlc` configuration and query file layout
5. How the event dispatcher maps channel names to handlers (static registration? dynamic?)
6. Subprocess output log format and rotation (stream to slog? write to files?)
7. Health endpoint semantics (what "healthy" means precisely)

Things the spec must *not* re-decide (these are settled above):
- Language, libraries, data model, pg_notify channel shape, missed-event strategy, concurrency model, scope of M1

---

## Acceptance criteria for M1

M1 is done when, starting from a clean Postgres:

1. Run the migrations; the schema exists
2. Insert a department (`INSERT INTO departments (slug, name, concurrency_cap) VALUES ('engineering', 'Engineering', 2)`)
3. Start the supervisor binary
4. In another connection, insert 3 tickets for that department in quick succession
5. Observe: 2 subprocesses spawn immediately, 1 is deferred until a slot frees
6. All 3 eventually produce `agent_instances` rows with `status = 'succeeded'`
7. All 3 `event_outbox` rows are marked `processed_at`
8. SIGTERM the supervisor; in-flight subprocesses are terminated cleanly; the binary exits within 30s
9. Restart the supervisor; the recovery query marks any leftover `running` instances as `failed`
10. Insert a 4th ticket; it processes cleanly on the new supervisor instance

If all 10 steps pass, M1 ships.
