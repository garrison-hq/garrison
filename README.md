# Garrison

[![Build](https://img.shields.io/badge/build-local-lightgrey)](#running-the-supervisor-locally)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](./LICENSE)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8.svg)](https://go.dev/dl/)

Most agent orchestrators either burn tokens on idle heartbeats or lose
everything between sessions. Garrison does neither.

Garrison is an event-driven orchestrator for AI agent subprocesses. A
Go supervisor listens on Postgres `pg_notify`, spawns Claude Code
processes on demand, enforces per-department concurrency caps, and
gets out of the way. State lives in Postgres. Memory lives in
MemPalace (not yet shipped). Agents are ephemeral: they wake on an
event, do the work, write results, and exit.

**Status: M1 shipped 2026-04-22.** M2 is in design. Built alongside
other work by one person. Production use at your own risk.

---

## Why this exists

Before Garrison, I was running several AI agents as long-lived
processes on cron heartbeats and hit an efficiency wall: most
agents spent most of their time waking up on schedule, checking for
work, finding nothing, and going back to sleep — burning tokens to
produce no output. Idle cost scaled with the number of agents, not
with the amount of actual work happening. Garrison is the
rebuild: drop the heartbeat loop, spawn agents only when something
real changed, and keep memory in a durable store instead of in
whatever process happens to be running.

Two defaults flip:

- **Events, not heartbeats.** Agents spawn when something actually
  changed. No work means no processes, no token burn.
- **Durable memory, not context windows.** Every agent writes to a
  shared store before exit. The next spawn reads what the last one
  learned. Institutional knowledge accumulates across instances of
  the same role.

The architecture bets on Postgres being load-bearing: both the state
store and the event bus. See `RATIONALE.md` §1 for why `pg_notify`
won over Redis Streams / RabbitMQ / NATS for this workload.

---

## Architecture (1 minute read)

```mermaid
flowchart LR
    UI[Operator UI] -->|INSERT ticket| PG[(Postgres)]
    PG -->|pg_notify| SUP[Supervisor]
    SUP -->|spawn| A[Agent subprocess]
    A -->|write| MP[(MemPalace)]
    A -->|transition| PG
```

Three components:

- **Postgres 17** — source of truth for tickets, departments, agent
  instances, and the event outbox. Every state change that another
  part of the system reacts to fires `pg_notify` in the same
  transaction as the change itself.
- **Supervisor** — single Go binary. Holds a dedicated LISTEN
  connection (not from the pool), runs a fallback poll in case a
  notification is lost during a reconnect, enforces per-department
  concurrency caps, spawns and reaps subprocesses, reconciles stale
  state on restart.
- **Agent subprocess** — in M1, a placeholder `sh -c` command. In M2
  onward, the real `claude` CLI with a scoped agent.md, a working
  directory, and MCP tools wired in.

The operator UI, MemPalace, CEO chat, and hiring flow do not exist
yet. See [Milestones](#milestones) for the order they arrive in.

For the full system picture (data model, event flow, dashboard
surfaces) see `ARCHITECTURE.md`. For the reasoning behind every
non-obvious choice, see `RATIONALE.md`.

---

## M1 in one paragraph

M1 is the event bus and supervisor core. Postgres schema with
`departments`, `tickets`, `event_outbox`, `agent_instances`. A
trigger that fires `pg_notify('work.ticket.created', ...)` on ticket
insert. A Go binary that holds a dedicated LISTEN connection,
acquires a fixed advisory lock so only one supervisor runs at a
time, reconciles stale `running` rows on startup, listens for new
tickets, checks the per-department cap, spawns a subprocess, pipes
its stdout/stderr into structured logs, waits for exit, writes a
terminal row, and marks the event processed — all under a single
atomic transaction boundary per event. Reconnects with exponential
backoff. Polls every N seconds as a safety net. Graceful shutdown
on SIGTERM. `/health` returns 200 when the DB ping and the most
recent poll are both within tolerance.

What M1 does **not** include: real Claude invocation, MemPalace,
the web UI, the CEO, hiring. Those are M2–M8.

---

## Milestones

| Milestone | Scope | Status |
|---|---|---|
| **M1** | Event bus + supervisor core. Fake agent (`sh -c`). | Shipped 2026-04-22. |
| **M2** | Swap fake agent for real Claude Code. One department. MemPalace MCP wired in. | In design. |
| **M3** | Next.js 16 dashboard, read-only. Kanban, ticket detail, agent activity feed. | Not started. |
| **M4** | Dashboard mutations. Create/drag tickets, edit agent configs. | Not started. |
| **M5** | CEO chat, summoned per-message, read-only. | Not started. |
| **M6** | CEO ticket decomposition + memory hygiene dashboard. | Not started. |
| **M7** | Hiring flow via skills.sh. | Not started. |
| **M8** | Agent-spawned tickets, cross-department dependencies. | Not started. |

Each milestone ships end-to-end functional before the next begins.
No scaffolding for future milestones lands early. See
`ARCHITECTURE.md` §"Build plan — milestones" for the scope notes
per milestone.

---

## Repository layout

```
garrison/
├── supervisor/              # Go 1.25 binary. The only code that runs today.
│   ├── cmd/supervisor/      # main + migrate subcommand + signal handling
│   ├── internal/
│   │   ├── config/          # env-driven typed config
│   │   ├── pgdb/            # pool + dedicated LISTEN conn + backoff dialer
│   │   ├── events/          # Dispatcher, LISTEN loop, fallback poll, reconnect
│   │   ├── spawn/           # subprocess lifecycle, dedupe tx, terminal tx
│   │   ├── concurrency/     # per-department cap check
│   │   ├── recovery/        # startup reconcile of stale `running` rows
│   │   ├── health/          # /health HTTP server
│   │   ├── store/           # sqlc-generated typed queries
│   │   └── testdb/          # testcontainers-go Postgres helper
│   ├── integration_test.go  # //go:build integration
│   ├── chaos_test.go        # //go:build chaos
│   ├── Dockerfile           # alpine:3.20 (see retro for why, not distroless)
│   └── Makefile
├── migrations/              # goose SQL migrations — SINGLE source of truth
├── specs/
│   ├── _context/            # per-milestone binding constraints
│   └── m1-event-bus/        # M1 spec, plan, tasks, acceptance evidence
├── docs/
│   ├── getting-started.md   # make-it-work-from-a-clean-clone instructions
│   ├── architecture.md      # short pointer into ARCHITECTURE.md + RATIONALE.md
│   └── retros/
│       └── m1.md            # what shipped, what the spec got wrong
├── ARCHITECTURE.md          # system structure, data model, build plan
├── RATIONALE.md             # 12 design decisions with alternatives rejected
├── AGENTS.md                # binding guidance for AI coding agents
├── LICENSE                  # AGPL-3.0-only (code)
├── LICENSE-DOCS             # CC-BY-4.0 (specs and documentation)
├── CHANGELOG.md
├── CONTRIBUTING.md
├── CODE_OF_CONDUCT.md
└── SECURITY.md
```

`migrations/` lives at the repo root (not under `supervisor/`) so a
future TypeScript dashboard can consume the same SQL as the Go
supervisor — both sides derive from one schema.

---

## Running the supervisor locally

You need Go 1.25+, Docker, and ~2 minutes. See
[`docs/getting-started.md`](./docs/getting-started.md) for the full
walk-through; the short version:

```bash
# 1. Start a fresh Postgres 17.
docker run -d --name pg-garrison -p 5432:5432 \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=orgos postgres:17

# 2. Build and run migrations.
cd supervisor
make build
export ORG_OS_DATABASE_URL='postgres://postgres:postgres@localhost:5432/orgos?sslmode=disable'
./bin/supervisor --migrate

# 3. Insert a department.
docker exec pg-garrison psql -U postgres orgos -c \
  "INSERT INTO departments (slug, name, concurrency_cap) VALUES ('engineering', 'Engineering', 2);"

# 4. Run the supervisor with a placeholder agent command.
export ORG_OS_FAKE_AGENT_CMD='sh -c "echo hello from $TICKET_ID; sleep 2"'
./bin/supervisor

# 5. In another shell, insert a ticket and watch the supervisor react.
docker exec pg-garrison psql -U postgres orgos -c \
  "INSERT INTO tickets (department_id, objective)
   SELECT id, 'hello-world' FROM departments WHERE slug = 'engineering';"
```

Acceptance evidence for M1 lives at
[`specs/m1-event-bus/acceptance-evidence.md`](./specs/m1-event-bus/acceptance-evidence.md).
It documents the 10-step acceptance sequence run against `make docker`
on a fresh Postgres 17, with full log excerpts.

---

## Tests

```bash
cd supervisor
make test               # unit tests (pkg-local, ~1s)
make test-integration   # full integration suite, spins Postgres via testcontainers
make test-chaos         # reconnect + external SIGKILL + shutdown-with-inflight
```

Integration and chaos suites use `//go:build integration` and
`//go:build chaos` tags and spin real Postgres containers via
`testcontainers-go`. No DB mocking. See `supervisor/chaos_test.go`
for the three fault scenarios M1 commits to handling: backend
termination during LISTEN, external SIGKILL of a running child, and
SIGTERM of the supervisor with an in-flight subprocess.

---

## Configuration

All configuration is env-driven. Defaults in parentheses.

| Variable | Purpose | Default |
|---|---|---|
| `ORG_OS_DATABASE_URL` | Postgres DSN. | *required* |
| `ORG_OS_POLL_INTERVAL` | Fallback-poll cadence. | `2s` |
| `ORG_OS_SUBPROCESS_TIMEOUT` | Per-subprocess hard timeout. | `60s` |
| `ORG_OS_SHUTDOWN_GRACE` | SIGTERM-to-forced-exit budget. | `30s` |
| `ORG_OS_HEALTH_PORT` | `/health` HTTP port. | `8080` |
| `ORG_OS_FAKE_AGENT_CMD` | Placeholder subprocess command (M1 only). | *required* |

Config is immutable per process. SIGHUP is treated as SIGTERM — see
the M1 retro for why.

---

## Why the AGPL

The code is licensed under **AGPL-3.0-only**. The specs and
documentation are licensed under **CC-BY-4.0**.

Choosing AGPL over MIT/Apache is a deliberate stance: Garrison is
the kind of thing a SaaS vendor could wrap and serve without ever
contributing back. AGPL's network-copyleft clause closes that loop.
If you run a modified Garrison as a service, you publish your
modifications. If you use it privately inside your own
organization, AGPL imposes nothing you wouldn't do anyway.

Specs and documentation are dual-licensed as CC-BY-4.0 because the
decisions in `RATIONALE.md` and the shape of the specs may be
useful reference material for anyone thinking through similar
problems, and copyleft has no particular benefit for prose.

See [`LICENSE`](./LICENSE) and [`LICENSE-DOCS`](./LICENSE-DOCS) for
the full texts.

---

## Contributing

The short version: **open an issue before a PR** for anything beyond
a typo or obvious bug fix. This is one person building alongside
other work, so response time is days-to-weeks, not hours. Not every
proposal will land — some will conflict with decisions in
`RATIONALE.md`, some will be out of scope for the current
milestone. That's fine; we can discuss on the issue.

Binding rules for any patch:

- Read `AGENTS.md` before generating code. It is binding for human
  and AI contributors alike.
- The supervisor has a **locked dependency list** in `AGENTS.md`.
  New dependencies need a commit-message justification and a retro
  flag.
- Tests matter: integration + chaos must pass. No DB mocking.
- Code goes in as AGPL-3.0-only. Documentation as CC-BY-4.0. See
  `CONTRIBUTING.md` for the licensing note.

Full details: [`CONTRIBUTING.md`](./CONTRIBUTING.md). Conduct:
[`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md). Security reports:
[`SECURITY.md`](./SECURITY.md).

---

## Specs and retros

Garrison is built spec-first. Each milestone begins with a spec in
`specs/m{N}-*/` and ends with a retro in `docs/retros/m{N}.md`.
The M1 artifacts are the first public evidence of what this
workflow actually produces:

- **Spec** — [`specs/m1-event-bus/spec.md`](./specs/m1-event-bus/spec.md)
- **Plan** — [`specs/m1-event-bus/plan.md`](./specs/m1-event-bus/plan.md)
- **Tasks** — [`specs/m1-event-bus/tasks.md`](./specs/m1-event-bus/tasks.md)
- **Acceptance evidence** — [`specs/m1-event-bus/acceptance-evidence.md`](./specs/m1-event-bus/acceptance-evidence.md)
- **Retro** — [`docs/retros/m1.md`](./docs/retros/m1.md)

The retro is the honest one: six things the spec got wrong, what the
tests that caught each one looked like, and the trade-offs accepted
when the fix went in. It's the best single document for
understanding what actually gets built when this process runs.

---

## Further reading

- [`ARCHITECTURE.md`](./ARCHITECTURE.md) — components, data model,
  event flow, dashboard surfaces, build plan.
- [`RATIONALE.md`](./RATIONALE.md) — 12 decisions with alternatives
  considered, trade-offs accepted, and the "what this system is
  not" list.
- [`AGENTS.md`](./AGENTS.md) — project-level guidance for any AI
  coding agent working in this repo. Also worth reading for human
  contributors: the scope discipline and the locked-dependency rule
  apply to everyone.
- [`docs/getting-started.md`](./docs/getting-started.md) —
  clean-clone-to-running walkthrough.
- [`CHANGELOG.md`](./CHANGELOG.md) — what shipped when.

---

## License

Code: [AGPL-3.0-only](./LICENSE). Specs and documentation:
[CC-BY-4.0](./LICENSE-DOCS).
