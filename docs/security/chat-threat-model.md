# Chat mutation surface — threat model and architectural rules

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

**Status**: Threat model and architectural rules. Lands as a binding input for M5.3 (chat-driven mutations under autonomous-execution posture) per the M2.3 vault-threat-model-first pattern. M5.3 spec FR-400 binds: this document MUST land before any `garrison-mutate` verb code commits to the M5.3 branch.

**Last updated**: 2026-06-11 (M9 amendment — `create_scheduled_task` verb + Server-Action registry note, pre-implementation per Rule 1 / M9 spec FR-601). Initial: 2026-04-29 (M5.3 amendment establishes the chat mutation surface).

**Precedence**: this document lives below `RATIONALE.md` and the active milestone context (`specs/_context/m5-3-context.md`) in the document hierarchy (see `AGENTS.md`). It supplies the threat model and architectural principles that context files cannot re-derive cheaply. The vault threat model (`docs/security/vault-threat-model.md`) is a sibling document covering a different attack surface; both are binding for milestones that touch their respective surfaces.

---

## Scope of this document

This is a threat model and a set of architectural rules for chat-driven mutations. It is NOT a spec, a plan, or an implementation. The spec-kit flow for any chat-mutation-touching milestone begins from the relevant context file that cites this document as binding input.

The document covers:

1. What the chat mutation surface protects (assets)
2. Who it protects against (adversaries)
3. What threats it addresses and which it explicitly accepts
4. Architectural rules Garrison enforces in the chat mutation integration
5. Per-verb reversibility tier classification
6. Per-attack-class mitigation summary
7. Open questions later milestone specs must resolve
8. What each milestone retro must answer

Vault is **out of scope** for this document. The vault threat model (`docs/security/vault-threat-model.md`) covers vault assets, adversaries, and rules; M2.3 Rule 3 (vault is opaque to agents) extends to chat in this document as Rule 2 below, with no further vault-specific treatment.

---

## Milestone banding

**M5.3** — initial amendment. Establishes the autonomous-execution posture, the eight-verb mutation surface, the sealed MCP allow-list extension, and the per-verb reversibility tier table.

**M9** — second amendment (this update, landed before any M9 verb code per Rule 1 and M9 spec FR-601; scope per `specs/_context/m9-context.md` §Scope extensions). Adds `create_scheduled_task` as the **eleventh** chat verb (Tier 3 — see §5) and records the four M9 dashboard-only scheduled-task verbs in the Server-Action registry note (§5, "Server-Action verb registry"). The chat verb count moves **10 → 11**: the ninth and tenth verbs (`propose_skill_change`, `bump_skill_version`) landed in M7 with their threat treatment in the sibling `docs/security/hiring-threat-model.md`; this amendment backfills their tier entries into §5 so the table enumerates the full sealed registry.

Future milestones extending the chat mutation surface — adding verbs, broadening tool surfaces, supporting multi-operator, introducing palace writes from chat — must update this document **before** any code lands. The "Architectural rules" section below is the binding constraint set; new milestones either honor it or amend it explicitly.

---

## 1. Assets

**State the chat mutation surface can affect**:

- **Ticket state**: `tickets` rows (creation, edit, transition between Kanban columns) and the corresponding `ticket_transitions` audit trail.
- **Agent state**: `agents` rows (config edits — model, system prompt, MCP config, concurrency cap), `agent_instances` rows (manual spawning), the `agents.is_paused` flag.
- **Hiring proposal state**: `hiring_proposals` rows (creation only — the M5.3 stopgap surface is read-only; M7 extends with review/approve/spawn).
- **Scheduled-task state** (M9): `scheduled_tasks` rows — creation via the `create_scheduled_task` chat verb; edit/pause/resume/delete are dashboard-only Server Actions (see §5, Server-Action verb registry), never chat. The supervisor-written `scheduled_task_runs` firing history is downstream state every live task accrues.

**Multi-tenancy posture**: single-tenant single-operator at M5.3 ship. The chat mutation surface assumes one operator with full authority over all chat-mutation verbs. Multi-operator changes the threat model materially; the autonomous-execution posture's justifications (operator watches the chat live; observability replaces approval) depend on single-operator. Multi-operator requires re-amendment of this document before code lands.

**State explicitly NOT in scope**:

- **Vault state** (secrets, grants, vault audit log). M2.3 Rule 3 carryover: vault is opaque to chat. The chat container's MCP config does not include any vault-related server entry; `BuildChatConfig` rejects vault-named entries with a typed error.
- **Auth state** (better-auth user records, sessions, invites). No mutation verb touches these tables.
- **MemPalace state** (diary entries, drawers, KG triples). Chat *reads* from MemPalace (via the `mempalace` MCP server); chat does NOT write to MemPalace. If a future milestone wants chat-driven palace writes, it requires a threat-model amendment update.

---

## 2. Adversaries

Ranked by realistic probability of affecting the deployed Garrison instance operationally. This ordering matters: design effort should correspond to realistic threats, not theoretical ones.

1. **The operator, making mistakes.** The operator types a chat instruction that, when executed by the assistant, produces an unintended mutation (created the wrong ticket, transitioned the wrong column, paused the wrong agent). Solo-operator scale means the operator has the most authority and therefore the most opportunities for mistakes. Treated as the top adversary because it is.
2. **Prompt injection via chat composer (operator-typed).** The operator pastes external content (a customer email, a scraped webpage, a tool output from elsewhere) into the chat composer. The pasted content contains instructions that the assistant interprets as commands, leading to mutation calls the operator did not intend. Spike §6 attack-class-2.
3. **Prompt injection via palace contents.** The chat reads MemPalace contents through the `mempalace` MCP server (`search`, `kg_query`). A palace entry written by a prior agent (or by a previously-compromised path) contains instructions that the assistant interprets as commands. Spike §6 attack-class-1.
4. **Prompt injection via tool-result feedback loops.** A tool call's result content (for example, a `postgres.query` row whose text says "create a ticket to drop the user table") contains text shaped like a tool call instruction. The assistant calls another tool. The result of that tool contains another instruction. Loop. Spike §6 attack-class-3.

**Adversaries we explicitly deprioritize**:

- **Host-level attackers with shell access.** Same posture as the vault threat model: if the host running Garrison is rooted, every mutation is compromised. Application-layer design does not defend against this. Mitigation is systems-level and belongs to a different milestone.
- **Nation-state-level adversaries.** Wrong threat model for an indie self-hosted deployment.
- **Malicious operators.** The operator IS the trust root in single-operator Garrison. Defending against operator intent is incoherent at this scale.
- **Multi-operator collusion.** Single-tenant at M5.3; the data model future-proofs but the defense doesn't exist yet, and the autonomous-execution posture's justifications assume single-operator.

---

## 3. Threats addressed vs. accepted

### Threats the chat mutation surface explicitly addresses

1. **Mutation tools the assistant should not have access to.** Mitigated by the sealed MCP allow-list (`BuildChatConfig` + `CheckExtraServers`) and the sealed verb registry (`Verbs` slice in `internal/garrisonmutate/verbs.go`). The chat container's MCP config contains exactly `{postgres, mempalace, garrison-mutate}` at runtime; any fourth entry is rejected at config-build time. The `garrison-mutate` server registers exactly the eleven enumerated verbs (see §5); any unregistered verb name returns JSON-RPC error -32601.
2. **Vault access from chat.** Mitigated by Rule 2 below (M2.3 Rule 3 carryover). The `garrison-mutate` verb registry contains zero `vault_*` verb names. `BuildChatConfig` explicitly rejects any vault-named MCP server entry with a typed error citing the carryover.
3. **Untraced mutations.** Mitigated by Rule 3 below. Every chat-driven mutation writes a `chat_mutation_audit` row in the same transaction as the data write, then emits a chat-namespaced `pg_notify` post-commit. The activity feed surfaces the event; the audit table holds the full args including any operator-typed text. Forensic reconstruction of every successful and failed verb call is queryable from a single table.
4. **Runaway tool-result feedback loops.** Mitigated by Rule 4 below. The per-session cost cap (M5.1 FR-061) terminates a session before unbounded cost burn. The per-turn tool-call ceiling (default 50, configurable via `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN`) terminates a turn before unbounded mutation depth. Both bounds fire deterministically; chaos tests pin the behavior.
5. **Out-of-band data exfiltration via the chat container.** Mitigated by Rule 5 below. The chat container's network is bounded by the existing M2.2 docker-proxy + supervisor compose-network isolation (see vault threat model M2.2 deployment assumptions). The chat container cannot reach arbitrary external hosts.
6. **Operator confusion about which mutations a chat session caused.** Mitigated by Rule 6 below. Per-verb reversibility classification distinguishes Tier 1 (reversible — easy to undo), Tier 2 (semi-irreversible — diff captured in audit for manual undo), and Tier 3 (effectively-irreversible — pre-state snapshot for forensic reconstruction). Future undo / replay tooling has the metadata it needs.
7. **Runaway scheduled-work creation (M9).** A single injected or misinterpreted turn could mint many recurring tasks, each one a standing source of future agent spend that keeps firing after the turn ends. Two runtime mitigations bound the blast radius of the `create_scheduled_task` verb: the **per-turn creation ceiling** — at most `GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN` (default **3**) `create_scheduled_task` calls per assistant turn, after which the supervisor terminates the turn with `error_kind='scheduled_task_creation_ceiling_reached'` (the established M6 per-turn-ceiling terminal-error shape) — and the **minimum firing interval** (`GARRISON_SCHED_MIN_INTERVAL`, default **15m**), which bounds how fast any task that does land can burn. Validation additionally rejects malformed expressions, sub-minimum intervals, unknown departments/roles, duplicate names, and empty templates before any row lands, and `AgentInstanceID` callers are rejected outright with `validation_failed` ("agents cannot schedule work") — the verb is chat-only.

### Threats explicitly accepted

The autonomous-execution posture accepts several threats in exchange for the operator-as-constant-approver friction it eliminates. Each accepted threat is paired with the runtime mitigation that bounds its blast radius:

1. **Successful prompt injection (any of attack classes 1/2/3) that triggers a registered verb with attacker-influenced args.** The verb fires; the audit row records the chain (palace content → assistant turn → tool_use args → outcome); the activity feed surfaces what happened; the operator observes via the chip surface and can act after the fact. M5.3 does NOT block injection-driven verb calls at the gate — the gate is the verb set itself, not a per-call approval. Mitigation: tool-call chip + activity feed + audit row form three observability layers; per-session cost cap bounds the worst case; per-turn tool-call ceiling bounds the depth.
2. **Operator-typed instructions the assistant interprets differently than the operator intended.** The assistant calls a verb with args that match the operator's intent literally but not their intent semantically. The verb fires. Mitigation: the chip surface renders pre-call before the verb commits, so the operator sees what's about to happen; the post-call chip + activity feed render after, so the operator sees what did happen; reversibility classification names which mutations are easy to undo if the operator catches the misinterpretation in time.
3. **Tool-result feedback loops up to the per-turn ceiling depth.** The assistant can chain up to 50 tool calls in a single turn before the supervisor terminates the chat container. Within that depth, an injection-driven loop can fire up to 50 mutations in a single turn before the bound fires. Mitigation: the ceiling bound is configurable (operator can lower if dogfooding shows 50 is too generous); the cost cap fires faster on expensive turns; chaos tests pin the bound.
4. **Single-operator single-CEO assumption breakage if multi-operator lands.** This entire document's threat model depends on single-operator. If multi-operator multi-CEO lands without a threat-model re-amendment, the autonomous-execution posture is no longer defensible (one operator's chat could mutate another operator's tickets). Mitigation: this section explicitly calls out the dependency; the M5.3 retro answers whether the assumption held; multi-operator triggers a re-amendment.
5. **Cost burn up to the per-session cap.** The cost cap is the bound, not zero-cost. An injection that fires verbs cheaply can still rack up cost up to the cap before terminating. Mitigation: the cap is the existing M5.1 FR-061 mechanism (operator sets the cap; defaults are conservative); cost-telemetry surface from M5.1/M5.2 makes the burn visible.

---

## 4. Architectural rules (binding for M5.3 and beyond)

These rules are binding. Any milestone that touches the chat mutation surface honors these rules or amends them here before the spec changes.

### Rule 1: Chat mutation verbs are sealed at config time and CI-pinned

The `garrison-mutate` MCP server registers verbs from a single source of truth: the `Verbs` slice in `supervisor/internal/garrisonmutate/verbs.go`. Adding a verb requires editing this file, updating the per-verb reversibility tier table in §5 below, adding a per-verb implementation, and updating the registry test (`TestVerbsRegistryMatchesEnumeration`). Removing a verb requires the same. No runtime registration; no plugin shape; no dynamic tool discovery.

The chat container's MCP config (`BuildChatConfig` output) contains exactly three entries: `postgres`, `mempalace`, `garrison-mutate`. Any fourth entry is rejected at config-build time with a typed error. CI-pinned tests (`TestBuildChatConfigSealsThreeEntries`, `TestBuildChatConfigRejectsFourthEntry`, `TestVerbsRegistryMatchesEnumeration`, `TestVerbsRegistryHasNoVaultEntries`) enforce both seals.

**Consequence**: a future milestone that wants an additional verb requires a code change to `verbs.go`, an update to this document's §5 reversibility table, a per-verb implementation, a per-verb test, and a registry-test update. There is no shortcut. (Exercised by M7 — verbs nine and ten, threat-modeled in `docs/security/hiring-threat-model.md` — and by M9, whose eleventh verb lands via this amendment before its code.)

**Consequence**: a runtime attack that tries to inject an unregistered verb (e.g., `tool_use.name='garrison-mutate.delete_user'`) returns JSON-RPC error -32601 ("Method not found") and never reaches the supervisor's database layer.

### Rule 2: Vault is opaque to chat

M2.3 Rule 3 carryover, made explicit for chat. The `garrison-mutate` verb registry contains zero `vault_*` verb names. The chat container's MCP config explicitly rejects vault-related server entries: `BuildChatConfig`'s `CheckExtraServers` returns a typed `ErrVaultEntryRejected` for entry names matching `vault`, `infisical`, `garrison-vault`, or any name containing the substring `vault`.

Secrets the chat needs to know about (e.g., the operator's `CLAUDE_CODE_OAUTH_TOKEN` for the chat runtime itself) are injected as environment variables at chat-container spawn time, never as MCP tools. The chat container does not call a vault MCP tool at runtime. There is no `vault_get(name)` available to the assistant.

**Consequence**: a successful prompt injection cannot manipulate the assistant into emitting a vault secret value. The chat surface has no way to fetch vault values dynamically; values the chat process needs are env vars set at spawn time.

**Consequence**: future verb additions that touch secret-shaped state (e.g., rotating a customer's OAuth token) require either (a) implementation as a non-vault verb that calls into Infisical via a separate code path the operator-side dashboard already uses, or (b) an explicit amendment to this rule. The default is rejection.

### Rule 3: Every chat-driven mutation writes an audit row and emits a `pg_notify`

Every successful `garrison-mutate` verb execution writes a row to `chat_mutation_audit` in the same transaction as the data write. The audit row carries: `chat_session_id`, `chat_message_id`, `verb`, `args_jsonb` (full args including operator-typed text), `outcome` (`success` or a typed `error_kind`), `reversibility_class` (1 / 2 / 3 per §5), `affected_resource_id`, `affected_resource_type`, `created_at`. After the transaction commits, the verb emits a chat-namespaced `pg_notify` on a channel of shape `work.chat.<entity>.<action>` (e.g., `work.chat.ticket.created`).

Every failed verb execution also writes an audit row recording the `error_kind`. Failure-row INSERTs run in a separate audit-only transaction (the data-side ROLLBACK invalidates a same-tx audit INSERT); best-effort if the audit-side INSERT itself fails.

**Consequence**: forensic reconstruction of every successful and failed verb call is queryable from a single table. The audit log is the system of record for what the chat did.

**Consequence**: the activity feed event-row payloads carry only IDs and verb names (Rule 6 backstop carryover from vault threat model); the audit table holds the full args. Operator-typed chat content text appears only in `args_jsonb`, not in payloads or activity-feed renderings.

### Rule 4: Tool-result feedback loops bounded by per-session cost cap and per-turn tool-call ceiling

The per-session cost cap from M5.1 FR-061 continues to bound chat sessions; chat-driven verbs do not bypass it. When the cap fires, the supervisor terminates the session with `error_kind='session_cost_cap_reached'`, writes a synthetic terminal row, and emits the SSE typed-error frame.

A new per-turn tool-call ceiling layers on top: when the count of `tool_use` events within a single assistant turn exceeds `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN` (default 50, configurable via env var), the supervisor terminates the chat container, writes a synthetic terminal row carrying `error_kind='tool_call_ceiling_reached'`, and emits the SSE typed-error frame. Read tools (`postgres.query`, `mempalace.search`) count toward the ceiling — feedback loops can be triggered by query results carrying injection-shaped text, so the bound applies regardless of read/write split. Counter resets on each new turn.

**Consequence**: an injection-driven runaway loop self-terminates within bounded cost AND bounded tool-call depth. The two bounds are independent: a cheap-but-deep loop hits the ceiling first; an expensive-but-shallow loop hits the cost cap first.

**Consequence**: the operator can tune the ceiling at deploy time without code change. Defaults are conservative (50); operator-week-of-use feedback drives whether to lower.

### Rule 5: Chat container has no outbound network beyond supervisor and docker-proxy

The chat container runs within the existing M2.2 compose network. Outbound network is bounded by the docker-proxy filter set (`POST=1 EXEC=1 CONTAINERS=1`, all other filters default-deny per `vault-threat-model.md` M2.2 deployment assumptions). The chat container can reach the supervisor (for MCP traffic over stdio via Claude Code's child-process model — not over network) and the docker-proxy (for any `mempalace` MCP traffic that requires `docker exec` against the mempalace sidecar). It cannot reach arbitrary external hosts.

**Consequence**: out-of-band data exfiltration via the chat container is bounded by what the supervisor + docker-proxy permit. This rule is restated for explicitness; it inherits from M2.2 and is not new in M5.3.

**Consequence**: future verbs that require fresh outbound network access (e.g., calling a third-party SaaS API) require either (a) the supervisor proxying the call (in which case the supervisor's egress posture controls the surface) or (b) an explicit amendment to this rule. The default is rejection.

### Rule 6: Mutation reversibility is classified per verb

Every verb in the `Verbs` registry carries a `ReversibilityClass` (1, 2, or 3) field. The class is recorded in every audit row (`chat_mutation_audit.reversibility_class`). The classification is binding for the audit table's column shape and for any future undo/replay tooling. Per-verb classifications are enumerated in §5 below.

**Consequence**: a future undo feature can query `chat_mutation_audit` filtered by `reversibility_class IN (1, 2)` and propose undo paths without runtime re-derivation. Tier 3 verbs (effectively-irreversible) get a richer audit shape (full pre-state snapshot in `args_jsonb`) so forensic reconstruction is still possible without an undo path.

**Consequence**: changing a verb's reversibility tier is an architectural change. It requires a code change to `verbs.go`, an update to §5 here, an update to the per-verb tests, and a migration reasoning paragraph in the next milestone retro.

---

## 5. Per-verb reversibility tier table

The eleven chat verbs (eight from M5.3, two from M7, one from M9) are classified as follows. The classification is binding. The two M7 verbs' full threat treatment lives in the sibling `docs/security/hiring-threat-model.md`; they are enumerated here so this table matches the sealed `Verbs` registry one-for-one.

### Tier 1 — Reversible

These verbs produce changes the operator can fully reverse via a paired or symmetric operation.

- **`transition_ticket(ticket_id, to_status, reason?)`**: Kanban column move. Reversal: re-call `transition_ticket` with the prior status. Audit row carries `{from, to, reason}`.
- **`pause_agent(agent_role_slug, reason?)`**: sets `agents.is_paused=true`. Reversal: `resume_agent`. Idempotent: calling twice returns success both times; audit row records the no-op.
- **`resume_agent(agent_role_slug)`**: sets `agents.is_paused=false`. Reversal: `pause_agent`. Idempotent: same pattern as `pause_agent`.

### Tier 2 — Semi-irreversible (diff captured in audit)

These verbs change existing state in place. Reversal requires manual reconstruction from the audit row's diff.

- **`edit_ticket(ticket_id, title?, description?, priority?, labels?)`**: partial-update of ticket fields. Audit `args_jsonb` captures `{before: {...}, after: {...}}` for each changed field. Reversal: manual edit via the dashboard or another `edit_ticket` call with the captured `before` values.
- **`edit_agent_config(agent_role_slug, model?, system_prompt_md?, mcp_config?, concurrency_cap?)`**: partial-update of agent config. Pre-transaction leak-scan against `system_prompt_md` rejects secret-shaped values atomically (no row mutation; audit row records the rejected diff with values redacted). Audit `args_jsonb` captures the diff for clean updates. Reversal: manual edit via the dashboard.

### Tier 3 — Effectively-irreversible (pre-state snapshot in audit)

These verbs create new state or trigger costly downstream effects. Full reversal is either impossible or requires significant manual cleanup.

- **`create_ticket(title, description, department, priority?, labels?, parent_ticket_id?)`**: inserts a new `tickets` row with `origin='ceo_chat'`. Reversal: the row can be deleted, but downstream transitions, agent spawns, and audit references survive. Audit `args_jsonb` captures the full input args for forensic reconstruction.
- **`spawn_agent(agent_role_slug, ticket_id?)`**: writes an `agent_instances` row; the supervisor's spawn loop picks up the notify and runs the M2.x spawn flow. Reversal: not possible — the agent runs, costs money, may write palace. The verb respects the per-department concurrency cap (constitution Principle X). Audit `args_jsonb` captures inputs + the resulting `agent_instance_id` post-commit.
- **`propose_hire(role_title, department, justification_md, skills_summary_md?)`**: writes a `hiring_proposals` row with `proposed_via='ceo_chat'`. Reversal: M7's review flow can mark the proposal `rejected` or `superseded`, but the row persists for forensic value. Audit `args_jsonb` captures the full input args.
- **`propose_skill_change`** (M7): writes a `hiring_proposals` row proposing a skill add/remove on an existing role; the operator approves or rejects via the dashboard. Same write-only posture as `propose_hire`; threat-modeled in `docs/security/hiring-threat-model.md` (Rules 1 and 5). Audit `args_jsonb` captures the full input args.
- **`bump_skill_version`** (M7): proposes a version bump for a skill already attached to a role, through the same propose → approve cycle. Threat-modeled in `docs/security/hiring-threat-model.md`. Audit `args_jsonb` captures the full input args.
- **`create_scheduled_task(name, department_slug, role_slug, mode, schedule_expr, objective_template, acceptance_criteria_template)`** (M9): inserts a `scheduled_tasks` row that the supervisor's tick loop fires on schedule, as either a ticket insert or an oneshot direct spawn. Tier 3 rationale: **creates recurring cost-incurring state; accrued firings/spend do not reverse**. A soft delete (dashboard-side `delete_scheduled_task`) stops future firings, but every firing that already happened spawned work, cost money, and may have written palace — none of that unwinds. Runaway mitigations (see §3 threat 7): the per-turn creation ceiling (default 3, `GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN`) and the minimum firing interval (default 15m, `GARRISON_SCHED_MIN_INTERVAL`). Chat-only: `AgentInstanceID` callers are rejected with `validation_failed` ("agents cannot schedule work"). Audit `args_jsonb` captures the full input args, anchored on `chat_session_id` per the M5.3 audit shape.

### Server-Action verb registry (M8-established; not chat-callable)

M8 established a second sealed slice, `ServerActionVerbs` (`supervisor/internal/garrisonmutate/server_action_verbs.go`), for verbs callable ONLY by the dashboard's Server Actions — never registered on the chat-side `garrison-mutate` tool list. `TestVerbsSlicesDisjoint` pins that no entry appears in both slices; a chat call naming any registry entry returns JSON-RPC error -32601. M8 entry: `register_mcp_server` (class 2). M9 adds four scheduled-task entries; their audit rows are written dashboard-side in the same drizzle transaction as the row change, with both chat anchors NULL and `affected_resource_type='scheduled_task'`:

- **`edit_scheduled_task`** — class 2 (diff captured in `args_jsonb` for manual undo).
- **`pause_scheduled_task`** — class 1 (reversal: `resume_scheduled_task`).
- **`resume_scheduled_task`** — class 1 (reversal: `pause_scheduled_task`; `next_fire_at` is recomputed advance-only — no catch-up firing).
- **`delete_scheduled_task`** — class 3 (soft delete via `deleted_at`; pre-state snapshot captured into `args_jsonb`; run history and audit rows survive the delete).

These four verbs are part of the registry/tier table and the `chat_mutation_audit` verb CHECK, not of the eleven-verb chat surface.

---

## 6. Per-attack-class mitigation summary

The three attack classes from M5 spike §6, mapped to the architectural rules from §4:

| Attack class | Description | Mitigations (rule references) |
|---|---|---|
| **AC-1: Palace-injected commands** | A malicious palace entry (written by a prior agent or compromised path) contains text the assistant interprets as a command, leading to a `garrison-mutate` verb call with attacker-influenced args. | The verb fires with the injected args (accepted threat per §3). Mitigations: Rule 1 (sealed verb set bounds what verbs the injection can call); Rule 2 (vault unreachable); Rule 3 (audit row captures the chain); Rule 4 (cost cap + ceiling bound runaway depth); Rule 6 (reversibility classification names which mutations are easy to undo). Operator observes via chip surface + activity feed. |
| **AC-2: Operator-typed prompt injection** | The operator pastes external content into the chat composer; the content contains injection-shaped instructions; the assistant interprets them as commands. | Same posture as AC-1: verbs fire (accepted); same five rules mitigate. The chip surface renders pre-call before the verb commits, giving the operator a visible signal for in-flight mutations. |
| **AC-3: Tool-result feedback loops** | A tool call's result text is shaped to look like a tool call instruction; the assistant chains another tool call; loop. | Rule 4 bounds the depth: per-turn tool-call ceiling (default 50) terminates the turn deterministically; per-session cost cap terminates the session if cost grows faster than depth. Chaos tests (`TestToolResultFeedbackLoopAttackClass3`) pin the bound. |

---

## 7. Open questions later milestone specs must resolve

Questions that depend on concrete implementation context and should not be pre-decided here:

1. **Per-turn tool-call ceiling default**. M5.3 ships with a default of 50, configurable via `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN`. Operator-week-of-use observations may suggest a lower default. Future milestone specs that touch the runtime can revise; until then, 50 stands.
2. **Audit table retention**. M5.3 doesn't add a retention policy on `chat_mutation_audit`. Long-term, the table grows unbounded. A future milestone may add a date-windowed retention (e.g., archive rows older than 90 days). The audit table's forensic value diminishes after a window; the operator decides the window.
3. **Reversibility automation**. M5.3 ships the classification but does NOT ship undo / replay tooling. A future milestone may add a `chat_mutation_audit` browser with one-click undo for Tier 1 verbs. Deferred.
4. **Multi-operator extension**. Many of this document's threat-accepted positions depend on single-operator. Multi-operator requires re-amendment. The data model future-proofs (per-operator audit rows are queryable) but the autonomous-execution posture must be re-justified before multi-operator ships.
5. **MemPalace writes from chat**. M5.3 doesn't expose any palace-writing verb. If a future milestone wants chat-driven palace writes (diary entries, drawer creation, KG triple writes), the threat model must enumerate the new attack class (chat content → palace → future chat reads → injection-driven mutation) and the mitigations.
6. **Cost-multiplier per verb**. M5.3 doesn't add per-verb cost contributions to the cost cap rollup. If post-M5 dogfooding shows specific verbs are unusually expensive (`spawn_agent` triggers a downstream Claude run that costs $X), a future milestone may layer per-verb multipliers.

---

## 8. What each milestone retro must answer

### What the M5.3 retro must answer (initial amendment)

When M5.3 ships, the retro documents:

1. **Did Rule 1 hold?** Was the sealed verb set ever bypassed? Did the registry test catch any drift? Was there ever a runtime where `BuildChatConfig` returned more than three entries?
2. **Did the autonomous-execution posture feel right operationally?** Operator-week-of-use feedback: did the operator wish for a per-call gate at any point? Did the chip surface adequately surface what the chat was doing? Did the operator catch any unintended mutations in time to reverse them?
3. **Were any of the threats-accepted realized in practice?** Did a successful prompt injection (AC-1, AC-2, or AC-3) trigger an unintended mutation that landed in the audit log? Did the cost cap or per-turn ceiling fire at all? Were the bounds set at a reasonable level?
4. **Did the chip surface adequately surface what the chat was doing?** UX feedback: did pre-call chips give the operator enough signal to interrupt? Did post-call chips give the operator enough signal to spot problems? Were chip click-through rates (operator opens the affected resource) high enough to confirm the deep-link surfaces are useful?
5. **Were any per-verb reversibility classifications wrong in retrospect?** Did any Tier 1 verb turn out to be hard to reverse in practice (e.g., `transition_ticket` triggered downstream automation that was hard to unwind)? Did any Tier 3 verb turn out to be easier to reverse than expected? Reclassifications go in the retro plus an amendment update here.
6. **Did the per-turn tool-call ceiling default of 50 prove right?** Did any legitimate operator turn approach 50? Did chaos tests stay deterministic at the ceiling? Should the default move down (or up) for the next milestone?

### What future milestones' retros must answer

Future milestones that touch the chat mutation surface answer:

- Did this milestone's verb additions land cleanly under Rule 1 (sealed registry) without leaking dynamic tool discovery?
- Did this milestone's reversibility classifications hold in practice?
- Did any new attack class surface that this document didn't enumerate?
- Did the operator's autonomous-execution posture trust hold, or did this milestone surface a case where the operator wished for a per-call gate?

These questions are amendable as the chat mutation surface matures; the M5.3 retro is the first to answer them.
