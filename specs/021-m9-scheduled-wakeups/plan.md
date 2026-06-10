# Implementation Plan: M9 — Scheduled / triggered wake-ups (heartbeat)

**Branch**: `021-m9-scheduled-wakeups` | **Date**: 2026-06-10 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification at `specs/021-m9-scheduled-wakeups/spec.md`
**Binding context**: [`specs/_context/m9-context.md`](../_context/m9-context.md), [`AGENTS.md`](../../AGENTS.md) §§"Activate before writing code" + "Concurrency discipline" + "Stack and dependency rules", [`RATIONALE.md`](../../RATIONALE.md) §1, [`ARCHITECTURE.md`](../../ARCHITECTURE.md) §M9, [`docs/security/chat-threat-model.md`](../../docs/security/chat-threat-model.md), [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md). The shipped M1 + M2.x + M3 + M4 + M5.x + M6 + M7 + M7.1 + M8 supervisor + dashboard is the foundation this plan extends; the M6 + M8 retros are prerequisite reading. No spike (see "No spike" in m9-context.md).

## Summary

M9 adds the proactive axis: a supervisor-internal tick loop claims due `scheduled_tasks` rows (`FOR UPDATE SKIP LOCKED`), fires each as either a ticket insert (normal Kanban flow) or an oneshot direct spawn exiting through a new `finalize_oneshot` MCP tool, with fire-on-recovery-collapse and no backfill. Every firing attempt produces a `scheduled_task_runs` record. The dashboard gains `/admin/recurring-jobs` with full CRUD; chat gains exactly one new verb, `create_scheduled_task` (Tier 3, per-turn ceiling), with the chat-threat-model amendment landing before verb code.

The firing path deliberately rides the M1 reactive shape: the tick transaction is short (claim + advance + run record + ticket-or-event insert + notify); spawns happen reactively via the existing dispatcher, never inside the tick transaction. Zero new Go or TypeScript dependencies.

## Technical context

**Language/Version**: Go 1.23+ (supervisor); TypeScript 5.x + React 19 (dashboard) — unchanged from M8.

**Primary Dependencies** (Go, locked per AGENTS.md): unchanged from M8. **Zero new Go deps** — the schedule-expression grammar is bounded specifically so stdlib (`time`, `strings`, `fmt`) parses it (FR-103; decision 3 below).

**Primary Dependencies** (TypeScript): unchanged. **Zero new TS deps.**

**Storage**: PostgreSQL 17 (garrison-postgres, shared). Two new tables (`scheduled_tasks`, `scheduled_task_runs`); one relaxation on `agent_instances`; CHECK extensions on `chat_mutation_audit`.

**Testing**: Go `testing` + testcontainers-go; tags default / `-tags=integration` / `-tags=chaos`. Go-side only per repo convention.

**Target Platform**: unchanged deployment topology — no new containers, no compose changes. The tick loop is a goroutine in the existing supervisor binary.

**Performance Goals**: tick query cost is one indexed SELECT per tick (default 30s) — negligible. Firing adds the same per-spawn envelope as reactive spawns. Zero token cost at idle (SC-008).

**Constraints**: M7.1 ships first (spawn transport is container exec; this plan touches no transport code). `finalize_ticket` byte-for-byte untouched (FR-302). Threat-model amendment precedes verb code (FR-601).

**Scale/Scope**: single-tenant alpha. `scheduled_tasks` keys to departments → companies, so per-customer scoping is not foreclosed; no multi-tenant machinery.

## Constitution check

| Constitution principle | M9 disposition |
|---|---|
| I. Postgres + pg_notify event bus | Pass — the tick loop is SQL-only and feeds the existing bus: ticket-mode firings emit the existing `work.ticket.created.*` events; oneshot firings emit a new `work.scheduled.oneshot_due` channel consumed by the M1 dispatcher. RATIONALE §1's "no scheduled heartbeats" rejected per-agent token-burning wake cycles; the tick is the same shape as M1's `processed_at IS NULL` poll fallback (zero idle token cost) and ARCHITECTURE §M9 commits it explicitly. |
| II. MemPalace as memory | Pass — oneshot finalize commits diary + KG triples through the same supervisor-side write path as `finalize_ticket`. |
| III. Agents are ephemeral | Pass — scheduled spawns are ordinary ephemeral spawns with a different origin. No daemon, no pool. |
| IV. Soft gates on hygiene | Pass — oneshot verification results land on the run record for operator review; nothing blocks on thin writes. |
| V/VI. Skills + hiring | N/A — untouched. |
| VII. Go supervisor, locked deps | Pass — zero new deps; grammar bounded to stay stdlib-parseable. |
| VIII. Goroutines accept context | Pass — `schedule.RunLoop(ctx, deps)` is ticker + `ctx.Done()` select, errgroup-managed, mirroring `events.Run`. |
| IX. Narrow spec, end-to-end ship | Pass — both firing modes exercisable live at ship. |
| X. Per-department caps | Pass — oneshot spawns transit `concurrency.CheckCap` in `prepareSpawnOneshot` exactly like ticket spawns. |
| Concurrency rule 7/8 (process groups, pipe drain) | N/A — no new subprocess mechanics; oneshot reuses the existing pipeline/transport. |

## Project structure

### Documentation (this feature)

```text
specs/021-m9-scheduled-wakeups/
├── spec.md              # committed
├── plan.md              # this file
└── tasks.md             # /garrison-tasks output (NOT created here)
```

### Source code (repository root)

```text
supervisor/
├── cmd/supervisor/
│   ├── main.go                                   # EXTENDED — wire schedule.RunLoop + oneshot dispatcher channel + config passthrough
│   └── mcp_finalize.go                           # EXTENDED — read GARRISON_FINALIZE_MODE + GARRISON_SCHEDULED_RUN_ID envs
├── internal/
│   ├── schedule/                                 # NEW — the M9 package
│   │   ├── expr.go                               # grammar: Parse, Expr, Next(after time.Time)
│   │   ├── expr_test.go
│   │   ├── template.go                           # RenderTemplate (two-variable substitution)
│   │   ├── template_test.go
│   │   ├── tick.go                               # RunLoop + tickOnce + claim/fire transaction
│   │   ├── tick_test.go
│   │   ├── fire.go                               # fireTicketMode / fireOneshotMode / overlap predicates
│   │   ├── fire_test.go
│   │   ├── validate.go                           # ValidateTask (grammar + min-interval + future-slot + dept/role existence)
│   │   ├── validate_test.go
│   │   └── deps.go                               # Deps struct + run-outcome constants
│   ├── spawn/
│   │   ├── oneshot.go                            # NEW — SpawnOneshot + prepareSpawnOneshot + WriteFinalizeOneshot
│   │   ├── oneshot_test.go                       # NEW
│   │   └── spawn.go                              # EXTENDED — mcpconfig finalize-mode env plumbing (small seam only)
│   ├── finalize/
│   │   ├── server.go                             # EXTENDED — Mode in Deps; tools/list returns exactly one tool per mode
│   │   ├── tool.go                               # EXTENDED — OneshotPayload (FinalizePayload minus TicketID)
│   │   ├── handler.go                            # EXTENDED — oneshot Handle path (run-id keyed already-committed check)
│   │   └── *_test.go                             # EXTENDED — mode-switch + oneshot payload tests
│   ├── garrisonmutate/
│   │   ├── verbs.go                              # EXTENDED — create_scheduled_task as 11th Verbs entry (Tier 3)
│   │   ├── verbs_scheduled.go                    # NEW — handler + arg validation
│   │   ├── verbs_scheduled_test.go               # NEW
│   │   └── server_action_verbs.go                # EXTENDED — edit/pause/resume/delete_scheduled_task entries
│   ├── chat/
│   │   ├── policy.go                             # EXTENDED — MaxScheduledTasksPerTurn ceiling (MaxTicketsPerTurn mirror)
│   │   └── policy_test.go                        # EXTENDED
│   ├── dashboardapi/
│   │   ├── schedule_handler.go                   # NEW — POST /schedule/validate
│   │   └── schedule_handler_test.go              # NEW
│   ├── config/
│   │   ├── config.go                             # EXTENDED — three GARRISON_SCHED_* / ceiling env vars
│   │   └── config_test.go                        # EXTENDED
│   └── store/                                    # REGENERATED — sqlc output for m9_schedule.sql
│
migrations/
└── 20260610000000_m9_scheduled_wakeups.sql       # NEW — single migration, all M9 schema deltas

migrations/queries/
└── m9_schedule.sql                               # NEW — all M9 sqlc queries

dashboard/
├── app/[locale]/(app)/admin/recurring-jobs/
│   ├── page.tsx                                  # NEW — list + create form
│   └── [id]/page.tsx                             # NEW — detail + edit/pause/resume/delete + run history
├── lib/queries/scheduledTasks.ts                 # NEW
├── lib/actions/scheduledTasks.ts                 # NEW — five Server Actions
├── components/features/scheduled-tasks/          # NEW — CreateTaskForm, TaskRow, RunHistoryTable, TaskDetailControls
├── components/layout/Sidebar.tsx                 # EXTENDED — NavSubLink under admin group
└── drizzle/schema.supervisor.ts                  # REGENERATED via bun run drizzle:pull

docs/
├── security/chat-threat-model.md                 # AMENDED pre-implementation (FR-601)
├── ops-checklist.md                              # EXTENDED — M9 env vars + first-task walkthrough
└── retros/m9.md                                  # NEW (retro phase)
AGENTS.md                                          # AMENDED pre-implementation — finalize sealed-surface note (sibling tool)
ARCHITECTURE.md                                    # EXTENDED at ship (M9 paragraph annotated Shipped)
```

**Structure decision**: one new Go package (`internal/schedule`), one new spawn file (`oneshot.go`), extensions to five existing packages (`finalize`, `garrisonmutate`, `chat`, `dashboardapi`, `config`), one migration, one sqlc query file, one new dashboard route pair. No new binaries, containers, or dependencies.

## Decisions baked into this plan

(Operator-approved slate, 2026-06-10.)

1. `internal/schedule` is the only new package; it owns grammar, templates, validation, tick loop, and firing.
2. Firing rides the M1 reactive shape: short tick tx (claim + advance + run record + ticket-or-event + notify); oneshot spawns happen via dispatcher → `spawn.SpawnOneshot`, never inside the tick tx.
3. Grammar (exact): `daily@HH:MM`, `weekly@{mon|tue|wed|thu|fri|sat|sun}@HH:MM`, `every@<N>{m|h}`. UTC. Stdlib parsing.
4. `scheduled_tasks` columns per §Data model; min-interval enforced in Go validation only. Deletion is a **soft delete** (`deleted_at`; FR-502 — run history is immutable, no cascade); name uniqueness via partial unique index over live rows only.
5. `scheduled_task_runs` with outcome CHECK (`fired`,`skipped_overlap`,`gate_deferred`,`failed`) + `structured_outcome` JSONB; oneshot completion reads through joined `agent_instances.status`. `gate_deferred` is non-terminal for oneshot (poll-retry may clear it to `fired`, FR-401) and terminal for ticket mode.
6. `agent_instances.ticket_id` DROP NOT NULL + `scheduled_task_run_id` NULL FK + exactly-one-of CHECK.
7. `chat_mutation_audit` CHECKs extended in the migration: five verbs, `scheduled_task` resource type, `scheduled_task_creation_ceiling_reached` outcome.
8. `finalize_oneshot` lives in `internal/finalize` behind a mode switch (`GARRISON_FINALIZE_MODE` + `GARRISON_SCHEDULED_RUN_ID`); `tools/list` returns exactly one tool per mode (FR-304 satisfied structurally).
9. Chat verb `create_scheduled_task` (11th `Verbs` entry, Tier 3) + `MaxScheduledTasksPerTurn` ceiling mirroring the M6 mechanism.
10. Expression validation single-sources in Go: dashboard Server Actions call `POST /schedule/validate` on the M5.4 `dashboardapi` server for grammar + next-fire computation; no TS date-math mirror.
11. Dashboard CRUD per the M4/M7 precedent: drizzle writes + audit rows written dashboard-side in the same drizzle tx; verb names from `ServerActionVerbs`.
12. Env vars: `GARRISON_SCHED_TICK_INTERVAL` (30s), `GARRISON_SCHED_MIN_INTERVAL` (15m), `GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN` (3).
13. Validation rejects reuse `validation_failed` with detailed messages; the only new mutate error kind is the ceiling.
14. Test strategy per §Test plan; chaos covers concurrent claim.
15. Zero new dependencies.

## Data model

### Migration `20260610000000_m9_scheduled_wakeups.sql`

```sql
-- +goose Up
CREATE TABLE scheduled_tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,                              -- operator-facing identity
    department_id UUID NOT NULL REFERENCES departments(id),
    role_slug TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('ticket','oneshot')),
    schedule_expr TEXT NOT NULL,                     -- grammar validated Go-side
    next_fire_at TIMESTAMPTZ NOT NULL,
    objective_template TEXT NOT NULL,                -- non-empty enforced Go-side + CHECK length > 0
    acceptance_criteria_template TEXT NOT NULL,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ NULL,                     -- soft delete (FR-502: run history survives)
    last_fired_at TIMESTAMPTZ NULL,                  -- set ONLY on outcome='fired' (FR-107 semantics)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(objective_template) > 0),
    CHECK (length(acceptance_criteria_template) > 0)
);
-- name uniqueness only among live tasks, so a deleted task's name is reusable
CREATE UNIQUE INDEX idx_scheduled_tasks_name_live ON scheduled_tasks (name) WHERE deleted_at IS NULL;
CREATE INDEX idx_scheduled_tasks_due ON scheduled_tasks (next_fire_at) WHERE NOT paused AND deleted_at IS NULL;

CREATE TABLE scheduled_task_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- no ON DELETE CASCADE: tasks soft-delete, runs are immutable history
    scheduled_task_id UUID NOT NULL REFERENCES scheduled_tasks(id),
    slot_at TIMESTAMPTZ NOT NULL,                    -- the slot this run answers for
    fired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    outcome TEXT NOT NULL CHECK (outcome IN ('fired','skipped_overlap','gate_deferred','failed')),
    detail TEXT NULL,                                -- human-readable reason for non-fired outcomes
    ticket_id UUID NULL REFERENCES tickets(id),      -- ticket-mode firings
    agent_instance_id UUID NULL REFERENCES agent_instances(id),  -- oneshot firings (backfilled at spawn)
    structured_outcome JSONB NULL                    -- finalize_oneshot payload commit
);
CREATE INDEX idx_scheduled_task_runs_task ON scheduled_task_runs (scheduled_task_id, fired_at DESC);

ALTER TABLE agent_instances ALTER COLUMN ticket_id DROP NOT NULL;
ALTER TABLE agent_instances ADD COLUMN scheduled_task_run_id UUID NULL REFERENCES scheduled_task_runs(id);
ALTER TABLE agent_instances ADD CONSTRAINT agent_instances_exactly_one_origin
    CHECK ((ticket_id IS NOT NULL)::int + (scheduled_task_run_id IS NOT NULL)::int = 1);
```

Plus: `chat_mutation_audit` verb CHECK gains `create_scheduled_task`, `edit_scheduled_task`, `pause_scheduled_task`, `resume_scheduled_task`, `delete_scheduled_task`; `affected_resource_type` CHECK gains `scheduled_task`; outcome CHECK gains `scheduled_task_creation_ceiling_reached`; `GRANT SELECT, INSERT, UPDATE, DELETE ON scheduled_tasks` + `GRANT SELECT ON scheduled_task_runs` to `garrison_dashboard_app` (dashboard CRUD is drizzle-direct per decision 11; runs are supervisor-written only). `+goose Down` reverses cleanly (note: the `agent_instances` CHECK drop precedes re-adding NOT NULL, and Down fails by design if oneshot rows exist — same posture as M8's PK reshape).

**Why the existing-instance rows survive**: all pre-M9 `agent_instances` rows have `ticket_id NOT NULL`, satisfying the new exactly-one-of CHECK with `scheduled_task_run_id NULL`.

### sqlc queries (`migrations/queries/m9_schedule.sql`)

| Query | Shape |
|---|---|
| `ClaimDueScheduledTasks` | `SELECT … FROM scheduled_tasks WHERE NOT paused AND deleted_at IS NULL AND next_fire_at <= now() ORDER BY next_fire_at FOR UPDATE SKIP LOCKED LIMIT $1` |
| `AdvanceScheduledTask` | `UPDATE scheduled_tasks SET next_fire_at=$2, last_fired_at=CASE WHEN $3::bool THEN $4 ELSE last_fired_at END, updated_at=now() WHERE id=$1` — `$3` is "this slot actually fired" (FR-107: skipped/deferred slots do not touch `last_fired_at`) |
| `InsertScheduledTaskRun` | insert with outcome + detail + optional ticket_id, returning id |
| `UpdateRunOutcome` | `UPDATE scheduled_task_runs SET outcome=$2, detail=$3 WHERE id=$1` (oneshot gate-defer / spawn-fail) |
| `UpdateRunStructuredOutcome` | `UPDATE scheduled_task_runs SET structured_outcome=$2 WHERE id=$1` |
| `SetRunAgentInstance` | backfill `agent_instance_id` at oneshot spawn |
| `InsertScheduledTicket` | M5.3 `InsertChatTicket` mirror (dept, role, objective, acceptance_criteria, `column_slug='todo'`) returning id |
| `HasOpenTicketForTask` | EXISTS: latest run's ticket not in a closed column (overlap predicate, ticket mode) |
| `HasRunningOneshotForTask` | EXISTS: latest run has `outcome IN ('fired','gate_deferred')` AND (`agent_instance_id IS NULL` OR joined instance `status='running'`) — fired-but-not-yet-dispatched and deferred-awaiting-poll-retry both count as in-flight, closing the tick→dispatch window (overlap predicate, oneshot mode) |
| `SelectScheduledTaskByRunID` | run → task join (SpawnOneshot prep) |
| `SelectScheduledTaskRunFinalizedState` | oneshot double-commit guard (FR-260 analog, keyed by run id) |
| `InsertScheduledTask` / `SelectScheduledTaskByName` / `ListScheduledTasks` | verb + validation support |
| `NotifyOneshotDue` | `SELECT pg_notify('work.scheduled.oneshot_due', $1::text)` (channel literal baked in, M6 retro gotcha 3) |

Event-outbox insert for oneshot reuses the existing outbox insert query with the new channel name; payload `{"event_id": "<outbox-row-id>", "scheduled_task_run_id": "...", "role_slug": "...", "department_id": "..."}` — same envelope contract the dispatcher already parses.

## Subsystem walkthroughs

### 1. `internal/schedule` — grammar, tick loop, firing

**Grammar** (`expr.go`):

```go
type Expr struct { /* unexported: kind, hh, mm, weekday, every time.Duration */ }

func Parse(s string) (Expr, error)           // typed *ParseError on bad grammar
func (e Expr) Next(after time.Time) time.Time // always strictly > after, UTC
func (e Expr) MinInterval() time.Duration     // daily=24h, weekly=168h, every@N=N
```

`Next` is pure date arithmetic (`time.Date` + day walking); `weekly` resolves the next matching weekday at HH:MM strictly after `after`. Collapse falls out for free: `Next(now)` after five missed days returns tomorrow's slot, not yesterday's.

**Validation** (`validate.go`):

```go
type ValidationInput struct { Name, RoleSlug, ScheduleExpr, ObjectiveTemplate, AcceptanceTemplate string; DepartmentID pgtype.UUID; Mode string }
func ValidateTask(ctx context.Context, q *store.Queries, minInterval time.Duration, now time.Time, in ValidationInput) (next time.Time, err error)
```

Checks: grammar parses; `expr.MinInterval() >= minInterval`; `Next(now)` is future (true by construction — kept as an explicit assertion per FR-105); name unique; department + role exist; templates non-empty; mode in enum. Errors are `*ValidationError{Field, Msg}` mapped to `validation_failed` by callers. Used by the chat verb, the dashboardapi endpoint, and (defensively) the tick loop.

**Templates** (`template.go`): `RenderTemplate(tpl string, fireAt time.Time, lastFiredAt pgtype.Timestamptz) string` — plain `strings.ReplaceAll` of `{{fire_at}}` (RFC 3339) and `{{last_fired_at}}` (RFC 3339 or the literal `never`). No text/template engine — no injection surface, no error path (FR-107).

**Tick loop** (`tick.go`):

```go
type Deps struct {
    Pool *pgxpool.Pool; Queries *store.Queries; Logger *slog.Logger
    TickInterval time.Duration; ClaimLimit int   // default 20
    Throttle throttle.Deps; Now func() time.Time
}
func RunLoop(ctx context.Context, deps Deps) error   // ticker + ctx.Done select, errgroup-managed
func tickOnce(ctx context.Context, deps Deps) (fired, skipped, deferred int, err error)
```

`tickOnce` transaction, per claimed task:

1. Overlap predicate per mode (`HasOpenTicketForTask` / `HasRunningOneshotForTask`) → if overlapping: `InsertScheduledTaskRun(outcome='skipped_overlap', slot_at=task.next_fire_at)`, advance, continue.
2. **Ticket mode** (`fire.go::fireTicketMode`): dept-weekly gate (`throttle.CheckDeptWeekly` — FR-402, M8 function reused) → on reject: run row `gate_deferred` + `throttle.FireDeptWeekly` evidence; on pass: render templates, `InsertScheduledTicket`, outbox row + the existing `work.ticket.created.<dept>.todo` notify (M5.3 in-tx shape), run row `fired` with `ticket_id`. The M6 company throttle fires later at the ticket's normal spawn-prep — existing behavior, not duplicated here (FR-400 ordering).
3. **Oneshot mode** (`fire.go::fireOneshotMode`): run row `fired` + outbox row + `NotifyOneshotDue`. Gates run at spawn-prep (next section), matching where reactive spawns gate.
4. `AdvanceScheduledTask(next=expr.Next(now), fired=<outcome=='fired'>, now)` — always advances exactly one future slot regardless of outcome (collapse + skip + defer all consume the slot; FR-104, Q6/Q7 semantics), but `last_fired_at` updates only when the slot actually fired (FR-107: `{{last_fired_at}}` means the previous *firing*, not the previous claim).
5. Commit. A task whose `schedule_expr` fails `Parse` at tick time (corrupted row) logs at error, fires nothing, and is left un-advanced for operator repair — defensive only; validation makes this unreachable through any authoring surface.

Claim limit 20/tick bounds tx size; remaining due tasks claim on the next tick (worst-case 30s slip, within the one-tick drift tolerance, FR-102).

**Test plan** (`expr_test.go`, `template_test.go`, `validate_test.go`, `tick_test.go`, `fire_test.go` — unit, no DB except where noted):

- `TestParseAcceptsGrammar` — table test over all three forms + boundary values (`00:00`, `23:59`, `every@15m`).
- `TestParseRejectsMalformed` — bad weekday, missing `@`, `25:00`, `every@0m`, empty, full-cron input each return `*ParseError`.
- `TestNextDailyComputesStrictlyFuture` — `after` exactly on the slot returns tomorrow.
- `TestNextWeeklyWalksToWeekday` — mid-week cases + same-day-before/after-time cases.
- `TestNextCollapsesMissedSlots` — `after` five days late returns the single next future slot (FR-104 arithmetic).
- `TestMinIntervalPerKind` — daily=24h, weekly=168h, every@N=N.
- `TestRenderTemplateSubstitutesBothVars` + `TestRenderTemplateNeverFired` — `never` literal.
- `TestValidateTaskRejectsSubMinimumInterval` / `TestValidateTaskRejectsUnknownDepartment` / `TestValidateTaskRejectsDuplicateName` (integration-tagged, DB-backed).
- `TestTickOnceAdvancesExactlyOneSlot` (integration) — seed due task, run `tickOnce`, assert run row + future `next_fire_at`.
- `TestTickOnceSkipsOverlapTicketMode` / `TestTickOnceSkipsOverlapOneshotMode` (integration).
- `TestTickOnceGateDeferredWritesEvidence` (integration) — dept budget 0 → `gate_deferred` run + `throttle_events` row, no ticket.
- `TestTickOncePausedTaskNotClaimed` (integration).
- `TestTickOnceCorruptExprLogsAndSkips` (integration) — defensive path.

### 2. `internal/spawn/oneshot.go` — SpawnOneshot + WriteFinalizeOneshot

**Public surface**:

```go
func SpawnOneshot(ctx context.Context, deps Deps, eventID pgtype.UUID) error
func WriteFinalizeOneshot(ctx context.Context, deps Deps, runID pgtype.UUID, instanceID pgtype.UUID, payload finalize.OneshotPayload) error
```

`SpawnOneshot` mirrors `Spawn`'s pipeline with the run record as origin:

1. Tx: `LockEventForProcessing` (existing dedupe), decode payload → `scheduled_task_run_id`, `SelectScheduledTaskByRunID`.
2. `concurrency.CheckCap` (dept) + `throttle.Check` (company) — on defer: `UpdateRunOutcome(gate_deferred, detail)` + the existing throttle-evidence writes, commit, leave `processed_at` NULL (M6 T007 posture: poll retries after the pause/budget window). `gate_deferred` is therefore **non-terminal for oneshot runs**: a successful poll re-check updates the run back to `fired` (`UpdateRunOutcome`) before the instance insert, per FR-401. Ticket-mode `gate_deferred` (dept-weekly rejection at fire time) is terminal for the slot — verb-level rejection precedent; the asymmetry is deliberate and documented in `deps.go`'s outcome constants.
3. `InsertRunningOneshotInstance` — `InsertRunningInstance` variant with `scheduled_task_run_id` instead of `ticket_id`; `SetRunAgentInstance` backfills the run row. Commit.
4. Run the existing pipeline (argv builder, claudeproto consumer, budget enforcement, terminal adjudication — transport untouched) with two differences: the rendered brief is the prompt source, and the per-spawn MCP config's finalize entry carries `GARRISON_FINALIZE_MODE=oneshot` + `GARRISON_SCHEDULED_RUN_ID=<run-id>` env (instead of the ticket id env). `finalizeExpectedForRole` is bypassed — oneshot spawns always expect finalize.
5. The pipeline's `onCommit` routes to `WriteFinalizeOneshot` (instead of `WriteFinalize`): one tx committing `UpdateRunStructuredOutcome` (full payload + a `verification` sub-object: diary length ≥ threshold, KG triple count ≥ 1 — the M2.x predicates applied inline, result recorded on the run, FR-403's no-hygiene-table-coupling) + palace diary/KG writes via the existing palace client path + `agent_instances` terminal row. `SelectScheduledTaskRunFinalizedState` guards double commit (FR-260 analog).

Timeout/failure exits mark the instance via the existing terminal adjudication; the run row keeps `outcome='fired'` with the instance's terminal status readable through the join (decision 5) — `UpdateRunOutcome(failed, …)` is reserved for pre-pipeline failures (container missing, exec setup error).

**Test plan** (`oneshot_test.go`):

- `TestSpawnOneshotGateDeferUpdatesRun` (integration) — paused company → run `gate_deferred`, `processed_at` NULL, no instance row.
- `TestSpawnOneshotRetryAfterGateClearsToFired` (integration) — deferred run + expired pause window → poll re-dispatch succeeds, run outcome back to `fired`, instance row lands (FR-401).
- `TestSpawnOneshotInsertsOriginInstance` (integration) — instance row has `scheduled_task_run_id` set, `ticket_id` NULL; CHECK satisfied.
- `TestWriteFinalizeOneshotCommitsAtomically` (integration) — payload commit writes structured_outcome + verification + terminal instance in one tx.
- `TestWriteFinalizeOneshotRejectsDoubleCommit` (integration).
- `TestOneshotMCPConfigCarriesModeEnvs` (unit) — config builder output inspection: finalize entry has both envs, no ticket env.

### 3. `internal/finalize` — mode switch

`Deps` gains `Mode string` (`"ticket"` default | `"oneshot"`) + `ScheduledRunID pgtype.UUID`; `runMCPFinalize` (cmd/supervisor/mcp_finalize.go) populates both from env. `tools/list` returns exactly one descriptor: `finalize_ticket` in ticket mode, `finalize_oneshot` in oneshot mode — an oneshot agent structurally cannot see or call `finalize_ticket` (FR-304), and vice versa. `OneshotPayload` = `FinalizePayload` minus `TicketID` (same Outcome/DiaryEntry/KGTriples validation, same "now" substitution). `tools/call` in oneshot mode validates and signals ok; the supervisor-side `onCommit` owns the atomic write (same division of labor as ticket mode). Ticket-mode behavior is byte-for-byte unchanged — existing tests untouched (FR-302/SC-010 enforced by CI).

**Test plan**: `TestToolsListTicketModeUnchanged`, `TestToolsListOneshotModeSingleTool`, `TestOneshotPayloadRejectsTicketID` (unknown field), `TestOneshotPayloadValidatesDiaryAndTriples` (bounds reuse), `TestOneshotModeRejectsFinalizeTicketCall` (-32601 path).

### 4. `internal/garrisonmutate` — verb + server-action entries

`verbs.go`: `create_scheduled_task` appended (ReversibilityClass 3, AffectedResourceType `scheduled_task`); `TestVerbsRegistryMatchesEnumeration` updated to 11. Handler (`verbs_scheduled.go`):

```go
func handleCreateScheduledTask(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error)
```

Args: `{name, department_slug, role_slug, mode, schedule_expr, objective_template, acceptance_criteria_template}`. Anchored like chat verbs (`ChatSessionID`); `assertExactlyOneCallerAnchor` NOT applied (chat-only verb — `AgentInstanceID` callers rejected explicitly with `validation_failed`, "agents cannot schedule work"). Calls `schedule.ValidateTask`, inserts, writes audit via `WriteAudit` (Tier 3 ⇒ full args in `args_jsonb`). All rejects map to `validation_failed` + detail (decision 13).

`server_action_verbs.go`: four entries (`edit_scheduled_task` class 2, `pause_scheduled_task` 1, `resume_scheduled_task` 1, `delete_scheduled_task` 3). These exist for the registry/tier table + CHECK alignment; execution is dashboard-side (decision 11). `TestVerbsSlicesDisjoint` (M8) covers the new entries automatically.

**Test plan**: `TestCreateScheduledTaskHappyPath` (integration — row + audit + computed next_fire_at), `TestCreateScheduledTaskRejectsAgentCaller`, `TestCreateScheduledTaskRejectsBadGrammar` / `RejectsSubMinInterval` / `RejectsDuplicateName` (audit rows carry `validation_failed`), `TestVerbsRegistryMatchesEnumeration` (11), `TestServerActionVerbsTierTable`.

### 5. `internal/chat/policy.go` — per-turn ceiling

Mirror of `MaxTicketsPerTurn` (M6 T011): fields `MaxScheduledTasksPerTurn int`, `scheduledTaskCreationCount int`, `scheduledTaskCeilingFired bool`; matcher `isCreateScheduledTaskBlockStart` (tool name `mcp__garrison-mutate__create_scheduled_task`); bail reason `ChatErrorScheduledTaskCreationCeilingReached = "scheduled_task_creation_ceiling_reached"`; same SSE assistant-error frame + terminal commit shape.

**Test plan**: `TestScheduledTaskCeilingFiresOnFourth` (default 3), `TestScheduledTaskCeilingIgnoresOtherTools`, `TestScheduledTaskCeilingEnvOverride`, `TestScheduledTaskCeilingIndependentOfTicketCeiling`.

### 6. `internal/dashboardapi/schedule_handler.go` — validate endpoint

`POST /schedule/validate`, body `{schedule_expr, mode?}` → `200 {ok: true, next_fire_at, min_interval_ok: true}` or `422` via the existing `writeErrorResponse` (`errKind: "validation_failed"`, message from `*ParseError`/`*ValidationError`). Auth + server wiring follow `objstore_handler.go` exactly. Grammar therefore lives only in Go (decision 10); the dashboard never computes a fire time.

**Test plan**: `TestScheduleValidateAcceptsGrammar`, `TestScheduleValidateRejects422WithDetail`, `TestScheduleValidateRequiresAuth`.

### 7. `internal/config` + `cmd/supervisor/main.go`

Config (existing parse patterns, decision 12): `SchedTickInterval time.Duration` (default 30s, min 1s — duration pattern), `SchedMinInterval time.Duration` (default 15m — duration pattern), `MaxScheduledTasksPerTurn int` (default 3, ≥1 — Sscanf int pattern). Tests: defaults + overrides + rejection on bad values (`TestConfigSchedDefaults`, `TestConfigSchedOverrides`, `TestConfigSchedRejectsZeroTick`).

main.go: build `schedule.Deps` after throttle deps; `g.Go(func() error { return schedule.RunLoop(gctx, schedDeps) })` alongside the existing errgroup members; register `work.scheduled.oneshot_due` as a base dispatcher channel routed to a handler closing over spawn deps → `spawn.SpawnOneshot`. The poll fallback covers oneshot events automatically (they're ordinary outbox rows). Shutdown: ticker loop exits on `gctx.Done()`; in-flight `tickOnce` finishes its tx (sub-second).

### 8. Dashboard

- `lib/actions/scheduledTasks.ts`: five Server Actions (`createScheduledTask`, `editScheduledTask`, `pauseScheduledTask`, `resumeScheduledTask`, `deleteScheduledTask`). Each: session check → call `POST /schedule/validate` (create/edit only; resume recomputes `next_fire_at` via the same endpoint) → drizzle tx writing the row change + the `chat_mutation_audit` row (verb from `ServerActionVerbs`, chat anchors NULL, `affected_resource_type='scheduled_task'`; delete captures pre-state into `args_jsonb` per Tier 3). `deleteScheduledTask` is a soft delete (`UPDATE … SET deleted_at=now()`) — runs and audit history survive; list queries filter `deleted_at IS NULL`. Typed result returns (`{ok} | {ok:false, errorKind, message}`), no throws — M8 `mcpServer.ts` shape.
- `lib/queries/scheduledTasks.ts`: `listScheduledTasks` (live rows only), `getScheduledTaskById`, `getTaskRunHistory(taskId, limit=50)` (runs joined to `agent_instances.status` for oneshot terminal state), `getScheduledOriginForTickets(ticketIds)` (ticket → run → task name, for the kanban chip below).
- Kanban anchor (FR-201 "queryable from existing surfaces" / US1-AS2): the ticket `TicketCard` gains a small scheduled-origin chip (task name, linking to `/admin/recurring-jobs/[id]`) when the ticket was fired by a schedule — same shape as M6 T017's parent chip.
- Routes: `/admin/recurring-jobs/page.tsx` (force-dynamic, `max-w-[1400px] mx-auto` + `w-full` per the M6 width-collapse retro lesson, `<SoftPoll intervalMs={60_000}>`) with `CreateTaskForm`; `[id]/page.tsx` detail with `TaskDetailControls` (edit/pause/resume/delete) + `RunHistoryTable` (outcome chips: fired / skipped overlap / gate deferred / failed, oneshot rows show instance status + structured-outcome summary). Sidebar `NavSubLink` under the admin group.
- No vitest per repo convention; Go integration tests pin the shapes these read.

## Integration, chaos, and regression test plan

`supervisor/internal/schedule/integration_test.go` (`//go:build integration`) unless noted:

- `TestTicketModeGoldenPath` — seed dept/role/task (due now); run `tickOnce`; assert ticket row content (rendered templates), run row `fired` + ticket_id, outbox row + notify observed via LISTEN, dispatcher spawn under fake agent, `next_fire_at` advanced once. **The milestone's ticket-mode smoke test.**
- `TestOneshotGoldenPath` — seed oneshot task; `tickOnce` → dispatcher → `SpawnOneshot` with fake agent emitting a `finalize_oneshot` NDJSON fixture; assert structured_outcome + verification on the run, terminal instance with `scheduled_task_run_id`, zero tickets. **The oneshot smoke test.**
- `TestRecoveryCollapseFiresOnce` — task with `next_fire_at` 3 slots in the past (supervisor "down"); one `tickOnce`; exactly one run row, future `next_fire_at`.
- `TestPauseResumeAdvanceOnly` — pause across 2 slots; resume; assert zero runs and future-only `next_fire_at`.
- `TestZeroIdleCost` — no due tasks; N ticks; zero runs, zero instances (SC-008 proxy).
- Chaos (`//go:build chaos`): `TestConcurrentClaimSingleFiring` — two goroutines run `tickOnce` concurrently against one due task; exactly one run row + one firing (SKIP LOCKED discipline; the M8 double-fire lesson applied up front).
- Regression: full default + integration + chaos suites from M1–M8 pass untouched; `internal/finalize` ticket-mode tests unchanged (SC-010).

Coverage target ≥82% on new Go code (M6 retro gotcha 7 — coverage clearance is an explicit final step, not a side effect).

## Deployment + housekeeping

- **No compose/Dockerfile changes.** Three new env vars documented in `docs/ops-checklist.md` (M9 section) + a first-task walkthrough (create a daily standup via dashboard, verify the fire).
- **Pre-implementation housekeeping commits** (before any verb code, FR-601): (1) `docs/security/chat-threat-model.md` amendment — `create_scheduled_task` threat row, §5 tier-table entry (Tier 3), verb count 10→11, plus the four Server-Action verbs noted in the M8-established registry section; (2) `AGENTS.md` sealed-surfaces note — finalize surface amended by M9 context to add the sibling tool, `finalize_ticket` schema unchanged.
- **At ship**: ARCHITECTURE.md §M9 paragraph annotated Shipped with implementation pointers (the M8 T021 pattern + amendment test assertions).

## Open questions / gaps

None. Every structural decision an implementing agent needs — package boundaries, signatures, schema, error vocabulary, channel name, env vars, file paths, test names — is fixed above. The three numeric defaults (30s / 15m / 3) are operator-tunable per the spec's assumptions and need no further decision.
