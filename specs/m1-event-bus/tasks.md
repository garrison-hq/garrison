---
description: "Task list for M1 — event bus and supervisor core"
---

# Tasks: M1 — event bus and supervisor core

**Feature branch**: `001-m1-event-bus`
**Inputs**: [spec.md](./spec.md), [plan.md](./plan.md), [data-model.md](./data-model.md), [quickstart.md](./quickstart.md), [contracts/](./contracts/), [../_context/m1-context.md](../_context/m1-context.md), [../../AGENTS.md](../../AGENTS.md)

## Ordering principle

Strict dependency order. Not easiest-first, not most-interesting-first. After every task the repository is in a working state — the supervisor binary (or a meaningful subset of it) runs, even if it does less than the final version. No parallelisation markers: M1 is executed linearly by one operator.

Each task carries:

- **Depends on** — prior task IDs (or "none")
- **Files** — exact paths the task creates or modifies
- **Completion condition** — the concrete passing check after the task
- **Out of scope for this task** — what to resist reaching ahead for

Total: **18 tasks**.

---

## Phase 1: Repository and tooling scaffold

- [ ] T001 Scaffold `supervisor/` Go module, Dockerfile, Makefile, and placeholder `main.go`
  - **Depends on**: none
  - **Files**: [supervisor/go.mod](../../supervisor/go.mod), [supervisor/go.sum](../../supervisor/go.sum), [supervisor/.gitignore](../../supervisor/.gitignore), [supervisor/Dockerfile](../../supervisor/Dockerfile), [supervisor/Makefile](../../supervisor/Makefile), [supervisor/cmd/supervisor/main.go](../../supervisor/cmd/supervisor/main.go), [supervisor/cmd/supervisor/version.go](../../supervisor/cmd/supervisor/version.go)
  - **Completion condition**: `cd supervisor && make build` produces `bin/supervisor`, and `./bin/supervisor --version` prints `supervisor dev` (or the ldflags-injected tag) and exits 0. `make lint` passes (`go vet ./...` + `gofmt -l .` empty). `docker build -t garrison/supervisor:scaffold supervisor/` succeeds.
  - **Out of scope for this task**: any `internal/` packages, any Postgres access, any flag beyond `--version` and `--help`, any sqlc-generated code, any migration logic.

---

## Phase 2: Configuration, schema, and generated types

- [ ] T002 Implement `internal/config` env-var loader with validation and unit tests
  - **Depends on**: T001
  - **Files**: [supervisor/internal/config/config.go](../../supervisor/internal/config/config.go), [supervisor/internal/config/config_test.go](../../supervisor/internal/config/config_test.go)
  - **Completion condition**: `go test ./internal/config/...` passes with the four tests from [plan.md](./plan.md) §"Test plan → Unit tests" (`TestLoadDefaults`, `TestLoadRejectsSubSecondPoll`, `TestLoadRejectsMissingRequired`, `TestLoadRejectsInvalidLogLevel`). All seven env vars in [plan.md](./plan.md) §"Config" are represented on the typed `Config` struct with the stated defaults.
  - **Out of scope for this task**: reading the config in `main.go` (wiring happens in T012), any Postgres connect, any logging beyond error returns from `Load`.

- [ ] T003 Author goose migrations for the M1 schema and trigger
  - **Depends on**: T001
  - **Files**: [migrations/20260421000001_initial_schema.sql](../../migrations/20260421000001_initial_schema.sql), [migrations/20260421000002_event_trigger.sql](../../migrations/20260421000002_event_trigger.sql)
  - **Completion condition**: running `goose -dir migrations postgres "$ORG_OS_DATABASE_URL" up` against a throwaway Postgres (a one-off `docker run postgres:17` is fine) applies both migrations without error, creates the four tables and two partial indexes verbatim from [../_context/m1-context.md](../_context/m1-context.md) §"Data model for M1", installs the `emit_ticket_created` trigger function and `ticket_created_emit` trigger, and `goose down` rolls both back cleanly. No Go code depends on this yet.
  - **Out of scope for this task**: embedding goose into the binary (that is T012's `--migrate` subcommand), any `sqlc` code, any test data seeding.

- [ ] T004 Author sqlc query SQL, configure sqlc, and commit generated Go
  - **Depends on**: T003
  - **Files**: [supervisor/sqlc.yaml](../../supervisor/sqlc.yaml), [migrations/queries/departments.sql](../../migrations/queries/departments.sql), [migrations/queries/tickets.sql](../../migrations/queries/tickets.sql), [migrations/queries/event_outbox.sql](../../migrations/queries/event_outbox.sql), [migrations/queries/agent_instances.sql](../../migrations/queries/agent_instances.sql), [supervisor/internal/store/](../../supervisor/internal/store/) (generated)
  - **Completion condition**: `make sqlc` (invoking `sqlc generate`) produces Go sources under `supervisor/internal/store/` that `go build ./...` accepts. The generated package exports the named methods listed in [plan.md](./plan.md) §"sqlc layout": `GetDepartmentByID`, `InsertDepartment`, `GetTicketByID`, `InsertTicket`, `GetEventByID`, `SelectUnprocessedEvents`, `LockEventForProcessing`, `MarkEventProcessed`, `InsertRunningInstance`, `UpdateInstanceTerminal`, `CountRunningByDepartment`, `RecoverStaleRunning`. Generated files are committed.
  - **Out of scope for this task**: any consumer code in other `internal/` packages, any runtime behaviour — this task stops at "the types and methods exist and compile".

---

## Phase 3: Internal packages in dependency order

- [ ] T005 Implement `internal/pgdb` with pool, dedicated LISTEN conn, and advisory lock
  - **Depends on**: T002, T004
  - **Files**: [supervisor/internal/pgdb/pgdb.go](../../supervisor/internal/pgdb/pgdb.go), [supervisor/internal/pgdb/advisory.go](../../supervisor/internal/pgdb/advisory.go), [supervisor/internal/pgdb/pgdb_test.go](../../supervisor/internal/pgdb/pgdb_test.go)
  - **Completion condition**: `Connect(ctx, cfg) (*pgxpool.Pool, *pgx.Conn, error)` applies the FR-017 100ms→30s backoff on initial connect failure. `AcquireAdvisoryLock(ctx, conn) error` wraps `pg_try_advisory_lock` with the fixed M1 key and returns a typed `ErrAdvisoryLockHeld` on contention. Unit tests verify backoff monotonicity and reset-on-success using a fake dialer; no Postgres is required in this task's tests.
  - **Out of scope for this task**: running the actual LISTEN loop (that is T010), any recovery query, any health-state writes.

- [ ] T006 Implement `internal/recovery.RunOnce` and unit test against a fake querier
  - **Depends on**: T004
  - **Files**: [supervisor/internal/recovery/recovery.go](../../supervisor/internal/recovery/recovery.go), [supervisor/internal/recovery/recovery_test.go](../../supervisor/internal/recovery/recovery_test.go)
  - **Completion condition**: `RunOnce(ctx, pool) (int, error)` calls the sqlc `RecoverStaleRunning` method and returns the reconciled-row count. Exported constant `RecoveryWindow = 5 * time.Minute` is defined with a comment citing NFR-006. Unit test uses a stub `store.Querier` interface (or equivalent) to assert the method is called exactly once.
  - **Out of scope for this task**: running recovery from `main.go` (T012), any continuous-lifecycle logic — this is a single-shot function.

- [ ] T007 Implement `internal/concurrency.CheckCap` with unit tests
  - **Depends on**: T004
  - **Files**: [supervisor/internal/concurrency/cap.go](../../supervisor/internal/concurrency/cap.go), [supervisor/internal/concurrency/cap_test.go](../../supervisor/internal/concurrency/cap_test.go)
  - **Completion condition**: `CheckCap(ctx, tx, departmentID) (allowed bool, cap int, running int, err error)` executes the two-query sequence from [plan.md](./plan.md) §"Concurrency accounting". The three unit tests (`TestCheckCapAllowsUnderCap`, `TestCheckCapBlocksAtCap`, `TestCheckCapBlocksAtZero`) pass against a stub querier, confirming cap=0 is the FR-003 pause signal.
  - **Out of scope for this task**: deciding what to do when blocked (defer vs. spawn lives in the events package), any advisory-locking for concurrency (M1 accepts the documented +1 race).

- [ ] T008 Implement command-template parsing in `internal/spawn` with unit tests
  - **Depends on**: T002
  - **Files**: [supervisor/internal/spawn/template.go](../../supervisor/internal/spawn/template.go), [supervisor/internal/spawn/template_test.go](../../supervisor/internal/spawn/template_test.go)
  - **Completion condition**: a pure function takes the raw `ORG_OS_FAKE_AGENT_CMD` string plus `TICKET_ID` and `DEPARTMENT_ID` UUIDs and returns an `*exec.Cmd` with both literal-token substitution in argv and the two env vars set, per [plan.md](./plan.md) §"Subprocess lifecycle manager" step 3. Three unit tests (`TestSubstituteLiteralTokens`, `TestSubstituteAlsoSetsEnv`, `TestShlexRejectsUnterminatedQuote`) pass. The shlex-vs-whitespace decision is resolved in code and noted in the commit message per AGENTS.md soft rule on locked deps.
  - **Out of scope for this task**: actually running the subprocess, any `agent_instances` row writes, any context-timeout plumbing.

- [ ] T009 Implement subprocess lifecycle, status classification, and dedupe transaction in `internal/spawn`
  - **Depends on**: T004, T007, T008
  - **Files**: [supervisor/internal/spawn/spawn.go](../../supervisor/internal/spawn/spawn.go), [supervisor/internal/spawn/lifecycle_test.go](../../supervisor/internal/spawn/lifecycle_test.go)
  - **Completion condition**: `Spawn(ctx, ev)` executes steps 1–8 of [plan.md](./plan.md) §"Subprocess lifecycle manager": `LockEventForProcessing` dedupe early-return, concurrency gate delegation, `InsertRunningInstance`, `exec.CommandContext` with subprocess-timeout context, per-stream line-scanning goroutines emitting one `slog` record per line with `stream="stdout"|"stderr"`, SIGTERM→5s→SIGKILL on context cancel, and the single terminal transaction that writes `UpdateInstanceTerminal` plus `MarkEventProcessed`. The four classification unit tests pass (`TestClassifyExitZero`, `TestClassifyExitNonZero`, `TestClassifyTimeout`, `TestClassifySIGKILL`). `exit_reason` strings match the vocabulary table in [data-model.md](./data-model.md) §"`exit_reason` vocabulary".
  - **Out of scope for this task**: the LISTEN loop and poll ticker (T010), the health endpoint (T011), any integration test against a real Postgres (T013+).

- [ ] T010 Implement `internal/events` dispatcher, LISTEN loop, fallback poll, and reconnect state machine
  - **Depends on**: T005, T006, T009
  - **Files**: [supervisor/internal/events/dispatcher.go](../../supervisor/internal/events/dispatcher.go), [supervisor/internal/events/listener.go](../../supervisor/internal/events/listener.go), [supervisor/internal/events/poller.go](../../supervisor/internal/events/poller.go), [supervisor/internal/events/reconnect.go](../../supervisor/internal/events/reconnect.go), [supervisor/internal/events/dispatch_test.go](../../supervisor/internal/events/dispatch_test.go)
  - **Completion condition**: `NewDispatcher(handlers map[string]Handler)` builds a static route table; `Run(ctx, deps)` executes the initial-connect lifecycle from [plan.md](./plan.md) §"pg_notify listener connection lifecycle" (connect → advisory lock → recovery → initial poll → LISTEN) and the reconnect variant (backoff → connect → poll → LISTEN, no recovery) with the NFR-002 100ms→30s backoff. Poll writes `state.LastPollAt` each cycle. Three dispatcher unit tests pass (`TestDispatchRoutesKnownChannel`, `TestDispatchErrorsOnUnknownChannel`, `TestDispatchRejectsMalformedPayload`). Handler routing is frozen at construction (FR-014).
  - **Out of scope for this task**: the HTTP health server (T011), end-to-end testing against a real Postgres (T013+), any handling of channels beyond `work.ticket.created`.

- [ ] T011 Implement `internal/health` HTTP server, `/health` handler, and shared state
  - **Depends on**: T002, T005
  - **Files**: [supervisor/internal/health/server.go](../../supervisor/internal/health/server.go), [supervisor/internal/health/state.go](../../supervisor/internal/health/state.go), [supervisor/internal/health/server_test.go](../../supervisor/internal/health/server_test.go)
  - **Completion condition**: `NewServer(cfg, state)` binds `0.0.0.0:cfg.HealthPort` with no auth (FR-016 + clarification Q2). `/health` returns 200 iff an on-demand 500ms-timeout `SELECT 1` ping succeeds AND `time.Since(state.LastPollAt) <= 2*cfg.PollInterval`, else 503. Unit test uses `httptest.NewRecorder` + a fake pinger + a controllable clock to verify both 200 and 503 branches. Graceful shutdown on ctx cancel uses `http.Server.Shutdown` with `cfg.ShutdownGrace`.
  - **Out of scope for this task**: any `/metrics` endpoint (deferred to M2), authentication (out of scope for M1), wiring the server into `main.go` (T012).

---

## Phase 4: Binary wiring

- [ ] T012 Wire every subsystem into `cmd/supervisor` via `errgroup.WithContext`, signals, and `--migrate`
  - **Depends on**: T003, T010, T011
  - **Files**: [supervisor/cmd/supervisor/main.go](../../supervisor/cmd/supervisor/main.go), [supervisor/cmd/supervisor/signals.go](../../supervisor/cmd/supervisor/signals.go), [supervisor/cmd/supervisor/migrate.go](../../supervisor/cmd/supervisor/migrate.go)
  - **Completion condition**: `./bin/supervisor --migrate` runs `goose.UpContext` against the embedded `migrations/` filesystem and exits 0 on success, non-zero on SQL error. Normal startup loads `config`, builds the static dispatcher map with `work.ticket.created → ticketCreatedHandler`, and runs five goroutines under `errgroup.WithContext`: `events.Run`, `spawn` consumer, fallback poll ticker, `health.Serve`, signal watcher. SIGTERM/SIGINT/SIGHUP cancel the root context. Exit code is 0 on clean shutdown, 4 on advisory-lock contention, 5 if any subprocess required SIGKILL, per [contracts/cli.md](./contracts/cli.md). Running `make run` locally against a seeded Postgres and inserting one ticket via psql produces the log sequence in [quickstart.md](./quickstart.md) §"Run the daemon".
  - **Out of scope for this task**: any test coverage beyond manual verification against a local Postgres — formal integration coverage lands in T013–T015.

---

## Phase 5: Integration, golden path, and chaos

- [ ] T013 Set up testcontainers-go harness and store-level integration tests
  - **Depends on**: T004
  - **Files**: [supervisor/internal/store/store_integration_test.go](../../supervisor/internal/store/store_integration_test.go), [supervisor/internal/testdb/testdb.go](../../supervisor/internal/testdb/testdb.go)
  - **Completion condition**: `make test-integration` (`go test -tags=integration ./...`) boots a single shared Postgres container per test run, applies both migrations, and passes `TestInsertAndGetDepartment`, `TestSelectUnprocessedEvents`, `TestMarkEventProcessedIdempotent`, `TestRecoverStaleRunning`. A reusable helper `testdb.Start(t)` returns a `*pgxpool.Pool` + migrated container and handles teardown on `t.Cleanup`. All integration tests sit behind the `integration` build tag so `make test` (unit only) stays fast.
  - **Out of scope for this task**: any full-binary integration (T014+), any chaos faults (T016).

- [ ] T014 Golden-path end-to-end test — insert one ticket, observe one `succeeded` `agent_instances` row
  - **Depends on**: T012, T013
  - **Files**: [supervisor/integration_test.go](../../supervisor/integration_test.go)
  - **Completion condition**: `TestEndToEndTicketFlow` (tag `integration`) boots a Postgres container, runs the supervisor binary as a child process via `exec.CommandContext` with `ORG_OS_FAKE_AGENT_CMD='sh -c "echo ok; exit 0"'`, inserts a department and a ticket via the testdb pool, waits up to 10s for exactly one `agent_instances` row with `status='succeeded'`, and verifies the corresponding `event_outbox.processed_at` is set. This is the M1 smoke test: if this passes, the happy path works end-to-end.
  - **Out of scope for this task**: concurrency, reconnect, shutdown, or recovery scenarios — those land in T015 and T016.

- [ ] T015 Remaining integration tests covering US2, US3 startup, recovery, `/health`, and edge cases
  - **Depends on**: T014
  - **Files**: [supervisor/integration_test.go](../../supervisor/integration_test.go) (append)
  - **Completion condition**: all of `TestConcurrencyCapEnforced`, `TestDeferredEventPickedUpOnPoll`, `TestDepartmentNotExistMarksProcessed`, `TestCapZeroPauses`, `TestStartupFallbackPollBeforeListen`, `TestAdvisoryLockRejectsDoubleRun`, `TestRecoveryMarksStaleRunning`, `TestHealthReturns200WhenReady`, and `TestHundredTicketVolume` pass under `make test-integration`. Each corresponds to an acceptance scenario in [spec.md](./spec.md) §"User scenarios". `TestHundredTicketVolume` inserts 100 tickets into a cap-2 department with a fake agent that sleeps 1–3s, polls for terminal state on each row, and asserts (i) exactly 100 `agent_instances` rows reach a terminal status with matching `event_outbox.processed_at`, and (ii) a background sampler observing `SELECT COUNT(*) FROM agent_instances WHERE status='running'` every 100ms never records a value greater than `cap + 1`. This covers SC-002 and SC-003.
  - **Out of scope for this task**: fault injection (paused containers, external SIGKILL) — that is T016.

- [ ] T016 Chaos tests for reconnect, external SIGKILL, and graceful shutdown
  - **Depends on**: T015
  - **Files**: [supervisor/chaos_test.go](../../supervisor/chaos_test.go)
  - **Completion condition**: `make test-chaos` (tag `chaos`) passes `TestReconnectCatchesMissedEvents` (pause Postgres container, insert 3 tickets via separate connection after unpause — adjust per container semantics, unpause, expect all 3 complete within one poll interval), `TestSIGKILLSubprocessRecordedFailed` (spawn a long-sleep subprocess, `kill -9` it externally, expect `status='failed'` with signal in `exit_reason`), and `TestGracefulShutdownWithInflight` (long-sleep agent, SIGTERM the supervisor, subprocess receives SIGTERM, supervisor exits within `ORG_OS_SHUTDOWN_GRACE`).
  - **Out of scope for this task**: performance benchmarks, long-soak runs — neither is an M1 requirement.

---

## Phase 6: Acceptance and closure

- [ ] T017 Execute the 10 acceptance steps from `m1-context.md` against a built binary and record evidence
  - **Depends on**: T016
  - **Files**: [specs/m1-event-bus/acceptance-evidence.md](./acceptance-evidence.md)
  - **Completion condition**: each of the ten numbered steps in [../_context/m1-context.md](../_context/m1-context.md) §"Acceptance criteria for M1" is executed in order, unmodified, against a freshly-migrated Postgres and the release Docker image (`make docker`). Evidence — log excerpts, SQL query outputs, exit codes — is recorded step-by-step in `acceptance-evidence.md`. All ten steps pass. This is SC-001 satisfied and the M1 ship gate.
  - **Out of scope for this task**: fixing any bug discovered during acceptance — if a step fails, open a focused patch against the relevant earlier task's files, then re-run the full ten steps from the top. This task does not introduce new features.

- [ ] T018 Write the M1 retro at `docs/retros/m1.md`
  - **Depends on**: T017
  - **Files**: [docs/retros/m1.md](../../docs/retros/m1.md)
  - **Completion condition**: the retro follows the structure required by [../../AGENTS.md](../../AGENTS.md) §"Retros" — "what shipped", "what the spec got wrong", "dependencies added outside the locked list (with justifications)", "open questions deferred to the next milestone". The two plan-level open questions (shlex dependency, SIGHUP semantics) are explicitly resolved in the retro with the decision each one actually received during implementation. This closes the milestone.
  - **Out of scope for this task**: moving the retro to MemPalace — per AGENTS.md that migration happens in M2, with the M1 retro staying in plain markdown.

---

## Dependencies at a glance

```
T001 ──► T002 ──► T005 ──► T010 ──► T012 ──► T014 ──► T015 ──► T016 ──► T017 ──► T018
  └──► T003 ──► T004 ──► T006 ──┤       │        │
                        └─► T007 ┤       │        │
                        └─► T009 ┤       │        │
                T008 ────────────┘       │        │
                        T011 ────────────┘        │
                        T013 ─────────────────────┘
```

The only thing T001 does not feed is T003 (migrations live under repo-root `migrations/`, not inside `supervisor/`). Everything else fans out from T001 → T002 / T003 → T004 and converges at T012.

## What this task list deliberately excludes

- Real Claude Code invocation, MemPalace wiring, dashboard, CEO agent, hiring, skills.sh — M2+ per [../../AGENTS.md](../../AGENTS.md) §"Scope discipline".
- Multi-channel dispatch beyond `work.ticket.created`.
- `/metrics` endpoint — deferred per [../_context/m1-context.md](../_context/m1-context.md) §"Observability".
- Blue/green rolling deploys — disallowed by FR-018 clarification.
- Per-agent-type concurrency sub-caps — RATIONALE §11 post-M1.
- Operator-tunable fallback poll batch size — NFR-009 fixed at 100.
- Human review as a task — reviews happen in PR review, not in this list.
