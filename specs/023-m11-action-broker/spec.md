# Feature Specification: M11 — Action Broker (outbound external actions, gated by policy)

**Feature Branch**: `023-m11-action-broker`
**Created**: 2026-06-12
**Status**: Draft
**Input**: `specs/_context/m11-context.md` (binding — read in full first); `ARCHITECTURE.md` §M11 (the committed design — the unusually prescriptive paragraph that names the verb, the four tiers, the dispatcher, the Outbox, the permanent-Approve floor, and the audit posture) + §M10/§M12/§M13 (the boundaries on either side); `RATIONALE.md` §6 (hiring is propose→approve→execute in the UI, not git — the pattern M11 universalizes) + the M2.2.1 mechanism-over-prompt posture; `docs/security/agent-sandbox-threat-model.md` + `supervisor/egress/squid.conf` (the default-deny networking + Anthropic-only egress allow-list that makes the broker the only door); `docs/security/hiring-threat-model.md` + `specs/_context/m7-context.md` + `docs/retros/m7.md` (the propose→approve→install lifecycle); `docs/security/vault-threat-model.md` (Rule 1 — credentials never in agent context, extended to outbound action credentials); `specs/_context/m8-context.md` + `docs/retros/m8.md` (the `agent_instance_id` audit anchor + sealed-verb registry mechanics); `specs/_context/m9-context.md` + `docs/retros/m9.md` (the most recent sealed-verb addition, the immutable-history table pattern, the `ServerActionVerbs` precedent, the `FOR UPDATE SKIP LOCKED` exactly-once discipline); `specs/_context/m10-context.md` + `docs/retros/m10.md` (the inbound boundary M11 completes, the supervisor-side-worker shape, the threat-model-first + git-log-acceptance discipline); `AGENTS.md` + `.specify/memory/constitution.md` (sealed-surfaces list, precedence, locked-deps soft rule, Principles I/III/VI).

M11 closes the structural gap ARCHITECTURE §M11 names as "the keystone of the AI-native-company arc": agents can currently *think* and *write internally* (read a sortie issue via M10, reason, write to workspace and MemPalace, create internal tickets via M8) but **cannot act on the world** — and today that gap is enforced *structurally*, because agent containers join the `internal: true` `garrison-agents` network whose only egress is a squid sidecar with an allow-list of exactly `api.anthropic.com` (see "Scope-mismatch check #1" in m11-context.md; verified against `supervisor/egress/squid.conf`). M11 gives agents a **requesting** path — never a direct-acting one — through a single audited, policy-gated broker, and it universalizes the M7 hiring queue (propose → operator approves → system executes; RATIONALE §6) into the universal outbound path.

The milestone ships the action-broker **framework** plus the **GitHub public comment-back** as the first concrete action type riding it (m11-context.md §Scope resolution). The framework is four threads: (1) the sealed `request_external_action` MCP verb — the 12th verb in the registry — that writes an immutable pending-action row and does not act; (2) the tier policy table that classifies every action type into `auto` / `notify` / `approve` / `human_only`, keyed on action type, in the table not in prompts, with a structural permanent-Approve floor for public-facing actions; (3) the supervisor-side dispatcher worker that executes approved/auto/notify actions with vault-scoped credentials agent containers never see; (4) the Outbox dashboard surface that is the operator's approval queue. GitHub comment-back is Approve-tier by the floor, so the first end-to-end path exercises the `approve` tier — the one the Outbox exists for. Mail-send, release-cut, price/copy changes, and social posts are fast-follow action types on the same framework, out of scope this ship (m11-context.md §Out of scope #3).

This spec elaborates the committed design and resolves the ten open questions in m11-context.md §Open questions; it does not re-decide the committed core (the verb, the four tiers and their semantics, the dispatcher, the Outbox, the permanent-Approve floor, the audit posture, the broker-is-the-only-door enforcement).

## Clarifications

The operator was absent for this phase. Every open question in m11-context.md §Open questions was resolved from the binding inputs using the context file's stated lean, and is recorded below. Each remains overridable at `/speckit.clarify`.

### Session 2026-06-12 (resolved from binding inputs, operator absent)

- Q1 (first action provider + spike): GitHub public comment-back is the first action type. It is spec'd directly — no milestone-gating spike — because comment-create reuses the GitHub auth surface the M10 spike already mapped (App / PAT / token landscape). The dispatcher's GitHub call is an HTTPS request to the GitHub REST API, achievable in stdlib `net/http`; any new Go dependency must be justified or avoided (AGENTS.md locked-deps soft rule). If the comment-create path turns out to need empirical probing, that is a narrow action-type spike inside `/garrison-plan`, not a re-spec. Encoded in FR-019, FR-020.
- Q2 (action-type count in M11): One action type this ship (GitHub comment-back), the framework-first lean. The tier table is built as a genuine table (keyed on action type), so a second action type rides it without re-architecture; whether a `notify`-tier second type co-ships is left to the operator at `/speckit.clarify` (the framework FRs hold regardless). Encoded in the In scope / Out of scope boundary.
- Q3 (`pending_actions` schema + tier-table shape): A new immutable `pending_actions` row table (action type, target, rendered payload, requesting `agent_instance_id`, serving ticket/context, tier, status) plus an immutable per-action outcome history table mirroring M9's `scheduled_task_runs` → `scheduled_tasks` shape. The tier policy lives in the table/code such that the permanent-Approve floor cannot be lowered (the enforcement shape — protected set, non-overridable column, or CHECK — is a plan decision; the floor itself is binding). Encoded in FR-010..FR-013, FR-024, Key Entities.
- Q4 (caller model): `request_external_action` is callable by any active agent, anchored on `agent_instances.id` (the M8 `Deps.AgentInstanceID` seam), with defence-in-depth from the tier gate + permanent-Approve floor downstream — the M8 lean. The verb joins the existing `garrison-mutate` MCP surface as a new entry rather than a new server (stay unified, the M8 Q6 lean). Whether the chat-CEO may also call it directly is left open at `/speckit.clarify`; the lean is yes, since the tier gate governs effect regardless of caller. Encoded in FR-001..FR-004.
- Q5 (tier-table editability): The tier table is deploy-time config for M11 alpha — no operator surface to raise or lower tiers — mirroring M10's deploy-time-connector-config lean. The permanent-Approve floor is never lowerable regardless of any future editability. Encoded in FR-009, FR-014; recorded as Assumption.
- Q6 (dispatcher placement + lifecycle): The dispatcher is a supervisor-side reactive worker outside the agent network (the M9 tick-loop / M10 ingress-server / `mcpserverwork.Worker` shape). The spec states the reactive contract (reacts to new `auto`/`notify` rows and to operator approvals; `pg_notify` with a `processed_at`-style fallback poll for at-least-once; exactly-once dispatch under the M1 race); the exact goroutine-vs-process placement is a plan decision. Encoded in FR-017..FR-024.
- Q7 (notify-tier + notification depth): Minimum for alpha is the Outbox approval queue plus the immutable action history; `notify`-tier actions surface as a post-hoc feed item in the Outbox (executed, then operator-told), and `approve`-tier pending actions surface as an Outbox queue entry. The broker stays pull-only for alpha — no active push notification ships in M11. Encoded in FR-027, FR-028; recorded as Assumption.
- Q8 (dispatch failure + retry posture): On a recoverable external-call failure (5xx, rate limit, transient network), the dispatcher records the failed attempt to the immutable outcome history and marks the action `failed` without auto-retry in M11 alpha — the safe posture given exactly-once must never become double-post. A failed action surfaces to the operator in the Outbox for manual re-request; no automatic retry/backoff loop ships this milestone. Encoded in FR-022, FR-023; recorded as Assumption.
- Q9 (`human_only` preparation surface): A `human_only` action is never dispatched. The agent's prepared payload is recorded on the pending row; the operator sees it in the same Outbox surface and marks it done by hand via a "mark as done" Server Action that records a completion outcome (and an optional free-text note of what was actually done) to the immutable history. Encoded in FR-027.
- Q10 (permanent-Approve default for unclassified action types): A newly-added action type with no explicit tier classification defaults to `approve` — safe-by-construction, so a new action type is never silently `auto`/`notify`. GitHub public comment-back is enumerated into the floor explicitly. Encoded in FR-014, FR-015.

### Session 2026-06-12 (resolved by verify-phase clarify, operator absent)

- Q-A (pending_actions schema namespace): `pending_actions` and its outcome-history sibling table live in the `work` schema — the same schema as `tickets`, `ticket_transitions`, and `hiring_requests`. All work-entity rows share a namespace; no new schema is introduced. The `garrison_agent_ro` and `garrison_dashboard_app` Postgres roles grant access to `work.*`; `pending_actions` inherits those grants without a separate grant statement. Encoded in Key Entities and FR-003.
- Q-B (dispatcher observability / slog signals): Dispatcher dispatch attempts, vault-fetch failures, and rate-cap hits are logged via `slog` structured fields (the M1/M10 pattern) and durably recorded in the action outcome-history table (the durable record IS the audit, per M9's `scheduled_task_runs` shape). No new `throttle_events` kind is introduced for dispatcher-internal events in M11 alpha; `throttle_events` continues to serve cost/rate-cap events only (M6 machinery unchanged). The dispatcher's `pg_notify` channel (`work.action.dispatch_requested` or similar) follows the dot-delimited naming convention. Encoded in FR-018, FR-024.
- Q-C (Outbox real-time update behavior): The Outbox is a pull-only surface for M11 alpha — consistent with the "broker stays pull-only for alpha" resolution (Q7/Assumption). It follows the M7 hiring-queue pattern (standard Next.js Server Component, re-fetches on navigation/Server Action completion), not the M3 activity-feed SSE pattern. No new `pg_notify` channel for Outbox live-push ships in M11. Encoded in FR-025, FR-028.
- Q-D (chat-CEO caller eligibility): `request_external_action` is agent-callers-only for M11 alpha. The chat-CEO is an operator-level principal (operator voice, not a sandboxed agent container); adding it as a direct caller in alpha introduces an approval-integrity question (operator queues action via CEO → same operator approves it — same human on both sides of the gate). The safe-by-default posture is agent-only: the operator can always instruct an agent to call the verb, providing the same practical capability through the governed path. The chat-CEO caller path is an additive follow-up. FR-002 is authoritative as written (agent-callers only). Encoded in FR-002; the Assumption "lean: yes for chat-CEO" is superseded by this resolution.
- Q-E (GitHub dispatcher credential type): The dispatcher uses a **GitHub Personal Access Token (PAT)** for M11 alpha — consistent with the M10 spike finding F5 ("GitHub App JWT: Not present — Not needed for alpha; App mode is follow-up"). A PAT is a single vault secret (e.g. path `actions/GITHUB_PAT`) fetched by the dispatcher at dispatch time and sent as `Authorization: token <PAT>` on the REST API comment-create call. No RS256 JWT signer, no installation-token exchange loop, no new Go dependency. GitHub App installation token (short-lived, multi-org) is the fast-follow for production hardening. The `internal/leakscan` patterns already include `github_pat` shape for leak-scan coverage. Encoded in FR-019, FR-020, FR-023; noted in Assumptions.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An agent requests a GitHub reply; it lands as a pending approval, nothing reaches the world (Priority: P1)

A Concierge agent, working a sortie-issue triage ticket that arrived via M10, decides the right response is to reply on the GitHub issue. It cannot reach GitHub — the agent network has no route out. Instead it calls the sealed `request_external_action` MCP verb with the action type (`github_issue_comment`), the target (the issue), and the rendered reply text. The verb does **not** post anything. It writes exactly one immutable `pending_actions` row anchored on the calling `agent_instance_id`, the tier policy table classifies the action as `approve` (GitHub public comment-back is on the permanent-Approve floor), and the verb returns a typed result telling the agent the action is *queued, pending operator approval at the approve tier*. The agent's diary reflects "requested, pending approval," not "done." The reply appears in the operator's Outbox approval queue. Nothing has reached GitHub.

**Why this priority**: this is the milestone's reason to exist — the first time an agent expresses an intent to act on the world and the broker turns it into a governed, immutable, operator-gated request instead of a direct action. It exercises the framework spine end-to-end (verb → tier classification → immutable pending row → typed queued result → Outbox surfacing) and proves the load-bearing invariant that an agent requests rather than acts.

**Independent Test**: from inside an agent container, call `request_external_action` for a `github_issue_comment`; observe exactly one immutable `pending_actions` row anchored on the calling `agent_instance_id`, tier `approve` assigned by the policy table (not by any agent-supplied field), a typed queued/at-tier result returned to the agent, the row visible in the Outbox queue, and **no** GitHub API call having been made.

**Acceptance Scenarios**:

1. **Given** an active agent instance working a ticket, **When** it calls `request_external_action` with action type `github_issue_comment`, a valid target, and rendered payload, **Then** exactly one immutable `pending_actions` row is written, anchored on the calling `agent_instance_id` and carrying the serving ticket/context, with status `pending` and tier `approve` assigned by the policy table.
2. **Given** the same call, **When** it returns, **Then** the verb returns a typed `garrisonmutate.Result` stating the action is queued and at the `approve` tier — never a result implying the action was performed.
3. **Given** the pending row, **When** the operator opens the Outbox, **Then** the action appears in the approval queue with action type, target, full rendered payload, the requesting agent + serving ticket, and the tier with the reason it is at that tier (permanent-Approve floor).
4. **Given** an agent that supplies a `tier` field in its request attempting to set `auto`, **When** the verb processes it, **Then** the agent-supplied tier is ignored and the policy-table classification (`approve`) is used — the tier is never agent-decided.

---

### User Story 2 - The operator approves the queued action; the dispatcher posts it exactly once with vault-scoped credentials (Priority: P1)

The operator reviews the pending GitHub reply in the Outbox, reads the rendered payload, and clicks Approve. The approve click is a dashboard Server Action (anchored like M7's `approve_hire` — Server-Action-only, both chat and agent anchors NULL), which transitions the pending row to `approved`. The supervisor-side dispatcher reacts to the approval, claims the row exactly once under the M1 race (`FOR UPDATE SKIP LOCKED` + a terminal status transition), fetches the GitHub credential from the vault — a credential no agent container ever sees — posts the comment to the GitHub issue, and appends an immutable `executed` outcome to the action's history anchored to the originating `agent_instance_id`. The reply now exists on GitHub; the audit reconstructs to the originating agent, the payload, the tier, the approving operator, and the outcome.

**Why this priority**: this is the other half of the keystone — the only path by which anything actually reaches the outside world. It proves the operator gate (approve transitions state, nothing fires before the click), the supervisor-side execution with vault-scoped credentials (the agent never holds the credential), exactly-once dispatch (no double-post under restart/race), and the immutable reconstructable audit. Without it the milestone is a queue that never drains.

**Independent Test**: approve a queued `approve`-tier action via the Outbox Server Action; observe the row transition to `approved`, the dispatcher claim it once, the GitHub comment posted exactly once using a vault-scoped credential absent from every agent context, an immutable `executed` outcome appended to the action's history anchored to the originating `agent_instance_id`, and the row reach a terminal status. Replay/restart mid-dispatch and confirm no second post.

**Acceptance Scenarios**:

1. **Given** a `pending`/`approve`-tier row, **When** the operator clicks Approve in the Outbox, **Then** an audited dashboard Server Action transitions the row to `approved` (recording the approving operator), and nothing has yet reached GitHub.
2. **Given** an `approved` row, **When** the dispatcher runs, **Then** it claims the row exactly once (`FOR UPDATE SKIP LOCKED` + terminal transition), posts the GitHub comment using a vault-scoped credential, and appends an immutable `executed` outcome to the action's history anchored to the originating `agent_instance_id`.
3. **Given** an `approved` action delivered twice (operator redelivery, `pg_notify` replay, or a supervisor restart mid-dispatch), **When** the dispatcher processes it, **Then** the GitHub comment is posted **exactly once** — no double-post — and the second claim is a no-op against the terminal status.
4. **Given** a dispatched action, **When** the audit is reconstructed, **Then** it yields the originating `agent_instance_id`, the requested payload, the assigned tier, the approving operator, and the dispatch outcome — all immutable.
5. **Given** the dispatcher's GitHub credential, **When** any agent container's env, prompt, and context are inspected, **Then** the credential never appears — it lives only in the supervisor-side dispatcher's vault-scoped fetch (vault-threat-model Rule 1 extended to outbound action credentials).

---

### User Story 3 - The permanent-Approve floor cannot be lowered (Priority: P1)

The tier policy table classifies GitHub public comment-back as `approve` by the permanent-Approve floor — the structural guarantee that anything public-facing (posts, replies, releases, mail to non-customers, price/copy changes) is Approve-tier permanently (ARCHITECTURE §M11). An attempt — whether a misconfigured deploy-time tier entry, an agent-supplied tier field, or a contrived edit — to classify a floor action type as `auto` or `notify` is rejected or ignored structurally; the action lands at `approve` regardless.

**Why this priority**: reputation is the existential risk for an agent-operated company — "one spammy autopost costs more than a year of approval clicks saves" (ARCHITECTURE §M11). The floor being a *structural* property of the table rather than a *current configuration* an injection could flip is what makes the milestone safe to ship. It is P1 because a silent downgrade defeats the broker's whole point.

**Independent Test**: attempt to classify a floor action type (`github_issue_comment`) as `auto`/`notify` via both an agent-supplied tier field and a contrived lower-the-tier config/edit; observe the attempt rejected or ignored and the action classified `approve` regardless. Add a new action type with no classification and observe it default to `approve`.

**Acceptance Scenarios**:

1. **Given** the tier policy table, **When** a floor action type is looked up, **Then** it resolves to `approve` and cannot resolve to `auto` or `notify` — the floor is structural, not a default config value.
2. **Given** a contrived attempt to set a floor action type's tier below `approve` (config or edit path), **When** it is processed, **Then** it is rejected or ignored and the floor action type remains `approve`.
3. **Given** an agent-supplied `tier` field set to `auto` on a floor action, **When** `request_external_action` classifies it, **Then** the field is ignored and the action is `approve`.
4. **Given** a newly-added action type with no explicit classification, **When** it is requested, **Then** it defaults to `approve` (safe-by-construction) — never silently `auto`/`notify`.

---

### User Story 4 - The four tiers behave distinctly (Priority: P2)

The tier policy table classifies action types into four tiers with distinct execution behavior: `auto` (the dispatcher executes it and logs, no operator gate); `notify` (the dispatcher executes it, then surfaces it post-hoc to the operator); `approve` (blocked until the operator's approve click, then dispatched); `human_only` (the dispatcher never executes it — the agent prepares the action, the operator performs it by hand and marks it done). Each tier path is backed by a test proving the gate holds. For M11 alpha the only enumerated concrete action type (GitHub comment-back) is `approve`, but the tier classification and dispatch dispatch-by-tier behavior is exercised so the table is a genuine table, not a GitHub-shaped special case.

**Why this priority**: the four-tier semantics are the committed design (ARCHITECTURE §M11). They are P2 rather than P1 because GitHub comment-back exercises only the `approve` path end-to-end against a real provider; the other three tiers must be proven distinct (the dispatcher acts on `auto`/`notify`, gates on `approve`, never acts on `human_only`) even if their first concrete riding action types arrive as fast-follows.

**Independent Test**: drive each tier through the dispatcher with a representative pending row and confirm: `auto` is executed with a log row and no operator gate; `notify` is executed then surfaced post-hoc; `approve` is blocked until the operator approves; `human_only` is never dispatched and waits for an operator "mark as done." Each path asserts the gate held.

**Acceptance Scenarios**:

1. **Given** an `auto`-tier pending action, **When** the dispatcher runs, **Then** it executes the action and appends an `executed` outcome with no operator approval gate.
2. **Given** a `notify`-tier pending action, **When** the dispatcher runs, **Then** it executes the action, appends an `executed`/`notified` outcome, and the action surfaces post-hoc as an Outbox feed item — not an approval gate.
3. **Given** an `approve`-tier pending action, **When** the dispatcher runs before any operator approval, **Then** the action is **not** executed and remains blocked until the approve Server Action transitions it to `approved`.
4. **Given** a `human_only`-tier pending action, **When** the dispatcher runs, **Then** it **never** executes the action — the prepared payload is recorded and the action waits for the operator's "mark as done" transition.

---

### User Story 5 - The operator rejects, marks-done, and reviews the immutable history (Priority: P2)

For an `approve`-tier action the operator can also Reject (an audited Server Action transitioning the row to `rejected` with an immutable trail), so nothing reaches the world. For a `human_only` action the operator marks it done by hand, recording a completion outcome (with an optional note of what was actually done). Every transition — requested, classified, approved, rejected, executed, failed, notified, done-by-hand — appends to the action's immutable outcome history; the request row's audit anchor is never mutated, the history is appended (the M9 `scheduled_task_runs` shape).

**Why this priority**: the approval queue is only useful if the operator can also say no, and `human_only` is one of the four committed tiers; both must produce the same immutable, reconstructable audit as approve/execute. P2 because the P1 stories establish the spine; this completes the operator surface around it.

**Independent Test**: reject a queued action and confirm it transitions to `rejected` with an immutable trail and never dispatches; mark a `human_only` action done and confirm a completion outcome (with optional note) is appended; reconstruct the history of each and confirm every transition is present and immutable.

**Acceptance Scenarios**:

1. **Given** a `pending`/`approve`-tier row, **When** the operator clicks Reject, **Then** an audited Server Action transitions it to `rejected` with an immutable trail, and the dispatcher never executes it.
2. **Given** a `human_only` pending action, **When** the operator marks it done in the Outbox, **Then** a `done` outcome (with an optional free-text note) is appended to the immutable history and the row reaches a terminal status — the dispatcher having never executed it.
3. **Given** any action, **When** its outcome history is read, **Then** every transition is present as an append-only immutable record anchored to the originating `agent_instance_id`; the request row itself is never mutated in place beyond its status transitions.

---

### Edge Cases

- **Agent-supplied tier or target spoofing**: the tier is always the policy-table lookup on action type, never an agent-supplied field (US1 #4, US3 #3). A malformed/unknown action type defaults to `approve` (US3 #4).
- **Approval race / double-approve**: two approve clicks or an approve + replay must still dispatch exactly once; the dispatcher's terminal-status claim makes the second a no-op (US2 #3).
- **Supervisor restart mid-dispatch**: an action claimed but not yet completed when the supervisor restarts must not double-post on recovery — the claim + terminal transition under `FOR UPDATE SKIP LOCKED` is the guard (US2 #3).
- **Dispatch failure (GitHub 5xx, rate limit, transient network)**: the dispatcher records a `failed` outcome and marks the action `failed` without auto-retry in alpha; the operator sees it in the Outbox and may re-request (Q8 resolution, FR-022/FR-023). It must never silently double-post on a retry.
- **Requesting agent gone by dispatch time**: the agent that requested the action may have exited (Principle III — agents are ephemeral); the pending row carries everything needed to dispatch and audit, so dispatch does not depend on a live agent.
- **Vault unavailable at dispatch time**: if the dispatcher cannot fetch the scoped credential, it records a `failed` outcome and does not execute — it never falls back to an unscoped or agent-visible credential (vault Rule 1 / fail-closed posture).
- **Direct egress attempt**: an agent attempting to reach GitHub directly is denied by the squid allow-list (TCP_DENIED); the only successful external action is one the dispatcher performed after the request transited the tier gate (FR-007, SC-004).
- **`event_outbox` name collision**: the M11 approval-queue surface and table are named unambiguously (`pending_actions` + an `/outbox` or `/admin/outbox` route) so no reader confuses them with M1's transactional `event_outbox` (m11-context.md §Scope-mismatch #2).

## Requirements *(mandatory)*

### Functional Requirements

#### Thread 1 — the sealed `request_external_action` verb

- **FR-001**: The system MUST add `request_external_action` as a new sealed verb in the `Verbs` registry (`supervisor/internal/garrisonmutate/verbs.go`) — the 12th verb — with full sealed-registry discipline: registry entry, per-verb handler, `TestVerbsRegistryMatchesEnumeration` update, and a `chat-threat-model.md` reversibility-class amendment, all in one change (chat-threat-model Rule 1; the M5.3/M8/M9 mechanism).
- **FR-002**: The verb MUST be callable by any active agent instance, anchored on the calling `agent_instances.id` (the M8 `Deps.AgentInstanceID` seam), with defence-in-depth provided by the downstream tier gate and permanent-Approve floor rather than a per-role caller allow-list.
- **FR-003**: The verb MUST NOT perform any external action. Its sole effect MUST be to write exactly one immutable `pending_actions` row in the `work` schema describing the requested action (action type, target, rendered payload, requesting `agent_instance_id`, serving ticket/context), with the tier assigned by the policy table at write time (FR-014) and status `pending`.
- **FR-004**: The verb MUST return a typed `garrisonmutate.Result` indicating the action was queued and at which tier (`auto` / `notify` / `approve` / `human_only`), so the agent's diary records "requested, pending" rather than "done."
- **FR-005**: The verb MUST ignore any agent-supplied tier classification; the tier is always the policy-table lookup on action type (FR-014).
- **FR-006**: The verb MUST join the existing `garrison-mutate` MCP surface as a new verb entry rather than a new MCP server (stay unified).

#### Broker-is-the-only-door invariant

- **FR-007**: The system MUST NOT widen the squid egress allow-list (`supervisor/egress/squid.conf`) to grant agent containers direct egress to any external action provider. Agent containers remain on the `internal: true` network with `api.anthropic.com` as the only permitted egress; the broker is the only door (m11-context.md §Scope-mismatch #1).
- **FR-008**: All external action execution MUST occur supervisor-side, outside the agent network, in the dispatcher (FR-017) — never in an agent container.

#### Thread 2 — the tier policy table and the permanent-Approve floor

- **FR-009 (editability)**: The tier policy table MUST be deploy-time configuration in M11 alpha — no operator surface to raise or lower tiers ships this milestone. The permanent-Approve floor (FR-014) is not lowerable regardless of any future editability surface.
- **FR-010**: The system MUST classify every action type into exactly one of four tiers: `auto` (dispatcher executes + logs, no operator gate), `notify` (dispatcher executes, then operator is told post-hoc), `approve` (blocked until operator approval click), `human_only` (dispatcher never executes; agent prepares, human performs).
- **FR-011**: The tier classification MUST be keyed on action type and live in the tier policy table/structure, not in any agent prompt or the immutable preamble (M2.2.1 mechanism-over-prompt; ARCHITECTURE §M11). It MUST be the single authority for an action's tier.
- **FR-012**: The system MUST record, with each pending action, the reason it is at its tier (e.g. "permanent-Approve floor") sufficient for the Outbox to display it (US1 #3).
- **FR-013**: The system MUST store the pending action with its assigned tier such that the tier is fixed at write time from the policy table and is never altered by agent input.
- **FR-014**: The system MUST encode a permanent-Approve floor: the public-facing action-type categories named in ARCHITECTURE §M11 (posts, replies, releases, mail to non-customers, price/copy changes) MUST be `approve` and MUST NOT be classifiable as `auto` or `notify`. The enforcement shape (protected set, non-overridable column, or CHECK) is a plan decision; the floor itself is binding. `github_issue_comment` MUST be enumerated into the floor.
- **FR-015**: The system MUST default any action type with no explicit classification to `approve` (safe-by-construction); a new action type MUST never be silently `auto`/`notify`.

#### Thread 3 — the dispatcher worker

- **FR-017**: The system MUST run a supervisor-side dispatcher worker (outside the agent network) that executes `auto`, `notify`, and approved `approve`-tier pending actions, and that MUST NOT execute `human_only` actions.
- **FR-018**: The dispatcher MUST be reactive — triggered by (a) a new `auto`/`notify` pending-action row and (b) an operator approval of an `approve`-tier row — via the `pg_notify` bus (channel name follows the dot-delimited convention, e.g. `work.action.dispatch_requested`), with a `processed_at`-style fallback poll for at-least-once delivery (Principle I; the M1/M8/M9/M10 shape). Every dispatch attempt, vault-fetch failure, and rate-cap hit MUST be logged via `slog` structured fields; the durable record is the outcome-history row (Q-B; no new `throttle_events` kind for dispatcher-internal events in M11 alpha).
- **FR-019**: The dispatcher MUST execute the external action using vault-scoped credentials fetched supervisor-side that never appear in any agent container's env, prompt, or context (vault-threat-model Rule 1 extended to outbound action credentials). For M11 alpha the GitHub credential is a PAT (Q-E resolution), fetched from Infisical at a configured vault path (e.g. `actions/GITHUB_PAT`) and sent as `Authorization: token <PAT>` — one vault secret, no token-exchange. GitHub App installation-token auth is the fast-follow.
- **FR-020**: For the GitHub comment-back action type, the dispatcher MUST post the comment to the target issue via the GitHub REST API (`POST /repos/{owner}/{repo}/issues/{issue_number}/comments`). The call MUST be achievable with stdlib `net/http` — no new Go dependency for the GitHub provider (consistent with M10's zero-new-Go-dep posture and the M10 spike F5 finding that PAT auth requires only a bearer-token header).
- **FR-021**: The dispatcher MUST dispatch each approved/auto/notify action **exactly once**, surviving the M1 dedup race and supervisor restart, via `FOR UPDATE SKIP LOCKED` claim plus a terminal status transition on the row (the M9/M10 idempotency discipline). A redelivered or re-claimed action MUST NOT double-execute.
- **FR-022**: On a recoverable external-call failure (5xx, rate limit, transient network), the dispatcher MUST record a `failed` outcome to the immutable history and mark the action `failed` without auto-retry in M11 alpha; the action surfaces in the Outbox for operator-initiated re-request. No automatic retry/backoff loop ships this milestone.
- **FR-023**: If the dispatcher cannot fetch its scoped credential (vault unavailable), it MUST record a `failed` outcome and MUST NOT execute — it MUST NOT fall back to any unscoped or agent-visible credential (fail-closed).
- **FR-024**: Every dispatch attempt MUST append an immutable outcome record (`executed` / `failed` / `notified` / `done` / `skipped_human_only`) to the action's outcome history, anchored to the originating `agent_instance_id`; the request row's audit anchor MUST NOT be mutated — outcomes are appended (the M9 `scheduled_task_runs` → `scheduled_tasks` shape).

#### Thread 4 — the Outbox dashboard surface

- **FR-025**: The system MUST provide an Outbox dashboard surface (named to avoid the `event_outbox` collision — e.g. `/outbox` or `/admin/outbox`) that lists pending `approve`-tier actions with enough detail to decide: action type, target, full rendered payload, the requesting agent + serving ticket, and the tier with the reason it is at that tier. The surface follows the M7 hiring-queue pattern (standard Next.js Server Component, re-fetches on navigation/Server Action completion) — not SSE-live; no new `pg_notify` channel for Outbox live-push ships in M11 alpha (Q-C).
- **FR-026**: The operator's Approve and Reject MUST be dashboard Server Actions anchored and audited like M7's `approve_hire` (Server-Action-only, both chat and agent anchors NULL; the `ServerActionVerbs` precedent). Approve MUST transition the pending row to `approved` (recording the approving operator), which the dispatcher reacts to; Reject MUST transition it to `rejected` with an immutable trail and the dispatcher MUST never execute it.
- **FR-027**: For `human_only` actions, the Outbox MUST display the agent-prepared payload and provide a "mark as done" Server Action that records a `done` completion outcome — with an optional free-text note of what was actually performed — to the immutable history and transitions the row to a terminal status. The dispatcher MUST never execute a `human_only` action.
- **FR-028**: For `notify`-tier actions, the Outbox MUST surface the executed action post-hoc as a feed item (executed, then operator-told) rather than as an approval gate. The broker stays pull-only for M11 alpha — no active push notification ships.

*Note on FR numbering: FR-016 and FR-029 are absent (gaps between FR-015→FR-017 and FR-028→FR-030). These numbers were not allocated; all named FRs are present and the thread structure is complete. The gaps have no functional impact.*

#### Threat-model-first

- **FR-030**: A `docs/security/action-broker-threat-model.md` (a new doc, or an amendment to an existing outbound-surface doc if the operator prefers consolidation) MUST be committed in git history **before** the first dispatcher-code commit, asserted by an acceptance script git-log check (the M9 SC-007 / M10 FR-800 pattern). It MUST cover at minimum: the agent-cannot-act-directly invariant; tier-bypass / tier-downgrade attacks; credential isolation; approval-queue integrity (an attacker cannot auto-approve their own pending action); the permanent-Approve floor as a structural property; blast radius of a misbehaving dispatcher (it cannot self-approve, mutate the tier table, or reach the vault beyond its scoped action credentials); and idempotency of dispatch (exactly-once across restart).

### Key Entities *(include if feature involves data)*

- **Pending action (`work.pending_actions` row)**: an immutable request to act on the world, residing in the `work` schema. Attributes: action type, target (e.g. the GitHub issue identifier/URL), rendered payload, requesting `agent_instance_id`, serving ticket/context, assigned tier (from the policy table), tier reason, status (`pending` → `approved`/`rejected`/`failed`/terminal), and the approving operator (for `approve`-tier). Anchored to `agent_instance_id` per the M8 audit posture. Distinct from M1's `event_outbox` (m11-context.md §Scope-mismatch #2). The `garrison_agent_ro` and `garrison_dashboard_app` Postgres roles cover it via existing `work.*` grants.
- **Action outcome history (`work.pending_action_outcomes` row)**: an append-only immutable record of every transition and dispatch attempt for an action (`executed` / `failed` / `notified` / `done` / `skipped_human_only`), anchored to the originating `agent_instance_id`, in the `work` schema. Mirrors M9's `scheduled_task_runs` appending to `scheduled_tasks` — the request row is not mutated in place beyond status. This table IS the durable observability record; no separate metrics store is introduced.
- **Tier policy classification**: the authoritative mapping from action type to tier (`auto` / `notify` / `approve` / `human_only`), keyed on action type, deploy-time config in alpha, with a permanent-Approve floor that cannot be lowered and an `approve` default for unclassified types. Lives in the table/structure, never in prompts.
- **Action type (M11: `github_issue_comment`)**: the discrete external action the broker can request and dispatch. M11 ships one — GitHub public comment-back — which is on the permanent-Approve floor. Others (mail-send, release-cut, price/copy, social posts) are fast-follow types on the same framework.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001 (request-to-pending lifecycle)**: An agent's `request_external_action` call produces exactly one immutable `pending_actions` row anchored on the calling `agent_instance_id`, with the tier assigned by the policy table (not an agent-supplied field) and a typed queued/at-tier result returned — and nothing has acted on the world. Verified by an integration test.
- **SC-002 (the four tiers behave distinctly)**: Each tier path is backed by a passing test — `auto` executes with a log and no gate; `notify` executes then surfaces post-hoc; `approve` is blocked until the operator approve click, then dispatches; `human_only` is never dispatched and waits for "mark as done." Each test proves its gate held.
- **SC-003 (permanent-Approve floor cannot be lowered)**: A contrived attempt — config or agent-supplied — to classify a floor action type (`github_issue_comment`) as `auto`/`notify` is rejected or ignored and the action lands at `approve` regardless; an unclassified action type defaults to `approve`. Verified by a passing test.
- **SC-004 (the broker is the only door)**: An agent cannot reach the external provider directly — asserted via the egress allow-list (the agent network has no route to GitHub; the squid log shows TCP_DENIED for any direct attempt). The only successful external action is one the dispatcher performed after the request transited the tier gate.
- **SC-005 (credential isolation)**: The dispatcher's vault-scoped action credential never appears in any agent container's env, prompt, or context; the `vaultlog`/leak-scan posture covers the new credential path. Verified by a leak-scan assertion.
- **SC-006 (exactly-once dispatch under restart + race)**: An `approve`-tier action approved once and then redelivered/re-claimed (or surviving a supervisor restart mid-dispatch) executes exactly once — no double-post. Verified with the M8/M9/M10 concurrent-claim chaos shape.
- **SC-007 (immutable, reconstructable audit)**: A randomly-picked dispatched action reconstructs to its originating `agent_instance_id`, the requested payload, the assigned tier, the approving operator (for `approve`-tier), and the dispatch outcome — all immutable.
- **SC-008 (threat model precedes code)**: `action-broker-threat-model.md` is committed in git history before the first dispatcher-code commit, asserted by the acceptance script's git-log check (the M9 SC-007 / M10 pattern).
- **SC-009 (no regression of sealed surfaces)**: finalize schemas, vault rules, agent roles, MemPalace wiring, the container model, MCPJungle naming, and the M10 ingress surface carry byte-for-byte; M11 adds one sealed verb, the tier table, the dispatcher, the Outbox, and a threat model, and changes none of the above. Verified by the registry/enumeration tests holding and the sealed-surface diff being additive only.

## Assumptions

- **The egress allow-list stays Anthropic-only.** M11 does not punch an egress hole for agents; the dispatcher's external reach is supervisor-side. This is the load-bearing precondition, not a configurable option (m11-context.md §Scope-mismatch #1; FR-007).
- **Single action type this ship.** M11 ships the framework + GitHub public comment-back (Q2 lean). The tier table is a genuine table so a second action type rides it without re-architecture; whether one co-ships is an operator call at `/speckit.clarify`.
- **Deploy-time tier config for alpha.** No operator surface to edit tiers ships in M11 (Q5 lean); the permanent-Approve floor is never lowerable regardless.
- **Pull-only for alpha.** The Outbox queue + immutable history is the surface; no active push notification ships (Q7 lean).
- **No auto-retry for alpha.** A failed dispatch is marked `failed` and surfaced for operator re-request; no retry/backoff loop ships, to keep exactly-once unambiguous (Q8 lean).
- **GitHub comment-create spec'd directly.** No milestone-gating spike; the M10 GitHub spike already mapped the auth surface (Q1 lean). A narrow action-type spike inside `/garrison-plan` is the fallback if the comment-create path needs probing.
- **Agent-callers only for M11 alpha.** `request_external_action` is callable by active agents only (Q-D resolution). The chat-CEO caller path is a follow-up addition; it is not in scope for M11 alpha. FR-002 is authoritative.
- **GitHub PAT credential for alpha.** The dispatcher's GitHub credential is a Personal Access Token stored in Infisical, fetched at dispatch time via the standard vault path (Q-E resolution; M10 spike F5 lean: App mode is follow-up). No RS256 JWT, no installation-token refresh loop, no new Go dependency. GitHub App installation token (short-lived, revocable, multi-org) is the production-hardening fast-follow.
- **Existing substrate is reused, not reinvented.** The sealed-verb registry (M5.3), the `agent_instance_id` anchor and `Deps.AgentInstanceID` seam (M8), the immutable-history table shape (M9 `scheduled_task_runs`), the `ServerActionVerbs` approve-path precedent (M9), the `FOR UPDATE SKIP LOCKED` exactly-once discipline (M1/M9/M10), the supervisor-side-worker-outside-the-agent-network shape (M10), and the vault scoped-credential discipline (M2.3) are composed at the outbound boundary; M11 invents no new mechanism beyond the one genuinely new thing — emitting attacker-influenceable actions to the outside world, which the threat-model-first discipline and the permanent-Approve floor exist to handle.
- **Migration version hygiene.** M11's goose migration must advance past M10's `20260612000000` to avoid the same-day version collision noted in M9 gotcha 5 / the M7.1 retro — checked at plan time.
- **Tests are Go-side only** per the standing repo convention; the dashboard Server Actions and Outbox view are covered by the Go integration tests' row shapes, not by frontend tests.

## Out of scope

These are non-goals or deferrals, restated from m11-context.md §Out of scope so the spec does not drift:

1. **Giving agents direct egress.** M11 does not widen the squid allow-list; the dispatcher is supervisor-side. The single most important boundary in this milestone (FR-007).
2. **Agent web access (research / fetch / browse).** General outbound web egress for agents through the sortie sidecar is M12 — a separate audited gateway. M11 is about discrete external actions, not general web egress.
3. **The full action-provider catalog this ship.** Mail-send, release-cut, price/copy changes, social posts are fast-follow action types on the same framework; their integrations and tier assignments land in their own follow-ups.
4. **Reclassifying the permanent-Approve floor.** Public-facing action types are Approve-tier permanently; M11 ships no mechanism to lower them.
5. **A workflow / approval-chain engine.** One pending action, one approval gate — no multi-approver chains, conditional approvals, or action DAGs.
6. **Multi-project / per-project tier policy.** Whether the tier table gains a `project_id` dimension is parked for M13; M11 ships single-project, keyed on action type only.
7. **Mutating sealed surfaces beyond the named additions.** finalize schemas, vault rules, agent roles, MemPalace wiring, the container model, MCPJungle naming, and the M10 ingress surface carry unchanged; M11 adds one sealed verb, the tier table, the dispatcher, the Outbox, and a threat model.
