# Feature specification: M1 — event bus and supervisor core

**Feature branch**: `m1-event-bus`
**Created**: 2026-04-21
**Status**: Draft
**Input**: `/speckit.specify` — "Produce the M1 specification for Garrison: the event bus and supervisor core."

This spec is bound by `specs/_context/m1-context.md`. Decisions settled there (stack, data model, channel names, missed-event strategy, concurrency model, scope) are inputs, not questions. Where this spec cites a constraint, the authoritative text lives in that file — consult it rather than the paraphrase here.

## Clarifications

### Session 2026-04-21

- Q: Startup behavior when Postgres is unreachable → A: Reuse the FR-008 reconnect backoff (100ms → 30s cap) for the initial connect; process stays up and `/health` returns 503 until first successful connect.
- Q: `/health` network binding and authentication → A: Bind `0.0.0.0:$PORT`, no authentication in M1; network-level exposure is governed by the container runtime and Coolify routing.
- Q: Multi-supervisor safety net (accidental double-run) → A: Acquire `pg_try_advisory_lock(<fixed M1 key>)` on the LISTEN connection at startup; exit non-zero if not acquired. Operators must stop the old supervisor before starting the new one (no blue/green in M1).
- Q: Startup sequence and `/health` warmup → A: Startup mirrors reconnect — connect → advisory lock → recovery → initial fallback poll → LISTEN. `/health` flips to 200 as soon as LISTEN is active; no warmup window in `/health` semantics.

## User scenarios and testing (mandatory)

### User story 1 — process a ticket end-to-end (priority: P1)

The operator inserts a ticket into Postgres. The supervisor picks it up from the event bus, spawns a subprocess (the M1 fake agent), records the subprocess lifecycle in `agent_instances`, and marks the `event_outbox` row processed once the work is accepted.

**Why this priority**: this is the minimum viable slice of Garrison. Everything else in M1 — concurrency caps, reconnect logic, shutdown — is protection around this path. If this path does not work, none of the others matter.

**Independent test**: starting from a clean Postgres with migrations applied and a single department inserted, insert one ticket and start the supervisor. The subprocess runs, an `agent_instances` row lands with a terminal status, and the corresponding `event_outbox` row has `processed_at` set.

**Acceptance scenarios**:

1. **Given** a clean database with migrations applied, a department `engineering` (cap 3), and the supervisor running, **When** the operator inserts a ticket for `engineering`, **Then** the supervisor receives `work.ticket.created` via LISTEN, spawns the configured fake subprocess, writes a `running` `agent_instances` row, and on subprocess exit updates the row to `succeeded` with `finished_at` set.
2. **Given** the same setup, **When** the subprocess exits successfully, **Then** the corresponding `event_outbox` row has `processed_at` set in the same transaction that transitions the `agent_instances` row to its terminal state.
3. **Given** the supervisor is not running, **When** the operator inserts a ticket, **Then** on later supervisor startup the ticket's `event_outbox` row is picked up by the fallback poll and processed normally.

---

### User story 2 — concurrency cap enforcement (priority: P2)

The operator inserts more tickets for a department than its `concurrency_cap`. The supervisor spawns up to the cap, defers excess events by leaving `processed_at` NULL, and the fallback poll continues picking them up until all have run.

**Why this priority**: without a cap, the operator cannot safely insert work in bulk. Garrison's cost bound depends on parallelism being bounded (see constitution principle X).

**Independent test**: with a department cap of 2 and a fake agent that sleeps long enough to hold a slot (e.g. 2s), insert 3 tickets in quick succession; observe that at most 2 subprocesses run concurrently and that all 3 reach a terminal status.

**Acceptance scenarios**:

1. **Given** `engineering` with `concurrency_cap = 2` and two `running` `agent_instances` rows, **When** a new `work.ticket.created` event arrives, **Then** the supervisor does not spawn and does not mark the event processed; the event remains visible to the next fallback poll.
2. **Given** a deferred event, **When** one of the two running subprocesses finishes and a fallback poll cycle runs, **Then** the deferred event is handled and a new subprocess spawns.
3. **Given** three tickets inserted in quick succession for a cap-2 department, **When** all subprocesses have completed, **Then** exactly three `agent_instances` rows exist with terminal statuses and all three `event_outbox` rows have `processed_at` set.

---

### User story 3 — survive Postgres disconnect (priority: P2)

Postgres becomes briefly unreachable. Events inserted during the outage — and events that arrived via LISTEN but were not delivered during the reconnect window — are still processed once the connection returns. The operator does not have to intervene.

**Why this priority**: a supervisor that loses events on every brief Postgres blip is not a supervisor the operator will trust on Hetzner. Reconnect + fallback poll is the correctness floor for the event bus.

**Independent test**: with the supervisor running, sever the pg connection (e.g. pause the container or drop the connection server-side); insert one or more tickets during the outage; restore the connection; observe all tickets get processed after the reconnect backoff settles.

**Acceptance scenarios**:

1. **Given** a running supervisor, **When** the LISTEN connection drops, **Then** the supervisor reconnects with exponential backoff bounded between 100ms and 30s as defined in "pg_notify contract" in `m1-context.md`.
2. **Given** a reconnect has just succeeded, **When** the supervisor resumes work, **Then** it runs the fallback poll immediately — before re-issuing LISTEN — and processes any unprocessed `event_outbox` rows.
3. **Given** a notification delivered twice (once via LISTEN after a reconnect, once via fallback poll), **When** the supervisor handles the duplicate, **Then** processing is idempotent: no second subprocess spawns and no duplicate `agent_instances` row is created.

---

### User story 4 — graceful shutdown and recovery on restart (priority: P3)

The operator sends SIGTERM (or SIGINT) to the supervisor while subprocesses are in flight. In-flight subprocesses are cancelled cleanly, the binary exits within the shutdown grace, and on restart any `agent_instances` rows left in `running` beyond the recovery window are reconciled to `failed`.

**Why this priority**: operator needs to deploy, restart, and reboot without leaking subprocesses or leaving the database in an inconsistent state. Lower than P2 only because in practice P3 is exercised less often than P2 conditions, not because correctness is optional.

**Independent test**: run the supervisor with a long-sleep fake agent, insert a ticket, then send SIGTERM; verify the subprocess receives SIGTERM, exits, and the binary terminates within the configured grace. Separately, seed an `agent_instances` row with `status = 'running'` and `started_at` older than the recovery window, start the supervisor, and verify the row is updated to `status = 'failed'` with `exit_reason = 'supervisor_restarted'`.

**Acceptance scenarios**:

1. **Given** the supervisor is running one or more subprocesses, **When** the operator sends SIGTERM, **Then** the supervisor stops accepting new events, cancels the root context, forwards SIGTERM to each subprocess, waits up to 5s per subprocess for clean exit, and escalates to SIGKILL on the holdouts before the shutdown grace expires.
2. **Given** the supervisor has cancelled all subprocesses, **When** the shutdown grace has elapsed (default 30s), **Then** the binary exits with a non-zero code only if subprocesses had to be SIGKILLed; otherwise exits 0.
3. **Given** a stale `agent_instances` row with `status = 'running'` older than the recovery window, **When** the supervisor starts, **Then** the recovery query updates the row to `status = 'failed'`, `exit_reason = 'supervisor_restarted'`, `finished_at = NOW()` before LISTEN begins.
4. **Given** recovery has completed, **When** the operator inserts a new ticket, **Then** the new supervisor instance processes it cleanly.

---

### Edge cases

- Event arrives via LISTEN after already having been processed via the fallback poll: the second handling must be a no-op (dedupe via `processed_at`).
- Notification payload cannot be parsed (malformed `event_id`, missing field): log at error level with the raw payload, drop the notification, and rely on the fallback poll to pick up the underlying row.
- `event_outbox` row referenced by a notification does not exist: this should be impossible because trigger + `pg_notify` share a transaction, but log and continue if observed.
- Subprocess exits with non-zero status: `agent_instances.status = 'failed'`, `exit_reason` captures the exit code.
- Subprocess exceeds the timeout context: `status = 'timeout'`, SIGTERM → 5s → SIGKILL lifecycle applies, `exit_reason` captures the timeout condition.
- Subprocess is SIGKILLed externally (OOM, kernel, operator): supervisor observes the exit and records `status = 'failed'`, `exit_reason` captures the kill signal.
- Ticket's `department_id` references a department that no longer exists: fail loudly. The supervisor MUST log at error level with `event_id`, `ticket_id`, and the missing `department_id`, MUST NOT spawn, and MUST mark the `event_outbox` row `processed_at` to prevent an infinite retry loop.
- Very high event rate exceeds the fallback poll batch size in one cycle: the poll runs again on the next interval and drains the backlog incrementally. LIMIT is fixed per cycle (see open questions on configurability).
- `concurrency_cap = 0` is valid and means "pause this department": all incoming events defer until the cap is raised.
- Two notifications for the same `event_id` arrive in rapid succession before either is marked processed: the second handler must observe `processed_at IS NOT NULL` after the first commits and no-op. This is the same mechanism as the LISTEN/poll dedupe case.

## Requirements (mandatory)

### Functional requirements

Behavioral requirements — the supervisor must do these things:

- **FR-001**: The supervisor MUST LISTEN on `work.ticket.created` using a dedicated Postgres connection held outside any connection pool, per "pg_notify contract" in `m1-context.md`.
- **FR-002**: The supervisor MUST parse the notification payload as `{"event_id": "<uuid>"}` and then fetch the full event row from `event_outbox` by id. It MUST NOT rely on notification payload content beyond `event_id`.
- **FR-003**: The supervisor MUST check the target department's `concurrency_cap` against the count of `agent_instances` rows with `status = 'running'` for that department before spawning. If at or over cap, the supervisor MUST defer the event by leaving `processed_at` NULL. A `concurrency_cap` of `0` is a valid "pause" signal: all events for that department defer until the cap is raised.
- **FR-004**: The supervisor MUST spawn the configured fake agent subprocess via `exec.CommandContext` with a derived context carrying the configured subprocess timeout. The command template MUST substitute `$TICKET_ID` and `$DEPARTMENT_ID` before execution, per "Subprocess contract (M1 placeholder)" in `m1-context.md`.
- **FR-005**: The supervisor MUST create an `agent_instances` row in `status = 'running'` before the subprocess starts, and MUST update it to a terminal status (`succeeded`, `failed`, or `timeout`) with `finished_at` and `exit_reason` set when the subprocess exits.
- **FR-006**: The supervisor MUST mark the corresponding `event_outbox.processed_at` in the same transaction that writes the terminal `agent_instances` update. Event handling MUST be idempotent: handling an already-processed event MUST be a no-op.
- **FR-007**: The supervisor MUST run a fallback poll of unprocessed `event_outbox` rows on a configurable interval (default 5s) using the query shape in "pg_notify contract" in `m1-context.md`. It MUST also run this poll immediately on initial startup (after the FR-011 recovery query) and on reconnect, in both cases before issuing LISTEN. The canonical startup sequence is therefore: connect (FR-017) → advisory lock (FR-018) → recovery (FR-011) → initial fallback poll → LISTEN (FR-001).
- **FR-008**: The supervisor MUST reconnect the LISTEN connection on drop with exponential backoff starting at 100ms and capped at 30s. The backoff MUST reset to its minimum on successful reconnect.
- **FR-009**: The supervisor MUST propagate a root `context.Context` that is cancelled on SIGTERM or SIGINT. All goroutines — LISTEN loop, fallback poller, subprocess manager, health endpoint — MUST accept a derived context and exit on cancellation, per "Concurrency model" in `m1-context.md`.
- **FR-010**: On root-context cancellation, the supervisor MUST send SIGTERM to each running subprocess, wait up to 5s per subprocess for clean exit, then send SIGKILL. It MUST NOT wait longer than the configured shutdown grace (default 30s) in total before exiting.
- **FR-011**: On startup, the supervisor MUST run the recovery query in "Graceful shutdown" of `m1-context.md` to mark any `agent_instances` rows with `status = 'running'` older than the recovery window as `failed` with `exit_reason = 'supervisor_restarted'`, before beginning LISTEN.
- **FR-012**: The supervisor MUST NOT introduce library dependencies beyond the locked list in "The stack (locked)" in `m1-context.md`. Proposed additions go in the spec's open questions, not the imports.

Requirements that the spec must answer (items 1, 5, 6, 7 of "What the spec must answer" in `m1-context.md`; items 3 and 4 are deferred to `/speckit.plan`; item 2 is flagged for `/speckit.clarify`):

- **FR-013** (CLI interface): The binary MUST take all runtime configuration from environment variables, with a typed config struct validated at startup. The binary MUST support at minimum: `--version` (print version and exit), `--help` (print usage and exit), and `--migrate` (run `goose` migrations against the configured database and exit, using the same connection-string env var as the supervisor runtime). Config loading from a file is NOT supported in M1.
- **FR-014** (event dispatcher registration): Event handlers MUST be registered statically at process startup, keyed by channel name (`work.ticket.created` is the only M1 channel). Dynamic registration is out of scope. The dispatcher MUST produce a clear error at startup if a received notification's channel has no registered handler.
- **FR-015** (subprocess output): Subprocess stdout and stderr MUST be captured by the supervisor line-by-line. Each complete line MUST produce one `slog` record with structured fields `ticket_id`, `department_id`, `agent_instance_id`, `event_id`, and `stream` (stdout|stderr). Per-invocation log files are out of scope for M1.
- **FR-016** (health endpoint): The binary MUST expose a single HTTP `/health` endpoint returning `200 OK` when both (a) a fresh database ping via the shared connection pool succeeds and (b) the fallback poller has completed a cycle within the last `2 × poll_interval`. Otherwise it MUST return `503`. No readiness/liveness split. The listener port MUST be configurable and defaults to `8080`. The endpoint MUST bind to `0.0.0.0` on that port and MUST NOT require authentication in M1; network-level exposure is delegated to the container runtime and Coolify routing rules.
- **FR-017** (initial connect): If Postgres is unreachable at startup, the supervisor MUST retry using the same exponential backoff as FR-008 (100ms → 30s cap, reset on success) rather than exit. The process MUST remain running and `/health` MUST return `503` until the first successful connect. The NFR-007 1s startup target only applies when Postgres is reachable at process start.
- **FR-018** (single-instance enforcement): On startup, after the initial connect and before the FR-011 recovery query, the supervisor MUST attempt `pg_try_advisory_lock(<fixed M1 key>)` on the dedicated LISTEN connection. If the lock is not acquired, the supervisor MUST log an error naming the contention and exit non-zero. The lock is released automatically by Postgres when the LISTEN connection closes. Blue/green rolling deploys are not supported in M1; the operator MUST stop the old supervisor before starting the new one.

### Non-functional requirements

- **NFR-001** (fallback poll interval): Default 5s. Configurable. Must be positive and at least 1s.
- **NFR-002** (reconnect backoff): Exponential 100ms → 30s cap. Resets on successful reconnect. Not operator-tunable in M1.
- **NFR-003** (subprocess timeout): Default 60s per subprocess. Configurable via environment variable.
- **NFR-004** (shutdown grace): Default 30s total from SIGTERM to forced exit. Configurable.
- **NFR-005** (per-subprocess cancellation grace): SIGTERM → wait 5s → SIGKILL. Not operator-tunable in M1.
- **NFR-006** (startup recovery window): Fixed at 5 minutes in M1. Not operator-configurable. Re-evaluated against real behavior in the M1 retro.
- **NFR-007** (startup time): The binary MUST begin listening for events within 1s of process start on a reachable Postgres. Startup time includes recovery-query execution and the initial fallback poll. Go was chosen in part to make this bound achievable (see RATIONALE §9).
- **NFR-008** (logging): All supervisor logs MUST be structured `slog` output with fields `ticket_id`, `department_id`, `agent_instance_id`, `event_id` whenever those identifiers are in scope.
- **NFR-009** (fallback poll batch size): Fixed at `LIMIT 100` per cycle in M1. Not operator-configurable. Tunability is a post-M1 concern if real load warrants it.
- **NFR-010** (deployment): The binary MUST build as a single static Linux binary via `CGO_ENABLED=0 go build`. No runtime dependencies beyond a reachable Postgres.

### Key entities

- **Department**: an organizational unit with a `concurrency_cap`. Departments bound the parallelism of any agent work targeted at them.
- **Ticket**: a unit of work targeted at a department. Insertion of a ticket is what the operator does; everything else is downstream reaction.
- **Event outbox entry**: a durable record of an event fired by a state change, plus a `processed_at` marker. The mechanism that makes missed notifications recoverable.
- **Agent instance**: a record of one subprocess spawn — its pid, started/finished timestamps, terminal status, exit reason. The supervisor's bookkeeping row for each unit of work it handled.

Full schema lives in "Data model for M1" in `m1-context.md`. Do not duplicate it here.

## Success criteria (mandatory)

### Measurable outcomes

- **SC-001**: All ten acceptance steps in "Acceptance criteria for M1" of `m1-context.md` pass against a clean Postgres, executed in order and unmodified.
- **SC-002**: Over a run of 100 tickets inserted into a cap-2 department with a fake agent that sleeps 1–3s, every ticket produces exactly one `agent_instances` row with a terminal status and the matching `event_outbox` row has `processed_at` set.
- **SC-003**: The observed count of concurrently-`running` `agent_instances` rows for a single department never exceeds `concurrency_cap + 1` during the 100-ticket run (the +1 is the documented M1 race window; see "Concurrency accounting" in `m1-context.md`).
- **SC-004**: After a forced Postgres disconnect of at least 5s during which at least one ticket is inserted, all inserted tickets are processed within one fallback poll interval after the reconnect settles.
- **SC-005**: After SIGTERM to a supervisor with at least one in-flight subprocess using the default fake agent, the binary exits within the configured shutdown grace; no subprocess survives the binary.
- **SC-006**: After supervisor restart with at least one stale `agent_instances` row in `running` older than the recovery window, that row is in a terminal status before the first new event is handled.
- **SC-007**: Integration tests using `testcontainers-go` cover the end-to-end event flow and at least one chaos case (connection drop, subprocess SIGKILL), as required by "Testing requirements" in `m1-context.md`.

## Assumptions

- The operator has direct Postgres access (psql or equivalent) for inserting departments and tickets. M1 has no UI surface.
- Only one supervisor instance runs per database. Enforced at runtime via `pg_try_advisory_lock` on the LISTEN connection (FR-018); the lock releases automatically on connection close. Multi-process parallel event processing is out of scope and flagged in "Concurrency accounting" of `m1-context.md` as a post-M1 concern.
- Postgres version supports `LISTEN`/`NOTIFY`, `gen_random_uuid()`, JSONB, and trigger functions — any recent Postgres (14+) qualifies.
- The fake-agent command is supplied by the operator via env var. No command is hard-coded into the binary.
- The binary runs in a Linux container (Hetzner + Coolify target per constitution principle XI and `AGENTS.md` deployment rules). Non-Linux OSes are not tested.
- Subprocess output volume is modest (fake agents for M1 emit a handful of lines). High-volume output handling is a future concern.
- Clock skew between the supervisor host and Postgres is small relative to the recovery window (5 minutes). NTP is assumed.

## Open questions for `/speckit.clarify`

The following are spec-level ambiguities that the inputs do not fully resolve:

- ~~**Q1 — migration tool**~~: **Resolved — `goose`.**
- ~~**Q2 — CLI scope**~~: **Resolved — the supervisor binary includes a `--migrate` subcommand that shares its connection-string env var with the runtime.** See FR-013.
- ~~**Q3 — subprocess log granularity**~~: **Resolved — line-by-line, one `slog` record per line.** See FR-015.
- ~~**Q4 — health endpoint semantics**~~: **Resolved — single `/health` endpoint; "connection alive" means a fresh ping query succeeded; default listener port `8080` (configurable).** See FR-016.
- ~~**Q5 — recovery window default**~~: **Resolved — fixed at 5 minutes for M1, not configurable.** See NFR-006.
- ~~**Q6 — fallback poll batch size configurability**~~: **Resolved — fixed at `LIMIT 100` for M1; tunability deferred post-M1 if real load warrants.** See NFR-009.
- ~~**Q7 — concurrency cap of 0**~~: **Resolved — valid, meaning "pause this department": all events defer until the cap is raised.** See FR-003.
- ~~**Q8 — stale department handling**~~: **Resolved — fail loudly. Log at error level, do not spawn, mark the event processed to avoid a retry loop.** See the edge-case entry on deleted-department references.
- ~~**Q9 — startup time target**~~: **Resolved — tightened to 1s. Go was chosen to make a sub-second bound achievable.** See NFR-007.

Items not listed here are either settled in `m1-context.md` (and therefore not in scope for clarification) or deferred to `/speckit.plan` (package structure, `sqlc` configuration, file layout — items 3 and 4 of "What the spec must answer" in `m1-context.md`).
