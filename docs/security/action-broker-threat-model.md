# Action broker surface — threat model and architectural rules

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

**Status**: Threat model and architectural rules. Committed before any dispatcher
code in `internal/actionbroker/` per FR-030 and the M2.3 / M5.3 / M7 / M10
threat-model-first precedent. M11 (Action Broker) is the active implementation
milestone; the binding spec is `specs/023-m11-action-broker/spec.md` and the
milestone context is `specs/_context/m11-context.md`.

**Last updated**: 2026-06-12 (initial, M11 amendment).

**Precedence**: this document lives below `RATIONALE.md` and the active milestone
context in the document hierarchy (see `AGENTS.md`). The active milestone context
supersedes this document for operational conflicts; this document supplies the
threat model and architectural principles that context files cannot re-derive
cheaply. The vault threat model (`docs/security/vault-threat-model.md`) covers
vault assets; the chat mutation surface threat model
(`docs/security/chat-threat-model.md`) covers the chat-driven mutation surface
(including the `request_external_action` verb's reversibility classification —
§5 in that document); the agent sandbox threat model
(`docs/security/agent-sandbox-threat-model.md`) covers the per-agent container
surface. All four are binding for M11 since all four surfaces remain live.

---

## Scope of this document

This is a threat model and a set of architectural rules for the action broker
surface — the supervisor-side machinery that receives agent requests to act on
the outside world, classifies them by policy tier, gates `approve`-tier requests
behind operator approval, and executes `auto`/`notify`/approved-`approve`
requests with vault-scoped credentials that agent containers never see. It is NOT
a spec, a plan, or an implementation.

The document covers:

1. What the action broker surface protects (assets)
2. Who it protects against (adversaries)
3. The seven FR-030-required threat areas with their controls
4. Architectural rules Garrison enforces in the action broker
5. Per-attack-class mitigation summary
6. Open questions later milestone specs must resolve
7. What the M11 retro must answer

---

## Milestone banding

**M11** — initial amendment. Establishes the action broker outbound surface, the
seven threat-area treatments below, and the five architectural rules. This
document is committed before any code in `internal/actionbroker/` per FR-030 and
the M9 SC-007 / M10 FR-800 git-log discipline (asserted by the M11 acceptance
script's git-log check in `scripts/m11-acceptance.sh`).

Future milestones extending the action broker surface — adding action providers
(mail-send, release-cut, social posts), adding operator-editable tier policies,
extending the permanent-Approve floor — must update this document **before** any
code lands for that surface. The architectural rules section below is the binding
constraint set; new milestones either honor it or amend it explicitly here.

---

## 1. Assets

**State the action broker surface can affect**:

- **Outbound external actions**: posts to external services (GitHub issues,
  future: email, social, release APIs) dispatched by the supervisor-side
  dispatcher after policy classification and optional operator approval. An
  executed action creates state in external systems that may not be reversible
  (a posted GitHub comment cannot be unposted without a follow-up API call).
- **Pending-action state** (`work.pending_actions` rows): immutable request rows
  anchored to the requesting `agent_instance_id`, carrying the action type,
  target, rendered payload, tier, tier reason, and status. Status transitions
  in-place (`pending` → `approved` / `rejected` / `executed` / `failed` /
  `done`); the audit anchor and request fields are never rewritten.
- **Action outcome history** (`work.pending_action_outcomes` rows): append-only
  immutable records of every transition and dispatch attempt, anchored to the
  originating `agent_instance_id`. Mirrors M9's `scheduled_task_runs` shape.
- **Vault-scoped action credentials** (`actions/GITHUB_PAT` and future analogues):
  credentials the dispatcher fetches supervisor-side to execute approved actions.
  These credentials are never visible in any agent container's environment,
  prompt, or context — M2.3 Rule 1 extended to outbound action credentials.
- **Chat mutation audit rows** (`chat_mutation_audit`): audit rows for
  `request_external_action`, `approve_action`, `reject_action`, and
  `mark_action_done` verbs, recording the full args and outcome.

**State the action broker surface explicitly CANNOT affect**:

- **Vault state**: the dispatcher holds only a `VaultFetcher` interface that
  fetches the single configured action-credential path. It cannot enumerate vault
  paths, write vault entries, rotate credentials, or reach any path outside
  `actions/GITHUB_PAT` (or the configured `GARRISON_ACTION_GITHUB_PAT_PATH`).
- **Agent state**: the dispatcher cannot spawn agents, pause or resume agents,
  or mutate `agents` rows. Agent spawning remains the M1 dispatcher's
  responsibility.
- **Tier policy**: the tier classification is a compile-time constant (`policy.go`
  `policy` and `floor` maps). The dispatcher reads it; it cannot mutate it.
  The permanent-Approve floor is baked into the `Classify` function — no runtime
  code path lowers it.
- **Auth state**: the action broker touches no better-auth session tables, user
  records, or invite flows.
- **MemPalace state**: the dispatcher writes no palace entries.
- **Squid egress allow-list**: the dispatcher is supervisor-side (outside the
  agent network). The `supervisor/egress/squid.conf` allow-list (`api.anthropic.com`
  only) is not amended by M11; the dispatcher's HTTP client is constructed in
  `main.go` and makes outbound calls supervisor-side, not through the squid proxy
  (FR-007, D20).

**Multi-tenancy posture**: single-tenant single-operator at M11 ship. The action
broker assumes one operator with full authority over approve / reject / done
Server Actions. Multi-operator approval chains require a threat-model re-amendment
before code lands.

---

## 2. Adversaries

Ranked by realistic probability of affecting the deployed Garrison instance
operationally.

1. **An agent under prompt injection that calls `request_external_action` with
   attacker-influenced args.** A prompt-injection payload in a ticket body
   (written by an ingress connector — M10) or a palace entry instructs the agent
   to call `request_external_action` with an attacker-chosen action type, target,
   or payload. The verb fires; a `pending_actions` row lands. This is the top
   adversary because it is structurally possible (the verb is callable by any
   active agent), and it is exactly the threat the tier gate and permanent-Approve
   floor exist to bound.

2. **An agent that attempts to self-classify a public-facing action as `auto` or
   `notify`** by supplying a `tier` field in its request args (attempting a
   tier-downgrade). The verb's struct has no `tier` field; agent-supplied tier
   JSON keys are silently dropped on unmarshal. The policy table is the single
   classification authority.

3. **An operator who clicks Approve on a maliciously-crafted pending action**
   without reading the rendered payload. An injection-driven request with a
   plausible-looking payload that actually contains adversarial content (e.g. a
   GitHub comment that posts spam under the operator's repo). The operator's
   responsibility to read the payload before approving is the primary mitigation;
   the Outbox surface is designed to make the rendered payload prominent.

4. **A double-approval race** — the operator approves the same action twice in
   quick succession, or a `pg_notify` redelivery causes the dispatcher to claim
   an already-dispatched row. The `FOR UPDATE SKIP LOCKED` claim + terminal status
   transition is the structural guard.

5. **A supervisor restart mid-dispatch** — the dispatcher claims a row, posts the
   external action, but crashes before committing the terminal status. On recovery,
   the dispatcher re-claims the still-`approved` row and attempts to post again.
   GitHub comment-create is not natively idempotent; the at-most-once-extra window
   is the accepted risk for this failure mode (D12 / FR-022). This is why
   auto-retry is forbidden.

**Adversaries we explicitly deprioritize**:

- **Host-level attackers with shell access.** Application-layer design does not
  defend against a rooted host. Systems-level mitigation belongs to a different
  document.
- **Nation-state-level adversaries.** Wrong threat model for an indie self-hosted
  deployment.
- **Malicious operators.** The operator IS the trust root in single-operator
  Garrison.

---

## 3. Seven threat areas required by FR-030

### 3.1 Agent-cannot-act-directly invariant (FR-007, m11-context §Scope-mismatch #1)

**Threat**: an agent, whether behaving normally or under prompt injection, attempts
to reach an external service directly (e.g. `curl https://api.github.com/...`
from inside its container).

**Structural enforcement**: agent containers join the `internal: true`
`garrison-agents` Docker network. The only egress from that network is a squid
sidecar whose committed allow-list contains **exactly `api.anthropic.com`** and
denies all else (`supervisor/egress/squid.conf`). A direct GitHub API call from
an agent container receives `TCP_DENIED` from squid and never reaches GitHub.

**The broker is the only door, not a convention.** An agent cannot route around
the broker by calling GitHub directly — it physically cannot reach GitHub. The
only successful external action is one the **supervisor-side dispatcher** performed
after the request transited the tier gate. This is the load-bearing precondition
for the entire milestone's security posture.

M11 does **not** widen the squid allow-list. The dispatcher's HTTP client is
constructed in `cmd/supervisor/main.go` (supervisor-side, outside the agent
network) and is never threaded into any agent container environment. The
`supervisor/egress/squid.conf` file is byte-for-byte unchanged by M11 (asserted
by the M11 acceptance script's `git diff --name-only` check — SC-004).

**Consequence**: any future milestone that wants to give agents direct external
reach (research / fetch / browse — M12's sortie gateway) must update this document
and the squid configuration with an explicit threat-model review. The default is
rejection.

### 3.2 Tier-bypass and tier-downgrade attacks (FR-005, FR-011, FR-013, FR-014)

**Threat**: a compromised or injected agent attempts to classify a public-facing
action (GitHub comment-back) at a lower tier (`auto` or `notify`) so it executes
without operator approval. Attack vectors: (a) supplying a `tier` field in the
request args, (b) exploiting a policy-table misconfiguration, (c) supplying an
action type that maps to a lower tier via the policy map.

**Three-layer structural enforcement** (D5 — all three layers must be in place):

**(a) `Classify` floor-first**: `internal/actionbroker/policy.go`'s `Classify`
function consults the `floor` map **before** the `policy` map. If the action type
is in `floor`, `Classify` returns `(TierApprove, "permanent-Approve floor
(public-facing)")` regardless of any `policy` entry. Even a contrived
`policy["github_issue_comment"] = TierAuto` assignment is overridden by the floor
check. This is tested by `TestFloorCannotBeLowered`.

**(b) No agent-supplied tier**: `RequestExternalActionArgs` has no `tier` field.
An agent-supplied `"tier"` key in the JSON args is silently dropped on
`json.Unmarshal`. The tier is always the `Classify(args.ActionType)` result —
the policy table is the single authority (FR-011).

**(c) DB CHECK as backend**: the `pending_actions` table carries
`CONSTRAINT pending_actions_floor_is_approve CHECK (action_type NOT IN
('github_issue_comment') OR tier = 'approve')`. This is the defense-in-depth
backstop: even a hand-edited `INSERT` or a future code path that bypasses
`Classify` cannot store a `github_issue_comment` row at `auto`/`notify`. This
is tested by `TestFloorEnforcedAtDB`.

**No operator surface to lower tiers in M11 alpha** (FR-009). The tier policy
is deploy-time config (compile-time constants). Whether a future milestone adds
an operator tier-editor, the permanent-Approve floor entries are never lowerable
regardless. `TestFloorCheckMatchesPolicy` asserts the floor set in the Go code
and the floor set in the DB CHECK are identical — no drift between layers.

**Consequence**: an unclassified action type (not in `floor` or `policy`) defaults
to `approve` — the safe-by-construction default (FR-015). A new action type is
never silently `auto`/`notify`.

### 3.3 Credential isolation — PAT never in agent context (FR-019, FR-023, M2.3 Rule 1 extended)

**Threat**: the GitHub PAT (or any future action-provider credential) appears in
an agent container's environment variable set, system prompt, context window,
MemPalace diary entry, or any agent-readable location.

M2.3 Rule 1 (credentials never in agent context) is extended to outbound action
credentials:

**Vault-stored, never in agent env**: the GitHub PAT lives at a configured vault
path (default `actions/GITHUB_PAT`). The dispatcher fetches it supervisor-side
at dispatch time via `deps.Vault.Fetch(ctx, ...)`. The PAT is never set as an
environment variable on any agent container, never placed in any `--system-prompt`
or `-p` argument, and never written to any agent-readable log, palette, or diary
entry.

**Fail-closed on vault unavailability**: if the vault is unreachable at dispatch
time, the dispatcher writes a `failed` outcome and marks the row `failed` — it
does **not** fall back to an unscoped credential, an environment-visible credential,
or any degraded posture (FR-023). A pending action stays pending until the vault
is available and the operator re-requests. This is tested by
`TestHandleVaultUnavailableFailsClosed`.

**SecretValue.Zero() after use**: the dispatcher calls `vault.SecretValue.Zero()`
on the fetched PAT after the HTTP header is constructed. The `tools/vaultlog`
go-vet analyzer enforces at build time that `vault.SecretValue` values are never
passed to `slog`/`fmt`/`log` calls. The PAT string (extracted from the
`SecretValue` for the HTTP Authorization header) is never passed to any logger.
This is tested by `TestPostCommentNeverLogsPAT`.

**No PAT in dispatcher struct or agent container config**: the PAT path is
configured on the `Deps` struct as a string (`PATPath`); the PAT value is fetched
per-dispatch and discarded after use. The `Deps` struct holds no live credential
between dispatches.

**Consequence**: a successful prompt injection into an agent container cannot
exfiltrate the GitHub PAT — the PAT is not in the agent's environment, context,
or any MCP-reachable surface. The only code that holds the PAT value is the
dispatcher goroutine between `Vault.Fetch` and the HTTP call (bounded by the
dispatch transaction).

### 3.4 Approval-queue integrity — operator cannot be bypassed (FR-026, US2 #1)

**Threat**: an agent or an external attacker auto-approves a pending action,
bypassing the operator's approval gate.

**Approve / reject / done are dashboard Server Actions only**: the `approveAction`,
`rejectAction`, and `markActionDone` Server Actions in
`dashboard/app/[locale]/(app)/admin/outbox/actions.ts` are the only code paths
that write a `status='approved'` transition to `pending_actions`. They are:

- **Not MCP-reachable**: `approve_action`, `reject_action`, and `mark_action_done`
  are in the `ServerActionVerbs` slice (`supervisor/internal/garrisonmutate/
  server_action_verbs.go`), not the `Verbs` slice. `TestVerbsSlicesDisjoint`
  asserts the two slices share no entry. A chat or agent call to any of these
  names returns JSON-RPC error -32601.
- **Not agent-container-reachable**: the Server Actions execute in the Next.js
  server runtime. They are not exposed via the `garrison-mutate` MCP server and
  have no socket, port, or HTTP path reachable from an agent container.
- **Anchored like M7's `approve_hire`**: both chat-anchors (`chat_session_id`,
  `chat_message_id`) are NULL; `affected_resource_type='pending_action'`. The
  approve Server Action records the `approved_by` operator identity on the
  `pending_actions` row (FR-026).

**Dispatcher only dispatches `approved` rows for `approve`-tier**: the dispatcher's
claim predicate (`ClaimDispatchablePendingAction`) requires the row's status to
already be `approved` for `approve`-tier rows. A `pending` approve-tier row is
never dispatched (tested by `TestHandleNeverExecutesPendingApprove`). The
dispatcher does not and cannot set `status='approved'` itself.

**Consequence**: an agent that calls `request_external_action` for a
`github_issue_comment` receives a typed result saying the action is queued at the
`approve` tier; nothing reaches GitHub until the operator explicitly clicks
Approve in the Outbox. The agent cannot shorten or skip this path.

### 3.5 Permanent-Approve floor as a structural property, not current config (FR-014, SC-003)

**Threat**: a misconfigured deploy-time tier entry, an injected `policy` map
write, or a future operator-tier-editor lowers `github_issue_comment` (or any
other public-facing action type) to `auto` or `notify`, causing public-facing
actions to execute without operator review.

**Reputation is the existential risk.** ARCHITECTURE §M11 is categorical: "one
spammy autopost costs more than a year of approval clicks saves." The floor is
the structural answer: public-facing action types are `approve` by construction,
not by current configuration.

The floor is not a default value that can be overwritten — it is the first check
in `Classify` (§3.2 layer (a)), backed by the DB CHECK (§3.2 layer (c)). Even
if someone edits the `policy` map to include `"github_issue_comment": TierAuto`:

1. `Classify` checks `floor` first and returns `TierApprove` regardless.
2. A direct DB INSERT with `tier='auto'` violates `CONSTRAINT pending_actions_floor_is_approve`.
3. `TestFloorCannotBeLowered` catches any code change that breaks layer (a).
4. `TestFloorEnforcedAtDB` catches any migration change that removes layer (c).

**`TestFloorCheckMatchesPolicy`** asserts that the action-type set in the Go
`floor` map equals the action-type set in the DB CHECK — no drift between the
two enforcement layers is possible without a failing test.

**Future action types**: a newly-added action type with no explicit classification
defaults to `approve` (§3.2, FR-015). A new public-facing action type added to the
`floor` map automatically gains the DB CHECK enforcement via the test-enforced
parity between the two sets.

### 3.6 Blast radius of a misbehaving dispatcher (FR-017, FR-021)

**Threat**: the dispatcher goroutine is compromised, buggy, or injected such that
it attempts to self-approve, escalate credentials, mutate the tier policy, or
dispatch actions beyond its authorized scope.

**What the dispatcher CAN do**:

- Claim and execute `pending_actions` rows whose `status IN ('pending','approved')`
  and `tier <> 'human_only'` (via `ClaimDispatchablePendingAction`).
- Fetch the configured vault path (`deps.PATPath`, default `actions/GITHUB_PAT`)
  via `deps.Vault.Fetch`.
- Construct and send one HTTPS POST to `api.github.com` per dispatch attempt.
- Write `pending_action_outcomes` outcome rows.
- Update `pending_actions.status` and `pending_actions.dispatched_at`.
- Log structured fields via `slog` (never secret values — vaultlog discipline).

**What the dispatcher CANNOT do** (the blast-radius boundary):

- **Cannot self-approve**: the dispatcher reads `status` from the claimed row;
  it does not write `status='approved'`. The only code path that writes `approved`
  is the dashboard Server Action (§3.4). No SQL query in `m11_action_broker.sql`
  contains an `UPDATE … SET status='approved'` statement.
- **Cannot mutate the tier policy**: the `policy` and `floor` maps are
  compile-time constants in `internal/actionbroker/policy.go`. There is no
  runtime API, no IPC interface, and no database table the dispatcher can write
  to that affects tier classification.
- **Cannot reach vault paths beyond its configured path**: the `VaultFetcher`
  interface receives a `[]vault.GrantRow` argument specifying exactly one path.
  The dispatcher constructs this argument with `deps.PATPath` — a string set in
  `main.go` from the `GARRISON_ACTION_GITHUB_PAT_PATH` config. No other vault
  path is constructed or passed.
- **Cannot grant new permissions**: the dispatcher holds no Infisical admin
  credentials. It cannot create new vault grants, new machine identities, or
  new secret paths.
- **Cannot escalate to agent containers**: the dispatcher goroutine runs in the
  supervisor process. It has no MCP server socket, no Docker API client, and no
  path to any agent container's environment.
- **Cannot dispatch `human_only` rows**: `ClaimDispatchablePendingAction` filters
  `tier <> 'human_only'`; the defence-in-depth code path `if tier == human_only →
  skipped_human_only` is present in `Handle` for robustness (tested by
  `TestHandleNeverExecutesHumanOnly`).
- **Cannot reach the dashboard API**: the dispatcher is a goroutine in the
  supervisor process. It shares the supervisor's Postgres pool but has no HTTP
  client pointed at port 8081.

**Consequence**: a fully compromised dispatcher can execute approved, auto, and
notify actions (with vault-scoped credentials) and write outcome rows. At worst,
it double-posts an approved action (the at-most-once-extra window — §3.7) or
falsely marks actions as executed. It cannot change governance structures,
escalate credentials, or act on unapproved requests.

### 3.7 Idempotency of dispatch — exactly-once across restart and race (FR-021, SC-006)

**Threat**: a supervisor restart between the dispatch POST and the commit of the
terminal status, or a concurrent `pg_notify` redelivery, causes the same action
to be dispatched twice. For GitHub comment-create (not natively idempotent), this
means two comments posted.

**Garrison-side exactly-once enforcement** is the structural answer, because no
provider-side idempotency key exists for GitHub comment-create. Two mechanisms
compose:

**(a) `FOR UPDATE SKIP LOCKED` claim**: `ClaimDispatchablePendingAction` runs
inside a transaction with `SELECT … FOR UPDATE SKIP LOCKED`. The row-level lock
prevents two concurrent dispatcher instances from claiming the same row
simultaneously. The second claimer skips the locked row and finds nothing to
dispatch (the claim returns zero rows). This guards the **concurrent race** —
multiple dispatcher goroutines or a fast `pg_notify` redelivery. Tested by
`TestConcurrentClaimDispatchesExactlyOnce`.

**(b) Terminal status transition**: `MarkPendingActionExecuted` (or
`MarkPendingActionFailed`) transitions the row to a terminal status before the
transaction commits. A re-claim of a row already in a terminal status
(`executed`/`failed`/`done`/`rejected`) is a no-op — `ClaimDispatchablePendingAction`
filters `status IN ('pending','approved')`. This guards the **restart race** — a
supervisor restart after the POST but before the commit leaves the row in
`approved`; the next dispatcher run re-claims and re-posts (the at-most-once-extra
window). Tested by `TestRestartMidDispatchNoDoublePost`.

**The at-most-once-extra window** (accepted risk, D12): if the dispatcher posts
the GitHub comment but the supervisor crashes before committing `executed`, the
row remains `approved` and the next dispatcher run posts again. This is a
one-comment duplication in the worst case. It is the accepted cost of the
fail-closed posture and the prohibition on auto-retry. Auto-retry with a committed
`executed` status is safe (the second claim is a no-op); auto-retry without a
committed status risks double-post. M11 accepts the window; a future milestone
may add a provider-side idempotency mechanism (e.g. GitHub App comment
dedupe-by-content) as defense-in-depth.

**No auto-retry** (FR-022 / D12): a recoverable failure (`5xx`, `429`, transient
network) marks the row `failed` and surfaces it in the Outbox for operator
re-request. Auto-retry would risk double-post in the at-most-once-extra window.

---

## 4. Architectural rules (binding for M11 and beyond)

These rules are binding. Any milestone that touches the action broker surface
honors these rules or amends them here before the spec changes.

### Rule 1: The broker is the only door — squid allow-list is not amended

Agent containers remain on the `internal: true` `garrison-agents` network with
`api.anthropic.com` as the only permitted egress. `supervisor/egress/squid.conf`
is not modified by M11 or any future milestone adding action providers. The
dispatcher is supervisor-side (outside the agent network) and makes external
calls using its own HTTP client constructed in `main.go` — not through the squid
proxy.

**Consequence**: a future action provider integration that wants agent-side egress
requires an explicit amendment to this rule AND to the squid configuration, with a
threat-model review. The default is rejection. The M12 sortie gateway (agent web
access) requires such an amendment; it does not change M11's posture.

**Consequence**: the M11 acceptance script asserts `supervisor/egress/squid.conf`
is byte-for-byte unchanged from M10 via `git diff --name-only HEAD~N --
supervisor/egress/squid.conf` returning empty (SC-004).

### Rule 2: Vault-scoped action credentials are never in agent context

M2.3 Rule 1 (credentials never in agent context) applies to outbound action
credentials. The GitHub PAT and any future action-provider credential lives in
Infisical, fetched supervisor-side at dispatch time, and is never set as an agent
environment variable, placed in a system prompt, or written to any
agent-accessible location.

**Consequence**: a future action provider integration that stores its credential
in an agent's environment variable, or passes it as a `--system-prompt` argument,
violates this rule and requires a threat-model amendment. The M2.3 vault
discipline applies in full to outbound credentials.

**Consequence**: the `tools/vaultlog` go-vet analyzer enforces at build time that
`vault.SecretValue` values are never passed to `slog`/`fmt`/`log` calls. The PAT
string (extracted via `UnsafeBytes()` for the HTTP header) is not a `SecretValue`
at the point it is used; the vaultlog discipline is reinforced by
`TestPostCommentNeverLogsPAT` in the test suite.

### Rule 3: The permanent-Approve floor cannot be lowered

Public-facing action types (`github_issue_comment` and any future type added to
the `floor` map) are `approve`-tier permanently. No operator surface, no deploy-time
config, no runtime mutation of the `policy` or `floor` maps lowers them.

**Consequence**: adding a new action type to `policy` with a tier below `approve`
for a floor type is a code change that breaks `TestFloorCannotBeLowered`. The test
is the compile-time guard.

**Consequence**: removing the `CONSTRAINT pending_actions_floor_is_approve` CHECK
from the migration is a DB migration that breaks `TestFloorEnforcedAtDB`. The test
is the DB guard.

**Consequence**: the floor set in Go code and the floor set in the DB CHECK must
match exactly. `TestFloorCheckMatchesPolicy` enforces this. Adding a new
public-facing action type requires: (1) adding it to the `floor` map in
`policy.go`; (2) extending the `NOT IN (...)` list in the DB CHECK (new migration);
(3) verifying `TestFloorCheckMatchesPolicy` still passes.

### Rule 4: Approve / reject / done transitions are Server-Action-only

The `approved`, `rejected`, and `done` status transitions on `pending_actions`
rows are written only by the dashboard Server Actions (`approveAction`,
`rejectAction`, `markActionDone`). These Server Actions are in the `ServerActionVerbs`
registry (`server_action_verbs.go`), not the `Verbs` registry. `TestVerbsSlicesDisjoint`
enforces disjointness. No MCP-callable path writes these transitions.

**Consequence**: a future milestone that wants agents or the chat-CEO to approve
their own pending actions requires an explicit amendment to this rule and a
threat-model review. The autonomous-approval scenario (same entity on both sides
of the gate) is the attack the gate exists to prevent.

**Consequence**: a future multi-operator scenario (one operator queues, another
approves) is additive — it does not require amending this rule, only adding
operator-identity tracking to the Server Actions.

### Rule 5: No auto-retry; every dispatch attempt is exactly one POST attempt per row

The dispatcher does not retry recoverable failures. A `5xx`, `429`, or transient
network error marks the row `failed` and surfaces it in the Outbox for operator
re-request. This is the safe posture given GitHub comment-create is not natively
idempotent and the at-most-once-extra window already exists for crash recovery.

**Consequence**: a future milestone that adds retry backoff for action providers
with native idempotency keys (e.g. a provider that accepts an idempotency header)
requires an amendment to this rule, evidence that the provider honors the key,
and a test that demonstrates the retry does not double-post on a key collision.

---

## 5. Per-attack-class mitigation summary

| Attack class | Description | Controls (rule / section references) |
|---|---|---|
| **AC-1: Agent direct-egress attempt** | Agent container tries to reach GitHub directly (e.g. `curl https://api.github.com/...`). | §3.1, Rule 1: squid allow-list `TCP_DENIED`; no route from agent network to GitHub. The broker is the only door structurally. |
| **AC-2: Tier-downgrade via agent-supplied `tier` field** | Agent supplies `"tier":"auto"` in request args attempting to skip the approve gate. | §3.2: `RequestExternalActionArgs` has no `tier` field; agent-supplied JSON key is silently dropped on unmarshal. Policy table is the single authority. |
| **AC-3: Floor bypass via policy map manipulation** | Contrived `policy["github_issue_comment"] = TierAuto` to bypass the permanent-Approve floor. | §3.2, Rule 3: `Classify` checks `floor` before `policy`; floor wins. `TestFloorCannotBeLowered` guards the code path. DB CHECK guards the insert path. |
| **AC-4: Approval bypass — agent auto-approves** | Agent or injected code calls `approve_action` verb to auto-approve its own pending action. | §3.4, Rule 4: `approve_action` is in `ServerActionVerbs` (not `Verbs`); JSON-RPC -32601 on any agent/chat call. Only dashboard Server Action writes `approved`. |
| **AC-5: Credential theft — PAT in agent context** | Injected or misbehaving agent reads the GitHub PAT from env, prompt, or MCP surface. | §3.3, Rule 2: PAT is vault-stored, fetched supervisor-side only, never in agent env or context. `TestPostCommentNeverLogsPAT` pins the no-log invariant. Fail-closed if vault unavailable. |
| **AC-6: Double-dispatch race** | Two concurrent dispatcher goroutines or `pg_notify` redelivery causes two POSTs for the same approved action. | §3.7: `FOR UPDATE SKIP LOCKED` claim — second claimer skips locked row. `TestConcurrentClaimDispatchesExactlyOnce` pins the exactly-once contract. |
| **AC-7: Double-dispatch on restart** | Dispatcher posts the action but crashes before committing terminal status; recovery re-claims and re-posts. | §3.7: accepted at-most-once-extra window. Terminal status makes re-claim a no-op for committed executions. No auto-retry (Rule 5). `TestRestartMidDispatchNoDoublePost` documents the contract. |
| **AC-8: Vault-unavailable fallback** | Vault unavailable at dispatch time; dispatcher falls back to an agent-visible or unscoped credential. | §3.3, Rule 2: fail-closed — `failed` outcome, no execution, no fallback. `TestHandleVaultUnavailableFailsClosed` pins the closed posture. |
| **AC-9: Dispatch of human_only row** | Dispatcher claims and executes a `human_only` row, acting on the world without human completion. | §3.6: `ClaimDispatchablePendingAction` filters `tier <> 'human_only'`; defence-in-depth `skipped_human_only` path in `Handle`. `TestHandleNeverExecutesHumanOnly` pins the guard. |

---

## 6. Open questions later milestone specs must resolve

1. **Provider-side idempotency for non-GitHub providers.** Some future action
   providers (transactional email APIs, Stripe, etc.) offer idempotency keys. A
   future milestone that adds such a provider may amend Rule 5 to permit a
   controlled retry that passes the idempotency key, with evidence that the provider
   honors it and a test that prevents double-send on key collision.

2. **Multi-operator approval.** M11 alpha is single-operator. Multi-operator
   (different operator queues, different operator approves) requires amending §1's
   multi-tenancy posture note and Rule 4's approval-path definition. The current
   `approved_by` column already records the approver identity; the structural change
   is the authorization check.

3. **Tier-policy editability via operator surface.** FR-009 defers tier editing to
   a future milestone (alpha is deploy-time config). When that surface lands, this
   document must enumerate: which tiers are editable (never the permanent-Approve
   floor), what the edit UI records in the audit trail, and whether a tier raise
   (auto → approve) is operator-only or requires a separate approval step.

4. **Concurrent outbound providers.** M11 ships one action provider (GitHub
   comment-create). When a second provider lands (mail-send, etc.), the dispatcher's
   `GitHubPoster` interface becomes a `ProviderDispatcher` or similar; the vault
   path convention (`actions/GITHUB_PAT` → `actions/<provider>_<credential>`)
   must be extended and documented here.

5. **Provenance of `pending_actions` rows in the Outbox.** M11 alpha shows the
   requesting `agent_instance_id` and the serving `ticket_id`. A future milestone
   may add the originating M10 ingress delivery ID (the external GitHub issue) to
   close the provenance chain end-to-end (inbound event → ticket → agent → outbound
   action → GitHub comment). This is observability, not a security change, but it
   is flagged here for completeness.

---

## 7. What the M11 retro must answer

When M11 ships, the retro (`docs/retros/m11.md`) documents:

1. **Did the broker hold as the only door?** Did anything reach the outside world
   *not* through the dispatcher? Did the SC-004 acceptance step (`git grep` +
   `git diff` assertions on `squid.conf`) return clean? Did any agent container
   env, prompt, or context carry the GitHub PAT (SC-005 + `TestPostCommentNeverLogsPAT`)?

2. **Did the permanent-Approve floor resist a lower-the-tier attempt?** Did
   `TestFloorCannotBeLowered` pass cleanly? Did `TestFloorEnforcedAtDB` fire
   the DB CHECK as expected? Did `TestFloorCheckMatchesPolicy` confirm no drift
   between Go code and DB?

3. **Did dispatch stay exactly-once under restart and race?** Did
   `TestConcurrentClaimDispatchesExactlyOnce` pass cleanly (N goroutines, one
   `PostComment` call, one `executed` outcome)? Did `TestRestartMidDispatchNoDoublePost`
   document the at-most-once-extra window correctly?

4. **Did the threat-model precede the first dispatcher code in git history?**
   Did the SC-008 acceptance step (`git log --oneline -- docs/security/
   action-broker-threat-model.md supervisor/internal/actionbroker/`) confirm
   the threat-model commit preceded any `internal/actionbroker/` file?

5. **Were any architectural rules violated in implementation?** (M9/M10 pattern:
   post-ship adversarial review sometimes surfaces issues the acceptance script
   missed. If any are found, they are patched before the retro task is checked
   off and documented here with the remediation.)

6. **Did the GitHub comment-create contract match the plan's Phase 0 assumptions?**
   Were there surprises in the PAT auth model, the `201`/`4xx`/`5xx` shape, or
   the idempotency contract? If so, what was the at-most-once-extra window's
   actual footprint in testing?
