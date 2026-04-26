<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/garrison-mono-dark.svg">
    <img alt="garrison" src="brand/garrison-mono-light.svg" width="320">
  </picture>
</p>

# Garrison

[![Build](https://img.shields.io/badge/build-local-lightgrey)](#running-the-supervisor-locally)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](./LICENSE)
[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8.svg)](https://go.dev/dl/)

Most agent orchestrators either burn tokens on idle heartbeats or lose
everything between sessions. Garrison does neither.

Garrison is an event-driven orchestrator for AI agent subprocesses. A
Go supervisor listens on Postgres `pg_notify`, fetches per-spawn
secrets from a self-hosted Infisical vault, spawns Claude Code
processes on demand, enforces per-department concurrency caps, and
gets out of the way. State lives in Postgres. Memory lives in
MemPalace, wired in as an MCP server. Agents are ephemeral: they
wake on an event, do the work, commit through a single
`finalize_ticket` MCP tool that atomically writes both the Postgres
transition and the MemPalace diary + KG triples, then exit.

**Status:** M1, M2.1, M2.2, M2.2.1, M2.2.2, and M2.3 shipped
(2026-04-22 → 2026-04-24). **M3 (read-only dashboard) is the active
milestone.** Built alongside other work by one person. Production
use at your own risk.

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
    UI[Operator] -->|insert ticket| PG[(Postgres)]
    PG -->|pg_notify| SUP[Supervisor]
    VAULT[(Infisical Vault)] -.->|secrets, supervisor-only| SUP
    SUP -->|spawn with env-injected secrets| A[Agent subprocess]
    A -->|read state via pgmcp| PG
    A -->|read+write memory via MCP| MP[(MemPalace)]
    A -->|finalize_ticket MCP| FIN[Atomic commit]
    FIN --> PG
    FIN --> MP
```

Components:

- **Postgres 17** — source of truth for tickets, departments, agent
  instances, the event outbox, and the M2.3 vault grant tables.
  Every state change that another part of the system reacts to fires
  `pg_notify` in the same transaction as the change itself.
- **Supervisor** — single Go binary. Holds a dedicated LISTEN
  connection (not from the pool), runs a fallback poll in case a
  notification is lost during a reconnect, enforces per-department
  concurrency caps, fetches per-spawn secrets from the vault and
  injects them as environment variables, spawns and reaps
  subprocesses, reconciles stale state on restart.
- **Infisical vault (M2.3)** — self-hosted secret store. The
  supervisor is the only Garrison component that talks to it; agents
  never see the vault MCP, and any agent MCP config that references
  the vault is rejected at spawn time. Secrets enter the agent only
  as environment variables.
- **MemPalace (M2.2)** — sidecar MCP server holding institutional
  memory across agent instances. Wings + halls structure;
  `mempalace_add_drawer` and `mempalace_kg_add` are the write
  primitives. Read+write access for agents is wing-scoped.
- **Agent subprocess** — `claude` CLI with a scoped agent.md, a
  working directory, vault-injected env vars, and MCP servers wired
  in: `pgmcp` (read-only Postgres), `mempalace` (memory), and
  `finalize_ticket` (the single transactional commit tool added in
  M2.2.1 — agents cannot transition a ticket any other way). M1's
  `sh -c` placeholder is preserved as `GARRISON_FAKE_AGENT_CMD` for
  fast tests.

Not yet shipped: the operator dashboard (M3, active), CEO chat
(M5), and hiring flow (M7). See [Milestones](#milestones).

For the full system picture (data model, event flow, dashboard
surfaces) see `ARCHITECTURE.md`. For the reasoning behind every
non-obvious choice, see `RATIONALE.md`.

---

## M1 + M2 in one paragraph each

**M1** is the event bus and supervisor core. Postgres schema with
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

**M2** is the first real agent loop, shipped across five sub-
milestones (M2.1 → M2.3, 2026-04-22 → 2026-04-24). M2.1 swapped the
fake agent for `claude` with stream-json parsing, an in-tree pgmcp
read-only Postgres MCP server, MCP-init health checks (a server with
`status != "connected"` kills the spawn), process-group kill
semantics, and per-spawn budget caps. M2.2 wired MemPalace as an MCP
sidecar with diary + KG triple write contracts and asynchronous
hygiene checks. M2.2.1 collapsed completion onto a single
`finalize_ticket` MCP tool — the only way an agent can transition a
ticket — bracketed by an atomic transaction across two MemPalace
writes and four Postgres writes. M2.2.2 added richer structured-error
responses (line/column/excerpt/constraint/expected/actual/hint), the
Adjudicate precedence fix (budget-cap events no longer masked as
`claude_error`), and calibrated seed agent.md prose. M2.3 introduced
the Infisical vault with a `vault.SecretValue` opaque type, a
`tools/vaultlog` go vet analyzer that prevents secrets from reaching
log calls at build time, four enforced rules (no-leaked-values,
zero-grants-zero-secrets, vault-opaque-to-agents, fail-closed-audit),
and Garrison's first PL/pgSQL trigger. Post-ship, a three-bug chain
in `pgmcp` that had been silently contaminating compliance signal
across three retros was found and fixed (`docs/forensics/pgmcp-
three-bug-chain.md`); the M2.2.x arc retro at
[`docs/retros/m2-2-x-compliance-retro.md`](./docs/retros/m2-2-x-compliance-retro.md)
documents the full recovery shape and is essential reading before
any M2-area work.

What M1 + M2 **do not** include: the operator dashboard (M3,
active), CEO chat (M5), the memory hygiene UI (M3/M6), hiring
(M7), agent-spawned tickets (M8). Two open follow-ups are tracked
under [`docs/issues/`](./docs/issues/): workspace sandboxing
(Docker-per-agent, planned post-M3) and the cost-telemetry blind
spot (supervisor signal-handling fix that lets `result` event land
before kill).

---

## Milestones

| Milestone | Scope | Status |
|---|---|---|
| **M1** | Event bus + supervisor core. Fake agent (`sh -c`). | Shipped 2026-04-22. |
| **M2.1** | `claude` CLI invocation. stream-json parsing, in-tree pgmcp, MCP-init health checks, process-group kill, per-spawn budget caps. | Shipped 2026-04-22. |
| **M2.2** | MemPalace MCP sidecar wired in. Diary + KG triple write contracts. Async hygiene checks. New `garrison_agent_mempalace` Postgres role. Three-container topology. | Shipped 2026-04-23. |
| **M2.2.1** | `finalize_ticket` MCP tool as the only commit path. Atomic transaction bracketing two MemPalace writes + four Postgres writes. | Shipped 2026-04-23. |
| **M2.2.2** | Richer structured errors + Adjudicate precedence fix + calibrated seed agent.md. Closed by the post-ship pgmcp three-bug-chain investigation; see arc retro. | Shipped 2026-04-24. |
| **M2.3** | Self-hosted Infisical vault. `SecretValue` opaque type. `vaultlog` go vet analyzer. Four vault rules. First PL/pgSQL trigger. | Shipped 2026-04-24. |
| **M3** | Next.js 16 dashboard, read-only. Kanban, ticket detail, agent activity feed. Surfaces hygiene status and the cost-telemetry blind spot. | **Active.** |
| **M4** | Dashboard mutations. Create/drag tickets, edit agent configs. | Not started. |
| **M5** | CEO chat, summoned per-message, read-only. | Not started. |
| **M6** | CEO ticket decomposition + memory hygiene dashboard + cost-based throttling. | Not started. |
| **M7** | Hiring flow via skills.sh + SkillHub (private skills registry). | Not started. |
| **M8** | Agent-spawned tickets, cross-department dependencies, MCP-server registry (MCPJungle leading candidate). | Not started. |

Each milestone ships end-to-end functional before the next begins.
No scaffolding for future milestones lands early. See
`ARCHITECTURE.md` §"Build plan — milestones" for the scope notes
per milestone.

---

## Repository layout

```
garrison/
├── brand/                          # logo SVGs (light + dark)
├── supervisor/                     # Go 1.25 binary. The only code that runs today.
│   ├── cmd/supervisor/             # main + migrate + `mcp postgres` + `mcp finalize` subcommands
│   ├── internal/
│   │   ├── claudeproto/            # M2.1: stream-json types + Router
│   │   ├── mcpconfig/              # M2.1 + M2.3: per-invocation MCP config + Rule 3 pre-check
│   │   ├── pgmcp/                  # M2.1: in-tree Postgres MCP server (post-M2.2.2 fixes
│   │   │                           #         for envelope shape + UUID encoding + grants)
│   │   ├── agents/                 # M2.1: startup-once cache
│   │   ├── spawn/                  # M2.1+: subprocess pipeline, finalize write, vault orchestration
│   │   ├── mempalace/              # M2.2: bootstrap + wake-up + Client + DockerExec seam
│   │   ├── hygiene/                # M2.2 + M2.2.1: Evaluator + listener + sweep
│   │   ├── finalize/               # M2.2.1 + M2.2.2: finalize_ticket MCP server + richer errors
│   │   ├── vault/                  # M2.3: SecretValue + Client + ScanAndRedact + audit row
│   │   ├── config/, store/, events/, pgdb/, recovery/, health/, concurrency/, testdb/
│   ├── tools/vaultlog/             # M2.3 custom go vet analyzer (rejects SecretValue logging)
│   ├── integration_test.go         # //go:build integration
│   ├── chaos_test.go               # //go:build chaos
│   ├── Dockerfile                  # alpine 3-stage build with claude CLI install (M2.1)
│   └── Makefile
├── migrations/                     # goose SQL migrations — SINGLE source of truth
│   └── seed/                       # engineer.md, qa-engineer.md (embedded via +embed-agent-md)
├── specs/                          # specify-cli output, one dir per milestone
│   ├── _context/                   # per-milestone binding constraints
│   ├── m1-event-bus/
│   ├── 003-m2-1-claude-invocation/
│   ├── 004-m2-2-mempalace/
│   ├── 005-m2-2-1-finalize-ticket/
│   ├── 006-m2-2-2-compliance-calibration/
│   └── 007-m2-3-infisical-vault/
├── docs/
│   ├── architecture-reconciliation-2026-04-24.md   # frozen decision-provenance snapshot
│   ├── mcp-registry-candidates.md                  # M8 input: MCPJungle commitment
│   ├── skill-registry-candidates.md                # M7 input: SkillHub commitment
│   ├── ops-checklist.md                            # post-migrate and post-deploy steps
│   ├── getting-started.md                          # clean-clone-to-running walkthrough
│   ├── architecture.md                             # short pointer into ARCHITECTURE.md
│   ├── research/                                   # spike outputs (m2-spike.md)
│   ├── security/                                   # vault-threat-model.md (M2.3 design input)
│   ├── forensics/pgmcp-three-bug-chain.md          # post-M2.2.2 root-cause investigation
│   ├── issues/                                     # tracked open issues (sandboxing, cost telemetry)
│   └── retros/                                     # m1, m1-retro-addendum, m2-1, m2-2,
│                                                   #   m2-2-1, m2-2-2, m2-2-x-compliance-retro, m2-3
├── examples/                       # toy company YAML, sample agent.md files
├── experiment-results/             # exploratory matrices (post-uuid-fix, etc.), not production
├── ARCHITECTURE.md                 # system structure, data model, build plan
├── RATIONALE.md                    # 13 design decisions with alternatives rejected
├── AGENTS.md                       # binding guidance for AI coding agents
├── LICENSE                         # AGPL-3.0-only (code)
├── LICENSE-DOCS                    # CC-BY-4.0 (specs and documentation)
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
export GARRISON_DATABASE_URL='postgres://postgres:postgres@localhost:5432/orgos?sslmode=disable'
./bin/supervisor --migrate

# 3. Insert a department.
docker exec pg-garrison psql -U postgres orgos -c \
  "INSERT INTO departments (slug, name, concurrency_cap) VALUES ('engineering', 'Engineering', 2);"

# 4. Run the supervisor with a placeholder agent command.
export GARRISON_FAKE_AGENT_CMD='sh -c "echo hello from $TICKET_ID; sleep 2"'
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
| `GARRISON_DATABASE_URL` | Postgres DSN. | *required* |
| `GARRISON_POLL_INTERVAL` | Fallback-poll cadence. | `2s` |
| `GARRISON_SUBPROCESS_TIMEOUT` | Per-subprocess hard timeout. | `60s` |
| `GARRISON_SHUTDOWN_GRACE` | SIGTERM-to-forced-exit budget. | `30s` |
| `GARRISON_HEALTH_PORT` | `/health` HTTP port. | `8080` |
| `GARRISON_FAKE_AGENT_CMD` | Placeholder subprocess command (M1 only). | *required* |

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

Garrison is built spec-first. Each milestone begins with a spec
under `specs/` and ends with a retro under `docs/retros/`.

**M1** — foundational event bus + supervisor core.
- [Spec](./specs/m1-event-bus/spec.md) · [Plan](./specs/m1-event-bus/plan.md) · [Tasks](./specs/m1-event-bus/tasks.md) · [Acceptance evidence](./specs/m1-event-bus/acceptance-evidence.md) · [Retro](./docs/retros/m1.md) · [Spike addendum](./docs/retros/m1-retro-addendum.md)

**M2 arc** (M2.1 → M2.3, 2026-04-22 → 2026-04-24) — first real agent loop, MemPalace, finalize tool, compliance calibration, vault.
- [M2.1 Claude invocation](./specs/003-m2-1-claude-invocation/) · [retro](./docs/retros/m2-1.md)
- [M2.2 MemPalace](./specs/004-m2-2-mempalace/) · [retro](./docs/retros/m2-2.md)
- [M2.2.1 finalize_ticket](./specs/005-m2-2-1-finalize-ticket/) · [retro](./docs/retros/m2-2-1.md)
- [M2.2.2 compliance calibration](./specs/006-m2-2-2-compliance-calibration/) · [retro](./docs/retros/m2-2-2.md)
- [M2.3 Infisical vault](./specs/007-m2-3-infisical-vault/) · [retro](./docs/retros/m2-3.md)
- **[M2.2.x arc retro](./docs/retros/m2-2-x-compliance-retro.md)** — synthesis that closed the M2.2.x compliance arc, including the three-bug pgmcp investigation that revised three milestones' interpretations against contaminated data. **Essential reading before any M2-area work.**

The M1 retro and the M2.2.x arc retro are the two best single
documents for understanding what actually gets built when this
process runs. Both are honest about what the specs got wrong.

---

## Further reading

- [`ARCHITECTURE.md`](./ARCHITECTURE.md) — components, data model,
  event flow, dashboard surfaces, build plan.
- [`RATIONALE.md`](./RATIONALE.md) — 13 decisions with alternatives
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
