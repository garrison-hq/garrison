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

Garrison is being built milestone-by-milestone (M1 through M9). Each milestone ships end-to-end functional before the next begins.

- **M1** — event bus + supervisor core. Shipped 2026-04-22.
- **M2** — first real agent loop, shipped 2026-04-22 → 2026-04-24 across five sub-milestones (M2.1 Claude Code invocation, M2.2 MemPalace MCP wiring, M2.2.1 `finalize_ticket`, M2.2.2 compliance calibration, M2.3 Infisical vault).
- **M3** — operator dashboard, read-only. Shipped 2026-04-26.
- **M4** — operator dashboard mutations. Shipped 2026-04-27.
- **M5.1** — CEO chat backend. Shipped 2026-04-28.
- **M5.2** — CEO chat dashboard surface. Shipped 2026-04-29.
- **M5.3** — chat-driven mutations under autonomous-execution posture. **Active.**
- **M6** through **M9** — see `ARCHITECTURE.md`.

Current milestone: **M5.3 — chat-driven mutations**.

Per-milestone domain knowledge (the "activate before writing code" detail) lives in [`docs/agents/milestone-context.md`](docs/agents/milestone-context.md) so it's not in every-session context.

---

## Binding documents

These documents govern this project. Read the ones relevant to your current task before generating code, specs, or plans.

| Document | Path | What it holds |
|---|---|---|
| Rationale | `RATIONALE.md` | 13 architectural decisions with alternatives considered and trade-offs accepted. Do not re-litigate decisions here. |
| Architecture | `ARCHITECTURE.md` | System components, data model, event flow, build plan, dashboard surfaces. |
| Active milestone context | `specs/_context/m{N}-context.md` (active: M5.3 — `specs/_context/m5-3-context.md`) | Binding constraints for the active milestone. The active milestone's context file is always the most operationally relevant document for code work. |
| Per-milestone agent context | `docs/agents/milestone-context.md` | The "activate before writing code" detail per milestone (M1 → M5.3). Read the entries for the current milestone plus all prior milestones whose code remains in the codebase. |
| Repository layout | `docs/agents/repository-layout.md` | Annotated file tree. Read on first session in the repo or when uncertain where a file belongs. |
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

Before producing any code in this repository, explicitly bring relevant domain knowledge into working memory. The per-milestone "activate" lists live in [`docs/agents/milestone-context.md`](docs/agents/milestone-context.md) so they're loaded on demand, not in every session.

The activation rule: read the entries for the **current milestone plus all prior milestones whose code remains in the codebase**. Future-milestone entries are listed only so the operator can plan ahead — do not pre-activate them. M5.3 sessions have no business carrying M7 hiring-flow or M8 MCP-registry context; it dilutes attention and tempts scope creep.

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

Specs are narrow per milestone. Each milestone's spec covers only that milestone's concerns. The active milestone's `specs/_context/m{N}-context.md` enumerates what is in scope; ARCHITECTURE.md and `docs/issues/` enumerate work that is intentionally deferred.

Standing out-of-scope for any non-named milestone:
- **Workspace sandboxing / Docker-per-agent** — see `docs/issues/agent-workspace-sandboxing.md`. Per-agent Docker containers with hard guardrails preventing workspace escape; deferred post-M5.
- **Cost-telemetry blind-spot fix** — see `docs/issues/cost-telemetry-blind-spot.md`. Successful finalize runs read `$0.00` for `total_cost_usd`; surface the caveat in any cost UI but do not fix the supervisor signal-handling here.
- **Mutating sealed M2/M2.3 surfaces** — supervisor spawn semantics, finalize tool schema, vault rules, `garrison_agent_*` Postgres roles, MemPalace MCP wiring. These are sealed unless the active milestone's context file explicitly amends them.
- Future-milestone surfaces — M5.3 sessions don't carry M7 hiring or M8 MCP-registry work; M7 sessions don't carry M8 work; etc.

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

**Step 0 (precondition, non-optional)**: create the milestone feature branch via `/speckit-git-feature` (or `git checkout -b NNN-mN-name` directly — e.g. `008-m3-dashboard`). Every milestone gets its own branch; the convention is one PR per milestone, merged to `main` before the next milestone's branch is cut. Made explicit here because M2.3's work landed on M2.2.2's branch when this step was implicit — the `007-m2-3-infisical-vault` branch was never created and the convention drifted. Do not repeat that pattern.

Then:

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
- Dashboard-relevant skills (Next.js 16 / React 19 patterns, `frontend-design`, shadcn/ui) — added when M3 became active; remain available for M3+ work.

Add when M7 becomes active:
- `anthropics/skills` for `mcp-builder`.

Skills are lower authority than this file, the rationale, and the milestone context.

---

## Repository layout

The annotated file tree lives at [`docs/agents/repository-layout.md`](docs/agents/repository-layout.md). Read it on first session in the repo or when uncertain where a file belongs. Top-level shape:

- `supervisor/` — Go binary. `cmd/supervisor/` (main + MCP subcommands), `internal/` (per-domain packages), `tools/vaultlog/` (custom go vet analyzer).
- `dashboard/` — Next.js 16 app. M3/M4/M5.1/M5.2 surfaces.
- `migrations/` — SQL, consumed by sqlc (Go) and Drizzle (TS).
- `specs/` — per-milestone spec-kit artifacts; `specs/_context/m{N}-context.md` is binding for the active milestone.
- `docs/` — non-spec documentation: `architecture.md`, `agents/` (this file's reference docs), `research/` (spikes), `security/` (threat models), `retros/`, `issues/`, `ops-checklist.md`.
- `.agents/` / `.claude/` — Garrison-flavoured slash commands and skills.
- `.specify/` — spec-kit scaffolding (constitution, scripts, templates).

When in doubt about where a file belongs, look at what the tree implies and follow the pattern. If the pattern doesn't cover your case, ask.

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
