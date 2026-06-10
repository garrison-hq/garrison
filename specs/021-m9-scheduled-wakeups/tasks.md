# M9 tasks — Scheduled / triggered wake-ups (heartbeat)

**Branch**: `021-m9-scheduled-wakeups` | **Plan**: [plan.md](./plan.md) | **Spec**: [spec.md](./spec.md) | **Context**: [m9-context.md](../_context/m9-context.md) | **M8 retro**: [m8.md](../../docs/retros/m8.md) | **M6 retro**: [m6.md](../../docs/retros/m6.md)

21 tasks total (T001–T021). Executed linearly by a solo operator. Each task is one Claude Code session in scope and produces a reviewable commit. The repo is in a working state after every task. No spike (see "No spike" in m9-context.md).

Risk callouts carried forward from the M6 + M8 retros (woven into specific tasks below):

- **Audit CHECK extensions land in T001 up front** — M8 discovered the `affected_resource_type` and outcome CHECK gaps at first integration-test run; M9's five verbs + `scheduled_task` resource type + ceiling outcome go into the migration on day one.
- **Drizzle:pull empty-default mangling** (M7/M8 gotcha): T001 includes the explicit `bunx tsc --noEmit` step + inline TS fix if it fires.
- **sqlc `sqlc.arg(name)` exclusively** in `m9_schedule.sql` (M7 gotcha).
- **LISTEN channel double-quoting** (M6 retro): `work.scheduled.oneshot_due` contains dots; every LISTEN statement double-quotes it, and `NotifyOneshotDue` bakes the channel literal into the sqlc query (M6 gotcha 3).
- **Coverage clearance is its own step** (M6 retro gotcha 7): T020 runs the new-code coverage probe and tops up Go-side tests to ≥82% before the PR push, plus Sonar new-issues pre-clearance (M8 T022 pattern).
- **Lint locally before every push**: `gofmt -l .` + `go vet ./...` from `supervisor/` (standing memory `feedback_lint_before_push`).

Zero new Go dependencies. Zero new TypeScript dependencies.

---

## Ordering principle

T001 lands the consolidated migration + sqlc + drizzle pull; every later task references the new schema. T002 (config) and T003–T004 (the `internal/schedule` pure core, then DB-backed validation) are foundations. T005 flips `internal/finalize` to mode-switched tool exposure while keeping ticket mode byte-for-byte (its regression suite is the completion gate). T006 builds the tick loop and firing transaction — after T006 the supervisor can fire ticket-mode tasks end-to-end with existing machinery. T007–T008 add the oneshot spawn path and its finalize commit; T009 wires both into main.go and the dispatcher, completing the supervisor side.

T010 amends the threat model + AGENTS.md **before** any verb code (chat-threat-model Rule 1 binding; FR-601). T011–T012 then land the chat verb and its ceiling. T013–T015 ship the dashboard (validate endpoint → actions/queries → routes). T016–T018 are the integration + chaos suites; T019 the doc amendments; T020 scripted acceptance; T021 the retro.

**Sequencing precondition**: M7.1 must be merged to main and this branch rebased onto it before T001 executes (the oneshot spawn path rides the container-exec transport; see "Sequencing assumption" in m9-context.md).

---

## Phase 1 — Foundations

- [ ] **T001** Goose migration `20260610000000_m9_scheduled_wakeups.sql` + sqlc query file + drizzle pull
  - **Depends on**: M7.1 shipped (merged to main; branch `021-m9-scheduled-wakeups` rebased onto it).
  - **Files**: `migrations/20260610000000_m9_scheduled_wakeups.sql` (new); `migrations/queries/m9_schedule.sql` (new); `supervisor/sqlc.yaml` (extend); `supervisor/internal/store/m9_schedule.sql.go` + `models.go` (sqlc-generated); `dashboard/drizzle/schema.supervisor.ts` (regenerated via `bun run drizzle:pull`).
  - **Completion condition**: Migration applies via `goose up` against a fresh testcontainer Postgres and reverses via `goose down`. Up section covers all plan §Data model deltas: `scheduled_tasks` table (incl. `deleted_at` soft-delete column, `idx_scheduled_tasks_name_live` partial unique index — name uniqueness among live rows only, `idx_scheduled_tasks_due` partial index `WHERE NOT paused AND deleted_at IS NULL`, template-length CHECKs), `scheduled_task_runs` table (outcome CHECK `fired|skipped_overlap|gate_deferred|failed`, **no ON DELETE CASCADE** — runs are immutable history surviving soft delete, `idx_scheduled_task_runs_task`), `agent_instances.ticket_id` DROP NOT NULL + `scheduled_task_run_id` FK + `agent_instances_exactly_one_origin` CHECK, `chat_mutation_audit` verb CHECK += the five M9 verbs, `affected_resource_type` CHECK += `scheduled_task`, outcome CHECK += `scheduled_task_creation_ceiling_reached`, dashboard grants (CRUD on `scheduled_tasks`, SELECT on `scheduled_task_runs`). All queries from plan §sqlc table present, `sqlc.arg(name)` style; `NotifyOneshotDue` bakes the channel literal. `make sqlc` + `go build ./...` succeed; `bunx tsc --noEmit` from `dashboard/` passes (inline-fix any empty-default mangling). `TestM9MigrationRoundtrip` in `supervisor/internal/store/migrate_integration_test.go` asserts apply → rollback → apply fingerprint stability, and asserts pre-existing `agent_instances` rows satisfy the new CHECK.
  - **Out of scope for this task**: any caller code (T002+); threat-model/AGENTS.md amendments (T010); ARCHITECTURE.md amendment (T019).

- [ ] **T002** `internal/config/` — three M9 env vars
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/config/config.go` (extend — `SchedTickInterval`, `SchedMinInterval`, `MaxScheduledTasksPerTurn`); `supervisor/internal/config/config_test.go` (extend).
  - **Completion condition**: `GARRISON_SCHED_TICK_INTERVAL` (duration, default `30s`, reject `< 1s`), `GARRISON_SCHED_MIN_INTERVAL` (duration, default `15m`, reject `<= 0`), `GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN` (int, default `3`, reject `< 1`) parse per the existing duration/Sscanf patterns. Tests pass: `TestConfigSchedDefaults`, `TestConfigSchedOverrides`, `TestConfigSchedRejectsZeroTick`, `TestConfigSchedRejectsZeroCeiling`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: main.go consumption (T009); chat policy consumption (T012).

- [ ] **T003** `internal/schedule/` — expression grammar + templates (pure core)
  - **Depends on**: T001 (package home exists in the tree; no DB use).
  - **Files**: `supervisor/internal/schedule/expr.go`, `expr_test.go`, `template.go`, `template_test.go`, `deps.go` (new — `Deps` struct + run-outcome string constants `fired`/`skipped_overlap`/`gate_deferred`/`failed`).
  - **Completion condition**: `Parse` accepts exactly `daily@HH:MM`, `weekly@{mon..sun}@HH:MM`, `every@<N>{m|h}` (UTC) and returns `*ParseError` otherwise; `Expr.Next(after)` is strictly future; `Expr.MinInterval()` per kind; `RenderTemplate` substitutes `{{fire_at}}` + `{{last_fired_at}}` (literal `never` when unset). Tests pass: `TestParseAcceptsGrammar`, `TestParseRejectsMalformed` (incl. full-cron input), `TestNextDailyComputesStrictlyFuture`, `TestNextWeeklyWalksToWeekday`, `TestNextCollapsesMissedSlots`, `TestMinIntervalPerKind`, `TestRenderTemplateSubstitutesBothVars`, `TestRenderTemplateNeverFired`. Stdlib only. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: DB-backed validation (T004); tick loop (T006).

- [ ] **T004** `internal/schedule/validate.go` — task validation
  - **Depends on**: T002, T003.
  - **Files**: `supervisor/internal/schedule/validate.go`, `validate_test.go` (new).
  - **Completion condition**: `ValidateTask(ctx, q, minInterval, now, in)` enforces grammar, min-interval, future first slot, name uniqueness, department + role existence, non-empty templates, mode enum; returns computed `next_fire_at` or `*ValidationError{Field, Msg}`. Tests pass: `TestValidateTaskRejectsSubMinimumInterval` (unit), `TestValidateTaskRejectsUnknownDepartment`, `TestValidateTaskRejectsDuplicateName` (integration-tagged), `TestValidateTaskComputesNextFire` (unit). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: verb handler (T011); dashboardapi endpoint (T013).

- [ ] **T005** `internal/finalize/` — mode switch + `OneshotPayload`
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/finalize/server.go`, `tool.go`, `handler.go` (extend); `supervisor/internal/finalize/*_test.go` (extend); `supervisor/cmd/supervisor/mcp_finalize.go` (extend — read `GARRISON_FINALIZE_MODE` + `GARRISON_SCHEDULED_RUN_ID`).
  - **Completion condition**: `Deps.Mode` defaults to `ticket`; `tools/list` returns exactly one descriptor per mode (`finalize_ticket` | `finalize_oneshot`); `OneshotPayload` = `FinalizePayload` minus `TicketID` with identical Outcome/DiaryEntry/KGTriples validation + "now" substitution; oneshot-mode `Handle` guards double commit via `SelectScheduledTaskRunFinalizedState`. Tests pass: `TestToolsListTicketModeUnchanged`, `TestToolsListOneshotModeSingleTool`, `TestOneshotPayloadRejectsTicketID`, `TestOneshotPayloadValidatesDiaryAndTriples`, `TestOneshotModeRejectsFinalizeTicketCall` (-32601). **Every pre-existing `internal/finalize` test passes byte-for-byte unmodified** (FR-302 gate). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: supervisor-side commit (`WriteFinalizeOneshot`, T008); spawn-side env plumbing (T007).

## Phase 2 — Tick loop + oneshot spawn path

- [ ] **T006** `internal/schedule/` — tick loop + firing transaction (ticket mode live end-to-end)
  - **Depends on**: T003, T004.
  - **Files**: `supervisor/internal/schedule/tick.go`, `tick_test.go`, `fire.go`, `fire_test.go` (new); `supervisor/internal/schedule/integration_test.go` (new, `//go:build integration`).
  - **Completion condition**: `RunLoop` (ticker + `ctx.Done()` select) and `tickOnce` implement plan §1's transaction exactly: claim (`ClaimDueScheduledTasks`, limit from `Deps.ClaimLimit` default 20) → per-task overlap predicate → ticket mode: dept-weekly gate (reject ⇒ `gate_deferred` run + `throttle.FireDeptWeekly` evidence) or render + `InsertScheduledTicket` + outbox + existing `work.ticket.created.<dept>.todo` notify + `fired` run with `ticket_id`; oneshot mode: `fired` run + outbox row + `NotifyOneshotDue` — then `AdvanceScheduledTask` (always exactly one future slot; `last_fired_at` updated ONLY when the slot's outcome is `fired`, per FR-107) and commit. Claim query excludes paused and soft-deleted tasks. Corrupt-expression rows log error, fire nothing, stay un-advanced. Tests pass: `TestTickOnceAdvancesExactlyOneSlot`, `TestTickOnceSkipsOverlapTicketMode`, `TestTickOnceSkipsOverlapOneshotMode`, `TestTickOnceGateDeferredWritesEvidence`, `TestTickOncePausedTaskNotClaimed`, `TestTickOnceCorruptExprLogsAndSkips` (all integration). `gofmt -l` + `go vet` clean. M6 throttle + M8 dept-weekly tests pass unchanged (regression).
  - **Out of scope for this task**: oneshot spawn consumption (T007); main.go wiring (T009); recovery-collapse test (T017).

- [ ] **T007** `internal/spawn/oneshot.go` — `SpawnOneshot` + oneshot MCP-config envs
  - **Depends on**: T005, T006.
  - **Files**: `supervisor/internal/spawn/oneshot.go`, `oneshot_test.go` (new); `supervisor/internal/spawn/spawn.go` (extend — finalize-mode env seam in the MCP-config builder only).
  - **Completion condition**: `SpawnOneshot(ctx, deps, eventID)` implements plan §2 steps 1–4: `LockEventForProcessing` dedupe → run/task resolution → `concurrency.CheckCap` + `throttle.Check` (defer ⇒ `UpdateRunOutcome(gate_deferred)` + evidence, `processed_at` stays NULL; a later successful poll re-dispatch clears the run back to `fired` before the instance insert, per FR-401 — `gate_deferred` is non-terminal for oneshot) → `InsertRunningOneshotInstance` + `SetRunAgentInstance` → existing pipeline with the rendered brief as prompt and the finalize entry carrying `GARRISON_FINALIZE_MODE=oneshot` + `GARRISON_SCHEDULED_RUN_ID` (no ticket env); `finalizeExpectedForRole` bypassed for oneshot. Pre-pipeline failures write `UpdateRunOutcome(failed, detail)`. Tests pass: `TestSpawnOneshotGateDeferUpdatesRun`, `TestSpawnOneshotRetryAfterGateClearsToFired`, `TestSpawnOneshotInsertsOriginInstance` (integration), `TestOneshotMCPConfigCarriesModeEnvs` (unit). `gofmt -l` + `go vet` clean. M7.1 container-exec spawn tests pass unchanged (regression).
  - **Out of scope for this task**: the finalize commit (T008); dispatcher channel registration (T009).

- [ ] **T008** `internal/spawn/` — `WriteFinalizeOneshot` + onCommit routing
  - **Depends on**: T007.
  - **Files**: `supervisor/internal/spawn/oneshot.go` (extend); `supervisor/internal/spawn/oneshot_test.go` (extend).
  - **Completion condition**: the oneshot pipeline's `onCommit` routes to `WriteFinalizeOneshot`: one tx committing `UpdateRunStructuredOutcome` (payload + `verification` sub-object: diary length vs `ThinDiaryThreshold`, KG count ≥ 1) + palace diary/KG writes via the existing client path + terminal `agent_instances` row; double-commit rejected. Timeout/failure exits leave run `fired` with terminal state readable through the instance join. Tests pass: `TestWriteFinalizeOneshotCommitsAtomically`, `TestWriteFinalizeOneshotRejectsDoubleCommit` (integration). `gofmt -l` + `go vet` clean. `internal/finalize` ticket-mode + M2.2.1 finalize-write tests pass unchanged (regression).
  - **Out of scope for this task**: golden-path end-to-end test (T016); hygiene-table writes (none in M9 per FR-403).

- [ ] **T009** `cmd/supervisor/main.go` — RunLoop + dispatcher channel wiring
  - **Depends on**: T002, T006, T007, T008.
  - **Files**: `supervisor/cmd/supervisor/main.go` (extend); `supervisor/internal/events/listener_test.go` (extend if the channel-registration seam needs a pin).
  - **Completion condition**: `schedule.Deps` composed from config + pool + queries + throttle deps; `g.Go(schedule.RunLoop)` added to the errgroup; `work.scheduled.oneshot_due` registered as a base dispatcher channel routed to a handler invoking `spawn.SpawnOneshot` (LISTEN statement double-quotes the channel). Poll fallback picks up oneshot outbox rows with no extra code (asserted in test). Supervisor boots and shuts down cleanly with the loop running. Tests pass: `TestOneshotChannelDispatchesToSpawn` (integration — emit notify, assert handler invocation), `TestPollFallbackPicksUpOneshotEvents` (integration). `gofmt -l` + `go vet` clean. M1 startup/shutdown integration tests pass unchanged (regression).
  - **Out of scope for this task**: dashboard (T013–T015); acceptance script (T020).

## Phase 3 — Authoring surfaces

- [ ] **T010** Docs — chat-threat-model amendment + AGENTS.md sealed-surface note (pre-verb-code, binding)
  - **Depends on**: T001 (verb names fixed); MUST land before T011 per chat-threat-model Rule 1 / FR-601.
  - **Files**: `docs/security/chat-threat-model.md` (amend — `create_scheduled_task` threat row, §5 reversibility-table entry Tier 3 with rationale "creates recurring cost-incurring state; accrued firings/spend do not reverse", verb count 10→11, Server-Action registry note for the four dashboard verbs); `AGENTS.md` (amend — sealed-surfaces line: finalize surface extended by M9 context with sibling tool `finalize_oneshot`; `finalize_ticket` schema unchanged).
  - **Completion condition**: both amendments committed with a commit message citing m9-context.md §Scope extensions. The threat-model amendment names the per-turn ceiling (default 3) and the minimum-interval bound as the runaway mitigations for the new verb. No code in this commit.
  - **Out of scope for this task**: ARCHITECTURE.md M9-shipped annotation (T019); retro notes (T021).

- [ ] **T011** `internal/garrisonmutate/` — `create_scheduled_task` verb + `ServerActionVerbs` entries
  - **Depends on**: T004, T010.
  - **Files**: `supervisor/internal/garrisonmutate/verbs.go` (extend — 11th entry, Tier 3, resource type `scheduled_task`); `supervisor/internal/garrisonmutate/verbs_scheduled.go`, `verbs_scheduled_test.go` (new); `supervisor/internal/garrisonmutate/server_action_verbs.go` (extend — `edit_scheduled_task` class 2, `pause_scheduled_task` 1, `resume_scheduled_task` 1, `delete_scheduled_task` 3); `supervisor/internal/garrisonmutate/verbs_test.go` (extend — registry count + disjointness).
  - **Completion condition**: handler validates via `schedule.ValidateTask`, inserts, writes the Tier-3 audit row (full args) anchored on `ChatSessionID`; `AgentInstanceID` callers rejected with `validation_failed` ("agents cannot schedule work"); all rejects map to `validation_failed` + detail. Tests pass: `TestCreateScheduledTaskHappyPath` (integration), `TestCreateScheduledTaskRejectsAgentCaller`, `TestCreateScheduledTaskRejectsBadGrammar`, `TestCreateScheduledTaskRejectsSubMinInterval`, `TestCreateScheduledTaskRejectsDuplicateName`, `TestVerbsRegistryMatchesEnumeration` (11), `TestVerbsSlicesDisjoint`, `TestServerActionVerbsTierTable`. `gofmt -l` + `go vet` clean. M5.3/M8 verb tests pass unchanged (regression).
  - **Out of scope for this task**: chat ceiling (T012); dashboard Server Actions that execute the four registry entries (T014).

- [ ] **T012** `internal/chat/policy.go` — `create_scheduled_task` per-turn ceiling
  - **Depends on**: T002, T011.
  - **Files**: `supervisor/internal/chat/policy.go`, `policy_test.go` (extend).
  - **Completion condition**: `MaxScheduledTasksPerTurn` ceiling mirrors the M6 `MaxTicketsPerTurn` mechanism (counts `mcp__garrison-mutate__create_scheduled_task` tool_use_block_start frames; bail reason `scheduled_task_creation_ceiling_reached`; assistant-error SSE frame + terminal commit). Tests pass: `TestScheduledTaskCeilingFiresOnFourth`, `TestScheduledTaskCeilingIgnoresOtherTools`, `TestScheduledTaskCeilingEnvOverride`, `TestScheduledTaskCeilingIndependentOfTicketCeiling`. `gofmt -l` + `go vet` clean. M6 ticket-ceiling tests pass unchanged (regression).
  - **Out of scope for this task**: dashboard (T013+).

- [ ] **T013** `internal/dashboardapi/schedule_handler.go` — `POST /schedule/validate`
  - **Depends on**: T004.
  - **Files**: `supervisor/internal/dashboardapi/schedule_handler.go`, `schedule_handler_test.go` (new); `supervisor/internal/dashboardapi/server.go` (extend — route registration).
  - **Completion condition**: endpoint validates `{schedule_expr}` via `schedule.Parse` + min-interval check and returns `200 {ok, next_fire_at}` or `422` through `writeErrorResponse` with `errKind: "validation_failed"` + the parse/validation detail; auth identical to `objstore_handler.go`. Tests pass: `TestScheduleValidateAcceptsGrammar`, `TestScheduleValidateRejects422WithDetail`, `TestScheduleValidateRequiresAuth`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: dashboard callers (T014).

- [ ] **T014** Dashboard — queries + Server Actions (audit-writing CRUD)
  - **Depends on**: T001, T013.
  - **Files**: `dashboard/lib/queries/scheduledTasks.ts` (new — `listScheduledTasks`, `getScheduledTaskById`, `getTaskRunHistory` joined to `agent_instances.status`); `dashboard/lib/actions/scheduledTasks.ts` (new — five actions per plan §8).
  - **Completion condition**: each action does session check → `POST /schedule/validate` (create/edit/resume) → drizzle tx writing the row change + the `chat_mutation_audit` row (verb from `ServerActionVerbs`, chat anchors NULL, `affected_resource_type='scheduled_task'`; delete snapshots pre-state into `args_jsonb`); typed `{ok} | {ok:false, errorKind, message}` returns, no throws. `deleteScheduledTask` is a soft delete (`SET deleted_at=now()`) — run history and audit rows survive; list queries filter live rows. Resume recomputes `next_fire_at` via the endpoint (advance-only). `bunx tsc --noEmit` passes. Per repo convention no vitest lands; the Go integration suite (T016/T017) pins the row shapes these write/read.
  - **Out of scope for this task**: pages/components (T015); SSE (out of milestone).

- [ ] **T015** Dashboard — `/admin/recurring-jobs` routes + components + sidebar
  - **Depends on**: T014.
  - **Files**: `dashboard/app/[locale]/(app)/admin/recurring-jobs/page.tsx`, `[id]/page.tsx` (new); `dashboard/components/features/scheduled-tasks/CreateTaskForm.tsx`, `TaskRow.tsx`, `TaskDetailControls.tsx`, `RunHistoryTable.tsx` (new); `dashboard/components/layout/Sidebar.tsx` (extend — `NavSubLink` under the admin group); kanban `TicketCard` component + its query (extend — scheduled-origin chip via `getScheduledOriginForTickets`, M6 T017 parent-chip pattern).
  - **Completion condition**: list page (force-dynamic, `max-w-[1400px] mx-auto` + `w-full` per the M6 width-collapse lesson, `<SoftPoll intervalMs={60_000}>`) renders tasks with schedule / next fire / mode / state / last outcome + `CreateTaskForm`; detail page renders edit/pause/resume/delete controls + `RunHistoryTable` with outcome chips (`fired` / `skipped overlap` / `gate deferred` / `failed`; oneshot rows show instance status + structured-outcome summary). Schedule-fired tickets show the scheduled-origin chip on the kanban `TicketCard`, linking to the task's detail page (FR-201 / US1-AS2). Validation errors from the actions render inline. `bunx tsc --noEmit` + `bun run lint` pass; page renders against a seeded dev stack.
  - **Out of scope for this task**: Go-side anything; acceptance walk (T020).

## Phase 4 — Test suites + docs + acceptance

- [ ] **T016** Golden-path integration tests — ticket mode + oneshot (the milestone smoke tests)
  - **Depends on**: T009.
  - **Files**: `supervisor/internal/schedule/integration_test.go` (extend).
  - **Completion condition**: `TestTicketModeGoldenPath` passes — seeded due task → `tickOnce` → ticket row with rendered templates + run row `fired`+`ticket_id` + LISTEN-observed notify + fake-agent dispatcher spawn + `next_fire_at` advanced exactly once. `TestOneshotGoldenPath` passes — seeded oneshot task → `tickOnce` → dispatcher → `SpawnOneshot` with a fake agent emitting a `finalize_oneshot` NDJSON fixture → structured_outcome + verification on the run + terminal instance with `scheduled_task_run_id` + **zero `tickets` rows**. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: recovery/pause cases (T017); chaos (T018).

- [ ] **T017** Recovery-collapse + pause/resume + zero-idle integration tests
  - **Depends on**: T016.
  - **Files**: `supervisor/internal/schedule/integration_test.go` (extend).
  - **Completion condition**: `TestRecoveryCollapseFiresOnce` (task 3 slots past-due → one `tickOnce` → exactly one run + future `next_fire_at`, no backfill), `TestPauseResumeAdvanceOnly` (pause across 2 slots → zero runs; resume → future-only `next_fire_at`, no catch-up fire), `TestZeroIdleCost` (no due tasks → N ticks → zero runs, zero instances) all pass. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: chaos (T018).

- [ ] **T018** Chaos test — concurrent claim single-firing
  - **Depends on**: T016.
  - **Files**: `supervisor/internal/schedule/chaos_test.go` (new, `//go:build chaos`).
  - **Completion condition**: `TestConcurrentClaimSingleFiring` passes — two goroutines run `tickOnce` concurrently against one due task (M8 chaos-test shape); exactly one run row and one firing land; SKIP LOCKED discipline verified. `gofmt -l` + `go vet` clean. Full prior chaos suite passes unchanged (regression).
  - **Out of scope for this task**: acceptance script (T020).

- [ ] **T019** `docs/ops-checklist.md` M9 section + ARCHITECTURE.md §M9 shipped annotation + pin test
  - **Depends on**: T016 (the behavior being documented is proven).
  - **Files**: `docs/ops-checklist.md` (extend — three env vars with defaults, first-task walkthrough: create a daily standup via dashboard, verify the fire and the run history); `ARCHITECTURE.md` (extend — §M9 paragraph annotated Shipped with per-thread implementation pointers); `dashboard/tests/architecture-amendment.test.ts` (extend — substring pins for the M9 annotation, M6 T019 pattern).
  - **Completion condition**: amendment test passes; ops-checklist section names every new env var and the recurring-jobs URL. No code changes.
  - **Out of scope for this task**: retro (T021).

- [ ] **T020** Scripted acceptance — `scripts/m9-acceptance.sh` walking SC-001..SC-010 + coverage + Sonar pre-clearance
  - **Depends on**: T001–T019 all complete.
  - **Files**: `scripts/m9-acceptance.sh` (new); patches against earlier tasks' files only if a step fails.
  - **Completion condition**: the script maps each spec success criterion to its verifying suite/step (SC-001/002 → T016 tests; SC-003 → T017; SC-004 → T018; SC-005 → T006/T007 gate tests; SC-006 → seeded-stack dashboard walk, operator-attended; SC-007 → chat-session walk + `git log` assertion that T010's amendment commit precedes T011's verb commit; SC-008 → T017 zero-idle test; SC-009 → T004/T011/T013 validation tests; SC-010 → full default+integration+chaos regression run) and exits 0 with all steps green. New-code coverage probe reports ≥82% on Go-side new code (top up tests if short — no production-code changes); Sonar new-issues API reports zero unresolved issues. If any step fails: focused patch against the relevant earlier task's files, then re-run all acceptance steps from the top. **No new features in this task.** `gofmt -l .` + `go vet ./...` clean before the push.
  - **Out of scope for this task**: retro (T021); any scope addition.

- [ ] **T021** Retro — `docs/retros/m9.md` + MemPalace drawer mirror
  - **Depends on**: T020 green.
  - **Files**: `docs/retros/m9.md` (new); MemPalace `wing_company / hall_events` drawer via `mempalace_add_drawer` (+ `mempalace_kg_add` for cross-milestone facts), per the M3+ dual-deliverable policy.
  - **Completion condition**: retro follows the AGENTS.md structure — what shipped (per task), what the spec got wrong, dependencies added outside the locked list (expected: none) with justifications if any, open questions deferred to the next milestone, and answers to the context file's named retro questions: did the zero-idle-cost invariant hold; any misfire/double-fire; did the collapse rule behave on a real supervisor restart; did chat authoring stay inside the amended threat model. Notes whether any task pushed lint-failing code. Palace drawer mirrors the markdown (markdown canonical).
  - **Out of scope for this task**: everything else — the retro adds no code.
