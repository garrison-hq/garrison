# M11 — Action Broker (outbound external actions, gated by policy) (context)

**Status**: context for `/speckit.specify`. M10 (ingress connectors) shipped
2026-06-12 (branch `022-m10-ingress-connectors`, merged at PR #25; retro
[`docs/retros/m10.md`](../../docs/retros/m10.md)). Branch
`023-m11-action-broker` is already cut from `main` at the M10 merge and is the
working branch. Written 2026-06-12.

**Produced non-interactively** — the operator was absent for this phase. The
scope decisions a live session would have surfaced in conversation are resolved
below from the committed docs, the operator's milestone brief, and the prior
milestone contexts, with each resolution named so the operator can override at
`/speckit.specify` or `/speckit.clarify` time (§Scope resolution). No blocking
ambiguity was found; the ARCHITECTURE §M11 paragraph is unusually prescriptive
and settles the milestone's shape on its own.

**Prior milestone**: M10 retro at
[`docs/retros/m10.md`](../../docs/retros/m10.md). M10 made the company
**reactive to the outside world** — inbound connectors normalize external events
(GitHub issues / review requests) into tickets on the existing bus, one-way
inbound only. M10's closing boundary is explicit (M10 retro §"No outbound
call"): "nothing reaches back out. M11's Action Broker is the correct boundary
for outbound action." **M11 is the other half of that valve** — the audited,
policy-gated path by which agents *act on the world*. With M11 the company can
both receive external events (M10) and emit external actions (M11) — and an
agent never holds the ability to act directly, only to *request* an action the
broker executes.

**Binding inputs** (read before specifying; full annotations in §Binding inputs
below): `ARCHITECTURE.md` §M11 paragraph (the committed design — unusually
prescriptive, names the verb, the four tiers, the dispatcher, the Outbox, and
the permanent-Approve rule), `ARCHITECTURE.md` §M10 + §M12 (the boundaries on
either side), `RATIONALE.md` §6 (hiring is propose→approve→execute in the UI,
not git — the pattern M11 generalizes) and the mechanism-over-prompt /
sealed-registry posture, the four `docs/security/` threat models (M11 opens a new
**outbound** trust boundary — see §Threat-model-first), the M7 hiring context +
retro + `hiring-threat-model.md` (the propose→approve→install lifecycle M11
universalizes), `agent-sandbox-threat-model.md` (the default-deny networking /
egress-proxy allow-list that makes the broker the *only* door), `m8-context.md` +
retro (the `agent_instance_id` audit anchor + sealed-verb registry mechanics),
`m9-context.md` + retro (the most recent sealed-verb addition; the
agent-caller-rejection precedent; the immutable-history + dashboard-surface
shape), `m10-context.md` (the boundary above M10 and the threat-model-first
discipline), `AGENTS.md` (sealed-surfaces list, precedence, locked-deps soft
rule, spec-kit flow), `.specify/memory/constitution.md` (Principles I, III, VI).

**No spike required (RATIONALE §13).** M11 does not depend on the observed
behavior of an external tool the way M10 depended on GitHub's webhook
mechanics. The broker's *external action providers* (GitHub API for a
comment-back, an email provider for mail-send, etc.) are per-action-type
integrations whose behavior is well-understood; the milestone's load-bearing
design is the **internal** request→policy→dispatch→audit machinery, which is
composed from M5.3/M7/M8/M9 substrate already in production. If a *specific*
first action provider (e.g. the GitHub comment-back API) turns out to need
empirical probing, that is a narrow action-type spike inside the M11 plan, not a
milestone-gating spike. Flagged as open question Q1.

---

## Scope resolution (operator absent — resolved from committed docs)

The single load-bearing scope question for M11 is: **does M11 ship the broker
framework (verb + tier table + dispatcher + Outbox) with one or two concrete
action types riding it, or does it attempt the full action-provider catalog?**
This is the same shape as M10's framework-vs-all-connectors question, and it
resolves the same way against the same kind of signal.

ARCHITECTURE §M11 describes the **machinery** in full (the sealed verb, the
four-tier policy table, the dispatcher worker, the Outbox surface, the
vault-scoped-credential discipline) but names **no specific first action type**
the way §M10 named GitHub as "the likely first connector." The lowest-regret
reading is:

- **M11 ships the action-broker *framework* — the load-bearing design: the
  sealed `request_external_action` verb, the tier policy table, the dispatcher
  worker, the Outbox approval surface, the vault-scoped-credential split — plus
  at least one concrete action type that exercises every tier path end-to-end.**
- The **first concrete action type is GitHub comment-back / reply**, because it
  is the natural completion of the M10 dogfood loop (M10 ingests a sortie issue
  → Concierge triages → M11 lets the agent *reply on the issue* through the
  broker). This proves the framework against a real provider and against the
  permanent-Approve rule (a public reply is Approve-tier forever).
- **Other action types (mail-send to customers/non-customers, release-cut,
  price/copy changes, social posts) are fast-follow** action types that ride the
  same framework, classified by the same tier table, executed by the same
  dispatcher. Whether a *second* action type lands inside M11 to prove the tier
  table is genuinely a table (not a GitHub-shaped special case) is the
  operator's call (Q2). This context is written **framework + first action
  type**, with the framework explicitly built to host the rest without
  re-architecture.

The framework is where the architecture lives — the request-to-pending-row
contract, the tier-classification lookup, the dispatcher's vault-scoped
execution, the Outbox approval queue, the immutable audit anchor. A second
action type proves the framework is a framework; whether it lands in M11-core or
as M11.1 sizes the milestone but does not change the framework sections.

No conflict with any committed doc was found in resolving this (§Scope-mismatch
check). If the operator wants the full catalog in one ship, widen §In scope and
re-run; the framework sections hold unchanged.

---

## Scope-mismatch check against committed docs

Per the skill's Behavior 3, the resolved scope was checked against the committed
docs before writing the body. **No blocking conflict was found.** Four tensions
are squared explicitly so the spec does not trip on them:

1. **The egress allow-list currently permits only Anthropic.** The
   `agent-sandbox-threat-model.md` 2026-06-10 amendment and
   `supervisor/egress/squid.conf` show the real enforcement primitive: agent
   containers join the `internal: true` `garrison-agents` network with **no
   route out**, and the only egress is CONNECT through the squid sidecar whose
   committed allow-list contains **exactly `api.anthropic.com`** and denies all
   else. The brief's framing — "M7 default-deny container networking is the
   enforcement primitive — the broker is the only door, not a convention" — is
   therefore already structurally true: an agent *physically cannot* reach
   GitHub, an email provider, or any external service. **This is not a conflict;
   it is the load-bearing precondition M11 relies on.** The spec must NOT widen
   the squid allow-list to give agents direct egress — that would dissolve the
   enforcement. The dispatcher worker is **supervisor-side** (outside the
   agent network, with its own egress path), exactly as M10's connectors are
   supervisor-side. Named here so the spec does not "helpfully" punch an egress
   hole that defeats the whole milestone. (M12's sortie web-gateway is the
   *separate, audited* exception for agent web access; it does not change M11's
   posture.)

2. **The word "Outbox" collides with the existing transactional `event_outbox`
   table** (M1's `pg_notify` reliability pattern, `supervisor/internal/store/
   event_outbox.sql.go`). These are unrelated: `event_outbox` is the internal
   at-least-once event-delivery buffer; the M11 **Outbox** is the operator's
   *approval queue* dashboard surface for pending external actions. The spec
   must name the new table/surface unambiguously (e.g. `pending_actions` +
   an `/outbox` or `/admin/outbox` dashboard route) so no reader confuses the
   two. Named here so the schema naming is deliberate, not accidental.

3. **RATIONALE §6 commits "hiring happens in the web UI... CEO proposes,
   operator approves, system writes + installs. No git PR."** M11 explicitly
   **generalizes** this exact pattern (propose → human approves → system
   executes) into the universal outbound path — so it *extends* §6's posture
   rather than contradicting it. The hiring queue (M7) is the concrete prior
   instance; the Action Broker is the same shape made universal. No amendment
   needed; flagged so the spec frames M11 as the generalization §M11 says it is,
   and reuses the M7 `hiring_proposals` / `approve_hire` Server-Action precedent
   rather than inventing a parallel approval mechanic.

4. **The sealed-verb registry is the only door for agent-initiated mutation
   (chat-threat-model Rule 1).** `request_external_action` joins the
   `Verbs` slice in `supervisor/internal/garrisonmutate/verbs.go` as the next
   sealed verb (the 12th), with all the registry discipline that implies: the
   verb entry, a threat-model reversibility-class amendment, a per-verb handler,
   and the `TestVerbsRegistryMatchesEnumeration` registry test all change
   together. This is the established mechanism, not a new one — named so the
   spec adds the verb the M5.3/M8/M9 way and does not invent a side channel.

No committed doc describes a structure M11 needs to *change*. No
amendment-before-write is required for the framework. The **threat-model
deliverable** (§Threat-model-first) is a pre-implementation prerequisite
following the M2.3 / M5.3 / M7 / M10 precedent — that is an addition, not a
contradiction.

---

## Threat-model-first (sealed-surface discipline)

M11 opens a new **outbound** trust boundary: the company can now *act on the
world*. Every prior milestone that opened a new trust boundary wrote (or
amended) a threat model **before code landed** — M2.3 (`vault-threat-model.md`
first), M5.3 (`chat-threat-model.md` amendment before the mutate verbs), M7
(`agent-sandbox-` + `hiring-threat-model.md`), M10 (`ingress-threat-model.md`
before connector code, asserted by a git-log acceptance check). M11 follows the
same discipline.

A new `docs/security/action-broker-threat-model.md` (or an amendment to an
existing inbound/outbound-surface doc if the operator prefers consolidation)
lands **before dispatcher code**. It must cover at minimum: the **agent-cannot-
act-directly invariant** (the broker is the only door — the egress allow-list +
supervisor-side dispatcher is the structural enforcement, §Scope-mismatch #1);
**tier-bypass / tier-downgrade attacks** (a compromised or injected agent
requesting a public-facing action mislabeled as `auto` — the tier is decided by
the **policy table keyed on action type**, never by an agent-supplied field, so
the agent cannot self-classify); **credential isolation** (the dispatcher holds
vault-scoped credentials the agent containers never see — M2.3 Rule 1 extended
to outbound action credentials); **approval-queue integrity** (an attacker
cannot auto-approve their own pending action; the approve click is an operator
Server Action anchored like M7's `approve_hire`); **the permanent-Approve floor**
(public-facing actions are Approve-tier *by construction*, not by current
config, and cannot be reclassified to a lower tier — reputation damage is the
existential risk); **blast radius of a misbehaving dispatcher** (it can execute
*approved/auto* actions; it cannot self-approve, cannot mutate the tier table,
cannot reach the vault beyond its scoped action credentials); and **idempotency
of dispatch** (an approved action must execute exactly once even across
supervisor restart — the same exactly-once discipline M1/M9/M10 enforce).

This is named as a prerequisite, not pre-written here: the threat model is its
own deliverable, and the first action provider's specifics (e.g. GitHub
comment-back auth) feed it.

---

## Why this milestone now

M10 finished the **inbound** half of the company's external interface — events
*arrive*. M11 closes the **outbound** half, and the ARCHITECTURE §M11 paragraph
calls it "the keystone of the AI-native-company arc." Concretely:

1. **Agents can think and write internally, but cannot act on the world.** An
   agent can read a sortie issue (M10), reason about it, write to its workspace
   and MemPalace, and create internal tickets (M8) — but it cannot reply on the
   issue, send a customer email, cut a release, or post anything. Today that gap
   is enforced *structurally* (the egress allow-list permits only Anthropic), so
   the company is mute to the outside world. M11 gives agents a **requesting**
   path — never a direct-acting one — through a single audited, policy-gated
   broker.

2. **The dogfood loop needs the round-trip.** M10 wired sortie issue → Concierge
   triage. The loop only closes when the Concierge can *respond* — reply on the
   issue, request a label, ask a clarifying question. That response is an
   outbound external action, and M11 is the milestone that makes it
   policy-gated rather than impossible-or-ungoverned.

3. **The hiring-queue pattern proved the shape; M11 universalizes it.** M7's
   propose → operator-approves → system-executes hiring flow (RATIONALE §6) is
   the prior concrete instance of exactly the broker pattern. M11 generalizes it
   from "hiring a role" to "any outbound action" — the universal outbound path.
   The substrate is all in production: the sealed-verb registry (M5.3), the
   `agent_instance_id` audit anchor (M8), the vault-scoped-credential discipline
   (M2.3), the Server-Action approval surface + immutable-history dashboard
   pattern (M7/M9), the supervisor-side worker-outside-the-agent-network shape
   (M10). M11 composes them at the outbound boundary rather than inventing new
   mechanism. The one genuinely new thing — emitting attacker-influenceable
   actions to the outside world — is exactly what the threat-model-first
   discipline and the permanent-Approve floor exist to handle.

4. **Reputation is the existential risk and the permanent-Approve floor is the
   answer.** ARCHITECTURE §M11 is categorical: anything public-facing (posts,
   replies, releases, mail to non-customers, price/copy changes) is **Approve-
   tier permanently** — "one spammy autopost costs more than a year of approval
   clicks saves." M11 is where that doctrine becomes a structural floor in the
   tier table, not a prompt instruction an injection could override.

---

## In scope

The four threads below mirror the §M11 design commitments. Thread 1 is the
sealed request verb; thread 2 is the tier policy table; thread 3 is the
dispatcher worker; thread 4 is the Outbox approval surface. The first concrete
action type (GitHub comment-back) rides all four.

### 1. The sealed `request_external_action` MCP verb (Thread 1)

- **New sealed verb** joining the `Verbs` slice in
  `supervisor/internal/garrisonmutate/verbs.go` as the next entry (the 12th),
  with full sealed-registry discipline (chat-threat-model Rule 1): registry
  entry + threat-model reversibility amendment + per-verb handler
  (`verbs_actions.go` or similar) + `TestVerbsRegistryMatchesEnumeration`
  update, all in one change. This is the M5.3/M8/M9 mechanism, not a new one.
- **Caller model** — the verb is callable **by agents** (running inside the
  per-agent container, anchored on `agent_instances.id` per M8's
  `Deps.AgentInstanceID` seam). Whether the chat-CEO can also call it directly,
  and whether any per-role allow-list applies, is a spec decision (Q4); the
  M8 default ("any active agent may call, defence-in-depth from downstream
  gates") is the lean.
- **Effect** — the verb does **not** act. It **writes a `pending_actions` row**
  (pending-action row, §M11) describing the requested action: action type,
  target, rendered payload, the requesting `agent_instance_id`, and the
  ticket/context it serves. The row is **immutable and anchored to
  `agent_instance_id`** (§M11, same M8 audit posture). The tier is **assigned
  by the policy table at write time** (Thread 2) — never supplied by the agent.
- **Return shape** — the verb returns a typed `Result` (the existing
  `garrisonmutate.Result` chip shape) telling the agent the action was *queued*
  and at what tier (auto / notify / approve / human_only), so the agent's diary
  reflects "requested, pending operator approval" rather than "done."

### 2. The tier policy table (Thread 2)

- **A tier policy table** classifying every action type into one of the four
  §M11 tiers: **`auto`** (act + log), **`notify`** (act, then tell the
  operator), **`approve`** (blocked until operator click), **`human_only`**
  (agent prepares the action; the dispatcher *never* executes it — a human does
  it by hand). The classification is **keyed on action type**, lives **in the
  table, not in prompts** (M2.2.1 mechanism-over-prompt; §M11 binding), and is
  the single authority for what tier an action runs at.
- **The permanent-Approve floor** — anything public-facing (posts, replies,
  releases, mail to non-customers, price/copy changes) is **Approve-tier
  permanently** (§M11 binding). This is a structural constraint the table must
  encode such that these action types **cannot be reclassified downward** — the
  spec decides the enforcement shape (a non-overridable column, a CHECK, a
  protected-set the edit path refuses to lower), but the *floor itself* is
  binding, not a default.
- **Whether the table is schema rows, code constants, or both** is a plan
  decision; whether the tier set is operator-editable (and for which action
  types — never for the permanent-Approve floor) is a spec decision (Q5). The
  M11 first action type (GitHub public comment-back) is Approve-tier by the
  floor, which means the first end-to-end path exercises the **approve** tier —
  the one the Outbox exists for.

### 3. The dispatcher worker (Thread 3)

- **A dispatcher worker** (supervisor-side, **outside** the agent network) that
  executes **approved and auto/notify** pending actions using **vault-scoped
  credentials the agent containers never see** (§M11 binding; M2.3 Rule 1
  extended to outbound action credentials). It reacts to (a) a new `auto`/
  `notify` pending-action row and (b) an operator approval of an `approve`-tier
  row — the reactive `pg_notify` shape (M1/M8/M9/M10), with the
  `processed_at`-fallback poll for at-least-once.
- **`human_only` actions are never executed by the dispatcher** — the agent
  prepares the action (the row records the prepared payload), and a human
  performs it by hand. The dispatcher skips these structurally (§M11 binding).
- **Exactly-once dispatch** — an approved action executes exactly once even
  across supervisor restart, surviving the M1 dedup race (`FOR UPDATE SKIP
  LOCKED` claim + a terminal status transition on the row, the M9/M10
  idempotency discipline). The spec must design for this from the start, not
  rediscover it in a retro.
- **Outcome recording** — every dispatch attempt writes an **immutable**
  outcome to the action's history (executed / failed / skipped_human_only /
  notified), anchored to the originating `agent_instance_id` (§M11 audit
  posture). The dispatcher does **not** mutate the request row's audit anchor;
  it appends outcome history the way M9's `scheduled_task_runs` appends to
  `scheduled_tasks`.

### 4. The Outbox dashboard surface (Thread 4)

- **An Outbox surface** (e.g. `/outbox` or `/admin/outbox`, named to avoid the
  `event_outbox` collision per §Scope-mismatch #2) that is the **operator's
  approval queue** (§M11 binding). Lists pending `approve`-tier actions with
  enough detail to decide (action type, target, full rendered payload, the
  requesting agent + ticket, the tier and *why* it is at that tier).
- **Approve / reject as Server Actions** — the operator's approve click is a
  **dashboard Server Action**, anchored and audited like M7's `approve_hire`
  (Server-Action-only, both chat + agent anchors NULL; the
  `ServerActionVerbs` precedent). Approval transitions the pending row to
  `approved`, which the dispatcher reacts to. Reject transitions to `rejected`
  with an immutable audit trail.
- **Notify-tier surfacing** — `notify`-tier actions (executed, then
  operator-told) show up as a *post-hoc feed* item, not an approval gate. The
  depth of the notify surface (a feed row vs. a notification) is an open
  question (Q7).
- Per the standing `feedback_test_scope_go_only` convention, **tests land on the
  Go side**; the dashboard Server Actions and Outbox view are covered by the
  Go integration tests' row shapes, not by frontend tests.

---

## Out of scope

Listed explicitly so the spec does not drift:

1. **Giving agents direct egress.** M11 does **not** widen the squid egress
   allow-list to let agent containers reach external services. The broker is the
   only door precisely because agents physically cannot reach the outside
   (§Scope-mismatch #1). The dispatcher is supervisor-side. This is the single
   most important boundary in this context — the inverse of M10's "inbound only"
   boundary.

2. **Agent web access (research / fetch / browse).** Outbound *web access* for
   agents (search, fetch, link verification) routed through the sortie sidecar
   is **M12**, a separate audited gateway. M11 is about **discrete external
   actions** (reply, send, release), not general web egress. M12's sortie
   gateway does not change M11's posture and vice versa.

3. **The full action-provider catalog (this ship).** Mail-send, release-cut,
   price/copy changes, social posts, etc. are fast-follow action types riding
   the same framework (§Scope resolution). Their provider integrations and tier
   assignments land in their own follow-ups (M11.x or named follow-ups). If the
   operator widens M11 to co-ship more action types, this moves into §In scope;
   the framework holds unchanged.

4. **Reclassifying the permanent-Approve floor.** Public-facing action types are
   Approve-tier permanently (§M11 binding). M11 does not ship a mechanism to
   lower them, and explicitly is not a milestone where "make autopost auto-tier"
   is a configurable option. This is a non-goal, not a deferral.

5. **A workflow / approval-chain engine.** A pending action is one row with one
   approval gate; M11 does not introduce multi-approver chains, conditional
   approvals, or action DAGs. M8's ticket dependencies remain the only
   inter-work-item structure.

6. **Multi-project / per-project tier policy.** Whether the tier table gains a
   `project_id` dimension is explicitly parked for **M13** (ARCHITECTURE §M13
   names it as an open question for M13's spec). M11 ships single-project; the
   tier table is keyed on action type, not (action type, project).

7. **Mutating sealed surfaces beyond the named additions.** `finalize_ticket` /
   `finalize_oneshot` schemas, vault rules, `garrison_agent_*` roles, MemPalace
   MCP wiring, the per-agent container model, MCPJungle naming, the M10 ingress
   surface — all carry unchanged. M11 **adds** one sealed verb
   (`request_external_action`), the tier table, the dispatcher, and the Outbox;
   it does not redesign the existing verb pipeline, the audit-row schema beyond
   the new action rows, or the egress posture.

---

## Binding inputs

| Document | Why it binds |
|---|---|
| `ARCHITECTURE.md` §M11 | **The committed design (read first).** Names the sealed `request_external_action` verb, the four tiers (`auto`/`notify`/`approve`/`human_only`) and their exact semantics, the dispatcher worker with vault-scoped credentials agents never see, the Outbox approval surface, the generalization of the M7 hiring queue, the two carried doctrines (policy-in-table-not-prompts; default-deny-networking-as-enforcement-primitive / broker-is-the-only-door), the permanent-Approve floor for public-facing actions, and the immutable-row-anchored-to-`agent_instance_id` audit posture. The spec elaborates this; it does not re-decide it. |
| `ARCHITECTURE.md` §M10 + §M12 | The boundaries on either side. §M10 is the inbound valve M11 completes (the round-trip). §M12 is agent *web access* via the sortie gateway — a separate outbound concern M11 does not touch. Binds the in/out-of-scope lines. |
| `RATIONALE.md` §6 + mechanism-over-prompt posture | "Hiring: CEO proposes, operator approves in the dashboard, system writes + installs. No git PR." This is the concrete prior instance of the propose→approve→execute pattern M11 universalizes. Also the M2.2.1 mechanism-over-prompt decision the tier-table-not-prompts doctrine extends. |
| `specs/_context/m7-context.md` + `docs/retros/m7.md` + `docs/security/hiring-threat-model.md` | The propose→approve→install lifecycle (proposal row → operator Server-Action approve → system executes) is the exact shape M11 generalizes to the universal outbound path. The hiring-threat-model's immutable-proposal-snapshot + reviewed-snapshot-in-audit rules are the precedent for the pending-action row's immutability and the approve audit anchor. |
| `docs/security/agent-sandbox-threat-model.md` + `supervisor/egress/squid.conf` | **The enforcement primitive.** The `internal: true` `garrison-agents` network + the squid egress allow-list (currently `api.anthropic.com` only) is *why* "the broker is the only door, not a convention" — agents physically cannot reach external services. M11 must not widen this. The dispatcher lives supervisor-side, outside the agent network. |
| `specs/_context/m8-context.md` + `docs/retros/m8.md` | The `agent_instance_id` audit anchor (`Deps.AgentInstanceID`) the pending-action row uses; the sealed-verb registry mechanics (`Verbs` slice, reversibility class, `TestVerbsRegistryMatchesEnumeration`); the agent-as-authorized-caller pattern. |
| `specs/_context/m9-context.md` + `docs/retros/m9.md` | The most recent sealed-verb addition (`create_scheduled_task`, the 11th) and its tier-3 / agent-rejection precedent; the immutable-history-table pattern (`scheduled_task_runs` appends to `scheduled_tasks`) the action-outcome history mirrors; the `ServerActionVerbs` dashboard-only-verb precedent for the approve/reject Server Actions; the fire-exactly-once / `FOR UPDATE SKIP LOCKED` dispatch discipline. **Check same-day goose migration version collisions at plan time** (M9 gotcha 5 / M7.1 retro — two consecutive milestones collided). |
| `specs/_context/m10-context.md` + `docs/retros/m10.md` | The boundary above M10 (outbound is M11); the threat-model-first discipline + git-log acceptance assertion shape (M10 SC pattern); the supervisor-side-worker-outside-the-agent-network shape the dispatcher reuses; the immutability/idempotency disciplines for an externally-influenced surface. |
| `docs/security/` threat models (`vault-`, `chat-`, `agent-sandbox-`, `hiring-`, `ingress-`) | The threat-model-first precedent and the existing trust-boundary analyses M11's new `action-broker-threat-model.md` builds alongside. Vault model binds the dispatcher's scoped-credential handling (Rule 1 extended to outbound action credentials); chat model's sealed-verb Rule 1 governs the new verb; hiring model's approval-integrity rules govern the Outbox approve path. |
| `AGENTS.md` + `.specify/memory/constitution.md` | Sealed-surfaces list (M11 adds one verb + threat model; amends none of the existing sealed surfaces); precedence order; locked-deps soft rule (the dispatcher's first action provider — a GitHub comment-back — is an HTTPS call to the GitHub REST API, achievable in stdlib `net/http`; justify or avoid any new dependency, Q1); spec-kit flow; constitution Principle I (Postgres source of truth, `pg_notify` bus — the dispatcher is reactive), III (agents ephemeral — the requesting agent may be gone by dispatch time, so the row carries everything), VI (UI-driven approval, not git). |

---

## Open questions the spec must resolve

Resolve via `/speckit.clarify` or pin as deferred-with-explicit-fallback. Not
pre-decided here.

1. **First action provider + whether it needs a narrow spike.** GitHub
   comment-back is the resolved first action type (§Scope resolution). Does the
   GitHub REST API comment-create path (auth model, idempotency, error shape)
   need a narrow action-type spike inside the M11 plan, or is it well-enough
   understood to spec directly? Lean: spec directly (the M10 GitHub spike
   already mapped the App/PAT/webhook-secret landscape; comment-create reuses
   the same auth surface). Any new Go dependency must be justified or avoided.

2. **Action-type count in M11.** Resolved here as framework + GitHub
   comment-back (§Scope resolution). Spec/operator confirms: one action type, or
   a second (e.g. a `notify`-tier action) to prove the tier table is genuinely
   a table and exercise a non-`approve` path end-to-end? The framework sections
   hold regardless; this sizes the milestone.

3. **`pending_actions` schema + tier-table shape.** New `pending_actions` table
   keyed how (action type, target, payload, `agent_instance_id`, tier, status)?
   Is the tier policy a separate `action_tier_policy` table, a CHECK-constrained
   column, code constants, or a hybrid? Where does the permanent-Approve floor
   live such that it **cannot** be lowered (a protected-set the edit path
   refuses, a non-nullable non-overridable column, a CHECK)? Relates to M9's
   immutable-history shape (`scheduled_task_runs`) for the outcome history.

4. **Caller model for `request_external_action`.** Agents call it (anchored on
   `agent_instance_id`, the M8 seam) — confirmed. Can the chat-CEO also call it
   directly? Any per-role allow-list, or any-active-agent (the M8 lean, with
   defence-in-depth from the tier gate + permanent-Approve floor)? Does it reuse
   the existing `garrison-mutate` MCP server/tool name, or a new entry (M8 Q6
   shape; lean: stay unified)?

5. **Tier-table editability.** Is the operator allowed to *raise* tiers (make an
   `auto` action `approve`) and/or *lower* non-floor tiers via a dashboard
   surface, or is the tier table deploy-time config in M11 alpha? The
   permanent-Approve floor is never lowerable regardless. Lean: deploy-time
   config for alpha, operator-editability a follow-up — mirrors M10's
   deploy-time-connector-config lean.

6. **Dispatcher placement + lifecycle.** Is the dispatcher a new goroutine in
   the supervisor errgroup (the M9 tick-loop / M10 ingress-server shape), a
   separate worker process, or folded into an existing worker
   (`mcpserverwork.Worker` is the nearest precedent — a reactive worker that
   writes an audit row when an external call returns)? Plan-level, but the spec
   states the reactive contract (reacts to new auto/notify rows + to approvals;
   `processed_at` fallback for at-least-once; exactly-once dispatch under the M1
   race).

7. **Notify-tier + approval-notification surface depth.** `notify`-tier actions
   are executed-then-operator-told. Is that a feed row in the Outbox, a separate
   notification, or both? And for `approve`-tier: does the operator get an
   active notification that something awaits approval, or only the Outbox badge?
   Minimum is the Outbox approval queue + the immutable action history; the
   active-notification depth is open (relates to whether M11 wants any
   push-notification path or stays pull-only for alpha).

8. **Dispatch failure + retry posture.** When the dispatcher's external call
   fails (GitHub 500, rate limit, network), does it retry (how many times, what
   backoff), surface to the operator, or mark the action `failed` and stop? The
   "back-pressure not back-off" M6 posture and the exactly-once constraint
   interact here — a retry must not double-post. Spec states the posture; plan
   implements.

9. **`human_only` preparation surface.** For `human_only` actions, the agent
   prepares the action and a human performs it by hand. Where does the operator
   *see* the prepared action and mark it done — the same Outbox surface with a
   "mark as done by hand" transition, or a separate view? Does marking it done
   require recording what was actually done (for audit), or just a completion
   transition? Spec resolves the minimum.

10. **Permanent-Approve action-type enumeration.** §M11 names the categories
    (posts, replies, releases, mail to non-customers, price/copy changes). The
    spec must enumerate the concrete *action types* M11 ships into the floor and
    the rule by which a new action type is classified (the default for an
    unclassified action type — lean: **Approve-tier by default**, the
    safe-by-construction posture, so a newly-added action type is never silently
    `auto`).

---

## Acceptance criteria framing

Detailed criteria belong in the spec; frame them along these axes:

- **Request-to-pending lifecycle**: an agent calls `request_external_action` via
  the per-agent container's MCP entry; exactly one immutable `pending_actions`
  row lands, anchored on the calling `agent_instance_id`, with the tier assigned
  **by the policy table** (not by any agent-supplied field). The verb returns a
  typed queued/at-tier result; nothing has acted on the world yet.

- **The four tiers behave distinctly**: an `auto` action is executed by the
  dispatcher with a log row and no operator gate; a `notify` action is executed
  then surfaced post-hoc; an `approve` action is **blocked** until an operator
  Server-Action approve click, then dispatched; a `human_only` action is
  **never** dispatched — the agent prepares it and a human completes it. Each
  tier path is backed by a test that proves the gate holds.

- **The permanent-Approve floor cannot be lowered**: an attempt (config or
  agent-supplied) to classify a public-facing action type (e.g. GitHub public
  comment-back) as `auto`/`notify` is rejected/ignored structurally; the action
  lands at `approve` regardless. Testable against a contrived
  lower-the-tier attempt.

- **The broker is the only door**: an agent cannot reach the external provider
  directly — asserted via the egress allow-list (the agent network has no route
  to GitHub; the squid log shows TCP_DENIED for any direct attempt). The only
  successful external action is one the **dispatcher** performed after the
  request transited the tier gate.

- **Credential isolation**: the dispatcher's vault-scoped action credentials
  never appear in any agent container's env, prompt, or context (M2.3 Rule 1
  extended). The `vaultlog` analyzer / leak-scan posture covers the new
  credential path.

- **Exactly-once dispatch under restart + race**: an `approve`-tier action
  approved once and then redelivered/re-claimed (or surviving a supervisor
  restart mid-dispatch) executes **exactly once** — no double-post. Testable
  with the M8/M9/M10 concurrent-claim chaos shape.

- **Immutable, reconstructable audit**: a randomly-picked dispatched action
  reconstructs to its originating `agent_instance_id`, the requested payload, the
  assigned tier, the operator who approved it (for `approve`-tier), and the
  dispatch outcome — the M8 audit posture, the M9 immutable-history shape.

- **Threat model precedes code**: `action-broker-threat-model.md` is committed in
  git history before the first dispatcher-code commit, asserted by the
  acceptance script's git-log check (the M9 SC-007 / M10 pattern).

---

## What this milestone is NOT

- **NOT direct agent action.** No agent ever acts on the world directly. The
  egress allow-list stays Anthropic-only; the dispatcher is supervisor-side. The
  instant code would give an agent direct external reach, that is a regression of
  the milestone's whole point — and it is M12's *separate, audited* sortie
  gateway, not M11, that handles agent web access.

- **NOT policy-in-prompts.** The tier classification lives in the **table**, not
  in any agent prompt or the immutable preamble. An injected agent cannot
  self-classify an action to a lower tier. (M2.2.1 mechanism-over-prompt.)

- **NOT a reclassification of the permanent-Approve floor.** Public-facing action
  types are Approve-tier permanently. M11 ships no mechanism to lower them and is
  not the milestone where autopost becomes auto-tier.

- **NOT a new spawn path.** The requesting agent uses the existing per-agent
  container + sealed-verb MCP entry; the dispatcher is a supervisor-side reactive
  worker. M11 adds no claudeproto, no finalize sibling, no agent-runtime change.
  (Like M10, unlike M9.)

- **NOT a workflow / multi-approver engine.** One pending action, one approval
  gate. No approval chains, no conditional approvals, no action DAGs.

- **NOT a multi-project ship.** The tier table is keyed on action type, not
  (action type, project). `project_id` on the tier policy is parked for M13.

- **NOT the M11 "Outbox" reusing `event_outbox`.** The new approval-queue surface
  is distinct from M1's transactional `event_outbox` table; the schema/route
  naming is deliberately unambiguous.

- **NOT a re-opening of sealed surfaces.** finalize schemas, vault rules, agent
  roles, MemPalace wiring, container model, MCPJungle naming, the M10 ingress
  surface all carry byte-for-byte. M11 adds one sealed verb, the tier table, the
  dispatcher, the Outbox, and a threat model; it changes none of the above.

---

## Spec-kit flow for M11

1. **Branch first** (AGENTS.md step 0): `023-m11-action-broker` — **already cut
   from `main` at the M10 merge and checked out.** No branch step needed.

2. **No milestone-gating spike.** M11's load-bearing design is internal
   machinery composed from in-production substrate (§"No spike required"). If the
   first action provider (GitHub comment-back) needs empirical probing, that is a
   narrow action-type spike *inside* the plan (Q1), not a pre-spec gate.

3. **`/speckit.constitution`** — already populated; no M11 amendments
   anticipated (M11 extends RATIONALE §6's propose→approve→execute posture; it
   introduces no new constitutional decision).

4. **`/garrison-specify m11`** — spec against this context. Four-thread
   structure mirrors §In scope (request verb, tier table, dispatcher, Outbox),
   with the GitHub comment-back first action type riding all four.

5. **`/speckit.clarify`** — burn down Q1–Q10. Likely operator-preference
   deferrals: Q2 (action-type count), Q5 (tier-table editability), Q7 (notify /
   notification surface depth).

6. **Pre-implementation housekeeping**: the
   **`action-broker-threat-model.md`** lands before dispatcher code (Rule-1-
   shaped, the M2.3 / M5.3 / M7 / M10 precedent) — pre-plan housekeeping commit
   or alongside the plan, with a git-log acceptance assertion (M9 SC-007 / M10
   shape). The sealed-verb amendment to `chat-threat-model.md` (the new verb's
   reversibility class) lands in the same pass. Any sealed-surface note in
   `AGENTS.md` for the new outbound surface lands here too.

7. **`/garrison-plan m11`**, **`/garrison-tasks m11`**, **`/speckit.analyze`**,
   **`/garrison-implement m11`** — standard cycle. Coverage target ≥82% on new
   Go code; lint locally before pushing (`gofmt -l .` + `go vet ./...` from
   `supervisor/`); Go-side tests only per repo convention. **Check same-day goose
   migration versions at plan time** (M9 gotcha 5 / M7.1 retro — two consecutive
   milestones collided; M10's migration is `20260612000000` so M11's must
   advance the date/sequence).

8. **Retro** — `docs/retros/m11.md` + MemPalace `wing_company / hall_events`
   drawer mirror per the M3+ dual-deliverable policy. Retro must answer: did the
   broker hold as the only door — did anything reach the outside world *not*
   through the dispatcher? Did the permanent-Approve floor resist a
   lower-the-tier attempt? Did dispatch stay exactly-once under restart/race? Did
   the dispatcher's credentials stay out of every agent context? Did the threat
   model precede the code in history?

---

## Additions vs. extractions (Behavior 6 — operator review)

This context is partly synthesis of the binding inputs and partly additions the
assistant made beyond what they literally say. The additions, flagged for
operator review:

- **Resolving GitHub comment-back as the first concrete action type.** §M11
  names no first action type (unlike §M10's explicit "GitHub is the likely first
  connector"). The choice is *inferred* from the M10 dogfood loop (sortie issue →
  Concierge triage needs a reply-back to close). If the operator wants a
  different first action type, swap it in Q1/Q2 — the framework sections are
  type-agnostic.
- **`pending_actions` as the request-row table name and `/outbox` (or
  `/admin/outbox`) as the route name.** §M11 says "pending-action row" and
  "Outbox surface" descriptively; the concrete names are the assistant's, chosen
  to avoid the `event_outbox` collision. If these belong in the spec rather than
  the context, treat them as suggestions, not decisions.
- **`action-broker-threat-model.md` as the new threat-model filename.** The
  threat-model-first *discipline* is binding (the precedent is unbroken); the
  specific filename and whether it is a new doc vs. an amendment is the
  assistant's suggestion (Q-adjacent — the operator may prefer a consolidated
  inbound/outbound surface doc).
- **The "Approve-tier by default for unclassified action types" lean (Q10).**
  §M11 establishes the permanent-Approve floor for *named* public-facing
  categories; the safe-by-construction default for an *unclassified* new action
  type is the assistant's extension of that doctrine, not a literal §M11
  statement. Flagged as an open question, not pre-decided.
- **The `verbs_actions.go` handler filename and "12th verb" count.** Mechanical
  extrapolation of the existing per-domain-file convention
  (`verbs_tickets.go` / `verbs_hiring.go` / `verbs_scheduled.go`); the plan owns
  the actual filename.

If any of these feel like spec-level decisions rather than context-level, they
move to §Open questions and the spec decides them.
