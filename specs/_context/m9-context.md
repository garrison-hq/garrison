# M9 — Scheduled / triggered wake-ups (heartbeat) (context)

**Status**: context for `/speckit.specify`. M8 shipped 2026-05-11
(branch `018-m8-zero-human-loop`); M7.1 (real container execution
pipeline) is in flight on branch `020-m7-1-container-exec` and is
assumed to ship before M9 implementation begins — see §Sequencing
assumption below. Written 2026-06-10.

**Prior milestone**: M8 retro at [`docs/retros/m8.md`](../../docs/retros/m8.md).
M8 closed the event-driven zero-human loop: agents create tickets,
cross-department dependencies gate spawns structurally, the
per-department weekly budget bounds runaway fan-out, and MCPJungle
fronts third-party MCP servers. Everything in that loop is still
**reactive** — nothing happens until an operator or an agent emits an
event.

**Binding inputs** (read before specifying; full annotations in
§Binding inputs below): `ARCHITECTURE.md` §M9 paragraph,
`RATIONALE.md` §1, `docs/security/chat-threat-model.md`,
`docs/retros/m8.md`, `specs/_context/m8-context.md`,
`docs/retros/m6.md`, `specs/_context/m6-context.md`,
`docs/retros/m7.md` (§Forward-looking),
`specs/_context/m7-1-context.md`.

**No spike.** RATIONALE §13's spike-first rule applies when a
milestone depends on how an external tool actually behaves. M9 is
internal Go + Postgres + dashboard composition — tick loop, firing
modes, CRUD — built entirely from in-tree patterns (M1's poll
fallback, M2.2.1's finalize discipline, M6's throttle gate, M5.3/M8's
verb machinery). Operator confirmed 2026-06-10: no spike.

---

## Scope extensions beyond ARCHITECTURE §M9

ARCHITECTURE.md §M9 commits the core design (tick loop, collapse
semantics, two firing modes, `finalize_oneshot`, concrete-named-jobs
discipline). Two pieces of M9's operator-confirmed scope go beyond
what §M9 writes down. Neither contradicts a committed doc; both
follow established amendment shapes:

1. **Chat-driven scheduled-task creation.** §M9 is silent on how
   `scheduled_tasks` rows get created. M9 ships both operator-driven
   creation (dashboard CRUD) and chat-driven creation (the operator
   asks the chat CEO to "run a standup every morning"). Chat-driven
   creation adds verb(s) to the sealed M5.3 `garrison-mutate` verb
   set, which per `chat-threat-model.md` Rule 1 and its preamble
   requires the threat-model amendment (per-verb threat rows +
   reversibility tiers + registry-test update) to land **before any
   code lands**. Same amendment shape as M6's parent-linkage
   consideration and M8's agent-caller extension.

2. **`finalize_oneshot` extends the sealed finalize surface.**
   AGENTS.md lists the finalize tool schema among sealed M2 surfaces,
   amendable only by the active milestone's context file. This
   context explicitly invokes that amendment right: M9 adds
   `finalize_oneshot` as a **sibling** MCP tool per §M9 — parallel to
   `finalize_ticket`, no `ticket_id`, no column transition, with
   structured outcome, hygiene-write verification, and the
   `agent_instances` audit row unchanged. `finalize_ticket`'s own
   schema and semantics are NOT touched.

Also worth squaring explicitly so the spec doesn't trip on it:
RATIONALE §1 says "no polling loops, no scheduled heartbeats." That
decision rejected the earlier scheduled-heartbeat setup's failure
mode — *per-agent token-burning wake cycles that mostly found
nothing*. M9's tick loop is a supervisor-side SQL poll (zero idle
token cost, same shape as M1's `processed_at IS NULL` fallback that
already runs in production), and every firing is a concrete named job
with a bounded objective. ARCHITECTURE §M9 was committed with exactly
this distinction; M9 must preserve it (see §What this milestone is
NOT).

---

## Why this milestone now

M8 finished the reactive loop. The remaining structural gap in the
alpha story — "a standing force operates on its own cadence rather
than only reacting" (ARCHITECTURE §M9) — is that all recurring
operational work still needs an external poke:

1. **Cadence work isn't first-class.** A daily standup, a weekly
   drift-check, a recurring metric report each currently require the
   operator to open chat and ask, or an external scheduler to insert
   rows into the database. The operator IS the cron daemon.

2. **The substrate is ready.** M7/M7.1 provide the per-agent
   container runtime the proactive spawn path rides (`docs/retros/m7.md`
   §Forward-looking commits `scheduled_tasks` to the same
   `Controller.Exec` primitive). M6's cost-throttle and M8's runaway
   control bound what a misconfigured schedule can burn. M2.2.1's
   finalize discipline gives every firing a structured exit. None of
   that existed when the scheduled-heartbeat approach was rejected;
   the discipline that makes scheduled work safe now exists.

3. **Alpha completeness.** Per operator memory
   `project_garrison_release_phases`, alpha = "prove it operates."
   An org that only reacts is half-proven; the daily-standup /
   weekly-drift-check cadence is the demonstration that the org runs
   itself between operator sessions.

### Sequencing assumption

M7.1 ships before M9 implementation begins. M9's spawn path (both
modes) is the existing spawn pipeline; by M9 time its transport is
container exec (`UseDirectExec=false`). M9 adds **no new transport
work** — if M7.1 slips, M9's spec still holds (the spawn pipeline
abstracts the transport), but acceptance runs against the container
path.

---

## In scope

### `scheduled_tasks` schema + supervisor tick loop

- New `scheduled_tasks` table. Each row is a concrete named job:
  identity (name, department, role), schedule expression,
  `next_fire_at`, firing mode (`ticket` | `oneshot`), templated
  objective + acceptance criteria, enabled/paused state. Exact
  column shape is the spec's to write; the §M9 invariants are
  binding: rows are bounded named jobs, never "wake up and look for
  work."
- Tick loop inside the supervisor — same architectural slot as M1's
  `processed_at IS NULL` poll fallback. Claim query is committed by
  §M9: `SELECT … FROM scheduled_tasks WHERE next_fire_at <= now()
  FOR UPDATE SKIP LOCKED`.
- **Fire-on-recovery with collapse** (binding, §M9): a slot missed
  while the supervisor was down fires exactly once on recovery;
  `next_fire_at` advances to the next future slot; intermediate
  missed slots are never backfilled.

### `ticket` firing mode

- A firing inserts a `tickets` row (templated objective + acceptance
  criteria, target department, `column_slug='todo'`) and lets the
  normal machinery take over: `pg_notify('work.ticket.created.…')`,
  dispatcher dedupe, concurrency cap, M8 dependency gate, M2.2.1
  `finalize_ticket` exit. No new Kanban machinery.
- The ticket/audit row carries an anchor back to the originating
  `scheduled_tasks` row so the operator can trace "this ticket exists
  because that schedule fired."

### `oneshot` firing mode + `finalize_oneshot`

- A firing spawns the agent directly with the bounded templated
  brief: no ticket row, no column transitions, no Kanban surface
  (§M9). Used for cadence reflections — read state, write findings,
  exit.
- New sibling MCP tool `finalize_oneshot` (sealed-surface amendment
  invoked in §Scope extensions above): structured outcome,
  hygiene-write verification, `agent_instances` audit row — minus
  `ticket_id` and column transition.
- Oneshot outcomes surface in the Recurring-jobs dashboard view (next
  section), not the Kanban.

### Gate inheritance (both modes)

Per §M9, both modes inherit unchanged: M6 per-company cost-throttle
at spawn-prep (including `DefaultSpawnCostUSD` estimation), M8
runaway control where a ticket row is created, M2.x hygiene-write
verification on exit, and the `agent_instances` audit row. The only
machinery skipped in oneshot mode is the ticket row and column hops.
A firing deferred by a gate writes the same `throttle_events`
evidence any other deferred spawn writes.

### Dashboard — Recurring-jobs view + full CRUD

- New "Recurring jobs" view (§M9 commits the view for oneshot
  surfacing; M9 scope makes it the home for all scheduled tasks):
  per-task schedule, next fire, last outcome, firing history.
- Full CRUD via Server Actions: create, edit, pause/resume, delete.
  Follows the M8 `ServerActionVerbs` precedent — Server-Action verbs
  disjoint from chat-side verbs, audit rows per mutation.
- Per the standing repo convention (`feedback_test_scope_go_only`),
  tests for M9 land on the Go side; dashboard surfaces are covered by
  the Go integration tests' shapes.

### Chat-driven creation

- The operator can create scheduled tasks conversationally via the
  chat CEO. New `garrison-mutate` verb(s) — granularity is an open
  question (Q3) — with the chat-threat-model amendment landing before
  code per Rule 1.
- Chat-created tasks are identical rows to dashboard-created ones:
  same template discipline, same gates, same audit anchoring. Chat is
  a second authoring surface, not a second kind of task.

---

## Out of scope

Listed explicitly so the spec doesn't drift:

1. **Generic agent wake-ups.** "Summon an agent and let it look for
   work" is rejected by §M9, with reasons (audit/hygiene/cost gaps).
   Not a deferral — a rejection.
2. **Backfill semantics.** Explicitly rejected by §M9. A standup
   missed five days fires once on recovery, not five times.
3. **Subagent token-budget inheritance** (M8 retro SC-008, tagged
   "M9+"). Deferred again — not scheduled-wake-up work. Rides the
   M9+ tag forward.
4. **Per-MCP-server cost telemetry** (M8 retro, tagged "M9+").
   Deferred again, same reasoning.
5. **External scheduler integration** (system cron, CI triggers,
   webhook-driven schedules). The tick loop is in-process; external
   triggers remain a non-goal.
6. **Multi-tenant scheduling.** Single-tenant alpha per
   `project_garrison_release_phases`. The schema should not foreclose
   per-customer scoping (tasks already key to a department, which
   keys to a company), but no multi-tenant machinery ships.
7. **M7.1b MCPJungle-gateway migration**, per-customer egress
   proxies, per-agent networks — M7.1's deferral list carries
   through untouched.
8. **Mutating sealed surfaces beyond the two named amendments** —
   `finalize_ticket` schema, vault rules, spawn pipeline contract,
   hiring flow, M7 preamble, MCPJungle naming convention all carry
   unchanged.

---

## Binding inputs

| Document | Why it binds |
|---|---|
| `ARCHITECTURE.md` §M9 | The committed design: tick-loop placement, claim query, fire-on-recovery-with-collapse, two firing modes, `finalize_oneshot`, concrete-named-jobs discipline, backfill rejection. The spec elaborates this; it does not re-decide it. |
| `RATIONALE.md` §1 | The zero-idle-cost rationale that killed the earlier scheduled-heartbeat setup. M9's tick must stay token-free at idle; any design that has agents waking to check for work violates this. |
| `docs/security/chat-threat-model.md` | Rule 1 sealed verb set + the "amend before code lands" preamble. Binds the chat-driven-creation thread: new verb rows, reversibility tiers, registry-test updates. |
| `docs/retros/m8.md` + `specs/_context/m8-context.md` | Runaway-gate mechanics the ticket mode inherits; `ServerActionVerbs` disjointness precedent for the CRUD surface; the "M9+" deferral list this context disposes of (out-of-scope items 3–4); dept-weekly gate counts ALL tickets in the window — scheduled ticket firings will count against department budgets. |
| `docs/retros/m6.md` + `specs/_context/m6-context.md` | Throttle gate (`throttle.Check` at spawn-prep), `DefaultSpawnCostUSD` estimation, back-pressure-not-back-off posture for gate-deferred work, hygiene predicates oneshot exits must satisfy. |
| `docs/retros/m7.md` §Forward-looking | Commits the proactive spawn path to the same per-agent container + `Controller.Exec` primitive as the reactive path. |
| `specs/_context/m7-1-context.md` | The container-exec pipeline M9 spawns ride (in flight). M9 adds no transport work; the sequencing assumption above. |
| `AGENTS.md` | Sealed-surfaces list (finalize amendment invoked here), concurrency rules 4/7/8, locked-deps soft rule (a cron-expression parser would be a new dependency — see Q1), spec-kit workflow. |
| Operator memory `project_garrison_release_phases` | Alpha line: prove it operates; multi-tenant and governance rollback are beta. |

---

## Open questions the spec must resolve

Resolve via `/speckit.clarify` or pin as deferred-with-explicit-
fallback. Not pre-decided here.

1. **Schedule expression format.** Full cron syntax vs a bounded
   named-cadence vocabulary (daily@HH:MM, weekly@DOW, every-N-hours)
   vs interval-only. Full cron likely means a parser dependency
   (locked-deps soft rule applies: justify or avoid); a bounded
   vocabulary is parseable with stdlib. Also: timezone semantics —
   UTC-only vs operator-local — and where `next_fire_at` computation
   lives (Go vs SQL).
2. **Template mechanism.** Are objective/acceptance-criteria
   templates static text per row, or parameterized (fire date, window
   since last firing)? If parameterized, what's the substitution
   vocabulary and where does it render?
3. **Chat verb granularity.** One `create_scheduled_task` verb only
   (edit/pause/delete stay dashboard-only), or a full verb family?
   Each verb costs a threat-model row + reversibility tier + tests.
   Delete is the Tier-3-shaped candidate (pre-state snapshot);
   creation of a recurring cost-incurring job may itself warrant
   Tier 3 treatment — spec argues the tiers.
4. **Oneshot outcome persistence.** Where does `finalize_oneshot`'s
   structured outcome land — a new table keyed to the
   `scheduled_tasks` row, or reuse of `agent_instances` payload
   columns? How does hygiene verification key its expected writes
   without a `ticket_id`?
5. **Tick loop integration.** Extend the M1 poll-fallback ticker or
   run a separate ticker? What interval, and what drift tolerance is
   acceptable between `next_fire_at` and actual fire time?
6. **Overlap policy.** A task's slot arrives while its previous
   firing (or its previously created ticket) is still in flight /
   still open. Fire anyway, skip-and-advance, or queue? §M9's
   collapse rule covers supervisor-down; this is overlap-while-up.
   Per-mode answers may differ (an open standup ticket from yesterday
   vs a still-running oneshot).
7. **Pause/resume semantics.** A task resumed after being paused
   across N slots — does the collapse rule apply (fire once on
   resume) or does it simply advance to the next future slot with no
   firing? Lean: advance-only (pause expresses "I don't want these"),
   but spec decides.
8. **Runaway bound on scheduling itself.** M6/M8 gates bound each
   firing, and the dept-weekly budget counts ticket-mode firings, but
   oneshot firings create no ticket and a chat-injected
   `create_scheduled_task` could mint a high-frequency burner. Does
   M9 need a per-task or global firing-frequency floor/cap (e.g.
   minimum interval), and is task-creation itself ceiling-bounded in
   chat like M6's `MaxTicketsPerTurn`?
9. **Past-dated creation.** A task created with `next_fire_at`
   already in the past — fire immediately on the next tick, or
   require the first slot to be future-dated at validation time?
10. **Failure visibility.** A firing that fails at spawn-prep
    (throttle defer, container missing) — where does the operator see
    "your standup didn't run this morning," beyond `throttle_events`?
    Recurring-jobs view's last-outcome column, hygiene surface, or
    both?

---

## Acceptance criteria framing

Detailed criteria belong in the spec; frame them along these axes:

- **Ticket-mode lifecycle**: a scheduled task with a near-future slot
  fires once — a `tickets` row lands with the templated objective and
  the scheduled-task anchor, `pg_notify` fires, the dispatcher spawns
  under normal dedupe/cap discipline, the agent exits through
  `finalize_ticket`, and `next_fire_at` has advanced exactly one
  slot.
- **Oneshot lifecycle**: a firing spawns the agent in its container
  with the bounded brief; the agent exits through `finalize_oneshot`;
  structured outcome + hygiene verification + `agent_instances` row
  land; no `tickets` row exists; the outcome is visible in the
  Recurring-jobs view.
- **Collapse on recovery**: with the supervisor stopped across ≥3
  slots of a task, restart produces exactly one firing and a
  future-dated `next_fire_at` with no backfill rows.
- **Claim idempotency**: concurrent tick execution (two pollers, or
  poller racing a long-running claim) produces exactly one firing per
  slot — the `FOR UPDATE SKIP LOCKED` discipline, testable with the
  M8 chaos-test shape.
- **Gates hold**: a firing whose company is over budget / paused
  defers with the standard `throttle_events` evidence and does not
  silently vanish; a ticket-mode firing counts against the
  department's weekly budget.
- **CRUD + audit**: operator creates, edits, pauses, and deletes via
  the dashboard with an audit row per mutation; a paused task does
  not fire.
- **Chat authoring**: the chat CEO creates a scheduled task via the
  amended verb; the audit row anchors the chat session; the resulting
  row is indistinguishable in behavior from a dashboard-created one;
  the threat-model amendment is committed before the verb code.
- **Zero idle cost**: with no due tasks, the tick loop spawns
  nothing and consumes no tokens — the RATIONALE §1 invariant,
  asserted, not assumed.

---

## What this milestone is NOT

- NOT a return to the scheduled-heartbeat approach RATIONALE §1
  rejected. No per-agent wake cycles, no "check the queue and go
  back to sleep," no token spend at idle. Every firing is a concrete
  named job.
- NOT a workflow engine. No DAGs of scheduled tasks, no
  task-triggers-task chaining. M8's ticket dependencies remain the
  only inter-work-item structure.
- NOT a backfill system. Rejected, not deferred.
- NOT an external-scheduler integration. The tick lives in the
  supervisor.
- NOT a spawn-pipeline change. Both modes feed the existing pipeline
  (claudeproto, budget, finalize observer, adjudication, typed exit
  reasons — sealed per M7.1). Oneshot differs only in the brief
  source and the finalize sibling.
- NOT a re-opening of `finalize_ticket`. `finalize_oneshot` is a
  sibling tool; the existing tool's schema, tests, and semantics
  carry byte-for-byte.
- NOT a multi-tenant ship, per the standing alpha line.

---

## Spec-kit flow for M9

1. **Branch first** (AGENTS.md step 0): `NNN-m9-scheduled-wakeups`
   off `main` after the M7.1 PR merges.
2. **`/speckit.constitution`** — already populated; no M9 amendments
   anticipated.
3. **`/garrison-specify m9`** — spec against this context. Thread
   structure mirrors §In scope.
4. **`/speckit.clarify`** — burn down Q1–Q10. Likely operator-
   preference deferrals: Q1 (expression format), Q7 (pause
   semantics).
5. **Pre-implementation housekeeping**: the chat-threat-model
   amendment (new verb rows + tiers) lands before implement, per
   Rule 1 — pre-plan housekeeping commit or alongside the plan, as
   M8 did with the registry-candidates amendments. The
   AGENTS.md sealed-surface note for `finalize_oneshot` lands in the
   same pass.
6. **`/garrison-plan m9`**, **`/garrison-tasks m9`**,
   **`/speckit.analyze`**, **`/garrison-implement m9`** — standard
   cycle. Coverage target ≥82% on new Go code; lint locally before
   pushing; Go-side tests only per repo convention.
7. **Retro** — `docs/retros/m9.md` + MemPalace drawer mirror per the
   M3+ dual-deliverable policy. Retro must answer: did the zero-idle-
   cost invariant hold in practice? Did any schedule misfire or
   double-fire? Did the collapse rule behave on a real supervisor
   restart? Did the chat-authoring path stay inside the amended
   threat model?
