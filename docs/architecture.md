# Architecture (pointer)

This is the short orientation document. The load-bearing
architecture material lives one directory up, in
[`ARCHITECTURE.md`](../ARCHITECTURE.md) and
[`RATIONALE.md`](../RATIONALE.md). This file exists so readers who
land in `docs/` know which one to open for which question.

## Which document answers which question

| You're asking... | Open... |
|---|---|
| What are the components and how do they talk? | [`ARCHITECTURE.md`](../ARCHITECTURE.md) §"System components" |
| What does the database schema look like? | [`ARCHITECTURE.md`](../ARCHITECTURE.md) §"Data model (sketch)" |
| How do events flow through the system? | [`ARCHITECTURE.md`](../ARCHITECTURE.md) §"Event flow" |
| What are the build milestones and their scope? | [`ARCHITECTURE.md`](../ARCHITECTURE.md) §"Build plan — milestones" |
| What will the dashboard surfaces look like? | [`ARCHITECTURE.md`](../ARCHITECTURE.md) §"Dashboard surfaces" |
| Why Postgres + `pg_notify` instead of Redis / RabbitMQ / NATS? | [`RATIONALE.md`](../RATIONALE.md) §1 |
| Why ephemeral agents instead of long-running daemons? | [`RATIONALE.md`](../RATIONALE.md) §2 |
| Why MemPalace instead of long context windows? | [`RATIONALE.md`](../RATIONALE.md) §3 |
| Why Postgres state instead of git-backed markdown? | [`RATIONALE.md`](../RATIONALE.md) §4 |
| Why soft gates on memory hygiene instead of hard ones? | [`RATIONALE.md`](../RATIONALE.md) §5 |
| Why summoned CEO instead of a daemon? | [`RATIONALE.md`](../RATIONALE.md) §6 |
| Why skills.sh instead of a curated library? | [`RATIONALE.md`](../RATIONALE.md) §7 |
| Why Go for the supervisor instead of TS/Python? | [`RATIONALE.md`](../RATIONALE.md) §8 |
| Why self-hosted Hetzner instead of a cloud provider? | [`RATIONALE.md`](../RATIONALE.md) §9 |
| Why spec-first instead of prototype-first? | [`RATIONALE.md`](../RATIONALE.md) §10 |
| Why per-department concurrency caps? | [`RATIONALE.md`](../RATIONALE.md) §11 |
| What is this system explicitly *not*? | [`RATIONALE.md`](../RATIONALE.md) §12 |
| What actually shipped in M1 and what the spec got wrong? | [M1 retro](./retros/m1.md) |

## The one-paragraph summary

Garrison is an event-driven orchestrator for AI agent subprocesses.
Postgres is the source of truth for state (tickets, departments,
concurrency, event outbox) and the event bus (`pg_notify` inside
the same transaction as the state change). A single Go supervisor
holds a dedicated LISTEN connection, enforces per-department
concurrency caps, spawns short-lived agent subprocesses, and reaps
them. Agents are ephemeral: they wake on an event, read their
context, do the work, write to MemPalace, transition the ticket,
and exit. The web UI (M3+) is the operator console. There is no
long-running agent daemon anywhere in the system. If `pg_notify`
drops a notification during a reconnect, a `processed_at`-driven
fallback poll picks it up.

## The one-diagram summary

```
            +-------------------+
            |   Operator UI     |   (M3+, not yet shipped)
            +---------+---------+
                      |
                      | INSERT ticket
                      v
            +-------------------+
            |    Postgres 17    |<--+  fallback poll
            |                   |   |  (processed_at IS NULL)
            | ticket_created_   |   |
            | emit trigger      |   |
            +---------+---------+   |
                      |             |
                      | pg_notify   |
                      v             |
            +-------------------+   |
            |    Supervisor     |---+
            | (Go, single proc) |
            +---------+---------+
                      |
                      | exec.CommandContext
                      v
            +-------------------+        +---------------+
            |  Agent subprocess |------->|   MemPalace   |
            |  (M1: sh -c ...   |        | (M2+, MCP)    |
            |   M2+: claude)    |        +---------------+
            +-------------------+
```

Load-bearing invariants:

- One supervisor per database (FR-018 advisory lock).
- One event = one successful spawn (FR-006 atomic dedupe tx).
- `pg_notify` + `processed_at` together mean notifications can be
  lost without work being lost.
- Agents hold no state. Anything that needs to survive restart
  belongs in Postgres or MemPalace.

Anything that contradicts those invariants is either a bug or a
deliberate scope-change that needs to be debated in `RATIONALE.md`
before code lands.
