# Feature specification: M6 — CEO ticket decomposition + hygiene checks + cost-throttle

**Feature Branch**: `015-m6-decomposition-hygiene-throttle`
**Created**: 2026-05-02
**Status**: Draft
**Input**: User description: "CEO ticket decomposition + hygiene checks + cost-throttle (M6)"

**Binding context**: [`specs/_context/m6-context.md`](../_context/m6-context.md) — every section is a binding input to this spec; defaults from "Open questions the spec must resolve" are encoded below per the operator-approved answer set.

**Substrate spike**: [`docs/research/m6-spike.md`](../../docs/research/m6-spike.md) — observation-only map of what's already wired and what's missing. Cited by section number in the requirements where the substrate matters.

**Prior milestone**: M5.4 ships in PR #17 (closes the M5 arc). M6 begins from the post-merge HEAD.

---

## Clarifications

### Session 2026-05-02

- Q: Tenancy table identity for budget + pause columns — new `customers` table, view over `companies`, or rename `companies` → `customers`? → A: Add columns to existing `companies` table; rename "customers" → "companies" across the spec. Avoids a forked tenancy entity for what's structurally the same thing.
- Q: Per-spawn cost-estimate strategy for FR-032 — flat env-var default, per-role rolling average, no estimate, or hybrid? → A: Flat env-var default (`GARRISON_DEFAULT_SPAWN_COST_USD=$0.05`) for M6. Per-role refinement is a clean follow-up once US2's cost-telemetry fix has accumulated honest historical data; the M6 budget-defer query stays single-line.
- Q: Throttle-events emission — pg_notify+SSE live, periodic poll, or static page-load only? → A: pg_notify on insert + SSE bridge → live update in the hygiene throttle sub-table. Mirrors the M5.x dashboard pattern; without live updates the actionable surface lags behind the operator's chat-driven action that triggered the defer.
- Q: Parent-on-child kanban surface — chip / badge with preview / nested card / detail-panel only? → A: Leading mono parent-id chip on the child card header (`parent: <8-char-prefix>`, `text-text-3` + `font-mono` matching the existing id+age row); click navigates to the parent ticket. Cheapest visual change consistent with the M5.4 card density; zero new component.
- Q: Hygiene status display labels — raw status names, friendly labels, both, or status+tone? → A: Raw status names (`thin_diary`, `missing_kg_facts`). Matches the existing M2.2/M2.3 hygiene-table convention; keeps a 1:1 mapping between dashboard surface, supervisor logs, and `internal/hygiene/evaluator.go` source. No translation surface to maintain.

---

## User scenarios & testing *(mandatory)*

### User story 1 — Decompose a vague goal into child tickets (priority: P1)

The operator opens CEO chat, types "build me a payment system with Stripe + invoicing + dunning + admin dashboard," and the CEO decomposes the goal into N child tickets — one per substantive feature slice — written transactionally as siblings under a parent ticket. The kanban shows the parent + the children with a visible parent badge so the operator can see the tree at a glance.

**Why this priority**: This is M6's headline operator-facing value. The current loop ships single 500-char tickets with bundled acceptance criteria; agents fail or succeed as a unit. Decomposition turns "build a payment system" from one all-or-nothing engineer spawn into N tickets each with its own acceptance check, hygiene status, and retry surface.

**Independent test**: Send a multi-feature objective in CEO chat; confirm N child tickets appear on the engineering kanban with `parent_ticket_id` pointing to the parent; click into one child and see the parent linked in the metadata block.

**Acceptance scenarios**:

1. **Given** an empty CEO chat thread, **when** the operator sends an objective spanning multiple feature areas (>3 acceptance criteria implied), **then** claude calls `create_ticket` 3+ times in the same turn, each call passes a `parent_ticket_id`, and the kanban renders the parent + children together.
2. **Given** a chat turn that has already issued 10 `create_ticket` calls (the per-turn cap), **when** claude attempts an 11th, **then** the supervisor terminates the turn with `error_kind='ticket_creation_ceiling_reached'`, the first 10 tickets persist, and the assistant message shows the typed-error chip from the M5.2 error palette.
3. **Given** a chat turn that issues 5 successful `create_ticket` calls then a 6th call that fails validation (e.g. missing department_slug), **when** the verb returns `validation_failed`, **then** the first 5 tickets persist, the chat surface shows the failure chip on call 6, and subsequent calls (7+) may proceed if claude retries.

---

### User story 2 — Honest cost telemetry on successful runs (priority: P1)

Today `agent_instances.total_cost_usd` reads `$0.00` on every clean-finalize run because the supervisor signal-kills claude before the `result` event lands (`docs/issues/cost-telemetry-blind-spot.md`). M6 fixes the signal-handling so the `result` event lands and the column reads the actual cost on success, not just on failure modes.

**Why this priority**: Operator-approved answer Q11 makes this a foundation deliverable for thread C. Every other cost-based feature in M6 (the daily budget actuator, the spend dashboard tile, throttle audit accuracy) sits on top of an honest cost column. Without this fix, the daily-budget check is fundamentally broken on the success path — the very path that should accumulate spend.

**Independent test**: Run any successful agent (existing engineer hello-world flow), then `SELECT total_cost_usd FROM agent_instances WHERE id = ...` and observe a non-zero value. Compare against a sequence of 10 successful runs to confirm the recorded sum is within ±5% of the operator's claude account dashboard for the same window.

**Acceptance scenarios**:

1. **Given** a clean engineer run that calls `finalize_ticket` successfully, **when** the agent_instances row reaches terminal status, **then** `total_cost_usd` is non-zero and matches the value carried in claude's terminal `result` event.
2. **Given** an engineer run that exits via a failure mode (`finalize_never_called`, `budget_exceeded`, `claude_error`), **when** the row reaches terminal status, **then** `total_cost_usd` is recorded as it is today (no regression).
3. **Given** the supervisor receives SIGTERM during an in-flight spawn, **when** the spawn shutdown path runs, **then** the supervisor terminal-write grace honors the existing M1 `TerminalWriteGrace` window (no longer-running shutdown).

---

### User story 3 — Company-scoped daily cost budget (priority: P2)

Each company carries a `daily_budget_usd` amount. The supervisor's spawn-prep transaction sums today's `agent_instances.total_cost_usd` for the company; if the running total + the conservative-estimated next spawn cost would exceed the budget, the spawn is deferred and a `throttle_events` row is written. The dashboard's hygiene surface shows recent throttle events so the operator can see what was deferred and why.

**Why this priority**: Company-scoped spend cap is the safety floor for shipping decomposition. Without it, a single CEO-chat-driven decomposition that fans out to 50 child tickets could burn the company's daily claude budget before the operator sees it. P2 because US1 (decomposition) and US2 (telemetry fix) are the load-bearing dependencies — this story ships once those land.

**Independent test**: Set `companies.daily_budget_usd = 1.00` for a test company; trigger a sequence of spawns that exceeds $1.00 in real cost; confirm the next spawn is deferred (event_outbox row stays unprocessed, agent_instances row is not created), a `throttle_events` row is written with `kind='company_budget_exceeded'`, and the next day's spawns proceed.

**Acceptance scenarios**:

1. **Given** `companies.daily_budget_usd = 1.00` and the rolling-24h cost sum is $0.95, **when** an event_outbox row arrives that would conservatively cost $0.10, **then** the spawn is deferred, `throttle_events` records the deferral, and the event row stays unprocessed for the next poll.
2. **Given** `companies.daily_budget_usd IS NULL`, **when** any number of spawns arrive, **then** no budget check fires (null budget = no throttle).
3. **Given** the rolling-24h cost sum has dropped below the budget (24+ hours after the trigger), **when** the deferred event_outbox row gets re-checked on the next poll, **then** the spawn proceeds normally.

---

### User story 4 — Rate-limit back-off pauses spawns per company (priority: P2)

When claude returns a `rate_limit_event` with `status='rejected'` during any spawn for company X, the supervisor sets `companies.pause_until = now() + back_off_seconds` (default 60s). Subsequent spawns for company X are deferred until `now() >= pause_until`. Other companies' spawns continue uninterrupted. In-flight spawns are not killed.

**Why this priority**: Today the supervisor keeps spawning until each call individually fails with rate-limit. This wastes spawn-prep work, fills the chat error surface with redundant rate-limit chips, and burns negligible-but-real cost on the rejected calls. M6 turns observation into action with a small per-company pause column.

**Independent test**: Simulate a `rate_limit_event` from a mock claude (or wait for a real one); confirm subsequent spawns for the same company get deferred for `back_off_seconds`; confirm spawns for a different company continue.

**Acceptance scenarios**:

1. **Given** a rate-limit event lands for company X with `status='rejected'`, **when** the supervisor processes it, **then** `companies.pause_until` is set 60 seconds in the future and a `throttle_events` row records the pause.
2. **Given** `companies.pause_until > now()` for company X, **when** an event_outbox row for company X is polled, **then** the spawn is deferred (event row stays unprocessed) and the supervisor logs the deferral.
3. **Given** `companies.pause_until > now()` for company X, **when** an event_outbox row arrives for company Y, **then** company Y's spawn proceeds normally.
4. **Given** the pause window has elapsed (`now() >= pause_until`), **when** the deferred event row is re-polled, **then** the spawn proceeds.

---

### User story 5 — Hygiene dashboard surfaces thin and missing-KG diaries (priority: P3)

`/hygiene` already lists non-clean rows. M6 extends it on three axes: a new `thin_diary` status when the diary length is below the configured threshold; activation of the `missing_kg_facts` evaluator path (the status was in the vocabulary but unwritten); a sub-tab split that reads "agent failures" by default so operator-drag audit rows don't dilute the actionable view.

**Why this priority**: Operator visibility into decomposition quality. Once US1 ships, the operator has more child tickets and more diaries to track; the hygiene surface needs to surface "decomposition produced thin work" and "agent shipped without writing KG facts" without forcing the operator to read every diary by hand.

**Independent test**: Force a successful finalize with a 50-char diary body; confirm the row appears in `/hygiene` with `hygiene_status='thin_diary'`. Force a successful finalize with no kg_query writes; confirm `hygiene_status='missing_kg_facts'`. Open `/hygiene` and confirm the three-tab strip (agent failures / operator audit / all) renders, with "agent failures" as the default view.

**Acceptance scenarios**:

1. **Given** a finalized ticket whose diary text is below `GARRISON_HYGIENE_THIN_DIARY_THRESHOLD` (default 200 chars), **when** the hygiene listener evaluates the row, **then** `hygiene_status='thin_diary'` and the row appears in the "agent failures" sub-tab.
2. **Given** a finalized ticket whose post-finalize `mempalace_kg_query` returns zero triples, **when** the hygiene listener evaluates the row, **then** `hygiene_status='missing_kg_facts'`.
3. **Given** a row with `hygiene_status='operator_initiated'`, **when** the operator opens `/hygiene` with the default sub-tab active, **then** the row does NOT appear; **when** the operator switches to "operator audit", **then** the row appears.

---

### Edge cases

- **Decomposition into a department with `concurrency_cap=0`** (paused). Children land in the `tickets` table; spawns are deferred indefinitely until the cap is raised. The kanban renders them; the chat shows the create-success chips. No special handling.
- **Parent ticket transitioned to `done` while a child is still in `in_dev`.** The parent is fully closed; children retain their `parent_ticket_id` linkage but no rollup is computed. The hygiene table treats children independently.
- **Decomposition spans multiple departments.** Out of scope per the context's "Not a cross-department decomposition primitive." All children must share the parent's department; `create_ticket` validation rejects mismatched departments under a parent.
- **`pause_until` arrives at a value in the past (clock-skew or stale state).** Treated as not-paused; the spawn proceeds. Idempotent — the next rate-limit event re-arms the pause.
- **A spawn already in-flight when `pause_until` is set.** The in-flight spawn continues to its terminal state; pause applies to subsequent event_outbox rows only.
- **Company with no rows in `agent_instances` for the day.** Rolling-24h sum is $0. Budget check passes trivially.
- **Concurrent rate-limit events for the same company.** Last write wins on `pause_until`; `throttle_events` records every event so the audit shows both. No locking needed — the column is single-writer per supervisor.
- **Hygiene threshold tuned at runtime** (env-var change requires supervisor restart per AGENTS.md "agents.changed listener" precedent — env-vars don't hot-reload). Documented; not a code path.
- **A diary that grows to threshold via post-finalize edits.** Out of scope — diaries are write-once at finalize per M2.2.1; later edits would be a separate milestone concern.
- **`companies` table doesn't exist yet.** Flagged in §"Items to clarify" — the spec assumes the table exists and adds two columns; if it doesn't, the plan introduces it.

---

## Requirements *(mandatory)*

### Functional requirements

#### Thread A — CEO ticket decomposition

- **FR-001**: The `garrison-mutate.create_ticket` verb MUST accept an optional `parent_ticket_id` argument (UUID).
- **FR-002**: When `parent_ticket_id` is supplied, the verb MUST validate that the parent ticket exists, is in the same `department_id` as the child, and is not already in `column_slug='done'`. Validation failure returns the existing `validation_failed` error shape (FR-422).
- **FR-003**: The supervisor MUST persist the linkage in a new typed column `tickets.parent_ticket_id UUID NULLABLE REFERENCES tickets(id)`, indexed for child-by-parent lookups.
- **FR-004**: The chat policy MUST cap `create_ticket` invocations per turn at `GARRISON_CHAT_MAX_TICKETS_PER_TURN` (default 10). Exceeding the cap terminates the turn with `error_kind='ticket_creation_ceiling_reached'` (mirrors the M5.3 `tool_call_ceiling_reached` lifecycle).
- **FR-005**: When the per-turn cap fires, tickets created earlier in the turn MUST persist (no rollback) and remain auditable through `chat_mutation_audit` rows.
- **FR-006**: The CEO chat policy's system prompt MUST include guidance for when to decompose (objective spans multiple verticals; >3 acceptance-criteria items; estimated effort >1 spawn). The guidance is operator-tunable via the existing `agents.agent_md` surface for the CEO role.
- **FR-007**: The kanban ticket-card MUST render a leading mono parent-id chip on every child ticket (`parent: <8-char-prefix>`, `text-text-3` + `font-mono`, on the existing id-and-age header row). The chip is clickable and navigates to the parent ticket's detail surface. Cards without a parent (`parent_ticket_id IS NULL`) render as today.

#### Thread B — Hygiene dashboard extension

- **FR-010**: The supervisor MUST evaluate diary length at finalize commit and write `ticket_transitions.hygiene_status='thin_diary'` when the body is shorter than `GARRISON_HYGIENE_THIN_DIARY_THRESHOLD` characters (default 200). Evaluation is deterministic — no LLM call.
- **FR-011**: The supervisor MUST add a `missing_kg_facts` evaluator path. After finalize, the hygiene listener queries the palace via `mempalace_kg_query` keyed on the ticket id; if zero triples returned, write `hygiene_status='missing_kg_facts'`. The status was already declared in the M2.2.2 vocabulary; M6 wires the writer.
- **FR-012**: The hygiene dashboard surface MUST present three tabs over the existing rows: "agent failures" (default; shows non-clean rows whose status is NOT `operator_initiated`), "operator audit" (shows `operator_initiated` only), "all" (no status filter). Status values render as raw column strings (`thin_diary`, `missing_kg_facts`, `clean`, etc.) matching the existing M2.2/M2.3 convention — no friendly-label translation layer.
- **FR-013**: The hygiene table MUST surface `parent_ticket_id` on each row so the operator can identify a parent's children at a glance. Rendering follows the same mono parent-id chip pattern as FR-007 (`parent: <8-char-prefix>`, clickable).
- **FR-014**: The thin-diary threshold MUST be runtime-configurable via the supervisor env var `GARRISON_HYGIENE_THIN_DIARY_THRESHOLD`; changes take effect on the next supervisor restart (no hot-reload).

#### Thread C — Cost-telemetry fix + cost throttle + rate-limit back-off

- **FR-020**: The supervisor MUST land claude's terminal `result` event before signal-killing the subprocess on a clean finalize. The cost-telemetry blind spot at `docs/issues/cost-telemetry-blind-spot.md` is closed: `agent_instances.total_cost_usd` reads non-zero on every clean-finalize run.
- **FR-021**: The fix MUST NOT regress failure-mode cost recording (`finalize_never_called`, `budget_exceeded`, `claude_error` continue to record cost as today).
- **FR-022**: The fix MUST NOT extend the supervisor SIGTERM grace beyond the existing M1 `TerminalWriteGrace` window. Shutdown semantics are unchanged.
- **FR-030**: The supervisor MUST add a `companies.daily_budget_usd NUMERIC(10,2) NULLABLE` column. NULL = no budget; positive value = enforced cap; zero = full pause (semantically equivalent to `concurrency_cap=0` for that axis).
- **FR-031**: The supervisor's spawn-prep transaction MUST compute the company's rolling-24h cost (sum of `agent_instances.total_cost_usd` for runs whose `started_at` is within the last 24 hours) and compare against `daily_budget_usd`. If `current_sum + estimated_next_spawn > budget`, the spawn is deferred; the event_outbox row stays unprocessed.
- **FR-032**: The estimated-next-spawn cost MUST use a flat env-var default `GARRISON_DEFAULT_SPAWN_COST_USD` (default $0.05) for every spawn regardless of role. Per-role rolling-average refinement is deferred to a post-M6 polish round once US2's honest telemetry has accumulated historical data per role.
- **FR-033**: When a budget defer fires, the supervisor MUST write a row to a new `throttle_events` table with `kind='company_budget_exceeded'`, the company id, the timestamp, and a JSONB payload carrying `{daily_budget_usd, current_sum, estimated_next}` for forensics.
- **FR-040**: The supervisor MUST add a `companies.pause_until TIMESTAMPTZ NULLABLE` column. When a `rate_limit_event` lands with `status='rejected'` during any spawn for company X, the supervisor sets `pause_until = now() + GARRISON_RATE_LIMIT_BACK_OFF_SECONDS` (default 60).
- **FR-041**: The supervisor's spawn-prep transaction MUST check `pause_until` before claiming an event_outbox row. If `pause_until > now()` for the row's company, defer; otherwise proceed. NULL `pause_until` = no pause.
- **FR-042**: When a rate-limit pause fires, the supervisor MUST write a `throttle_events` row with `kind='rate_limit_pause'`.
- **FR-043**: In-flight spawns MUST NOT be killed by a rate-limit pause; only subsequent event_outbox rows are deferred.
- **FR-044**: The dashboard's `/hygiene` surface MUST render a "throttle events" sub-table fed by the `throttle_events` table, ordered by `fired_at DESC`, capped at the same N as the existing hygiene-table page size.
- **FR-045**: The supervisor MUST emit `pg_notify('work.throttle.event', payload)` in the same transaction as every `throttle_events` INSERT, where `payload` carries `{event_id, company_id, kind, fired_at}` (matches the `work.ticket.*` channel-payload shape). The dashboard subscribes via an SSE bridge so the throttle sub-table updates live without manual refresh; bridge wiring follows the M5.1/M5.2 chat-SSE pattern (per-route LISTEN connection, payload forwarded as an SSE event).

#### Cross-thread

- **FR-050**: The `tickets.parent_ticket_id` schema addition MUST NOT require updates to any existing row. The column is nullable; existing tickets default to NULL.
- **FR-051**: The `companies.daily_budget_usd` and `companies.pause_until` schema additions MUST NOT require updates to any existing row. Both columns are nullable; existing companies default to NULL semantics (no budget, no pause).
- **FR-052**: ARCHITECTURE.md MUST be amended in the same PR as M6 ship: the M6 paragraph annotated with shipped status; the schema section annotated with the three new columns + the `throttle_events` table; a substring-match assertion test in the dashboard pins the amendment (matches the M5.x `architecture-amendment.test.ts` precedent).

### Key entities

- **Parent ticket linkage**: `tickets.parent_ticket_id` (nullable UUID, foreign key to `tickets.id`). Children share the parent's `department_id`. No rollup logic — the hygiene surface treats children independently.
- **Company budget knob**: `companies.daily_budget_usd` (nullable NUMERIC(10,2)). NULL = no enforcement; otherwise the spawn-prep transaction defers when the rolling-24h cost would exceed the budget.
- **Company pause window**: `companies.pause_until` (nullable TIMESTAMPTZ). NULL or past timestamp = no pause; future timestamp = subsequent event_outbox rows for this company are deferred.
- **Throttle event audit**: `throttle_events (id, company_id, kind, fired_at, payload JSONB)`. Append-only, indexed on `(company_id, fired_at DESC)`. Surfaces in the hygiene dashboard's throttle sub-table.
- **Hygiene status vocabulary** (extending the M2.2.2 set): existing `clean / pending / missing_diary / missing_kg_facts / suspected_secret_emitted / finalize_failed / finalize_partial / stuck / operator_initiated`, plus M6's `thin_diary`. The `missing_kg_facts` status was previously declarative-only; M6 activates the writer path.

---

## Success criteria *(mandatory)*

### Measurable outcomes

- **SC-001**: For a corpus of 20 successful clean-finalize runs after FR-020 lands, the recorded `agent_instances.total_cost_usd` sum is within ±5% of the operator's claude account dashboard total for the same window. (Cost-telemetry blind-spot closed.)
- **SC-002**: A CEO chat turn that issues 11 `create_ticket` calls terminates with `error_kind='ticket_creation_ceiling_reached'` in fewer than 2 seconds from the 11th call (the M5.3 ceiling-reached lifecycle benchmark).
- **SC-003**: A finalized ticket whose diary body is 50 characters renders in the `/hygiene` dashboard's "agent failures" tab with `hygiene_status='thin_diary'` within one supervisor poll cycle of finalize commit (default 5s).
- **SC-004**: With `companies.daily_budget_usd = 1.00` and a pre-loaded $0.95 rolling-24h sum, a 21st spawn whose estimated cost is $0.10 is deferred (not started) within the spawn-prep transaction; the event_outbox row remains unprocessed for the next poll; a `throttle_events` row with `kind='company_budget_exceeded'` is visible in the hygiene throttle sub-table within one render cycle.
- **SC-005**: A `rate_limit_event` with `status='rejected'` for company X causes the next event_outbox row for company X (arriving within the back-off window) to be deferred. An event_outbox row for company Y arriving in the same window proceeds to spawn within one poll cycle.
- **SC-006**: The hygiene dashboard's three-tab split renders correctly across page reload, sub-tab switch, and filter combinations with the same Playwright assertion shape as M5.4's test surface (no new test framework introduced).
- **SC-007**: The dashboard `architecture-amendment.test.ts` substring-match assertion includes the M6 amendment lines (three new columns + `throttle_events` table) and passes on M6 ship.

---

## Assumptions

- **The `companies` table exists in the current schema with at least `id` and `name` columns.** The M5.3 `chat_sessions.started_by_user_id` and `tickets.department_id → departments.company_id → companies` chain implies a tenancy model. If a `companies` table doesn't exist (vs. `companies`), M6 either adds it or the budget/pause columns land on `companies` instead. Flagged in "Items to clarify."
- **The cost-telemetry fix (FR-020) is implementable without rearchitecting the M2.x finalize signal-handling path.** The issue doc characterizes the root cause as a signal-kill ordering issue; the fix is presumed to be a localized change in `internal/spawn` shutdown ordering. If the fix turns out to require a deeper rewrite, the plan re-scopes.
- **`mempalace_kg_query` is reliably callable from the hygiene listener path.** The M5.4 retro confirms the kg_query MCP surface is alive end-to-end. M6 reuses it; no new palace-side work.
- **Operator manages `companies.daily_budget_usd` via SQL.** No dashboard mutation surface for budgets in M6 — the column exists but is admin-edited. Per the context's "Not a UI for editing customer budgets" non-goal.
- **One claude OAuth token per customer.** This is the M5.1 deployment shape today; the rate-limit-back-off semantics rely on it (a rate-limit-from-claude is per-token, which the supervisor maps to per-company). If the deployment ever serves multiple companies off one token, the rate-limit pause becomes overly broad. Documented; not a code path to pre-handle.
- **Per-day = rolling 24h window, not calendar-day.** Avoids the midnight-cliff edge case where every company's spawns simultaneously unblock at 00:00.
- **Decomposition guidance lives in `agents.agent_md` for the CEO role**, not in supervisor code. Operator tunes via the existing edit-agent surface (FR-100). Constants like the per-turn cap live in env vars, not in agent_md.
- **No regression on the existing `/hygiene` page.** The three-tab split is additive; the existing rows render in the new "all" tab without changes.
- **AGENTS.md active-milestone pointer is flipped to M6 by the operator at M6 implementation kickoff** (not by this spec). Pre-existing pointer staleness flagged in the M6 context commit.

---

## Items to clarify *(post-spec, before plan)*

These survived the binding-input pass and need operator input or schema-state confirmation before `/garrison-plan`:

1. ~~Tenancy table identity~~ — **Closed 2026-05-02 by /speckit-clarify Q1**: the existing `companies` table receives the budget + pause columns (M2.1 schema, `id / name / created_at`). No new table.
2. ~~Hygiene-extension status display labels~~ — **Closed 2026-05-02 by /speckit-clarify Q5**: raw status names; matches existing M2.2/M2.3 convention.
3. ~~Parent-on-child kanban surfacing~~ — **Closed 2026-05-02 by /speckit-clarify Q4**: leading mono parent-id chip (`parent: <8-char-prefix>`) on the child card header row.
4. ~~Per-role cost estimate for FR-032~~ — **Closed 2026-05-02 by /speckit-clarify Q2**: flat env-var default for M6; per-role refinement deferred to post-M6 polish.

---

## Out of scope

Mirrors the context doc's "Out of scope" section verbatim — repeated here so the spec is self-contained:

- Per-agent custom skills, per-agent Docker runtime, immutable agent prompt-hardening preamble, SkillHub integration → M7.
- Cross-department dependencies, agent-spawned tickets, MCP server registry → M8.
- LLM-quality-scored hygiene check (separate research thread).
- Multi-tenant cost-budget UI (operator manages via SQL until a future milestone).
- Auto-merging completed child tickets back into a parent (M8 territory).
- Per-thread context-token counter on the chat surface (deferred from M5.4).
- Verb-set extension (the M5.3 8-verb seal stays; Q1 chose N-individual-calls).
- Per-department or per-agent-type cost budget (only per-company in M6; finer-grained budgets layer on later).
- Cost-prediction model (running 24h sum is the comparison; no forecasting).
- A UI to raise a budget mid-day (operator edits the column directly until a future milestone).
- Backwards-compat migration burden (every new column is nullable, every new table is independent).
- Cross-department decomposition primitive (parent + children must share a department).

---

## Non-goals (explicit)

Mirrors the context doc's "What this milestone is NOT" section:

- Not a verb-set extension.
- Not a per-agent runtime change.
- Not a UI for editing customer budgets.
- Not a multi-customer dashboard fan-out.
- Not a cost-prediction model.
- Not a hygiene LLM-judge.
- Not a chat-side throttle change.
- Not a backwards-compat migration burden.
- Not a cross-department decomposition primitive.
