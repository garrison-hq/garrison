# Implementation Plan: M8 — Agent-spawned tickets, cross-department dependencies, runaway control, MCP-server registry

**Branch**: `018-m8-zero-human-loop` | **Date**: 2026-05-10 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification at `specs/018-m8-zero-human-loop/spec.md`
**Binding context**: [`specs/_context/m8-context.md`](../_context/m8-context.md), [`docs/research/m8-mcpjungle-spike.md`](../../docs/research/m8-mcpjungle-spike.md), [`AGENTS.md`](../../AGENTS.md) §§"Activate before writing code" + "Concurrency discipline" + "Stack and dependency rules", [`RATIONALE.md`](../../RATIONALE.md), [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md). The shipped M1 + M2.x + M3 + M4 + M5.x + M6 + M7 supervisor + dashboard is the foundation this plan extends; the M6 + M7 retros are prerequisite reading.

## Summary

M8 closes the event-driven zero-human loop by extending the M5.3 `create_ticket` verb's caller surface to agents, adding structural cross-department dependencies, gating both with a per-department weekly runaway-control budget, and integrating MCPJungle as the chosen MCP-server registry/proxy. Four threads ship in one PR (mirrors M7's three-thread merge): every thread alone leaves the others as latent risk or latent demand. Single MCPJungle instance for M8 alpha; per-customer-instance pattern (Option A from the multi-tenant analysis) is the committed beta path, structurally encoded via the `companies.customer_slug` primitive + the `mcpjungle.URLForCustomer(customerID)` lookup interface seam.

No new Go dependencies. No new TypeScript dependencies. Locked-deps streak (M3 → M7) extends through M8.

## Technical context

**Language/Version**: Go 1.23+ (supervisor); TypeScript 5.x + React 19 (dashboard) — unchanged from M7.

**Primary Dependencies** (Go, locked per AGENTS.md): `github.com/jackc/pgx/v5`, `sqlc` (build-time), `log/slog`, `golang.org/x/sync/errgroup`, `github.com/stretchr/testify`, `github.com/testcontainers/testcontainers-go`, `github.com/pressly/goose/v3`, `github.com/google/shlex`, `github.com/infisical/go-sdk v0.7.1`, `golang.org/x/tools v0.44.0`. **Zero new Go deps anticipated.**

**Primary Dependencies** (TypeScript, locked per M3+ list): unchanged. **Zero new TS deps anticipated.**

**Storage**: PostgreSQL 17 (garrison-postgres, shared); new dedicated MCPJungle Postgres container (`garrison-mcpjungle-postgres`); Infisical-Postgres (existing M2.3 sidecar); MinIO (existing M5.4 sidecar).

**Testing**: Go's `testing` package + `testcontainers-go` for integration suites; Vitest + Playwright for dashboard. Test tags: default (unit), `-tags=integration` (DB + per-agent container), `-tags=chaos` (failure injection).

**Target Platform**: Linux server (Coolify + Hetzner). MCPJungle deployed as a sidecar container on `garrison-net` alongside the existing four-container deployment topology (`garrison-supervisor`, `garrison-mempalace`, `garrison-docker-proxy`, `garrison-postgres`) + the M2.3 vault sidecars + the M5.4 MinIO. Post-M8 the stack has six service containers + two Postgres backings.

**Project Type**: event-driven multi-agent orchestration system (Go supervisor + Next.js dashboard).

**Performance Goals**: same envelope as M7 (per-spawn p95 cold-start under M7's bridge-driver baseline). New surfaces: MCPJungle HTTP call adds ≤50ms per agent-side tool call (per spike §F1's `Stateful or stateless session modes` framing); per-department weekly-budget gate adds one query per `create_ticket` (lateral subquery mirroring M6's `GetCompanyThrottleState`).

**Constraints**: zero new dependencies; degrade-with-warning for MCPJungle unreachability (FR-308); supervisor must start cleanly even if MCPJungle is down. Customer-prefix invariant on all McpClient + mcp_servers names from M8 day one (SC-013).

**Scale/Scope**: M8 alpha single-tenant (`customer_slug='garrison'`). Beta multi-tenant scaling is structurally enabled but out of scope. Sandbox per the M7 sandbox-threat-model carries unchanged.

## Constitution check

The constitution at `.specify/memory/constitution.md` was populated for M2.1; reviewed in M2.2 + M2.2.1 + M2.2.2 + M2.3 + M3 + M4 + M5.x + M6 + M7. **No M8-specific amendments anticipated.** Gates re-checked against M8 work:

| Constitution principle | M8 disposition |
|---|---|
| I. Reactive event loop via `pg_notify` | Pass — M8 extends M1's listener pattern (cross-dependency unblock via existing `work.ticket.transitioned.*` channels; new `work.mcp_server.registration_requested` for the dashboard→supervisor handoff). No polling. |
| II. Atomic per-event tx with dedupe | Pass — `create_ticket`'s tx remains the M5.3 shape; the runaway gate runs inline in the same tx; the agent-anchored audit row writes in the same tx. |
| III. Supervisor manages subprocesses; never agents | Pass — agents run inside the M7-shipped per-agent container; the MCPJungle bearer-token is env-injected at container-start (M2.3 Rule 1). The supervisor itself does not host MCP servers. |
| IV. MemPalace as shared memory | Pass — M8 does not touch MemPalace. |
| V. Skills from skills.sh / SkillHub | Pass — M7's skill-install flow carries through M8 unchanged; M8 extends the M7 install_journal with two new steps (`mcpjungle_client_create`, `mcpjungle_allowlist_apply`). |
| VI. Hiring is UI-driven | Pass — M7's `ApproveHire` is the entry point; M8 adds the `register_mcp_server` Server Action for operator-driven MCP server registration. No agent-side hire surface. |
| VII. Go supervisor with locked deps | Pass — zero new Go deps. |
| VIII. Every goroutine accepts context | Pass — new `internal/mcpserverwork` worker, the MCPJungle HTTP client, and the per-spawn token-injector all thread `ctx`. `errgroup` wraps the worker; shutdown uses `context.WithoutCancel + TerminalWriteGrace`. |
| Concurrency rule 7 (subprocess process groups) | N/A — M8 does not spawn new subprocesses. The per-agent container's claude process is owned by M7's `agentcontainer.Controller.Exec`. |
| Concurrency rule 8 (pipe drain before Wait) | N/A — same reason. |

## Project structure

### Documentation (this feature)

```text
specs/018-m8-zero-human-loop/
├── spec.md              # /speckit.specify + /speckit.clarify output (committed)
├── plan.md              # This file
└── tasks.md             # Phase 2 output (/garrison-tasks; NOT created here)
```

### Source code (repository root)

```text
supervisor/                                       # Go binary
├── cmd/supervisor/
│   ├── main.go                                   # EXTENDED — wire MCPJungle controller + mcpserverwork worker
│   └── migrations/                               # build-staging copy of repo-root migrations/
├── internal/
│   ├── mcpjungle/                                # NEW — MCPJungle admin-API HTTP client
│   │   ├── client.go                             # CreateMcpClient / DeleteMcpClient / RegisterServer / etc.
│   │   ├── client_test.go                        # unit tests
│   │   ├── reconcile.go                          # ensure-McpClient-per-agent reconciler
│   │   ├── reconcile_test.go
│   │   ├── healthcheck.go                        # GET /health degrade-with-warning helper
│   │   └── types.go                              # request/response shapes, error sentinels
│   ├── mcpserverwork/                            # NEW — reactive worker: dashboard write → MCPJungle API call
│   │   ├── worker.go                             # LISTEN on work.mcp_server.registration_requested
│   │   ├── worker_test.go
│   │   └── statemachine.go                       # pending → registered | failed
│   ├── garrisonmutate/
│   │   ├── verbs_tickets.go                      # EXTENDED — accept AgentInstanceID caller; auto-inherit parent
│   │   ├── verbs_tickets_test.go                 # EXTENDED — agent-caller cases
│   │   ├── server_action_verbs.go                # NEW — separate registry for Server-Action-only verbs
│   │   ├── register_mcp_server.go                # NEW — Server-Action verb impl
│   │   ├── register_mcp_server_test.go           # NEW
│   │   ├── deps.go                               # EXTENDED — AgentInstanceID field
│   │   ├── audit.go                              # EXTENDED — agent_instance_id passthrough
│   │   └── errors.go                             # EXTENDED — dependency_cycle + dept_weekly + access_denied
│   ├── spawn/
│   │   ├── spawn.go                              # EXTENDED — depends_on_ticket_id spawn-prep gate
│   │   ├── spawn_test.go                         # EXTENDED — dependency + mcpjungle env-inject tests
│   │   ├── m8.go                                 # NEW — agent_install_journal step extensions; MCPJungle env inject
│   │   └── m8_test.go                            # NEW
│   ├── throttle/
│   │   ├── dept_weekly.go                        # NEW — per-department weekly-budget gate
│   │   ├── dept_weekly_test.go                   # NEW
│   │   ├── throttle.go                           # EXTENDED — Check() now considers dept gate alongside company
│   │   └── throttle_test.go                      # EXTENDED — composition tests
│   ├── vault/
│   │   ├── grants.go                             # EXTENDED — resolve grants honouring agent_id discriminator
│   │   └── grants_test.go                        # EXTENDED — agent-scoped grant tests
│   ├── store/                                    # REGENERATED — sqlc output for new queries
│   ├── config/
│   │   ├── config.go                             # EXTENDED — GARRISON_MCPJUNGLE_URL + ADMIN_TOKEN_PATH
│   │   └── config_test.go
│   └── events/
│       ├── listener.go                           # EXTENDED — registration_requested + dependency_unblock listeners
│       └── listener_test.go
├── docker-compose.yml                            # EXTENDED — garrison-mcpjungle + garrison-mcpjungle-postgres
└── Dockerfile                                    # NO CHANGE — supervisor image stays same shape

migrations/
└── 20260510000000_m8_zero_human_loop.sql         # NEW — single migration covering all M8 schema deltas

migrations/queries/
├── m8_tickets.sql                                # NEW — depends_on_ticket_id queries
├── m8_throttle.sql                               # NEW — dept-weekly window count query
├── m8_audit.sql                                  # NEW — agent-anchored audit queries
├── m8_mcp_servers.sql                            # NEW — Garrison-side mcp_servers CRUD
└── m8_vault.sql                                  # NEW — agent-scoped grant lookup

dashboard/
├── app/[locale]/(app)/admin/mcp-servers/
│   ├── page.tsx                                  # NEW — registered MCP server list + register form
│   ├── [id]/page.tsx                             # NEW — single-server detail
│   └── searchParams.ts                           # NEW
├── app/[locale]/(app)/activity/
│   └── page.tsx                                  # EXTENDED — agent_instance_id filter + render path
├── lib/queries/
│   ├── mcpServers.ts                             # NEW
│   └── audit.ts                                  # EXTENDED — agent-anchored row shape
├── lib/actions/
│   └── mcpServer.ts                              # NEW — register_mcp_server Server Action (DB-direct + pg_notify)
├── components/features/mcp/                      # NEW — RegisterForm, ServerRow, StatusChip
└── drizzle/schema.supervisor.ts                  # REGENERATED via bun run drizzle:pull

docs/
├── retros/m8.md                                  # NEW (lands at retro phase)
└── ARCHITECTURE.md                               # EXTENDED at ship time (M8 paragraph annotated Shipped + retro link)

scripts/
└── dev-stack-up.sh                               # EXTENDED — provision MCPJungle + its Postgres
```

**Structure decision**: M8 lands two new in-tree Go packages (`internal/mcpjungle`, `internal/mcpserverwork`), extends six existing packages (`garrisonmutate`, `spawn`, `throttle`, `vault`, `config`, `events`), one migration, five sqlc query files, one new dashboard route + one extended route, and one docker-compose extension. No new binaries; no new external dependencies; MCPJungle runs as its own digest-pinned sidecar.

## Decisions baked into this plan

From the spec's resolved clarifications + the context file:

1. **Caller-identity discriminator**: `garrisonmutate.Deps` gains `AgentInstanceID pgtype.UUID` alongside `ChatSessionID`. Helper `assertExactlyOneCallerAnchor(deps)` validates exactly-one-of for `create_ticket`. Other verbs unaffected.
2. **Auto-inherit `parent_ticket_id`** on agent-spawned tickets per FR-006; explicit overrides honoured.
3. **Cycle detection**: O(N) walk capped at depth 32 (FR-103); cycles reject `dependency_cycle`; depth-cap exceeded rejects `dependency_chain_too_deep`.
4. **Dependency satisfaction default** `["qa_review", "done"]` per FR-101; operator-tunable per-department via M4 `edit_department` Server Action.
5. **Runaway window**: rolling 7-day per FR-201 (matches M6 throttle's rolling 24h precedent); gate scopes against **target** department.
6. **MCPJungle DB shape**: own Postgres container per FR-301.
7. **Token rotation**: deferred to M9+ per spec assumptions; M8 ships create-once-and-keep semantics.
8. **MCPJungle health check**: degrade-with-warning per FR-308.
9. **McpClient lifecycle**: persist across pause/resume (FR-311); deletion ties to agent deletion (no agent-delete verb in M8 — operator-manual cleanup).
10. **Customer-slug primitive**: `companies.customer_slug TEXT UNIQUE NOT NULL DEFAULT 'garrison'` (FR-303); alpha row auto-populated via migration default.
11. **Vault-grant shape**: `agent_role_secrets` gains nullable `agent_id` discriminator (FR-403).
12. **Dashboard agent-anchored audit**: minimal in-M8 filter surface on existing `/activity` page (FR-502).
13. **Server Action wiring**: DB-direct write + supervisor reactive pickup (pattern: `mcpserverwork` worker LISTENs on `work.mcp_server.registration_requested`). Admin token stays in supervisor's vault.
14. **FR-005 amendment**: DB-level CHECK → Go-side verb-time validation. M7-era Server Action audit rows have both `chat_session_id` + `chat_message_id` + `agent_instance_id` NULL (operator-driven, no chat anchor); exactly-one-of only applies to `create_ticket` rows.

---

## Subsystem walkthroughs

### 1. `internal/mcpjungle/` — MCPJungle admin-API HTTP client

**Public interface** (`client.go`):

```go
type Client struct {
    BaseURL     string         // GARRISON_MCPJUNGLE_URL
    AdminToken  vault.SecretValue
    HTTP        *http.Client   // 30s default timeout
    Logger      *slog.Logger
}

type CreateMcpClientParams struct {
    Name        string   // <customer_slug>.<role-slug>.<agent-uuid-short>
    AllowList   []string // MCP server names from agents.mcp_servers_jsonb
    AccessToken string   // operator-supplied or auto-generated
}

func (c *Client) CreateMcpClient(ctx context.Context, params CreateMcpClientParams) (mcpClientID string, err error)
func (c *Client) DeleteMcpClient(ctx context.Context, name string) error
func (c *Client) UpdateAllowList(ctx context.Context, name string, allowList []string) error
func (c *Client) RegisterServer(ctx context.Context, spec ServerSpec) (serverID string, err error)
func (c *Client) DeregisterServer(ctx context.Context, name string) error
func (c *Client) HealthCheck(ctx context.Context) error
```

**Error sentinels** (`types.go`):
- `ErrServerNotFound`, `ErrClientNotFound`, `ErrAdminTokenInvalid`, `ErrUnreachable`, `ErrServerRegistrationConflict`.

**Auth shape**: every request carries `Authorization: Bearer <admin-token>` via the `AdminToken` field. Token is a `vault.SecretValue` so the M2.3 `vaultlog` analyzer rejects accidental logging.

**HealthCheck**: 5-second timeout. Returns `nil` on 200; `ErrUnreachable` on connection refused / timeout; `ErrAdminTokenInvalid` on 401. Used by supervisor startup (degrade-with-warning per FR-308).

**Reconciler** (`reconcile.go`):

```go
func ReconcileMcpClients(ctx context.Context, deps ReconcileDeps) (ReconcileReport, error)

type ReconcileDeps struct {
    Client          *Client
    Pool            *pgxpool.Pool
    Queries         *store.Queries
    VaultClient     vault.Fetcher
    CustomerSlug    string  // M8 alpha: "garrison"
    Logger          *slog.Logger
}

type ReconcileReport struct {
    Created   []pgtype.UUID  // agent_ids that gained an McpClient
    Existing  []pgtype.UUID  // already had McpClient
    Failed    []ReconcileFailure
}
```

**Reconciler algorithm**:
1. Query `agents WHERE status='active'`.
2. For each, derive expected McpClient name `<customer_slug>.<role-slug>.<agent-uuid-short>`.
3. Try `CreateMcpClient` — MCPJungle's existing-name path returns 409 conflict; treat as "already exists, skip."
4. Generate a per-agent bearer token (cryptographically random) → write to vault at `mcpjungle/agents/<agent-id>` + INSERT `agent_role_secrets` row with `agent_id=<id>, role_slug='', secret_path='mcpjungle/agents/<id>'`.
5. Build AllowList from `agents.mcp_servers_jsonb`; call `UpdateAllowList`.
6. Append M8 `agent_install_journal` rows: `mcpjungle_client_create` + `mcpjungle_allowlist_apply` (both `outcome='success'` or `'failed'`).

Idempotent — runs at supervisor startup after `migrate7.Run`. Pattern matches M7's grandfathering reconciler.

**Test plan**:
- `client_test.go::TestCreateMcpClientHappyPath` — httptest.Server fakes MCPJungle 201 response; client returns the id.
- `client_test.go::TestCreateMcpClientConflictIsNotError` — 409 maps to "already exists, ok" (not an error sentinel).
- `client_test.go::TestRegisterServerRollsBackOnConflict` — 409 conflict surfaces `ErrServerRegistrationConflict`.
- `client_test.go::TestHealthCheckSurfacesUnreachable` — connection-refused on httptest URL → `ErrUnreachable`.
- `client_test.go::TestAdminTokenInjectedAsBearer` — request inspector verifies the Authorization header.
- `reconcile_test.go::TestReconcileCreatesForMissingAgents` — three agents, two missing McpClients, one existing; reconciler creates the two + skips the one.
- `reconcile_test.go::TestReconcileIsIdempotent` — second invocation produces zero `Created` entries.

### 2. `internal/mcpserverwork/` — reactive Server Action pickup worker

**Public interface** (`worker.go`):

```go
type Worker struct {
    Pool      *pgxpool.Pool
    Queries   *store.Queries
    Client    *mcpjungle.Client
    Logger    *slog.Logger
}

func (w *Worker) Run(ctx context.Context) error  // errgroup-managed
```

**Lifecycle**: `Run` subscribes via the M1 `events.Listener` to channel `work.mcp_server.registration_requested`. On each event:
1. Parse payload (`{mcp_server_id, customer_slug, name, transport, url, bearer_token_path}`).
2. Fetch bearer token (if present) from vault.
3. Call `Client.RegisterServer(ctx, spec)`.
4. UPDATE `mcp_servers SET status='registered', registered_at=NOW()` on success; `status='failed', failure_reason=<err>` on error.
5. INSERT follow-up `chat_mutation_audit` row with `verb='register_mcp_server'` + `outcome='success'|'failed'`.

Errgroup-managed alongside the M2.2 hygiene listener, the M5.1 chat worker, etc. SIGTERM → ctx cancel → drain → return.

**State machine** (`statemachine.go`): single transition `pending → registered` or `pending → failed`. No retry on `failed` in M8 — operator re-submits via the dashboard if needed (idempotent: dashboard's `register_mcp_server` Server Action UPDATEs the existing row to `pending` if the name matches).

**Test plan**:
- `worker_test.go::TestWorkerPicksUpRegistrationEvent` — seed an `mcp_servers` row with status='pending'; emit pg_notify; assert the worker calls MCPJungle's RegisterServer + UPDATEs the row.
- `worker_test.go::TestWorkerFailedRegistrationWritesFailureRow` — fake MCPJungle returns 409; worker UPDATEs `status='failed'` + writes audit row.
- `worker_test.go::TestWorkerHonoursCtxCancel` — cancel context mid-call; worker exits with ctx.Err.

### 3. `internal/garrisonmutate/` — caller surface + register_mcp_server verb

**`verbs_tickets.go` extension**:

```go
// Existing: type Deps struct { Pool, ChatSessionID, ChatMessageID, Logger }
// M8 extension:
type Deps struct {
    Pool             *pgxpool.Pool
    ChatSessionID    pgtype.UUID  // chat-CEO caller anchor
    ChatMessageID    pgtype.UUID
    AgentInstanceID  pgtype.UUID  // NEW: agent caller anchor
    Logger           *slog.Logger
}

// New helper:
func assertExactlyOneCallerAnchor(deps Deps) error {
    chatValid := deps.ChatSessionID.Valid
    agentValid := deps.AgentInstanceID.Valid
    if chatValid == agentValid {
        return fmt.Errorf("garrisonmutate: exactly one of ChatSessionID or AgentInstanceID must be set")
    }
    return nil
}
```

**`realCreateTicketHandler` mutations**:
- At the top: call `assertExactlyOneCallerAnchor(deps)`; reject on violation with `Result{ErrorKind: "internal", Success: false}` (this is supervisor wiring failure, not caller-input failure).
- After resolving the target `department_id`: call the runaway gate (`throttle.CheckDeptWeekly(ctx, q, deptID)`); reject with `dept_weekly_ticket_budget_exceeded` if the gate fires.
- After verb-level field validation passes: if `deps.AgentInstanceID.Valid && args.ParentTicketID == ""`, resolve `agent_instances.ticket_id` for the agent instance + set `args.ParentTicketID` to that value (FR-006 auto-inherit).
- Dependency cycle check: if `args.DependsOnTicketID` is non-empty, run the O(N) walker (`walkDependencyChain(ctx, q, args.DependsOnTicketID, depthCap=32)`); reject on cycle or depth-cap exceeded.
- INSERT the ticket row with `parent_ticket_id` + `depends_on_ticket_id` populated.
- Write the audit row with `agent_instance_id=deps.AgentInstanceID` (NULL if chat-CEO).

**New file `server_action_verbs.go`**:

```go
// ServerActionVerbs is the separate registry for Server-Action-only verbs
// per M7's F3 lean. These verbs DO NOT appear in the chat-side `Verbs` slice;
// the M5.3 sealed verb test asserts disjointness.
var ServerActionVerbs = []Verb{
    {
        Name: "register_mcp_server",
        Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
            return handleRegisterMcpServer(ctx, deps, args)
        },
        ReversibilityClass:   2,  // operator can deregister
        AffectedResourceType: "mcp_server",
        Description:          "Server-Action-only. Register a new MCP server in MCPJungle + Garrison's mcp_servers table.",
    },
}
```

**`register_mcp_server.go` handler**:
- Validates `name` starts with `<active customer_slug>.` (reject `validation_failed` otherwise — FR-307).
- Validates `transport` is one of `http`, `stdio`, `sse`.
- Inserts `mcp_servers` row with `status='pending'`.
- Returns immediately; the actual MCPJungle API call happens in the `mcpserverwork` worker post-commit (pg_notify trigger).
- Audit row records the registration request.

**Test plan**:
- `verbs_tickets_test.go::TestCreateTicketAgentCallerSucceeds` — `Deps{AgentInstanceID: <valid>}` lands a row with `agent_instance_id` populated.
- `verbs_tickets_test.go::TestCreateTicketAgentCallerAutoInheritsParent` — agent omits `parent_ticket_id`; resolved row inherits the agent's `agent_instances.ticket_id`.
- `verbs_tickets_test.go::TestCreateTicketAgentExplicitParentOverridesAutoInherit` — agent supplies explicit parent; auto-inherit skipped.
- `verbs_tickets_test.go::TestCreateTicketBothAnchorsRejects` — supervisor wiring bug; both `ChatSessionID` + `AgentInstanceID` set → rejection.
- `verbs_tickets_test.go::TestCreateTicketDependencyCycleRejects` — A→B→C, attempt to add C→A; rejected with `dependency_cycle`.
- `verbs_tickets_test.go::TestCreateTicketDependencyChainTooDeep` — 33-hop chain; rejected with `dependency_chain_too_deep`.
- `verbs_tickets_test.go::TestCreateTicketDeptWeeklyBudgetExceeded` — fake clock + 50 prior tickets; 51st rejects.
- `verbs_tickets_test.go::TestCreateTicketCrossDeptGateScopesAgainstTarget` — caller is engineering, target is marketing; marketing's budget binds.
- `register_mcp_server_test.go::TestRegisterRequiresCustomerPrefix` — name `twilio-sms` rejects; name `garrison.twilio-sms` accepts.
- `register_mcp_server_test.go::TestRegisterWritesPendingRow` — happy path; row's `status='pending'` + `mcp_server_id` returned.
- `register_mcp_server_test.go::TestRegisterAuditRowVerb` — audit row has `verb='register_mcp_server'`.

### 4. `internal/throttle/dept_weekly.go` — per-department runaway gate

**Public interface**:

```go
// CheckDeptWeekly returns nil if the create is allowed; ErrDeptWeeklyBudgetExceeded
// (or a wrapped flavor) if the rolling-7-day count + 1 would exceed the budget.
// NULL budget = unlimited (M8 alpha default).
func CheckDeptWeekly(ctx context.Context, q TxLike, deptID pgtype.UUID) (Decision, error)

type Decision struct {
    Allowed       bool
    CurrentCount  int64
    Budget        *int64  // NULL = unlimited
}
```

**Implementation**:
- One sqlc query `GetDeptWeeklyState`: lateral subquery returning `(SELECT COUNT(*) FROM tickets WHERE department_id = $1 AND created_at > NOW() - INTERVAL '7 days')` + `departments.weekly_ticket_budget`.
- If `budget IS NULL` → return `Decision{Allowed: true, Budget: nil}`.
- If `count + 1 > budget` → return `Decision{Allowed: false}` + caller writes the `throttle_events` row + the audit row.

**Composition with M6's company gate**: `throttle.Check` is amended — runs the M6 company gate first; if allowed, runs the dept-weekly gate. Both must allow; either rejection short-circuits.

**Test plan**:
- `dept_weekly_test.go::TestGateBlocksAt51` — 50 tickets in window, budget=50 → 51st rejects.
- `dept_weekly_test.go::TestGateAllowsAt50WhenBudget100` — 50 in window, budget=100 → allows.
- `dept_weekly_test.go::TestGateNullBudgetUnlimited` — 1000 in window, budget=NULL → allows.
- `dept_weekly_test.go::TestGateRollingWindowExpiry` — fake clock + tickets at varying ages; only those within 7 days count.
- `throttle_test.go::TestCheckComposesCompanyAndDept` — company gate fires first; dept gate not invoked.
- `throttle_test.go::TestCheckSurvivesNullCompanyBudgetWithDeptBudget` — company budget NULL, dept budget set → dept gate is the binding constraint.

### 5. `internal/spawn/` — dependency gate + MCPJungle env injection

**`spawn.go` extensions**:
- `prepareSpawn` adds a "dependency satisfied" check after the existing concurrency-cap check. For each pending ticket with `depends_on_ticket_id IS NOT NULL`, query the predecessor's `column_slug` against the predecessor's department's `dependency_satisfaction_columns`. If unsatisfied, the prep returns `spawnPrep{done: true}` (defer like the M6 throttle defer) without rolling back.
- The transition listener gains a callback: every `work.ticket.transitioned.*` event triggers `events.HandleDependencyUnblock(ctx, q, transitioning_ticket_id)` which queries `tickets WHERE depends_on_ticket_id = <transitioning_id> AND column_slug='todo'` and re-enqueues a synthetic `work.ticket.created.*` event for each. Dedupe in the dispatcher handles repeat events.

**`m8.go` new file**:

```go
// EnsureMcpjungleTokenForAgent fetches the agent's MCPJungle bearer token from
// vault (path mcpjungle/agents/<agent-id>) and returns it as an env var for the
// spawn's container env. Called from runRealClaudeViaContainer at spawn time
// per FR-309.
func EnsureMcpjungleTokenForAgent(ctx context.Context, deps Deps, agentID pgtype.UUID) (envVar string, err error)
```

**Per-agent container's `--mcp-config`** gains:
```json
{
    "mcpjungle": {
        "command": "...",  // claude's mcp invocation; via env-var-resolved URL
        "headers": {"Authorization": "Bearer ${MCPJUNGLE_BEARER_TOKEN}"}
    }
}
```

`MCPJUNGLE_BEARER_TOKEN` env var is injected via the vault fetcher per M2.3 Rule 1; not visible in agent's prompt context.

**Test plan**:
- `spawn_test.go::TestSpawnPrepBlocksOnUnsatisfiedDependency` — seed predecessor in `in_dev`, dependent in `todo`; `prepareSpawn` returns done without spawning.
- `spawn_test.go::TestSpawnPrepUnblocksAfterPredecessorTransition` — same setup, then transition predecessor to `qa_review`; second prepareSpawn call now spawns.
- `m8_test.go::TestEnsureMcpjungleTokenFetchesFromVault` — fake vault returns the token; env-var injection assertion.
- `m8_test.go::TestEnsureMcpjungleTokenSurfacesUnreachable` — vault returns ErrUnreachable; spawn fails with `mcpjungle_unreachable` exit reason.

### 6. `internal/vault/` — agent-scoped grant resolution

**`grants.go` extension**: existing query `ListGrantsForRole(roleSlug, customerID)` becomes `ListGrantsForRoleAndAgent(roleSlug, customerID, agentID)`. Resolves:
- Role-scoped grants: `role_slug=<role>` AND `agent_id IS NULL`.
- Agent-scoped grants: `agent_id=<agent>` (regardless of role_slug — agent scoping is strictly tighter).

Both shapes return a unified grant list; the fetcher fetches all listed paths.

**Test plan**:
- `grants_test.go::TestListGrantsRoleScopedOnly` — only role rows; agent-scope rows for a different agent are not returned.
- `grants_test.go::TestListGrantsAgentScopedReturned` — agent row for the queried agent is returned.
- `grants_test.go::TestListGrantsBothShapes` — agent has both role-scoped (from M2.x seeds) + agent-scoped (M8 MCPJungle token) grants; both returned.

### 7. Migration `20260510000000_m8_zero_human_loop.sql`

**+goose Up** sections:

1. **`chat_mutation_audit` extensions**:
   - `ADD COLUMN agent_instance_id UUID NULL REFERENCES agent_instances(id) ON DELETE SET NULL`.
   - DROP+ADD verb-CHECK constraint to include `register_mcp_server`.
   - DROP+ADD outcome-CHECK constraint to include `dept_weekly_ticket_budget_exceeded`.

2. **`tickets` extensions**:
   - `ADD COLUMN depends_on_ticket_id UUID NULL REFERENCES tickets(id) ON DELETE SET NULL`.
   - `ADD CONSTRAINT depends_on_not_self CHECK (depends_on_ticket_id <> id OR depends_on_ticket_id IS NULL)`.
   - `CREATE INDEX idx_tickets_depends_on ON tickets(depends_on_ticket_id) WHERE depends_on_ticket_id IS NOT NULL`.

3. **`departments` extensions**:
   - `ADD COLUMN dependency_satisfaction_columns JSONB NULL DEFAULT '["qa_review", "done"]'::jsonb`.
   - `ADD COLUMN weekly_ticket_budget INT NULL`.

4. **`throttle_events.kind` CHECK extension**: add `dept_weekly_ticket_budget_exceeded`.

5. **`companies.customer_slug`**:
   - `ADD COLUMN customer_slug TEXT NOT NULL DEFAULT 'garrison'`.
   - `ADD CONSTRAINT companies_customer_slug_unique UNIQUE (customer_slug)`.

6. **`agent_role_secrets.agent_id`**:
   - `ADD COLUMN agent_id UUID NULL REFERENCES agents(id) ON DELETE CASCADE`.
   - `CREATE INDEX idx_agent_role_secrets_agent_id ON agent_role_secrets(agent_id) WHERE agent_id IS NOT NULL`.

7. **New `mcp_servers` table**:
   ```sql
   CREATE TABLE mcp_servers (
       id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
       customer_slug TEXT NOT NULL REFERENCES companies(customer_slug),
       name TEXT NOT NULL,
       transport TEXT NOT NULL CHECK (transport IN ('http', 'stdio', 'sse')),
       url TEXT,
       bearer_token_path TEXT,  -- vault path if upstream needs auth
       status TEXT NOT NULL DEFAULT 'pending'
           CHECK (status IN ('pending', 'registered', 'failed', 'deregistered')),
       failure_reason TEXT,
       registered_by UUID NULL,  -- operator user_id; M8 audits via chat_mutation_audit
       registered_at TIMESTAMPTZ,
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       UNIQUE (customer_slug, name)
   );
   CREATE INDEX idx_mcp_servers_status_pending ON mcp_servers(created_at)
       WHERE status = 'pending';
   GRANT SELECT ON mcp_servers TO garrison_dashboard_app;
   GRANT INSERT, UPDATE ON mcp_servers TO garrison_dashboard_app;
   ```

8. **`mcp_servers` INSERT trigger**: `emit_mcp_server_registration_request` fires `pg_notify('work.mcp_server.registration_requested', payload)` post-INSERT for any row with `status='pending'`. Reuses the M1 trigger pattern.

9. **Seed update**: M2.x departments (engineering + qa-engineer) auto-pick up `dependency_satisfaction_columns=DEFAULT, weekly_ticket_budget=NULL`. No `UPDATE` needed — `ADD COLUMN ... DEFAULT` populates existing rows.

**+goose Down**: clean reversal of all the above, plus DELETE `mcp_servers` rows (`mcp_servers.customer_slug` FK to `companies.customer_slug` cascades on `customer_slug` DROP, but we drop the column last). Audit-row CHECK constraints restore to their M7-era CHECK list.

### 8. sqlc queries

**`migrations/queries/m8_tickets.sql`**:
- `InsertTicketM8` — extends `InsertTicket` with `parent_ticket_id` + `depends_on_ticket_id` named-arg params.
- `WalkDependencyChain` — recursive CTE for cycle detection (used at verb-time, not in tx).
- `ListBlockedDependents` — `SELECT id FROM tickets WHERE depends_on_ticket_id = $1 AND column_slug = 'todo'`.

**`migrations/queries/m8_throttle.sql`**:
- `GetDeptWeeklyState` — lateral subquery returning `(current_count, weekly_ticket_budget)` for a department.

**`migrations/queries/m8_audit.sql`**:
- `InsertAgentAnchoredAudit` — variant of `InsertChatMutationAudit` accepting `agent_instance_id`.
- `ListAuditByAgentInstance` — read-side for FR-502.

**`migrations/queries/m8_mcp_servers.sql`**:
- `InsertMcpServer` — Server Action write path.
- `UpdateMcpServerStatus` — worker write path.
- `ListMcpServers` — dashboard read path.
- `GetMcpServerByID` — detail-page read path.
- `ListMcpServersByCustomer` — beta-time multi-tenant read path (M8 alpha returns all).

**`migrations/queries/m8_vault.sql`**:
- `ListGrantsForRoleAndAgent` — extends the M2.3 grant lookup.

### 9. Dashboard `/admin/mcp-servers` surface

**`page.tsx`**: lists registered MCP servers + a register form. Reads via Drizzle. The form's Server Action (`registerMcpServerAction`) writes the `mcp_servers` row with `status='pending'` + audit row; the trigger fires the pg_notify; the worker picks it up.

**`[id]/page.tsx`**: per-server detail. Shows status, registered_at, failure_reason (if any), list of agents whose AllowList includes this server (cross-reference from `agents.mcp_servers_jsonb`).

**`lib/actions/mcpServer.ts`**: Server Action wraps the two-write tx (mcp_servers + audit) inside one Drizzle transaction. No HTTP call to the supervisor — the reactive worker handles MCPJungle.

**Per AGENTS.md memory `feedback_test_scope_go_only`**: no vitest tests for the dashboard. Go-side `register_mcp_server_test.go` + `worker_test.go` cover the data shape.

### 10. `internal/config/` — env-var extensions

```go
type Config struct {
    // ... existing fields ...
    MCPJungleURL             string  // GARRISON_MCPJUNGLE_URL
    MCPJungleAdminTokenPath  string  // GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH (default "mcpjungle/admin")
}
```

`Load` populates from env vars. `Validate` requires `MCPJungleURL` non-empty when running in non-fake-agent mode (degrade-with-warning means the supervisor STARTS even when MCPJungle is unreachable, but the URL config must be present so the supervisor knows where to try).

**Test plan**: `config_test.go::TestParseMCPJungleEnvVars` covers default + override paths.

### 11. `internal/events/` — listener extensions

Two new channels wired into the M1 listener:

- `work.mcp_server.registration_requested` → handler dispatches to `mcpserverwork.Worker`.
- For dependency unblock: the existing `work.ticket.transitioned.<dept>.<from>.<to>` parameterised listener gains a callback registration. Pattern mirrors M2.2's transition-driven hygiene re-evaluation.

**Test plan**:
- `listener_test.go::TestRegistrationRequestListenerDispatches` — emit the event; assert the worker's input channel sees it.
- `listener_test.go::TestDependencyUnblockOnTransition` — emit a transition event; assert the dependency-unblock handler runs.

### 12. `supervisor/docker-compose.yml` extensions

Two new services on `garrison-net`:

```yaml
  garrison-mcpjungle-postgres:
    image: postgres:17
    environment:
      POSTGRES_DB: mcpjungle
      POSTGRES_USER: mcpjungle
      POSTGRES_PASSWORD: ${GARRISON_MCPJUNGLE_PG_PASSWORD:?GARRISON_MCPJUNGLE_PG_PASSWORD required}
    volumes:
      - mcpjungle-pg-data:/var/lib/postgresql/data
    restart: unless-stopped

  garrison-mcpjungle:
    image: ${GARRISON_MCPJUNGLE_IMAGE:-mcpjungle/mcpjungle@sha256:<digest-pinned-at-release>}
    depends_on:
      garrison-mcpjungle-postgres:
        condition: service_started
    environment:
      SERVER_MODE: enterprise
      MCPJUNGLE_DB_URL: postgres://mcpjungle:${GARRISON_MCPJUNGLE_PG_PASSWORD}@garrison-mcpjungle-postgres:5432/mcpjungle
    restart: unless-stopped

volumes:
  mcpjungle-pg-data:
```

Operator runs `mcpjungle init-server` once at deploy time; the admin token gets stored at vault path `mcpjungle/admin`. `scripts/dev-stack-up.sh` automates this for the dev stack.

### 13. ARCHITECTURE.md amendment (post-ship)

M8 paragraph already updated at context-doc time (leading-candidate → chosen). At ship time the paragraph gets the `✅ Shipped YYYY-MM-DD` annotation + retro link, mirroring M5/M6/M7 patterns. The dashboard's `tests/architecture-amendment.test.ts` gains three new substring assertions: shipped annotation, MCPJungle license reference, customer-slug primitive reference.

---

## Lifecycle + state machines

### Per-agent McpClient lifecycle

```
[new agent activated]
        │ (M7 ApproveHire OR M7 migrate7 OR M8 reconciler)
        ▼
[McpClient created in MCPJungle]  ← bearer token written to vault at mcpjungle/agents/<id>
        │
        ├─[pause_agent]──────► [agents.status='paused']  ← McpClient untouched; spawn-prep blocks new spawns
        │                              │
        │                              ▼
        │                       [resume_agent]
        │                              │
        │                              ▼
        ◄──── (no McpClient mutations) ┘
        │
        ▼
[agent deletion]  ← operator-manual; cascades the agent_role_secrets row; supervisor reconciler deletes MCPJungle McpClient
```

### MCP server registration state machine (per registration request)

```
[Server Action writes mcp_servers.status='pending' + audit] ──► [pg_notify(registration_requested)]
                                                                          │
                                                                          ▼
                                                                  [mcpserverwork worker picks up]
                                                                          │
                                                                          ▼
                                                         ┌────────────[MCPJungle API call]────────────┐
                                                         │                                            │
                                                         ▼ (success)                                  ▼ (failure)
                                          [UPDATE status='registered'                       [UPDATE status='failed',
                                           + registered_at=NOW()                              failure_reason=<err>]
                                           + audit row outcome='success']                    + audit row outcome='failed']
```

### Dependency unblock event flow

```
[predecessor transitions: column_slug X → Y]
                  │
                  ▼
[pg_notify(work.ticket.transitioned.<dept>.X.Y)]
                  │
                  ▼
[dispatcher's transition listener]
                  │
                  ├─► [existing M5.3 hygiene handler]
                  │
                  └─► [NEW M8 dependency-unblock handler]
                              │
                              ▼
                       [SELECT tickets WHERE depends_on_ticket_id = <transitioning_id> AND column_slug='todo']
                              │
                              ▼
                       [for each blocked dependent: emit synthetic work.ticket.created.<dept>.todo]
                              │
                              ▼
                       [dispatcher's existing dedupe handles double-fire]
                              │
                              ▼
                       [prepareSpawn re-checks; gate now clears; spawn proceeds]
```

### Supervisor restart reconciliation

```
[supervisor boots]
   │
   ├─► migrate7.Run (M7) — grandfather any M2.x agent without a container
   │
   ├─► mcpjungle.ReconcileMcpClients (NEW) — ensure every active agent has an McpClient + vault grant
   │
   ├─► mcpjungle.Client.HealthCheck (NEW) — log warning if unreachable; continue boot
   │
   ├─► events.Listener.Run — subscribe to all M1 + M5.3 + M8 channels
   │
   ├─► mcpserverwork.Worker.Run — start the reactive registration worker
   │
   └─► (existing M2.2 hygiene, M5.1 chat, M6 throttle, M7 actuator subsystems)
```

---

## Test strategy

### Unit tests (Go, default tag)

Per-package unit tests covering the behaviour without DB or Docker, listed in the subsystem walkthroughs above. Pattern matches M7's discipline.

### Integration tests (Go, `-tags=integration`)

Each user story gets at least one dedicated integration test against a real Postgres testcontainer + (where applicable) the per-agent container or a fake MCPJungle backend:

- `supervisor/m8_golden_path_integration_test.go::TestM8AgentSpawnsFollowUpTicket` — US1 end-to-end. Engineer agent's spawn calls `create_ticket`; verify the row + audit row + auto-inherited parent.
- `supervisor/m8_dependency_integration_test.go::TestM8CrossDeptDependencyBlocksAndUnblocks` — US2. Two tickets; dependent blocks until predecessor transitions; dispatcher re-enqueues.
- `supervisor/m8_dependency_integration_test.go::TestM8DependencyCycleRejects` — US2 cycle case.
- `supervisor/m8_runaway_integration_test.go::TestM8DeptWeeklyBudgetFires` — US3. Seed 50 tickets, attempt 51st, assert rejection + throttle_events row.
- `supervisor/m8_mcpjungle_integration_test.go::TestM8RegisterServerEndToEnd` — US5. Server Action writes pending row; fake MCPJungle accepts; worker UPDATEs status to 'registered'.
- `supervisor/m8_mcpjungle_integration_test.go::TestM8AgentReachesRegisteredServer` — US4. Agent's spawn calls `mcp__mcpjungle__<server>-<tool>`; fake MCPJungle routes; response returns.
- `supervisor/m8_mcpjungle_integration_test.go::TestM8AllowListRejects` — US4 AS2. Agent attempts a server not in AllowList; 403 surfaces.
- `supervisor/m8_audit_surface_integration_test.go::TestM8AgentAnchoredAuditFilters` — SC-014. Seed mixed chat-anchored + agent-anchored audit rows; filter by `agent_instance_id` returns the right subset.

### Chaos tests (Go, `-tags=chaos`)

- `supervisor/chaos_m8_test.go::TestMCPJungleDownAtSupervisorStartup` — fake MCPJungle's health endpoint returns connection-refused; supervisor still boots with warning; per-spawn MCPJungle calls fail individually.
- `supervisor/chaos_m8_test.go::TestSimultaneousCreateTicketCallersRaceOnBudget` — two concurrent callers at budget-49; assert exactly one succeeds (50th allowed) + exactly one rejects (51st blocked).
- `supervisor/chaos_m8_test.go::TestRegistrationWorkerSurvivesMcpjungleFlap` — start registration; MCPJungle becomes unreachable mid-call; worker marks `status='failed'`; operator re-submits; worker re-attempts.

### Regression check

The M2.x + M3 + M4 + M5.x + M6 + M7 test suites run unchanged under `-tags=integration` + `-tags=chaos`. M8's CI matrix asserts no regression. Specifically: M7's `TestM7GoldenPathHireProposeAndApprove` + `TestM7MigrationGrandfathersM2xAgentsAndIsIdempotent` + M5.x chat integration tests must pass post-M8.

### SonarCloud quality gate

≥82% coverage on new code (matches M5.4 / M6 / M7 thresholds); 0 new Sonar issues, 0 security hotspots (the M7 PR's first-pass approach: query the issues API after the analysis lands + clear every issue before declaring done). Particular attention to:
- shell:S5332 (clear-text protocols): MCPJungle URL is HTTP-scheme by design (internal-network only). Same pattern as M7's socket-proxy-policy-test.sh fix — drop `http://` literals from source; require fully-qualified URL from env.
- go:S3776 (cognitive complexity): pre-split helper functions in `verbs_tickets.go` extension + `dept_weekly.go` per the M7 retro's pattern.

---

## Deployment changes

- **`supervisor/docker-compose.yml`**: two new services (`garrison-mcpjungle`, `garrison-mcpjungle-postgres`), one new named volume (`mcpjungle-pg-data`).
- **`scripts/dev-stack-up.sh`**: provisions the two new services + runs `mcpjungle init-server` to create the admin user + stores the token in the dev Infisical instance.
- **`docs/ops-checklist.md`**: M8 section added describing the post-deploy admin-token rotation step, the MCPJungle Postgres backup expectation, the digest-pinning convention for the MCPJungle image.
- **No supervisor Dockerfile changes**.
- **No new dashboard dependencies**.

---

## Open questions remaining for `/garrison-tasks`

Plan-level decisions still pending operator preference but acceptable to defer to tasks phase:

1. **MCPJungle image digest** — needs the operator's chosen digest at deploy time. Tasks phase commits the pin.
2. **`mcpjungle init-server` automation in dev-stack-up.sh** — does the script auto-init on first run, or expect the operator to run it manually? Lean: auto-init for dev stack; production keeps it manual (operator approval for any vault-token write).
3. **dashboard `/admin/mcp-servers` UX polish** — pagination, sorting, search. Tasks phase decides whether these land in M8 or M8.1.
4. **MCPJungle's `Tool Groups` feature usage** — the spike noted this lets the operator slice the tool surface for a client. M8 doesn't use it (per-server AllowList is sufficient); leaving Tool Groups for M9+ when finer-grained per-tool scoping becomes load-bearing.

---

## What this plan does not pre-decide

- Task ordering within the M8 PR (that's `/garrison-tasks`).
- Exact dashboard component styling (that follows M3+ design-system precedent; not plan territory).
- M9 forward-compatibility shape (no heartbeats / scheduled-wake-ups work here; one-line forward mentions only).
- Whether/when to amend `docs/mcp-registry-candidates.md` further (the spike-driven amendments already landed; further updates only if MCPJungle's signals degrade).

---

## Spec-kit flow next

1. **`/garrison-tasks m8`** — break this plan into ordered tasks, each as a single Claude Code session with a verifiable completion condition. Expect ~20–24 tasks (mirrors M7's count; four threads of comparable complexity).
2. **`/speckit.analyze`** — cross-artefact consistency check before implementing.
3. **`/garrison-implement m8`** — execute the tasks.
4. **Retro** — `docs/retros/m8.md` + MemPalace `wing_company / hall_events` drawer mirror per M3+ dual-deliverable policy.
