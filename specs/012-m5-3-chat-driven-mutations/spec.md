# Feature Specification: M5.3 — Chat-driven mutations (autonomous-execution posture)

**Feature Branch**: `012-m5-3-chat-driven-mutations`
**Created**: 2026-04-29
**Status**: Draft
**Input**: M5.3 context (`specs/_context/m5-3-context.md`); M5.2 retro (`docs/retros/m5-2.md`); M5.1 retro + spec (`docs/retros/m5-1.md`, `specs/010-m5-1-ceo-chat-backend/spec.md`); M5 spike (`docs/research/m5-spike.md`); vault threat model (`docs/security/vault-threat-model.md`); `internal/finalize` precedent (`docs/retros/m2-2-1.md`).
**Scope marker**: chat-driven mutations under an autonomous-execution posture. Sits on M5.1's runtime + M5.2's frontend without revising either. Adds the `garrison-mutate` MCP server, the chat threat-model amendment, the inline tool-call chip surface, and per-verb activity-feed event rendering. M5.4 takes the knowledge-base "WHAT THE CEO KNOWS" pane; M7 extends `propose_hire` into the full hiring flow.

---

## Clarifications

### Session 2026-04-29 (operator-approved during /garrison-specify)

Sixteen binding decisions resolved before spec drafting; they shape the FRs below rather than appearing as inline `[NEEDS CLARIFICATION]` markers.

- Q: Where does the chat threat-model amendment live? → A: New file at `docs/security/chat-threat-model.md`, structured the same way `docs/security/vault-threat-model.md` is (assets / adversaries / threats addressed / threats accepted / numbered architectural rules / open questions / retro questions). Cleaner separation from vault; chat and vault are different attack surfaces.
- Q: Where does the mutation tooling live? → A: New supervisor-owned in-tree Go MCP server at `supervisor mcp garrison-mutate` (option 1 from spike §4). Mirrors `mcp finalize` precedent — in-tree, single subcommand, in-process JSON-RPC, atomic Postgres-write per call, audit row + `pg_notify` per call. Constitution principle III + AGENTS.md `internal/finalize` activation favour this over HTTP-call-tools (option 2) or shared-library refactor (option 3).
- Q: Where do tool-call chips read their data from? → A: Live render from the SSE delta stream (`tool_use` / `tool_result` events arriving via `/api/sse/chat`). Reconnect + replay reads from `chat_messages.raw_event_envelope` per the M5.2 amended FR-261 row-state-read mechanism.
- Q: How is each mutation verb classified for reversibility? → A: Three tiers, recorded in the threat-model amendment's reversibility table and surfaced in the audit table's `reversibility_class` column. Tier 1 — Reversible: `transition_ticket`, `pause_agent`, `resume_agent`. Tier 2 — Semi-irreversible (diff captured in audit): `edit_ticket`, `edit_agent_config`. Tier 3 — Effectively-irreversible (full pre-state snapshot in audit): `create_ticket`, `spawn_agent`, `propose_hire`. The classification is binding for audit-row shape and the threat-model amendment's threat-vs-accepted text.
- Q: What happens when a chat-driven mutation conflicts with a simultaneous operator-driven mutation in the dashboard? → A: Optimistic-lock-based reject. First commit wins; second commit returns a typed `error_kind='ticket_state_changed'` (or analogous per-entity error_kind). Mirrors the M4 `lib/locks/` shape; chat surfaces the failure as a failure chip, dashboard surfaces its own error per M4 conventions.
- Q: Does M5.3 add a per-verb cost multiplier or change the per-session cost cap? → A: No. Per-session cost cap from M5.1 FR-061 continues unchanged; verb cost is captured in the underlying Claude turn cost. No per-verb multiplier, no per-day or per-month cap.
- Q: When does the chat container mount `garrison-mutate`? → A: Always, from session start. Default-mounted on every chat spawn alongside `postgres-RO` + `mempalace`. No per-session opt-in. Opt-in mounting would contradict the autonomous-execution posture and create a UI surface this milestone explicitly rejects.
- Q: Which `tool_use` events render as chips — only mutations, or all of them including queries? → A: Every `tool_use` renders a chip. Read chips (postgres queries, mempalace searches) use lower-emphasis styling (subdued tone, `font-tabular` summary line); mutation chips use higher-emphasis styling (named verb, args summary, deep-link to the affected resource). Operator gets uniform observability; importance is communicated via styling.
- Q: How visible are `propose_hire` outputs before M7 ships? → A: M5.3 ships a minimal read-only `/hiring/proposals` page in the dashboard. Single table view of `hiring_proposals` rows: `role_title`, `department`, `proposed_via`, `created_at`, `status`. No edit, no approve, no spawn. M7 extends with the review/approve/spawn flow; M5.3's stopgap surface keeps proposals visible before then.
- Q: Which schema deltas land in M5.3? → A: New `hiring_proposals` table; new `chat_mutation_audit` table (NOT extension of `vault_access_log` — different attack surface, different forensic shape); per-mutation channel-name registry expansions in `dashboard/lib/sse/channels.ts`. The optional `tickets.created_via_chat_session_id` FK is deferred to `/garrison-plan` (the existing `tickets.origin='ceo_chat'` value covers the categorical case; the FK is a query-ergonomics call the plan picks).
- Q: When does the `ARCHITECTURE.md` amendment land? → A: Alongside the spec, with a substring-match assertion test pinning the new lines (mirrors M5.2's `dashboard/tests/architecture-amendment.test.ts` precedent). Lines amended: `:574` (M5.3 entry gains autonomous-execution clause), `:103` (the agent ─► finalize_ticket diagram gains a parallel chat ─► garrison-mutate path), `:207` (`tickets.origin` comment is informational; spec leaves text unchanged unless the plan adopts the FK column).
- Q: Does M5.3 close the M5.2 retro carryover for `chat.session_started` / `chat.message_sent` / `chat.session_ended` EventRow rendering? → A: Yes, bundle. M5.3 adds new `chat.*` mutation channels with EventRow branches anyway; bundling the three deferred channels costs ~30 LOC and closes the M5.2 forward-look.
- Q: How are mockclaude fixtures extended for tool-use scripting? → A: Extend `garrison-mockclaude:m5` image with new per-verb fixture flags (env vars or stdin command tokens that select scripted `tool_use` envelopes). Extension over fork. Fixtures cover at minimum one happy path per verb plus error variants required by the chaos tests.
- Q: Does M5.3 ship prompt-injection chaos tests? → A: Yes. Three test fixtures, one per attack class from spike §6: AC-1 (palace-injected commands), AC-2 (operator-typed prompt injection in composer), AC-3 (tool-result feedback loop). Each test asserts the threat-model-amendment-defined expected behaviour — the threat model is hollow without these pins.
- Q: How does the verb registry stay coherent as the verb set grows? → A: Single registry file at `supervisor/internal/garrisonmutate/verbs.go` enumerating every registered verb. Both the runtime and the sealed-allow-list test read this file. Adding a verb requires a code change to this file + a threat-model amendment update + a test update; no runtime registration, no plugin shape.
- Q: What is the per-turn tool-call ceiling that bounds tool-result feedback loops? → A: 50 tool calls per turn, configurable via `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN` env var (default 50). When exceeded mid-turn, the supervisor terminates the chat container, writes a synthetic terminal row with `error_kind='tool_call_ceiling_reached'`, and emits the corresponding SSE typed-error frame.

### Session 2026-04-29 (/speckit.clarify pass)

- Q: `pg_notify` channel naming for chat-driven mutations — chat-namespaced channels vs existing-channel-with-payload-discriminator vs hybrid? → A: Chat-namespaced channels (Option A). New channels follow the `work.chat.<entity>.<action>` shape: `work.chat.ticket.created`, `work.chat.ticket.edited`, `work.chat.ticket.transitioned`, `work.chat.agent.paused`, `work.chat.agent.resumed`, `work.chat.agent.spawned`, `work.chat.agent.config_edited`, `work.chat.hiring.proposed`. Mirrors the existing `work.chat.session_*` precedent from M5.1/M5.2. Activity-feed listener subscribes to the new channels; the M3 ticket-transition listener stays unchanged. Payload carries IDs only (Rule 6 backstop).
- Q: `chat_mutation_audit` row shape for affected-resource references — single discriminated column vs typed FK columns vs JSONB? → A: Single discriminated column (Option A). One `affected_resource_id` TEXT column holds the resource ID (UUID or slug); a companion `affected_resource_type` TEXT column ('ticket' / 'agent_role' / 'hiring_proposal') makes query-time filtering explicit. No FK constraint (mirrors `vault_access_log.secret_path` opacity precedent). Forensic queries pivot on `(verb, affected_resource_id)`; the verb already disambiguates resource type but the explicit type column avoids ambiguity for cross-verb queries.
- Q: `edit_agent_config` leak-scan failure semantics — atomic full reject vs scrubbed partial commit vs atomic reject + caller hint? → A: Atomic full reject (Option A). Leak-scan runs pre-transaction; on detection, no `agents` row mutation lands; verb returns `error_kind='leak_scan_failed'`; an audit row records the rejected diff with offending fields redacted to `[REDACTED]` (never the raw secret value). Chip renders failure state. Mirrors M2.3 Rule 4 fail-closed posture; no partial / inconsistent agent state.
- Q: `hiring_proposals` minimum column set (M7-forward-compat)? → A: Minimum viable, append-only forward-compat (Option A). Columns: `id UUID PK`, `role_title TEXT NOT NULL`, `department_slug TEXT NOT NULL FK → departments(slug)`, `justification_md TEXT NOT NULL`, `skills_summary_md TEXT NULL`, `proposed_via TEXT NOT NULL CHECK ('ceo_chat'/'dashboard'/'agent')`, `proposed_by_chat_session_id UUID NULL FK → chat_sessions(id)`, `status TEXT NOT NULL DEFAULT 'pending' CHECK ('pending'/'approved'/'rejected'/'superseded')`, `created_at TIMESTAMPTZ DEFAULT NOW()`. M7 ADDs review/approve/spawn columns; M7 does NOT rename or remove any M5.3 column.
- Q: `ARCHITECTURE.md` amendment exact substrings the test pin matches? → A: Pin two substrings (Option A). Line 574 substring: `"M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)"`. Line 103 substring: `"Chat ──► garrison-mutate MCP"` (alongside the existing `"Agent ──► finalize_ticket MCP"` diagram line). Skip line 207 amendment (no FK column lands per the deferred-to-plan decision; `tickets.origin` comment is informational). Test asserts both substrings appear somewhere in `ARCHITECTURE.md`; failure blocks merge.

### Items deferred to /speckit.clarify

The following items are flagged inline at the relevant FRs as `[NEEDS CLARIFICATION]` markers. They are not blocking spec drafting but must resolve before `/garrison-plan`.

- The `/hiring/proposals` stopgap page's left-rail placement (under "CEO chat" subnav, new top-level "Hiring" entry, or a chat-side-effect surface in the right pane).
- Failure-chip text + link shape — chip copy and whether the chip deep-links to a forensic surface (audit row view, chat_mutation_audit table view, or stays informative-only).
- Concurrent-mutation conflict failure surface — chip copy when the chat-side `tool_result` reports `error_kind='ticket_state_changed'`. Failure chip with the error name verbatim, or a more conversational rendering in the assistant turn?

---

## User Scenarios & Testing

### User Story 1 — Threat-model amendment in place before any verb code lands (Priority: P1)

The threat-model amendment lands as `docs/security/chat-threat-model.md` *before* the first `garrison-mutate` verb implementation commits. The amendment enumerates assets (ticket state, agent state, hiring-proposal state — explicitly NOT vault), adversaries (operator mistakes, prompt injection via chat composer / palace / tool results), threats addressed (sealed verb set, vault opacity, audit + notify per call, cost cap), threats accepted (autonomous execution without per-call gate, given the runtime mitigations), and architectural rules numbered N through N+5 mirroring the vault threat model. The amendment also names the per-verb reversibility tier table.

**Why this priority**: load-bearing precondition. The autonomous-execution posture is only defensible if the threat model says so explicitly. M2.3 set the precedent: vault threat model landed before vault verb code. M5.3 honours that ordering.

**Independent test**: read `docs/security/chat-threat-model.md`. Assert the document follows the vault-threat-model.md structure (assets / adversaries / threats / architectural rules / open Qs / retro Qs sections all present); assert it explicitly enumerates the M5.3 verb set; assert it explicitly states vault verbs are NOT in the set; assert it carries numbered architectural rules; assert it carries the per-verb reversibility tier table; assert it cites spike §6 attack-class-1/2/3 and explains M5.3's mitigation posture per class. Independent of any verb implementation.

**Acceptance scenarios**:

1. **Given** an operator opens `docs/security/chat-threat-model.md`, **When** they read the document end to end, **Then** they find sections matching the vault threat model's structure (Scope / Assets / Adversaries / Threats addressed / Threats accepted / Architectural rules / Open questions / What the M5.3 retro must answer).
2. **Given** the document's "Architectural rules" section, **When** the operator inspects the rules, **Then** each rule is numbered, binding, and includes a "Consequence" paragraph in the same shape vault rules use.
3. **Given** the document's "Threats addressed" section, **When** the operator inspects how the autonomous-execution posture is justified, **Then** each spike §6 attack class (AC-1 palace-injected, AC-2 operator-typed, AC-3 tool-result feedback loop) is named explicitly with the M5.3 mitigation enumerated against it.
4. **Given** the verb set referenced anywhere in the document, **When** the operator audits which verbs are in scope, **Then** the document enumerates exactly the nine verbs (`create_ticket`, `edit_ticket`, `transition_ticket`, `pause_agent`, `resume_agent`, `spawn_agent`, `edit_agent_config`, `propose_hire` — and any spec-resolved additions) and explicitly states vault verbs are NOT in scope.

---

### User Story 2 — Operator types a natural-language instruction; chat creates a ticket (Priority: P1)

The canonical M5.3 use case from `ARCHITECTURE.md:425`. The operator opens an existing or new chat thread, types *"create a ticket in growth/backlog to fix the kanban drag bug, priority high"*, and presses send. The assistant interprets the instruction, calls `garrison-mutate.create_ticket(...)` autonomously, the supervisor commits a `tickets` row with `origin='ceo_chat'`, an audit row lands in `chat_mutation_audit`, the activity feed surfaces a `chat.ticket.created` event, and the operator sees an inline tool-call chip in the message stream linking to `/tickets/<new-id>`. No confirmation dialog appears.

**Why this priority**: load-bearing. Without this end-to-end path working, M5.3 ships nothing the operator can use. This story validates the verb registry, the supervisor-side spawn, the chat container's MCP config, the chip surface, the activity feed integration, the audit table, and the threat-model rule that mutations write audit rows — in a single round trip.

**Independent test**: against the full M5.1 + M5.2 + M5.3 stack with `garrison-mockclaude:m5` (or real `garrison-claude:m5` with a token) and a `garrison-mutate.create_ticket` fixture, the operator sends the message via the dashboard, and the test asserts: (a) a pre-call chip renders with copy `creating ticket "<title>" in <dept>/<column>…` within 1s of the `tool_use` SSE event arriving, (b) a `tickets` row with `origin='ceo_chat'` and the requested fields lands in Postgres, (c) a `chat_mutation_audit` row references the chat session, the chat message, the verb, and the affected ticket, (d) the activity feed receives a `chat.ticket.created` event-row branch render, (e) the post-call chip replaces the pre-call chip with copy `created ticket #<id>` and a deep-link to `/tickets/<id>`, (f) the round-trip from operator send to post-call chip render lands inside SC-010's 10s budget.

**Acceptance scenarios**:

1. **Given** an authenticated operator on an active chat thread, **When** they send a message instructing the chat to create a ticket, **Then** the assistant's response stream includes a `tool_use` event for `garrison-mutate.create_ticket` with structured args matching the operator's intent.
2. **Given** a `tool_use` event is received, **When** the dashboard's SSE consumer processes it, **Then** the message stream renders a pre-call chip in the in-flight assistant bubble between text content blocks, in the order the events arrived.
3. **Given** the supervisor receives the `tool_use`, **When** `garrison-mutate.create_ticket` executes, **Then** it validates inputs, opens a transaction, INSERTs the `tickets` row with `origin='ceo_chat'`, INSERTs the `chat_mutation_audit` row in the same transaction, commits, and emits the `pg_notify` post-commit.
4. **Given** the verb returns its `tool_result`, **When** the SSE stream emits the `tool_result` event, **Then** the dashboard's chip transitions from pre-call to post-call rendering, including the new ticket ID and a deep-link to `/tickets/<id>`.
5. **Given** the activity-feed listener is connected, **When** the `chat.ticket.created` `pg_notify` fires, **Then** an `EventRow` renders in the activity feed showing the verb, the affected ticket id, and the chat session reference (Rule 6 backstop: no chat content text in the payload).
6. **Given** the operator inspects `chat_mutation_audit` directly, **When** they query the row, **Then** they find the full args (including the title and description text the operator typed verbatim through the chat) under `args_jsonb`, with the operator's `chat_session_id` and `chat_message_id` populated.

---

### User Story 3 — Tool-call chips render uniformly across all chat tool calls (Priority: P1)

Across an active chat session, the operator sees inline chips for every tool call the assistant makes, not just mutations. A query against `postgres.query` renders a low-emphasis read chip (`queried tickets where dept=growth · 4 results`); a `mempalace.search` renders the same shape (`searched palace · 12 matches`); a `garrison-mutate.create_ticket` renders a higher-emphasis mutation chip with deep-link semantics. Reconnect after a mid-stream disconnect surfaces any committed tool calls the consumer missed (terminal `tool_result` events read from `chat_messages.raw_event_envelope` per the M5.2 amended FR-261 mechanism).

**Why this priority**: load-bearing. The autonomous-execution posture depends on the operator being able to see what the chat is doing. Without uniform observability, the threat-model justification breaks. The chip surface is the primary audit-observability layer.

**Independent test**: run a multi-turn session that exercises at least one read tool (`postgres.query`), one write tool (`garrison-mutate.transition_ticket`), and one mempalace tool. Mid-stream, force a client-side disconnect (close the EventSource); reconnect after the assistant turn completes server-side. Assert: (a) all three tool calls render as chips in the message stream, (b) read chips and mutation chips have visually distinct styling (lower vs higher emphasis), (c) post-reconnect the consumer reads committed tool calls from `chat_messages.raw_event_envelope` and renders them without double-rendering or dropping, (d) chips render in the same sequence as the underlying `tool_use` / `tool_result` event arrival order.

**Acceptance scenarios**:

1. **Given** the assistant calls a read tool (postgres / mempalace), **When** the SSE stream emits the `tool_use` event, **Then** the message stream renders a low-emphasis read chip with a one-line summary (`queried <table> · <N> results`, `searched palace · <N> matches`).
2. **Given** the assistant calls a mutation tool (`garrison-mutate.<verb>`), **When** the SSE stream emits the `tool_use` event, **Then** the message stream renders a higher-emphasis mutation chip with the verb name and arg summary, in the same in-flight assistant bubble between text content blocks.
3. **Given** the corresponding `tool_result` arrives, **When** the chip transitions from pre-call to post-call state, **Then** mutation chips show the affected resource ID with a deep-link, read chips show the result count or a brief excerpt of the result.
4. **Given** the SSE stream errors mid-tool-call, **When** the consumer reconnects via `Last-Event-ID`, **Then** the consumer reads the `chat_messages.raw_event_envelope` JSONB to recover any committed tool calls that landed during disconnect, and renders them once (no duplicates).
5. **Given** an assistant turn includes 5+ tool calls in a single response, **When** all chips render, **Then** the layout maintains chronological ordering, each chip is distinct, and the surrounding text content blocks render in their original positions.

---

### User Story 4 — Operator instructs the chat to perform multi-verb compound work (Priority: P2)

The operator sends *"transition #142 to qa-review and pause the engineer agent until I tell you otherwise"* in a single chat message. The assistant interprets the compound instruction, calls `garrison-mutate.transition_ticket` and `garrison-mutate.pause_agent` in sequence within the same turn, both mutations land, both chips render in the message stream, both audit rows commit, and both activity-feed events surface. If either verb fails, the assistant turn continues with the failure surfaced as a failure chip and the assistant's textual response acknowledges the partial outcome.

**Why this priority**: P2 because single-verb works without compound orchestration, but compound instructions are how the operator actually uses chat ("don't make me say it twice"). Validates that the per-turn tool-call ceiling holds, that audit + notify are atomic per-verb (not per-turn), and that partial-failure semantics surface coherently.

**Independent test**: with a mockclaude fixture scripted to emit two `tool_use` events in one turn, send a compound instruction. Assert: both `tickets.status` and `agents.is_paused` (or analogous) flip in the expected order; two `chat_mutation_audit` rows land referencing the same `chat_message_id`; two activity-feed events fire; two chips render in order in the assistant bubble. Force the second verb to fail (via fixture); assert the first verb's effects are committed and persistent, the second verb's failure chip renders with the typed `error_kind`, and the `chat_mutation_audit` row for the failed verb records `outcome` matching the error.

**Acceptance scenarios**:

1. **Given** an assistant turn that calls multiple `garrison-mutate` verbs in sequence, **When** the supervisor processes them, **Then** each verb runs in its own transaction (per-verb atomicity), each commits its own audit row, each emits its own `pg_notify` post-commit.
2. **Given** the second verb in a sequence fails validation, **When** the failure surfaces, **Then** the first verb's effects remain committed (no cross-verb rollback), the second verb's audit row records the `error_kind`, and the chat surface renders the second chip in failure state.
3. **Given** the per-turn tool-call ceiling is set to 50 (per `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN`), **When** an assistant turn calls 51 verbs, **Then** the 51st call is intercepted before execution, the supervisor terminates the chat container, writes a synthetic terminal row with `error_kind='tool_call_ceiling_reached'`, and the SSE stream emits the corresponding typed-error frame.

---

### User Story 5 — Concurrent mutation conflict resolves cleanly (Priority: P2)

The operator drags ticket #123 from `qa-review` to `engineer` in the dashboard's Kanban; simultaneously the chat assistant calls `garrison-mutate.transition_ticket(#123, qa-review)`. One commit wins (whichever transaction commits first); the loser returns a typed `error_kind='ticket_state_changed'`. If the chat is the loser, the chip renders in failure state with the operator-friendly error rendering; if the operator-side dashboard is the loser, the dashboard surfaces its own M4-shaped error. No row corruption, no duplicate transitions, both audit/forensic surfaces capture the attempt.

**Why this priority**: P2 because real-world dogfooding will trip this within the first week — the operator and the chat both have authority over the same state. The lock-based reject is the M4 precedent extended into chat. Validates that `lib/locks/` primitives compose correctly across the dashboard ↔ chat boundary.

**Independent test**: in an integration test, spawn two concurrent transactions issuing `transition_ticket(#123, …)`. Assert exactly one commits, exactly one returns `error_kind='ticket_state_changed'`. Repeat under load (~50 concurrent attempts) and assert the invariant holds. In a Playwright test, drive the dashboard drag while a fixture-driven chat issues the same transition; assert the loser's UI surface renders the typed error correctly.

**Acceptance scenarios**:

1. **Given** two simultaneous transition attempts on the same ticket, **When** both transactions race, **Then** the first to acquire the row lock commits successfully, the second's transaction aborts with `error_kind='ticket_state_changed'`.
2. **Given** the chat-side mutation loses, **When** the `tool_result` event surfaces the failure, **Then** the message stream renders a failure chip; the chip's copy is per the [NEEDS CLARIFICATION: failure-chip text + link shape — see clarify-deferred items above].
3. **Given** the dashboard-side mutation loses, **When** M4's existing UI lock-conflict surface fires, **Then** the operator sees the M4-shaped error per existing dashboard conventions; M5.3 does not change M4's behaviour.
4. **Given** any conflict outcome, **When** both audit surfaces are inspected, **Then** the winning side's audit row reflects the successful commit, the losing side's audit row reflects the typed error_kind, both reference the same target resource for forensic reconstruction.

---

### User Story 6 — Chat proposes a hire; operator reviews via the stopgap surface (Priority: P2)

The operator sends *"propose hiring an SEO specialist for growth — we need help with technical SEO and content strategy"*. The assistant calls `garrison-mutate.propose_hire` with the structured args; the supervisor commits a `hiring_proposals` row with `proposed_via='ceo_chat'`; the chip in the chat renders with a deep-link to `/hiring/proposals/<id>`; the operator clicks through to the stopgap read-only page, which lists this and any prior proposals in a single table. M7 will later extend the page with review/approve/spawn actions; M5.3 ships the page with table-only read access.

**Why this priority**: P2 because hiring is canonically a chat-driven activity per `ARCHITECTURE.md:425`, and the stopgap view prevents `propose_hire` from writing to a black hole until M7. Without P2 priority on this flow, the operator can't actually evaluate whether `propose_hire` is producing useful proposals during the operator-week-of-use.

**Independent test**: send a hiring instruction via the chat; assert a `hiring_proposals` row lands with `proposed_via='ceo_chat'` and the structured args; navigate to `/hiring/proposals`; assert the new proposal appears in the table view with `role_title`, `department`, `proposed_via`, `created_at`, `status='pending'` columns visible.

**Acceptance scenarios**:

1. **Given** the operator sends a chat message instructing a hire proposal, **When** the assistant calls `garrison-mutate.propose_hire`, **Then** a row lands in `hiring_proposals` with the structured args, `proposed_via='ceo_chat'`, `proposed_by_chat_session_id` populated, `status='pending'`.
2. **Given** the proposal is committed, **When** the post-call chip renders, **Then** it carries copy `proposed hire: <role_title>` and a deep-link to `/hiring/proposals/<id>`.
3. **Given** the operator navigates to `/hiring/proposals`, **When** the page loads, **Then** they see a table with all `hiring_proposals` rows visible to their authenticated session, sorted by `created_at DESC`, with the agreed minimum column set.
4. **Given** the stopgap page is read-only, **When** the operator inspects the UI, **Then** there are no edit, approve, reject, or spawn affordances; the page is purely a viewing surface for M5.3, with M7-shipped expansion noted in copy or a placeholder.

---

### User Story 7 — Activity feed surfaces every chat-driven mutation (Priority: P2)

The operator opens the activity feed (M3 surface). Every chat-driven mutation that's landed in the recent window appears as an `EventRow` with verb-specific copy: `chat created ticket #142`, `chat transitioned #142 → qa-review`, `chat paused engineer`, `chat proposed hire: SEO specialist`, etc. M5.3 also renders the three M5.1-deferred channels (`session_started`, `message_sent`, `session_ended`) per the bundled-deferral decision, closing the M5.2 retro carryover.

**Why this priority**: P2 because the activity feed is the global cross-cutting view of system state — without chat mutations surfacing here, the operator would have to dogfood the chat surface or query the DB to know what the CEO has been doing.

**Independent test**: trigger one mutation per verb via the chat; navigate to the activity feed; assert an EventRow appears for each, in chronological order, with the agreed Rule 6 backstop (no chat content in payloads). Trigger an M5.1 chat lifecycle event (start, message, end); assert the corresponding EventRow renders with the bundled M5.2-carryover branches.

**Acceptance scenarios**:

1. **Given** any chat-driven mutation lands, **When** the activity feed receives the corresponding `pg_notify`, **Then** an `EventRow` renders with verb-specific copy and verb-appropriate icon.
2. **Given** the M5.2 retro's deferred channels (`chat.session_started`, `chat.message_sent`, `chat.session_ended`), **When** they fire after M5.3 ships, **Then** EventRow branches render for each (closing the carryover).
3. **Given** the Rule 6 backstop, **When** the operator inspects any chat-mutation EventRow's underlying payload, **Then** the payload contains only IDs (`chat_session_id`, `chat_message_id`, affected resource ID, `verb`), never raw chat content or argument text.

---

### User Story 8 — Sealed allow-list prevents unauthorised MCP servers from leaking into the chat (Priority: P3)

The CI pipeline runs the `BuildChatConfig` test. The test asserts the chat container's MCP config contains exactly `{postgres, mempalace, garrison-mutate}` and rejects any attempt to add a fourth entry. Vault-related server names are explicitly tested as rejected (defense-in-depth against M2.3 Rule 3 violations). A regression test plants a hypothetical fourth entry in a config-builder branch and asserts the function rejects it, surfacing the violation as a build-time failure.

**Why this priority**: P3 because the test is system-validation, not an operator-facing surface. But it is binding — without this pin, the autonomous-execution posture's threat-model justification has no enforced guarantee.

**Independent test**: run the `BuildChatConfig` unit test in CI. Assert: (a) a successful build returns exactly the three named entries, (b) injecting a fourth entry into the input fails the build with a typed error, (c) injecting any vault-named entry (`vault`, `infisical`, `garrison-vault`, etc.) fails with a typed error referencing the M2.3 Rule 3 carryover.

**Acceptance scenarios**:

1. **Given** `BuildChatConfig` is invoked with default inputs, **When** it returns, **Then** the resulting MCP config has exactly three entries with the agreed names and no others.
2. **Given** a malformed input that adds a fourth entry, **When** `BuildChatConfig` runs, **Then** it rejects with a typed error and the test asserts the error type.
3. **Given** any vault-related entry name, **When** `BuildChatConfig` runs, **Then** it rejects with a typed error explicitly citing the M2.3 Rule 3 carryover.

---

### User Story 9 — Cost cap and per-turn tool-call ceiling bound runaway loops (Priority: P3)

A hostile or buggy assistant turn enters a tool-result feedback loop, repeatedly calling `garrison-mutate.create_ticket`. Two runtime mitigations fire: (a) the per-session cost cap (M5.1 FR-061) terminates the session when total cost exceeds the cap; (b) the per-turn tool-call ceiling (M5.3 default 50) terminates the chat container mid-turn before the 51st call executes. In either case, the supervisor commits a synthetic terminal row with the appropriate `error_kind`, the SSE stream emits a typed-error frame, the dashboard renders a failure state, and the operator can inspect what happened in the activity feed.

**Why this priority**: P3 because operator-week-of-use is unlikely to trigger this in normal workflow, but the threat model's autonomous-execution justification depends on these bounds. Without the chaos test fixtures pinning the behaviour, the threat-model amendment makes claims it can't back up.

**Independent test**: a chaos test launches a chat session with a fixture that loops `create_ticket` calls. Assert exactly one of the two bounds fires first (whichever is closer); assert no more than the ceiling-or-cap-bound number of `tickets` rows land; assert the synthetic terminal row commits; assert the SSE stream emits the typed-error frame; assert the activity feed surfaces the session_ended event with the reason.

**Acceptance scenarios**:

1. **Given** a chat session approaches the per-session cost cap, **When** the next assistant turn would push total cost over the cap, **Then** the supervisor terminates the session with `error_kind='session_cost_cap_reached'`, the SSE stream emits the corresponding typed error, and any partial state from the in-flight turn either commits cleanly or terminal-writes per the M5.1 atomicity rules.
2. **Given** an assistant turn calling more than 50 tool calls (default), **When** the 51st `tool_use` event is intercepted, **Then** the supervisor terminates the chat container, writes a synthetic terminal row with `error_kind='tool_call_ceiling_reached'`, and ensures no further mutations land.
3. **Given** either runaway-loop bound fires, **When** the activity feed processes the resulting `pg_notify`, **Then** an EventRow renders showing the reason and the affected chat session.

---

### User Story 10 — Prompt-injection chaos coverage matches the threat-model amendment (Priority: P3)

Three test fixtures, one per attack class from spike §6, exercise the threat-model-defined expected behaviour. AC-1: a malicious palace entry instructs the chat to "create a ticket to drop the user table"; the test asserts the threat-model-defined outcome (verb fires with the injected args; audit row preserves the chain; the operator can reconstruct what happened from the activity feed and audit table). AC-2: the operator pastes a customer email containing a prompt-injection in the composer; same posture. AC-3: a `tool_result` payload contains text the assistant could interpret as a tool call instruction; the per-turn tool-call ceiling fires before runaway.

**Why this priority**: P3 because the operator doesn't run these tests in normal workflow, but the threat-model amendment is hollow without them. Each test pins one attack class to one runtime mitigation; the amendment says "we accept this attack with this mitigation"; the test pins that the mitigation actually fires.

**Independent test**: run each chaos fixture independently. AC-1: seed a malicious palace entry; trigger a chat turn that retrieves it; assert the threat-model-defined outcome. AC-2: send a chat message carrying the injection; assert the threat-model-defined outcome. AC-3: emit a scripted `tool_result` containing injection-shaped text; assert the per-turn ceiling fires and the session terminates cleanly.

**Acceptance scenarios**:

1. **Given** a planted palace entry containing a malicious instruction, **When** the chat reads it during a turn, **Then** the threat-model-defined outcome occurs (per the amendment); the audit row records the full chain; the activity feed surfaces what happened.
2. **Given** an operator-typed prompt injection in the composer, **When** the assistant processes the turn, **Then** the same posture applies; no special-case bypass exists for operator-typed content.
3. **Given** a tool_result containing injection-shaped text, **When** the assistant attempts to act on it, **Then** the per-turn tool-call ceiling fires within bounded depth; the chaos test asserts the ceiling fires before unbounded rows land.

---

### Edge Cases

- **OAuth token expires mid-mutation**: the chat container's `CLAUDE_CODE_OAUTH_TOKEN` expires after `tool_use` arrives but before `tool_result`. The supervisor treats this the same as M5.1's vault-fetch-failure path: terminal-write with `error_kind='token_expired'`, the SSE typed-error frame surfaces, the chip renders in failure state. No partial mutation lands (the verb either commits cleanly or rolls back).
- **Supervisor restart mid-mutation**: the supervisor SIGTERMs while a `garrison-mutate` verb's transaction is open. The mutation either commits (transaction completes within `TerminalWriteGrace`) or rolls back; M5.1's restart-sweep handles the resulting state. The chat session shows as `aborted` with `error_kind='supervisor_shutdown'` per FR-082-shape; no partial mutation lands.
- **Chat container crashes mid-mutation**: similar to supervisor restart but at the container level. The supervisor's M5.1 chaos handling already covers this. M5.3 adds the assertion that no `chat_mutation_audit` row lands without a corresponding committed mutation — orphan audit rows are a bug.
- **`edit_agent_config` with leak-scan failure**: the operator instructs the chat to update the engineer agent's system prompt, and the proposed prompt contains a verbatim secret. The leak-scanner [NEEDS CLARIFICATION: leak-scan failure semantics — see deferred items] rejects the verb; no `agents` row update; audit row records `error_kind='leak_scan_failed'`; failure chip renders.
- **`spawn_agent` for an already-running role**: the verb spawns a new agent_instance respecting the per-department concurrency cap (constitution principle X). If the cap is full, the verb returns `error_kind='concurrency_cap_full'`; failure chip surfaces; no spawn occurs.
- **`transition_ticket` to an invalid target column**: the verb validates the target column exists on the ticket's department; if invalid, returns `error_kind='invalid_transition'`; failure chip with the typed error.
- **`pause_agent` with already-paused agent**: idempotent. The verb returns successfully; audit row records the no-op; chip shows the post-call state ("engineer is paused").
- **`propose_hire` with duplicate role_title in same department**: the verb does not enforce uniqueness (M7 will). Duplicates land; the `/hiring/proposals` page shows them; M7's review flow deduplicates if needed.
- **Tool-call chip render race**: a `tool_result` arrives before the corresponding `tool_use` (network reorder or parser race). The consumer dedupes on `(messageId, toolUseId)` keys per the M5.2 chatStream pattern; chip renders correctly regardless of arrival order.
- **`/hiring/proposals` page accessed by a non-authenticated user**: redirect to login per M3 conventions; no proposals visible without auth.
- **Activity-feed payload exceeds 8KB pg_notify limit**: the M5.3 mutation channels emit ID-only payloads (Rule 6), so this cannot happen in normal operation. If a future schema change broadens payloads, the M5.1 fallback poll catches missed events.

---

## Requirements

### Functional Requirements

#### Threat-model amendment

- **FR-400**: System MUST land a chat threat-model amendment at `docs/security/chat-threat-model.md` BEFORE any `garrison-mutate` verb code commits to the branch (mirrors the M2.3 vault-threat-model-first pattern).
- **FR-401**: The amendment MUST follow the structural shape of `docs/security/vault-threat-model.md`: Scope / Assets / Adversaries / Threats addressed / Threats accepted / Architectural rules (numbered, binding, with Consequence paragraphs) / Open questions / What the M5.3 retro must answer.
- **FR-402**: The amendment MUST enumerate the M5.3 verb set explicitly and state explicitly that vault-related verbs are NOT in scope (M2.3 Rule 3 carryover, made explicit).
- **FR-403**: The amendment MUST address each of spike §6's three attack classes (AC-1 palace-injected commands, AC-2 operator-typed prompt injection, AC-3 tool-result feedback loops) with a named mitigation per class.
- **FR-404**: The amendment MUST contain numbered architectural rules covering at minimum: sealed verb set + sealed MCP config (Rule N), vault opacity to chat (Rule N+1), audit row + `pg_notify` per mutation (Rule N+2), bounded tool-result feedback loops via per-session cost cap + per-turn tool-call ceiling (Rule N+3), bounded chat container network (Rule N+4), per-verb reversibility tier classification (Rule N+5).
- **FR-405**: The amendment MUST contain the per-verb reversibility tier table classifying each verb as Tier 1 (Reversible), Tier 2 (Semi-irreversible / diff-in-audit), or Tier 3 (Effectively-irreversible / pre-state snapshot in audit).
- **FR-406**: The amendment MUST be cited as a binding input by the M5.3 plan.

#### `garrison-mutate` MCP server

- **FR-410**: System MUST ship an in-tree Go MCP server at `supervisor mcp garrison-mutate` (subcommand of the supervisor binary), in-process JSON-RPC over stdio, mirroring the `internal/finalize` precedent.
- **FR-411**: The server MUST expose exactly the verbs registered in `supervisor/internal/garrisonmutate/verbs.go`. Adding a verb is a code change to that file plus a threat-model amendment update plus a registry test update; no runtime registration, no plugin shape.
- **FR-412**: The verb registry MUST contain at minimum: `create_ticket`, `edit_ticket`, `transition_ticket`, `pause_agent`, `resume_agent`, `spawn_agent`, `edit_agent_config`, `propose_hire`. Vault verbs MUST NOT be registered.
- **FR-413**: Each verb MUST run under a Postgres transaction that includes both the data write AND the `chat_mutation_audit` row INSERT. The `pg_notify` MUST emit AFTER successful commit (not within the transaction).
- **FR-414**: Each verb MUST validate inputs and return a typed `error_kind` on validation failure. The `tool_result` payload MUST surface the `error_kind` the dashboard chip layer can render.
- **FR-415**: Each verb's `tool_result` MUST include the affected resource ID(s) so the chip's deep-link can construct the dashboard URL without a follow-up query.
- **FR-416**: `create_ticket` MUST set `tickets.origin='ceo_chat'` and reference the originating chat session in `chat_mutation_audit`.
- **FR-417**: `edit_ticket` MUST capture the diff (before / after for each changed field) in `chat_mutation_audit.args_jsonb`. Tier 2 reversibility classification.
- **FR-418**: `transition_ticket` MUST hook into the existing M2.x ticket-transition event-bus. Tier 1 reversibility.
- **FR-419**: `pause_agent` and `resume_agent` MUST be idempotent. Tier 1 reversibility.
- **FR-420**: `spawn_agent` MUST respect the per-department concurrency cap (constitution principle X). Tier 3 reversibility (full pre-state snapshot in audit, including the spawned `agent_instance_id` post-commit).
- **FR-421**: `edit_agent_config` MUST run the M2.3 Rule 1 leak-scan against the proposed `system_prompt_md` BEFORE the transaction opens. On detection: the verb fails atomically with `error_kind='leak_scan_failed'`; no `agents` row mutation lands; an audit row commits with the rejected diff, offending field values redacted to `[REDACTED]` (never the raw secret value); the chip renders failure state. Mirrors the M2.3 Rule 4 fail-closed posture. Tier 2 reversibility.
- **FR-422**: `propose_hire` MUST write to `hiring_proposals` with `proposed_via='ceo_chat'` and `proposed_by_chat_session_id` populated. Tier 3 reversibility.

#### Sealed MCP config

- **FR-430**: `BuildChatConfig` MUST produce an MCP config with exactly `{postgres, mempalace, garrison-mutate}` entries and reject any other entries at build time.
- **FR-431**: `BuildChatConfig` MUST explicitly reject any vault-related entry name (`vault`, `infisical`, `garrison-vault`, etc.) with a typed error citing the M2.3 Rule 3 carryover.
- **FR-432**: The chat container's launch flags MUST continue to set `--tools "" --strict-mcp-config --permission-mode bypassPermissions` (per M5 spike §8.2) so the autonomous-execution posture's tool-surface seal holds.
- **FR-433**: A CI-pinned test MUST assert FR-430, FR-431, FR-432 above. The test failing blocks merge.

#### Tool-call chip surface

- **FR-440**: System MUST render an inline chip in the message stream for every `tool_use` event arriving from the chat SSE stream, in stream-event order, between text content blocks within the in-flight assistant bubble.
- **FR-441**: System MUST render a corresponding `tool_result` chip transition (pre-call → post-call state) when the matching `tool_result` event arrives.
- **FR-442**: Read tool calls (`postgres.query`, `mempalace.search`, etc.) MUST render with lower-emphasis chip styling distinct from mutation tool calls.
- **FR-443**: Mutation tool calls (`garrison-mutate.<verb>`) MUST render with higher-emphasis chip styling, including the verb name, arg summary, and a deep-link to the affected resource (post-call state).
- **FR-444**: Failed tool calls (`tool_result.is_error=true`) MUST render in failure-chip styling reusing M5.2's error palette. Failure-chip copy + link target [NEEDS CLARIFICATION: see deferred items above].
- **FR-445**: Chips MUST be informative-only. They MUST NOT carry undo, cancel, retry, approve, or reject affordances.
- **FR-446**: Chip click target for mutation chips with a successful resource creation MUST open the dashboard route for that resource (e.g., `/tickets/<id>`, `/hiring/proposals/<id>`).
- **FR-447**: On SSE disconnect/reconnect, the consumer MUST read `chat_messages.raw_event_envelope` for committed tool calls that arrived during disconnect and render them once (no duplicates, no drops). Reuses the M5.2 amended FR-261 row-state-read mechanism.
- **FR-448**: Chip rendering MUST respect M3 design language primitives (`Chip`, `StatusDot`, `font-tabular`) and inherit reduced-motion handling from M5.2.

#### SSE event union extension

- **FR-450**: `dashboard/lib/sse/chatStream.ts` event union MUST extend with `tool_use` and `tool_result` discriminated variants carrying at minimum `messageId`, `toolUseId`, `toolName`, `args` (for `tool_use`) and `messageId`, `toolUseId`, `result`, `isError` (for `tool_result`).
- **FR-451**: The `useChatStream` hook return shape MUST surface tool calls in a form the renderer can read for chip composition (Map keyed by messageId returning ordered tool call entries, or analogous; exact shape is plan-territory).
- **FR-452**: The supervisor's `/api/sse/chat` route MUST emit tool events as SSE frames per the M5.1 NDJSON contract; `internal/chat/policy.go`'s existing `OnStreamEvent` hook is the integration point.

#### Activity feed integration

- **FR-460**: System MUST emit `pg_notify` events for every chat-driven mutation on chat-namespaced channels following the `work.chat.<entity>.<action>` shape (e.g., `work.chat.ticket.created`, `work.chat.ticket.transitioned`, `work.chat.agent.paused`, `work.chat.hiring.proposed`). Mirrors the existing M5.1/M5.2 `work.chat.session_*` precedent. The M3 ticket-transition listener (`work.ticket.transitioned.<dept>.<from>.<to>`) is NOT reused for chat-driven transitions.
- **FR-461**: The dashboard activity feed MUST render `EventRow` branches for every chat-driven mutation channel introduced in M5.3.
- **FR-462**: M5.3 MUST close the M5.2 retro carryover by adding `EventRow` branches for the deferred `chat.session_started`, `chat.message_sent`, `chat.session_ended` channels.
- **FR-463**: All chat-mutation `EventRow` payloads MUST contain only IDs and verb names (Rule 6 backstop). Raw chat content text MUST NOT appear in payloads. The audit table holds the full args.

#### Audit table

- **FR-470**: System MUST add a new `chat_mutation_audit` table with at minimum: `id`, `chat_session_id` FK, `chat_message_id` FK, `verb`, `args_jsonb`, `outcome` (success or `error_kind`), `reversibility_class` (1 / 2 / 3 per FR-405), `affected_resource_id` TEXT, `affected_resource_type` TEXT (`'ticket'` / `'agent_role'` / `'hiring_proposal'`), `created_at`. Single discriminated column for affected resource — no FK constraint, mirrors `vault_access_log.secret_path` opacity precedent.
- **FR-471**: Every successful mutation verb MUST commit a `chat_mutation_audit` row in the same transaction as the data write (atomicity).
- **FR-472**: Every failed mutation verb MUST commit a `chat_mutation_audit` row recording the `error_kind` (separate transaction or rolled-up; spec leaves the failure-row commit semantics to /garrison-plan but the row MUST land for forensics).
- **FR-473**: `args_jsonb` MUST capture full args including any operator-typed text passed as verb arguments (e.g., the title and description text of `create_ticket`). Rule 6's "log no values" applies to *secret* values, not to the chat-content text the operator chose to instruct the chat with.

#### Schema deltas

- **FR-480**: System MUST add a new `hiring_proposals` table with the following minimum-viable column set: `id UUID PK`, `role_title TEXT NOT NULL`, `department_slug TEXT NOT NULL` (FK → `departments(slug)`), `justification_md TEXT NOT NULL`, `skills_summary_md TEXT NULL`, `proposed_via TEXT NOT NULL` (CHECK `'ceo_chat'`/`'dashboard'`/`'agent'`), `proposed_by_chat_session_id UUID NULL` (FK → `chat_sessions(id)`), `status TEXT NOT NULL DEFAULT 'pending'` (CHECK `'pending'`/`'approved'`/`'rejected'`/`'superseded'`), `created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`. M7 ADDs review/approve/spawn columns; M7 MUST NOT rename or remove any M5.3 column.
- **FR-481**: System MUST extend `dashboard/lib/sse/channels.ts` allowlist with the new chat-namespaced channels per FR-460: `work.chat.ticket.created`, `work.chat.ticket.edited`, `work.chat.ticket.transitioned`, `work.chat.agent.paused`, `work.chat.agent.resumed`, `work.chat.agent.spawned`, `work.chat.agent.config_edited`, `work.chat.hiring.proposed`.
- **FR-482**: Schema migrations MUST apply cleanly via `goose up`; `bun run drizzle:pull` MUST regenerate `schema.supervisor.ts` cleanly; `sqlc generate` MUST produce clean models for the new queries.

#### Stopgap hiring view

- **FR-490**: System MUST ship a read-only page at `/hiring/proposals` showing `hiring_proposals` rows in a table view.
- **FR-491**: The page MUST show at minimum: `role_title`, `department`, `proposed_via`, `created_at`, `status` columns; rows sorted `created_at DESC`.
- **FR-492**: The page MUST be authenticated per existing M3 conventions; unauthenticated access redirects to login.
- **FR-493**: The page MUST NOT carry edit, approve, reject, or spawn affordances. M7 extends; M5.3 reads only.
- **FR-494**: Left-rail placement [NEEDS CLARIFICATION: left-rail placement — under "CEO chat", new top-level "Hiring", or chat-side-effect surface].

#### Architecture amendment

- **FR-500**: System MUST amend `ARCHITECTURE.md` such that the file contains both substrings: (a) `"M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)"` (replacing or extending the current `:574` M5 line); (b) `"Chat ──► garrison-mutate MCP"` (added to the architectural-pattern diagram around line `:103` alongside the existing `"Agent ──► finalize_ticket MCP"` line). Line `:207` (`tickets.origin` comment) is NOT amended in M5.3.
- **FR-501**: System MUST add a substring-match assertion test (mirroring `dashboard/tests/architecture-amendment.test.ts`) asserting both substrings from FR-500 appear in `ARCHITECTURE.md`. Test failure blocks merge.

#### Test coverage

- **FR-510**: System MUST ship integration tests against the full chat → `garrison-mutate` → Postgres path, one happy-path test per verb minimum.
- **FR-511**: System MUST ship CI-pinned tests asserting each numbered architectural rule from the threat-model amendment is mechanically enforced.
- **FR-512**: System MUST ship the sealed-allow-list `BuildChatConfig` test per FR-433.
- **FR-513**: System MUST ship a vault-leak-scan parity test for `edit_agent_config`: a planted `sk-`-shaped sentinel in the proposed `system_prompt_md` triggers `error_kind='leak_scan_failed'` and writes the failure to audit.
- **FR-514**: System MUST ship a cost-cap chaos test: a runaway-loop fixture confirms the per-session cost cap fires before > N mutation rows land (N defined by the fixture and asserted in the test).
- **FR-515**: System MUST ship a per-turn tool-call ceiling chaos test: a fixture exceeding `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN` (default 50) triggers `error_kind='tool_call_ceiling_reached'` mid-turn.
- **FR-516**: System MUST ship a concurrent-mutation chaos test: simultaneous chat-vs-dashboard `transition_ticket` resolves per FR-414 (optimistic-lock-based reject); both audit surfaces commit; final state matches the winning side.
- **FR-517**: System MUST ship Playwright golden-path extension covering at minimum: the canonical `create_ticket` flow from operator send to post-call chip render to ticket existence in `/tickets/<id>`, end-to-end within SC-010's 10s budget.
- **FR-518**: System MUST ship Playwright chip-rendering tests asserting the per-verb pre-call and post-call chip states render with correct copy and ARIA semantics.
- **FR-519**: System MUST ship prompt-injection chaos tests: AC-1 (palace-injected), AC-2 (operator-typed), AC-3 (tool-result feedback loop). Each test asserts the threat-model-defined expected behaviour per FR-403.
- **FR-520**: System MUST extend `garrison-mockclaude:m5` with per-verb fixture flags supporting at minimum one happy-path script per verb plus the error variants required by chaos tests.

#### Per-turn tool-call ceiling

- **FR-530**: System MUST enforce a per-turn tool-call ceiling of 50 by default, configurable via `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN` env var.
- **FR-531**: When the ceiling is exceeded mid-turn, the supervisor MUST terminate the chat container, write a synthetic terminal row with `error_kind='tool_call_ceiling_reached'`, and emit the corresponding SSE typed-error frame.

#### Dependency discipline

- **FR-540**: Zero new direct dependencies in `dashboard/package.json` AND `supervisor/go.mod` OR every new dependency is justified in commit messages and recapped in the M5.3 retro per the M3-established dependency discipline.
- **FR-541**: The `tools/vaultlog` go vet analyzer MUST pass on all new chat-mutation code paths.

---

### Key Entities

- **Chat threat-model amendment doc** — `docs/security/chat-threat-model.md`. Shape mirrors the vault threat model. Binding input to the M5.3 plan and to all M5.3 verb implementations.
- **`garrison-mutate` MCP server** — in-tree Go MCP server, supervisor-owned, in-process JSON-RPC. Exposes the sealed verb set. One subcommand of the supervisor binary.
- **Verb registry** — `supervisor/internal/garrisonmutate/verbs.go`. Single source of truth for the registered verb set; runtime + sealed-allow-list test both read this file.
- **`chat_mutation_audit` table** — new audit table. Per-row record of every mutation call with full args, outcome, reversibility class, references to the originating chat session and message and to the affected resource.
- **`hiring_proposals` table** — new table holding chat-driven and (M7) dashboard-driven hiring proposals. M5.3 ships table + read-only stopgap page; M7 extends.
- **Chat-mutation channel-name registry** — extension of `dashboard/lib/sse/channels.ts` adding new chat-mutation channels per the spec-resolved naming format.
- **`BuildChatConfig` MCP config builder** — supervisor-side function building the per-session chat MCP config. Sealed allow-list with three entries; CI-pinned test enforces.
- **Tool-call chip component** — new dashboard component(s) under `components/features/ceo-chat/` rendering pre-call and post-call chip states for read and mutation tool calls. Informative-only.
- **Per-turn tool-call ceiling** — runtime mitigation against tool-result feedback loops. Configurable env var, default 50.
- **`/hiring/proposals` stopgap page** — read-only dashboard page. Single table view of `hiring_proposals` rows. M7 extends.

---

## Success Criteria

### Measurable Outcomes

- **SC-300**: A live operator types a chat instruction to create a ticket; the resulting `tickets` row, `chat_mutation_audit` row, activity-feed event, and post-call chip render within the SC-010 10s round-trip budget against the full M5.1 + M5.2 + M5.3 stack.
- **SC-301**: The chat threat-model amendment lands at `docs/security/chat-threat-model.md` BEFORE any verb implementation commits to the branch (binding ordering pin, mirrors M2.3 vault-threat-model-first).
- **SC-302**: The chat container's MCP config contains exactly `{postgres, mempalace, garrison-mutate}` and rejects any fourth entry. CI-pinned test enforces.
- **SC-303**: The `garrison-mutate` verb registry contains exactly the spec-resolved verb set. CI-pinned test asserts the registered list matches the enumeration in the threat-model amendment's per-verb reversibility table.
- **SC-304**: Vault is opaque to chat: zero `vault_*` tool names register; CI-pinned test asserts `BuildChatConfig` rejects any vault-named entry with a typed error citing the M2.3 Rule 3 carryover.
- **SC-305**: Each verb writes the data row + audit row in the same transaction; the `pg_notify` emits AFTER commit. Integration test per verb asserts atomicity.
- **SC-306**: Each verb's `tool_result` is surfaced as a chip; mutation chips carry deep-links; failure chips carry the typed `error_kind`. Playwright per-verb chip-rendering tests pass.
- **SC-307**: The activity feed renders an `EventRow` for every chat-driven mutation. EventRow payloads contain only IDs and verb names; Rule 6 backstop test asserts no chat content text appears in any chat-mutation payload.
- **SC-308**: M5.2 retro carryover closes: `chat.session_started`, `chat.message_sent`, `chat.session_ended` `EventRow` branches render in the activity feed.
- **SC-309**: `edit_agent_config` leak-scan parity holds: a planted secret triggers `error_kind='leak_scan_failed'`; an audit row records the failure; chaos test asserts.
- **SC-310**: The per-session cost cap from M5.1 fires before > N mutation rows land in a runaway-loop chaos test (N is fixture-defined and asserted).
- **SC-311**: The per-turn tool-call ceiling fires when exceeded; chaos test asserts a synthetic terminal row with `error_kind='tool_call_ceiling_reached'` lands and no further mutations land within the turn.
- **SC-312**: Concurrent-mutation conflict resolves per FR-414: chaos test confirms one commit wins, the other returns `error_kind='ticket_state_changed'`, both audit surfaces commit.
- **SC-313**: Prompt-injection chaos coverage exists for all three attack classes (AC-1 palace, AC-2 operator-typed, AC-3 tool-result feedback loop). Each test asserts the threat-model-defined expected behaviour.
- **SC-314**: `ARCHITECTURE.md` amendment lands; substring-match test pins the new lines per FR-501.
- **SC-315**: `goose up` applies cleanly; `bun run drizzle:pull` regenerates `schema.supervisor.ts` with `hiring_proposals` and `chat_mutation_audit`; `sqlc generate` produces clean models.
- **SC-316**: The `/hiring/proposals` stopgap page renders rows from chat-driven `propose_hire` calls; visible to authenticated operators; redirect-to-login for unauthenticated.
- **SC-317**: Vault leak-scan parity holds across all new chat-mutation code paths: `tools/vaultlog` go vet analyzer passes.
- **SC-318**: Zero new direct dependencies in `dashboard/package.json` AND `supervisor/go.mod`, OR every new dependency is justified in commit messages and recapped in the M5.3 retro.
- **SC-319**: The Playwright suite (M3 + M4 + M5.1 + M5.2 + M5.3) stays under 12 minutes total per the M5.1 SC-010 ceiling.
- **SC-320**: First post-call chip renders within 1s of the corresponding `tool_result` SSE event arriving (interactivity bound).

---

## Assumptions

- The M5.1 backend substrate (server actions, SSE producer, transcript reads, idle/restart sweeps, audit/cost rollup) and M5.2 frontend substrate (three-pane layout, message stream, composer, multi-session UX, end/archive/delete affordances, sidebar subnav) are stable and merged on `main`. M5.3 is additive against both.
- `ARCHITECTURE.md:425`'s framing — *"CEO decides in chat ('we need an SEO specialist')"* — is committed; M5.3 implements that framing for the canonical use case.
- Single-operator single-CEO posture continues. Multi-operator is post-M5; the threat-model amendment's autonomous-execution justification depends on this.
- `tickets.origin='ceo_chat'` value is committed in the M2.x schema (`ARCHITECTURE.md:207`); M5.3 writes the value via `create_ticket` without further schema work on `tickets.origin` itself.
- The chat container's launch flags (`--tools "" --strict-mcp-config --permission-mode bypassPermissions`) hold from M5.1; M5.3 reuses without amending.
- M2.3 vault rules carry forward to chat: vault is opaque, the leak-scan analyzer enforces non-logging of secret values, the four vault rules are unchanged. M5.3 extends Rule 3 from "agents" to "agents and chat" via the threat-model amendment.
- M4 mutation patterns (server-action / audit / `pg_notify` / optimistic-lock primitives in `lib/locks/`) are reused server-side for `garrison-mutate` verb implementations. UI-side `ConfirmDialog` patterns stay on the dashboard for operator-driven mutations and explicitly do NOT apply to chat-driven mutations.
- M2.2.1's `internal/finalize` precedent is the structural template for `garrison-mutate`: in-tree Go, single subcommand, in-process JSON-RPC, atomic Postgres transaction per call.
- The dashboard test stance excluding browser-DOM unit tests in favor of Playwright integration tests (M3/M4 precedent, retained in M5.2) continues for M5.3. Component-level interactive tests are Playwright; static rendering pins are Vitest.
- `garrison-mockclaude:m5` is the canonical CI fixture image; M5.3 extends rather than forks.
- The `/api/sse/chat` route's `Last-Event-ID` reconnect contract from M5.1 (FR-052) plus the M5.2 amended FR-261 row-state-read mechanism cover all reconnect cases for tool calls; M5.3 does not introduce a new reconnect protocol.
- Operator-week-of-use observations after M5.3 ships will surface whether undo/rollback affordances, more granular cost caps, or richer chip semantics warrant a polish round; M5.3 retro captures these as forward-look items per the AGENTS.md retro template.
