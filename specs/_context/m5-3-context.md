# M5.3 — Chat-driven mutations (autonomous-execution posture)

**Status**: Context for `/speckit.specify`. Ships chat-driven write paths that run autonomously — no per-call operator approval — against a sealed mutation MCP server. Threat-model amendment is in scope and load-bearing.
**Prior milestone**: M5.2 (CEO chat dashboard surface, read-only) merged on `main` 2026-04-29. Retro at [`docs/retros/m5-2.md`](../../docs/retros/m5-2.md).
**M5 decomposition**: M5.1 → M5.2 → **M5.3** → M5.4. M5.4 (knowledge-base pane) stays after M5.3.

**Binding inputs** (read first, in order):

1. **M5.2 retro** ([`docs/retros/m5-2.md`](../../docs/retros/m5-2.md)) — load-bearing for what M5.3 inherits as substrate (server actions, queries, SSE consumer, sidebar subnav, chat shell components) and the M5.3-deferred items the retro names: chat-driven mutations + threat-model amendment, the EventRow-rendering polish for M5.1 chat channels (`session_started`, `message_sent`, `session_ended`), the orphan-sweep notify decision (resolved Q4 — re-evaluate if operator-week-of-use surfaces stuck loading states).
2. **M5.1 retro + spec** ([`docs/retros/m5-1.md`](../../docs/retros/m5-1.md), [`specs/010-m5-1-ceo-chat-backend/spec.md`](../010-m5-1-ceo-chat-backend/spec.md)) — substrate vocabulary: `ChatErrorKind` enum, FR-051 (`pg_notify('chat.assistant.delta', …)`), the `OnStreamEvent` extension hook that already routes `tool_use` / `tool_result` events through the parser today, the chat container's launch flag set (`--tools "" --mcp-config /etc/garrison/mcp.json --strict-mcp-config --permission-mode bypassPermissions`), and `BuildChatConfig`'s sealed `postgres + mempalace`-only allow-list test M5.3 amends.
3. **M5 spike** ([`docs/research/m5-spike.md`](../../docs/research/m5-spike.md)) — load-bearing for **§4** (the three mutation-MCP-server design alternatives, with option 1 / new supervisor-owned `garrison-mutate` server recommended) and **§6** (attack-class-1/2/3 framing — palace-injected commands, operator-typed prompt injection, tool-result feedback loops). The mitigations §6 sketches include "Confirmation tier on every mutation tool" — M5.3 deviates; see "Scope deviation" below. Also **§8.2** (`--tools ""` + `--strict-mcp-config` already seal the chat container's tool surface to exactly what `--mcp-config` mounts — that property is what makes the autonomous-execution posture defensible, and M5.3 must not weaken it).
4. **Vault threat model** ([`docs/security/vault-threat-model.md`](../../docs/security/vault-threat-model.md)) — the writing precedent for a Garrison threat model: assets, adversaries, threats addressed/accepted, architectural rules (numbered, binding), milestone banding, open questions, retro questions split. M5.3's threat-model amendment uses the same shape. **Rule 3 carryover**: "the vault is opaque to agents." M5.3 extends that to "the vault is opaque to chat" — chat does NOT get vault mutations even with the broader verb set.
5. **M2.2.1 retro + `internal/finalize`** ([`docs/retros/m2-2-1.md`](../../docs/retros/m2-2-1.md), `supervisor/internal/finalize/`) — `finalize_ticket` is the canonical precedent for a single-tool transactional commit path the supervisor owns. `garrison-mutate` mirrors that shape: in-tree Go, `supervisor mcp garrison-mutate` subcommand, in-process JSON-RPC, atomic Postgres-write per tool call, audit row + `pg_notify` per call. The atomic-write idiom and the "agent cannot exit successfully without the tool" framing carry; the verb set differs.
6. **M4 retro + mutation patterns** ([`docs/retros/m4.md`](../../docs/retros/m4.md), `dashboard/lib/actions/`) — the dashboard-side mutation idioms M5.3 reuses *server-side* but explicitly does NOT mirror UI-side: server-action shape, audit-row vocabulary, `pg_notify` channel-name conventions, optimistic-locking primitives (`lib/locks/`), the `event_outbox` discipline. The `ConfirmDialog` tier system (`single-click` / `typed-name`) is the dashboard's gate for operator-driven mutations and stays there — M5.3's chat-driven mutations do NOT use it (see "Scope deviation").
7. **M2.3 vault threat model architectural rules** + **`mcpconfig.CheckExtraServers`** (Rule 3 implementation in `internal/mcpconfig/`) — the precedent for a sealed-MCP-allow-list test that rejects unauthorized server entries at config-build time. M5.3's `BuildChatConfig` extension follows the same pattern: the chat config must mount exactly `postgres + mempalace + garrison-mutate` and reject any fourth entry, with a CI-pinned test asserting it.
8. **`ARCHITECTURE.md`**:
    - **`:574`** — "M5.3 adds chat-driven mutations behind a threat-model amendment." The line is committed; this context honours it. The autonomous-execution posture is a *how*, not a *whether*.
    - **`:69`** — supervisor summons Claude per-message; mutations land via the same per-message lifecycle.
    - **`:103`** — "Agent ──► finalize_ticket MCP ✓ single tool, transactional, only commit path" — the architectural pattern `garrison-mutate` extends.
    - **`:207`** — `tickets.origin TEXT NOT NULL, -- 'ui', 'ceo_chat', 'agent_spawned'` — the `ceo_chat` value is committed in the M2.x schema for this milestone to write.
    - **`:425`** — hiring trigger: "CEO decides in chat ('we need an SEO specialist')" — names `propose_hire` as canonically a chat-driven verb even though the hiring UI is M7.
9. **`RATIONALE.md`** — `:117–130` (summon-per-message framing) plus the "audit-row-discipline applies to chat surfaces too" framing the M5.1/M5.2 contexts already lean on. No new RATIONALE positions need to land in M5.3; the autonomous-execution posture is consistent with the existing "single-operator, single-CEO, operator-watches-the-chat" framing.
10. **`AGENTS.md`** — Rule 6 (audit everything, log no values — extends to chat-driven mutation rows the same way it does to vault rows: audit *that* a mutation happened and *which* verb/args were called, never the chat content that triggered it), MCP allow-list discipline, locked-deps soft rule, §Retros dual-deliverable.
11. **M5.2 substrate references** (not binding, reference only):
    - [`dashboard/components/features/ceo-chat/MessageStream.tsx`](../../dashboard/components/features/ceo-chat/MessageStream.tsx) — the message stream M5.3 extends with tool-call chips
    - [`dashboard/components/features/ceo-chat/MessageBubble.tsx`](../../dashboard/components/features/ceo-chat/MessageBubble.tsx) — bubble-internal layout the chips compose with
    - [`dashboard/lib/sse/chatStream.ts`](../../dashboard/lib/sse/chatStream.ts) — SSE consumer hook; M5.3 extends the event union with `tool_use` / `tool_result` variants
    - [`supervisor/internal/chat/policy.go`](../../supervisor/internal/chat/policy.go) — chat policy already wires `OnStreamEvent`; M5.3 extends to surface tool events through the SSE bridge

If this context contradicts any of the above, the binding inputs win. The architecture amendment for the autonomous-execution stance lands during /speckit.specify (or as a follow-up commit during M5.3 implementation — operator picks).

---

## Scope deviation from committed framing

**The deviation**: M5.3 ships chat-driven mutations *without* per-call operator confirmation gates. The chat container runs with `--permission-mode bypassPermissions` (or `--dangerously-skip-permissions` — same effect, the spec picks the spelling). Tool calls render in the message stream as **informative chips** so the operator can see what the chat is doing, but the operator is *not* in the approval loop on a per-call basis.

**What this deviates from**:

- **M5 spike §6 ("Mitigations to bake into the M5 design")** explicitly recommends *"Confirmation tier on every mutation tool, like the M4 dashboard's `ConfirmDialog tier='typed-name'`. The chat can't actually execute a mutation without the operator typing back the typed-name confirmation in the chat surface. The mutation tool's tool_result blocks until the dashboard echoes the confirmation back through the supervisor."* The spike is non-normative ("Status: research, not normative") but it's been cited as binding by both prior M5 contexts. M5.3 rejects this specific mitigation and substitutes a different mitigation set.

- **M5.2 retro's "Open questions deferred to the next milestone"** says the M5.3 deliverable is a `garrison-mutate` MCP server **"with confirmation gating"**. That phrasing is a forward-look, not a sealed commitment, but the framing has been carried forward in operator-side discussions. M5.3 explicitly revises it.

**Why M5.3 deviates** (and what the threat-model amendment must justify):

1. **Operator-as-constant-approver is friction the system shouldn't have.** Garrison's whole shape — "the operator runs a small staff of agents, the CEO orchestrates" — is broken if every CEO action requires the operator to alt-tab to the dashboard and click ✅. The autonomous posture is the design.

2. **The chat is *already* a different trust posture than M2.x agents** — and M2.x agents already run with `bypassPermissions` against MCP tools today. M5.3 unifies CEO chat with that model rather than making chat the exception.

3. **The security boundary moves entirely to MCP server selection + verb implementation**:
   - The chat container has exactly `postgres-RO + mempalace + garrison-mutate` mounted. `--tools ""` + `--strict-mcp-config` (M5 spike §8.2) seal everything else off — no Bash, no Write, no Read, no built-in agentic tools.
   - The `garrison-mutate` verb set is sealed at config time and CI-pinned. No dynamic tool discovery, no operator-runtime-mounting, no opt-in/opt-out shape.
   - Vault is *not* in the verb set (M2.3 Rule 3 carryover applies to chat).

4. **Three observability layers replace the per-call gate**:
   - **Inline tool-call chip** in the message stream — the operator sees what just happened in real time, in the same surface they're already watching.
   - **Activity feed event row** for every mutation verb — the global feed surfaces what the chat did alongside what dashboard mutations did.
   - **Audit row** in `vault_access_log`-shaped audit table (or extension thereof) for forensic reconstruction.

5. **Per-session cost cap from M5.1 bounds runaway-injection cost.** A palace-injected feedback loop that calls `create_ticket` 100 times still hits the cap and terminates.

6. **Single-operator single-CEO posture caps blast radius.** Multi-operator multi-CEO is post-M5; if/when it lands, the threat model gets re-amended.

The threat-model amendment in M5.3 is where this justification gets *adopted as a binding architectural rule* — not just argued. The amendment must enumerate attack-classes 1/2/3 from the spike §6, name what the autonomous posture defends against and what it explicitly accepts, and add architectural rules (numbered, binding) the way the vault threat model does.

**Resolution path**: the threat-model amendment lands as a new doc (`docs/security/chat-threat-model.md`) or as an extension to the vault threat model with a chat section — spec picks. Either way it lands as a binding input *before* any `garrison-mutate` verb ships, mirroring the M2.3 vault-threat-model-first pattern.

---

## Why this milestone now

M5.1 + M5.2 shipped the read-only chat: backend, frontend, idle/restart/orphan sweeps, audit, cost rollup, three-pane layout, multi-session UX, end/archive/delete, sidebar subnav, tool-call chip stub deferred to v1.1. The CEO can ask questions; the CEO cannot do anything.

`ARCHITECTURE.md:425` already names the canonical use case the read-only stance can't reach — *"CEO decides in chat ('we need an SEO specialist'), or you click 'Propose hire' in the UI"*. M5.3 is when "decides in chat" becomes mechanical.

The substrate is healthy:
- The chat MCP allow-list is already sealed and CI-pinned (`BuildChatConfig` test).
- The supervisor's `internal/chat/policy.go` already routes `tool_use` / `tool_result` events through `OnStreamEvent` — M5.3 extends downstream to surface them as SSE deltas the dashboard renders as chips.
- `tickets.origin = 'ceo_chat'` is in the M2.x schema waiting for this milestone to write the value (`ARCHITECTURE.md:207`).
- The atomic-write pattern is proven: `internal/finalize/finalize.go` is the template `garrison-mutate`'s commit path follows.

Splitting M5.3 (mutations) from M5.4 (knowledge-base pane) follows the same M3/M4 pattern of bounding cross-stack milestones. M5.4 is layout-and-content; M5.3 is a new authority surface that needs a threat model. They land separately so the threat model isn't competing with knowledge-base UX work for milestone budget.

---

## In scope

### Threat-model amendment (load-bearing, lands first)

A new threat-model document covering chat-driven mutations. The spec picks the location:
- **Option A**: new `docs/security/chat-threat-model.md`, structured the same way the vault threat model is (assets, adversaries, threats addressed/accepted, architectural rules, open questions, retro questions). Cleaner separation; one document per attack surface.
- **Option B**: extend `docs/security/vault-threat-model.md` with a "§8 Chat mutation surface" section and additional architectural rules (Rule 8+). Less cross-doc navigation; clearer that the chat threat model inherits Rule 3 carryover.

The amendment's binding outputs:

1. **Asset enumeration** — what chat mutations can affect: ticket state, agent state, hiring proposals. Not vault, not auth, not user data.
2. **Adversary ranking** — same shape as vault threat model (operator mistakes, prompt injection, tool-result feedback loops). Operator-typed prompt injection (attack-class 2) gets specific treatment because chat composer is the natural attack vector.
3. **Threats addressed vs. accepted** — autonomous-execution posture means several threats are explicitly *accepted* with specific runtime mitigations rather than gate-blocked.
4. **Architectural rules** (numbered, binding):
    - Rule N: Chat mutation verbs are sealed at config time and CI-pinned. `BuildChatConfig` rejects unauthorized MCP server entries; the verb set in `garrison-mutate` is enumerated in code and tested.
    - Rule N+1: Vault is opaque to chat (M2.3 Rule 3 carryover, made explicit).
    - Rule N+2: Every chat-driven mutation writes an audit row + emits a `pg_notify` on the activity feed channel. Rule 6 carryover: audit row records `chat_session_id`, `chat_message_id`, `verb`, `args_signature`, `outcome`, never raw chat content.
    - Rule N+3: Tool-result feedback loops bounded by per-session cost cap + per-turn tool-call ceiling (the spec picks a number — e.g., 50 tool calls per turn — to prevent runaway loops).
    - Rule N+4: Chat container has no outbound network beyond supervisor + docker-proxy (already true; restated for explicitness).
    - Rule N+5: Mutation reversibility classified per verb. Spec writes the table. Reversible verbs (e.g., `transition_ticket` — easy to move back) have a lighter audit framing; semi-irreversible (e.g., `spawn_agent`) have a heavier audit framing including args snapshot.
5. **Open questions for milestone retro** — same shape as the vault threat model's §7. The M5.3 retro answers what the operator-week-of-use observed: any prompt injections that landed mutations, any tool-call chips that the operator wished had been gates, any verbs that should have been reversibility-classified differently.

### `garrison-mutate` MCP server

A new in-tree Go MCP server, supervisor-owned, exposed as `supervisor mcp garrison-mutate` subcommand. Stdio JSON-RPC, mirrors the `mcp finalize` shape. ~600-1000 LOC plus tests (larger than `finalize` because the verb set is broader). Lives at `supervisor/internal/garrisonmutate/` (or analogous; spec picks).

The mounted verb set:

**Tickets**:
- `create_ticket(title, description, department, priority?, labels?, parent_ticket_id?)` — writes to `tickets`, sets `origin = 'ceo_chat'`, returns `{ticket_id, ticket_url}`. Audit row references the chat session.
- `edit_ticket(ticket_id, title?, description?, priority?, labels?)` — partial-update of ticket fields the operator-side dashboard already lets the operator edit. Audit row carries the diff.
- `transition_ticket(ticket_id, to_status, reason?)` — moves between Kanban columns. Audit row carries from/to + reason. Hooks into the existing M2.x ticket-transition event-bus (`work.ticket.transitioned.<dept>.<from>.<to>`) so the activity feed surfaces the move alongside operator-driven moves.

**Agents**:
- `pause_agent(agent_role_slug, reason?)` — sets a flag the supervisor's spawn loop checks; in-flight spawns finish, no new spawns start. Audit row.
- `resume_agent(agent_role_slug)` — clears the pause flag. Audit row.
- `spawn_agent(agent_role_slug, ticket_id?)` — manually invokes the spawn path for a role on a specific ticket (or pulls the next eligible ticket from the role's queue). Mirrors what the operator-side dashboard does today; chat issues the command, supervisor spawns. Audit row.
- `edit_agent_config(agent_role_slug, model?, system_prompt_md?, mcp_config?, concurrency_cap?)` — updates `agents` row. Subject to vault leak-scan (the system_prompt_md must pass `vaultlog`-equivalent regex before commit; if a verbatim secret appears, the verb fails with `error_kind='leak_scan_failed'` and writes the failure to audit). Audit row carries the diff.

**Hiring**:
- `propose_hire(role_title, department, justification_md, skills_summary_md?)` — writes to a new `hiring_proposals` table (M5.3 schema delta — see "Schema deltas" below) with `proposed_via = 'ceo_chat'`. Does NOT spawn an agent or commit a hire — that's M7. Returns `{hiring_proposal_id, dashboard_url}` so the operator can review the proposal in M7's surface (or, until M7 ships, in a stopgap read-only view that M5.3 *may* include — see Open Q 9).

**Vault**: NOT IN VERB SET. (M2.3 Rule 3 carryover.)

Each verb implementation:
- Validates inputs (ticket exists, agent role exists, etc.) — returns typed `error_kind` on validation failure.
- Performs the write under a transaction.
- Writes the audit row in the same transaction.
- Emits the `pg_notify` after commit (Rule 6 + M4 audit pattern carryover).
- Returns a structured tool_result the chat surface renders as a chip.

The verb set is **sealed**: a CI-pinned test asserts the registered verb list is exactly the enumerated set. New verbs require an explicit code change + test update + (probably) a threat-model amendment.

### `BuildChatConfig` extension

The M5.1/M5.2 `BuildChatConfig` test asserts the chat container's MCP config has exactly `{postgres, mempalace}`. M5.3 extends to `{postgres, mempalace, garrison-mutate}` and the test asserts no fourth entry. The extension follows `mcpconfig.CheckExtraServers` (M2.3 Rule 3) precedent.

Per-spawn config is built from the chat session id; same per-session lifecycle as M5.1.

### Tool-call chip surface (informative, not gating)

The M5.2 message stream renders operator + assistant text bubbles. M5.3 extends to render `tool_use` and `tool_result` events as inline chips inside the assistant message envelope, between text content blocks, in stream-event order.

Chip variants per verb (the spec writes copy):
- **Pre-call chip** rendered on `tool_use` event arrival: `creating ticket "fix kanban drag bug" in growth/backlog…` (verbed-form, present-progressive). Visually distinct from text — chip primitive with an icon, slightly indented.
- **Post-call chip** rendered on `tool_result` event arrival: `created ticket #142` with a clickable deep-link to `/tickets/<id>`. Replaces the pre-call chip in place.
- **Failure chip** if the tool_result carries `is_error=true`: `failed to create ticket — <error_kind>` in the M5.2 error palette.

Chip styling reuses M3-design-language primitives (`Chip.tsx`, `StatusDot.tsx`, `font-tabular` for IDs and counts).

The chip surface is **purely informative**. Clicking a chip opens the linked resource in a new tab (or in the right pane once M5.4 ships); chips do NOT carry undo/cancel/approve affordances. The activity feed is the global view; the audit log is the forensic surface.

### SSE event union extension

`dashboard/lib/sse/chatStream.ts` event union extends with `tool_use` and `tool_result` variants. The supervisor's `internal/chat/policy.go` already routes these through `OnStreamEvent`; M5.3 wires them through to the `/api/sse/chat` route's frame emitter so the dashboard's `useChatStream` hook surfaces them.

The hook's return shape gains a new field (e.g., `toolCalls: Map<messageId, ToolCall[]>`) or extends `partialDeltas` with discriminated tool variants — spec picks. The renderer reads the buffer and composes chips between text blocks.

`Last-Event-ID` reconnect semantics: the supervisor's row-state-read mechanism (M5.2 amended FR-261) extends to read `chat_messages.raw_event_envelope` for committed tool calls and re-emit them on reconnect. Mid-stream tool calls that haven't terminal-committed yet are not replayed (same posture as text deltas).

### Activity feed event-row variants

New `ActivityEvent` discriminated-union variants per mutation verb:
- `chat.ticket.created`, `chat.ticket.edited`, `chat.ticket.transitioned`
- `chat.agent.paused`, `chat.agent.resumed`, `chat.agent.spawned`, `chat.agent.config_edited`
- `chat.hiring.proposed`

Each variant rendered by a new `EventRow` branch in `components/features/activity-feed/EventRow.tsx`, reusing the M3 audit-event design language. Rule 6 backstop: the variant payload carries only IDs and verbs, never raw chat content or argument text. (The audit row holds the full args; the activity feed shows the verb + IDs.)

The M5.2 retro-flagged "EventRow rendering for the M5.1 chat channels" (`session_started`, `message_sent`, `session_ended`) is **a separate decision** the operator made at M5.2 retro time — see Open Q 12. M5.3 may bundle this if it's easy; spec confirms.

### Audit table

A new `chat_mutation_audit` table (or extension of `vault_access_log` with a discriminator column — spec picks). Columns: `id`, `chat_session_id` (FK), `chat_message_id` (FK), `verb`, `args_jsonb` (the full args, including ID references), `outcome` (`success`/`error_kind`), `created_at`, optional `affected_resource_id` (ticket_id, agent_role_slug, hiring_proposal_id) for cross-table joins.

Why a new table vs. extending `vault_access_log`: chat mutations and vault accesses are different attack surfaces and different forensic needs; conflating them muddles both audit views. Spec picks if there's a good reason to share.

### Schema deltas

- **`hiring_proposals` table** for `propose_hire`. Columns at minimum: `id`, `role_title`, `department`, `justification_md`, `skills_summary_md`, `proposed_via` (`ceo_chat` / `dashboard` / `agent`), `proposed_by_chat_session_id` (FK, nullable), `status` (`pending` / `approved` / `rejected` / `superseded`), `created_at`. M7 extends with hiring-flow columns; M5.3 commits the table shape that won't churn under M7 work.
- **`chat_mutation_audit` table** (per above).
- **Channel-name registry expansion** in `dashboard/lib/sse/channels.ts` for the new `chat.*` channels.
- **Possibly**: `tickets.created_via_chat_session_id` FK column to make the chat-driven ticket origin queryable without joining through `chat_mutation_audit`. Spec picks; the existing `tickets.origin = 'ceo_chat'` value covers the categorical case but the FK lets the dashboard render "this ticket was created in chat session #142" without the join.

### Architecture amendment

`ARCHITECTURE.md:574` line gains an autonomous-execution-posture clause (the line currently says "M5.3 adds chat-driven mutations behind a threat-model amendment" — fine; the *autonomous* part wants explicit mention so future readers don't assume confirmation gates).

`ARCHITECTURE.md:103` (the agent ─► finalize_ticket diagram) gains a parallel chat ─► garrison-mutate path so the architectural pattern is visible.

`ARCHITECTURE.md:207` `tickets.origin` comment may want amendment if the spec opts for the FK column above.

Test pin (mirrors M5.2's `dashboard/tests/architecture-amendment.test.ts`): substring-match assertions for the M5.3 amendment line(s).

### Test coverage

- **Integration test** against the full chat → garrison-mutate → Postgres path. A test fixture in `garrison-mockclaude:m5` (extended) emits scripted `tool_use` envelopes for each verb; the supervisor's chat policy routes them to `garrison-mutate`; the test asserts the DB row lands, the audit row lands, the `pg_notify` fires, the SSE stream emits the `tool_use` + `tool_result` events.
- **Threat-model rule tests** — CI-pinned tests asserting the architectural rules from the amendment are mechanically enforced (sealed verb set, vault opacity, etc.).
- **Sealed-allow-list test** — `BuildChatConfig` extension test rejects a fourth MCP entry.
- **Vault leak-scan parity** — `edit_agent_config`'s `system_prompt_md` arg passes the `vaultlog`-equivalent scanner before commit; a test plants a `sk-`-shaped sentinel and asserts the verb fails with `leak_scan_failed`.
- **Cost-cap chaos test** — a scripted runaway loop (e.g., a tool-result feedback loop that re-triggers `create_ticket` on every response) hits the per-session cost cap and terminates with `error_kind='session_cost_cap_reached'`; no more than N rows land before the cap fires (the spec picks the bound — e.g., 50 ticket rows).
- **Concurrent-mutation chaos test** — chat issues `transition_ticket(#123, qa-review)` while operator drags `#123` from the dashboard; the spec-resolved conflict-handling strategy (Open Q 5) is exercised; both audit rows land; the final ticket state matches the spec-defined winner.
- **Playwright golden-path extension** to `dashboard/tests/integration/m5-2-chat-golden-path.spec.ts` (or a new sibling file): the operator asks the chat to create a ticket; the chip renders; the activity feed event row appears; the ticket exists in the dashboard; the operator navigates to the ticket; the round-trip is under SC-010's 10s budget.
- **Playwright chip-rendering test** — assert chip variants for each verb render with the spec-defined copy and ARIA semantics.

---

## Out of scope

### Per-call confirmation-gate UI for chat-driven mutations

The whole point. Tool calls are informative; the operator does not approve per-call. (Dashboard mutations continue to use `ConfirmDialog` tiers — that's a different surface.)

### Vault mutations from chat

Rule 3 carryover. Chat does not get `rotate_secret`, `delete_secret`, `edit_grant`, `add_grant`. Those are dashboard-only, with the M4 confirmation tiers, today and beyond M5.3.

### Knowledge-base pane (M5.4)

Right-pane "WHAT THE CEO KNOWS" content (Company.md / palace-writes / KG-facts) stays M5.4. M5.3 doesn't change the placeholder.

### M7 hiring flow

`propose_hire` writes a row; it doesn't ship the hiring review UI, the candidate-evaluation flow, the agent-config-template generation, or the agent-spawn handoff. All M7. M5.3 may include a stopgap read-only "Recent hiring proposals" surface so proposals are visible before M7 lands — Open Q 9.

### Mutation undo / rollback affordances

The activity feed is the audit; reversal is via dashboard or chat re-instruction. No "undo" button in the chip, no "rollback" verb in `garrison-mutate`. If post-M5.3 operator-week-of-use shows the operator wanting undo, M5.4 polish round or M5.5 layers it in.

### Multi-operator / multi-CEO

Single-operator continues. The threat model's autonomous-execution justification depends on this; multi-operator is post-M5 and will require threat-model re-amendment.

### Warm-pool / Claude pool optimization

Tracked-but-deferred from M5.1, M5.2. Still tracked-but-deferred. M5.3 doesn't address.

### Cost-telemetry blind spot fix and workspace-sandbox-escape fix

Both still surfaced from M3. Tracked at `docs/issues/`. M5.3 doesn't touch.

### Per-day / per-month cost caps

Per-session cap from M5.1 continues. M5.3 doesn't add a date-windowed cap.

### Model picker

Cosmetic badge from M5.2 stays cosmetic.

### Chat-history search

Post-M5.

### Mobile-first design

≥768px continues.

### Operator-tooling polish for M5.1 chat channels (M5.2 retro carryover)

The M5.2 retro flagged that EventRow rendering for `chat.session_started`, `chat.message_sent`, `chat.session_ended` was deferred. M5.3 *may* include this if it's natural (the new mutation channels need EventRow branches anyway, so adding three more is cheap), but it's not load-bearing. See Open Q 12.

### Replacing or revising any sealed M2-arc, M3, M4, M5.1, M5.2 surface

M5.3 is additive. No changes to existing tables/columns/server-actions/queries/routes outside the chat domain (and within the chat domain, M5.3 *adds* — it doesn't revise the M5.1 substrate or the M5.2 surface).

---

## Open questions the spec must resolve

These are decisions deliberately left for `/speckit.specify` + `/speckit.clarify` to make.

1. **Threat-model amendment location**: new `docs/security/chat-threat-model.md` (Option A — cleaner separation) or extension to `docs/security/vault-threat-model.md` with §8+ (Option B — closer to existing precedent)? Spec picks. Default lean: A, because chat and vault are different attack surfaces and conflating them muddles both.

2. **Mutation verb implementation strategy**: spike §4 listed three options — (1) new supervisor-owned MCP server (recommended), (2) HTTP-call tools targeting the dashboard's server actions (zero duplication, requires CSRF/session refactor), (3) shared library both M4 dashboard + M5.3 chat call into (cleanest long-term, biggest refactor). Spec confirms option 1 unless there's a reason to revise.

3. **Tool-call chip data source**: chips render from (a) the SSE delta stream (event-driven, simplest), (b) periodic Postgres reads of `chat_messages.raw_event_envelope` (consistent with M5.2's row-state-read mechanism), or (c) a new audit-row read keyed on the chat message. Spec picks. Default lean: (a) for live rendering, with (b) as the reconnect/replay source.

4. **Reversibility classification per verb**: the threat model's Rule N+5 needs a table. Reversible: `transition_ticket` (move back), `pause_agent` / `resume_agent` (paired). Semi-irreversible: `edit_ticket` (diff is preserved in audit), `edit_agent_config` (diff in audit). Effectively-irreversible: `create_ticket` (deletable but with side effects), `spawn_agent` (the agent runs, costs money, may write palace), `propose_hire` (a row in `hiring_proposals` that affects M7 visibility). Spec writes the binding classification.

5. **Concurrent mutation conflict handling**: chat issues `transition_ticket(#123, qa-review)` while operator drags `#123` in dashboard. Three strategies: (a) last-write-wins with no conflict surfacing (worst); (b) optimistic-lock-based reject — first commit wins, second commit returns `error_kind='ticket_state_changed'` (mirrors M4 lock pattern); (c) chat-mutation-defers-to-operator — chat checks for in-flight operator interaction and yields. Spec picks. Default lean: (b) — the M4 `lib/locks/` primitives already exist.

6. **Cost ceiling shape**: per-session cost cap from M5.1 continues; does M5.3 add a per-mutation-verb-cost contribution to the rollup, or just bills the underlying Claude turn cost? Spec confirms. Default lean: stays as-is — Claude turn cost already includes the tool-call overhead; mutation verbs are cheap to invoke server-side.

7. **`garrison-mutate` mount lifecycle**: chat container starts with mutate-mounted by default (broad scope, autonomous-execution-aligned), or does the spec hold a "mount only after operator opt-in for the session" stance from spike §6? The user's autonomous stance favours default-mounted; spec confirms. Default lean: default-mounted.

8. **Chip surface granularity**: every `tool_use` → chip, including queries (`postgres.query`, `mempalace.search`)? Or only mutation tool_uses get chips, and queries remain hidden? Spec picks. Default lean: every tool_use renders a chip (the operator wants to see what the chat is doing, not just what it wrote — informative observability is uniform). Chip styling differs (read chips lower-emphasis than mutation chips).

9. **Stopgap hiring-proposals view**: M7 ships the full hiring flow. M5.3 writes `hiring_proposals` rows. Should M5.3 ship a minimal "Recent hiring proposals" read-only surface (a sidebar entry or a dashboard page), or does it leave proposals invisible until M7? Spec picks. Default lean: minimal read-only surface — without it, `propose_hire` writes to a black hole until M7.

10. **Schema delta scope**: which of these land — `hiring_proposals` (definitely), `chat_mutation_audit` (definitely or extension of `vault_access_log`), `tickets.created_via_chat_session_id` FK (optional), per-mutation channel-name expansions (definitely)? Spec writes the migration.

11. **Architecture amendment timing**: amend `ARCHITECTURE.md` lines 103 + 207 + 574 alongside the spec, or as a follow-up commit during implementation, or at retro time? Spec picks. Default lean: alongside spec — the M5.2 retro's amendment-at-T012 pattern worked.

12. **EventRow rendering for M5.1 deferred chat channels**: M5.2 retro deferred `session_started`, `message_sent`, `session_ended` EventRow branches. M5.3 adds new chat.* channels with EventRow branches anyway; bundling the three deferred ones costs ~30 lines and closes the M5.2 carryover. Bundle, or defer further? Spec picks. Default lean: bundle.

13. **Test fixture extension**: `garrison-mockclaude:m5` ships scripted streams for M5.1/M5.2. M5.3 needs scripted streams that include `tool_use` envelopes for each verb. Does the mockclaude image grow per-verb fixture scripts, or does M5.3 ship a separate `mockclaude:m5-mutate` variant? Spec picks. Default lean: extend the existing image with new fixture flags.

14. **Prompt-injection chaos tests beyond cost-cap**: spike §6 attack-class-1 (palace-injected commands) and attack-class-2 (operator-typed prompt injection) deserve test fixtures. A test plants a malicious string in the palace; runs a chat turn that retrieves the string; asserts no `garrison-mutate` verb fires (or fires with the injected args, which is the threat the threat model has to *accept* if no gate exists). Does M5.3's test suite include these? Spec picks. Default lean: yes — the threat model is hollow if attack scenarios aren't asserted.

15. **MCP allow-list expansion ergonomics**: when the verb set inevitably grows post-M5.3, the spec's "sealed at config time + CI-pinned" rule applies to additions. Is there a developer-experience layer (e.g., a registry file the test reads) so adding a verb is one diff in two places, not five? Spec picks. Default lean: registry file under `supervisor/internal/garrisonmutate/verbs.go` that both the runtime and the test read.

16. **Tool-result feedback-loop bound**: the threat-model Rule N+3 names a per-turn tool-call ceiling. What's the number? 20? 50? 100? The bound is a runtime mitigation against attack-class-3 (tool-result feedback loops). Spec picks. Default lean: 50, with the cap configurable via env var (`GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN=50`).

---

## Acceptance criteria framing

The spec writes the full SC list. Framing for the spec to fill in:

- The threat-model amendment doc lands at the spec-resolved location, enumerates assets/adversaries/threats-addressed/threats-accepted, names architectural rules N through N+5 (or analogous), and is cited as binding by the M5.3 plan.
- The chat container's MCP config is exactly `postgres-RO + mempalace + garrison-mutate`. CI-pinned test asserts no fourth entry can be silently added.
- `garrison-mutate` exposes exactly the spec-resolved verb set. CI-pinned test asserts the registered verb list matches the enumeration.
- Each verb writes the expected DB row + audit row + emits the expected `pg_notify`. Integration test per verb.
- Each verb returns a structured `tool_result` the dashboard renders as a chip; the chip renders pre-call (on `tool_use`) and post-call (on `tool_result`); the chip variants match the spec-defined copy and ARIA semantics.
- The activity feed renders an event row for each chat-driven mutation per the new variants; the event-row payload contains zero raw chat content or argument-string text (Rule 6 backstop).
- Vault is opaque to chat: no verb in `garrison-mutate` references the vault; CI-pinned test asserts no `vault_*` tool name is registered.
- Vault leak-scan parity: `edit_agent_config`'s `system_prompt_md` passes the `vaultlog`-equivalent scanner; a planted secret triggers `error_kind='leak_scan_failed'` and an audit row.
- Per-session cost cap from M5.1 continues to hold; runaway-loop chaos test confirms the cap fires before > N mutation rows land (N is spec-defined).
- Per-turn tool-call ceiling from Rule N+3 fires when exceeded; chaos test confirms.
- Concurrent mutation conflict per Open Q 5: chat-vs-operator simultaneous `transition_ticket` resolves per the spec-resolved strategy; both audit rows land; final state matches the resolution.
- Playwright golden-path: operator types a chat instruction that triggers `create_ticket`; first chip renders within `<bound>` ms of `tool_use`; ticket exists in DB; activity feed event row appears; round-trip total under SC-010's 10s budget.
- Playwright chip-rendering: each verb's chip variant renders with the spec-defined copy and the M3 design language primitives.
- Architecture amendment lines 103 / 207 (if FK lands) / 574 are amended; test pin asserts the substrings.
- M5.2 retro carryover (Open Q 12) closed if bundled: EventRow renders for `chat.session_started`, `chat.message_sent`, `chat.session_ended`.
- Hiring stopgap (Open Q 9) renders if shipped: a minimal `/hiring/proposals` read-only view shows `hiring_proposals` rows.
- Threat-model amendment's chaos test fixtures (Open Q 14): planted-palace-injection test runs; the test asserts the *expected* behavior per the threat model (whether that's "verb fires with injected args, audit row records the chain" or "verb declines because of a runtime mitigation" — the threat model defines, the test pins).
- Zero new direct dependencies in `dashboard/package.json` AND `supervisor/go.mod` OR every new dependency is justified in commit messages and recapped in the M5.3 retro per the M3-established discipline.
- `vaultlog` analyzer passes on all new chat code paths.
- `goose up` applies cleanly; `bun run drizzle:pull` regenerates `schema.supervisor.ts` with the new tables; `sqlc generate` produces clean models for the new queries.

---

## What this milestone is NOT

- **Not a per-call confirmation-gate UI for chat-driven mutations.** Autonomous execution is the design; chips are observability, not approval.
- **Not a vault mutation surface from chat.** Vault stays operator-only via the dashboard.
- **Not the M5.4 knowledge-base pane.** Right-pane placeholder remains.
- **Not the M7 hiring flow.** `propose_hire` writes a row; the review/approve/spawn flow is M7.
- **Not a mutation undo/rollback surface.** Activity feed + dashboard + chat re-instruction are the recovery paths.
- **Not a multi-operator system.** Threat-model amendment depends on single-operator; multi-operator is post-M5.
- **Not a warm-pool optimization.** Tracked-but-deferred continues.
- **Not a cost-telemetry blind spot fix.** Surfaced from M3, still surfaced.
- **Not a workspace-sandbox-escape fix.** Surfaced from M3, still surfaced.
- **Not a per-day / per-month cost cap.** Per-session cap continues.
- **Not a model picker.** Cosmetic badge stays.
- **Not a chat-history search.** Persistence-only.
- **Not a mobile-first redesign.** ≥768px continues.
- **Not a replacement of the M4 ConfirmDialog patterns.** Dashboard mutations continue to use them; chat mutations don't.
- **Not a replacement or revision of any sealed M2-arc, M3, M4, M5.1, M5.2 surface.** Additive only.

---

## Spec-kit flow

1. **Now**: `/speckit.specify` against this context. Spec resolves the open questions inline (or flags the ones genuinely needing a clarify round). Specifically: threat-model location (Open Q 1), mutation verb implementation strategy (Open Q 2), chip data source (Open Q 3), reversibility classification table (Open Q 4), conflict-handling strategy (Open Q 5), cost-ceiling shape (Open Q 6), mount lifecycle (Open Q 7), chip granularity (Open Q 8), stopgap hiring view decision (Open Q 9), schema delta scope (Open Q 10), architecture amendment timing (Open Q 11), M5.2 EventRow carryover bundling (Open Q 12), mockclaude fixture extension shape (Open Q 13), prompt-injection chaos tests (Open Q 14), verb-registry developer ergonomics (Open Q 15), per-turn tool-call ceiling (Open Q 16).
2. **Then**: `/speckit.clarify` only if the spec leaves residual ambiguity. The threat-model amendment is the most likely source of clarify-round questions — it has to make load-bearing claims the M5.3 implementation depends on.
3. **Threat-model amendment lands as a binding input** before `/speckit.plan` — same M2.3 vault-threat-model-first pattern. The plan cites the amendment by section.
4. **Then**: `/speckit.plan` — picks the directory layout for `supervisor/internal/garrisonmutate/`, the verb-registry shape, the audit-table location, the schema migration, the SSE event union extension, the chip component shape, the test-fixture extension for mockclaude, the chaos test shapes for prompt injection + cost-cap + concurrent mutation, the architecture amendment patch.
5. **Then**: `/speckit.tasks` — turns the plan into ordered tasks with completion conditions. Expected shape: threat-model amendment doc (T001), schema migrations (T002), `garrison-mutate` server skeleton + verb registry (T003), per-verb implementations + audit + notify + tests (T004–T011, one per verb), `BuildChatConfig` extension (T012), SSE event union extension + dashboard chip components (T013), activity feed event-row variants (T014), Playwright golden-path + chip rendering (T015), chaos tests (T016), architecture amendment patch (T017).
6. **Then**: `/speckit.analyze` — checks for spec/plan/tasks consistency, flags FR/SC inconsistencies between the threat model and the implementation, asserts every architectural rule has a test pin.
7. **Then**: `/garrison-implement` (or `/speckit.implement`) — task-by-task execution.
8. **Then**: M5.3 retro at `docs/retros/m5-3.md`, MemPalace mirror per AGENTS.md §Retros, threat-model retro questions answered (analogous to vault threat model §7). The M5.4 context (knowledge-base pane) can start from the resulting healthy substrate.
