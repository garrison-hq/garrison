# M6 — CEO ticket decomposition + hygiene + cost-throttle (context)

**Status**: context for `/speckit.specify`. M5.4 shipping in PR #17 (post-merge HEAD becomes the M6 substrate); M6 work begins after #17 merges.

**Prior milestone**: M5.4 retro lands at `docs/retros/m5-4.md` post-ship. M5 closes after M5.4; **M6 is the first milestone in the post-M5 arc**. M7 (hiring flow + per-agent runtime + immutable preamble) is the milestone immediately after — see `docs/research/m7-spike.md`.

**Binding inputs** (read before specifying):
- [`ARCHITECTURE.md`](../../ARCHITECTURE.md) — M6 paragraph: "CEO ticket decomposition + hygiene checks. CEO writes tickets from conversation. Hygiene dashboard shows thin/missing writes. Rate-limit back-off and cost-based throttling land here (M2.1 observes cost and rate-limit events; M6 acts on them)."
- [`docs/research/m6-spike.md`](../../docs/research/m6-spike.md) — substrate map. The spike identified 8 open questions; this context doc closes some with leans and forwards the rest to `/speckit.clarify`.
- [`docs/retros/m5-3.md`](../../docs/retros/m5-3.md) — M5.3 sealed the 8-verb garrison-mutate set. Whether M6 re-opens that seal is a binding question (Open Q1).
- [`docs/security/chat-threat-model.md`](../../docs/security/chat-threat-model.md) — M5.3 amended threat model. Rule 1 (sealed verb set) is the precedent any M6 verb-set extension must amend.
- [`docs/retros/m5-4.md`](../../docs/retros/m5-4.md) — once it lands, captures M5.4 surface area + the live-stack discoveries that surfaced during the smoke walk (todo backlog vs auto-spawn, transition channels, chip persistence). Feeds the hygiene-extension thread.
- [`AGENTS.md`](../../AGENTS.md) — locked-deps soft rule, concurrency rules, spec-kit workflow.
- `migrations/20260430000000_m5_3_chat_driven_mutations.sql` — current verb-set surface + audit row schema.
- `supervisor/internal/spawn/pipeline.go:OnRateLimit` + `supervisor/internal/chat/policy.go:OnRateLimit` — current rate-limit telemetry observation points.
- `supervisor/internal/chat/persistence.go:42` — current per-thread cost cap implementation, the only existing throttle actuator in the codebase.

---

## Scope deviation from committed docs

**No scope deviation.** ARCHITECTURE.md's M6 paragraph commits all three threads; M6 is composition of M5.x substrate, not new infrastructure. Two things to call out:

1. **Schema additions are minimal.** The biggest deviation from the M5.4 ship pattern: M6 likely needs a `tickets.parent_ticket_id UUID` column (Open Q2) and possibly a `customers.daily_budget_usd NUMERIC` column (Open Q6 lean). Both are additive — no destructive migration.

2. **Verb-set extension is conditional.** Open Q1 asks whether M6 adds a `create_tickets_bulk` verb to the sealed set. If yes, this is the first M6 amendment to the M5.3 chat-threat-model.md. The amendment shape is set by M5.3's precedent (extend the doc; pin the new verb's threat-model row; bump the verb count from 8 to 9).

---

## Why this milestone now

M5.x shipped a CEO-chat surface where the operator (acting as CEO) talks to claude, claude calls the 8 garrison-mutate verbs, and the dashboard surfaces every action. The current loop:
- CEO says "build me a payment system."
- Claude calls `create_ticket` once with a 500-char objective.
- An engineer agent spawns, runs against an objective that bundles a dozen acceptance criteria, and either succeeds or fails as a unit.

That loop is unit-of-work-too-large. The same chat substrate, with M6's three threads in place, becomes:
- CEO says "build me a payment system."
- Claude decomposes the goal into N child tickets (one per acceptance criterion or feature slice), writes them transactionally with a parent linkage, and the operator sees the tree on the kanban.
- The hygiene dashboard surfaces tickets where the agent shipped a thin / missing diary so the operator can re-prompt or re-decompose.
- A rate-limit event from the claude API pauses new spawns at the right scope (per-customer / per-org) until the limit resets, instead of letting the supervisor keep slamming claude until each spawn fails individually. Cost throttling enforces a daily budget the same way.

Each thread is small in isolation; the value comes from shipping all three at once. The hygiene surface is what tells the operator decomposition is going wrong; the throttle is what keeps a runaway decomposition from costing $1000 in claude credits before lunch.

---

## In scope

### A — CEO ticket decomposition

The CEO chat path can already issue N `create_ticket` calls per turn (M5.3 left no per-turn ticket-creation cap besides the 50-tool-call ceiling). M6 promotes "N individual creates" to a first-class decomposition primitive:

- **Parent → child linkage.** New optional argument `parent_ticket_id` on `create_ticket` (UUID). The verb validates the parent exists in the same department + is not already in `done`. Child tickets render on the kanban with a visible parent badge.
- **Decomposition prompt scaffolding.** The chat policy's system prompt grows a section explaining when to decompose (objective spans multiple verticals; acceptance_criteria has >3 distinct items; estimated effort >1 spawn). Operator-tunable via the existing agent_md surface for the CEO role.
- **Per-turn ticket-creation cap.** New chat-policy ceiling — `MaxTicketsPerTurn` env var, default 10 (well below the 50-tool-call limit). Prevents a runaway decomposition. Threshold-fired terminal-error matches the M5.3 `tool_call_ceiling_reached` pattern (`assistant_error` SSE frame, terminal commit with `error_kind='ticket_creation_ceiling_reached'`).

**Out of scope for thread A**: an LLM-as-judge that scores decomposition quality (future milestone); auto-merging child tickets back into a parent on completion (M8 cross-department dependencies territory).

### B — Hygiene dashboard extension

`/hygiene` already exists (M3) and shows non-clean rows. M6 extends it on three axes:

- **`missing_kg_facts` evaluator activation.** The status value is in the vocabulary but no evaluator path writes it today. M6 adds the evaluator: post-finalize, query `mempalace_kg_query` with the ticket id; if zero triples, set the row's `hygiene_status='missing_kg_facts'`.
- **Thin-diary predicate.** New status: `thin_diary`. Triggered when the diary length is below a threshold (default 200 chars; tunable via supervisor env var). Deterministic, cheap to compute — no LLM call. Surfaces decomposition-without-substance: a child ticket where the agent finalized but didn't actually do meaningful work.
- **Operator-initiated row filtering.** Today `operator_initiated` rows (operator drags) appear in the hygiene table mixed with real failures. M6 adds a sub-tab: "operator audit" (the existing audit data) vs "agent failures" (the M2.x hygiene values) vs "all". Default view: agent failures.

### C — Rate-limit + cost throttle actuator

M2.1 observes `rate_limit_event`; M5.x observes `chat_sessions.total_cost_usd` and rejects new chat spawns at a thread-scoped cap. Neither pauses ticket-execution agent spawns. M6 introduces:

- **Rate-limit back-off.** When a `rate_limit_event` with `status='rejected'` lands during any spawn, the supervisor sets a per-customer `pause_until TIMESTAMPTZ` value (new column on `customers`). All new ticket-execution spawns for that customer are deferred until `now() >= pause_until`. Existing in-flight spawns continue. Default back-off: 60s (matching the typical claude rate-limit window).
- **Daily cost budget.** New column `customers.daily_budget_usd NUMERIC(10,2)`. The supervisor's spawn-prep transaction sums today's `agent_instances.total_cost_usd` for the customer; if `current + estimated_next > budget`, defer the spawn and write a `customer_budget_exceeded` row to a new `throttle_events` audit table. Idempotent — the deferred event_outbox row gets re-checked on the next poll.
- **Surfacing.** The dashboard's org-overview page gains a "today's spend" tile per customer (existing cost-rollup query, new aggregation). Throttle-events render as a hygiene-table peer (sub-table) so the operator sees throttle activity without leaving the hygiene surface.

**Out of scope for thread C**: per-department budget (could be added later atop the per-customer column); rate-limit handling for chat spawns (M5.3 already handles this via `session_cost_cap_reached`; rate-limit-vs-cost are observably distinct already); a UI to *raise* a budget mid-day (operator edits the column directly via SQL or the agents/edit surface; no dashboard mutation surface in M6).

---

## Out of scope

- Per-agent custom skills (M7).
- Per-agent Docker runtime (M7).
- Immutable agent prompt-hardening preamble (M7).
- SkillHub integration (M7).
- Cross-department dependencies (M8).
- Agent-spawned tickets (M8).
- MCP server registry (M8).
- An LLM-quality-scored hygiene check (separate research thread; M6 ships only deterministic predicates).
- A multi-tenant cost-budget UI (operator manages via SQL until a future milestone introduces a customer-admin surface).
- Auto-merging completed child tickets back to a parent (M8 territory).
- A per-thread context-token counter on the chat surface (deferred from M5.4).

---

## Binding inputs (read first)

| Document | Why it's binding |
|---|---|
| `ARCHITECTURE.md` (M6 paragraph + M5/M6 sequencing notes) | Defines the three threads M6 must close |
| `docs/research/m6-spike.md` | Substrate map; 8 open questions (see Open Q section below) |
| `docs/retros/m5-3.md` | Sealed verb-set precedent; the verb-set freeze applies until amended |
| `docs/security/chat-threat-model.md` | M5.3 threat model; any verb addition requires amendment + threat-model row |
| `docs/retros/m5-4.md` (post-ship) | M5.4 live-stack discoveries (todo column semantics, dispatch channels, chip persistence) — affects hygiene-table extension copy + filter shape |
| `AGENTS.md` | Locked-deps soft rule, concurrency, spec-kit workflow |
| `migrations/20260430000000_m5_3_chat_driven_mutations.sql` | M5.3 verb + audit schema as-shipped |
| `migrations/20260424000005_m2_2_1_finalize_ticket.sql` + `20260424000006_m2_2_2_compliance_calibration.sql` | Hygiene-status writers + vocabulary |
| `supervisor/internal/spawn/pipeline.go` `OnRateLimit` | Existing rate-limit observation; M6's actuator extends this |
| `supervisor/internal/chat/persistence.go` (cost-cap branch) | Existing per-thread cost cap; M6's actuator parallels this for tickets |

---

## Open questions the spec must resolve

The M6 spike enumerated 8. Some are leaned closed below; others go to `/speckit.clarify`.

1. **Decomposition transactionality** — `create_tickets_bulk` verb (one tx, all-or-nothing) or N individual `create_ticket` calls (chat policy bears consistency burden)? **Default lean: N individual calls.** Adding a 9th verb to the sealed set requires a chat-threat-model.md amendment + a threat-row + extra audit shape; the value of all-or-nothing is small compared to the operator's ability to see partial-progress and cancel via `transition_ticket(to=done, hygiene_status=cancelled)`. Spec confirms; if the operator wants the bulk verb, scope the threat-model amendment as a separate sub-deliverable.

2. **Parent/child schema** — typed column (`tickets.parent_ticket_id UUID NULLABLE REFERENCES tickets(id)`) or `metadata->>'parent_ticket_id'` JSONB shim? **Default lean: typed column.** Hygiene rollup ("does any child have `missing_diary`?") becomes a simple SQL query against an indexed FK; metadata-shim makes that a JSONB walk. Spec confirms.

3. **Thin-diary threshold** — what character count is "thin"? **Default lean: 200 chars, supervisor env var `GARRISON_HYGIENE_THIN_DIARY_THRESHOLD` (default 200).** 200 is roughly two short paragraphs — below that the diary is almost always template-quality. Operator can tune. Spec confirms.

4. **`missing_kg_facts` evaluator activation** — confirm the column path is alive end-to-end, or pure declarative-only today? **Default lean: declarative only today; M6 wires the evaluator.** Verify in spec by reading `internal/hygiene/evaluator.go` against the vocabulary.

5. **Rate-limit back-off scope** — per-org? per-customer? per-token? **Default lean: per-customer.** Garrison's deployment story is multi-tenant-from-day-1; a rate-limit on customer A's claude account shouldn't pause customer B's spawns. Per-token is the same as per-customer in our auth model (one OAuth token per customer). Spec confirms.

6. **Cost-budget shape** — daily / weekly / per-customer? **Default lean: per-customer + daily, rolling 24h window.** Daily aligns with claude's billing rhythm; rolling avoids the "all spawns deferred at midnight" edge case. New column `customers.daily_budget_usd NUMERIC(10,2)`. Spec confirms.

7. **CEO-as-decomposer per-turn cap** — what's the max number of `create_ticket` calls in a single chat turn? **Default lean: 10.** Below the M5.3 50-tool-call ceiling so claude can still call read tools (mempalace_search, postgres) while decomposing. Configurable via env (`GARRISON_CHAT_MAX_TICKETS_PER_TURN`). Spec confirms.

8. **Hygiene-table filter for `operator_initiated`** — filter out by default, sub-tab, or keep mixed? **Default lean: sub-tab with three views (agent failures / operator audit / all).** Default view is "agent failures" so the table reads as "things I need to act on." Spec confirms.

Two additional open questions surface from drafting this context:

9. **Decomposition rollback semantics.** If claude calls `create_ticket` 5 times and the 6th call fails (validation error, rate-limit, ceiling reached), do the first 5 stay? **Default lean: yes, stay.** Each verb invocation is its own audited transaction; the chat policy can mark the turn as `error_kind='ticket_creation_ceiling_reached'` but the audit trail keeps the partial work visible. Operator can manually delete via `transition_ticket(to=cancelled)` if desired. Spec confirms.

10. **Throttle-event audit table.** New table `throttle_events (id, customer_id, kind, fired_at, payload JSONB)` for the cost-budget + rate-limit pause records? **Default lean: yes, single table.** Mirrors the `chat_mutation_audit` precedent — the actuator's mutations are audit-shaped, not control-flow-shaped. Spec confirms.

---

## Acceptance-criteria framing

Not enumerating ACs — that's the spec's job. The framing the spec works within:

- **Operator can decompose a vague goal.** CEO chat: "build me a payment system." Claude decomposes into N child tickets with `parent_ticket_id` pointing to a parent ticket; kanban surfaces the tree.
- **Per-turn ticket-creation cap fires correctly.** A chat turn that tries to create 11 tickets (default cap = 10) terminates with `error_kind='ticket_creation_ceiling_reached'`; the first 10 land + are audited.
- **Hygiene table surfaces thin diaries.** A finalized ticket whose diary length is < 200 chars shows up in `/hygiene` with `hygiene_status='thin_diary'`. Operator can filter to "agent failures" and see only thin/missing rows, not operator drags.
- **Hygiene table surfaces missing KG facts.** A finalized ticket whose `mempalace_kg_query` returns zero triples shows up with `hygiene_status='missing_kg_facts'`.
- **Rate-limit pauses spawns per-customer.** A rate-limit event during any spawn sets `customers.pause_until = now() + 60s`; new event_outbox rows for that customer are deferred until `now() >= pause_until` and re-attempt cleanly. Other customers' spawns continue uninterrupted.
- **Daily budget pauses spawns.** A spawn-prep transaction that would push today's customer cost over `daily_budget_usd` defers the row + writes a `throttle_events` audit row + surfaces in `/hygiene`'s throttle sub-table.
- **Architecture amendment lands + is pinned by test.** ARCHITECTURE.md M6 paragraph annotated with shipped status; the three columns added (`tickets.parent_ticket_id`, `customers.daily_budget_usd`, `customers.pause_until`) and the `throttle_events` table reflected in the schema section; substring-match test pins the amendment.

---

## What this milestone is NOT

- **Not** a verb-set extension (default — Open Q1 closes via N-individual-calls). The 8-verb seal stays unless the operator overrides Q1's lean.
- **Not** a per-agent runtime change. M6 ships on the existing direct-exec spawn path; per-agent containerization is M7.
- **Not** a UI for editing customer budgets. Operator updates `customers.daily_budget_usd` via SQL until M8-or-later introduces a customer-admin surface.
- **Not** a multi-customer dashboard at all. Garrison's M6 dashboard still assumes one customer per deployment for visualization purposes; the schema accommodates multi-tenant but the UI doesn't fan out.
- **Not** a cost-prediction model. The budget check uses the running 24h sum, not a forecast. Spawns that complete cheaply land cheaply; spawns that turn out to be expensive trigger the next defer.
- **Not** a hygiene LLM-judge. Thin-diary is a deterministic char-count predicate. Quality scoring is a separate research thread.
- **Not** a chat-side throttle change. Chat already has `session_cost_cap_reached`; M6 doesn't touch the chat throttle path.
- **Not** a backwards-compat migration burden. The `parent_ticket_id` column is nullable, defaulting to null; existing tickets have no parent. Existing departments have no budget — they get null + the supervisor falls through to "no budget" semantics until an operator sets one.
- **Not** a cross-department decomposition primitive. A parent ticket and its children must share a department. Cross-department dependencies are M8.

---

## Spec-kit flow for M6

1. **First**: PR #17 (M5.4) merges to main. M6 work begins from the post-merge HEAD.
2. **Branch**: `git checkout -b 015-m6-decomposition-hygiene-throttle main`. Numbering follows the existing 003-014 sequence.
3. **No pre-spec spike needed** — the M6 spike (`docs/research/m6-spike.md`) is already on branch `014-m6-m7-spikes`. That branch merges or rebases into main alongside the M6 working branch; the spike doc is a binding input to the spec.
4. `/speckit.constitution` — already populated; reuse.
5. `/speckit.specify` — drafts `specs/015-m6-decomposition-hygiene-throttle/spec.md` from this context + the spike findings.
6. `/speckit.clarify` — resolves the 10 open questions above plus anything the spec surfaces. Operator confirms or overrides each lean.
7. `/garrison-plan m6` — implementation plan.
8. `/garrison-tasks m6` — task list.
9. `/speckit.analyze` — cross-artifact consistency.
10. `/garrison-implement m6` — execute.
11. **Then**: M6 retro at `docs/retros/m6.md`, palace mirror, ARCHITECTURE.md amendment + test pin landed in the same PR. M7 (hiring flow + per-agent runtime + immutable preamble) starts from this substrate.
