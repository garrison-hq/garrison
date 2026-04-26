# AGENTS.md

Repository-level guidance for any AI coding agent working in this project (Garrison). This file is read automatically by Claude Code, Codex, Cursor, GitHub Copilot, and any other `AGENTS.md`-aware agent. Every agent session in this repo inherits the rules below.

Read this file completely before doing anything else. If any instruction here conflicts with something you think you know, the instruction here wins.

---

## Project

**Garrison** is an event-driven agent orchestration system. A solo operator commands a standing force of AI agents organized into departments, working tickets through Kanban workflows backed by Postgres and a shared memory layer (MemPalace).

Three primary components:

- **Supervisor** — Go 1.23+ binary. Long-running process manager. Listens on `pg_notify`, spawns Claude Code subprocesses, enforces per-department concurrency caps.
- **Dashboard** — Next.js 16 + React 19 app. Operator console for Kanban, hiring, activity, CEO chat.
- **Postgres** — source of truth for state, event bus via `pg_notify`, shared by supervisor and dashboard.

Garrison is being built milestone-by-milestone (M1 through M8). Each milestone ships end-to-end functional before the next begins.

- **M1** — event bus + supervisor core. Shipped 2026-04-22.
- **M2** — first real agent loop. Shipped 2026-04-22 → 2026-04-24 across five sub-milestones:
  - **M2.1** — Claude Code invocation. Shipped 2026-04-22.
  - **M2.2** — MemPalace MCP wiring. Shipped 2026-04-23.
  - **M2.2.1** — Structured completion via `finalize_ticket` tool. Shipped 2026-04-23.
  - **M2.2.2** — Compliance calibration. Shipped 2026-04-24.
  - **M2.3** — Secret vault (Infisical). Shipped 2026-04-24.
- **M3** — dashboard. **Active.**
- **M4** through **M8** — see `ARCHITECTURE.md`.

Current milestone: **M3 — dashboard**.

---

## Binding documents

These documents govern this project. Read the ones relevant to your current task before generating code, specs, or plans.

| Document | Path | What it holds |
|---|---|---|
| Rationale | `RATIONALE.md` | 13 architectural decisions with alternatives considered and trade-offs accepted. Do not re-litigate decisions here. |
| Architecture | `ARCHITECTURE.md` | System components, data model, event flow, build plan, dashboard surfaces. |
| Active milestone context | `specs/_context/m{N}-context.md` (active: M3 — context file written via `/garrison-specify m3` when M3 spec phase opens) | Binding constraints for the active milestone. The active milestone's context file is always the most operationally relevant document for code work. |
| Constitution | `.specify/memory/constitution.md` | Spec-kit's constitution file. Populated via `/speckit.constitution`. |
| Research spikes | `docs/research/m{N}-spike.md` | Observed behavior of external tools that a milestone depends on. Binding input to the milestone's context file. |
| Vault threat model | `docs/security/vault-threat-model.md` | Design input for M2.3 (Infisical integration). |
| Ops checklist | `docs/ops-checklist.md` | Post-migrate and post-deploy steps operators must run. Updated at every milestone ship. |
| Retros | `docs/retros/m{N}.md` (canonical) **plus** MemPalace `wing_company / hall_events` mirror via the wired-in MCP tools (from M3 onwards). M1, M2.1, and the M2.x arc retros are markdown-only by historical decision — see Retros section. | Post-ship findings. |

When the active milestone changes, the relevant context file path changes with it. Always check which milestone is active before assuming a particular context file applies.

---

## Precedence when documents conflict

When two sources disagree, resolve in this order (highest authority first):

1. **`RATIONALE.md`** — design decisions are binding. If something here says "we chose X over Y," do not propose Y.
2. **Active milestone context** (`specs/_context/m{N}-context.md`) — operational constraints for current work.
3. **`ARCHITECTURE.md`** — the system's structural truth.
4. **Constitution** (`.specify/memory/constitution.md`) — spec-kit conventions.
5. **Installed skills** (under `obra/superpowers`, `supabase/agent-skills`, etc.) — general discipline.
6. **Your own defaults** — what you'd do in a generic project.

If a lower authority contradicts a higher one, the higher one wins. Flag the contradiction in your commit message or PR description so the lower-authority document can be updated. Do not stop to ask the human unless two equal-authority sources conflict with each other.

---

## Activate before writing code

Before producing any code in this repository, explicitly bring relevant domain knowledge into working memory. Which domains to activate depends on which milestone is active. Activate the current milestone's domains plus all prior milestones' domains that remain in the codebase.

### M1 — event bus + supervisor core (shipped)

- **Go 1.23+ idioms**: `context.Context` threaded through every goroutine; no bare `go func()`; `errgroup.WithContext` for "run N subsystems, cancel all if one fails" at the top level; channels with explicit sender/receiver responsibility; buffered channels only with a documented reason; stdlib `log/slog` for structured logging.
- **`jackc/pgx/v5` patterns**: the LISTEN connection is dedicated (a plain `*pgx.Conn` or manually hijacked `*pgxpool.Conn`), never from the pool; reconnect with exponential backoff (100ms → 30s cap); on reconnect, run the `processed_at` fallback poll before re-LISTENing.
- **Postgres LISTEN/NOTIFY**: payload size is limited (8KB); channel names are dot-delimited (`work.ticket.created.<dept>.<column>`); NOTIFY fires inside the same transaction as the state change; missed notifications are expected and handled via `processed_at` + periodic poll.
- **`sqlc`**: SQL migrations are the source of truth; query types are generated, not hand-written; both Go (`sqlc`) and TypeScript (Drizzle, later) derive from the same SQL.
- **`os/exec` with `CommandContext`**: every subprocess has a timeout context; cancellation sends SIGTERM then SIGKILL after a grace period; stdout/stderr piped per the pipeline-drain rule (concurrency rule 8).
- **`testcontainers-go`**: integration tests spin ephemeral Postgres containers; no mocking of the DB layer.

### M2.1 — Claude Code invocation (shipped)

- **Claude Code non-interactive invocation contract**: exact flags, input format, stdout/stderr shape, exit code vocabulary. Specifics from `docs/research/m2-spike.md` Part 1. Do not activate assumptions — activate the spike's findings.
- **stream-json event routing**: every invocation uses `--output-format stream-json --verbose`. The first event is `system`/`init`; subsequent events are `assistant`, `user` (tool_result), `rate_limit_event`, and a terminal `result`. The supervisor parses NDJSON line-by-line and routes by `type`.
- **MCP health via init event**: the supervisor parses the `mcp_servers[]` array in the `system`/`init` event. A server with `status == "connected"` is healthy; anything else (`failed`, `needs-auth`, unknown value) means the supervisor kills the Claude process group immediately and records the agent_instance as failed with `exit_reason = "mcp_<server>_<status>"`. Unknown status values are treated as failures (fail-closed).
- **`internal/pgmcp`**: in-tree Go Postgres MCP server (~300 LOC), stdio JSON-RPC, `query` + `explain` tools. Read-only enforced via Postgres role (`garrison_agent_ro`) plus protocol-layer filter. Works as `supervisor mcp postgres` subcommand.

### M2.2 — MemPalace MCP wiring (shipped)

- **MemPalace MCP server lifecycle and concurrent access semantics.** From `docs/research/m2-spike.md` Part 2.
- **MemPalace `init --yes` discipline.** Runs against a dedicated non-git-tracked directory. Never against the Garrison repo or supervisor workspace. T001 finding F1: the init is idempotent in 3.3.2; the `chroma.sqlite3` marker-file heuristic is unreliable.
- **Diary entry and KG triple write contracts** as described in `ARCHITECTURE.md` "MemPalace write contract" section.
- **Hygiene check query patterns** — post-transition async checks for expected writes. Two evaluation paths now coexist (see M2.2.1).
- **`garrison_agent_mempalace` Postgres role** — SELECT-only on `ticket_transitions + agent_instances + tickets + agents`; password set via ops checklist post-migrate; composed into `AgentMempalaceDSN()` at supervisor startup (parallels M2.1's `garrison_agent_ro`).
- **Three-container deployment topology**: supervisor + `Dockerfile.mempalace` (python:3.11-slim with chromadb wheels — alpine fails) + `linuxserver/socket-proxy` on `tcp://garrison-docker-proxy:2375` (NOT a unix-socket proxy, despite the name).
- **`emit_ticket_transitioned` Postgres trigger** with `department_id` resolved via subquery — the M2.2 retro flagged the original payload-omission bug.

### M2.2.1 — Structured completion via `finalize_ticket` tool (shipped)

- **`internal/finalize` MCP server**: in-tree, single-tool (`finalize_ticket`), the only path that commits a ticket transition. Schema-strict (`outcome ≥ 10 chars`, `rationale ≥ 50`, `kg_triples ≥ 1` with each field ≥ 3 chars).
- **`spawn.WriteFinalize` atomic transaction**: brackets two MemPalace sidecar calls (AddDrawer + N× AddTriples) and four DML writes (`ticket_transitions`, `tickets` update, `agent_instances` terminal, `event_outbox` mark-processed). 30-second wall-clock ceiling via `GARRISON_FINALIZE_WRITE_TIMEOUT`. Retries cap at 3 in `internal/spawn/pipeline.go`.
- **Hygiene-status vocabulary extensions**: `finalize_failed`, `finalize_partial`, `stuck` alongside the M2.2 vocabulary.
- **Two hygiene evaluation paths coexist**: pure-Go `EvaluateFinalizeOutcome` for finalize-shaped rows; palace-query `Evaluate` for legacy M2.2 rows. Routed by `agent_instances.exit_reason`. No retroactive UPDATE; no CHECK constraint on the column during the transition window.
- **The objective-prose-prepend pattern** for diary bodies — `mempalace_search` is vector-similarity, not substring; raw UUIDs return zero matches. Anything you want findable by ticket-id needs prose around it.

### M2.2.2 — Compliance calibration (shipped)

- **Richer structured error responses** in `internal/finalize`: `Failure`/`Constraint` typed enums; `ValidationError` with `line/column/excerpt/constraint/expected/actual/hint` fields. `decodePosition` helper sanitises ASCII control chars in excerpts to `·`.
- **Adjudicate precedence fix**: `isBudgetTerminalReason` checked BEFORE `result.IsError` when `ResultSeen=true` (one-line swap in `internal/spawn/pipeline.go`). Budget-cap events no longer masked as `claude_error`. See M2.2.1 retro §"Bonus finding" for the surfacing run.
- **Seed agent.md shape**: front-loaded goal sentence in `## Wake-up context`; palace-search calibration as 3 bullets (Skip-if-trivial / Search-if / In-doubt-skip); example payload as fenced JSON with angle-bracket placeholders; retry framing in `## Failure modes` naming the `hint` field. Files at `migrations/seed/{engineer,qa-engineer}.md`, embedded into migrations via `+embed-agent-md` markers.
- **Live-matrix caveat**: M2.2.2's retro originally read the calibration thesis as falsified on live models. The M2.2.x arc retro (`docs/retros/m2-2-x-compliance-retro.md`) revises that read against the contaminated-data root cause uncovered in the post-ship pgmcp investigation. The richer-error infra is correct and ready; whether it moves model behaviour is partially observed (one positive retry in the post-fix matrix).

### M2.3 — Secret vault / Infisical (shipped)

- **`vault.SecretValue` opaque type**: `LogValue()` returns `[REDACTED]`; `UnsafeBytes()` is grep-auditable (only 2 production call sites: spawn env injection, Rule 1 leak scan); `Zero()` clears the backing slice after subprocess start. **The `tools/vaultlog` go vet analyzer rejects slog/fmt/log calls with `SecretValue` arguments at build time.**
- **Four vault rules**: (1) no agent.md may contain a raw secret value (Rule 1 leak scan against fetched grant set); (2) zero-grants spawns get zero secrets (no implicit fetch); (3) the agent's MCP config cannot reference the vault MCP — vault is opaque to agents, `mcpconfig.CheckExtraServers` runs BEFORE vault fetch; (4) every fetch writes a `vault_access_log` row inside the spawn tx, **fail-closed if INSERT fails**.
- **Seven-step spawn ordering** in `spawn.go` (D4.5 / FR-416): grants → mcpconfig pre-check → vault fetch → leak scan → audit row → env build + `cmd.Start` (with `defer val.Zero()`) → existing M2.2.x subprocess pipeline.
- **Three new tables + first production trigger**: `agent_role_secrets`, `vault_access_log`, `secret_metadata`; trigger `rebuild_secret_metadata_role_slugs` (AFTER INSERT/DELETE on `agent_role_secrets`) rebuilds the denorm `allowed_role_slugs` array. Use this trigger pattern for denorm columns derived from another table where the rebuild is cheap; avoid for cross-schema joins or expensive computation.
- **Finalize-path scanner hook** (`scanAndRedactPayload` in `finalize.go`): non-blocking pattern scan over `DiaryEntry.Rationale` and `KGTriples[*].{Subject,Predicate,Object}` before MemPalace write; sets `hygiene_status='suspected_secret_emitted'` on any match. 10 patterns: sk-prefix, xoxb, AWS AKIA, PEM header, GitHub PAT/App/User/Server/Refresh, bearer-shape.
- **Infisical SDK quirks** (caught at integration-test time, not in the spike): SDK caches access tokens eagerly and renews on 401 — auth-expired tests need a short-lived ML (`accessTokenTTL=1`, `numUsesLimit=1`), not a rotated client_secret. 403/429 testing requires an HTTP proxy injecting status codes; testcontainers can't reproduce them natively.

### M3 — dashboard (active)

When working on M3, additionally activate:
- Next.js 16 App Router patterns, React 19 (including Server Components, Server Actions where appropriate)
- Tailwind v4 and shadcn/ui conventions
- Note from M2.1 retro: real Claude Code emits 5-10 `assistant` events per run (not the mockclaude fixture's 2). M3's activity feed UI must render high-volume event streams without visual bloat.
- M3 reads from the M2.3 vault tables (`agent_role_secrets`, `vault_access_log`, `secret_metadata`) via a dashboard-scoped read role with explicit grants — do NOT read these via `garrison_agent_ro` or any agent-facing role.
- M3 surfaces both `hygiene_status` failure modes (sandbox-escape "artifact claimed vs artifact on disk", finalize-never-called, suspected_secret_emitted) and the cost-telemetry blind spot — see `docs/issues/` for the open issues.

### M7 — hiring (not yet active)

When M7 becomes active, additionally activate:
- MCP server authoring patterns (using `anthropics/skills/mcp-builder`)
- `skills.sh` registry semantics for discovery and install
- **SkillHub (iflytek)** as the target-state private-skills component alongside the public `skills.sh` feed; see `docs/skill-registry-candidates.md` and `docs/architecture-reconciliation-2026-04-24.md` §2 for the decision provenance.

### M8 — MCP server registry (not yet active)

When M8 becomes active, additionally activate:
- **MCPJungle** as the leading candidate for the M8-era MCP server registry (self-hosted, Go-based, Postgres-backed, combines registry + runtime proxy). Maturity re-check at M7 kickoff. See `docs/mcp-registry-candidates.md`.

**Do not** activate domains from future milestones. M3 sessions have no business carrying M7 hiring-flow context or M8 MCP-registry context; it dilutes attention and tempts scope creep.

---

## Research spikes

Some milestones begin with a research spike before speccing. This applies when the milestone depends on how an external tool actually behaves (not how its documentation says it behaves). See `RATIONALE.md` §13 for the full rule.

**Workflow**:

1. Spike runs in a scratch directory outside the Garrison repo (e.g. `~/scratch/m{N}-spike/`). The scratch dir is throwaway — nothing from it is copied back into Garrison except the findings document.
2. Spike produces a single file at `docs/research/m{N}-spike.md` with these sections: Environment, findings (one subsection per investigated question), Surprises, Open questions, Implications for the upcoming milestone(s).
3. Spike is time-boxed (typically 2-4 hours). If the time budget is exhausted, stop and write up what was found. Partial findings are more valuable than complete speculation.
4. The spike document becomes binding input to the milestone's context file. Context files reference the spike by path; they do not paraphrase or duplicate it.

**Validation from M2.1**: the M2 spike prevented 5 issues that would have otherwise surfaced during implementation, while only 1 genuinely surprising issue escaped to the M2.1 retro. Prevention rate well above the informal 50% validation target. RATIONALE §13 is empirically validated.

---

## Stack and dependency rules

### Supervisor (Go)

Locked dependency list:

- `github.com/jackc/pgx/v5` — Postgres driver and LISTEN/NOTIFY
- `sqlc` (build-time) — typed query generation from SQL
- `log/slog` (stdlib) — structured logging
- `golang.org/x/sync/errgroup` — concurrent subsystem lifecycle
- `github.com/stretchr/testify` — test assertions
- `github.com/testcontainers/testcontainers-go` — integration tests (promoted to direct dep at M2.3 for the Infisical container)
- `github.com/testcontainers/testcontainers-go/modules/postgres` — direct since M2.3 (organic `go mod tidy` consequence of T011)
- `github.com/pressly/goose/v3` — migrations
- `github.com/google/shlex` — POSIX-like argv splitter for subprocess command templates (accepted in M1; see retro)
- `github.com/infisical/go-sdk v0.7.1` — Infisical Universal Auth + secret fetch + machine identity management (added M2.3). Stdlib HTTP cannot reproduce the auth refresh loop and Infisical-specific path API shape without reimplementing the SDK; alternatives (raw `net/http`, Vault OSS client) were rejected. MIT-licensed, small dependency footprint.
- `golang.org/x/tools v0.44.0` — `go/analysis` framework backing the `tools/vaultlog` custom vet analyzer that rejects slog/fmt/log calls with `vault.SecretValue` arguments at build time (added M2.3). No stdlib alternative for a `go vet`-compatible analyzer.

**Locked-deps streak**: M1 → M2.2.2 shipped with **zero** new dependencies (five consecutive milestones). M2.3 broke the streak with the two principled additions above; both are justified in `docs/retros/m2-3.md` and represent the load-bearing minimum for vault integration and compile-time secret-leak prevention. Future milestones should treat the post-M2.3 list as the new bar.

In-tree components by milestone (Garrison's own code; not on the locked-dep list):
- **M2.1**: `internal/claudeproto`, `internal/mcpconfig`, `internal/pgmcp`, `internal/agents`, `internal/spawn/{pipeline,pgroup,exitreason}`
- **M2.2**: `internal/mempalace`, `internal/hygiene`
- **M2.2.1**: `internal/finalize` (single-tool MCP server for `finalize_ticket`)
- **M2.3**: `internal/vault`, `tools/vaultlog`

**Soft rule on the list**: adding a dependency outside this list is allowed but must be justified in the commit message (one paragraph: what it does, why stdlib or an already-installed dep isn't enough, what the alternatives were). New dependencies must also be flagged in the milestone retro. Agents that silently add dependencies without justification are doing the wrong thing.

Do NOT reach for: `lib/pq` (pgx supersedes it), `gin`/`echo`/`fiber` (stdlib `net/http` + `chi` if routing is needed), `logrus`/`zap` (slog is stdlib), `viper` (env vars + a typed config struct).

### Deployment

Single static binary via `CGO_ENABLED=0 go build`. Current production Dockerfile is roughly 10 lines; M2.1's three-stage build installs Claude Code 2.1.117 with GPG fingerprint + manifest signature + SHA256 verification. Final image 264 MB (musl deps + alpine runtime). No runtime, no venv, no multistage gymnastics beyond alpine + ca-certificates + Claude Code.

---

## Concurrency discipline (non-negotiable)

These apply throughout the supervisor code:

1. Every goroutine accepts a `context.Context` and respects cancellation. No bare `go func()`.
2. `main` owns a root context. Subsystems get derived contexts. SIGTERM/SIGINT cancels the root.
3. Use `errgroup.WithContext` for the "run N subsystems, cancel all if one fails" pattern at the top level.
4. Every spawned subprocess uses `exec.CommandContext(ctx, ...)` with a timeout-derived context. Context cancellation kills the subprocess (SIGTERM, 5s grace, SIGKILL).
5. Channels: specify sender vs receiver responsibility; close from the sender side only; buffered channels need a documented reason.
6. Terminal writes during graceful shutdown use `context.WithoutCancel(ctx)` plus a `TerminalWriteGrace` timeout, not the already-cancelled root context. See M1 retro §2.
7. **Spawned subprocesses that may themselves spawn children (Claude Code from M2.1 onwards) run in their own process group.** Set `SysProcAttr.Setpgid = true` on the `exec.Cmd`, and when terminating, signal the group with `syscall.Kill(-pgid, SIGTERM)` / `SIGKILL` — never just the PID. PID-level signals allow children to survive past a supposedly-killed parent. See M1 retro addendum, `docs/research/m2-spike.md` §2.7, and `internal/spawn/pgroup.go`.
8. **Supervisor-managed subprocess pipes are drained to completion before `cmd.Wait` is called.** Any code path that pipes a subprocess's stdout (via `cmd.StdoutPipe` or equivalent) to a reader goroutine MUST wait for that reader to finish before calling `cmd.Wait`. Go's `StdoutPipe` documentation warns: "It is thus incorrect to call Wait before all reads from the pipe have completed." Violating this produces truncated reads with `"file already closed"` errors, especially on short output streams. The correct pattern: `select` on a `pipelineDone` channel, then call `Wait`. See M2.1 retro §1 and `internal/spawn/pipeline.go` for the canonical implementation. Applies to every future milestone that spawns subprocesses the supervisor directly waits on.

---

## Scope discipline (the most important rule)

Specs are narrow per milestone. Each milestone's spec covers only that milestone's concerns. The following are explicitly **out of scope for M3** even though they appear in `ARCHITECTURE.md` or in open issues:

- CEO agent + chat surfaces (M5)
- Hiring flow / agent creation UI (M7)
- skills.sh / SkillHub integration (M7)
- MCPJungle MCP-server registry (M8)
- **Workspace sandboxing / Docker-per-agent** — post-M2 follow-up; see `docs/issues/agent-workspace-sandboxing.md`. The chosen resolution is per-agent Docker containers with hard guardrails preventing workspace escape; this work happens after M3 ships.
- **Cost-telemetry blind-spot fix** — post-M2 follow-up; see `docs/issues/cost-telemetry-blind-spot.md`. M3 reads from `agent_instances.total_cost_usd` and must surface the blind spot, but does not fix it.
- Multi-department wire-up beyond engineering + qa-engineer (M2.2 / M2.2.1 shipped these two; new departments are post-M3)
- Mutating any sealed M2/M2.3 surface — supervisor spawn semantics, finalize tool schema, vault rules, `garrison_agent_*` Postgres roles, MemPalace MCP wiring. M3 is read-side work over data the M2 arc owns.

When tempted to broaden scope because "we'll need it later," stop. Note it as an open question in the spec or the retro. Do not implement it.

Milestones ship end-to-end functional. If a task list produces scaffolding that can't be exercised against real-world use, the task list is wrong.

---

## Retros

At the end of each milestone, write a retro capturing what worked, what surprised you, and what changed vs. the spec.

**Policy from M3 onwards** — every retro lands as **both**:

1. A markdown file at `docs/retros/m{N}.md`. **Canonical.** Grep-able, diff-able, survives palace drift, readable without an MCP client. On disagreement with the palace mirror, the markdown wins.
2. A MemPalace `wing_company / hall_events` drawer **mirror** of the same content, written via the wired-in `mempalace_add_drawer` MCP tool (and `mempalace_kg_add` for any KG triples that capture cross-milestone facts worth surfacing in future agent context). The drawer keeps retro content in the same memory layer agents read from when activating context.

Both are non-optional. The retro task in `/garrison-tasks` lists both deliverables explicitly; the retro is not done until both have landed.

**Historical exceptions** (markdown-only by decision at the time, kept as-is):

- **M1 retro**: `docs/retros/m1.md` — predates MemPalace.
- **M1 retro addendum**: `docs/retros/m1-retro-addendum.md` — M2 spike findings.
- **M2.1 retro**: `docs/retros/m2-1.md` — MemPalace not yet wired up.
- **M2.2, M2.2.1, M2.2.2, M2.3 retros**: all `docs/retros/m{N}.md`. Markdown-only by operator call: the M2.x arc had pgmcp bugs in the live runs that made dogfooding the palace mid-arc misleading. Read-back parity is preserved by post-merge backfill of `wing_company / hall_events` drawers (operator-owned, one-shot).
- **M2.2.x compliance arc retro**: `docs/retros/m2-2-x-compliance-retro.md` — the synthesis that closed the arc.

The retro includes:
- What shipped
- What the spec got wrong
- Dependencies added outside the locked list (with justifications)
- Open questions deferred to the next milestone
- For spike-first milestones: whether the spike paid off (prevention count vs. discovery count)

---

## Spec-kit workflow

This repo uses GitHub Spec Kit (`specify-cli`) with Garrison-flavored slash commands at `.agents/commands/garrison-*.md` (symlinked via `.claude`). The canonical workflow per milestone:

1. If the milestone requires a spike (RATIONALE §13): run the spike, produce `docs/research/m{N}-spike.md`, then continue.
2. Write `specs/_context/m{N}-context.md` using the spike findings (if any) plus the architecture doc as binding inputs.
3. `/speckit.constitution` — populate `.specify/memory/constitution.md` (first milestone only; reused thereafter; amend only when RATIONALE amends)
4. `/garrison-specify m{N}` — draft the milestone spec with Garrison discipline layered on
5. `/speckit.clarify` — resolve ambiguities before planning
6. `/garrison-plan m{N}` — produce the implementation plan
7. `/garrison-tasks m{N}` — break the plan into tasks
8. `/speckit.analyze` — cross-artifact consistency check before implementing
9. `/garrison-implement m{N}` — execute the tasks
10. Write the retro.

Each command loads the active milestone's context file as binding input. Generated specs inherit constraints from the context file; they do not re-decide them.

---

## Installed skills

These are installed and will auto-activate when relevant:

- `obra/superpowers` — spec → plan → implement discipline, verification-before-completion, systematic debugging, TDD, requesting/receiving code review, dispatching parallel agents, using git worktrees, finishing a development branch
- `supabase/agent-skills` — general Postgres best practices (despite the name, not Supabase-specific)

When M3 becomes active, add:
- `vercel-labs/agent-skills` — Next.js 16 / React 19 patterns
- `anthropics/skills` for `frontend-design`
- `shadcn/ui` if shadcn is used in the dashboard

When M7 becomes active, add:
- `anthropics/skills` for `mcp-builder`

Skills are lower authority than this file, the rationale, and the milestone context.

---

## Repository layout

```
garrison/
├── AGENTS.md                     ← this file
├── ARCHITECTURE.md
├── RATIONALE.md
├── README.md
├── CONTRIBUTING.md
├── CODE_OF_CONDUCT.md
├── SECURITY.md
├── CHANGELOG.md
├── LICENSE                       ← AGPL-3.0-only
├── LICENSE-DOCS                  ← CC-BY-4.0
├── .agents/                      ← Garrison-flavored slash commands (.claude → .agents)
│   └── commands/
│       ├── garrison-specify.md
│       ├── garrison-plan.md
│       ├── garrison-tasks.md
│       └── garrison-implement.md
├── specs/
│   ├── _context/                 ← filenames mix dots/hyphens (historical — do not normalise mid-milestone)
│   │   ├── m1-context.md
│   │   ├── m2.1-context.md
│   │   ├── m2.2-context.md
│   │   ├── m2-2-1-context.md
│   │   ├── m2.2.2-context.md
│   │   └── m2.3-context.md
│   ├── m1-event-bus/             ← M1 (un-numbered, predates the 00N scheme)
│   ├── 003-m2-1-claude-invocation/
│   ├── 004-m2-2-mempalace/
│   ├── 005-m2-2-1-finalize-ticket/
│   ├── 006-m2-2-2-compliance-calibration/
│   └── 007-m2-3-infisical-vault/
├── supervisor/                   ← Go binary
│   ├── cmd/supervisor/           ← main + `mcp postgres` + `mcp finalize` subcommands
│   ├── internal/
│   │   ├── claudeproto/          ← M2.1: stream-json event types + Router
│   │   ├── mcpconfig/            ← M2.1 + M2.3: per-invocation MCP config + Rule 3 pre-check
│   │   ├── pgmcp/                ← M2.1: in-tree Postgres MCP server (CallToolResult-shape after 59fc977)
│   │   ├── agents/               ← M2.1: startup-once cache
│   │   ├── spawn/                ← M2.1 + M2.2 + M2.2.1 + M2.3: subprocess pipeline, finalize write, vault orchestration
│   │   ├── mempalace/            ← M2.2: bootstrap + wake-up + Client + DockerExec seam
│   │   ├── hygiene/              ← M2.2 + M2.2.1: Evaluator + listener + sweep
│   │   ├── finalize/             ← M2.2.1 + M2.2.2: finalize_ticket MCP server + richer-error infra
│   │   ├── vault/                ← M2.3: SecretValue + Client + ScanAndRedact + audit row
│   │   ├── config/, store/, events/, pgdb/, recovery/, health/, concurrency/, testdb/
│   ├── tools/
│   │   └── vaultlog/             ← M2.3: custom go vet analyzer rejecting SecretValue logging
│   ├── go.mod
│   └── Dockerfile
├── dashboard/                    ← Next.js 16 app (M3 — active)
├── migrations/                   ← SQL, consumed by sqlc (Go) and Drizzle (TS)
│   └── seed/                     ← engineer.md, qa-engineer.md (embedded into migrations via +embed-agent-md)
├── docs/
│   ├── architecture.md           ← pointer file
│   ├── architecture-reconciliation-2026-04-24.md  ← frozen decision-provenance snapshot
│   ├── getting-started.md
│   ├── mcp-registry-candidates.md   ← M8 input: MCPJungle commitment
│   ├── skill-registry-candidates.md ← M7 input: SkillHub commitment
│   ├── ops-checklist.md          ← post-migrate and post-deploy steps
│   ├── README.md
│   ├── research/
│   │   └── m2-spike.md
│   ├── security/
│   │   └── vault-threat-model.md
│   ├── forensics/
│   │   └── pgmcp-three-bug-chain.md  ← post-M2.2.2 root-cause investigation
│   ├── issues/
│   │   ├── agent-workspace-sandboxing.md  ← Docker-per-agent fix planned post-M3
│   │   └── cost-telemetry-blind-spot.md   ← supervisor signal-handling fix
│   └── retros/
│       ├── m1.md
│       ├── m1-retro-addendum.md
│       ├── m2-1.md
│       ├── m2-2.md
│       ├── m2-2-1.md
│       ├── m2-2-2.md
│       ├── m2-2-x-compliance-retro.md     ← arc synthesis
│       └── m2-3.md
├── experiment-results/           ← exploratory matrices (e.g. matrix-post-uuid-fix.md), not production
├── examples/                     ← toy company YAML, sample agent.md files
└── .specify/                     ← spec-kit scaffolding
    ├── memory/constitution.md
    ├── scripts/
    └── templates/
```

When in doubt about where a file belongs, look at what this tree implies and follow the pattern. If the pattern doesn't cover your case, ask.

---

## What agents should not do

- Do not edit `RATIONALE.md`, `ARCHITECTURE.md`, or this file without explicit operator instruction. These are governance documents.
- Do not add a cloud dependency. Garrison is self-hosted.
- Do not introduce Node/Python tooling into the supervisor. Go only.
- Do not introduce Go tooling into the dashboard. Node/TS only.
- Do not change the event bus contract (`pg_notify` channel names, payload shapes) without updating the milestone context and noting it in the retro.
- Do not mark a task complete until you've actually verified the acceptance criterion. "Seems right" is not done.
- Do not reproduce large portions of `ARCHITECTURE.md`, `RATIONALE.md`, or spike documents in commit messages, specs, or PR descriptions. Link to them; don't paraphrase them into drift.
- Do not treat a research spike as a design phase. Spikes report observations; they do not make decisions.
- Do not run `mempalace init` against any git-tracked directory. MemPalace's `init` command auto-modifies `.gitignore` in the scanned directory and drops `mempalace.yaml` / `entities.json` into it. The palace must be bootstrapped in a dedicated path, never against the Garrison repo or supervisor workspace. See `docs/research/m2-spike.md` §3.2.
- Do not rely on Claude Code's exit code to detect MCP server failures. `--strict-mcp-config` with a broken server exits 0. The supervisor must parse the `system`/`init` event and check `mcp_servers[].status == "connected"` for each required server.
- Do not call `cmd.Wait` on a subprocess before the stdout/stderr pipe readers have completed. This is concurrency rule 8; violating it causes truncated reads on short streams.
- Do not trust `agent_instances.total_cost_usd` for clean-finalize runs. The column reads `$0.00` on every successful finalize because the supervisor signal-kills before claude's `result` event lands. Open issue tracked at `docs/issues/cost-telemetry-blind-spot.md`; failure-mode exits (`finalize_never_called`, `budget_exceeded`, `claude_error`) record cost correctly. Any cost-based SLO or per-ticket cost UI must surface this caveat until the supervisor signal-handling fix lands.
- Do not log values that touch `vault.SecretValue`. The `tools/vaultlog` go vet analyzer enforces this at build time and will reject slog/fmt/log calls with `SecretValue` arguments. If you need to log around vault code, log non-secret metadata only — secret path, role slug, outcome, audit row ID — never the value itself. Only two production call sites should reach `UnsafeBytes()`: spawn env injection and Rule 1 leak scan.

---

## When in doubt

Ask the operator. Garrison is built by a solo founder; the operator is reachable and prefers a question over wasted work. Questions that are always welcome:

- "RATIONALE and the M2.2 context seem to disagree on X — which wins?"
- "This task implies a dependency outside the locked list. Should I proceed or propose an alternative?"
- "This work crosses milestone boundaries. Am I correct that I should stop at the M2.2 boundary and surface the M2.3 implication?"
- "The spike doc says tool X behaves way Y, but my task seems to assume way Z. Which is correct?"

Questions that are not welcome:

- "Should I use `gin` instead of `chi`?" (no — stdlib + chi is the locked choice)
- "Wouldn't it be better to do X differently?" (if X is decided in RATIONALE.md, the answer is no)
- Anything that re-opens decisions from RATIONALE.md without a substantive reason the rationale didn't consider.
