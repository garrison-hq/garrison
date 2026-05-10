# Feature Specification: M8 — Agent-spawned tickets, cross-department dependencies, runaway control, MCP-server registry

**Feature Branch**: `018-m8-zero-human-loop`
**Created**: 2026-05-03
**Status**: Draft
**Input**: User description: "M8 — close the event-driven zero-human loop. Four threads in one ship: agent-spawned tickets, cross-department dependencies, per-department weekly runaway-control gate, MCPJungle as the chosen MCP-server registry."

**Binding context**: [`specs/_context/m8-context.md`](../_context/m8-context.md). All structural decisions (four-thread merge, any-active-agent caller surface, same-write-path runaway gate, single-MCPJungle-instance for alpha + per-customer-instance Option A for beta) are inputs to this spec, not questions.

**Spike**: [`docs/research/m8-mcpjungle-spike.md`](../../docs/research/m8-mcpjungle-spike.md) — binding for MCPJungle's ACL + license + maturity facts.

**Substrate retros**: [`docs/retros/m7.md`](../../docs/retros/m7.md) (per-agent container, hiring flow, immutable preamble), [`docs/retros/m6.md`](../../docs/retros/m6.md) (throttle gate; M8 extends `throttle_events` with a new kind).

---

## Clarifications

### Session 2026-05-10

- Q: MCPJungle bearer-token vault-grant shape — new per-agent table, extend `agent_role_secrets` with discriminator, or synthetic role_slug encoding? → A: Extend `agent_role_secrets` with nullable `agent_id` column (option B). One table, discriminator column, one grant-lookup query path; matches the M7 audit-table-extension precedent.
- Q: Agent-spawned ticket `parent_ticket_id` — auto-inherit calling agent's current ticket, NULL by default, or auto-inherit and reject explicit overrides? → A: Auto-inherit (option A). New ticket's `parent_ticket_id` defaults to the calling agent's current ticket; an explicit agent-supplied value overrides the default.
- Q: Paused agent's MCPJungle McpClient — delete on pause, stay-but-inaccessible, or empty allow-list on pause? → A: Stay-but-inaccessible (option B). McpClient row + bearer token persist across pause/resume; `agents.status='paused'` blocks new spawns so the token isn't reachable. McpClient deletion ties to agent deletion (not pause).
- Q: Dashboard surface for agent-anchored audit rows — defer to M8.1 polish, ship minimal filter surface in M8, or build dedicated per-role audit page? → A: Minimal in-M8 (option B). Extend the existing audit-row surface with an `agent_instance_id` filter + render path + links to ticket + agent role. Dedicated per-role audit page deferred (M8.1 candidate).
- Q: `acceptance_criteria` validation parity for agent callers — keep parity, relax for agents, or require + auto-derive? → A: Keep parity (option A). FR-005a confirmed as-is: non-empty `acceptance_criteria` required for both chat-CEO and agent callers; agent-prompt discipline (one bullet in each role's `agent.md`) propagates via M7's agent.md edit flow.

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Agent reflects on finalize and spawns a follow-up ticket (Priority: P1)

The engineer agent, mid-`finalize_ticket` reflection on a fix it just shipped, observes "this same bug exists in the marketing dashboard's analytics widget — should be a separate ticket." Today the agent has no way to record this; the operator only sees prose buried in a diary entry. Under M8, the agent calls `mcp__garrison-mutate__create_ticket` with the new ticket's objective + department; a row lands in `tickets` with the audit anchor pointing at the agent's `agent_instances.id`, and the dispatcher picks it up under the same dedupe + concurrency-cap discipline as any other ticket.

**Why this priority**: this is the load-bearing capability that closes the event-driven zero-human loop. M8's other three threads exist primarily to make this safe (runaway gate + cross-dept deps + per-agent ACLs).

**Independent Test**: with M7's runtime substrate (engineer agent in a per-agent container, hiring flow live), an integration test seeds a `column_slug='in_dev'` ticket with an `acceptance_criteria` that hints "if X, also create a marketing follow-up." The engineer's spawn observes the hint and calls `create_ticket`; the new row's `chat_mutation_audit` entry carries `agent_instance_id != NULL` and `chat_session_id IS NULL`. Verifies the agent-side caller surface end-to-end.

**Acceptance Scenarios**:

1. **Given** an active engineer agent spawned against a ticket whose acceptance criteria suggests a follow-up, **when** the agent calls `create_ticket` via the per-agent container's `garrison-mutate` MCP entry, **then** a `tickets` row lands with `column_slug='todo'` in the named department.
2. **Given** the agent-created ticket from #1, **when** the audit row is read back, **then** `chat_mutation_audit.agent_instance_id` matches the calling agent's `agent_instances.id`, `chat_session_id` is NULL, and `chat_message_id` is NULL.
3. **Given** the agent-created ticket from #1, **when** the dispatcher reads `pg_notify('work.ticket.created.<dept>.todo')`, **then** spawn-prep proceeds under the same dedupe + concurrency-cap path as a chat-CEO-created ticket.
4. **Given** the agent provides explicit `acceptance_criteria` text in the verb call, **when** the row lands, **then** `tickets.acceptance_criteria` matches the agent's input verbatim.
5. **Given** the agent omits `acceptance_criteria`, **when** the verb runs, **then** the call rejects with `validation_failed` per FR-005a (chat-CEO callers carry the same rejection in M5.3).
6. **Given** the agent omits `parent_ticket_id`, **when** the verb runs, **then** the new ticket's `parent_ticket_id` defaults to the calling agent's current ticket (auto-inherit per FR-006).
7. **Given** the agent supplies an explicit `parent_ticket_id` value, **when** the verb runs, **then** the explicit value is honoured verbatim and the auto-inherit default is skipped (FR-006 second clause).

---

### User Story 2 — Cross-department dependency gates spawn-prep (Priority: P1)

The chat-CEO decomposes a goal: "ship the new pricing page" requires marketing copy + engineering implementation. Marketing's ticket is in `column_slug='in_dev'`; engineering's ticket carries `depends_on_ticket_id` pointing at marketing. The engineering ticket does not spawn until marketing reaches a configured satisfaction state (default `qa_review` or `done`). When marketing's `pg_notify('work.ticket.transitioned.marketing.in_dev.qa_review')` fires, the dispatcher re-evaluates engineering's spawn-prep; the gate clears; engineering's spawn proceeds.

**Why this priority**: without cross-department dependencies, agent-spawned tickets create a fan-out hazard — a single agent's reflection produces N independent tickets that race each other or block on prose-tracked dependencies. The structural primitive replaces prose with a foreign key.

**Independent Test**: integration test seeds two tickets with the dependency relationship; asserts the dependent stays at `column_slug='todo'` until the predecessor reaches the satisfaction state, then spawns. The reactive event-loop integration is exercised by the existing `pg_notify` machinery from M1 + M5.x.

**Acceptance Scenarios**:

1. **Given** ticket A in department X with `column_slug='in_dev'` and ticket B in department Y with `depends_on_ticket_id=A.id`, **when** the dispatcher walks B's spawn-prep, **then** B does NOT spawn (stays at `column_slug='todo'`).
2. **Given** the same setup, **when** A transitions to `column_slug='qa_review'` (or another column in Y's `dependency_satisfaction_columns`), **then** the dispatcher re-evaluates B's spawn-prep within one `pg_notify` round-trip and B spawns.
3. **Given** an attempt to create a ticket whose `depends_on_ticket_id` would close a cycle (A→B→C→A), **when** the verb runs, **then** the call rejects with a typed `dependency_cycle` error and no row lands.
4. **Given** a ticket whose dependency chain depth exceeds 32 hops, **when** the verb runs, **then** the call rejects with `dependency_chain_too_deep` (the cycle-detection walker's depth cap).
5. **Given** ticket A is deleted (cascade), **when** B references A via `depends_on_ticket_id`, **then** B's `depends_on_ticket_id` becomes NULL via `ON DELETE SET NULL` (the dependency was satisfied by removal of the unmet predecessor; spawn-prep proceeds).

---

### User Story 3 — Runaway control gates a runaway agent past the weekly budget (Priority: P1)

A misbehaving agent (e.g. a prompt-injection bypass that slipped past the M7 preamble) loops through `create_ticket` calls. The engineering department's `weekly_ticket_budget` is set to 50; the agent has already spawned 50 tickets in the rolling 7-day window. The agent's 51st call is rejected at the `create_ticket` write path; a `throttle_events` row lands with `kind='dept_weekly_ticket_budget_exceeded'`; the agent receives a typed failure result with no `tickets` row written.

**Why this priority**: P1 because the broadest-surface (any-active-agent can `create_ticket`) is unsafe to ship without the structural backstop. The M7 preamble is one defence; the runaway gate is the structural defence that doesn't depend on prompt fidelity.

**Independent Test**: unit test against a fake clock — seed 50 tickets in the window, attempt the 51st, verify the gate fires + the `throttle_events` row + no `tickets` row.

**Acceptance Scenarios**:

1. **Given** `departments.engineering.weekly_ticket_budget=50` and 50 tickets created in the rolling 7-day window, **when** any caller (chat-CEO or agent) calls `create_ticket` against engineering, **then** the verb rejects with `dept_weekly_ticket_budget_exceeded` and no `tickets` row lands.
2. **Given** the same rejection, **when** the audit table is read back, **then** a `throttle_events` row with `kind='dept_weekly_ticket_budget_exceeded'` is present, plus a `chat_mutation_audit` row with `outcome='dept_weekly_ticket_budget_exceeded'`.
3. **Given** `departments.engineering.weekly_ticket_budget IS NULL`, **when** any caller invokes `create_ticket`, **then** the gate is bypassed (NULL = unlimited; M8 alpha default).
4. **Given** the rolling window advances (a 51st-most-recent ticket falls out of the 7-day window), **when** the next `create_ticket` runs, **then** it succeeds.
5. **Given** an agent attempts a cross-department create (engineer creating a marketing ticket), **when** marketing's budget is exceeded but engineering's is not, **then** the call rejects (the gate scopes against the **target** department's budget, not the caller's).

---

### User Story 4 — Agent calls a registered third-party MCP server through MCPJungle (Priority: P2)

Operator approves a hire that includes the `twilio-sms` MCP server in `mcp_servers_jsonb`. M7's `ApproveHire` flow creates the agent, M7's actuator installs the agent's skills, M8's actuator extension creates an MCPJungle `McpClient` row for the new agent with `AllowList=["twilio-sms"]` and a customer-prefixed name (`garrison.engineer.<agent-uuid-short>`). The agent's per-agent container's `--mcp-config` includes an `mcpjungle` entry pointing at MCPJungle's `/mcp` endpoint with the agent's bearer token (vault-injected). The agent's first spawn calls `mcp__mcpjungle__twilio-sms-send` and the call routes through MCPJungle, hits the registered Twilio MCP server upstream, returns a result.

**Why this priority**: P2 — MCPJungle integration is the unblocker for Hey Anton-style product-surface integrations, but M8 ships with zero registered MCP servers (operator registers them via Server Action separately). The capability is what M8 enables; the actual third-party servers ship in later milestones or operator-side.

**Independent Test**: integration test against a fake MCP upstream registered in MCPJungle. New hire's first spawn issues a call against the registered server; assert the bearer token reaches MCPJungle, MCPJungle routes to the upstream, the response returns to the agent.

**Acceptance Scenarios**:

1. **Given** an MCPJungle server registered with name `twilio-sms` and an agent whose AllowList includes `twilio-sms`, **when** the agent's spawn calls `mcp__mcpjungle__twilio-sms-send`, **then** the call routes through MCPJungle to the upstream and the response returns to the agent.
2. **Given** the same setup but the agent's AllowList does NOT include `twilio-sms`, **when** the agent attempts the call, **then** MCPJungle returns 403 + the agent's `tool_use` surfaces a typed rejection in the spawn's audit trail.
3. **Given** an attempt to register an MCP server via the mcpjungle CLI directly (bypassing Garrison's hire flow), **when** the operator runs the CLI, **then** the production MCPJungle deployment rejects (admin token is operator-secret, in vault, not on the operator's local CLI). The supported path is exclusively the `register_mcp_server` Server Action.
4. **Given** MCPJungle is unreachable at supervisor startup, **when** the supervisor boots, **then** it logs a warning and starts (degrade-with-warning, not fail-closed); per-spawn calls that need MCPJungle fail individually with a typed error.

---

### User Story 5 — Operator registers a new MCP server via the dashboard Server Action (Priority: P2)

Operator decides to enable `twilio-sms` for the engineer role. From `/admin/mcp-servers`, operator submits a new server registration: name, transport (HTTP / stdio), upstream URL or command, optional bearer token. The `register_mcp_server` Server Action validates inputs, calls MCPJungle's HTTP API to register the upstream, writes a Garrison-side `mcp_servers` row with the registration metadata + audit, and returns success.

**Why this priority**: P2 — admin path supporting US4. Without this, the only way to register an MCP server is direct mcpjungle CLI, which contradicts the structural commitment that all MCP-server writes go through Garrison's hire flow.

**Independent Test**: Server Action unit-test against a fake MCPJungle HTTP backend; verify the API call shape and the audit-row write.

**Acceptance Scenarios**:

1. **Given** operator submits a valid server registration, **when** the Server Action runs, **then** MCPJungle receives the corresponding `POST /servers` API call, a Garrison `mcp_servers` row lands with the registration shape, and a `chat_mutation_audit` row records `verb='register_mcp_server'`.
2. **Given** operator submits a registration whose name violates the customer-prefix convention (e.g. trying to register a server named `twilio-sms` instead of `garrison.twilio-sms`), **when** the Server Action runs, **then** the call rejects with `validation_failed` and no MCPJungle write happens.
3. **Given** MCPJungle's API call fails (network error or upstream rejection), **when** the Server Action's tx is in flight, **then** the Garrison-side rollback unwinds the audit + mcp_servers writes (no partial state).

---

### User Story 6 — Operator views per-department runaway counts on the dashboard (Priority: P3)

From `/hygiene` (or a new `/throttle` surface), operator sees: per-department weekly ticket-creation count, configured budget, days-since-budget-exceeded. Operator can edit the budget via the existing `edit_department` Server Action shape (extending M4's pattern with a new editable field).

**Why this priority**: P3 — observability and admin polish. The data is recordable from M8 ship via `throttle_events`; the UI is an extension of existing M3/M4 patterns. Not gating M8 ship if the dashboard surface lands as M8.1 polish.

**Independent Test**: dashboard read-side unit test against seeded throttle_events rows; assert the per-department counts + budget render correctly.

**Acceptance Scenarios**:

1. **Given** a department with `weekly_ticket_budget=50` and 47 tickets created in the rolling window, **when** operator loads `/hygiene` (or `/throttle`), **then** the surface shows `47 / 50` for that department.
2. **Given** a budget-exceeded event, **when** operator loads the surface, **then** a typed warning indicator + most-recent `throttle_events.fired_at` is visible.
3. **Given** operator edits the budget via the Server Action, **when** the action commits, **then** `departments.weekly_ticket_budget` is updated + an `event_outbox` row records the diff (per M4's pattern).

---

### Edge Cases

- **Agent calls `create_ticket` while its parent ticket is being deleted by the operator.** The agent's tx sees a stale `ticket_id` reference; the FK fails. Verb rejects with `resource_not_found`; agent receives the typed failure.
- **Agent calls `create_ticket` cross-department to a department whose runaway budget is exceeded.** Per AS5 of US3, the gate scopes against the target department; rejection lands. The agent's audit row carries `cross_dept_create=true` regardless of outcome (forensic record).
- **Cross-department dependency points at a ticket in a deleted department.** The dependency FK enforces ticket existence; ticket-level deletion cascades the dependency to NULL (US2 AS5). Department deletion is rare; if it happens, the cascade chain is well-defined.
- **MCPJungle's Postgres becomes unreachable mid-spawn.** Agent's MCPJungle call times out at the agent's claude-side timeout; the spawn's `pipeline.go` propagates the timeout as a typed `tool_use` failure. Supervisor stays up; only the in-flight agent call fails.
- **Agent's bearer token rotates mid-spawn (e.g. operator demotes the agent).** Rotation is "delete McpClient + recreate" per the spike findings; the agent's in-flight spawn carries the old token in env-var. M8 ships create-once-keep semantics (no rotation); this edge surfaces only if a future milestone adds rotation.
- **Two agents in the same department both call `create_ticket` simultaneously and the budget is at 49 (one slot left).** The 50th + 51st calls race in the gate's read-then-write tx; one succeeds, one rejects with `dept_weekly_ticket_budget_exceeded`. No two-tickets-when-budget-allows-one race condition.
- **Cycle-detection walker hits a ticket marked `column_slug='deleted'` (if such a state exists in M8).** Walker treats deleted predecessors as satisfied (terminate the walk); cycle through deleted nodes is impossible because they're terminal.
- **MCP server registered with a customer prefix that doesn't match the active customer.** Server Action rejects per US5 AS2; the customer-slug invariant is enforced Garrison-side.
- **Agent calls `create_ticket` with `parent_ticket_id` pointing at a chat-CEO-created ticket in another department.** Allowed (M6 already permits cross-dept parents); audit row records the cross-dept tag.

---

## Requirements *(mandatory)*

### Functional Requirements

#### Agent-spawned tickets

- **FR-001**: System MUST extend the M5.3 `create_ticket` verb's caller surface to accept agent callers in addition to chat-CEO callers. Caller identity is established via `agent_instances.id` (passed to the supervisor via the verb's MCP call context, mirroring M5.3's `chat_session_id` pattern).
- **FR-002**: System MUST add a column `chat_mutation_audit.agent_instance_id UUID NULL REFERENCES agent_instances(id) ON DELETE SET NULL` so agent-anchored audit rows are forensically resolvable.
- **FR-003**: System MUST permit any agent whose `agents.status='active'` to call `create_ticket`. No per-role caller allow-list at M8 alpha (defence is runaway gate + M7 preamble).
- **FR-004**: System MUST keep verb-name unification: agent callers and chat-CEO callers both invoke `mcp__garrison-mutate__create_ticket`. Caller identity (chat session vs agent instance) is the discriminator on the audit row.
- **FR-005**: System MUST validate that one and only one of `chat_session_id` or `agent_instance_id` is non-NULL on every `chat_mutation_audit` row written for `create_ticket`. Enforcement is **Go-side verb-time validation** (helper `assertExactlyOneCallerAnchor(deps)` in `internal/garrisonmutate`), NOT a DB-level CHECK constraint — a table-wide CHECK would break M7-era Server Action audit rows (approve_hire, reject_hire, etc.) which have both anchors NULL legitimately. The exactly-one-of rule applies only to rows with `verb='create_ticket'`.
- **FR-005a**: System MUST require non-empty `acceptance_criteria` for both chat-CEO and agent callers (carryover from M5.3); rejection on omission is `validation_failed`.
- **FR-006**: System MUST default `tickets.parent_ticket_id` on agent-spawned tickets to the calling agent's current ticket (resolved via `agent_instances.ticket_id` for the caller's `agent_instance_id`). An agent-supplied explicit `parent_ticket_id` value overrides the default; if supplied, the explicit value is honoured verbatim and the auto-inherit path is skipped. Chat-CEO callers retain M6's behaviour (no auto-inherit; `parent_ticket_id` is set only when the chat verb call supplies it).

#### Cross-department dependencies

- **FR-100**: System MUST add `tickets.depends_on_ticket_id UUID NULL REFERENCES tickets(id) ON DELETE SET NULL` plus partial index `idx_tickets_depends_on` on non-NULL rows. Cross-department FK is allowed; CHECK rejects self-reference (`depends_on_ticket_id <> id`).
- **FR-101**: System MUST add `departments.dependency_satisfaction_columns JSONB NULL` carrying the array of `column_slug` values that satisfy a dependency. Default at department creation: `["qa_review", "done"]`. Operator-tunable per-department via the M4 `edit_department` Server Action.
- **FR-102**: System MUST gate spawn-prep (the dispatcher's per-event tx) on dependency satisfaction. A ticket with `depends_on_ticket_id IS NOT NULL` does not spawn until the predecessor's `column_slug` is in the predecessor's department's `dependency_satisfaction_columns`.
- **FR-103**: System MUST detect dependency cycles at `create_ticket` time via an in-tx O(N) walk capped at depth 32. Cycles reject with `dependency_cycle`; depth-cap exceeded rejects with `dependency_chain_too_deep`.
- **FR-104**: System MUST emit `pg_notify('work.ticket.dependency_added.<dept>', payload)` post-commit for every `depends_on_ticket_id` write. Payload shape: `{ticket_id, depends_on_ticket_id, dept, created_at}`.
- **FR-105**: System MUST re-evaluate blocked dependents on every `pg_notify('work.ticket.transitioned.<dept>.<from>.<to>')` event. The dispatcher's existing M5.3 transition listener is the natural seam; no new listener required.

#### Runaway control

- **FR-200**: System MUST add `departments.weekly_ticket_budget INT NULL`. NULL = unlimited (M8 alpha default). Operator-tunable via the M4 `edit_department` Server Action.
- **FR-201**: System MUST gate `create_ticket` inline in the verb's tx. Gate fires if the rolling-7-day count of tickets in the target department + 1 would exceed the budget. The gate runs against the **target** department (where the new ticket lands), not the caller's department.
- **FR-202**: System MUST extend `throttle_events.kind` CHECK with `dept_weekly_ticket_budget_exceeded`. The audit row carries `payload_jsonb` with `{department_id, dept_slug, current_count, budget, attempted_caller_id}`.
- **FR-203**: System MUST extend `chat_mutation_audit.outcome` CHECK with `dept_weekly_ticket_budget_exceeded`. The verb's failure surface uses this outcome (parallel to existing `concurrency_cap_full`).
- **FR-204**: System MUST emit `pg_notify('work.throttle.event', payload)` for every `dept_weekly_ticket_budget_exceeded` row (reuses M6's channel; same shape).
- **FR-205**: System MUST surface the budget-exceeded reason as a typed `Result.ErrorKind` to the verb caller. Agents see the typed failure in their `tool_result`; chat-CEO callers see it as a failed verb result frame.
- **FR-206**: System MUST tag agent-driven cross-department creates in the audit row's `args_jsonb` with `cross_dept_create=true` (forensic record; orthogonal to the gate's accept/reject decision).

#### MCP-server registry — MCPJungle integration

- **FR-300**: System MUST deploy MCPJungle as a sidecar container on `garrison-net` in enterprise mode (`SERVER_MODE=enterprise`). Admin token is initialised at deployment time and stored in Infisical at path `mcpjungle/admin`; access scoped via an `agent_role_secrets` row with role_slug=`<reserved-operator-role>` (admin token is operator-only; never granted to agents).
- **FR-301**: System MUST run MCPJungle against its own dedicated Postgres container (separate from `garrison-postgres`), matching the M2.3 Infisical-Postgres precedent.
- **FR-302**: System MUST add a `mcpjungle.URLForCustomer(customerID pgtype.UUID) string` lookup interface in the supervisor. M8 alpha implementation returns `GARRISON_MCPJUNGLE_URL` unconditionally; beta-time multi-tenant swaps in a customer-id-keyed map (Option A).
- **FR-303**: System MUST add `companies.customer_slug TEXT UNIQUE NOT NULL` (data-model primitive enabling beta-time per-customer-instance lookup). M8 alpha seeds a single row with `customer_slug='garrison'`.
- **FR-304**: System MUST create exactly one MCPJungle `McpClient` per `agents` row at agent-activation time (post-M7 `ApproveHire` or post-M7 `migrate7` grandfathering). Naming convention: `<customer_slug>.<role-slug>.<agent-uuid-short>`. Allow-list populated from `agents.mcp_servers_jsonb`.
- **FR-305**: System MUST inject the agent's MCPJungle bearer token into the per-agent container's environment at container-start time (M2.3 Rule 1 — secrets never in prompts). The token is fetched from Infisical via the existing vault fetcher; the grant lives in `agent_role_secrets` with a non-NULL `agent_id` column (added by this milestone — see FR-403 below) scoping the grant to one specific agent instance. Token path: `mcpjungle/agents/<agent-id>`.
- **FR-306**: System MUST provide a Server-Action-only `register_mcp_server` verb (per M7's F3 lean — Server-Action-only mutations live outside the chat verb registry). The verb writes a Garrison `mcp_servers` row with `status='pending'` in a Drizzle transaction; the INSERT trigger emits `pg_notify('work.mcp_server.registration_requested', ...)`; the supervisor's reactive worker (plan §2) picks up the event, calls MCPJungle's HTTP API to register the upstream, UPDATEs `mcp_servers.status` to `registered` | `failed`, and writes **exactly one** `chat_mutation_audit` row with `verb='register_mcp_server'` + `outcome='success'|'failed'` recording the final state. The Server Action itself does NOT write an audit row — the audit anchors on the worker's completion so a single row reflects the true outcome.
- **FR-307**: System MUST validate that every `register_mcp_server` invocation includes the active customer's slug as a name prefix (`<customer_slug>.<server_name>`). Rejections land as `validation_failed`.
- **FR-308**: System MUST implement degrade-with-warning behaviour at supervisor startup if MCPJungle is unreachable (matches M2.2's mempalace startup gate). Supervisor logs a warning + starts; per-spawn calls that need MCPJungle fail individually with a typed error.
- **FR-309**: System MUST extend the per-agent container's `--mcp-config` with an `mcpjungle` entry pointing at the customer's MCPJungle URL with the agent's bearer token (via env-var resolution).
- **FR-310**: System MUST extend the M7 `agent_install_journal` step set with two new steps: `mcpjungle_client_create`, `mcpjungle_allowlist_apply`. Both run after `container_start`; failures are recoverable per M7's `Resume` algorithm.
- **FR-311**: System MUST NOT mutate an agent's MCPJungle `McpClient` row on `pause_agent` / `resume_agent`. The McpClient row + bearer token persist across pause cycles; the existing `agents.status='paused'` check in the supervisor's spawn-prep blocks new spawns, which is what keeps the token unreachable. McpClient deletion is tied to agent deletion (the agent's removal cascades the per-agent vault grant from FR-403 + the McpClient via a Garrison-side teardown hook), NOT to pause.

#### Schema + audit consolidation

- **FR-400**: System MUST add a Goose migration `20260510000000_m8_zero_human_loop.sql` covering FR-002, FR-100, FR-101, FR-200, FR-202, FR-203, FR-303 + the `chat_mutation_audit` CHECK extensions for the new audit verbs (`register_mcp_server`) + the `agent_install_journal.step` CHECK extension for the two new step values from FR-310 + the `agent_role_secrets.agent_id` column from FR-403 + the `mcp_servers` table.
- **FR-401**: System MUST extend `chat_mutation_audit_verb_check` with `register_mcp_server`.
- **FR-402**: System MUST regenerate `dashboard/drizzle/schema.supervisor.ts` to reflect the new columns + tables (per the M5.4 / M7 retro lessons — drizzle-pull post-migration is non-optional).
- **FR-403**: System MUST extend `agent_role_secrets` with a nullable `agent_id UUID REFERENCES agents(id) ON DELETE CASCADE` column. Existing role-scoped grants leave `agent_id` NULL; M8's per-agent MCPJungle bearer-token grants set `agent_id` to the specific `agents.id` the grant scopes to (lifetime tied to the agent's lifecycle, cascaded on agent deletion). The vault fetcher's grant-lookup query gains a single OR clause to honour either anchor (role-scope OR agent-scope).

#### Dashboard surfaces

- **FR-500**: System MUST add `/admin/mcp-servers` (Server Component listing registered MCP servers + a registration form) backed by the `register_mcp_server` Server Action (FR-306).
- **FR-501**: System MUST extend `/hygiene` (or add `/throttle`) with per-department weekly ticket-creation counts + budget configuration. Operator-edits route through the M4 `edit_department` Server Action with a new editable field.
- **FR-502**: System MUST extend the existing M3-shipped audit-row surface (on `/activity` or its equivalent — whichever M3 wired as the audit-row reader) with an `agent_instance_id` filter and a render path for agent-anchored rows. Each rendered row links to (a) the originating ticket via `agent_instances.ticket_id` and (b) the agent role via `agent_instances.role_slug` joined to `agents`. A dedicated per-role audit timeline page is explicitly out of scope for M8 (M8.1 polish candidate).

### Key Entities

- **Agent-anchored audit row**: extension of the M5.3 `chat_mutation_audit` shape with a new `agent_instance_id` column. Carries the same `verb` + `args_jsonb` + `outcome` + `reversibility_class` + `affected_resource_id` semantics as the chat-anchored variant. Distinguishable from chat-anchored rows via the (chat_session_id, agent_instance_id) discriminator pair.
- **Cross-department ticket dependency**: a foreign-key relationship `tickets.depends_on_ticket_id` carrying the predecessor's `tickets.id`. Spawn-prep gate honours the relationship until the predecessor reaches a satisfaction state. Cycles rejected at create-time.
- **Per-department weekly ticket-creation budget**: `departments.weekly_ticket_budget INT NULL`. The runaway-control gate's policy primitive.
- **`throttle_events` row**: extension of M6's table with a new `kind` value `dept_weekly_ticket_budget_exceeded`. Same row shape; new accepted enum value.
- **MCPJungle `McpClient`**: per-agent record in MCPJungle's database carrying the agent's bearer token + `AllowList` (JSON array of server names). Created at agent-activation time; persists across `pause_agent` / `resume_agent` cycles (FR-311); deleted at agent deletion via the per-agent teardown hook. Naming convention encodes customer slot from day one.
- **`mcp_servers` row (Garrison-side)**: Garrison's record of a registered MCP server. Mirrors MCPJungle's row but tracks Garrison-specific metadata (registering operator, audit cross-reference). Populated only via `register_mcp_server` Server Action.
- **`companies.customer_slug`**: per-customer string primitive enabling beta-time per-customer-instance lookup. M8 alpha seeds one row (`garrison`); beta onboarding adds rows.
- **MCPJungle URL lookup**: `mcpjungle.URLForCustomer(customerID) string` — M8 alpha returns the single configured URL; Option A beta-time swap-in returns customer-id-keyed values.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A real engineer agent (post-`finalize_ticket`) calls `create_ticket` against the marketing department; a row lands with the correct `agent_instance_id` audit anchor and `chat_session_id IS NULL`. End-to-end via integration test against testcontainer Postgres + the per-agent container.
- **SC-002**: A dependent ticket with `depends_on_ticket_id` set does not spawn until the predecessor reaches the satisfaction state. The dispatcher re-evaluates within one `pg_notify` round-trip after the predecessor's transition.
- **SC-003**: A 51st `create_ticket` call against a department with `weekly_ticket_budget=50` and 50 tickets in the rolling window rejects with `dept_weekly_ticket_budget_exceeded` and writes a `throttle_events` row; no `tickets` row lands.
- **SC-004**: `register_mcp_server` Server Action registers a fake MCP server in MCPJungle + writes a Garrison `mcp_servers` row + writes a `chat_mutation_audit` row, all in one transaction. Rollback on MCPJungle API failure unwinds the Garrison-side writes.
- **SC-005**: A new hire's first spawn calls a registered MCP server through MCPJungle; the bearer token reaches MCPJungle, MCPJungle routes to the upstream, the response returns to the agent. No direct call to MCPJungle's admin API from the agent's path.
- **SC-006**: An attempt by an agent to call an MCP server NOT in its `AllowList` returns 403 from MCPJungle; the agent's `tool_use` audit records the rejection with `error_kind='access_denied'`.
- **SC-007**: M8 ships zero new Go dependencies and zero new TypeScript dependencies; the locked-deps streak (M3 → M7) extends through M8.
- **SC-008**: M2.x integration suite (M2.2 + M2.2.1 + M2.2.2 + M2.3) passes unchanged under `-tags=integration` post-M8 ship; M5.x + M6 + M7 integration suites pass unchanged.
- **SC-009**: The cycle detector rejects an attempted dependency cycle (A→B→C→A) at `create_ticket` time with no row landing; depth > 32 rejects with `dependency_chain_too_deep`.
- **SC-010**: SonarCloud quality gate ≥82% coverage on new code (matches M5.4 / M6 / M7 thresholds); 0 new Sonar issues, 0 security hotspots after the M8 PR's first issue-clearing pass.
- **SC-011**: Supervisor starts cleanly when MCPJungle is unreachable (degrade-with-warning); per-spawn MCPJungle calls fail individually with typed errors; M2.x suite still passes (regression gate).
- **SC-012**: A randomly-selected agent-anchored audit row from a populated test DB resolves to the originating `agents.id` + the `agent_instances.id` of the calling spawn within one query (forensic reconstruction).
- **SC-013**: The customer-prefix invariant on McpClient + mcp_servers names holds across **every hire flow executed during the M8 acceptance run** (the operator's scripted `scripts/m8-acceptance.sh` walks ≥10 hires). Every McpClient name carries `<customer_slug>.<role-slug>.<uuid-short>` shape; every `mcp_servers.name` carries `<customer_slug>.<server-name>` shape. Zero unprefixed names land.
- **SC-014**: An operator-side forensic query against the M3 audit-row surface filtered by `agent_instance_id` returns the matching `chat_mutation_audit` row(s), each row linking to its originating ticket + agent role within one Server Component render path (FR-502).

---

## Assumptions

- **Operator runs M8 against a single tenant in alpha.** `companies.customer_slug='garrison'` is the only seeded row. Beta-time multi-tenant onboarding requires a separate provisioner (out of scope; beta-band per `project_garrison_release_phases`).
- **MCPJungle's source signals at M8 plan time match the 2026-05-03 spike's positive signals.** If degradation surfaces (single-contributor abandonment, security CVE without patch, removal of enterprise mode), the M8 plan triggers the fallback path per `docs/mcp-registry-candidates.md` (MCPProxy or build-our-own).
- **The M7 substrate is in production.** Per-agent containers, hiring flow, immutable preamble — all live. M8 tests run against the M7-shipped surface.
- **The M6 throttle-events table + pg_notify channel are stable surfaces.** M8 extends; no M6-internal refactor.
- **Operator never directly invokes `mcpjungle create mcp-server` from the local CLI.** The admin token's vault placement enforces this by withholding the token from local CLI sessions; supported path is `register_mcp_server` Server Action.
- **No new third-party MCP servers ship in M8.** M8 enables registration + ACL surfaces; actual integrations (Twilio, Gmail, Vapi) are operator-driven post-M8.
- **MCPJungle's per-client `AllowList` JSON-array primitive is workable for M8.** Watch-item: upstream `// will be removed in favor of a separate table for ACLs` refactor may land in a future MCPJungle release; Garrison adopts the new shape if it lands without losing the customer-prefix discipline.
- **Items A, B, C, D were closed by the 2026-05-10 clarify session** (see `## Clarifications` above). No open clarify items remain at spec-handoff.
