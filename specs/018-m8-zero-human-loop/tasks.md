# M8 tasks — Agent-spawned tickets, cross-department dependencies, runaway control, MCP-server registry

**Branch**: `018-m8-zero-human-loop` | **Plan**: [plan.md](./plan.md) | **Spec**: [spec.md](./spec.md) | **Context**: [m8-context.md](../_context/m8-context.md) | **Spike**: [m8-mcpjungle-spike.md](../../docs/research/m8-mcpjungle-spike.md) | **M7 retro**: [m7.md](../../docs/retros/m7.md) | **M6 retro**: [m6.md](../../docs/retros/m6.md)

24 tasks total (T001–T023 plus the inserted T014a for FR-501's `/hygiene` runaway-control surface that the /speckit.analyze pass surfaced as a coverage gap). Executed linearly by a solo operator. Each task is one Claude Code session in scope and produces a reviewable commit. The repo is in a working state after every task. Count matches M7's four-thread merge shape exactly.

Risk callouts carried forward from the M7 retro (woven into specific tasks below):
- **Drizzle:pull empty-default mangling** (M7 gotcha: `default("")` → `default(')` for empty-string-default columns). T001 includes an explicit typecheck step + inline TS-fix if it fires.
- **sqlc `$N::cast` → ColumnN naming**. T001 uses `sqlc.arg(name)` exclusively in the new query files.
- **chat_mutation_audit nullability** — already resolved at M7 (both anchor columns nullable post-M7 migration). T007 asserts the agent-only audit row commits cleanly so future drift surfaces.
- **CI Sonar pre-clearance**. T022 acceptance script queries Sonar's new-issues API + clears every issue before declaring done (avoids the M5.4 / M7 multi-PR Sonar fix loops).
- **Lint locally before push**. T023 retro notes whether any tasks pushed lint-failing code (M5.4 / M7 pattern: gofmt + `go vet` from `supervisor/` before every push).

Zero new Go dependencies. Zero new TypeScript dependencies. Locked-deps streak continues past M7 → M8.

---

## Ordering principle

T001 lands the consolidated migration + sqlc query files + drizzle pull. Every later task references the new schema (depends_on_ticket_id, agent_instance_id on audit, customer_slug, agent_id on agent_role_secrets, mcp_servers table, weekly_ticket_budget, dependency_satisfaction_columns).

T002–T004 stand up the three small foundation packages with no inter-dependency: `config` (env vars), `vault` (grant extension), `throttle/dept_weekly` (gate). Order between them is arbitrary; sequenced below for forward-dependency convenience.

T005–T006 build the MCPJungle integration plumbing: the HTTP client + reconciler land first (T005), then the reactive worker (T006) consumes the client.

T007 + T008 wire the garrisonmutate verb extensions: `create_ticket` caller-surface extension + cycle detection + auto-inherit (T007), then the `register_mcp_server` Server-Action-only verb (T008). T007 depends on T004 (the dept-weekly gate it hooks into).

T009 + T010 extend `internal/spawn`: dependency gate at prepareSpawn (T009), MCPJungle env-var injection at container-start (T010). T010 depends on T002 (config), T003 (vault grant), T005 (mcpjungle client).

T011 wires the event listener for `registration_requested` + the dependency-unblock callback at the transition listener seam, plus the main.go boot order. T012 is the deployment delta (docker-compose + dev-stack-up.sh extension).

T013 + T014 + T014a ship the dashboard surfaces: `/admin/mcp-servers` (T013) backed by the Server Action; `/activity` agent-anchored audit filter (T014); `/hygiene` runaway-control surface (T014a) closing FR-501.

T015–T019 are the integration test suite — golden path (T015), dependency block + unblock + cycle (T016), runaway gate (T017), MCPJungle register + agent reaches + AllowList rejects (T018), audit-surface filter (T019). T020 is the chaos-test extension.

T021 lands the ARCHITECTURE.md amendment + the dashboard's substring-pin test. T022 walks the spec's 14 success criteria as a scripted check. T023 ships the retro per the AGENTS.md dual-deliverable rule.

---

## Phase 1 — Foundations

- [ ] **T001** Goose migration `20260510000000_m8_zero_human_loop.sql` + sqlc query files + drizzle pull
  - **Depends on**: M7 shipped (PR #20 merged to main) + branch `018-m8-zero-human-loop` carries the spec/plan/context.
  - **Files**: `migrations/20260510000000_m8_zero_human_loop.sql` (new); `migrations/queries/m8_tickets.sql`, `m8_throttle.sql`, `m8_audit.sql`, `m8_mcp_servers.sql`, `m8_vault.sql` (all new); `supervisor/sqlc.yaml` (extend with the new migration entry); `supervisor/internal/store/m8_tickets.sql.go`, `m8_throttle.sql.go`, `m8_audit.sql.go`, `m8_mcp_servers.sql.go`, `m8_vault.sql.go` (sqlc-generated); `supervisor/internal/store/models.go` (regenerated to include the new `mcp_servers` table + extended columns); `dashboard/drizzle/schema.supervisor.ts` (regenerated via `bun run drizzle:pull`).
  - **Completion condition**: Migration applies cleanly via `goose up` against a fresh testcontainer Postgres; `goose down` reverses cleanly. Up section covers all plan §8 schema deltas: `chat_mutation_audit` += `agent_instance_id` + verb-CHECK extension (`register_mcp_server`) + outcome-CHECK extension (`dept_weekly_ticket_budget_exceeded`); `tickets` += `depends_on_ticket_id` + self-reference CHECK + partial index; `departments` += `dependency_satisfaction_columns` (DEFAULT `["qa_review","done"]`) + `weekly_ticket_budget`; `throttle_events.kind` CHECK += `dept_weekly_ticket_budget_exceeded`; `companies` += `customer_slug` (DEFAULT `'garrison'`, UNIQUE); `agent_role_secrets` += `agent_id UUID NULL REFERENCES agents(id) ON DELETE CASCADE` + partial index on non-NULL; `agent_install_journal.step` CHECK += `mcpjungle_client_create` + `mcpjungle_allowlist_apply` (T001 is authoritative for this; T010 references but does NOT extend); new `mcp_servers` table + `emit_mcp_server_registration_request` INSERT trigger emitting `pg_notify('work.mcp_server.registration_requested', payload)`. All sqlc queries use `sqlc.arg(name)` (NOT `$N::jsonb` etc.) to keep generated field names readable (M7 gotcha). `make sqlc` regenerates without errors. `bun run drizzle:pull` regenerates `schema.supervisor.ts`; immediately run `bunx tsc --noEmit` and inline-fix any `default(').notNull()` mangling on empty-string-default columns (M7 retro gotcha). `go build ./...` succeeds; `bunx tsc --noEmit` from `dashboard/` passes. `internal/store/migrate_integration_test.go::TestM8MigrationRoundtrip` asserts fingerprint stability (apply → rollback → apply).
  - **Out of scope for this task**: any caller code (T002 onward); ARCHITECTURE.md amendment (T021); MCPJungle deployment (T012); dashboard surface (T013/T014).

- [ ] **T002** `internal/config/` — `GARRISON_MCPJUNGLE_URL` + admin token path env vars
  - **Depends on**: T001 (no direct schema dep but foundations land first).
  - **Files**: `supervisor/internal/config/config.go` (extend with `MCPJungleURL`, `MCPJungleAdminTokenPath` fields + Load + Validate); `supervisor/internal/config/config_test.go` (extend).
  - **Completion condition**: `cfg.MCPJungleURL` reads from `GARRISON_MCPJUNGLE_URL`; `cfg.MCPJungleAdminTokenPath` reads from `GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH` (default `mcpjungle/admin`). `Validate` requires `MCPJungleURL` non-empty when `UseFakeAgent=false` (degrade-with-warning means supervisor starts when MCPJungle is unreachable, but the URL must be configured so the supervisor knows where to try). Tests: `TestParseMCPJungleEnvVars` (default + override paths), `TestValidateRequiresMCPJungleURLInRealMode`, `TestAdminTokenPathDefaults`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: actual MCPJungle HTTP client (T005); main.go wiring (T011); supervisor startup-time health check (T011).

- [ ] **T003** `internal/vault/` — `agent_role_secrets.agent_id` discriminator in grant lookup
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/vault/grants.go` (extend `ListGrantsForRole` → `ListGrantsForRoleAndAgent`; resolve agent-scoped grants alongside role-scoped); `supervisor/internal/vault/grants_test.go` (extend).
  - **Completion condition**: `ListGrantsForRoleAndAgent(ctx, q, roleSlug, customerID, agentID)` returns both role-scoped rows (`role_slug=<role> AND agent_id IS NULL`) and agent-scoped rows (`agent_id=<agent>`). Existing M2.3 callers continue to work via a thin wrapper or named-param API (operator picks the migration shape; no caller-side spec break). Tests: `TestListGrantsRoleScopedOnly` (only role rows; agent-scope rows for a different agent are not returned); `TestListGrantsAgentScopedReturned` (agent row for the queried agent is returned); `TestListGrantsBothShapes` (agent has both role + agent grants; both returned). `gofmt -l` + `go vet` clean. M2.3 vault integration tests pass unchanged (regression).
  - **Out of scope for this task**: any caller that writes agent-scoped grants (T005 reconciler does this); MCPJungle bearer token paths (T005).

- [ ] **T004** `internal/throttle/dept_weekly.go` — per-department weekly-budget gate + composition with M6 company gate
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/throttle/dept_weekly.go` (new — `CheckDeptWeekly`, `Decision` shape); `supervisor/internal/throttle/dept_weekly_test.go` (new); `supervisor/internal/throttle/throttle.go` (extend `Check` to compose company gate + dept-weekly gate); `supervisor/internal/throttle/throttle_test.go` (extend composition tests).
  - **Completion condition**: `CheckDeptWeekly(ctx, q, deptID)` returns `Decision{Allowed: bool, CurrentCount: int64, Budget: *int64}`. NULL budget → `Allowed=true` (M8 alpha default). Window is rolling 7d (matches M6's rolling 24h precedent). Tests: `TestGateBlocksAt51` (50 in window, budget=50, 51st rejects); `TestGateAllowsAt50WhenBudget100`; `TestGateNullBudgetUnlimited`; `TestGateRollingWindowExpiry` (fake clock + tickets at varying ages); `TestCheckComposesCompanyAndDept` (company gate fires first; dept gate not invoked); `TestCheckSurvivesNullCompanyBudgetWithDeptBudget`. `gofmt -l` + `go vet` clean. M6 throttle tests pass unchanged (regression).
  - **Out of scope for this task**: `throttle_events` row write (caller writes; T007's verbs_tickets call site owns this); audit-row write (T007); pg_notify on gate fire (T011's listener wiring + verb-side notify).

---

## Phase 2 — MCPJungle plumbing

- [ ] **T005** `internal/mcpjungle/` — admin-API HTTP client + reconciler + healthcheck + URLForCustomer seam
  - **Depends on**: T002, T003.
  - **Files**: `supervisor/internal/mcpjungle/client.go` (new — `Client` struct + `CreateMcpClient`, `DeleteMcpClient`, `UpdateAllowList`, `RegisterServer`, `DeregisterServer`, `URLForCustomer(customerID pgtype.UUID) string` per FR-302); `supervisor/internal/mcpjungle/types.go` (new — request/response shapes + error sentinels `ErrServerNotFound`, `ErrClientNotFound`, `ErrAdminTokenInvalid`, `ErrUnreachable`, `ErrServerRegistrationConflict`); `supervisor/internal/mcpjungle/healthcheck.go` (new — `HealthCheck(ctx)` returning typed errors for unreachable / unauth); `supervisor/internal/mcpjungle/reconcile.go` (new — `ReconcileMcpClients(ctx, deps)` ensuring every `agents.status='active'` row has a McpClient + an agent-scoped vault grant for its bearer token); `supervisor/internal/mcpjungle/client_test.go` (new); `supervisor/internal/mcpjungle/reconcile_test.go` (new).
  - **Completion condition**: `Client.CreateMcpClient(ctx, params)` issues `POST /clients` with `Authorization: Bearer <admin-token>`; returns ID on 201, `ErrServerRegistrationConflict` on 409, `ErrAdminTokenInvalid` on 401, `ErrUnreachable` on connection refused. `Client.RegisterServer` follows the same shape. `Client.URLForCustomer(customerID)` returns `c.BaseURL` unconditionally in M8 alpha (single-instance); the signature is the structural seam for beta-time multi-tenant (Option A — customer-id-keyed map swap-in). `HealthCheck` returns `nil` on 200, typed errors otherwise; 5s default timeout. `ReconcileMcpClients` walks `agents WHERE status='active'`, derives expected McpClient name `<customer_slug>.<role-slug>.<agent-uuid-short>`, calls CreateMcpClient (treating 409 conflict as "already exists"), generates a per-agent bearer token (crypto/rand), writes to vault at `mcpjungle/agents/<agent-id>`, INSERTs the `agent_role_secrets` row with `agent_id=<id>`, builds AllowList from `agents.mcp_servers_jsonb`, calls UpdateAllowList. Reconciler is idempotent — second invocation produces zero `Created` entries. Tests: `TestCreateMcpClientHappyPath`, `TestCreateMcpClientConflictIsNotError`, `TestRegisterServerRollsBackOnConflict`, `TestHealthCheckSurfacesUnreachable`, `TestAdminTokenInjectedAsBearer`, `TestReconcileCreatesForMissingAgents`, `TestReconcileIsIdempotent`, `TestURLForCustomerAlphaReturnsBaseURL` (single customer; lookup returns `BaseURL` regardless of input). `gofmt -l` + `go vet` clean. **No new Go deps** (stdlib `net/http` only).
  - **Out of scope for this task**: the reactive worker (T006); supervisor startup-time wiring (T011); per-spawn token injection (T010); dashboard registration form (T013).

- [ ] **T006** `internal/mcpserverwork/` — reactive worker for `register_mcp_server` Server-Action pickup
  - **Depends on**: T005.
  - **Files**: `supervisor/internal/mcpserverwork/worker.go` (new — `Worker.Run(ctx)` LISTENs on `work.mcp_server.registration_requested`); `supervisor/internal/mcpserverwork/statemachine.go` (new — `pending → registered | failed` transitions); `supervisor/internal/mcpserverwork/worker_test.go` (new).
  - **Completion condition**: `Worker.Run(ctx)` subscribes via `events.Listener` (the M1 listener pattern), picks up `work.mcp_server.registration_requested` events, parses payload (`{mcp_server_id, customer_slug, name, transport, url, bearer_token_path}`), fetches any upstream bearer token from vault, calls `Client.RegisterServer(ctx, spec)`, UPDATEs `mcp_servers.status='registered'+registered_at=NOW()` on success or `status='failed'+failure_reason=<err>` on error, INSERTs a follow-up `chat_mutation_audit` row with `verb='register_mcp_server' + outcome='success'|'failed'`. Worker is errgroup-managed; SIGTERM → ctx cancel → drain → return per AGENTS.md rule 1. Tests: `TestWorkerPicksUpRegistrationEvent`, `TestWorkerFailedRegistrationWritesFailureRow`, `TestWorkerHonoursCtxCancel`, `TestWorkerNoRetryOnFailure` (M8 ships single-attempt semantics; operator re-submits via dashboard). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: main.go boot wiring (T011); dashboard polling for status (T013); operator's re-submit UX (T013).

---

## Phase 3 — Verb + spawn extensions

- [ ] **T007** `internal/garrisonmutate/` — `create_ticket` caller-surface extension + cycle detect + auto-inherit + dept-weekly gate hookup + agent-anchored audit
  - **Depends on**: T001, T004.
  - **Files**: `supervisor/internal/garrisonmutate/deps.go` (extend `Deps` with `AgentInstanceID pgtype.UUID`); `supervisor/internal/garrisonmutate/verbs_tickets.go` (extend `realCreateTicketHandler`: assertExactlyOneCallerAnchor; auto-inherit parent; cycle walker; dept-weekly gate call); `supervisor/internal/garrisonmutate/audit.go` (extend `AuditWriteParams` with `AgentInstanceID`; pass through to `InsertChatMutationAudit`); `supervisor/internal/garrisonmutate/errors.go` (add `ErrDependencyCycle`, `ErrDependencyChainTooDeep`); `supervisor/internal/garrisonmutate/verbs_tickets_test.go` (extend with agent-caller cases).
  - **Completion condition**: `Deps.AgentInstanceID` field added; new helper `assertExactlyOneCallerAnchor(deps)` validates exactly-one-of (ChatSessionID, AgentInstanceID) for `create_ticket` and rejects supervisor wiring failures with internal error. Auto-inherit: when `deps.AgentInstanceID.Valid && args.ParentTicketID == ""`, the handler resolves `agent_instances.ticket_id` for the caller and sets `args.ParentTicketID` to that value (FR-006). Cycle walker (`walkDependencyChain(ctx, q, dependsOnID, depthCap=32)`) runs in-tx; rejects with `dependency_cycle` on cycle, `dependency_chain_too_deep` at depth 32+. Dept-weekly gate (`throttle.CheckDeptWeekly`) runs against the **target** department; on rejection, writes `throttle_events` row + audit row with `outcome='dept_weekly_ticket_budget_exceeded'`; tag agent-driven cross-dept creates with `cross_dept_create=true` in `args_jsonb`. **Post-commit notifies**: emit `pg_notify('work.ticket.dependency_added.<dept>', payload)` for every non-NULL `depends_on_ticket_id` write (FR-104; payload shape `{ticket_id, depends_on_ticket_id, dept, created_at}`); emit `pg_notify('work.throttle.event', payload)` after every `throttle_events` INSERT (FR-204; reuses M6's channel). Tests: `TestCreateTicketAgentCallerSucceeds` (agent caller; row + audit with `agent_instance_id` populated); `TestCreateTicketAgentCallerAutoInheritsParent` (agent omits parent; resolved row carries agent's current ticket as parent); `TestCreateTicketAgentExplicitParentOverridesAutoInherit`; `TestCreateTicketBothAnchorsRejects` (both Chat + AgentInstance set → internal error); `TestCreateTicketDependencyCycleRejects` (A→B→C, attempt C→A → rejection); `TestCreateTicketDependencyChainTooDeep` (33-hop chain → rejection); `TestCreateTicketDeptWeeklyBudgetExceeded`; `TestCreateTicketCrossDeptGateScopesAgainstTarget`; `TestCreateTicketAgentOnlyAuditCommitsCleanly` (agent-only audit row with `chat_session_id IS NULL AND agent_instance_id IS NOT NULL` commits — defends against M7-shape regressions); `TestCreateTicketDependencyAddedEmitsNotify`; `TestCreateTicketThrottleFireEmitsNotify`. `gofmt -l` + `go vet` clean. M5.3 chat-caller tests pass unchanged (regression).
  - **Out of scope for this task**: dispatcher dependency-unblock listener (T009); MCPJungle env injection (T010); the `register_mcp_server` verb (T008); dashboard surface (T014).

- [ ] **T008** `internal/garrisonmutate/register_mcp_server.go` — Server-Action-only verb + `ServerActionVerbs` registry
  - **Depends on**: T001 (mcp_servers table).
  - **Files**: `supervisor/internal/garrisonmutate/server_action_verbs.go` (new — `ServerActionVerbs` slice separate from chat-side `Verbs`); `supervisor/internal/garrisonmutate/register_mcp_server.go` (new — `realRegisterMcpServerHandler`); `supervisor/internal/garrisonmutate/register_mcp_server_test.go` (new); `supervisor/internal/garrisonmutate/verbs_test.go` (extend — assert chat-side `Verbs` and `ServerActionVerbs` are disjoint).
  - **Completion condition**: `ServerActionVerbs` slice declared with one entry `register_mcp_server` (ReversibilityClass=2, AffectedResourceType="mcp_server"). Handler validates `name` starts with `<active customer_slug>.` (FR-307; rejects `validation_failed` otherwise); validates `transport` in `{http, stdio, sse}`; INSERTs `mcp_servers` row with `status='pending'` (the M8 migration's INSERT trigger emits the pg_notify). **No audit row written here per FR-306 amendment** — the reactive worker (T006) writes the single canonical `chat_mutation_audit` row when MCPJungle's API call completes (success or failed); anchoring the audit on the final outcome avoids the two-rows-with-different-outcomes ambiguity. Returns the row's id + URL on success. Tests: `TestRegisterRequiresCustomerPrefix` (name without prefix → validation_failed); `TestRegisterWritesPendingRow` + `TestRegisterDoesNotWriteAuditAtServerActionTime` (defends FR-306 single-row invariant); `TestRegisterDuplicateNameRejects` (UNIQUE on (customer_slug, name) FK error → friendly typed result); `TestVerbsSlicesDisjoint` (chat-side `Verbs` does NOT include `register_mcp_server`; mirrors M7's F3 lean test). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: dashboard form wiring (T013); the reactive worker's MCPJungle API call (T006 already shipped that); ACL on which operators can register (M9+ multi-operator territory).

- [ ] **T009** `internal/spawn/spawn.go` — `depends_on_ticket_id` spawn-prep gate + dispatcher dependency-unblock callback
  - **Depends on**: T001, T007.
  - **Files**: `supervisor/internal/spawn/spawn.go` (extend `prepareSpawn` with dependency check; extend the transition-listener callback to re-enqueue blocked dependents); `supervisor/internal/spawn/spawn_test.go` (extend); `supervisor/internal/events/listener.go` (extend the transition-listener registration to wire the unblock callback).
  - **Completion condition**: `prepareSpawn` blocks if the candidate ticket's `depends_on_ticket_id` references a predecessor whose `column_slug` is NOT in the predecessor's department's `dependency_satisfaction_columns`. Block returns `spawnPrep{done: true}` (defer pattern, same as M6 throttle defer; does NOT roll back the event). On `work.ticket.transitioned.<dept>.<from>.<to>`, the dispatcher queries `tickets WHERE depends_on_ticket_id=<transitioning_id> AND column_slug='todo'`; for each result, emits a synthetic `work.ticket.created.<dept>.todo` re-enqueue (the M1 dispatcher's existing dedupe handles double-fire). Tests: `TestSpawnPrepBlocksOnUnsatisfiedDependency` (predecessor in `in_dev`, dependent in `todo`; prep defers without spawning); `TestSpawnPrepUnblocksAfterPredecessorTransition` (transition fires synthetic create event; second prep call spawns); `TestSpawnPrepDependencyNullSkipsCheck` (NULL `depends_on_ticket_id` bypasses the gate); `TestSpawnPrepHonoursPerDeptSatisfactionColumns` (different `dependency_satisfaction_columns` configurations for the predecessor's dept). `gofmt -l` + `go vet` clean. M5.x + M6 + M7 spawn-side tests pass unchanged (regression).
  - **Out of scope for this task**: MCPJungle env-var injection (T010); main.go boot wiring (T011); dashboard surface for blocked-dependent observability (M9+ polish).

- [ ] **T010** `internal/spawn/m8.go` — MCPJungle bearer-token env-var injection at container start
  - **Depends on**: T002, T003, T005.
  - **Files**: `supervisor/internal/spawn/m8.go` (new — `EnsureMcpjungleTokenForAgent(ctx, deps, agentID) (envVar string, err error)`); `supervisor/internal/spawn/m8_test.go` (new); `supervisor/internal/spawn/spawn.go` (extend `runRealClaudeViaContainer` to call `EnsureMcpjungleTokenForAgent` + add `MCPJUNGLE_BEARER_TOKEN` to container env + update `--mcp-config` with the `mcpjungle` entry); `supervisor/internal/spawn/spawn_test.go` (extend).
  - **Completion condition**: `EnsureMcpjungleTokenForAgent` resolves the per-agent vault path `mcpjungle/agents/<agent-id>` via the existing M2.3 vault fetcher (using the agent-scoped grant from T003), returns the env-var string `MCPJUNGLE_BEARER_TOKEN=<token>`. Container env at spawn time includes the env var (vault-injected per M2.3 Rule 1; never enters claude's prompt context). The per-agent container's `--mcp-config` gains an `mcpjungle` entry with `Authorization: Bearer ${MCPJUNGLE_BEARER_TOKEN}`. The `agent_install_journal.step` CHECK constraint already includes `mcpjungle_client_create` + `mcpjungle_allowlist_apply` (added in T001 — this task only writes the rows, doesn't extend the constraint). Tests: `TestEnsureMcpjungleTokenFetchesFromVault` (fake vault returns the token; env-var injection assertion); `TestEnsureMcpjungleTokenSurfacesUnreachable` (vault returns ErrUnreachable; spawn falls back to spawn_failed with typed exit_reason); `TestSpawnInjectsMcpjungleEntryIntoMcpConfig` (asserted against the rendered MCP config JSON). `gofmt -l` + `go vet` clean. M7 spawn integration tests pass unchanged (regression).
  - **Out of scope for this task**: dispatcher dependency-unblock (T009 owns); the reconciler that creates the McpClient at first activation (T005); main.go startup-time reconciler invocation (T011).

---

## Phase 4 — Wiring + deployment

- [ ] **T011** `internal/events/` listener wiring + `cmd/supervisor/main.go` MCPJungle boot order
  - **Depends on**: T005, T006, T009, T010.
  - **Files**: `supervisor/internal/events/listener.go` (extend with `work.mcp_server.registration_requested` channel subscription routing to `mcpserverwork.Worker`); `supervisor/internal/events/listener_test.go` (extend); `supervisor/cmd/supervisor/main.go` (extend boot: construct `mcpjungle.Client` from config; run `mcpjungle.Client.HealthCheck` and log warning + continue on error; run `mcpjungle.ReconcileMcpClients` AFTER `migrate7.Run` and BEFORE the events listener subscribes; errgroup-add `mcpserverwork.Worker.Run`).
  - **Completion condition**: Supervisor boots with the new sequence: migrate7.Run → mcpjungle.ReconcileMcpClients → events.Listener.Run → mcpserverwork.Worker.Run (errgroup-managed alongside existing M2.2 + M5.1 + M6 + M7 subsystems). When `cfg.MCPJungleURL` is unreachable at startup, the supervisor logs a structured warning (`mcpjungle_unreachable_at_startup` log key) and continues; the reconciler is skipped (returns early with the same warning). Tests: `TestRegistrationRequestListenerDispatches` (emit pg_notify; assert the worker channel sees it); `TestDependencyUnblockOnTransitionEmitsSyntheticCreate` (transition → blocked dependent → synthetic create event); `TestSupervisorBootSurvivesMcpjungleDown` (fake MCPJungle returns connection-refused at health check; supervisor still boots). `gofmt -l` + `go vet` clean. M1 + M2.x + M5.x + M6 + M7 startup integration tests pass unchanged (regression).
  - **Out of scope for this task**: deployment (T012); dashboard wiring (T013); golden-path integration test (T015).

- [ ] **T012** `supervisor/docker-compose.yml` + `scripts/dev-stack-up.sh` — MCPJungle + Postgres sidecars
  - **Depends on**: T011 (so the deployment matches the boot-order code-side).
  - **Files**: `supervisor/docker-compose.yml` (extend with `garrison-mcpjungle` + `garrison-mcpjungle-postgres` services + a named volume); `scripts/dev-stack-up.sh` (extend to provision the new services + run `mcpjungle init-server` once + store the admin token at the dev Infisical's path `mcpjungle/admin`); `docs/ops-checklist.md` (new M8 section: post-deploy steps for admin-token rotation, MCPJungle Postgres backup expectation, digest-pinning convention for the MCPJungle image).
  - **Completion condition**: `docker compose -f supervisor/docker-compose.yml up -d garrison-mcpjungle-postgres garrison-mcpjungle` brings the two new services up cleanly. `docker compose ps` shows both healthy. `mcpjungle` listens on port 8080 inside the compose network. `scripts/dev-stack-up.sh` runs end-to-end: brings up Postgres + MemPalace + docker-proxy + MinIO + Infisical + MCPJungle + supervisor + dashboard; the init-server step writes the admin token to the dev Infisical instance. `docs/ops-checklist.md` M8 section ≥ 100 words covering: admin-token rotation procedure, Postgres backup expectation, digest-pinning convention (`GARRISON_MCPJUNGLE_IMAGE` env var pattern matching the M2.3 Infisical image pinning).
  - **Out of scope for this task**: Production-deploy automation (operator-driven Coolify deploy); supervisor-image Dockerfile changes (none anticipated); dashboard deployment changes.

---

## Phase 5 — Dashboard surfaces

- [ ] **T013** Dashboard `/admin/mcp-servers` — page + Server Action + Drizzle queries + components
  - **Depends on**: T001, T008.
  - **Files**: `dashboard/app/[locale]/(app)/admin/mcp-servers/page.tsx` (new — list of registered MCP servers + registration form); `dashboard/app/[locale]/(app)/admin/mcp-servers/[id]/page.tsx` (new — single-server detail with AllowList membership); `dashboard/app/[locale]/(app)/admin/mcp-servers/searchParams.ts` (new); `dashboard/lib/queries/mcpServers.ts` (new — Drizzle queries); `dashboard/lib/actions/mcpServer.ts` (new — Server Action wrapping the `mcp_servers` row INSERT + audit row in one Drizzle tx); `dashboard/components/features/mcp/RegisterForm.tsx`, `ServerRow.tsx`, `StatusChip.tsx` (all new); `dashboard/components/layout/Sidebar.tsx` (extend nav with `/admin/mcp-servers` link).
  - **Completion condition**: Operator can navigate to `/admin/mcp-servers`, see the list of registered servers grouped by status (pending / registered / failed), click the register form, submit a new server with name `<customer_slug>.<server-name>`, see the new row land with `status='pending'`, and watch the status flip to `registered` (or `failed`) as the reactive worker (T006) processes it (poll-based; SSE wiring is M8.1). The Server Action writes the `mcp_servers` row + `chat_mutation_audit` row in one Drizzle tx; pg_notify fires from the M8 migration's INSERT trigger. `bun run typecheck` passes; `bun run dev` renders the page without runtime error. Per AGENTS.md memory `feedback_test_scope_go_only`: no vitest tests land alongside this surface; the Go-side T008 + T018 tests cover the data shape.
  - **Out of scope for this task**: agent-anchored audit filter (T014); SSE for live status (M8.1 polish); pagination / search (M8.1 polish); per-MCP-server cost telemetry (M9+).

- [ ] **T014** Dashboard `/activity` agent-anchored audit filter — extend the existing M3 audit-row surface
  - **Depends on**: T001.
  - **Files**: `dashboard/app/[locale]/(app)/activity/page.tsx` (extend with `agent_instance_id` filter param + render path for agent-anchored rows); `dashboard/lib/queries/audit.ts` (extend with `ListAuditByAgentInstance` query); `dashboard/components/features/activity/AuditRow.tsx` (extend rendering: link to ticket via `agent_instances.ticket_id`; link to agent role via `agent_instances.role_slug`).
  - **Completion condition**: Operator navigating to `/activity?agent_instance_id=<uuid>` sees only the audit rows for that specific agent instance, rendered with links to (a) the originating ticket and (b) the agent role. Chat-anchored rows continue to render via the M5.3 path unchanged. `bun run typecheck` passes; `bun run dev` renders the page without runtime error. No vitest tests per the memory rule; the Go-side T019 integration test covers the data shape.
  - **Out of scope for this task**: dedicated per-role audit timeline page (M8.1 polish candidate per FR-502); SSE for live audit updates (out of scope); pagination polish (M8.1).

- [ ] **T014a** Dashboard `/hygiene` per-department runaway-control surface (FR-501)
  - **Depends on**: T001.
  - **Files**: `dashboard/app/[locale]/(app)/hygiene/page.tsx` (extend with per-department weekly ticket-count + budget panel); `dashboard/lib/queries/throttle.ts` (extend with `ListDeptWeeklyState` query returning `{dept_slug, current_count, weekly_ticket_budget, last_fired_at}` per department); `dashboard/lib/actions/department.ts` (extend the M4 `edit_department` Server Action with the `weekly_ticket_budget` editable field); `dashboard/components/features/hygiene/RunawayPanel.tsx` (new — renders count/budget bar + a "days since last budget-exceeded" badge if any rows in `throttle_events` for the dept).
  - **Completion condition**: Operator navigating to `/hygiene` sees a new "Runaway control" section: per-department row with `current_count / weekly_ticket_budget` (or "unlimited" when NULL); a typed warning indicator + most-recent `throttle_events.fired_at` timestamp if a `dept_weekly_ticket_budget_exceeded` event has fired within the rolling 7d. Operator clicks "Edit budget" inline; the form submits through `edit_department` Server Action; new budget persists. `bun run typecheck` passes; `bun run dev` renders without runtime error. No vitest tests per the memory rule; the Go-side T017 integration test covers the data shape that the surface reads from.
  - **Out of scope for this task**: per-agent runaway counts (the budget is per-department); SSE for live count updates (M8.1 polish); the actual gate enforcement (T004 + T007 own that — this task is pure read-side + budget-edit UX).

---

## Phase 6 — Integration tests

- [ ] **T015** GOLDEN PATH — `supervisor/m8_golden_path_integration_test.go` (US1)
  - **Depends on**: T010, T011.
  - **Files**: `supervisor/m8_golden_path_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test exercises spec US1 end-to-end: spawn an engineer agent against a seeded `in_dev` ticket; the agent (via mockclaude script or real claude when ANTHROPIC_API_KEY is set) calls `mcp__garrison-mutate__create_ticket` with a follow-up objective; assert a new `tickets` row lands with `column_slug='todo'`, the `chat_mutation_audit` row carries `agent_instance_id != NULL` + `chat_session_id IS NULL`, the new ticket's `parent_ticket_id` auto-inherits the engineer's current ticket. Test runs against a real testcontainer Postgres + the per-agent container (M7 substrate). Test under 90s wall-clock (matches CI integration-job budget).
  - **Out of scope for this task**: dependency-test scenarios (T016); runaway gate (T017); MCPJungle integration (T018); audit-surface filter (T019); chaos (T020).

- [ ] **T016** `supervisor/m8_dependency_integration_test.go` (US2 — block + unblock + cycle)
  - **Depends on**: T009.
  - **Files**: `supervisor/m8_dependency_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test exercises spec US2: seed ticket A in department X with `column_slug='in_dev'`, ticket B in department Y with `depends_on_ticket_id=A.id`; assert B does NOT spawn (stays at `column_slug='todo'`). Then transition A to `qa_review`; assert the dispatcher re-evaluates B's spawn-prep within one pg_notify round-trip and B spawns. Additionally exercise cycle rejection: attempt to add C→A (closing A→B→C→A); assert `dependency_cycle` rejection. Attempt 33-hop chain; assert `dependency_chain_too_deep` rejection. The per-department `dependency_satisfaction_columns` JSONB override is tested with an operator-tunable column (e.g. `["done"]` rather than the default `["qa_review","done"]`).
  - **Out of scope for this task**: golden path (T015); runaway gate (T017); MCPJungle (T018); chaos (T020).

- [ ] **T017** `supervisor/m8_runaway_integration_test.go` (US3)
  - **Depends on**: T007.
  - **Files**: `supervisor/m8_runaway_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test exercises spec US3: seed `departments.engineering.weekly_ticket_budget=50` + 50 tickets in the rolling 7-day window; the 51st `create_ticket` call rejects with `dept_weekly_ticket_budget_exceeded`; the `throttle_events` table gains one row with `kind='dept_weekly_ticket_budget_exceeded'`; the `chat_mutation_audit` row carries the matching outcome. NULL-budget bypass: seed `weekly_ticket_budget=NULL`; 1000 tickets succeed. Cross-dept scoping: engineering caller targeting marketing's ticket; marketing's budget binds. Rolling window expiry: the 51st-oldest ticket falls out of the 7-day window; the next `create_ticket` succeeds. Test under 60s wall-clock.
  - **Out of scope for this task**: chaos (T020); per-department dashboard surface tests (M8.1 polish).

- [ ] **T018** `supervisor/m8_mcpjungle_integration_test.go` (US4 + US5 — register + agent reaches + AllowList rejects)
  - **Depends on**: T006, T008, T010, T011, T013.
  - **Files**: `supervisor/m8_mcpjungle_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test exercises spec US4 + US5 end-to-end against a fake MCPJungle backend (httptest.Server). Server Action writes `mcp_servers` row with `status='pending'`; the M1 dispatcher picks up the pg_notify; the T006 worker calls the fake MCPJungle's `POST /servers`; row's `status` flips to `registered`. A subsequent hire's first spawn calls `mcp__mcpjungle__<server>-<tool>`; the fake MCPJungle routes the call; the agent's `tool_result` succeeds. Attempt a tool call against a server NOT in the agent's AllowList; the fake returns 403; the agent's `tool_use` records the rejection with `error_kind='access_denied'`. Additionally: `TestPauseResumeDoesNotMutateMcpClient` (FR-311) — pause an active agent via `pause_agent`; assert the fake MCPJungle backend received zero `DELETE /clients/<name>` or `PATCH /clients/<name>/allowlist` requests; resume the agent; assert still zero MCPJungle mutations; the McpClient row + bearer token at vault path `mcpjungle/agents/<id>` are byte-identical before and after the pause cycle. Test under 90s wall-clock.
  - **Out of scope for this task**: chaos for MCPJungle-down at startup (T020); real MCPJungle integration (operator soak); per-server cost telemetry (M9+).

- [ ] **T019** `supervisor/m8_audit_surface_integration_test.go` (SC-014)
  - **Depends on**: T014.
  - **Files**: `supervisor/m8_audit_surface_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test seeds a mixed set of chat-anchored audit rows (M5.3 shape) + agent-anchored audit rows (M8 shape) for a single agent instance + cross-cuts. Asserts: (1) `ListAuditByAgentInstance(agentInstanceID)` returns only the agent-anchored rows for that instance; (2) each row resolves to its originating ticket via `agent_instances.ticket_id` + the agent role via `agent_instances.role_slug` within one query (forensic reconstruction per SC-014); (3) chat-anchored rows are NOT returned by the agent-filter query. Test under 30s wall-clock.
  - **Out of scope for this task**: dashboard rendering tests (no vitest per memory rule); the dedicated per-role audit timeline page (M8.1 polish).

---

## Phase 7 — Chaos tests

- [ ] **T020** `supervisor/chaos_m8_test.go` extensions — MCPJungle down + simultaneous callers race + worker survives flap
  - **Depends on**: T011, T017, T018.
  - **Files**: `supervisor/chaos_m8_test.go` (new — `//go:build chaos`).
  - **Completion condition**: Three new tests pass under `-tags=chaos`: `TestMCPJungleDownAtSupervisorStartup` (fake MCPJungle's health endpoint returns connection-refused; supervisor still boots with warning; per-spawn MCPJungle calls fail individually with typed `mcpjungle_unreachable`); `TestSimultaneousCreateTicketCallersRaceOnBudget` (two concurrent callers at budget-49; assert exactly one succeeds + exactly one rejects with no double-tickets-when-budget-allows-one race); `TestRegistrationWorkerSurvivesMcpjungleFlap` (start registration; MCPJungle becomes unreachable mid-call; worker marks `status='failed'`; operator re-submits via dashboard; worker re-attempts and succeeds). Each test gated on `GARRISON_CHAOS_DOCKER=1` env-var opt-in (matches M7 chaos test pattern). M7 chaos extensions pass unchanged (regression gate).
  - **Out of scope for this task**: integration tests (T015–T019); SC-aligned scripted check (T022).

---

## Phase 8 — Wrap-up

- [ ] **T021** Add ship-status annotation to ARCHITECTURE.md's already-amended M8 paragraph + dashboard substring-pin test
  - **Depends on**: T020 (wraps after all implementation lands).
  - **Files**: `ARCHITECTURE.md` (modify — append `✅ Shipped 2026-MM-DD` annotation + retro link to the M8 paragraph; the paragraph's structural content was already amended at the context-doc commit time during /garrison-milestone-context, so this task is an annotation only, NOT a rewrite); `dashboard/tests/architecture-amendment.test.ts` (add three substring assertions: shipped annotation, MCPJungle license MPL-2.0 reference, customer-slug primitive reference).
  - **Completion condition**: ARCHITECTURE.md M8 paragraph carries the shipped-status annotation; existing M3/M4/M5/M6/M7 amendments unchanged. The three new substring assertions pass under vitest; the existing M3/M4/M5/M5.4/M6/M7 amendment-pin assertions also pass (regression).
  - **Out of scope for this task**: RATIONALE.md edits (no M8 RATIONALE amendments — all decisions traced to the spike + spec + clarify); substantive M8 paragraph rewrites (already done at context-doc time — this task is annotation-only).

- [ ] **T022** ACCEPTANCE-RUN — `scripts/m8-acceptance.sh` + Sonar new-issues pre-clearance
  - **Depends on**: T021.
  - **Files**: `scripts/m8-acceptance.sh` (new — orchestrates running all 14 spec SCs as discrete checks).
  - **Completion condition**: Script invokes each of SC-001..SC-014 from spec.md as a discrete check (SC-001 timed via Playwright + dev-stack; SC-002 via SQL count on `tickets` with `depends_on_ticket_id`; SC-003 via the runaway gate integration; SC-004 via the Server Action + worker round-trip; SC-005..SC-006 via the MCPJungle integration test; SC-007 via `git diff --stat origin/main..HEAD -- supervisor/go.mod supervisor/go.sum dashboard/package.json`; SC-008 via the M2.x + M5.x + M6 + M7 integration suite; SC-009 via the cycle-detect test; SC-010 via the SonarCloud `api/issues/search` query against the M8 PR (gate ≥82% + 0 new issues + 0 hotspots — re-runs if non-zero, opens a focused patch against the relevant earlier task's files, does NOT introduce new features here); SC-011 via the chaos test; SC-012 via the audit-surface integration; SC-013 via N=10 hire-flow McpClient name-prefix check; SC-014 via the audit-filter integration). Script exits 0 if all 14 pass; non-zero otherwise. **If a step fails, open a focused patch against the relevant earlier task's files; do NOT introduce new features here. Re-run from the top.**
  - **Out of scope for this task**: any new feature code; the retro (T023); Playwright UI polish (M8.1).

- [ ] **T023** RETRO — `docs/retros/m8.md` + MemPalace `wing_company / hall_events` drawer mirror
  - **Depends on**: T022.
  - **Files**: `docs/retros/m8.md` (new — canonical); MemPalace drawer via `mempalace_add_drawer` MCP tool (mirror).
  - **Completion condition**: Retro markdown follows AGENTS.md §"Retros" + the M7-retro shape (what shipped / what the spec got wrong / dependencies added outside the locked list / open questions deferred to next milestone / spike-vs-implement validation count / gotchas worth remembering for M9). The retro answers: did the alpha vs beta line hold? Did the runaway gate fire on any agent (intentional or otherwise) during soak? Did MCPJungle integration introduce any new attack surface beyond what the threat models anticipated? Did lint pass locally before every push (M5.4 + M7 retro lesson)? Did the M7-gotcha drizzle:pull mangling + sqlc ColumnN naming recur (T001's defensive steps should have prevented; retro confirms)? Dependencies-added section: zero new Go/TypeScript deps expected; locked-deps streak continues. Spike validation: prevention-vs-discovery count from `m8-mcpjungle-spike.md` (the per-tenant primitive gap was the spike's main finding; was it the right call to defer to beta?). MemPalace drawer mirrors the markdown content; both deliverables non-optional per AGENTS.md M3+ policy. Retro acknowledges and lists post-M8 polish items (SSE for MCP server registration status, dedicated per-role audit timeline page, dashboard pagination / search).
  - **Out of scope for this task**: any code changes (retro is documentation only); planning M9 (the retro lists open questions but doesn't pre-scope M9); ARCHITECTURE.md amendment (T021).

---

## What this task list does not include

- **Per-customer MCPJungle instance provisioning** — beta-band per `project_garrison_release_phases`. The structural commitments in T011 + T013 (`mcpjungle.URLForCustomer` lookup interface + customer_slug primitive) make beta-time multi-tenant mechanical. The actual per-customer onboarding provisioner is M9+ territory.
- **Cross-agent skill-grant policies** — post-alpha territory; M8 alpha keeps per-agent skill mounts.
- **MCPJungle's Tool Groups feature** — M8 uses per-server AllowList only. Tool Groups become load-bearing at M9+ when finer-grained per-tool scoping matters.
- **SSE for live MCP server registration status** — M8 alpha uses operator-refresh polling. SSE polish lands M8.1 if the operator wants real-time updates.
- **Dedicated per-role audit timeline page** — M8 alpha extends `/activity` with a filter; the dedicated page is M8.1 polish candidate per FR-502.
- **Cost-telemetry per MCP tool call** — M9+ when third-party MCP servers actually ship and operators want per-server billing breakdowns.
- **Heartbeats / scheduled wake-ups** — M9.
- **AGENTS.md "Standing out-of-scope" reflip post-ship** — the per-agent container + sandbox surface from M7 stays sealed; M8 doesn't add new entries.
