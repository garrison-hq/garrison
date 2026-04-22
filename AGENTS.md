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

Garrison is being built milestone-by-milestone (M1 through M8). Each milestone ships end-to-end functional before the next begins. Current milestone: **M1 — event bus + supervisor core**.

---

## Binding documents

These documents govern this project. Read the ones relevant to your current task before generating code, specs, or plans.

| Document | Path | What it holds |
|---|---|---|
| Rationale | `RATIONALE.md` | 12 architectural decisions with alternatives considered and trade-offs accepted. Do not re-litigate decisions here. |
| Architecture | `ARCHITECTURE.md` | System components, data model, event flow, build plan, dashboard surfaces. |
| Active milestone context | `specs/_context/m1-context.md` | Binding constraints for M1. The active milestone's context file is always the most operationally relevant document for code work. |
| Constitution | `.specify/memory/constitution.md` | Spec-kit's constitution file. Not yet committed — will be populated via `/speckit.constitution`. |

When the active milestone changes (M1 → M2 → ...), the relevant context file path changes with it. Always check which milestone is active before assuming `m1-context.md` applies.

---

## Precedence when documents conflict

When two sources disagree, resolve in this order (highest authority first):

1. **`RATIONALE.md`** — design decisions are binding. If something here says "we chose X over Y," do not propose Y.
2. **Active milestone context** (`specs/_context/m{N}-context.md`) — operational constraints for current work.
3. **`ARCHITECTURE.md`** — the system's structural truth.
4. **Constitution** (`.specify/memory/constitution.md`) — spec-kit conventions.
5. **Installed skills** (under `obra/superpowers`, `supabase/agent-skills`, etc.) — general discipline.
6. **Your own defaults** — what you'd do in a generic project.

If a lower authority contradicts a higher one, the higher one wins silently. Do not stop to ask the human unless two equal-authority sources conflict with each other. If RATIONALE and the M1 context truly disagree on something, that is a bug in one of those files — stop and surface it.

---

## Activate before writing code

Before producing any code in this repository, explicitly bring relevant domain knowledge into working memory. Which domains to activate depends on which milestone is active.

### M1 — event bus + supervisor core (active)

Activate:

- **Go 1.23+ idioms**: `context.Context` threaded through every goroutine; no bare `go func()`; `errgroup.WithContext` for "run N subsystems, cancel all if one fails" at the top level; channels with explicit sender/receiver responsibility; buffered channels only with a documented reason; stdlib `log/slog` for structured logging.
- **`jackc/pgx/v5` patterns**: the LISTEN connection is dedicated (a plain `*pgx.Conn` or manually hijacked `*pgxpool.Conn`), never from the pool; reconnect with exponential backoff (100ms → 30s cap); on reconnect, run the `processed_at` fallback poll before re-LISTENing.
- **Postgres LISTEN/NOTIFY**: payload size is limited (8KB); channel names are dot-delimited (`work.ticket.created`); NOTIFY fires inside the same transaction as the state change; missed notifications are expected and handled via `processed_at` + periodic poll.
- **`sqlc`**: SQL migrations are the source of truth; query types are generated, not hand-written; both Go (`sqlc`) and TypeScript (Drizzle, later) derive from the same SQL.
- **`os/exec` with `CommandContext`**: every subprocess has a timeout context; cancellation sends SIGTERM then SIGKILL after a grace period; stdout/stderr piped to slog with structured fields.
- **`testcontainers-go`**: integration tests spin ephemeral Postgres containers; no mocking of the DB layer.

### M3 — dashboard (not yet active)

When M3 becomes active, additionally activate:
- Next.js 16 App Router patterns, React 19 (including Server Components, Server Actions where appropriate)
- Tailwind v4 and shadcn/ui conventions (matching the Obsidian Mint design language from `benchr.io` if the operator has referenced it)

### M7 — hiring (not yet active)

When M7 becomes active, additionally activate:
- MCP server authoring patterns (using `anthropics/skills/mcp-builder`)
- `skills.sh` registry semantics for discovery and install

**Do not** activate domains from future milestones. M1 sessions have no business carrying Next.js 16 context; it dilutes attention and tempts scope creep.

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
- One of `goose` or `tern` for migrations (pick one per project, stick to it)

**Soft rule on the list**: adding a dependency outside this list is allowed but must be justified in the commit message (one paragraph: what it does, why stdlib or an already-installed dep isn't enough, what the alternatives were). New dependencies must also be flagged in the milestone retro. Agents that silently add dependencies without justification are doing the wrong thing.

Do NOT reach for: `lib/pq` (pgx supersedes it), `gin`/`echo`/`fiber` (stdlib `net/http` + `chi` if routing is needed), `logrus`/`zap` (slog is stdlib), `viper` (env vars + a typed config struct).

### Deployment

Single static binary via `CGO_ENABLED=0 go build`. Dockerfile is roughly 6 lines. Runs on Hetzner + Coolify. No runtime, no venv, no multistage gymnastics.

---

## Concurrency discipline (non-negotiable)

These apply throughout the supervisor code:

1. Every goroutine accepts a `context.Context` and respects cancellation. No bare `go func()`.
2. `main` owns a root context. Subsystems get derived contexts. SIGTERM/SIGINT cancels the root.
3. Use `errgroup.WithContext` for the "run N subsystems, cancel all if one fails" pattern at the top level.
4. Every spawned subprocess uses `exec.CommandContext(ctx, ...)` with a timeout-derived context. Context cancellation kills the subprocess (SIGTERM, 5s grace, SIGKILL).
5. Channels: specify sender vs receiver responsibility; close from the sender side only; buffered channels need a documented reason.

---

## Scope discipline (the most important rule)

Specs are narrow per milestone. M1 specs cover the event bus and supervisor core only. The following are explicitly **out of scope for M1** even though they appear in `ARCHITECTURE.md`:

- Claude Code invocation (M1 uses a fake `echo`-style subprocess; real Claude Code lands in M2)
- MemPalace integration
- Web UI
- Workflow engine beyond a minimal ticket state machine
- Hiring flow
- CEO agent
- skills.sh integration

When tempted to broaden scope because "we'll need it later," stop. Note it as an open question in the spec or the retro. Do not implement it.

Milestones ship end-to-end functional. If a task list produces scaffolding that can't be exercised against real-world use, the task list is wrong.

---

## Retros

At the end of each milestone, write a retro capturing what worked, what surprised you, and what changed vs. the spec.

- **M1 retro**: `docs/retros/m1.md`. Plain markdown. MemPalace is not wired up yet; the retro moves to MemPalace (`wing_company / hall_events`) when M2 delivers the integration.
- **M2 onwards**: retros go to MemPalace directly, with a pointer from `docs/retros/m{N}.md`.

The retro includes:
- What shipped
- What the spec got wrong
- Dependencies added outside the locked list (with justifications)
- Open questions deferred to the next milestone

---

## Spec-kit workflow

This repo uses GitHub Spec Kit (`specify-cli`). The canonical workflow:

1. `/speckit.constitution` — populate `.specify/memory/constitution.md` (first milestone only; reused thereafter)
2. `/speckit.specify` — draft the milestone spec
3. `/speckit.clarify` — resolve ambiguities before planning
4. `/speckit.plan` — produce the implementation plan
5. `/speckit.tasks` — break the plan into tasks
6. `/speckit.analyze` — cross-artifact consistency check before implementing
7. `/speckit.implement` — execute the tasks

Each command loads the active milestone's context file (`specs/_context/m{N}-context.md`) as binding input. Generated specs inherit constraints from the context file; they do not re-decide them.

---

## Installed skills

These are installed and will auto-activate when relevant:

- `obra/superpowers` — spec → plan → implement discipline, verification-before-completion, systematic debugging, TDD, requesting/receiving code review, dispatching parallel agents, using git worktrees, finishing a development branch
- `supabase/agent-skills` — general Postgres best practices (despite the name, not Supabase-specific)

Skills are lower authority than this file, the rationale, and the milestone context. If a skill suggests something that contradicts higher-authority sources, follow the higher authority and note the conflict in your commit message so the skill list can be revised.

---

## Repository layout (target)

The repository is a monorepo. Target structure (not yet fully realized — M1 will populate the supervisor and migrations directories):

```
garrison/
├── AGENTS.md                     ← this file
├── ARCHITECTURE.md
├── RATIONALE.md
├── README.md
├── specs/
│   ├── _context/
│   │   └── m1-context.md         ← active milestone context
│   └── m1-event-bus/             ← populated by /speckit.specify
├── supervisor/                   ← Go binary (M1)
│   ├── cmd/supervisor/
│   ├── internal/
│   ├── go.mod
│   └── Dockerfile
├── dashboard/                    ← Next.js 16 app (M3)
├── migrations/                   ← SQL, consumed by sqlc (Go) and Drizzle (TS)
├── docs/
│   └── retros/
│       └── m1.md                 ← milestone retro
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
- Do not reproduce large portions of `ARCHITECTURE.md` or `RATIONALE.md` in commit messages, specs, or PR descriptions. Link to them; don't paraphrase them into drift.

---

## When in doubt

Ask the operator. Garrison is built by a solo founder; the operator is reachable and prefers a question over wasted work. Questions that are always welcome:

- "RATIONALE and the M1 context seem to disagree on X — which wins?"
- "This task implies a dependency outside the locked list. Should I proceed or propose an alternative?"
- "This work crosses milestone boundaries. Am I correct that I should stop at the M1 boundary and surface the M2 implication?"

Questions that are not welcome:

- "Should I use `gin` instead of `chi`?" (no — stdlib + chi is the locked choice)
- "Wouldn't it be better to do X differently?" (if X is decided in RATIONALE.md, the answer is no)
- Anything that re-opens decisions from RATIONALE.md without a substantive reason the rationale didn't consider.
