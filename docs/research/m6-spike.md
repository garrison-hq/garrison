# M6 ticket-decomposition + hygiene + cost-throttle spike

**Status**: research, not normative. Findings here inform the M6 context doc; the context's open questions cite specific sections of this spike.
**Date**: 2026-05-02
**Branch**: `014-m6-m7-spikes`
**Tooling**: post-M5.4 supervisor (`supervisor/cmd/supervisor/main.go`), dashboard 0.x.

## Why a spike before the context

`ARCHITECTURE.md` defines M6 as three loosely-coupled threads:
1. **CEO ticket decomposition** — chat writes tickets from conversation.
2. **Hygiene dashboard** — surface thin/missing writes to operators.
3. **Cost / rate-limit throttling** — M2.1 *observes* the events; M6 *acts* on them.

Each thread has a substrate already in the codebase from prior milestones, but none of the M6-specific actions (auto-decompose long objectives, surface thin diaries, throttle on rate-limit) have a wire today. The spike's job is to map exactly what's there, what M6 has to add, and what's still unknown.

---

## §1 — Existing CEO-driven ticket creation (M5.3 baseline)

**Already wired** (do not redesign):

- `supervisor/internal/garrisonmutate/verbs_tickets.go` — sealed 8-verb MCP set including `create_ticket`, `edit_ticket`, `transition_ticket`, `pause_agent`, `resume_agent`, `spawn_agent`, `edit_agent_config`, `propose_hire`. Each verb is a single Postgres transaction with a `chat_mutation_audit` row written in the same tx (Rule 6 from the M2.3 vault threat model: every mutation is audit-traceable).
- `supervisor/internal/chat/policy.go:emitToolUseFromRaw` — emits the `chat.tool.use` SSE frame the dashboard's ToolCallChip subscribes to. Verb invocation is observable per-turn.
- Dashboard `lib/actions/tickets.ts:createTicket` — operator-driven path. M5.3 chat-driven path goes through the verb instead, not this server action.
- `chat_mutation_audit` table — every chat verb invocation lands a row keyed by `chat_session_id` + `chat_message_id` for forensics. Schema at `migrations/20260430000000_m5_3_chat_driven_mutations.sql`.

**What M6 has to add**:

- *Decomposition* — today a CEO chat that says "build a payment system" can call `create_ticket` once with a 500-char objective. M6's claim is that the CEO should split this into N child tickets (probably one per acceptance-criteria line) and write them as a transactional set. There's no `create_tickets_bulk` verb today — needs to be added to the sealed set, OR M6 commits to "always loop on create_ticket" with each call audited individually. Open question: §5 Q1.
- *Parent/child linkage* — `tickets` schema has no `parent_ticket_id` column today. `metadata JSONB` is the natural escape hatch (`metadata->>'parent_ticket_id'`) but a typed column is cleaner if decomposition becomes load-bearing. Open question: §5 Q2.

---

## §2 — Hygiene status: writers, vocabulary, current visibility

**Writers** (every mutation that touches `ticket_transitions.hygiene_status`):

- `supervisor/internal/spawn/finalize.go` — atomic finalize writer; sets `hygiene_status='clean'` on a successful finalize commit. Hygiene-status set inside the same tx as the diary write (FR-264 / FR-265a).
- `supervisor/internal/hygiene/listener.go` — listens on `work.ticket.transitioned.engineering.qa_review` (M2.2) + `work.ticket.transitioned.engineering.qa_review.done`, evaluates whether the expected diary writes actually landed (`internal/hygiene/evaluator.go`), and writes the terminal status via the at-most-once-to-terminal UPDATE.
- M5.4 retro added the `transitioned.todo.in_dev` and `transitioned.qa_review.in_dev` channels for engineer dispatch but **not** for hygiene evaluation. Hygiene only fires on the qa_review→done step today.
- Dashboard `lib/actions/tickets.ts:moveTicket` — operator drags set `hygiene_status='operator_initiated'` (FR-027 audit trail, not a real check).

**Vocabulary** (post-M2.2.2):

| Status | Meaning | Source |
|---|---|---|
| `clean` | finalize commit landed with all expected writes present | finalize.go |
| `pending` | initial state, evaluator hasn't run yet | trigger default |
| `missing_diary` | transition fired, no palace diary entry within the window | hygiene listener |
| `missing_kg_facts` | diary present, kg_query yielded no facts | hygiene listener |
| `suspected_secret_emitted` | leak-scan flagged the diary body | finalize.go (M2.3) |
| `finalize_failed` / `finalize_partial` / `stuck` | M2.2.1 atomic-write failure modes | finalize.go |
| `operator_initiated` | operator drag (no agent involved) | dashboard |

**Current dashboard visibility**:

- `/hygiene` page exists (`dashboard/app/[locale]/(app)/hygiene/page.tsx`).
- `HygieneTable`, `FailureModeFilter`, `PatternCategoryFilter` components ship.
- Query: `lib/queries/hygiene.ts` — selects rows where status is non-clean. Ordered by recent first, capped at N rows.
- Sidebar carries a Hygiene nav entry with an unread-count badge (already in `Sidebar.tsx`).

**What M6 has to add**:

- A "thin write" predicate distinct from "missing write." Today `missing_diary` only fires when the diary row is absent. A diary that exists but is short/templated/uninformative reads "clean" today. Open question: §5 Q3 — does M6 need a second evaluator pass that scores diary quality, or is that a research thread out of scope?
- A surfacing mechanism for `missing_kg_facts` — the column already supports the value but the evaluator never writes it (the `Evaluate` path hardcodes a pre-M2.3 vocabulary). Verify before designing the M6 dashboard surface. Open question: §5 Q4.

---

## §3 — Rate-limit + cost telemetry: where it's recorded today

**Rate-limit events** (claude wire-format `rate_limit_event`):

- `supervisor/internal/spawn/pipeline.go:OnRateLimit` — logs `rate_limit_event` with status (`rejected` / `using_overage`), rate-limit-type, total_cost_usd. Sets `p.rateLimitOverage=true` if rejected. Does NOT throttle the spawn directly.
- `supervisor/internal/chat/policy.go:OnRateLimit` — same pattern for chat turns; logs and sets a flag.
- Both paths *observe* but neither *acts* — the supervisor doesn't pause spawns or back off when a rate-limit fires. The flag is consumed only at terminal-classification time.

**Cost telemetry**:

- Per-spawn (ticket execution): `agent_instances.total_cost_usd` written from claude's terminal `result` event. Rolled into department / org dashboards.
- Per-message (chat): `chat_messages.cost_usd` written on terminal commit (`policy.go:CommitAssistantTerminal`).
- Per-thread (chat): `chat_sessions.total_cost_usd` rolled up via `RollUpSessionCost` after every message commit.
- Per-thread cost cap: `chat_sessions.total_cost_usd >= cap` is checked at spawn-prep time (`internal/chat/persistence.go:42`); if over, the spawn is rejected with `error_kind='session_cost_cap_reached'`. The cap is supplied via `Deps` (clarify Q5 in chat code).
- Dashboard cost surfaces: per-thread badge in ChatSessionView, per-department roll-up in OrgOverview, no global throttle visualization.

**What M6 has to add**:

- *Rate-limit back-off* — when `rate_limit_event` carries `status='rejected'`, the supervisor should pause new spawns (department-scoped or global?) for the rate-limit window. Today the supervisor keeps spawning until claude refuses each one individually. Open question: §5 Q5 — back-off granularity (per-org / per-customer / per-claude-token).
- *Cost-based throttling* — M5.x has *thread-scoped* cost caps. M6's claim per ARCHITECTURE.md is that throttle decisions land here; means a per-org / per-day rolling budget that pauses ticket spawns (not just chat). Open question: §5 Q6 — does M6 introduce a new `customers.daily_budget_usd` column, or reuse department concurrency_cap as the throttle dial?

---

## §4 — What M6 inherits cleanly from M5.x

Mostly positive — M6 is composition, not new infrastructure:

- The garrison-mutate verb set is sealed and audited; M6 builds atop with no schema rework on the verb side.
- The chat-driven invocation pipeline (chat → MCP → verb → Postgres + audit) is the same pipeline operator-driven UI uses; the dashboard's surface for hygiene + budget can read the SAME `chat_mutation_audit` table the verbs already write to.
- The hygiene listener's at-most-once-to-terminal UPDATE pattern is directly extensible — M6 can add new predicates without redesigning the lifecycle.

What M6 does *not* inherit:

- A "decompose" verb. Adding it requires a sealed-set extension which is a non-trivial spec amendment (M5.3 deliberately froze the set at 8 — see `docs/security/chat-threat-model.md` Rule 1).
- Parent-child ticket relationships at the schema level.
- Any throttle actuator. Today everything throttle-shaped is an observation-only counter.

---

## §5 — Open questions for the M6 context doc

1. **Decomposition transactionality** — does M6 add a `create_tickets_bulk` verb (one tx, all-or-nothing) or commit to N individual `create_ticket` calls (the chat policy bears the consistency burden)? The verb-set freeze is the binding constraint; if we add to the set, the threat-model amendment is part of M6 scope.
2. **Parent/child schema** — typed column (`tickets.parent_ticket_id UUID`) or `metadata->>'parent_ticket_id'`? Touches: ARCHITECTURE schema diagram, the M5.3 verb's argument set, the hygiene path (a parent's hygiene_status might roll up from children).
3. **"Thin diary" predicate** — score a diary's informativeness, or hold this for a future milestone? If in M6 scope, the score function needs to be deterministic (no LLM call inside the hygiene listener — it runs every transition).
4. **`missing_kg_facts` activation** — verify the evaluator can produce this status today, or is the column purely declarative? If declarative, M6 has to add the evaluator path.
5. **Rate-limit back-off scope** — per-org pause? Per-customer? Per-token? Is the "pause" implemented via department concurrency_cap=0 (existing primitive) or a new `pause_until TIMESTAMPTZ` column?
6. **Cost-budget shape** — daily? weekly? per-customer? shared with the chat-thread cap, or independent? The answer dictates whether `chat_sessions.total_cost_usd` is the right rollup or we need a new aggregate.
7. **CEO-as-decomposer threat model** — if the CEO can issue N tickets from one chat turn, what's the per-turn cap? The M5.3 tool-call ceiling is set at 50; a decomposition that exceeds it should fail closed.
8. **Hygiene surface for `operator_initiated`** — these rows are NOT failures, they're audit. Today they show up in the hygiene table mixed with real failures. M6 should filter them (or move them to an "audit" sub-tab).

---

## §6 — Things this spike does NOT cover (pre-context-doc work)

- Empirical cost-throttle thresholds. The math depends on the customer's claude account cost ceiling — needs a stakeholder input, not a code change.
- The dashboard UX for the hygiene-extension surface (separate question for the design pass; spec-kit specs typically scaffold this from the M6 context doc + a UI mock).
- Test data for decomposition correctness — depends on §5 Q1 + Q2 outcomes.

These are flagged so the M6 plan doc can scope them explicitly.
