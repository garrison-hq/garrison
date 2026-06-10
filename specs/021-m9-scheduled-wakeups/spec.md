# Feature Specification: M9 — Scheduled / triggered wake-ups (heartbeat)

**Feature Branch**: `021-m9-scheduled-wakeups`
**Created**: 2026-06-10
**Status**: Draft
**Input**: `specs/_context/m9-context.md` (binding); ARCHITECTURE.md §M9; RATIONALE.md §1, §13; `docs/security/chat-threat-model.md`; `docs/retros/m8.md`; `specs/_context/m8-context.md`; `docs/retros/m6.md`; `specs/_context/m6-context.md`; `docs/retros/m7.md` §Forward-looking; `specs/_context/m7-1-context.md`. No research spike — see "No spike" in m9-context.md.

M9 adds the proactive axis to Garrison's event loop: a supervisor-internal tick fires concrete named jobs on a cadence, alongside (never replacing) the pg_notify-reactive loop. The design core — claim query, fire-on-recovery-with-collapse, two firing modes, `finalize_oneshot` as a sibling tool, concrete-named-jobs discipline — is committed by ARCHITECTURE.md §M9 and is input to this spec, not output. This spec resolves the ten open questions from "Open questions the spec must resolve" in m9-context.md; the resolution table at the end maps each to its requirements.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Recurring ticket-mode job fires through the normal Kanban flow (Priority: P1)

The operator defines a scheduled task — "daily standup, engineering, every weekday at 09:00 UTC" — in `ticket` mode. At each slot the supervisor inserts a ticket with the templated objective and acceptance criteria into the department's `todo` column, and the existing machinery takes over: dispatcher pickup, agent spawn, `finalize_ticket` exit, hygiene verification. The operator can trace the ticket back to the schedule that fired it.

**Why this priority**: this is the milestone's reason to exist — recurring work becomes first-class without an external scheduler or the operator acting as the cron daemon. It exercises the schema, the tick loop, slot advancement, and the anchor, all against machinery that already ships.

**Independent Test**: seed one ticket-mode task with a near-future slot against a live stack; observe exactly one ticket land with the templated content and the scheduled-task anchor, the dispatcher spawn under normal discipline, and `next_fire_at` advanced exactly one slot.

**Acceptance Scenarios**:

1. **Given** an enabled ticket-mode task whose `next_fire_at` has arrived, **When** the tick loop runs, **Then** exactly one ticket lands in the target department's `todo` column with the rendered objective and acceptance criteria, the ticket-created notification fires on the existing channel, and `next_fire_at` advances to the next future slot.
2. **Given** the fired ticket, **When** the operator inspects it on the kanban or in the audit surface, **Then** the originating scheduled task is identifiable from the ticket's anchor.
3. **Given** the fired ticket flows to completion, **When** the agent exits through `finalize_ticket`, **Then** hygiene verification, the `agent_instances` audit row, and column transitions behave exactly as for any operator- or agent-created ticket — no scheduled-ticket special case.
4. **Given** a ticket-mode task whose previously fired ticket is still open (not yet `done`), **When** the next slot arrives, **Then** no second ticket lands; the firing is recorded as skipped-for-overlap in the task's run history and `next_fire_at` advances.

---

### User Story 2 - Oneshot cadence reflection exits through finalize_oneshot (Priority: P2)

The operator defines a "weekly drift-check" task in `oneshot` mode. At the slot the supervisor spawns the agent directly with the bounded templated brief — no ticket row, no Kanban surface. The agent reads state, writes findings, and exits through the new `finalize_oneshot` tool: structured outcome, hygiene-write verification, and the `agent_instances` audit row, minus the ticket and column transition. The outcome appears in the Recurring jobs view.

**Why this priority**: oneshot mode is the second of §M9's two committed firing modes and carries the milestone's only sealed-surface amendment (`finalize_oneshot` as a sibling of `finalize_ticket`, invoked in "Scope extensions" in m9-context.md). It depends on US1's schema and tick loop but on none of US1's ticket-side behavior.

**Independent Test**: seed one oneshot task with a near-future slot; observe the agent spawn in its container with the rendered brief, a run record holding the `finalize_oneshot` structured outcome, an `agent_instances` row, and zero new `tickets` rows.

**Acceptance Scenarios**:

1. **Given** an enabled oneshot task whose slot has arrived, **When** the tick loop fires it, **Then** the agent spawns through the existing spawn pipeline with the rendered brief, and no `tickets` row is created at any point in the firing's lifecycle.
2. **Given** the spawned agent completes its brief, **When** it calls `finalize_oneshot`, **Then** the structured outcome is committed to the firing's run record, hygiene-write verification runs against the firing's expected writes, and the `agent_instances` audit row lands — and the call carries no ticket identifier and triggers no column transition.
3. **Given** a completed firing, **When** the operator opens the Recurring jobs view, **Then** the task shows the firing in its run history with the structured outcome.
4. **Given** an oneshot task whose previous firing is still in flight, **When** the next slot arrives, **Then** no second spawn occurs; the firing is recorded as skipped-for-overlap and `next_fire_at` advances.
5. **Given** an agent spawned by an oneshot firing, **When** it attempts to call `finalize_ticket`, **Then** the call fails as it would for any caller without a valid ticket context — `finalize_ticket` semantics are untouched.

---

### User Story 3 - Operator manages schedules in the dashboard (Priority: P3)

The operator opens the Recurring jobs view to see every scheduled task — schedule, next fire, last outcome, run history — and can create, edit, pause, resume, and delete tasks through dashboard mutations, each producing an audit row.

**Why this priority**: without the CRUD surface the operator authors rows by SQL, which contradicts the milestone's "operator stops being the cron daemon" value but doesn't block US1/US2 mechanics — those are testable with seeded rows.

**Independent Test**: create a task via the dashboard, watch it fire, pause it, observe a skipped slot, resume it, observe advance-only behavior, delete it; verify one audit row per mutation and that the paused task never fired.

**Acceptance Scenarios**:

1. **Given** the dashboard, **When** the operator creates a scheduled task with a valid schedule expression and a future first slot, **Then** the task appears in the Recurring jobs view with its computed next fire time, and an audit row records the creation.
2. **Given** a paused task, **When** its slot passes, **Then** nothing fires and nothing is recorded as missed-pending-backfill; **When** the operator resumes it, **Then** `next_fire_at` is set to the next future slot and no catch-up firing occurs.
3. **Given** a create or edit attempt with a schedule below the minimum firing interval, a past-dated first slot, or a malformed expression, **When** the operator submits, **Then** the mutation is rejected with a typed validation error and no row change.
4. **Given** an existing task, **When** the operator deletes it, **Then** future firings cease, the audit row carries the task's pre-deletion state, any in-flight firing runs to its normal exit, and the task's run history remains queryable.

---

### User Story 4 - Operator schedules work conversationally via the chat CEO (Priority: P4)

The operator tells the chat CEO "run an engineering standup every weekday at 09:00." The assistant calls the new `create_scheduled_task` verb; the resulting row is identical in shape and behavior to a dashboard-created task, and the audit row anchors the chat session.

**Why this priority**: chat is a second authoring surface over machinery US1–US3 already prove; it carries the threat-model amendment obligation and ships last.

**Acceptance Scenarios**:

1. **Given** a chat session, **When** the assistant calls `create_scheduled_task` with valid arguments, **Then** the task lands with the same validation, template, and anchor behavior as a dashboard create, and the audit row carries the chat-session anchor and the verb's reversibility class.
2. **Given** a chat turn, **When** the assistant attempts more `create_scheduled_task` calls than the per-turn ceiling, **Then** the ceiling fires the established terminal-error shape (M6 `MaxTicketsPerTurn` pattern) and no further tasks land that turn.
3. **Given** the sealed verb registry, **When** the verb set is enumerated, **Then** `create_scheduled_task` is the only M9 addition, and edit/pause/delete of scheduled tasks are not callable from chat.

---

### User Story 5 - Schedules survive supervisor downtime without duplication or backfill (Priority: P5)

The supervisor goes down across one or more slots of an enabled task. On restart the task fires exactly once (the collapse rule), `next_fire_at` advances to the next future slot, and no intermediate slots are backfilled. Concurrent claim attempts never double-fire a slot.

**Why this priority**: §M9's recovery semantics are binding and must be proven, but they're meaningful only once US1/US2 firing paths exist.

**Acceptance Scenarios**:

1. **Given** an enabled task and a supervisor stopped across at least three slots, **When** the supervisor restarts, **Then** exactly one firing occurs and `next_fire_at` is future-dated with no backfill firings.
2. **Given** two concurrent claim attempts on the same due task (concurrent tick execution, or a poller racing a long-running claim), **When** both run, **Then** exactly one firing occurs per slot — the skip-locked claim discipline, testable with the M8 chaos-test shape.
3. **Given** no due tasks, **When** ticks elapse, **Then** no agent spawns and no tokens are consumed — the RATIONALE §1 zero-idle-cost invariant, asserted rather than assumed.

---

### Edge Cases

- A task is created or edited with `next_fire_at` in the past → rejected at validation (FR-105); fire-on-recovery remains exclusively a supervisor-down semantic.
- A slot arrives while the company is over its M6 daily budget or inside a rate-limit pause → the firing defers with standard `throttle_events` evidence and a typed run-record outcome; it does not silently vanish (FR-401, FR-403).
- A ticket-mode firing would exceed the target department's M8 weekly ticket budget → the firing is rejected by the existing gate, evidence lands, run record carries the typed outcome (FR-402).
- A template renders for a task that has never fired → the last-fired variable renders as an explicit "never" value, not an error or empty string (FR-107).
- A task's agent role or department is deactivated or deleted after task creation → oneshot firings fail at spawn-prep with a typed run-record outcome visible in the Recurring jobs view; ticket-mode firings surface the failure through the fired ticket's normal lifecycle (the run record stays `fired` with the ticket anchor). The task is not auto-deleted either way.
- An oneshot firing's container is missing or spawn-prep fails → typed run-record outcome; event/poll retry semantics for the reactive loop are unaffected.
- The operator deletes a task while a firing is in flight → the firing completes through its normal exit; only future firings cease.
- A paused task is edited (schedule changed) and then resumed → next fire computes from the new expression, advance-only.
- Clock skew / DST: schedule expressions are UTC-only (FR-103); no local-time slot ambiguity exists by construction.

## Requirements *(mandatory)*

### Functional Requirements

**Schema + tick loop (FR-1xx)**

- **FR-100**: The system MUST persist scheduled tasks as concrete named jobs, each carrying at minimum: a unique name, target department and role, schedule expression, computed next fire time, firing mode (`ticket` or `oneshot`), templated objective, templated acceptance criteria, and enabled/paused state. A task that does not name its objective and acceptance criteria MUST NOT be creatable — "wake up and look for work" rows are structurally impossible (§M9, binding).
- **FR-101**: A supervisor-internal tick loop MUST claim due tasks with the committed claim discipline (`FOR UPDATE SKIP LOCKED` over rows whose next fire time has arrived, per §M9) such that each due slot produces at most one firing regardless of concurrent claim attempts.
- **FR-102**: The tick interval MUST default to 30 seconds and be operator-tunable via a supervisor environment variable. A slot is considered on time if it fires within one tick interval of its scheduled time. *(Resolves context Q5.)*
- **FR-103**: Schedule expressions MUST use a bounded named-cadence vocabulary — daily at HH:MM, weekly at day-of-week + HH:MM, and every-N-minutes/hours — interpreted in UTC only. Next-fire computation happens in supervisor code. Full cron syntax is out of scope; no schedule-parsing dependency may be added (AGENTS.md locked-deps soft rule). *(Resolves Q1.)*
- **FR-104**: On recovery after downtime, a task with one or more missed slots MUST fire exactly once and advance its next fire time to the next future slot. Intermediate missed slots MUST NOT be backfilled (§M9, binding).
- **FR-105**: Task creation and edits MUST validate that the first computed slot is future-dated and that the effective firing interval is at or above the minimum firing interval (FR-404); violations are rejected with typed validation errors. *(Resolves Q9.)*
- **FR-106**: Pausing a task MUST stop firings without recording missed slots. Resuming MUST set the next fire time to the next future slot computed from the schedule expression, with no catch-up firing. *(Resolves Q7.)*
- **FR-107**: Objective and acceptance-criteria templates MUST support exactly two substitution variables — the firing timestamp and the previous firing's timestamp — rendered supervisor-side at fire time. A never-fired task renders the latter as an explicit "never" value. No other templating capability ships. *(Resolves Q2.)*
- **FR-108**: Every firing attempt — fired, skipped for overlap, deferred by a gate, or failed — MUST produce a run record linked to the task (and, where a spawn occurred, to the `agent_instances` row) carrying a typed outcome. *(Resolves Q4, Q10.)*
- **FR-109**: When no tasks are due, a tick MUST perform no spawns and consume no model tokens (RATIONALE §1 zero-idle-cost invariant).

**Ticket firing mode (FR-2xx)**

- **FR-200**: A ticket-mode firing MUST insert a ticket with the rendered objective and acceptance criteria into the target department's `todo` column and emit the existing ticket-created notification, after which all existing machinery (dispatcher dedupe, concurrency caps, M8 dependency gate, `finalize_ticket` exit, hygiene verification) applies without any scheduled-ticket special case.
- **FR-201**: The fired ticket and its audit trail MUST carry an anchor to the originating scheduled task, queryable from the dashboard's existing surfaces and from the Recurring jobs view.
- **FR-202**: A ticket-mode slot arriving while the task's previously fired ticket is still open MUST skip-and-advance: no new ticket, a `skipped_overlap`-typed run record, next fire time advanced. *(Resolves Q6, ticket half.)*

**Oneshot firing mode + finalize_oneshot (FR-3xx)**

- **FR-300**: An oneshot firing MUST spawn the target agent through the existing spawn pipeline with the rendered brief as its bounded objective, creating no ticket row and touching no Kanban machinery at any point in the firing's lifecycle (§M9, binding).
- **FR-301**: A new MCP tool `finalize_oneshot` MUST be the oneshot exit path: it accepts the structured-outcome payload shape of `finalize_ticket` minus the ticket identifier, commits the outcome to the firing's run record, triggers hygiene-write verification keyed to the firing, and lands the `agent_instances` audit row. It MUST NOT accept a ticket identifier or commit any column transition. *(Sealed-surface amendment invoked in "Scope extensions" in m9-context.md; resolves Q4.)*
- **FR-302**: `finalize_ticket`'s schema, registration, and semantics MUST be byte-for-byte unaffected; existing finalize tests pass untouched.
- **FR-303**: An oneshot slot arriving while the task's previous firing is still in flight MUST skip-and-advance with a `skipped_overlap` run record. *(Resolves Q6, oneshot half.)*
- **FR-304**: An oneshot agent's MCP surface MUST include `finalize_oneshot` and exclude `finalize_ticket`'s ticket-commit path for that spawn; the agent's brief and the per-spawn config make the expected exit unambiguous.

**Gate inheritance (FR-4xx)**

- **FR-400**: Both firing modes MUST transit the M6 cost-throttle check at spawn-prep, including the default spawn-cost estimation, exactly as reactive spawns do.
- **FR-401**: A firing deferred by the M6 gate (budget or pause) MUST write the standard `throttle_events` evidence and a typed run-record outcome, and MUST NOT be immediately re-attempted. For oneshot firings the existing poll fallback re-checks the deferred event after the gate window (the M6 back-pressure posture); a successful re-check updates the run outcome from `gate_deferred` to `fired`. Ticket-mode gate rejections are terminal for that slot (verb-level rejection precedent).
- **FR-402**: Ticket-mode firings MUST count against and be gated by the target department's M8 weekly ticket budget like any other ticket creation.
- **FR-403**: Gate-deferred and failed firings MUST be visible in the Recurring jobs view's run history as the canonical operator surface; M9 adds no hygiene-table coupling. *(Resolves Q10.)*
- **FR-404**: A global minimum firing interval (default 15 minutes, operator-tunable via environment variable) MUST be enforced at task validation, bounding the worst-case firing frequency of any single task regardless of authoring surface. *(Resolves Q8, interval half.)*

**Dashboard — Recurring jobs view + CRUD (FR-5xx)**

- **FR-500**: A Recurring jobs view MUST list every scheduled task with its schedule, next fire time, mode, enabled/paused state, last outcome, and per-task run history (the FR-108 records).
- **FR-501**: The operator MUST be able to create, edit, pause, resume, and delete scheduled tasks via dashboard Server Actions following the M8 server-action-verb precedent: verbs disjoint from the chat verb set, one audit row per mutation, validation per FR-105.
- **FR-502**: Deletion MUST capture the task's pre-deletion state in the audit row (effectively-irreversible mutation; M5.3 Tier-3 audit shape precedent), MUST NOT interrupt an in-flight firing, and MUST preserve the task's run history (deletion is a soft delete; a deleted task never fires again but its runs remain queryable).

**Chat-driven creation (FR-6xx)**

- **FR-600**: Exactly one verb, `create_scheduled_task`, MUST be added to the sealed `garrison-mutate` verb set, classified reversibility Tier 3 (creates recurring cost-incurring state; delete exists but accrued firings and spend do not reverse). Edit, pause, resume, and delete MUST NOT be chat-callable in M9. *(Resolves Q3.)*
- **FR-601**: The chat-threat-model amendment (verb row, tier-table entry, registry-test update) MUST land before any verb code, per `chat-threat-model.md` Rule 1 and its preamble.
- **FR-602**: Chat-created tasks MUST be behaviorally indistinguishable from dashboard-created tasks — same validation, template discipline, gates, and anchors — with the audit row anchoring the chat session per the M5.3 audit shape.
- **FR-603**: A per-turn ceiling on `create_scheduled_task` calls (default 3, operator-tunable via environment variable) MUST fire the established M6 per-turn-ceiling terminal-error shape when exceeded. *(Resolves Q8, ceiling half.)*

### Key Entities

- **Scheduled task**: a concrete named recurring job — identity (name, department, role), schedule expression (FR-103 vocabulary), next fire time, firing mode, templated objective and acceptance criteria, enabled/paused state, minimum-interval-validated. Owned by the supervisor's tick loop; authored by dashboard or chat.
- **Scheduled-task run**: the per-firing record (FR-108) — linked to its task and, where a spawn occurred, its `agent_instances` row; carries a typed outcome (fired / skipped overlap / gate-deferred / failed) and, for oneshot firings, the `finalize_oneshot` structured outcome. The Recurring jobs view's data source.
- **`finalize_oneshot` tool**: sibling of `finalize_ticket` — same structured-outcome discipline, hygiene verification, and audit row; no ticket identifier, no column transition (FR-301).
- **`create_scheduled_task` verb**: the single M9 addition to the sealed chat verb set, Tier 3, ceiling-bounded per turn (FR-6xx).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A seeded ticket-mode task fires on a live stack: exactly one ticket per slot, templated content rendered, anchor traceable, dispatcher pickup under normal discipline, next fire time advanced exactly one slot.
- **SC-002**: A seeded oneshot task completes the full loop — spawn in container, brief execution, `finalize_oneshot` commit, hygiene verification, audit row — with zero `tickets` rows created.
- **SC-003**: With the supervisor stopped across ≥3 slots of an enabled task, restart produces exactly one firing and a future-dated next fire time; the run history shows no backfill.
- **SC-004**: Concurrent claim execution against the same due task (M8 chaos-test shape) yields exactly one firing and one run record per slot.
- **SC-005**: A firing against an over-budget or paused company defers with `throttle_events` evidence and a typed run-record outcome; a ticket-mode firing against an exhausted department weekly budget is rejected with the same evidence discipline.
- **SC-006**: Dashboard CRUD round-trip: create → fire → pause (slot passes, nothing fires, nothing recorded as pending) → resume (advance-only) → delete (pre-state in audit row), with exactly one audit row per mutation.
- **SC-007**: Chat authoring: `create_scheduled_task` via a live chat session lands a task behaviorally identical to a dashboard-created one; the per-turn ceiling fires at call N+1; the threat-model amendment is committed in history before the verb code.
- **SC-008**: Zero idle cost: a supervisor running ≥1 hour with no due tasks records zero scheduled-task spawns and zero scheduled-task token spend.
- **SC-009**: Validation rejects: past-dated first slot, sub-minimum interval, malformed expression — each with a typed error and no row change, from both authoring surfaces.
- **SC-010**: All M2.x/M5.x/M6/M7/M8 regression suites pass untouched; `finalize_ticket` tests unchanged.

## Assumptions

- M7.1 (container execution pipeline) ships before M9 implementation; M9 spawns ride the existing spawn pipeline whose transport is container exec. M9 adds no transport work ("Sequencing assumption" in m9-context.md).
- Single-tenant alpha per operator memory `project_garrison_release_phases`; tasks key to departments (which key to a company), so per-customer scoping is not foreclosed, but no multi-tenant machinery ships.
- All schedule arithmetic is UTC; operator-local display is a dashboard rendering concern, not a scheduling semantic.
- Tests land Go-side only per the standing repo convention; dashboard surfaces are covered by the Go integration tests' shapes.
- The three tunables (tick interval 30s, minimum firing interval 15m, chat per-turn ceiling 3) are operator-adjustable defaults, not contested design decisions.
- Package layout, table/column names, and function signatures are `/garrison-plan`'s to decide; this spec binds behavior and entity relationships only.

## Resolution of context open questions

Per the `/garrison-specify` contract, every question from "Open questions the spec must resolve" in m9-context.md is resolved here, none deferred:

| Context Q | Resolution | Where |
|---|---|---|
| Q1 expression format | Bounded named-cadence vocabulary, UTC-only, computed in Go, no parser dependency | FR-103 |
| Q2 templates | Static text + two substitution variables, rendered at fire time | FR-107 |
| Q3 chat verb granularity | One verb (`create_scheduled_task`), Tier 3; edit/pause/delete dashboard-only | FR-600 |
| Q4 oneshot persistence | Per-firing run record linked to task + agent instance; hygiene keys off the firing | FR-108, FR-301 |
| Q5 tick loop | Separate ticker, 30s default, env-tunable, one-tick drift tolerance | FR-102 |
| Q6 overlap | Skip-and-advance per mode with typed `skipped_overlap` run records | FR-202, FR-303 |
| Q7 pause/resume | Advance-only; no catch-up firing on resume | FR-106 |
| Q8 runaway bound | Minimum firing interval (15m default) + chat per-turn ceiling (3 default) | FR-404, FR-603 |
| Q9 past-dated creation | Rejected at validation; recovery semantics stay supervisor-down-only | FR-105 |
| Q10 failure visibility | Run history in Recurring jobs view is canonical; typed outcomes; no hygiene coupling | FR-108, FR-403 |
