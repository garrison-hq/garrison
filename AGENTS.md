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
- **M2** — first real agent loop. Split into two sub-milestones, each with its own spec and retro:
  - **M2.1** — Claude Code invocation (spike-first)
  - **M2.2** — MemPalace MCP wiring (spike-first)
- **M3** through **M8** — see `ARCHITECTURE.md`.

Current milestone: **M2.1 — Claude Code invocation** (spike complete at `docs/research/m2-spike.md`; context file pending).

---

## Binding documents

These documents govern this project. Read the ones relevant to your current task before generating code, specs, or plans.

| Document | Path | What it holds |
|---|---|---|
| Rationale | `RATIONALE.md` | 13 architectural decisions with alternatives considered and trade-offs accepted. Do not re-litigate decisions here. |
| Architecture | `ARCHITECTURE.md` | System components, data model, event flow, build plan, dashboard surfaces. |
| Active milestone context | `specs/_context/m{N}-context.md` (active: `m2-1-context.md` when written) | Binding constraints for the active milestone. The active milestone's context file is always the most operationally relevant document for code work. |
| Constitution | `.specify/memory/constitution.md` | Spec-kit's constitution file. Populated via `/speckit.constitution`. |
| Research spikes | `docs/research/m{N}-spike.md` | Observed behavior of external tools that a milestone depends on. Binding input to the milestone's context file. |
| Retros | `docs/retros/m{N}.md` (M1), MemPalace `wing_company / hall_events` (M2.2 onwards) | Post-ship findings. |

When the active milestone changes (M1 → M2.1 → M2.2 → M3 → …), the relevant context file path changes with it. Always check which milestone is active before assuming a particular context file applies.

---

## Precedence when documents conflict

When two sources disagree, resolve in this order (highest authority first):

1. **`RATIONALE.md`** — design decisions are binding. If something here says "we chose X over Y," do not propose Y.
2. **Active milestone context** (`specs/_context/m{N}-context.md`) — operational constraints for current work.
3. **`ARCHITECTURE.md`** — the system's structural truth.
4. **Constitution** (`.specify/memory/constitution.md`) — spec-kit conventions.
5. **Installed skills** (under `obra/superpowers`, `supabase/agent-skills`, etc.) — general discipline.
6. **Your own defaults** — what you'd do in a generic project.

If a lower authority contradicts a higher one, the higher one wins. Flag the contradiction in your commit message or PR description so the lower-authority document can be updated. Do not stop to ask the human unless two equal-authority sources conflict with each other. If RATIONALE and the active milestone context truly disagree on something, that is a bug in one of those files — stop and surface it.

---

## Activate before writing code

Before producing any code in this repository, explicitly bring relevant domain knowledge into working memory. Which domains to activate depends on which milestone is active. Activate the current milestone's domains plus all prior milestones' domains that remain in the codebase.

### M1 — event bus + supervisor core (shipped; domains remain active for maintenance)

- **Go 1.23+ idioms**: `context.Context` threaded through every goroutine; no bare `go func()`; `errgroup.WithContext` for "run N subsystems, cancel all if one fails" at the top level; channels with explicit sender/receiver responsibility; buffered channels only with a documented reason; stdlib `log/slog` for structured logging.
- **`jackc/pgx/v5` patterns**: the LISTEN connection is dedicated (a plain `*pgx.Conn` or manually hijacked `*pgxpool.Conn`), never from the pool; reconnect with exponential backoff (100ms → 30s cap); on reconnect, run the `processed_at` fallback poll before re-LISTENing.
- **Postgres LISTEN/NOTIFY**: payload size is limited (8KB); channel names are dot-delimited (`work.ticket.created`); NOTIFY fires inside the same transaction as the state change; missed notifications are expected and handled via `processed_at` + periodic poll.
- **`sqlc`**: SQL migrations are the source of truth; query types are generated, not hand-written; both Go (`sqlc`) and TypeScript (Drizzle, later) derive from the same SQL.
- **`os/exec` with `CommandContext`**: every subprocess has a timeout context; cancellation sends SIGTERM then SIGKILL after a grace period; stdout/stderr piped to slog with structured fields.
- **`testcontainers-go`**: integration tests spin ephemeral Postgres containers; no mocking of the DB layer.

### M2.1 — Claude Code invocation (active)

When working on M2.1, additionally activate:

- **Claude Code non-interactive invocation contract**: exact flags, input format, stdout/stderr shape, exit code vocabulary. The specifics come from `docs/research/m2-spike.md` (Part 1), which is the binding source. Do not activate assumptions — activate the spike's findings.
- **stream-json event routing**: every invocation uses `--output-format stream-json --verbose`. The first event is `system`/`init`; subsequent events are `assistant`, `user` (tool_result), `rate_limit_event`, and a terminal `result`. The supervisor parses NDJSON line-by-line and routes by `type`.
- **MCP health via init event**: the supervisor does NOT pre-spawn MCP servers for health checks. It parses the `mcp_servers[]` array in the `system`/`init` event. A server with `status == "connected"` is healthy; anything else (`failed`, `needs-auth`, or an unknown value) means the supervisor kills the Claude process group immediately and records the agent_instance as failed with `exit_reason = "mcp_<server>_<status>"`. Unknown status values are treated as failures (fail-closed). This rule comes from the M2 spike §2.5 and §A.
- **Subprocess log parsing under structured output**: Claude's stdout is NDJSON, not log lines. The M1 per-stream line-scanning goroutines are reused, but each line is parsed as JSON and routed rather than emitted verbatim to slog.

### M2.2 — MemPalace MCP wiring

When M2.2 becomes active, additionally activate:
- **MemPalace MCP server lifecycle and concurrent access semantics**. From `docs/research/m2-spike.md` (Part 2).
- **Diary entry and KG triple write contracts** as described in `ARCHITECTURE.md` "MemPalace write contract" section.
- **Hygiene check query patterns** — post-transition async checks for expected writes.

### M3 — dashboard

When M3 becomes active, additionally activate:
- Next.js 16 App Router patterns, React 19 (including Server Components, Server Actions where appropriate)
- Tailwind v4 and shadcn/ui conventions

### M7 — hiring

When M7 becomes active, additionally activate:
- MCP server authoring patterns (using `anthropics/skills/mcp-builder`)
- `skills.sh` registry semantics for discovery and install

**Do not** activate domains from future milestones. M2.1 sessions have no business carrying Next.js 16 context; it dilutes attention and tempts scope creep.

---

## Research spikes

Some milestones begin with a research spike before speccing. This applies when the milestone depends on how an external tool actually behaves (not how its documentation says it behaves). See `RATIONALE.md` §13 for the full rule.

**Workflow**:

1. Spike runs in a scratch directory outside the Garrison repo (e.g. `~/scratch/m{N}-spike/`). The scratch dir is throwaway — nothing from it is copied back into Garrison except the findings document.
2. Spike produces a single file at `docs/research/m{N}-spike.md` with these sections: Environment, findings (one subsection per investigated question), Surprises, Open questions, Implications for the upcoming milestone(s).
3. Spike is time-boxed (typically 2-4 hours). If the time budget is exhausted, stop and write up what was found. Partial findings are more valuable than complete speculation.
4. The spike document becomes binding input to the milestone's context file. Context files reference the spike by path; they do not paraphrase or duplicate it.

**What a spike is NOT**:
- Not a prototype. Prototypes accrete into production; spikes are explicitly throwaway.
- Not a design document. Spikes report observations; context files and specs encode decisions.
- Not a replacement for speccing. Spikes inform specs; they don't replace them.

**When NOT to spike**:
- When the milestone is built on well-characterized primitives the operator and installed skills already have ground truth for.
- When the cost of speccing wrong is low (e.g. UI layout that's easy to iterate).
- Retroactively. Decisions settled in earlier milestones are not re-spiked; they are reviewed in retros and fixed in forward work.

The `docs/research/` directory is under CC-BY-4.0 along with the rest of the docs tree and the specs.

---

## Stack and dependency rules

### Supervisor (Go)

Locked dependency list:

- `github.com/jackc/pgx/v5` — Postgres driver and LISTEN/NOTIFY
- `sqlc` (build-time) — typed query generation from SQL
- `log/slog` (stdlib) — structured logging
- `golang.org/x/sync/errgroup` — concurrent subsystem lifecycle
- `github.com/stretchr/testify` — test assertions
- `github.com/testcontainers/testcontainers-go` — integration tests
- `github.com/pressly/goose/v3` — migrations (chosen in M1; stick to it)
- `github.com/google/shlex` — POSIX-like argv splitter for subprocess command templates (accepted in M1; see retro for justification)

**Soft rule on the list**: adding a dependency outside this list is allowed but must be justified in the commit message (one paragraph: what it does, why stdlib or an already-installed dep isn't enough, what the alternatives were). New dependencies must also be flagged in the milestone retro. Agents that silently add dependencies without justification are doing the wrong thing.

Do NOT reach for: `lib/pq` (pgx supersedes it), `gin`/`echo`/`fiber` (stdlib `net/http` + `chi` if routing is needed), `logrus`/`zap` (slog is stdlib), `viper` (env vars + a typed config struct).

### Deployment

Single static binary via `CGO_ENABLED=0 go build`. Dockerfile is roughly 10 lines; the M1 retro documents why the current base is `alpine:3.20` rather than distroless (the fake agent uses `sh -c`; real Claude Code in M2.1 is expected to trigger a base image review). Runs on Hetzner + Coolify. No runtime, no venv, no multistage gymnastics beyond alpine + ca-certificates.

---

## Concurrency discipline (non-negotiable)

These apply throughout the supervisor code:

1. Every goroutine accepts a `context.Context` and respects cancellation. No bare `go func()`.
2. `main` owns a root context. Subsystems get derived contexts. SIGTERM/SIGINT cancels the root.
3. Use `errgroup.WithContext` for the "run N subsystems, cancel all if one fails" pattern at the top level.
4. Every spawned subprocess uses `exec.CommandContext(ctx, ...)` with a timeout-derived context. Context cancellation kills the subprocess (SIGTERM, 5s grace, SIGKILL).
5. Channels: specify sender vs receiver responsibility; close from the sender side only; buffered channels need a documented reason.
6. Terminal writes during graceful shutdown use `context.WithoutCancel(ctx)` plus a `TerminalWriteGrace` timeout, not the already-cancelled root context. See M1 retro §2 for the bug that established this rule.
7. **Spawned subprocesses that may themselves spawn children (Claude Code from M2.1 onwards) run in their own process group.** Set `SysProcAttr.Setpgid = true` on the `exec.Cmd`, and when terminating, signal the group with `syscall.Kill(-pgid, SIGTERM)` / `SIGKILL` — never just the PID. PID-level signals allow children to survive past a supposedly-killed parent. See M1 retro addendum and `docs/research/m2-spike.md` §2.7.

---

## Scope discipline (the most important rule)

Specs are narrow per milestone. Each milestone's spec covers only that milestone's concerns. The following are explicitly **out of scope for M2.1** even though they appear in `ARCHITECTURE.md`:

- MemPalace integration (that's M2.2)
- Web UI / dashboard (that's M3)
- CEO agent (M5)
- Hiring flow (M7)
- skills.sh integration (M7)
- Multi-channel event dispatch beyond `work.ticket.created`
- Hygiene dashboard (M6)

When tempted to broaden scope because "we'll need it later," stop. Note it as an open question in the spec or the retro. Do not implement it.

Milestones ship end-to-end functional. If a task list produces scaffolding that can't be exercised against real-world use, the task list is wrong.

---

## Retros

At the end of each milestone, write a retro capturing what worked, what surprised you, and what changed vs. the spec.

- **M1 retro**: `docs/retros/m1.md`. Plain markdown. Written before MemPalace existed. An addendum at the bottom captures findings from the M2 spike that changed how M2.1 approaches subprocess termination.
- **M2.1 retro**: `docs/retros/m2-1.md`. Plain markdown. MemPalace is not yet wired up as of M2.1 (that's the point of M2.2), so this retro cannot use MemPalace as its store.
- **M2.2 onwards**: retros go to MemPalace directly (`wing_company / hall_events`), with a pointer at `docs/retros/m{N}.md` that links to the palace entry. This is the first milestone where the system dogfoods its own memory contract.

The retro includes:
- What shipped
- What the spec got wrong
- Dependencies added outside the locked list (with justifications)
- Open questions deferred to the next milestone
- For spike-first milestones: whether the spike paid off (was it worth the 2-4 hours?)

---

## Spec-kit workflow

This repo uses GitHub Spec Kit (`specify-cli`). The canonical workflow per milestone:

1. If the milestone requires a spike (RATIONALE §13): run the spike, produce `docs/research/m{N}-spike.md`, then continue.
2. Write `specs/_context/m{N}-context.md` using the spike findings (if any) plus the architecture doc as binding inputs.
3. `/speckit.constitution` — populate `.specify/memory/constitution.md` (first milestone only; reused thereafter; amend only when RATIONALE amends)
4. `/speckit.specify` — draft the milestone spec
5. `/speckit.clarify` — resolve ambiguities before planning
6. `/speckit.plan` — produce the implementation plan
7. `/speckit.tasks` — break the plan into tasks
8. `/speckit.analyze` — cross-artifact consistency check before implementing
9. `/speckit.implement` — execute the tasks
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

Skills are lower authority than this file, the rationale, and the milestone context. If a skill suggests something that contradicts higher-authority sources, follow the higher authority and note the conflict in your commit message so the skill list can be revised.

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
├── specs/
│   ├── _context/
│   │   ├── m1-context.md
│   │   ├── m2-1-context.md       ← to be written next
│   │   └── m2-2-context.md       ← to be written next
│   ├── 001-m1-event-bus/
│   ├── 002-m2-1-claude-code/     ← future
│   └── 003-m2-2-mempalace/       ← future
├── supervisor/                   ← Go binary
│   ├── cmd/supervisor/
│   ├── internal/
│   ├── go.mod
│   └── Dockerfile
├── dashboard/                    ← Next.js 16 app (M3)
├── migrations/                   ← SQL, consumed by sqlc (Go) and Drizzle (TS)
├── docs/
│   ├── research/
│   │   └── m2-spike.md           ← M2.1 and M2.2 binding input
│   └── retros/
│       ├── m1.md
│       └── m2-1.md               ← future
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
- Do not add a cloud dependency. Garrison is self-hosted on Hetzner.
- Do not introduce Node/Python tooling into the supervisor. Go only.
- Do not introduce Go tooling into the dashboard. Node/TS only.
- Do not change the event bus contract (`pg_notify` channel names, payload shapes) without updating the milestone context and noting it in the retro.
- Do not mark a task complete until you've actually verified the acceptance criterion. "Seems right" is not done (see `obra/superpowers/verification-before-completion`).
- Do not reproduce large portions of `ARCHITECTURE.md`, `RATIONALE.md`, or spike documents in commit messages, specs, or PR descriptions. Link to them; don't paraphrase them into drift.
- Do not treat a research spike as a design phase. Spikes report observations; they do not make decisions.
- **Do not run `mempalace init` against any git-tracked directory.** MemPalace's `init` command auto-modifies `.gitignore` in the scanned directory and drops `mempalace.yaml` / `entities.json` into it. The palace must be bootstrapped in a dedicated path (`~/.garrison/palace/` or an operator-configured path), never against the Garrison repo or the supervisor workspace. See `docs/research/m2-spike.md` §3.2.
- **Do not rely on Claude Code's exit code to detect MCP server failures.** `--strict-mcp-config` with a broken server exits 0. The supervisor must parse the `system`/`init` event and check `mcp_servers[].status == "connected"` for each required server. See `docs/research/m2-spike.md` §2.5 and §A.

---

## When in doubt

Ask the operator. Garrison is built by a solo founder; the operator is reachable and prefers a question over wasted work. Questions that are always welcome:

- "RATIONALE and the M2.1 context seem to disagree on X — which wins?"
- "This task implies a dependency outside the locked list. Should I proceed or propose an alternative?"
- "This work crosses milestone boundaries. Am I correct that I should stop at the M2.1 boundary and surface the M2.2 implication?"
- "The spike doc says tool X behaves way Y, but my task seems to assume way Z. Which is correct?"

Questions that are not welcome:

- "Should I use `gin` instead of `chi`?" (no — stdlib + chi is the locked choice)
- "Wouldn't it be better to do X differently?" (if X is decided in RATIONALE.md, the answer is no)
- Anything that re-opens decisions from RATIONALE.md without a substantive reason the rationale didn't consider.