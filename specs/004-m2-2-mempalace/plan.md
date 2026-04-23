# Implementation plan: M2.2 — MemPalace MCP wiring

**Branch**: `004-m2-2-mempalace` | **Date**: 2026-04-23 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/004-m2-2-mempalace/spec.md`
**Binding context**: [`specs/_context/m2.2-context.md`](../_context/m2.2-context.md), [`docs/research/m2-spike.md`](../../docs/research/m2-spike.md) Part 2, [`AGENTS.md`](../../AGENTS.md) §§"Activate before writing code → M2.2" + "Concurrency discipline" + "What agents should not do" + "Stack and dependency rules", [`RATIONALE.md`](../../RATIONALE.md) §§3, 5, 13, [`ARCHITECTURE.md`](../../ARCHITECTURE.md) "MemPalace write contract", "Agent.md template", [`docs/retros/m2-1.md`](../../docs/retros/m2-1.md), [`docs/retros/m1.md`](../../docs/retros/m1.md) + addendum, [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md). The M1 + M2.1 supervisor (`supervisor/`) is the foundation this plan extends; its code and commit log are prerequisite reading.

## Summary

Wire MemPalace into every agent spawn, validate cross-agent memory end-to-end through an engineer → qa-engineer ticket flow, and begin producing the `hygiene_status` data stream the M3 dashboard will consume. MemPalace runs as a dedicated sidecar container (`garrison-mempalace`) fronted by a locked-down `linuxserver/socket-proxy` (`garrison-docker-proxy`); the supervisor stays Go-only and drives MemPalace via `docker exec` through the proxy's filtered socket. The per-invocation MCP config gains a second entry whose `command` is `docker` (with `args` pointing at `python -m mempalace.mcp_server --palace /palace` inside the sidecar); the wake-up context runs via `docker exec garrison-mempalace mempalace wake-up` with a 2-second timeout; the hygiene checker spawns a short-lived `mempalace.mcp_server` per evaluation through the same docker path. Two new internal packages — `internal/mempalace` (bootstrap + wake-up + MCP-spec builder) and `internal/hygiene` (LISTEN + evaluator + periodic sweep + palace-query client) — land alongside M2.1 code; existing packages (`mcpconfig`, `config`, `agents`, `spawn`, `claudeproto`/pipeline, `store`, `cmd/supervisor`) gain focused extensions. One forward migration adds `agent_instances.wake_up_status`, creates the `garrison_agent_mempalace` role with SELECT-only grants, installs an `emit_ticket_transitioned` trigger, widens the engineering workflow JSONB, rewrites the engineer seed to execute the full MemPalace write contract, and seeds the new qa-engineer row. The M2.2 release image is unchanged in shape from M2.1's supervisor image except for adding the `docker-cli` binary; MemPalace runs from a new thin `Dockerfile.mempalace` pinning 3.3.2.

## Technical context

**Language/Version**: Go 1.25 (inherited from M1/M2.1).
**Primary dependencies**: inherited — `github.com/jackc/pgx/v5`, `github.com/jackc/pgx/v5/pgxpool`, `golang.org/x/sync/errgroup`, `log/slog`, `github.com/pressly/goose/v3`, `github.com/google/shlex` (test harnesses only), `github.com/stretchr/testify`, `github.com/testcontainers/testcontainers-go`. **No new Go dependencies.** `internal/mempalace` and `internal/hygiene` shell out to the `docker` CLI via `os/exec` (stdlib) and speak JSON-RPC 2.0 over stdio using `encoding/json` — the same pattern `internal/pgmcp` established in M2.1.
**Storage**: PostgreSQL 17+ (unchanged); MemPalace SQLite + ChromaDB (binary index + KG) on a dedicated volume inside the MemPalace container (no Postgres mirror per Clarify Q5).
**Testing**: stdlib `testing` + `testify` + `testcontainers-go`; build tags `integration` and `chaos` reused; `internal/mempalace` and `internal/hygiene` expose dockerexec seams for unit tests that bypass the docker daemon entirely; integration/chaos tests stand up the full three-container topology via testcontainers-go.
**Target platform**: Linux server (Hetzner + Coolify); single static Go binary for the supervisor (`CGO_ENABLED=0 go build`); Alpine 3.20 runtime with `docker-cli` added; MemPalace image is Python 3.11 on Alpine with `mempalace==3.3.2` pip-installed and hash-pinned; socket-proxy is `ghcr.io/linuxserver/socket-proxy:latest` (or a pinned digest) with `POST=1 EXEC=1 CONTAINERS=1` only.
**External binaries**: `claude` (2.1.117, unchanged from M2.1) in the supervisor image; `mempalace` (3.3.2) in the MemPalace image; `docker` client binary in the supervisor image.
**Project type**: CLI/daemon + sidecar services. The supervisor binary still has two entrypoints (`supervisor` daemon, `supervisor mcp-postgres`); M2.2 adds no new Go subcommands.
**Performance goals**: inherited M1/M2.1 NFRs plus M2.2 additions — $0.10 per-invocation budget cap (NFR-201); wake-up timeout 2 s (NFR-202); hygiene delay default 5 s (NFR-203); hygiene sweep cadence default 60 s (NFR-204); MemPalace MCP health-bail latency 2 s (NFR-206, shared with M2.1's NFR-106 for postgres).
**Constraints**: locked dependency list preserved; no Node/Python in the supervisor binary or image (MemPalace's Python stays isolated in the sidecar); process-group termination for every Claude subprocess (AGENTS.md rule 7); pipeline-drain-before-Wait (AGENTS.md rule 8); terminal writes under `context.WithoutCancel` (AGENTS.md rule 6); every goroutine threads `ctx`; hygiene checker single-goroutine + periodic sweep, no concurrent evaluation (NFR-205); supervisor cannot run `mempalace init` against a git-tracked directory (spike §3.2, AGENTS.md).
**Scale/scope**: single operator; engineering department with cap=1; engineer + qa-engineer roles; sequential two-agent ticket flow; per-invocation cost ≤ $0.10 target; tens of invocations per week during M2.2 validation.

## Constitution check

*Gate: must pass before planning proceeds. Re-checked before `/speckit.tasks`.*

| Principle | Compliance |
|-----------|------------|
| I. Postgres is sole source of truth; pg_notify is the bus | Pass — MemPalace palace state is *memory*, not current state (per Principle II). No Postgres-side mirror of palace metadata (Clarify Q5). New trigger `emit_ticket_transitioned` emits inside the same transaction as the `ticket_transitions` INSERT. |
| II. MemPalace is sole memory store | **Activated by this milestone.** Every agent spawn now sees MemPalace as an MCP tool; the completion protocol writes diaries + KG triples to wings. No alternative memory layer introduced. |
| III. Agents are ephemeral | Pass — every Claude invocation is a fresh subprocess; MemPalace MCP server is spawned per-invocation (via `docker exec`, not a long-running daemon); wake-up subprocess is short-lived; hygiene checker's palace queries spawn a fresh `mempalace.mcp_server` per evaluation. |
| IV. Soft gates on memory hygiene | Pass — `hygiene_status` is written *after* the transition, never blocking it. At-most-once-to-terminal discipline (FR-215). Palace-unreachable → `'pending'`, recovered by sweep; never fails the transition. |
| V. Skills from skills.sh | N/A — M7. Engineer + qa-engineer seed rows have `skills=[]`. |
| VI. Hiring is UI-driven | N/A — M7. Both roles seeded via migration. |
| VII. Go supervisor with locked deps | Pass — no new Go dependencies. `internal/mempalace` and `internal/hygiene` use only stdlib + pgx/v5. MemPalace's Python stays in its sidecar container; the supervisor image gains the `docker-cli` binary (already in Alpine repos, not a Go dep). |
| VIII. Every goroutine accepts context | Pass — hygiene LISTEN goroutine, sweep goroutine, wake-up subprocess, docker-exec'd palace queries, and the existing pipeline/stderr goroutines all thread `ctx`; shutdown uses `context.WithoutCancel + TerminalWriteGrace`. |
| IX. Narrow specs per milestone | Pass — scope limited to M2.2's spec; no dashboard (M3), no secrets vault (M2.3), no CEO (M5), no hiring (M7). |
| X. Per-department concurrency caps | Pass — engineering cap remains 1. Two-agent flow is sequential (engineer finishes before qa-engineer spawns). |
| XI. Self-hosted on Hetzner | Pass — Compose topology suits Coolify per-project deployment. No cloud services introduced. |

No violations → Complexity Tracking intentionally empty.

## Project structure

### Documentation (this feature)

```text
specs/004-m2-2-mempalace/
├── spec.md                 # locked after clarify
├── plan.md                 # this file
├── research.md             # populated by T001 validation-spike outputs
├── checklists/             # populated by /speckit.checklist if needed
└── tasks.md                # produced later by /garrison-tasks
```

Research, data-model, contracts, and quickstart artefacts that spec-kit's generic template splits into separate files are inlined into this plan's sections. M2.2's scope fits cleanly in one document plus an operator-friendly `research.md` summarising the T001 spike outputs; adding more files adds indirection without benefit.

### Source code (repository root) — M2.2 delta

```text
garrison/
├── supervisor/
│   ├── cmd/supervisor/
│   │   ├── main.go                      # extends errgroup wiring + two handler registrations
│   │   ├── migrate.go                   # unchanged
│   │   ├── signals.go                   # unchanged
│   │   ├── version.go                   # unchanged
│   │   └── mcp_postgres.go              # unchanged
│   ├── internal/
│   │   ├── mempalace/                   # NEW
│   │   │   ├── bootstrap.go             # unconditional mempalace init --yes through docker exec (T001 finding F1)
│   │   │   ├── wakeup.go                # 2-s wake-up client, returns (stdout, status, elapsed)
│   │   │   ├── mcpspec.go               # builds the mcpconfig mempalace entry
│   │   │   ├── dockerexec.go            # dockerExec seam for unit test injection
│   │   │   └── *_test.go
│   │   ├── hygiene/                     # NEW
│   │   │   ├── listener.go              # LISTEN on work.ticket.transitioned.*.*, dedicated pgx.Conn
│   │   │   ├── sweep.go                 # periodic sweep of pending/NULL rows
│   │   │   ├── evaluator.go             # pure rule logic over (run window, drawers, triples)
│   │   │   ├── palace.go                # JSON-RPC client to mempalace.mcp_server over docker exec
│   │   │   └── *_test.go
│   │   ├── mcpconfig/mcpconfig.go       # EXTENDED — accepts palace-path + container name params
│   │   ├── config/config.go             # EXTENDED — palace/container/proxy env vars
│   │   ├── agents/agents.go             # EXTENDED — expose palace_wing (already loaded in M2.1)
│   │   ├── spawn/
│   │   │   ├── spawn.go                 # EXTENDED — wake-up step, role-slug param, wake_up_status write
│   │   │   ├── pipeline.go              # EXTENDED — tool_use/tool_result structured logging
│   │   │   ├── exitreason.go            # EXTENDED — ExitBudgetExceeded constant
│   │   │   └── mockclaude/
│   │   │       ├── main.go              # EXTENDED — new directives
│   │   │       └── scripts/             # new ndjson fixtures for M2.2 flows
│   │   ├── claudeproto/                 # EXTENDED — AssistantEvent carries tool_uses; UserEvent tool_results already exist
│   │   ├── testdb/                      # EXTENDED — SetAgentMempalacePassword + M2.2 seed helpers
│   │   └── store/                       # REGENERATED from new sqlc queries
│   ├── Dockerfile                       # EXTENDED — adds `apk add --no-cache docker-cli`
│   ├── Dockerfile.mempalace             # NEW — python:3.11-slim + pip install -r requirements-lock.txt (T001 findings F3, F4)
│   ├── requirements-lock.txt            # NEW — pip-compile'd full-tree lockfile for mempalace 3.3.2 + transitives
│   ├── docker-compose.yml               # NEW — three-container topology (+ postgres)
│   ├── Makefile                         # extended — seed-qa-engineer-agent target, compose helpers
│   └── ...
├── migrations/
│   ├── 20260421000001_initial_schema.sql              # unchanged
│   ├── 20260421000002_event_trigger.sql               # unchanged
│   ├── 20260422000003_m2_1_claude_invocation.sql      # unchanged
│   ├── 20260423000004_m2_2_mempalace.sql              # NEW — see §Migration
│   ├── queries/
│   │   ├── agent_instances.sql          # extended — UpdateInstanceTerminalWithCostAndWakeup
│   │   ├── ticket_transitions.sql       # NEW — UpdateTicketTransitionHygiene, ListStuckHygieneTransitions, GetAgentInstanceRunWindow
│   │   └── agents.sql                   # extended — GetAgentByID for hygiene checker
│   └── seed/
│       ├── engineer.md                  # REWRITTEN — full MemPalace write contract
│       └── qa-engineer.md               # NEW — parallel QA protocol
└── docs/
    ├── ops-checklist.md                 # extended — M2.2 post-migrate step + compose topology notes
    └── retros/
        └── m2-2.md                      # written at ship time (pointer to MemPalace wing_company/hall_events per AGENTS.md §Retros)
```

**Structure decision**: extend, do not rewrite. M1's orchestration and M2.1's Claude-invocation pipeline are reused as-is. Two new packages land alongside existing ones; targeted edits to existing packages add the M2.2 surface without reshaping anything M2.1 shipped. The supervisor remains a single static binary with two entrypoints.

## Decisions baked into this plan (from /speckit.specify, /speckit.clarify, and operator sign-off)

### Binding questions from `m2.2-context.md`, committed in the spec

| Question | Commit |
|---|---|
| Palace path default | `/palace` *inside the MemPalace container*; override via `GARRISON_PALACE_PATH`. The spec's `~/.garrison/palace/` wording (NFR-208) describes supervisor-hosted deployment; under the sidecar topology the path is container-internal (see §"Spec-level findings"). |
| MemPalace MCP invocation | `python -m mempalace.mcp_server --palace /palace` inside the sidecar, exec'd via `docker exec -i garrison-mempalace …`. **Spec Clarify Q1's `mempalace mcp` is factually wrong for 3.3.2** (see §"Spec-level findings"); the plan proceeds under the corrected invocation. |
| Wake-up flag shape | `mempalace --palace <path> wake-up --wing <wing>` (T001 finding F2 — no `--max-tokens` flag exists in 3.3.2; output implicitly bounded by L0+L1 renderer). |
| Wake-up failure policy | Non-blocking; log warn; `wake_up_status='failed'`; spawn proceeds without a wake-up block. |
| Hygiene delay | 5 s default; `GARRISON_HYGIENE_DELAY` override; reject ≤ 0. |
| Hygiene concurrency | Single goroutine + single sweep goroutine; sequential processing. |
| QA engineer trigger | `work.ticket.transitioned.engineering.in_dev.qa_review`. |
| Per-invocation budget | $0.10 via `--max-budget-usd 0.10`. |
| `garrison_agent_mempalace` grants | `SELECT` only on `ticket_transitions`, `agent_instances`, `tickets` (no palace-metadata tables — Clarify Q5). |
| Seed `agent.md` content | Committed at `migrations/seed/engineer.md` and `migrations/seed/qa-engineer.md`; bodies specified in §"Seed content" below. |

### Clarify-session answers (2026-04-22), committed in the spec

| Clarify question | Commit |
|---|---|
| MemPalace MCP stdio invocation | `mempalace mcp` per Clarify Q1 → **correction**: `python -m mempalace.mcp_server --palace <path>` per T001 validation-spike confirmation of 3.3.2. |
| Version pin | 3.3.2 exactly; no "or newer" hedge. |
| KG triple shape | Triples keyed on `ticket_<id>`; hygiene checker queries via `mempalace_kg_query(entity='ticket_<id>')`; direction-agnostic. |
| Palace bootstrap strategy | Unconditional `mempalace init --yes /palace` at supervisor startup (T001 finding F1 — `init --yes` is idempotent on 3.3.2; the `chroma.sqlite3` marker assumption was wrong). |
| Postgres palace-metadata tables | None. SELECT-only role on existing M2.1 tables. |

### Plan-phase decisions (operator slate approval, 2026-04-23)

| Category | Decision |
|---|---|
| Deployment topology | Three containers: `garrison-supervisor`, `garrison-mempalace`, `garrison-docker-proxy` (linuxserver/socket-proxy). Compose-delivered. |
| Docker transport | Supervisor calls `docker exec` through the proxy over the compose network; `DOCKER_HOST=tcp://garrison-docker-proxy:2375` (T001 finding F5 — linuxserver/socket-proxy is a TCP :2375 HAProxy, not a unix-socket proxy). |
| Proxy permissions | `POST=1 EXEC=1 CONTAINERS=1`, default-deny on everything else. |
| Validation spike before implementation | Yes (T001) — verify MCP init-event reports `mempalace.status=connected`, kill-chain tears down cleanly, wake-up fits under 2 s. |
| Engineer listens_for | `["work.ticket.created.engineering.in_dev"]` (tickets inserted directly at `in_dev` for acceptance per FR-228's "scope simplicity" default). |
| `spawn.Spawn` role parameterization | Handler closure passes `roleSlug string` into `Spawn`; the hardcoded `"engineer"` string in M2.1's `runRealClaude` becomes the parameter. Fake-agent path untouched. |
| Hygiene palace-query model | Per-evaluation short-lived `docker exec -i` spawning `mempalace.mcp_server`; teardown on EOF. No long-running daemon in M2.2. |
| Testing layering | Unit tests mock the `dockerexec` seam. Integration/chaos stand up the full three-container topology via testcontainers-go. |
| Supervisor Dockerfile delta | Add `apk add --no-cache docker-cli` only. No Python runtime. |

## Deployment topology (binding)

This is the largest shape change from what the spec's FRs assume. The functional behaviour FR-203/204/207/213/NFR-208/NFR-213 specify is preserved; only the *implementation mechanism* changes.

```
  ┌─────────────────────────────────────────────────────────────┐
  │                    Coolify project network                   │
  │                                                              │
  │  ┌────────────────────┐    ┌────────────────────────────┐   │
  │  │  garrison-         │    │  garrison-docker-proxy     │   │
  │  │  supervisor        │    │  (linuxserver/socket-proxy)│   │
  │  │                    │    │  POST=1 EXEC=1             │   │
  │  │  - Go binary       │    │  CONTAINERS=1              │   │
  │  │  - docker-cli      │    │  (default-deny else)       │   │
  │  │  - claude 2.1.117  │    └───────────┬────────────────┘   │
  │  │                    │                │                    │
  │  │  DOCKER_HOST=      │                │ TCP :2375 over     │
  │  │    tcp://garrison- │                │ compose network    │
  │  │    docker-proxy:   │────────────────┤ (HAProxy filters   │
  │  │    2375            │                │  per endpoint)     │
  │  │                    │                │                    │
  │  └──┬───┬─────────────┘                │                    │
  │     │   │                              │ /var/run/          │
  │     │   │                              │ docker.sock        │
  │     │   │                              │ (read-only mount)  │
  │     │   │                              ▼                    │
  │     │   │                        ┌──────────────┐           │
  │     │   │   docker exec via      │  host docker │           │
  │     │   │   proxy (EXEC+POST)    │  daemon      │           │
  │     │   │─────────────────────▶  └──────┬───────┘           │
  │     │   │                               │                   │
  │     │   │                               ▼                   │
  │     │   │   ┌─────────────────────────────────────────┐     │
  │     │   │   │  garrison-mempalace                     │     │
  │     │   │   │  - python3.11, mempalace 3.3.2          │     │
  │     │   │   │  - entrypoint: sleep infinity           │     │
  │     │   │   │  - volume: /palace (ChromaDB + SQLite)  │     │
  │     │   │   │                                         │     │
  │     │   │   │  on demand via docker exec:             │     │
  │     │   │   │    - mempalace init --yes /palace       │     │
  │     │   │   │    - mempalace wake-up --wing … --max…  │     │
  │     │   │   │    - python -m mempalace.mcp_server     │     │
  │     │   │   │        --palace /palace                 │     │
  │     │   │   └─────────────────────────────────────────┘     │
  │     │   │                                                   │
  │     │   └─── Postgres (existing; M1/M2.1)                   │
  │     └─────── Claude subprocesses (spawned inside supervisor │
  │              container; inherit DOCKER_HOST from env)        │
  └─────────────────────────────────────────────────────────────┘
```

**Key invariants:**

1. **Supervisor container never mounts `/var/run/docker.sock` directly.** It only mounts the proxy's filtered socket.
2. **Claude subprocesses run inside the supervisor container**, inherit `DOCKER_HOST` from the supervisor's env, and use the `docker` CLI (already in the image) as their MCP config's `command` for the `mempalace` entry. Claude does not talk to the docker daemon directly; it calls `docker exec` which goes through the proxy.
3. **MemPalace container is idle by default** (`sleep infinity` entrypoint). All real work is per-invocation `docker exec`. This matches RATIONALE §3's ephemeral-agent principle — the MCP server itself is ephemeral, spawned per-Claude-invocation and per-hygiene-evaluation.
4. **Palace volume is a Docker-named volume** mounted at `/palace` in the MemPalace container. Not bind-mounted; not shared with the supervisor. The supervisor interacts with palace state only through `docker exec` (init marker check, wake-up, MCP queries). The palace is outside any git-tracked directory by topology (FR-202, spike §3.2).
5. **Socket-proxy is a pinned digest**, not `:latest`. The operator sets the digest in compose; ops checklist documents the upgrade cadence.

**Residual security envelope (documented in `docs/security/vault-threat-model.md` under "M2.2 deployment assumptions"):** a compromised supervisor can `docker exec` into any container on the *same Docker engine*. For per-Coolify-project deployment, that's garrison-supervisor, garrison-mempalace, garrison-docker-proxy, garrison-postgres. The worst case (exec into Postgres and dump) is already reachable via `GARRISON_DATABASE_URL` in supervisor env, so the proxy does not widen the existing blast radius. For shared-engine deployment, the envelope widens to other projects' containers; operator concern, flagged in ops checklist.

## Pre-implementation validation spike (T001)

15–30 minutes in `~/scratch/m2-2-topology-spike/`. The first task in `/garrison-tasks` output and a hard gate on every downstream task. Findings land in `specs/004-m2-2-mempalace/research.md`.

**Goal**: prove three kill-chain claims the plan depends on. Failure of any claim blocks implementation and returns the milestone to clarify.

**Setup**: docker-compose stack from the plan's `docker-compose.yml`, plus a throwaway Claude config pointing at a mempalace entry as specified in §"MCP config extension".

**Claims under test**:

1. **MCP init reports `connected`.** Spawn `claude -p "say hi" --output-format stream-json --verbose --strict-mcp-config --mcp-config <file> --no-session-persistence --model claude-haiku-4-5-20251001 --max-budget-usd 0.02` where `<file>` contains a `mempalace` entry with `command: docker`, `args: [exec, -i, garrison-mempalace, python, -m, mempalace.mcp_server, --palace, /palace]`, `env: {DOCKER_HOST: <proxy sock>}`. Parse the first stream-json event; confirm `mcp_servers[]` has an entry with `name='mempalace'` and `status='connected'`. Confirm all 29 spike §3.6 tools appear in `tools[]` with `mcp__mempalace__*` prefix.
2. **Kill-chain tears down cleanly.** Start the above subprocess, let it reach the init event, then SIGTERM Claude's process group (`kill -TERM -$pgid`). Verify: Claude exits within 2 s; the `docker exec` subprocess exits; `docker ps --format '{{.Names}}:{{.Status}}'` shows `garrison-mempalace` still healthy (container not killed); no lingering `mempalace.mcp_server` Python processes remain inside the mempalace container (`docker exec garrison-mempalace pgrep -f mempalace.mcp_server` returns empty).
3. **Wake-up fits under 2 s.** Run `time docker exec garrison-mempalace mempalace --palace /palace wake-up --wing test_wing` against a bootstrapped palace 10 times; record p50/p95/p99. All values must be < 1.5 s (leaves 500 ms margin inside the 2 s NFR-202 ceiling). **T001 measured**: p50=925ms, p95=1031ms. PASS.

**Pass criteria**: all three claims hold. On pass: record the measured latencies and the verbatim `mcp_servers` entry in `research.md`; proceed to `/garrison-tasks`.

**Fail handling**: if any claim fails, halt the milestone. Possibilities and responses:
- Claim 1 fails (init reports `failed` or a different status): re-check the exec command, permissions, and whether `mempalace.mcp_server` really reads from the inherited stdin. If it's genuinely an MCP-config-shape issue, the plan falls back to shape 2a (authored HTTP wrapper + supervisor `mcp-mempalace` subcommand).
- Claim 2 fails (lingering processes or unresponsive shutdown): investigate whether `docker exec -i` needs an explicit `--sig-proxy` or similar. If the stdin-EOF → python-exit chain is broken, the plan adds an explicit `pkill mempalace.mcp_server` escalation inside the existing `killProcessGroup` helper.
- Claim 3 fails (wake-up > 2 s): either raise NFR-202 (operator decision in spec amendment) or increase the wake-up timeout env var default. Plan does not bake a workaround without operator sign-off.

## Changes to existing M1/M2.1 packages

### `internal/config`

Adds fields and env-var loaders for M2.2:

- `MempalaceContainer string` — `GARRISON_MEMPALACE_CONTAINER`, default `"garrison-mempalace"`. Validator rejects empty strings and whitespace-only values.
- `PalacePath string` — `GARRISON_PALACE_PATH`, default `"/palace"`. This is the path *inside* the MemPalace container where the volume is mounted; the supervisor passes it as the `--palace` argument to every MemPalace invocation.
- `DockerBin string` — `GARRISON_DOCKER_BIN`; if empty, `exec.LookPath("docker")` at startup. Fail-fast error: `"config: cannot find docker binary on $PATH and GARRISON_DOCKER_BIN is unset"`. Skipped under `UseFakeAgent`.
- `AgentMempalacePassword string` — `GARRISON_AGENT_MEMPALACE_PASSWORD`, required under `!UseFakeAgent`. Stored in an unexported field; composed into `AgentMempalaceDSN()`.
- `HygieneDelay time.Duration` — `GARRISON_HYGIENE_DELAY`, default `5 * time.Second`. Validator rejects `<= 0`.
- `HygieneSweepInterval time.Duration` — `GARRISON_HYGIENE_SWEEP_INTERVAL`, default `60 * time.Second`. Validator rejects `<= 0`.
- `ClaudeBudgetUSD` — existing field; **default bumped from 0.05 to 0.10** per NFR-201. The validator's `< 1.0` upper bound remains.
- `AgentMempalaceDSN() string` — derived method, parallel to `AgentRODSN()`. Composes the M1 `DatabaseURL` with `garrison_agent_mempalace` + the unexported password. Used only by the hygiene checker's dedicated `pgx.Conn`.

**Removed proposal from slate**: `GARRISON_MEMPALACE_BIN` is not introduced (not a local binary under shape 2b). `exec.LookPath("mempalace")` never runs on the supervisor host.

Validation order in `Load`: palace path + container name + hygiene durations parsed first, then `docker` binary resolution, then agent-mempalace password. Fake-agent escape hatch (M2.1) continues to suppress all `!UseFakeAgent` checks.

### `internal/spawn`

`runRealClaude` gains a wake-up step and a role-slug parameter. New flow inserted between current step 3 (agent resolve) and step 4 (mcpconfig.Write):

- **Step 3a: Wake-up context capture.** Call `internal/mempalace.Wakeup(ctx, cfg, agent.PalaceWing)` with `context.WithTimeout(ctx, 2*time.Second)`. Returns `(stdout string, status WakeUpStatus, elapsed time.Duration, err error)`. On `WakeUpOK`, the stdout is spliced into `--system-prompt` per FR-207a's template. On `WakeUpFailed`, log warn with fields `palace_wing`, `elapsed_ms`, `error`; compose `--system-prompt` without the wake-up block. The `status` flows through to the terminal write.

- **Step 5 (argv): `--max-budget-usd`** uses `deps.ClaudeBudgetUSD` (default 0.10 per config change above). `--system-prompt` is the composed string from step 3a.

`Spawn` function signature extends: `func Spawn(ctx context.Context, deps Deps, eventID pgtype.UUID, roleSlug string) error`. The M1 fake-agent path ignores `roleSlug`. `runRealClaude` calls `deps.AgentsCache.GetForDepartmentAndRole(ctx, dept.ID, roleSlug)` instead of the hardcoded `"engineer"`. `cmd/supervisor/main.go` closures pass the literal `"engineer"` or `"qa-engineer"` depending on which channel the handler was registered for.

`writeTerminalCost` widens to `writeTerminalCostAndWakeup(ctx, deps, instanceID, eventID, ticketID, status, exitReason, cost, wakeUpStatus, insertTransition, ticketFromColumn, ticketToColumn)`. The engineer path uses `fromColumn='in_dev', toColumn='qa_review'`; the qa-engineer path uses `fromColumn='qa_review', toColumn='done'`. The M1 fake-agent path continues to call `writeTerminal` (no cost, no wake-up status column). Rationale for not unifying: M1 tests are byte-identical sensitive; we don't destabilise them for aesthetic uniformity.

Init-event health check: **no change to `internal/claudeproto.CheckMCPHealth`.** The existing function iterates `mcp_servers[]` and bails on the first non-`"connected"` entry — it has always been array-size agnostic, confirmed by M2.1 retro §"Ready to start M2.2". Adding the mempalace entry to the array happens in `internal/mcpconfig`; the health-check path handles it automatically.

MCP-bail `exit_reason`: M2.1's `FormatMCPFailure(name, status)` produces `mcp_<name>_<status>`; calling it with `name="mempalace"` produces the exact strings FR-208/FR-210 and the chaos tests expect. No new formatter needed.

New `exit_reason` constant in `internal/spawn/exitreason.go`:
- `ExitBudgetExceeded = "budget_exceeded"` — mapped from the terminal `result` event when `terminal_reason` indicates budget overrun. Precise `terminal_reason` string matched case-insensitively against a small set (`budget_exceeded`, `max_budget_usd_exceeded`, or similar — exact enum resolved from Claude 2.1.117 behaviour at implementation time; T001 spike does not cover budget overruns, so the adjudication path uses a prefix match as a safety net).

Adjudicate table gets one new row, inserted above `case !helloTxtOK`:
```
case result.ResultSeen && strings.Contains(strings.ToLower(result.TerminalReason), "budget"):
    return "failed", ExitBudgetExceeded
```
`total_cost_usd` is still captured from the result event in this case (the budget-exceeded result carries the cost).

### `internal/claudeproto` and `internal/spawn/pipeline`

M2.2 needs structured logging of every `mempalace_*` `tool_use` + matching `tool_result` (FR-218, NFR-210). The existing `AssistantEvent` and `UserEvent` types are extended without reshaping:

- `AssistantEvent` gains `ToolUses []ToolUseBlock` where `ToolUseBlock = struct { Name, ToolUseID string; InputRaw json.RawMessage }`. M2.1 already exposed `ContentTypes []string`; this adds the detail layer for the subset of content blocks that are `tool_use`. Parsing is best-effort — if the shape drifts, `ToolUses` is nil and the supervisor logs via the existing Raw field; no parse error.
- `UserEvent` already has `ToolResults []ToolResult` (M2.1). M2.2 does not change its shape.

`pipelineRouter` extends its `OnAssistant` and `OnUser` handlers:

- `OnAssistant`: for each `ToolUseBlock` whose `Name` starts with `mempalace_` or `mcp__mempalace__`, emit an `info`-level slog line with fields `ticket_id`, `agent_instance_id`, `tool_name`, `tool_use_id`, `outcome="pending"`. Store the `tool_use_id → tool_name` mapping in a per-run map (bounded; each invocation sees ≪ 100 tool uses).
- `OnUser`: for each `ToolResult` whose `ToolUseID` is in the map, emit a follow-up `info`-level slog line with the same `tool_use_id`, the original `tool_name`, `outcome=<"ok"|"error">`, and (if error) `error_detail`. Remove from the map.

At EOF, if the map is non-empty (tool_use without tool_result), leave the `outcome="pending"` lines as-is — FR-218's "observational only" discipline applies; no dispatch consequence.

FR-218a guarantee: these slog lines are produced via `pipelineRouter`, which has no dispatch authority. Adjudicate does not read the map. The terminal transaction does not depend on tool-use observation.

### `internal/mcpconfig`

`Write` signature extends to accept the palace-path and container parameters (plus the docker host env):

```go
func Write(ctx context.Context, dir string, instanceID pgtype.UUID,
    supervisorBin, agentRODSN, dockerBin, mempalaceContainer, palacePath, dockerHost string,
) (path string, err error)
```

The emitted `mcpServers` object gains one key:

```json
"mempalace": {
  "command": "<dockerBin, e.g. /usr/bin/docker>",
  "args": ["exec", "-i", "<mempalaceContainer>", "python", "-m", "mempalace.mcp_server", "--palace", "<palacePath>"],
  "env": {"DOCKER_HOST": "<dockerHost>"}
}
```

The `postgres` key is unchanged from M2.1. Per FR-205, the addition is purely additive.

Unit-testable: the existing `fileOps` seam is preserved. New `TestWritePostgresPlusMempalace` asserts both entries present and the mempalace entry's command/args/env shape.

### `internal/agents`

M2.1 already loads `palace_wing` into `Agent.PalaceWing *string`. M2.2 adds no fields; it just stops being unused. The `GetForDepartmentAndRole` lookup key is unchanged.

New test: `internal/agents/cache_test.go::TestPalaceWingExposed` asserts that after loading a seeded engineer row with `palace_wing='wing_frontend_engineer'`, `cache.GetForDepartmentAndRole(ctx, deptID, "engineer").PalaceWing` returns `"wing_frontend_engineer"` (dereferenced).

FR-226a: hot-reload remains deferred. The M2.2 retro re-notes this.

### `internal/events`

No code change. `cmd/supervisor/main.go` registers two channels (see §wire-up below). The M1 dispatcher accepts any channel-string-keyed handler map.

### `internal/store`

Regenerated from new SQL. New queries:

- `agent_instances.sql`:
  - `UpdateInstanceTerminalWithCostAndWakeup`:
    ```sql
    UPDATE agent_instances
    SET status = $2, finished_at = NOW(), exit_reason = $3,
        total_cost_usd = $4, wake_up_status = $5
    WHERE id = $1;
    ```
- `ticket_transitions.sql` (new file):
  - `UpdateTicketTransitionHygiene`:
    ```sql
    UPDATE ticket_transitions
    SET hygiene_status = $2
    WHERE id = $1
      AND (hygiene_status IS NULL OR hygiene_status = 'pending');
    ```
    At-most-once-to-terminal via the WHERE clause (FR-215).
  - `ListStuckHygieneTransitions`:
    ```sql
    SELECT tt.id, tt.ticket_id, tt.triggered_by_agent_instance_id
    FROM ticket_transitions tt
    WHERE (tt.hygiene_status IS NULL OR tt.hygiene_status = 'pending')
      AND tt.at < NOW() - $1::interval
    ORDER BY tt.at ASC
    LIMIT $2;
    ```
    `$1` is the delay interval (default 5 s); `$2` is the batch cap (default 100). Used by the sweep (FR-216).
  - `GetAgentInstanceRunWindow`:
    ```sql
    SELECT ai.id, ai.started_at, ai.finished_at, ai.department_id,
           a.palace_wing, a.role_slug
    FROM agent_instances ai
    JOIN agents a
      ON a.department_id = ai.department_id
     AND a.role_slug = (
       SELECT role_slug FROM agents WHERE id = ai.agent_id LIMIT 1
     )  -- see note
    WHERE ai.id = $1;
    ```
    Note: M2.1's `agent_instances` table has `department_id` but not `agent_id`. The join resolves `palace_wing` by `department_id + role_slug`. The role_slug is needed from *somewhere*. Two options: (a) add `agent_instances.role_slug TEXT NOT NULL` in the M2.2 migration (back-filled to `'engineer'` for existing rows, supervisor populates going forward); (b) resolve via the `ticket_transitions.triggered_by_agent_instance_id → department_id` chain plus a convention that only one active agent per (dept, role) exists (which is the M2.2 invariant anyway). Decision: **option (a) — add `role_slug` to `agent_instances`**. It's one column, non-NULL with default `'engineer'`, and it makes the hygiene query unambiguous. `InsertRunningInstance` is extended to accept a `role_slug` parameter.
- `agents.sql`:
  - `GetAgentByID` — used defensively by the hygiene checker if needed; primary path uses the run-window join.

Terminal-transaction helper widens to call `UpdateInstanceTerminalWithCostAndWakeup` + (optionally) `InsertTicketTransition` + (optionally) `UpdateTicketColumnSlug` + `MarkEventProcessed`. Non-success paths skip the ticket-related statements; `wake_up_status` is always written (may be `'ok'`, `'failed'`, or NULL if the spawn failed before wake-up ran).

### `internal/testdb`

Adds:
- `SeedM22(ctx, pool) (engineerAgentID, qaEngineerAgentID pgtype.UUID, err error)` — seeds the engineering department's extended workflow, the engineer row (UPDATE from M2.1) and the qa-engineer row (INSERT).
- `SetAgentMempalacePassword(t, password string)` — runs `ALTER ROLE garrison_agent_mempalace PASSWORD '<password>'` against the test DB, parallel to the M2.1 `SetAgentROPassword` helper.
- Extends the per-test `TRUNCATE` cascade to include any new M2.2 seeds.

### `cmd/supervisor/main.go`

Wiring changes:

1. Load extended `config.Config`; validate `DockerBin`, `MempalaceContainer`, hygiene durations, and `AgentMempalacePassword`.
2. Inject `DOCKER_HOST` into the supervisor's env at startup so every child subprocess (Claude, docker-exec'd mempalace) inherits it.
3. **New**: run `internal/mempalace.Bootstrap(ctx, cfg)` after advisory-lock acquisition and before `agents.NewCache`. Bootstrap unconditionally runs `mempalace init --yes /palace` (idempotent on 3.3.2 per T001 finding F1). On success, log `palace_initialized=true`; on failure, log the path and underlying error and return `ExitFailure`.
4. Construct `agents.Cache` — unchanged.
5. Register two handlers:
   - `work.ticket.created.engineering.in_dev` → `func(ctx, eventID) error { return spawn.Spawn(ctx, spawnDeps, eventID, "engineer") }`.
   - `work.ticket.transitioned.engineering.in_dev.qa_review` → `func(ctx, eventID) error { return spawn.Spawn(ctx, spawnDeps, eventID, "qa-engineer") }`.
6. **New**: add two goroutines to the errgroup:
   - `hygiene.RunListener(gctx, hygiene.Deps{...})` — the LISTEN goroutine.
   - `hygiene.RunSweep(gctx, hygiene.Deps{...})` — the periodic sweep goroutine.
   `hygiene.Deps` carries: the hygiene-role DSN + dialer, the Queries, the logger, `cfg.HygieneDelay`, `cfg.HygieneSweepInterval`, a `palace.Client` (constructed from `cfg.DockerBin`/`MempalaceContainer`/`PalacePath`), and `TerminalWriteGrace`.
7. Unchanged: errgroup, signal handler, exit codes, health server, advisory lock, recovery query.

## New packages

### `internal/mempalace`

Owns palace bootstrap, wake-up invocation, and MCP-server-spec construction. Pure I/O wrappers around `os/exec`, with a `dockerExec` seam for unit tests.

Public surface:

```go
// Status enumerates wake_up_status column values.
type Status string
const (
    StatusOK      Status = "ok"
    StatusFailed  Status = "failed"
    StatusSkipped Status = "skipped" // reserved; M2.2 never writes it
)

// Bootstrap runs `mempalace init --yes /palace` unconditionally through
// docker exec. Idempotent in MemPalace 3.3.2 per T001 finding F1.
func Bootstrap(ctx context.Context, cfg BootstrapConfig) error

type BootstrapConfig struct {
    DockerBin          string
    MempalaceContainer string
    PalacePath         string
    Logger             *slog.Logger
    InitTimeout        time.Duration // default 30s
}

// Wakeup runs `mempalace --palace <path> wake-up --wing <wing>` through
// docker exec, with a context-derived 2s ceiling. Non-blocking on failure.
// Flag shape per T001 finding F2 — 3.3.2's wake-up subcommand takes only
// --wing; --palace is top-level; --max-tokens does not exist.
func Wakeup(ctx context.Context, cfg WakeupConfig, wing string) (stdout string, status Status, elapsed time.Duration, err error)

type WakeupConfig struct {
    DockerBin          string
    MempalaceContainer string
    PalacePath         string
    Timeout            time.Duration // default 2s
    Logger             *slog.Logger
    Exec               DockerExec    // injection seam; nil → real os/exec
}

// MCPServerSpec builds the per-invocation mcpconfig entry for mempalace.
// Returned shape matches mcpconfig's internal mcpServerSpec; mcpconfig
// embeds this via a builder call, not an import (to avoid cycles).
func MCPServerSpec(cfg SpecConfig) (command string, args []string, env map[string]string)

type SpecConfig struct {
    DockerBin          string
    MempalaceContainer string
    PalacePath         string
    DockerHost         string
}

// DockerExec is the seam for tests. Production uses RealDockerExec.
type DockerExec interface {
    Run(ctx context.Context, args []string, stdin io.Reader) (stdout, stderr []byte, err error)
}
```

Error types: `ErrPalaceInitFailed`, `ErrPalaceUnreachable`. `Bootstrap` returns a wrapped error identifying which step failed (marker check, init invocation, init exit code). `Wakeup` never returns `ErrPalaceInitFailed`; it returns nil-err-with-StatusFailed for the non-blocking paths (timeout, non-zero exit, missing binary, unreachable container).

Wake-up composition of `--system-prompt`:

```go
// Called by spawn.runRealClaude.
func ComposeSystemPrompt(agentMD, wakeUpStdout, ticketID string) string {
    if wakeUpStdout == "" {
        return agentMD + "\n\n---\n\n## This turn\n\nYou have been spawned to work ticket " + ticketID + ". Read it, then execute your completion protocol.\n"
    }
    return agentMD + "\n\n---\n\n## Wake-up context\n\n" + wakeUpStdout + "\n\n---\n\n## This turn\n\nYou have been spawned to work ticket " + ticketID + ". Read it, then execute your completion protocol.\n"
}
```

Exact string per FR-207a. Exposed as `mempalace.ComposeSystemPrompt` for use by `spawn`.

### `internal/hygiene`

Owns the LISTEN + evaluate + sweep + palace-query pipeline.

Public surface:

```go
type HygieneStatus string
const (
    StatusClean         HygieneStatus = "clean"
    StatusMissingDiary  HygieneStatus = "missing_diary"
    StatusMissingKG     HygieneStatus = "missing_kg"
    StatusThin          HygieneStatus = "thin"
    StatusPending       HygieneStatus = "pending"
)

type Deps struct {
    DSN                  string           // garrison_agent_mempalace DSN
    Dialer               pgdb.Dialer
    Queries              *store.Queries
    Palace               *palace.Client   // docker-exec'd mempalace.mcp_server client
    Logger               *slog.Logger
    Delay                time.Duration    // default 5s
    SweepInterval        time.Duration    // default 60s
    TerminalWriteGrace   time.Duration    // inherited from spawn constant
}

// RunListener starts the LISTEN goroutine on a dedicated *pgx.Conn.
// Returns only on root-ctx cancellation or unrecoverable error (same
// shape as events.Run). Reconnects on transient pgx errors with
// exponential backoff (100ms → 30s cap), identical to M1's listener.
func RunListener(ctx context.Context, deps Deps) error

// RunSweep runs the periodic sweep at SweepInterval cadence. Each tick,
// it calls ListStuckHygieneTransitions, evaluates each returned row,
// and updates hygiene_status. Returns only on root-ctx cancellation.
func RunSweep(ctx context.Context, deps Deps) error

// Evaluator — pure rule logic extracted for unit testing.
type EvaluationInput struct {
    TicketID         pgtype.UUID
    TicketIDText     string            // "ticket_<id>" shape for KG
    RunWindowStart   time.Time
    RunWindowEnd     time.Time
    PalaceWing       string            // from the agent row
    Drawers          []PalaceDrawer    // results of mempalace_list_drawers / search
    KGTriples        []PalaceTriple    // results of mempalace_kg_query
    PalaceErr        error             // non-nil → Status='pending'
}

type PalaceDrawer struct {
    Body      string
    Wing      string
    CreatedAt time.Time
}

type PalaceTriple struct {
    Subject   string
    Predicate string
    Object    string
    ValidFrom time.Time
}

func Evaluate(in EvaluationInput) HygieneStatus
```

Evaluation rules (pure, no I/O; implemented in `evaluator.go`):

```
if in.PalaceErr != nil:                         → Pending
matchingDiary := first Drawer where
    Wing == in.PalaceWing AND
    CreatedAt ∈ [RunWindowStart, RunWindowEnd] AND
    strings.Contains(Body, in.TicketIDText)
if matchingDiary is nil:                        → MissingDiary
if len(matchingDiary.Body) < 100:               → Thin
matchingTriple := first KGTriple where
    (Subject == in.TicketIDText OR Object == in.TicketIDText) AND
    ValidFrom ∈ [RunWindowStart, RunWindowEnd]
if matchingTriple is nil:                       → MissingKG
otherwise:                                      → Clean
```

This exactly implements FR-214 (including the "Thin overrides MissingKG" precedence explicitly called out). Tests pin every branch.

`palace.Client` (in `hygiene/palace.go`):

```go
type Client struct {
    DockerBin, Container, PalacePath, DockerHost string
    Exec mempalace.DockerExec // shared seam
    Timeout time.Duration     // per-evaluation palace-query budget; default 10s
}

// Query spawns a short-lived `docker exec -i <container> python -m
// mempalace.mcp_server --palace <path>`, sends JSON-RPC requests over its
// stdin, reads responses from stdout, closes stdin to trigger EOF-based
// cleanup, and returns the parsed drawers + triples or PalaceErr.
func (c *Client) Query(ctx context.Context, ticketIDText, wing string, window TimeWindow) (drawers []PalaceDrawer, triples []PalaceTriple, err error)
```

The Query implementation issues two JSON-RPC `tools/call` requests in sequence inside the same process: `mempalace_search` (bounded to the wing and ticket ID) and `mempalace_kg_query` (entity=ticketIDText). Timeout governs the whole query; on timeout or JSON-RPC error, `err` is non-nil → Evaluator returns `Pending`. No retries inside `Query`; retry is the sweep's job.

Hygiene goroutine shutdown behaviour (FR-217): on root-ctx cancellation, RunListener/RunSweep finish their in-flight evaluation using `context.WithoutCancel(ctx)` + `TerminalWriteGrace`, then exit. No row left mid-update.

## Subsystem state machines

### Palace bootstrap (simplified per T001 finding F1)

```
   ┌──────────┐
   │ startup  │
   └────┬─────┘
        │ docker exec garrison-mempalace mempalace init --yes /palace
        │ (unconditional; init --yes is idempotent in 3.3.2)
        ├──── exit 0 ─► log palace_initialized=true → continue
        └──── exit N ─► log path+error → return ExitFailure
                         (covers: docker unreachable, permission denied,
                          interactive-prompt regression, timeout)
```

Rationale: T001 proved that `mempalace init --yes` is idempotent — a second call exits 0 and re-writes the identical `mempalace.yaml` without side effect. The detect-first branch with a marker file was premised on a false assumption that `chroma.sqlite3` is the post-init marker (it is actually the post-first-write marker). Leaning on MemPalace's own idempotency is simpler and correct.

### Wake-up invocation

```
spawn.runRealClaude entry (after agent resolve, before mcpconfig.Write):
  ctx2, cancel := context.WithTimeout(ctx, cfg.HygieneWakeupTimeout /* 2s */)
  defer cancel()
  stdout, status, elapsed, err := mempalace.Wakeup(ctx2, …, agent.PalaceWing)
  // status is one of: StatusOK (exit 0, stdout captured)
  //                   StatusFailed (timeout, non-zero exit, missing binary, unreachable container)
  // err is always nil for StatusFailed — failure is signalled via status, not error (non-blocking)
  // For StatusOK:     wake-up block inserted into --system-prompt
  // For StatusFailed: wake-up block omitted, warn-level log with fields
  //                   (palace_wing, elapsed_ms, error_summary)
```

### Hygiene evaluation (one row)

```
       ┌───────────────────────────┐
       │ NOTIFY arrives on         │
       │ work.ticket.transitioned  │
       │ .<dept>.<from>.<to>       │
       └──────────────┬────────────┘
                      │ payload = {transition_id, ticket_id, agent_instance_id}
                      ▼
            ┌──── time.Sleep(Delay=5s) ─────┐
            │ (interruptible by ctx)         │
            └────────────┬───────────────────┘
                         ▼
       ┌─────────────────────────────────────┐
       │ q.GetAgentInstanceRunWindow(ai_id)   │
       │ → {started_at, finished_at, wing,    │
       │    role_slug}                        │
       │ If triggered_by_agent_instance_id is │
       │ NULL → skip (edge case; hygiene_status│
       │ stays NULL)                           │
       └────────────┬─────────────────────────┘
                    ▼
       ┌─────────────────────────────────────┐
       │ palace.Client.Query(                 │
       │   ticketIDText, wing, runWindow)     │
       │ Errors/timeout → PalaceErr ≠ nil     │
       └────────────┬─────────────────────────┘
                    ▼
       ┌─────────────────────────────────────┐
       │ Evaluate(EvaluationInput{...})       │
       │ → HygieneStatus                      │
       └────────────┬─────────────────────────┘
                    ▼
       ┌─────────────────────────────────────┐
       │ q.UpdateTicketTransitionHygiene(     │
       │   transition_id, status)             │
       │ WHERE hygiene_status IS NULL OR      │
       │ = 'pending' (at-most-once-to-        │
       │ terminal per FR-215)                 │
       └──────────────────────────────────────┘
```

On shutdown, any in-flight row completes its UPDATE under `context.WithoutCancel + TerminalWriteGrace`, then the goroutine exits.

The sweep goroutine runs the same evaluate/update sequence for rows returned by `ListStuckHygieneTransitions`, batched at `LIMIT 100` per tick.

## Data model + migration

**Migration file**: `migrations/20260423000004_m2_2_mempalace.sql` (concrete timestamp decided at implementation time).

### Schema changes

```sql
-- (1a) Wake-up outcome capture on agent_instances.
ALTER TABLE agent_instances ADD COLUMN wake_up_status TEXT NULL;
-- Permitted values: 'ok', 'failed', 'skipped'. Enforced at the application layer.

-- (1b) Role slug on agent_instances (for hygiene's run-window → wing resolution).
ALTER TABLE agent_instances ADD COLUMN role_slug TEXT NOT NULL DEFAULT 'engineer';
-- Back-fills all M2.1 rows to 'engineer'. M2.2 InsertRunningInstance passes
-- the role_slug explicitly going forward.
```

No new palace-metadata tables (Clarify Q5).

### Role and grants

```sql
CREATE ROLE garrison_agent_mempalace LOGIN;
-- No password set; operator runs ALTER ROLE post-migrate per ops checklist.
GRANT SELECT ON ticket_transitions, agent_instances, tickets, agents
  TO garrison_agent_mempalace;
-- Note: agents added to the SELECT set so the hygiene join can resolve
-- palace_wing via role_slug. Strict SELECT-only; no write grants.
```

### New trigger — `emit_ticket_transitioned`

```sql
CREATE OR REPLACE FUNCTION emit_ticket_transitioned() RETURNS trigger AS $$
DECLARE
  event_id UUID;
  payload JSONB;
  dept_slug TEXT;
  channel TEXT;
BEGIN
  -- Resolve department from the ticket.
  SELECT d.slug INTO dept_slug
  FROM tickets t JOIN departments d ON d.id = t.department_id
  WHERE t.id = NEW.ticket_id;

  IF dept_slug IS NULL THEN
    RAISE EXCEPTION 'emit_ticket_transitioned: ticket % has no department', NEW.ticket_id;
  END IF;

  -- from_column is nullable (entry into the board); skip emit if NULL.
  -- M2.2 transitions always carry a from_column, but future flows may not.
  IF NEW.from_column IS NULL THEN
    RETURN NEW;
  END IF;

  channel := 'work.ticket.transitioned.' || dept_slug || '.' || NEW.from_column || '.' || NEW.to_column;
  payload := jsonb_build_object(
    'transition_id', NEW.id,
    'ticket_id', NEW.ticket_id,
    'agent_instance_id', NEW.triggered_by_agent_instance_id,
    'from_column', NEW.from_column,
    'to_column', NEW.to_column,
    'at', NEW.at
  );

  INSERT INTO event_outbox (channel, payload)
    VALUES (channel, payload) RETURNING id INTO event_id;
  PERFORM pg_notify(channel, jsonb_build_object('event_id', event_id)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ticket_transitioned_emit
  AFTER INSERT ON ticket_transitions
  FOR EACH ROW EXECUTE FUNCTION emit_ticket_transitioned();
```

The trigger fires inside the same transaction as the supervisor's `InsertTicketTransition`, preserving M1's event-bus-transactional guarantee.

### Workflow JSONB update

```sql
UPDATE departments
SET workflow = jsonb_build_object(
  'columns', jsonb_build_array(
    jsonb_build_object('slug','todo',      'label','To do',     'entry_from', jsonb_build_array('backlog')),
    jsonb_build_object('slug','in_dev',    'label','In dev',    'entry_from', jsonb_build_array('todo')),
    jsonb_build_object('slug','qa_review', 'label','QA review', 'entry_from', jsonb_build_array('in_dev')),
    jsonb_build_object('slug','done',      'label','Done',      'entry_from', jsonb_build_array('qa_review'))
  ),
  'transitions', jsonb_build_object(
    'todo',      jsonb_build_array('in_dev'),
    'in_dev',    jsonb_build_array('qa_review'),
    'qa_review', jsonb_build_array('done')
  )
)
WHERE slug = 'engineering';
```

### Seed updates

```sql
-- Engineer: UPDATE palace_wing, listens_for, agent_md.
UPDATE agents
SET palace_wing = 'wing_frontend_engineer',
    listens_for = '["work.ticket.created.engineering.in_dev"]'::jsonb,
    agent_md    = $engineer_md$<...contents of migrations/seed/engineer.md via +embed-agent-md tooling...>$engineer_md$
WHERE role_slug = 'engineer'
  AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');

-- QA engineer: INSERT new row.
INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
VALUES (
  gen_random_uuid(),
  (SELECT id FROM departments WHERE slug = 'engineering'),
  'qa-engineer',
  $qa_md$<...contents of migrations/seed/qa-engineer.md...>$qa_md$,
  'claude-haiku-4-5-20251001',
  '[]'::jsonb,
  '[]'::jsonb,
  '["work.ticket.transitioned.engineering.in_dev.qa_review"]'::jsonb,
  'wing_qa_engineer',
  'active'
);
```

Embed-agent-md tooling from M2.1 (`make seed-engineer-agent`) is extended with `make seed-qa-engineer-agent`; both paste file contents between `-- +embed-agent-md:<role>:begin/end` markers in the migration file. Pattern inherited verbatim from M2.1.

### Down migration

```sql
DROP TRIGGER IF EXISTS ticket_transitioned_emit ON ticket_transitions;
DROP FUNCTION IF EXISTS emit_ticket_transitioned();
DELETE FROM agents WHERE role_slug = 'qa-engineer'
  AND department_id = (SELECT id FROM departments WHERE slug = 'engineering');
-- Engineer agent_md and palace_wing reset to M2.1 state intentionally skipped:
-- down migrations in this project are best-effort for disaster recovery, not
-- perfect time travel. M2.1's SQL re-apply if the operator needs the M2.1 shape.
REVOKE SELECT ON ticket_transitions, agent_instances, tickets, agents FROM garrison_agent_mempalace;
DROP ROLE IF EXISTS garrison_agent_mempalace;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS role_slug;
ALTER TABLE agent_instances DROP COLUMN IF EXISTS wake_up_status;
-- Workflow JSONB reset to M2.1 shape (todo → done).
UPDATE departments SET workflow = '{"columns":[{"slug":"todo","label":"To do","entry_from":["backlog"]},{"slug":"done","label":"Done","entry_from":["todo"]}],"transitions":{"todo":["done"]}}'::jsonb
WHERE slug = 'engineering';
```

## Error vocabulary

**`exit_reason`** — M2.1 canon preserved; M2.2 additions:
- `mcp_mempalace_connected` — never written (happy path); listed for completeness.
- `mcp_mempalace_failed` | `mcp_mempalace_needs-auth` | `mcp_mempalace_<unknown>` — produced by `FormatMCPFailure("mempalace", <verbatim status>)`. `<unknown>` is any string outside the spike's `{connected, failed, needs-auth}` enum, treated as failure (fail-closed).
- `budget_exceeded` — new constant `ExitBudgetExceeded = "budget_exceeded"`. Written when the terminal result event reports budget overrun.

**`wake_up_status`** — column values: `'ok'`, `'failed'`, `'skipped'`. M2.2 never writes `'skipped'`.

**`hygiene_status`** — column values: `'clean'`, `'missing_diary'`, `'missing_kg'`, `'thin'`, `'pending'`, plus NULL (no evaluation yet / edge case with NULL triggered_by_agent_instance_id).

**New pg_notify channel template**: `work.ticket.transitioned.<dept_slug>.<from_column>.<to_column>`. M2.2 registers exactly one: `work.ticket.transitioned.engineering.in_dev.qa_review`.

## Seed content

Both files committed at `migrations/seed/*.md`. They are the source of truth for `agents.agent_md` at migration time.

### `migrations/seed/engineer.md`

```markdown
# Engineer (M2.2)

## Role

You are the engineer in the Garrison engineering department. You work one
ticket per invocation: you read it, produce its deliverable, record what
you did in MemPalace, and transition it to QA review.

## Tools

- `postgres` MCP — SELECT-only SQL. Use the `query` tool to read tickets
  and the `ticket_transitions` table; do NOT use it to write.
- `mempalace` MCP — read + write. Use `mempalace_search`,
  `mempalace_kg_query`, `mempalace_add_drawer`, `mempalace_kg_add`.
- Claude Code built-in tools (Read, Write, Bash) — use Write to produce
  your deliverable; use Read to verify it.

Your wing is `wing_frontend_engineer`.

## Completion protocol (MANDATORY)

Execute these steps in order. Do not skip any; do not add steps.

### 1. Read the ticket

Call the `query` tool:

    SELECT id, objective, acceptance_criteria, metadata
      FROM tickets WHERE id = '<ticket-id-from-your-task-prompt>';

### 2. Search the palace

Call `mempalace_search` with a query derived from the ticket objective,
scoped to your wing:

    mempalace_search(query="<ticket objective keywords>",
                     wing="wing_frontend_engineer")

Then call `mempalace_kg_query` to fetch any prior triples mentioning
this specific ticket:

    mempalace_kg_query(entity="ticket_<ticket-id>")

Read what comes back. If it's relevant, let it inform step 3.

### 3. Implement

Create a file at `changes/hello-<ticket-id>.md` in your current working
directory (use Claude Code's Write tool). Its body is ONE paragraph
describing what you did on this ticket. Keep it factual — a reader
should learn what changed and why from that paragraph alone.

### 4. Write a diary entry

Call `mempalace_add_drawer` with wing="wing_frontend_engineer",
room="hall_events", and body = the YAML frontmatter below followed by
a prose paragraph. The prose MUST mention the ticket id explicitly.

    ---
    ticket_id: <ticket-id>
    outcome: <one-line summary>
    artifacts:
      - changes/hello-<ticket-id>.md
    rationale: |
      <one paragraph: why you implemented it this way>
    blockers: []
    discoveries: []
    completed_at: <ISO timestamp>
    ---

    <prose paragraph; must mention ticket <ticket-id> by id; ≥ 100 chars>

### 5. Write KG triples

Call `mempalace_kg_add` TWICE (minimum):

    mempalace_kg_add(subject="agent_instance_<your-instance-id>",
                     predicate="completed",
                     object="ticket_<ticket-id>")

    mempalace_kg_add(subject="changes/hello-<ticket-id>.md",
                     predicate="created_in",
                     object="ticket_<ticket-id>")

Your instance id is in your task prompt. If you made non-trivial
decisions, add more triples with predicate="decided_because".

### 6. Transition the ticket

Call the `query` tool with an UPDATE — wait, `query` is SELECT-only.
Use the built-in postgres MCP `execute` tool (or equivalent write path
exposed to you):

    UPDATE tickets SET column_slug = 'qa_review' WHERE id = '<ticket-id>';

Then INSERT the transition row:

    INSERT INTO ticket_transitions
      (ticket_id, from_column, to_column, triggered_by_agent_instance_id)
      VALUES ('<ticket-id>', 'in_dev', 'qa_review', '<your-instance-id>');

Only after all of 1–5 have completed successfully.

## What you do not do

- Do not skip the diary write even if the implementation was trivial.
  Thin diaries are flagged; missing diaries block the hygiene dashboard
  from learning anything about this ticket.
- Do not invent new `mempalace_*` tool names. If you need a capability
  that isn't in your tool list, stop and report.
- Do not write to wings other than `wing_frontend_engineer`.
- Do not transition the ticket if any of steps 1–5 failed.

## Failure modes

- Postgres MCP unavailable → stop; the supervisor's init-event health
  check will have already bailed.
- MemPalace MCP unavailable → stop; same.
- `mempalace_add_drawer` errors → retry once; if it fails again, report
  via a tool_result with is_error and stop.
- `mempalace_kg_add` errors → retry once; same discipline.
```

### `migrations/seed/qa-engineer.md`

```markdown
# QA Engineer (M2.2)

## Role

You are the QA engineer. You spawn after an engineer transitions a ticket
to `qa_review`. You verify their work, record what you found, and
transition the ticket to `done`.

## Tools

- `postgres` MCP — SELECT-only. Use `query` to read tickets, transitions,
  and (for the write at the end) the write path your environment exposes.
- `mempalace` MCP — read + write. Use `mempalace_search`,
  `mempalace_kg_query`, `mempalace_add_drawer`, `mempalace_kg_add`.
- Claude Code built-in tools — Read for verifying the engineer's output.

Your wing is `wing_qa_engineer`.

## Completion protocol (MANDATORY)

### 1. Read the ticket and its transition

    SELECT id, objective, acceptance_criteria, metadata, column_slug
      FROM tickets WHERE id = '<ticket-id-from-your-task-prompt>';

    SELECT id, from_column, to_column, triggered_by_agent_instance_id
      FROM ticket_transitions
      WHERE ticket_id = '<ticket-id>' AND to_column = 'qa_review'
      ORDER BY at DESC LIMIT 1;

### 2. Fetch the engineer's palace context

Search your own wing first (in case you've reviewed a related ticket
before):

    mempalace_search(query="<ticket objective keywords>",
                     wing="wing_qa_engineer")

Then fetch the engineer's KG triples for this ticket:

    mempalace_kg_query(entity="ticket_<ticket-id>")

Then search broadly for the engineer's diary entry:

    mempalace_search(query="ticket <ticket-id>")

You should see the engineer's diary drawer (from `wing_frontend_engineer`)
referencing this ticket. If you don't, that's a hygiene failure — still
proceed; the hygiene checker will flag it.

### 3. Verify the engineer's output

Read `changes/hello-<ticket-id>.md` using Claude Code's Read tool.
Confirm the file exists and reads as a coherent paragraph describing
what was done on this ticket. If it's missing or nonsense, record that
in your diary (step 4) and still transition to `done` — M2.2 soft-gates
the QA verdict; real review logic arrives in a later milestone.

### 4. Write a diary entry

Call `mempalace_add_drawer` with wing="wing_qa_engineer",
room="hall_events", body = YAML frontmatter + prose:

    ---
    ticket_id: <ticket-id>
    outcome: reviewed — <one-line verdict>
    artifacts: []
    rationale: |
      <one paragraph: what you looked at, what you concluded>
    blockers: []
    discoveries: []
    completed_at: <ISO timestamp>
    ---

    <prose paragraph; must mention ticket <ticket-id> by id; ≥ 100 chars>

### 5. Write KG triples

Call `mempalace_kg_add` at least once:

    mempalace_kg_add(subject="agent_instance_<your-instance-id>",
                     predicate="reviewed",
                     object="ticket_<ticket-id>")

### 6. Transition the ticket

    UPDATE tickets SET column_slug = 'done' WHERE id = '<ticket-id>';

    INSERT INTO ticket_transitions
      (ticket_id, from_column, to_column, triggered_by_agent_instance_id)
      VALUES ('<ticket-id>', 'qa_review', 'done', '<your-instance-id>');

## Failure modes

Same as the engineer's: MCP unavailable → stop; diary/KG write failures →
retry once then stop. Do not transition if steps 1–5 failed.
```

FR-225 compliance: every `mempalace_*` tool named above is in spike §3.6's enumeration. No new tool names invented.

## Dockerfiles + Compose topology

### `supervisor/Dockerfile` (extends M2.1)

The only delta from M2.1's Dockerfile is one line in the runtime stage:

```dockerfile
# Runtime stage (extends M2.1)
RUN apk add --no-cache docker-cli
```

No Python runtime, no MemPalace install, no extra stages. Image grows by ~30 MB for the docker CLI binary.

### `supervisor/Dockerfile.mempalace` (new; per T001 findings F3/F4/F6)

```dockerfile
# syntax=docker/dockerfile:1

# python:3.11-slim, not alpine — chromadb's Rust-compiled transitives
# (tokenizers etc.) have no musl wheels; alpine would require a Rust
# toolchain (T001 finding F3).
FROM python:3.11-slim AS mempalace

# procps gives ps/pgrep for human operator inspection (T001 finding F6).
RUN apt-get update && apt-get install -y --no-install-recommends \
      procps && \
    rm -rf /var/lib/apt/lists/*

# Hash-pinned install via pip-compile-generated lockfile (T001 finding F4).
# The top-level mempalace hash is:
#   sha256:cb288e8028d26dfb384125baecfcf584aa2ba5a30a216ff82d9745af070d5e45
# Every transitive dep is pinned-and-hashed in the lockfile; regenerated
# on every version bump via `pip-compile` (pip-tools).
COPY requirements-lock.txt /tmp/requirements-lock.txt
RUN pip install --no-cache-dir --require-hashes -r /tmp/requirements-lock.txt

# Pre-create the palace mount point with sensible defaults.
RUN mkdir -p /palace && chown -R nobody:nogroup /palace

USER nobody
WORKDIR /palace

# Idle host: supervisor drives everything via `docker exec`.
ENTRYPOINT ["sleep", "infinity"]
```

`supervisor/requirements-lock.txt` is a new committed artifact, produced by running `pip-compile --generate-hashes requirements.in` where `requirements.in` contains one line — `mempalace==3.3.2`. Image size observed in T001: **478 MB**.

### `supervisor/docker-compose.yml` (new)

```yaml
services:
  postgres:
    image: postgres:17-alpine
    # ...existing M1/M2.1 postgres config...

  mempalace:
    build:
      context: .
      dockerfile: Dockerfile.mempalace
    container_name: garrison-mempalace
    volumes:
      - mempalace-palace:/palace
    restart: unless-stopped

  docker-proxy:
    image: ghcr.io/linuxserver/socket-proxy:<pinned-digest>
    container_name: garrison-docker-proxy
    environment:
      POST: 1
      EXEC: 1
      CONTAINERS: 1
      # everything else defaults to 0 (deny)
    volumes:
      # Host docker socket, read-only. Proxy is the trust boundary.
      - /var/run/docker.sock:/var/run/docker.sock:ro
    # Proxy listens on TCP :2375 inside the container (T001 finding F5 —
    # linuxserver/socket-proxy is a HAProxy TCP listener, not a unix-socket
    # proxy). Not published to host; reachable from the supervisor over the
    # compose network via service-name DNS.
    restart: unless-stopped

  supervisor:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: garrison-supervisor
    depends_on:
      - postgres
      - mempalace
      - docker-proxy
    environment:
      GARRISON_DATABASE_URL: postgres://supervisor:<pw>@postgres:5432/garrison
      GARRISON_AGENT_RO_PASSWORD: ${GARRISON_AGENT_RO_PASSWORD}
      GARRISON_AGENT_MEMPALACE_PASSWORD: ${GARRISON_AGENT_MEMPALACE_PASSWORD}
      GARRISON_MEMPALACE_CONTAINER: garrison-mempalace
      GARRISON_PALACE_PATH: /palace
      GARRISON_CLAUDE_BUDGET_USD: "0.10"
      # Supervisor talks to the proxy over compose-network TCP; the proxy
      # filters per endpoint (POST=1 EXEC=1 CONTAINERS=1). No shared socket
      # volume needed.
      DOCKER_HOST: tcp://garrison-docker-proxy:2375
    restart: unless-stopped

volumes:
  mempalace-palace:
```

Secrets (GARRISON_*_PASSWORD) come from Coolify's env-var store or operator-supplied `.env`; the compose file is the plan artifact, the values are operator-owned.

## Test strategy

Every test named below is a committed artefact in M2.2. "Verifies X" describes the assertion, not a vague intent.

### Unit tests (no docker, no postgres)

- `internal/mempalace/bootstrap_test.go::TestBootstrapRunsInit` — fake DockerExec returns exit 0; verifies init was invoked with `docker exec <container> mempalace init --yes <palace_path>`, returns nil error.
- `internal/mempalace/bootstrap_test.go::TestBootstrapIdempotent` — fake DockerExec returns exit 0 on two successive calls; both return nil; no in-band state flipping (T001 finding F1 rationale).
- `internal/mempalace/bootstrap_test.go::TestBootstrapFailsOnInitError` — init returns exit 1 with stderr; Bootstrap returns wrapped error naming the step, stderr snippet, and the palace path.
- `internal/mempalace/wakeup_test.go::TestWakeupOK` — DockerExec returns stdout `"L0: …\nL1: …"`, exit 0, within timeout. Returns StatusOK, the exact stdout, elapsed ≥ 0.
- `internal/mempalace/wakeup_test.go::TestWakeupTimeout` — DockerExec blocks longer than the timeout. Returns StatusFailed, err == nil, elapsed ≈ timeout. Log fields `palace_wing`, `elapsed_ms`, `error` present.
- `internal/mempalace/wakeup_test.go::TestWakeupNonZeroExit` — DockerExec returns exit 2 with stderr. Returns StatusFailed, stdout empty.
- `internal/mempalace/wakeup_test.go::TestWakeupExecError` — DockerExec returns `exec: "docker": executable file not found`. Returns StatusFailed with that error message.
- `internal/mempalace/mcpspec_test.go::TestMCPServerSpec` — asserts `MCPServerSpec({DockerBin: "/usr/bin/docker", MempalaceContainer: "garrison-mempalace", PalacePath: "/palace", DockerHost: "tcp://garrison-docker-proxy:2375"})` yields exactly `command="/usr/bin/docker"`, `args=["exec", "-i", "garrison-mempalace", "python", "-m", "mempalace.mcp_server", "--palace", "/palace"]`, `env={"DOCKER_HOST":"tcp://garrison-docker-proxy:2375"}`.
- `internal/mcpconfig/mcpconfig_test.go::TestWritePostgresPlusMempalace` — asserts the emitted JSON has both `postgres` and `mempalace` keys, shapes exact per §MCP config extension.
- `internal/hygiene/evaluator_test.go::TestEvaluatePalaceError` — PalaceErr set → StatusPending.
- `internal/hygiene/evaluator_test.go::TestEvaluateMissingDiary` — empty Drawers → StatusMissingDiary.
- `internal/hygiene/evaluator_test.go::TestEvaluateThinDiaryOverridesMissingKG` — Drawer with 50-char body, empty Triples → StatusThin (not StatusMissingKG; FR-214 precedence).
- `internal/hygiene/evaluator_test.go::TestEvaluateMissingKG` — Drawer with 120-char body, empty Triples → StatusMissingKG.
- `internal/hygiene/evaluator_test.go::TestEvaluateClean` — Drawer + matching Triple, both within window → StatusClean.
- `internal/hygiene/evaluator_test.go::TestEvaluateDiaryOutsideWindow` — Drawer body matches but CreatedAt outside window → StatusMissingDiary.
- `internal/hygiene/evaluator_test.go::TestEvaluateKGDirectionAgnostic` — Triple where object == ticketIDText (not subject), within window → counted; Clean.
- `internal/hygiene/palace_test.go::TestClientQuerySuccess` — mock DockerExec returns well-formed JSON-RPC responses for `mempalace_search` and `mempalace_kg_query`; parsed drawers/triples match fixture.
- `internal/hygiene/palace_test.go::TestClientQueryTimeout` — DockerExec blocks longer than Timeout; returns PalaceErr.
- `internal/hygiene/palace_test.go::TestClientQueryDockerError` — DockerExec returns non-zero exit (e.g. container missing); returns PalaceErr with stderr summary.
- `internal/hygiene/listener_test.go::TestAtMostOnceToTerminal` — evaluate a row once → UpdateTicketTransitionHygiene called; evaluate the same row again (simulating a replay) → the UPDATE's WHERE-clause no-ops (verified by mocking the Queries).
- `internal/spawn/spawn_test.go::TestWriteTerminalWithWakeUpStatusOK` — WakeUpOK path writes `wake_up_status='ok'`.
- `internal/spawn/spawn_test.go::TestWriteTerminalWithWakeUpStatusFailed` — WakeUpFailed path writes `wake_up_status='failed'`.
- `internal/spawn/spawn_test.go::TestAdjudicateBudgetExceeded` — result event with terminal_reason containing "budget" → (failed, budget_exceeded).
- `internal/claudeproto/router_test.go::TestAssistantEventToolUses` — parses a stream-json assistant line with `content: [{type:"tool_use", name:"mempalace_add_drawer", id:"toolu_1", input:{...}}]` and verifies `ToolUses` contains the block with Name="mempalace_add_drawer".
- `internal/agents/cache_test.go::TestPalaceWingExposed` — loads a row with `palace_wing='wing_frontend_engineer'`, asserts `GetForDepartmentAndRole(...).PalaceWing` dereferences to that string.
- `internal/config/config_test.go::TestM22ConfigDefaults` — asserts `ClaudeBudgetUSD=0.10`, `HygieneDelay=5s`, `HygieneSweepInterval=60s` when no env vars set.
- `internal/config/config_test.go::TestM22ConfigRejectsZeroHygieneDelay` — `GARRISON_HYGIENE_DELAY=0` → Load returns error.

### Integration tests (`//go:build integration`; full three-container topology via testcontainers-go)

- `integration/m2_2_happy_path_test.go::TestM22EngineerPlusQAHappyPath` — seeds a ticket at `in_dev`; supervisor spawns engineer → writes diary + KG + transitions; qa-engineer spawns → writes diary + KG + transitions to `done`. Asserts: two `agent_instances` rows both `succeeded`, both `exit_reason='completed'`, both `wake_up_status='ok'`, both `total_cost_usd ≤ 0.10`; combined cost `< 0.20` (SC-202); one diary in each wing body ≥ 100 chars mentioning the ticket (SC-203); at least two KG triples mentioning the ticket (SC-203); both ticket_transitions rows resolve to `hygiene_status='clean'` within 10 s (SC-204).
- `integration/m2_2_wakeup_failure_test.go::TestM22WakeUpFailureIsNonBlocking` — configures GARRISON_MEMPALACE_CONTAINER to point at a stopped container name for this test's scope; ticket flow still completes; `wake_up_status='failed'` on the agent_instance; diary + transition still land (SC-207).
- `integration/m2_2_regression_test.go::TestM22M21RegressionStillPasses` — runs the M2.1 happy-path fixture (engineer writes hello.txt, seeds only the M2.1 role set) against the M2.2 binary; verifies the M2.1 behaviour is byte-identical with the M2.2 binary (SC-211).
- `integration/m2_2_preseed_palace_test.go::TestM22QAReadsEngineersDiary` — pre-populates `wing_frontend_engineer` with a diary and KG triples for a seeded ticket; transitions the ticket to `qa_review` directly; qa-engineer spawns, `mempalace_search` returns the pre-populated diary (observable in structured logs), qa-engineer writes its own diary and transitions to `done` (US2 isolation).

### Chaos tests (`//go:build chaos`; testcontainers-go with induced failure)

- `chaos/m2_2_broken_mempalace_mcp_config_test.go::TestM22BrokenMempalaceMCPConfigBail` — seeds a test ticket; configures the MCP config writer to point the mempalace entry at a non-existent container name; init event reports `mempalace.status=failed`; supervisor signals Claude's process group within 2 s; agent_instances row shows `status='failed'`, `exit_reason='mcp_mempalace_failed'`; no ticket transitions; `event_outbox.processed_at` set (SC-205).
- `chaos/m2_2_mempalace_mid_run_kill_test.go::TestM22MempalaceContainerStoppedMidRun` — starts the full stack; seeds a ticket; when the Claude subprocess emits its first `mempalace_*` tool_use, external test harness runs `docker stop garrison-mempalace`; verifies the run terminates via the M2.1 adjudication path (result with is_error, no_result, or parse_error depending on timing); failed agent_instances row written; no ticket transitions (SC-206).
- `chaos/m2_2_hygiene_pending_recovery_test.go::TestM22HygienePendingThenSweepRecovers` — seeds a transition row with a matching palace state already in place; blocks the hygiene checker's palace query (stop the mempalace container briefly); verifies `hygiene_status='pending'` is written on first evaluation; restart the container; verify the next sweep cycle updates the row to `'clean'` (SC-208).
- M1 + M2.1 chaos suites run unchanged (SC-210).

### Mockclaude extensions

`supervisor/internal/spawn/mockclaude/main.go` adds directives:
- `#init-mcp-servers <json>` — override the init event's `mcp_servers` array. Enables testing the two-server health-check path without a real MCP subprocess.
- `#mempalace-tool-use <name> <input-json>` — emit one `assistant` event containing a `tool_use` block with the given name and input, followed (on the next line) by a matching `user` event with a `tool_result` (success by default; `#mempalace-tool-use-error <name> <detail>` for errors). Enables testing FR-218 tool-use logging without running a real palace.
- `#budget-exceeded` — emit a terminal `result` event with `terminal_reason="budget_exceeded"` and `is_error=true`. Enables testing the `ExitBudgetExceeded` adjudication path.

New fixture scripts:
- `scripts/m2_2_happy_path.ndjson` — init (both servers connected), assistant with `mempalace_add_drawer` tool_use, user tool_result ok, assistant with `mempalace_kg_add` tool_use, user tool_result ok, result success.
- `scripts/m2_2_mempalace_init_failed.ndjson` — init with mempalace status=failed.
- `scripts/m2_2_budget_exceeded.ndjson` — init ok, assistant text, `#budget-exceeded` terminal.

### Regression check (inherited unchanged)

- All M1 tests (unit + integration + chaos) pass.
- All M2.1 tests (unit + integration + chaos) pass against the M2.2 binary with the M2.1-only test seed set.
- M2.1 `testdb` cleanup cascade continues to work under the expanded M2.2 FK graph (verified via test-startup order tests).

## Forward-compatibility hooks (one line each; not designed for)

- M2.3 vault: `config.AgentMempalacePassword` loading path will be replaced by an Infisical fetch without touching the hygiene checker. Same env-var-based carrier; Infisical populates the env at container start.
- M3 hygiene dashboard: consumes `ticket_transitions.hygiene_status` (already written by M2.2) plus the FR-218 tool-use slog stream. No new data surface.
- Hot-reload of `internal/agents` cache: re-deferred; M2.2 retro re-notes.
- Multi-department wire-up beyond engineering + qa-engineer: purely additive channel registration in `cmd/supervisor/main.go`.
- MemPalace HTTP MCP (if 3.3.2's successor adds it): shape 2b's `docker exec` path is swappable for an HTTP client with zero spec changes; only `internal/mempalace` and `internal/hygiene/palace` change.

## Spec-level findings requiring follow-up

These are plan-phase discoveries that the spec and its clarifications will need to absorb. The plan proceeds correctly without them, but future readers of the spec will be confused without the corrections.

1. **Clarify Q1 is factually wrong for MemPalace 3.3.2.** The spec's Clarifications section says the MemPalace MCP server invocation is `mempalace mcp`; the T001 validation spike (and a direct check of the 3.3.2 package source) confirmed that `mempalace mcp` is a help-text printer, not a server. The authoritative invocation is `python -m mempalace.mcp_server --palace <path>`. **Action**: spec's Clarifications block should get a follow-up entry noting the correction, before /speckit.analyze runs.

2. **FR-203 / FR-204 / FR-207 / FR-213 / NFR-208 / NFR-213 are deployment-shape-dependent.** Each assumes a local `mempalace` binary on the supervisor's $PATH and a supervisor-hosted palace path. Under 2b + socket-proxy, MemPalace lives in a sidecar container; the palace path is container-internal; the supervisor image has no Python runtime; wake-up and MCP invocations go through `docker exec`. Behaviour is preserved — wake-up returns ~170 tokens; MCP serves 29 tools; init marker detected before init; per-invocation MCP config has both entries. Only the *implementation mechanism* differs. **Action**: spec amendment noting the deployment shape and pointing at this plan's §"Deployment topology" for the concrete call shapes. No FR numbers change.

3. **Clarify C (auth model) resolves to "none beyond --palace".** The plan's T001 spike-source-check confirms `mempalace.mcp_server`'s only flag is `--palace`. No API key, no token. The spec's Clarify C can be closed: auth is filesystem-scope-of-the-palace-path only. **Action**: spec amendment closes Clarify C.

4. **Engineer's `listens_for` shifts from `work.ticket.created.engineering.todo` (M2.1) to `work.ticket.created.engineering.in_dev` (M2.2).** Per FR-228's "scope simplicity" default: M2.2 acceptance testing inserts tickets directly at `in_dev`. **Action**: explicit in the migration's engineer UPDATE, implicit in the spec via FR-228.

5. **MemPalace `mempalace_add_drawer` takes a `room` parameter, not just `wing`/`body`.** Spike §3.6's signature is `wing,room,content`. The spec's references to "write a drawer to the wing" are imprecise; the seed agent.md files (§"Seed content") use `wing=<wing>, room="hall_events"` per ARCHITECTURE.md's "halls within wings" taxonomy. **Action**: none; seed content aligns. Flagged so /speckit.analyze doesn't re-raise it.

## Open questions (flagged; not resolved)

- **Claude 2.1.117's exact `terminal_reason` string on budget overrun**: the M2.1 retro observed `"allowed_warning"` for `rate_limit_info.status` as a real-claude surprise; the exact budget-overrun terminal_reason string is not spike-characterized. Plan's adjudication uses a case-insensitive `strings.Contains(..., "budget")` prefix match as a defensive shim; the first real-claude budget overrun observed in M2.2 testing pins the string, which feeds the M2.2 retro.
- **Wing lazy-creation in 3.3.2** (Clarify F): spike §3.7 validated on 3.3.x earlier; T001 re-verifies against 3.3.2 specifically. Not a plan blocker; recorded in research.md.
- **docker-exec overhead at p99**: T001 measures; if p99 pushes close to 1.5s, the plan keeps the 2s NFR-202 ceiling but flags the thin margin for the retro. If p99 exceeds 1.5s, we negotiate with the operator on either raising the ceiling or exploring shape 2a.

---

This plan is complete. Another agent can read it and produce compiling, testable code without making further structural decisions. Any perceived gap is either (a) deferred to a named future milestone, (b) flagged as an open question with a concrete T001 validation step, or (c) a spec-level finding with a named amendment path.
