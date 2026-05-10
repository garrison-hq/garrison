# M8 — Agent-spawned tickets, cross-department dependencies, runaway control, MCP-server registry (context)

**Status**: context for `/speckit.specify`. M7 shipped 2026-05-03 in
PR #20; per-agent container runtime + hiring flow + immutable preamble
are live. M8 starts from that substrate to close the event-driven
zero-human loop.

**Prior milestone**: M7 retro at [`docs/retros/m7.md`](../../docs/retros/m7.md).
M7 closed three threads: per-agent Docker runtime (sandbox structurally
enforced), hiring flow (operator approves new agents through chat +
`/admin/hires`), immutable security preamble (every spawn carries the
operator-controlled policy text above `agent.md`). M8 lifts that
substrate from "operator initiates work" to "agents close the loop
themselves" by extending the M5.3 `create_ticket` verb to agents,
adding cross-department dependencies, gating with per-department
runaway control, and integrating MCPJungle as the MCP-server registry.

**Binding inputs** (read before specifying):

- [`ARCHITECTURE.md`](../../ARCHITECTURE.md) §M8 paragraph (will be
  amended on M8 ship to remove "leading candidate" framing for
  MCPJungle — see "Scope deviation" below).
- [`docs/research/m8-mcpjungle-spike.md`](../../docs/research/m8-mcpjungle-spike.md)
  — fresh spike (2026-05-03) confirming MCPJungle's per-agent ACL
  primitive (`McpClient.AllowList`), the absence of a native tenant
  primitive, and the MPL-2.0 license. Three amendments to
  `docs/mcp-registry-candidates.md` flow from this spike.
- [`docs/mcp-registry-candidates.md`](../../docs/mcp-registry-candidates.md)
  — committed candidate evaluation. M8 commits MCPJungle. Doc gets the
  three spike-flagged amendments before the M8 plan cites it as
  binding.
- [`docs/architecture-reconciliation-2026-04-24.md`](../../docs/architecture-reconciliation-2026-04-24.md)
  — frozen snapshot of the 2026-04-24 architecture-session decision
  designating MCPJungle as the leading candidate. Kept unchanged.
- [`docs/security/chat-threat-model.md`](../../docs/security/chat-threat-model.md)
  — M5.3's sealed verb set. M8 extends the call-site policy: chat
  callers were the only callers in M5.3+M7; M8 adds agents (running
  inside the per-agent container) as a second authorized caller of
  the same `create_ticket` verb. Same per-row amendment shape as M5.3
  → M7's `propose_skill_change` extension.
- [`docs/security/agent-sandbox-threat-model.md`](../../docs/security/agent-sandbox-threat-model.md)
  — M7's; carries unchanged. Agents can call MCP tools via the
  per-agent container's `internal/garrisonmutate` MCP entry; the
  container's network isolation + cap-drop posture covers the
  agent-spawned-ticket call path the same way it covered hiring.
- [`docs/security/hiring-threat-model.md`](../../docs/security/hiring-threat-model.md)
  — M7's; carries unchanged. M8 doesn't touch the hiring flow.
- [`docs/retros/m7.md`](../../docs/retros/m7.md) — substrate. The
  per-agent container's `garrison-mutate` MCP entry already exists
  (M5.3); M8 wires the agent-side bind-mount + auth so agents can
  reach it.
- [`docs/retros/m6.md`](../../docs/retros/m6.md) — substrate. The
  M6 throttle gate (per-company budget + rate-limit pause) is the
  precedent M8's per-department weekly ticket-creation budget
  extends. Same `throttle_events` table; new `kind` value.
- Operator memory `project_garrison_release_phases` — alpha vs beta
  line. M8 is alpha-track ("prove it operates"). Multi-tenant runtime
  is beta. The MCPJungle deployment shape M8 commits has to leave the
  beta-time multi-tenant transition mechanically achievable.
- `supervisor/internal/garrisonmutate/verbs_tickets.go` — current
  `create_ticket` handler. Today only chat-CEO can call it; M8 lifts
  the chat-only assertion.
- `supervisor/internal/throttle/` — M6's gate. M8 extends with the
  per-department-weekly-budget primitive.

---

## Scope deviation from committed doc

M8 commits MCPJungle as the chosen MCP-server registry. The M7-kickoff
re-check gate from `docs/mcp-registry-candidates.md` ("re-evaluate at
M7 kickoff whether SkillHub has remained the right call... is
MCPJungle still actively maintained, getting security updates")
fired on 2026-05-03 via the spike, with positive signals: 995 stars,
last commit same day as the spike, MPL-2.0 license, Go + Postgres
stack match, per-agent ACL primitive confirmed.

The committed candidates doc still uses leading-candidate framing.
M8 ship moves MCPJungle from leading-candidate to chosen. Three
related amendments flow from the spike:

1. **License correction.** Earlier framing assumed Apache-2.0; the
   project is MPL-2.0. Practically equivalent for self-host, but the
   distinction matters if Garrison ever forks.
2. **ACL framing softened.** The doc says MCPJungle's ACL story "is
   the right shape for 'this tenant's agents can only reach these
   MCP servers.'" The spike confirmed per-agent (per-McpClient) is
   native; per-tenant requires Garrison-side naming convention. The
   doc should reflect the convention dependency.
3. **Watch-item added.** MCPJungle's source carries
   `// will be removed in favor of a separate table for ACLs` on
   `mcp_client.go`. The current per-client ACL is JSON-array-on-row;
   an upstream relational-ACL refactor is documented intent. Garrison
   tracks this as a watch-item for the M9+ multi-tenant transition.

These three amendments land in the M8 plan or as a pre-plan housekeeping
commit; they do NOT need to land here in the context. Surfacing them
so the spec doesn't cite the candidates doc verbatim while the doc
still has the off-the-cuff license framing.

---

## Why this milestone now

M7 shipped the runtime substrate (per-agent containers) + the hiring
flow + the immutable preamble. With those in place, three operator-
loop bottlenecks become structurally addressable in M8:

1. **The "operator must initiate every ticket" bottleneck.** Today
   tickets land via chat-CEO `create_ticket`, dashboard `/tickets/new`,
   or the Postgres seed. Agents reading their `finalize_ticket`
   completions can already see "this would benefit from a follow-up
   ticket in marketing" — they just can't create it. Agent-spawned
   tickets close that gap.

2. **The "ticket dependencies are tracked in prose" bottleneck.**
   M6 added `tickets.parent_ticket_id` for CEO decomposition (one
   parent → N children, all in the same department). But the engineer
   needing the marketer's research before they can start is still
   tracked in `acceptance_criteria` markdown. Cross-department
   dependencies make this structural — `tickets.depends_on_ticket_id`
   gates spawn-prep until the dependency is satisfied.

3. **The "broadest-surface = most dangerous" coupling.** Once any
   agent can `create_ticket` and dependencies are honored, a runaway
   agent (intentional or via prompt injection, despite the M7
   preamble) could fan out N tickets in a loop. M6's per-company
   budget catches the cost dimension; M8 adds the per-department
   weekly ticket-creation budget catches the work-volume dimension.
   Both gates sit on the same write path so every agent-spawned
   ticket transits both.

4. **MCPJungle integration** is the unblocker for "agents can call
   third-party MCP servers." Today Garrison's MCP set is in-tree
   (`pgmcp`, `finalize`, `mempalace`, `garrison-mutate`). Once Hey
   Anton work needs Twilio / Gmail / Vapi-style MCP servers, the
   registry + proxy + ACL story becomes load-bearing. M8 lays it
   down before agent-spawned-tickets create demand for it.

The four threads compose: agent-spawned tickets without dependencies
is a one-shot fan-out tool; with dependencies but without runaway
control is a structural risk; with all three but without MCPJungle
restricts agents to in-tree MCP only and forecloses Hey Anton's
product surface integrations. Shipping any subset leaves the others
as latent risk or latent demand — same composition logic as M7's
three-thread merge.

This timing also matches the alpha vs beta line per
`project_garrison_release_phases`. M8 closes the event-driven loop in
alpha. Multi-tenant runtime, BYO-agent, onboarding wizards, mobile,
and governance rollback are explicitly beta. M8's MCPJungle
integration is single-instance for alpha, with the per-customer-
instance pattern committed as the beta path (Option A from the
multi-tenant analysis: hard isolation, network + DB level, per-customer
admin tokens in vault).

---

## In scope

### Agent-spawned tickets (Thread 1)

- **Authorization extension** — the M5.3 `create_ticket` verb in
  `internal/garrisonmutate` is callable today only from chat-CEO
  contexts (the verb's `Deps.ChatSessionID` field is the auth anchor).
  M8 extends the auth model: agents call the verb from inside their
  per-agent container, with auth via the agent's
  `agent_instances.id` rather than a chat session. New audit-row
  shape: `chat_session_id` and `chat_message_id` may be NULL (already
  permitted post-M7 migration); a new `agent_instance_id` column on
  `chat_mutation_audit` (FK to `agent_instances.id`) carries the
  agent-side anchor.

- **Caller restriction** — any active agent can call `create_ticket`
  per the operator's M8 scope decision (no per-role allow-list).
  Defence-in-depth comes from runaway control (Thread 4) + the M7
  preamble's "complete only the assigned ticket" Required clause.

- **Verb behaviour** — tickets created by agents land in the same
  shape as chat-created tickets; the new audit row's
  `agent_instance_id` is the only schema-level distinguisher. Default
  `column_slug='todo'`. Department defaults to the calling agent's
  department unless an explicit override is supplied (cross-
  department creation is allowed but goes through additional checks
  per Thread 4's gate).

### Cross-department dependencies (Thread 2)

- **Schema extension** — `tickets.depends_on_ticket_id UUID NULL
  REFERENCES tickets(id)` (parallel to M6's `parent_ticket_id`).
  Partial index `idx_tickets_depends_on` on non-NULL rows. Cross-
  department-FK is allowed (no department equality check at FK
  level); plan owns whether to reject same-ticket self-references at
  CHECK level.

- **Spawn-prep gate** — `internal/spawn`'s prep step blocks if any
  ticket carries `depends_on_ticket_id` pointing at a ticket whose
  `column_slug` hasn't reached the configured satisfaction state
  (default: `qa_review`, per spec — operator-tunable per-department).
  Blocked tickets stay in `column_slug='todo'`; the dispatcher
  re-checks on every `pg_notify('work.ticket.transitioned.…')` event
  for the dependency's department. Same shape as M1's reactive event
  loop.

- **Dependency cycle detection** — at `create_ticket` time, the
  verb walks the dependency chain and rejects with
  `ErrDependencyCycle` if creating the link would close a cycle. M8
  picks: O(N) walk vs CTE — plan decides. Verb-level rejection means
  no dependency-cycle row ever lands.

- **Audit + observability** — every `depends_on_ticket_id` write
  emits a `pg_notify('work.ticket.dependency_added.<dept>', payload)`
  for the activity feed.

### Runaway control (Thread 3)

- **Schema extension** — `departments.weekly_ticket_budget INT NULL`
  (NULL = unlimited, the default for M8 alpha). New `throttle_events`
  CHECK kind: `dept_weekly_ticket_budget_exceeded`. Reuses M6's
  `throttle_events` table verbatim; no new table.

- **Gate** — `create_ticket`'s spawn-prep transaction adds a
  per-department-weekly-window count check using a lateral subquery
  shape mirrored from M6's `GetCompanyThrottleState`. When the count
  + 1 would exceed the budget, the verb rejects with
  `ErrConcurrencyCapFull` semantics (mapped via a new
  `dept_weekly_ticket_budget_exceeded` outcome on the audit table)
  and writes a `throttle_events` row. The agent receives a typed
  failure result; the dispatcher does NOT retry agent-spawned tickets
  blocked by the gate (per the M6 retro's "back-pressure not
  back-off" framing).

- **Same write path as `create_ticket`** — the gate is inline in the
  verb's tx, not a separate observer. Every agent-spawned create
  transits it. Chat-CEO `create_ticket` calls also transit the same
  gate (consistent treatment; if the operator wants chat
  exemption, that's a per-call flag rather than a separate gate).

- **Dashboard surface** — `/throttle` (or extended `/hygiene`) shows
  per-department weekly counts + budget. Operator can edit the budget
  via the `edit_department` Server Action (extension of M4's pattern).

### MCP-server registry — MCPJungle integration (Thread 4)

- **Deployment** — single MCPJungle container on `garrison-net`.
  Mounted Postgres (own DB or own schema in garrison-postgres; plan
  picks). Enterprise mode (`SERVER_MODE=enterprise`) is non-optional
  in production. Admin token initialised at deploy time + stored in
  Infisical as a Garrison secret. Per spec FR-X (the spec writes the
  X), the supervisor calls MCPJungle's HTTP API via a new
  `internal/mcpjungle` consumer package; no in-tree CLI shell-out.

- **Per-customer-instance scaffolding (option A path)** — even with
  one hardcoded customer in M8 alpha, the lookup interface
  `mcpjungle.URLForCustomer(customerID) string` is in place from day
  one. M8 alpha's implementation is "return GARRISON_MCPJUNGLE_URL
  unconditionally"; beta-time multi-tenant swaps it for a
  customer-id-keyed map populated by an `internal/migrate7`-shape
  per-customer onboarding provisioner. The structural commitment
  protects beta from a rename pass.

- **McpClient lifecycle** — every active agent gets exactly one
  MCPJungle `McpClient` row. Created at agent-activation time
  (post-`ApproveHire` from M7, or post-grandfathering from
  `migrate7`). Naming convention: `<customer>.<role-slug>.<agent-uuid-
  short>`. The `<customer>` slot is encoded from M8 day one
  (hardcoded `garrison` in alpha; per-customer in beta). The McpClient's
  `AllowList` is populated from `agents.mcp_servers_jsonb` (already
  M7 schema); operator-approved at hire time, mutated only via the
  M7 `ApproveSkillChange` flow.

- **MCP server registration** — operator never invokes `mcpjungle
  create mcp-server` directly. New verb (Server-Action only, similar
  to M7's `update_agent_md` F3 lean): `register_mcp_server` writes a
  Garrison-side `mcp_servers` row + calls MCPJungle's API to register
  the upstream. Audit row records the registration's payload + admin
  approval.

- **Agent-side wiring** — the per-agent container's `--mcp-config`
  gains a `mcpjungle` entry pointing at MCPJungle's `/mcp` endpoint
  with the agent's bearer token. The token lives in the container's
  env (vault-injected per M2.3 Rule 1; new vault grant
  `mcpjungle/<agent-id>` mapping the McpClient access token).

- **Per-agent ACLs via MCPJungle's primitive** — the McpClient's
  AllowList controls which registered MCP servers the agent can
  reach. ACL violations surface as MCPJungle's standard "client lacks
  access to server" 403; agent's spawn surfaces this as a
  `tool_use` rejection.

---

## Out of scope

Listed explicitly so the spec doesn't drift:

1. **Multi-tenant runtime.** Single-tenant alpha is what ships. Per-
   customer MCPJungle instance (option A from the multi-tenant
   analysis) is the committed beta path — beta-band per
   `project_garrison_release_phases`. M8's job is to make the beta-
   time transition mechanical (the three structural commitments
   above), not to ship multi-tenant itself.

2. **Building Garrison's own in-tree MCP gateway** to replace
   MCPJungle. Tracked as a fallback in
   `docs/mcp-registry-candidates.md`; revisit at customer-#2 in beta.

3. **MCP-server-bearing skills** — skills that ship their own MCP
   server inside the package. Deferred from M7; still deferred. M8's
   MCPJungle lets an operator register an externally-deployed MCP
   server (e.g. an Hey Anton Twilio integration); skills bringing
   their own MCP server is an M9+ concern.

4. **MCPJungle's relational-ACL refactor adoption.** Watch-item; if
   the upstream refactor lands a tenant primitive, Garrison
   re-evaluates. Until then, naming convention enforced Garrison-side.

5. **Cross-agent skill-grant policies** — "agent A can use the same
   skill another agent B has installed." Today every agent's skill
   bind-mount is per-agent (M7's `/var/lib/garrison/skills/<agent-id>`
   pattern). Sharing skills across agents is post-alpha territory.

6. **Heartbeats / scheduled wake-ups.** M9 owns this. M8 stays
   reactive (`pg_notify`-driven).

7. **Mutating sealed M2.3 + M5.3 + M7 surfaces** — vault rules,
   M5.3 verb-set machinery, hiring flow, M7 preamble. M8 extends
   `chat_mutation_audit`'s caller surface (chat → chat OR agent), but
   doesn't change the verb-execution shape or the audit-row schema
   beyond the `agent_instance_id` addition.

8. **Per-call MCP server invocation cost telemetry.** M6 closed the
   per-spawn cost-telemetry blind-spot for claude itself. Per-MCP-
   tool-call cost (e.g. Twilio API charges) is an observability
   concern for M9+ when third-party MCP servers actually ship.

9. **The post-M7 polish PR removing `cfg.UseDirectExec`.** Operator-
   driven post-soak; not blocked by M8, not enabled by it either.

---

## Open questions the spec must resolve

These ride forward from the threads, the spike, and the M7 retro.
The spec should resolve via `/speckit.clarify` or pin as deferred-
with-explicit-fallback.

1. **`agent_instance_id` on `chat_mutation_audit`** — new column or
   extend the existing `affected_resource_id` polymorphic column?
   New column wins forensic clarity; polymorphic wins schema
   stability. Plan picks.

2. **Dependency cycle detection algorithm** — O(N) walk in the
   `create_ticket` tx vs recursive CTE. Plan picks based on whether
   the dependency chain depth is bounded (and how) or unbounded.

3. **Default `weekly_ticket_budget` value** — NULL (unlimited, M8
   alpha disposition) vs a sane default (e.g. 50). Plan picks; spec
   resolves whether M8 ships with operator-explicit budgets or a
   safety-net default.

4. **MCPJungle DB shape** — own MCPJungle Postgres container
   (dedicated DB, separate from `garrison-postgres`) vs new schema
   inside `garrison-postgres`. Trade-off: blast radius isolation vs
   operational simplicity. Plan picks; spec resolves whether to share
   the existing Postgres.

5. **Bearer-token rotation surface** — the spike noted MCPJungle
   doesn't document an in-place token-rotation API; rotation today
   is "create new client + delete old." Does M8 need rotation? If
   yes, it's either a Garrison-side wrapper that does the
   delete+recreate atomically, or a deferral. Plan picks.

6. **Agent-spawned ticket via chat verb path or new MCP entry** —
   does the agent call `mcp__garrison-mutate__create_ticket` (the
   existing M5.3 verb name, just from a different caller) or a new
   `mcp__garrison-agent__create_ticket` (separate MCP server, separate
   tool name)? Same audit + same gate either way; the question is
   whether the verb-name surface stays unified or splits. Plan picks
   (lean: stay unified, single audit-row schema, single test surface).

7. **Cross-department dependency satisfaction state** — `qa_review`
   default vs `done` vs operator-tunable per-department. Spec
   resolves; plan implements.

8. **Runaway control rollover** — weekly window starts Monday
   00:00 UTC, or rolling 7-day window? Rolling matches M6's company
   throttle (rolling 24h). Calendar matches operator intuition
   ("this week's budget"). Spec picks; plan implements.

9. **MCPJungle health check at supervisor startup** — fail-closed
   (supervisor refuses to start if MCPJungle is unreachable) vs
   degrade-with-warning (start, log warning, agents that need MCPJungle
   fail individually). Same shape as M2.2's mempalace startup gate.
   Lean: degrade-with-warning to match the M2.2 pattern.

10. **Cross-department ticket creation by agents** — the engineer
    creating a marketer ticket. Allowed by default? Operator-approved
    explicitly? Spec resolves; if allowed, the runaway gate scopes
    against the **target** department's budget (not the creator's).

11. **Customer-id encoding in M8 alpha** — hardcoded string
    `"garrison"` or pulled from a new `companies.customer_slug`
    column? The lookup interface is in place either way; the question
    is whether Garrison's data model adds a customer-slug primitive
    in M8 or in beta. Lean: add it in M8 (zero ops cost, extension-
    enables beta).

---

## Acceptance criteria framing

Detailed criteria belong in the spec. The spec should frame criteria
along these axes:

- **Agent-spawned ticket lifecycle**: an active engineer agent calls
  `create_ticket` via the per-agent container's MCP entry; a new
  `tickets` row lands with the audit row carrying
  `agent_instance_id`. The new ticket fires
  `pg_notify('work.ticket.created.<dept>.todo')` and the dispatcher
  picks it up under the same dedupe + concurrency-cap discipline as
  any other ticket.

- **Cross-department dependency**: a `qa-engineer` ticket with
  `depends_on_ticket_id` pointing at an `engineer` ticket in
  `column_slug='in_dev'` does NOT spawn until the engineer's ticket
  reaches `qa_review`. Once it does, the dispatcher's `pg_notify`
  handler triggers the dependent ticket's spawn-prep gate to
  re-evaluate.

- **Dependency cycle**: an attempt to create a ticket whose
  `depends_on_ticket_id` would close a cycle is rejected at verb
  time with `ErrDependencyCycle`; no row lands.

- **Runaway gate fires**: at the configured weekly budget +1
  invocation, `create_ticket` returns the typed gate-failure result
  + a `throttle_events` row + a `dept_weekly_ticket_budget_exceeded`
  audit row. No `tickets` row lands.

- **MCPJungle integration**: a hire flows through M7's `ApproveHire`
  + creates the agent's McpClient in MCPJungle with the AllowList
  matching the proposal's `mcp_servers_jsonb`. The agent's first spawn
  successfully calls a registered MCP server's tool, and a violation
  attempt (calling a server NOT in the allow-list) returns 403 with
  the audit row recording the rejection.

- **Customer-prefix discipline**: every McpClient created by Garrison
  carries the `<customer>.<role>.<uuid>` naming convention. The
  one-customer alpha uses `garrison` as the slug; the lookup
  interface compiles even though only one entry resolves.

---

## What this milestone is NOT

- NOT a multi-tenant ship. Single-tenant alpha. The structural
  commitments make beta-time multi-tenant mechanical, not premature.
- NOT a Garrison-built MCP gateway. MCPJungle ships. Build-our-own
  is a tracked fallback.
- NOT an event-bus rewrite. M8 reuses M1's `pg_notify` shape +
  M5.3's verb pipeline + M6's throttle gate verbatim. The new
  surfaces (cross-dept deps, agent-spawned tickets) extend the
  existing patterns rather than replacing them.
- NOT a hiring flow change. M7's `propose_hire` / `propose_skill_
  change` / `bump_skill_version` + Server Action approve helpers
  carry through unchanged. M8 only adds `register_mcp_server` as a
  new Server-Action verb.
- NOT a sandbox change. M7's per-agent container + cap-drop +
  bind-mount layout carries unchanged. The new MCPJungle bearer token
  enters the container via the vault-injected env-var path (M2.3
  Rule 1).
- NOT a Heartbeats / scheduled wake-up ship. M9.
- NOT a coverage of cross-agent skill-grants. Beta.

---

## Spec-kit flow for M8

1. **`/speckit.constitution`** — already populated. No M8-specific
   amendments anticipated. (If any of the open questions surface a
   constitutional principle change, that's a separate amendment
   before /garrison-specify runs.)

2. **Pre-spec housekeeping**: amend
   `docs/mcp-registry-candidates.md` per the three deltas in §"Scope
   deviation" above. Operator-owned commit; can land before or
   alongside the spec.

3. **`/garrison-specify m8`** — draft the spec from this context.
   Four-thread structure mirrors this doc's "In scope" section.

4. **`/speckit.clarify`** — close as many of the 11 open questions
   as can be closed without empirical input. Likely deferrals:
   #4 (MCPJungle DB shape — operator preference), #5 (token
   rotation — depends on whether Hey Anton needs it at M8 ship), #8
   (calendar vs rolling window — operator preference).

5. **`/garrison-plan m8`** — produce the implementation plan. Plan
   commits the resolutions for #1, #2, #6, #7, #9, #10, #11.

6. **Pre-plan empirical work**: re-run the MCPJungle maturity check
   at plan time (does the 2026-05-03 spike's signals still hold?
   stars, last commit, security advisories). If degraded, plan
   surfaces the fallback path (option B from the multi-tenant
   analysis: single instance + naming convention; or option C:
   build-our-own).

7. **`/garrison-tasks m8`** — break the plan into tasks. Coverage
   target ≥82% on new code per the M5.4 / M7 lessons. Lint locally
   before pushing per the M5.4 retro. Sonar issues clear (not just
   gate) per the M7 retro.

8. **`/speckit.analyze`** — cross-artefact consistency check.

9. **`/garrison-implement m8`** — execute. Per the M7 → M8 lesson:
   `chat_mutation_audit.chat_session_id` and `chat_message_id` are
   already nullable post-M7, so the agent-side audit row shape doesn't
   need a migration to drop NOT NULL — it just adds the
   `agent_instance_id` column.

10. **Retro** — `docs/retros/m8.md` + MemPalace `wing_company /
    hall_events` drawer mirror per M3+ dual-deliverable policy.
    Retro must answer: did the alpha vs beta line hold? Did the
    runaway gate fire on any agent (intentional or otherwise)? Did
    MCPJungle integration introduce any new attack surface beyond
    what the threat models anticipated?

---

## Cross-references

- `docs/research/m8-mcpjungle-spike.md` — spike (this doc's binding
  input).
- `docs/security/agent-sandbox-threat-model.md` — sandbox rules,
  carry through.
- `docs/security/hiring-threat-model.md` — hiring rules, carry
  through.
- `docs/security/chat-threat-model.md` — sealed verb set; M8 extends
  the caller surface (chat → chat OR agent).
- `docs/security/vault-threat-model.md` — Rule 1 (secrets never in
  prompts) carries through; new vault grant for MCPJungle bearer
  tokens.
- `docs/retros/m6.md` — throttle gate substrate; M8 extends
  per-department weekly budget.
- `docs/retros/m7.md` — runtime substrate; per-agent container is
  where M8's agent-side `create_ticket` call lands.
- `docs/mcp-registry-candidates.md` — registry-decision document
  (subject to three amendments before M8 plan cites it).
- `docs/architecture-reconciliation-2026-04-24.md` — frozen snapshot
  of the original 2026-04-24 architecture-session decision.
