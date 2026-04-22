---
description: "Task list for M2.1 — Claude Code invocation"
---

# Tasks: M2.1 — Claude Code invocation

**Feature branch**: `003-m2-1-claude-invocation`
**Inputs**: [spec.md](./spec.md), [plan.md](./plan.md), [../_context/m2.1-context.md](../_context/m2.1-context.md), [../../docs/research/m2-spike.md](../../docs/research/m2-spike.md), [../../AGENTS.md](../../AGENTS.md), [../../docs/retros/m1.md](../../docs/retros/m1.md), the M1 spec and task list under [../m1-event-bus/](../m1-event-bus/).

## Ordering principle

Strict dependency order, same discipline as M1. M2.1 extends the M1 supervisor; it does not rebuild it. After every task the repository is in a working state — the supervisor binary compiles, the existing M1 tests still pass, and the newly-added behaviour is independently verifiable by the completion condition of that task.

Acceptance criteria numbers (A1–A11) in this file refer to the list in `specs/_context/m2.1-context.md` §"Acceptance criteria for M2.1". Spec functional-requirement numbers (FR-1xx) and non-functional numbers (NFR-1xx) refer to `spec.md` §"Requirements".

Each task carries:

- **Depends on** — prior task IDs (or "M1 shipped" for tasks building directly on M1 code with no M2.1 predecessor)
- **Files** — exact paths the task creates or modifies
- **Completion condition** — the concrete passing check after the task
- **Out of scope for this task** — what to resist reaching ahead for

Total: **20 tasks**.

---

## Phase 1: Infrastructure — schema, sqlc, seed content, Dockerfile

- [ ] T001 Author the M2.1 forward migration (schema, role, grants, trigger rewrite)
  - **Depends on**: M1 shipped
  - **Files**: [migrations/20260422000003_m2_1_claude_invocation.sql](../../migrations/20260422000003_m2_1_claude_invocation.sql), [supervisor/cmd/supervisor/migrate.go](../../supervisor/cmd/supervisor/migrate.go) (to pass `GARRISON_AGENT_RO_PASSWORD` into goose)
  - **Completion condition**: `goose -dir migrations postgres "$GARRISON_DATABASE_URL" up` against a fresh `postgres:17` container applies the migration cleanly, producing: (a) the `agent_instances.total_cost_usd NUMERIC(10,6)` nullable column, (b) the `garrison_agent_ro` role with the login permission and `GRANT SELECT` on exactly the M2.1 read surface (`tickets`, `ticket_transitions`, `departments`, `agents`) and no other grants — verifiable with `SELECT privilege_type FROM information_schema.role_table_grants WHERE grantee='garrison_agent_ro'`, (c) the rewritten `emit_ticket_created` function that emits qualified channels of shape `work.ticket.created.<department_slug>.<column_slug>`. `goose down` rolls the migration back cleanly. Seed-row `INSERT` statements are placeholders for now (engineering department with placeholder `workspace_path='/workspaces/engineering'`; engineer agent row with placeholder `agent_md='PLACEHOLDER — seed by T003'`) so downstream tasks have rows to refer to.
  - **Out of scope for this task**: the final engineer `agent.md` text (T003 owns it); any Go code that reads the new column or new role; the Dockerfile change for the `claude` binary (T004); any sqlc regeneration (T002).

- [ ] T002 Add sqlc queries and regenerate `internal/store/`
  - **Depends on**: T001
  - **Files**: [migrations/queries/agent_instances.sql](../../migrations/queries/agent_instances.sql) (extend), [migrations/queries/agents.sql](../../migrations/queries/agents.sql) (new), [migrations/queries/departments.sql](../../migrations/queries/departments.sql) (extend), [migrations/queries/tickets.sql](../../migrations/queries/tickets.sql) (extend), [supervisor/internal/store/](../../supervisor/internal/store/) (regenerated)
  - **Completion condition**: `make sqlc` regenerates the `store` package with new methods: `UpdatePID(id, pid)`, `UpdateInstanceTerminalWithCost(id, status, exitReason, totalCostUSD)`, `GetAgentByDepartmentAndRole(deptID, role)`, `ListActiveAgents()`, `GetDepartmentBySlug(slug)`, `InsertTicketTransition(...)`, `UpdateTicketColumnSlug(id, slug)`. `go build ./...` passes. Generated files are committed. Existing M1 methods remain present and unchanged.
  - **Out of scope for this task**: any callers of the new methods (they arrive in the packages that consume them — T010 for agents, T013 for spawn, etc.); any behavioural change to existing queries.

- [ ] T003 Commit the engineer `agent.md` content and seed it into the migration
  - **Depends on**: T001
  - **Files**: [examples/agents/engineer.md](../../examples/agents/engineer.md), [supervisor/internal/tools/embed-agent-md/main.go](../../supervisor/internal/tools/embed-agent-md/main.go), [supervisor/Makefile](../../supervisor/Makefile) (add `seed-engineer-agent` target), [migrations/20260422000003_m2_1_claude_invocation.sql](../../migrations/20260422000003_m2_1_claude_invocation.sql) (seed section updated)
  - **Completion condition**: `examples/agents/engineer.md` contains the exact text in [plan.md](./plan.md) Appendix A. `make seed-engineer-agent` reads the file and rewrites the `$$...$$`-quoted block in the migration's engineer seed section so the two match byte-for-byte. Running `make seed-engineer-agent && goose up` against a fresh Postgres container results in a row in `agents` where `role_slug='engineer'` and `agent_md` equals the file contents (verified via `psql -c "SELECT agent_md FROM agents WHERE role_slug='engineer'" | diff - examples/agents/engineer.md`, allowing for the psql formatting wrapper). If the embed utility proves fragile, the fallback committed in [plan.md](./plan.md) §Migration-Section-5 is hand-maintained SQL; pick whichever works at task time and document the choice in the commit message.
  - **Out of scope for this task**: any Go code that reads `agents.agent_md` (T010 owns that); any test that spawns a real Claude subprocess using this seed (T015+).

- [ ] T004 Rewrite the supervisor Dockerfile to install the pinned `claude` binary via verified manifest
  - **Depends on**: M1 shipped
  - **Files**: [supervisor/Dockerfile](../../supervisor/Dockerfile)
  - **Completion condition**: the Dockerfile follows the three-stage shape in [plan.md](./plan.md) §"Dockerfile + claude binary install" — `build` (unchanged from M1's Go build step), `claude-install` (GPG fingerprint verification of Anthropic's key, manifest signature verification, SHA256 verification of the `linux-x64-musl` binary for pinned version `2.1.117`), and the final `alpine:3.20` runtime stage with `libgcc`, `libstdc++`, `ripgrep`, `ca-certificates` installed and `USE_BUILTIN_RIPGREP=0`, `DISABLE_AUTOUPDATER=1`, `GARRISON_CLAUDE_BIN=/usr/local/bin/claude`, `GARRISON_MCP_CONFIG_DIR=/var/lib/garrison/mcp` set as env. `docker build -t garrison/supervisor:m2-1-dev supervisor/` succeeds. `docker run --rm garrison/supervisor:m2-1-dev /usr/local/bin/claude --version` prints a version string (`2.1.117` or adjacent) and exits 0. `docker run --rm garrison/supervisor:m2-1-dev /supervisor --version` still prints the supervisor version. Image size is noted in the commit message (expected 280–400 MB given Claude's ~250 MB binary).
  - **Out of scope for this task**: any Go code that uses the `claude` binary (T013 owns the spawn path); any MCP-config-file handling (T007); any integration test harness using the real binary — integration tests use a mock claude (T015).

---

## Phase 2: New leaf packages

- [ ] T005 Implement `internal/spawn/exitreason.go` with constants, helpers, and unit tests
  - **Depends on**: M1 shipped
  - **Files**: [supervisor/internal/spawn/exitreason.go](../../supervisor/internal/spawn/exitreason.go), [supervisor/internal/spawn/exitreason_test.go](../../supervisor/internal/spawn/exitreason_test.go)
  - **Completion condition**: the file declares the static `Exit*` constants listed in [plan.md](./plan.md) Appendix B (`ExitCompleted`, `ExitClaudeError`, `ExitParseError`, `ExitTimeout`, `ExitSupervisorShutdown`, `ExitSpawnFailed`, `ExitNoResult`, `ExitAcceptanceFailed`, `ExitAgentMissing`, plus M1's `ExitSupervisorRestart`, `ExitDepartmentMissing`) and two helper functions (`FormatMCPFailure(serverName, status string) string`, `FormatSignalled(sig syscall.Signal) string`). Unit tests in `exitreason_test.go` cover: canonical format (`TestFormatMCPFailureCanonical` — e.g. `("postgres","failed") == "mcp_postgres_failed"`), unknown-status fall-through (`TestFormatMCPFailureHonoursUnknownStatus`), empty-server defensive fallback (`TestFormatMCPFailureRejectsEmpty` — returns `"mcp_unknown_failed"`), and signal naming (`TestFormatSignalledKnownSignals` for SIGTERM/SIGKILL/SIGINT). `go test ./internal/spawn/... -run TestFormat -count=1` passes.
  - **Out of scope for this task**: touching `spawn.go` to use these constants (T013); any router or pipeline code (T006, T012).

- [ ] T006 Implement `internal/claudeproto` event vocabulary, router interface, and init-health check
  - **Depends on**: M1 shipped
  - **Files**: [supervisor/internal/claudeproto/events.go](../../supervisor/internal/claudeproto/events.go), [supervisor/internal/claudeproto/router.go](../../supervisor/internal/claudeproto/router.go), [supervisor/internal/claudeproto/init.go](../../supervisor/internal/claudeproto/init.go), [supervisor/internal/claudeproto/events_test.go](../../supervisor/internal/claudeproto/events_test.go), [supervisor/internal/claudeproto/init_test.go](../../supervisor/internal/claudeproto/init_test.go)
  - **Completion condition**: the package exports the types and functions enumerated in [plan.md](./plan.md) §`internal/claudeproto` (`InitEvent`, `AssistantEvent`, `UserEvent`, `RateLimitEvent`, `ResultEvent`, `TaskStartedEvent`, `UnknownEvent`; `Router` interface with seven methods; `Route(ctx, rawLine, router) (RouterAction, error)`; `RouterAction` constants; `CheckMCPHealth(servers) (ok bool, offenderName, offenderStatus string)`; `MCPServer struct{Name, Status string}`). All nine unit tests from [plan.md](./plan.md) §"Unit tests → claudeproto" pass: `TestRouteInitEvent`, `TestRouteAssistantEvent`, `TestRouteUserEvent`, `TestRouteRateLimitEvent`, `TestRouteResultEvent`, `TestRouteTaskStartedEvent`, `TestRouteUnknownEventType`, `TestRouteUnknownSystemSubtype`, `TestRouteMalformedJSON`. All five init-health tests pass: `TestCheckMCPHealthAllConnected`, `TestCheckMCPHealthOneFailed`, `TestCheckMCPHealthNeedsAuth`, `TestCheckMCPHealthUnknownStatus`, `TestCheckMCPHealthEmptyServers`. The package has zero I/O — no file, no DB, no subprocess — so all tests run in-memory using captured JSON fixtures from [docs/research/m2-spike.md](../../docs/research/m2-spike.md) §2.1 and §A (verbatim samples).
  - **Out of scope for this task**: wiring the router to the spawn pipeline (T012); any MCP config file handling (T007); any real Claude subprocess invocation.

- [ ] T007 Implement `internal/mcpconfig` per-invocation file lifecycle with unit tests
  - **Depends on**: M1 shipped
  - **Files**: [supervisor/internal/mcpconfig/mcpconfig.go](../../supervisor/internal/mcpconfig/mcpconfig.go), [supervisor/internal/mcpconfig/mcpconfig_test.go](../../supervisor/internal/mcpconfig/mcpconfig_test.go)
  - **Completion condition**: the package exports `Write(ctx, dir, instanceID, supervisorBin, dsn) (path string, err error)` and `Remove(path) error`. `Write` creates a file at `<dir>/mcp-config-<instanceID>.json` containing exactly the JSON shape specified in [plan.md](./plan.md) §`internal/mcpconfig` (one `mcpServers` entry named `postgres` with `command=supervisorBin`, `args=["mcp-postgres"]`, `env.GARRISON_PGMCP_DSN=dsn`). File permission is `0o600`. `Remove` returns nil for both a present and a missing file. Four unit tests pass: `TestWriteHappyPath`, `TestWriteIsolationAcrossInvocations`, `TestRemoveMissingFile`, `TestWriteReturnsErrorOnDiskFull` (using an injected writer that returns `syscall.ENOSPC` — the package exposes its file ops through an internal seam to support this test without a real full disk). `go test ./internal/mcpconfig/... -count=1` passes.
  - **Out of scope for this task**: the `internal/pgmcp` server the config file points at (T008); any spawn-path integration (T013).

- [ ] T008 Implement `internal/pgmcp` in-tree Postgres MCP server with protocol-layer filter and integration tests
  - **Depends on**: T001 (role exists), T002 (sqlc types available for typed row mapping if desired)
  - **Files**: [supervisor/internal/pgmcp/server.go](../../supervisor/internal/pgmcp/server.go), [supervisor/internal/pgmcp/tools.go](../../supervisor/internal/pgmcp/tools.go), [supervisor/internal/pgmcp/auth.go](../../supervisor/internal/pgmcp/auth.go), [supervisor/internal/pgmcp/pgmcp_test.go](../../supervisor/internal/pgmcp/pgmcp_test.go), [supervisor/internal/pgmcp/pgmcp_integration_test.go](../../supervisor/internal/pgmcp/pgmcp_integration_test.go) (build tag `integration`)
  - **Completion condition**: the package exports `Serve(ctx, stdin, stdout, dsn) error`. The server responds to `initialize`, `tools/list`, `tools/call` JSON-RPC methods and rejects anything else with error code `-32601`. Two tools are exposed: `query` (accepts `{"sql": string}`, rejects non-`SELECT`/`EXPLAIN` prefix at the protocol layer with JSON-RPC error code `-32001 "read-only violation"`, returns up to 100 rows as `{"rows": [{col:val}...], "truncated": bool}`) and `explain` (rewrites SQL to `EXPLAIN <sql>`). Stderr output is structured slog with `stream="pgmcp"`. Unit tests in `pgmcp_test.go` (3+ tests) exercise the JSON-RPC envelope parsing and the statement-prefix filter with no DB. Integration tests in `pgmcp_integration_test.go` against a testcontainers Postgres (3 tests: `TestPgmcpQueryHappyPath`, `TestPgmcpRejectsDML`, `TestPgmcpRejectsDDL`) confirm end-to-end behaviour against the real `garrison_agent_ro` role from T001. `go test -tags integration ./internal/pgmcp/... -count=1` passes. Uses only stdlib plus the already-locked `pgx/v5`; no new dependencies.
  - **Out of scope for this task**: the `supervisor mcp-postgres` subcommand that invokes `Serve` (T014); any spawn-path MCP config file wiring (T007 already owns the file; T013 owns the use).

---

## Phase 3: Config, agents cache, and spawn surgery

- [ ] T009 Extend `internal/config` with M2.1 env vars and validation
  - **Depends on**: T004 (Dockerfile sets the new env vars so tests can reference them)
  - **Files**: [supervisor/internal/config/config.go](../../supervisor/internal/config/config.go), [supervisor/internal/config/config_test.go](../../supervisor/internal/config/config_test.go)
  - **Completion condition**: the `Config` struct gains exported fields for `ClaudeBin`, `ClaudeModel`, `ClaudeBudgetUSD` (default `0.05`, validated in `(0, 1)`), `MCPConfigDir` (default `/var/lib/garrison/mcp/`), `AgentROPassword` (required), `UseFakeAgent` (derived from whether `GARRISON_FAKE_AGENT_CMD` is set). An unexported `agentROPassword` plus an exported `(*Config).AgentRODSN() string` method composes the read-only DSN by swapping credentials in `DatabaseURL` for `garrison_agent_ro` / `AgentROPassword`. On `Load`, `ClaudeBin` is resolved via `exec.LookPath("claude")` if `GARRISON_CLAUDE_BIN` is unset; failure to resolve produces the exact error string `"config: cannot find claude binary on $PATH and GARRISON_CLAUDE_BIN is unset"`. `MCPConfigDir` is `os.MkdirAll`'d and a writability check runs. `GARRISON_FAKE_AGENT_CMD` remains supported (sets `UseFakeAgent=true`). New unit tests: `TestLoadResolvesClaudeBin`, `TestLoadFailsWhenClaudeMissing`, `TestLoadParsesBudgetUSD`, `TestLoadRejectsOutOfRangeBudget`, `TestAgentRODSNComposition`, `TestLoadHonoursFakeAgentFlag`. All existing M1 config tests still pass.
  - **Out of scope for this task**: using any of these fields from other packages (T013 wires spawn, T014 wires main.go); any interaction with the real `claude` binary beyond `exec.LookPath`.

- [ ] T010 Implement `internal/agents` cache
  - **Depends on**: T002 (sqlc has `GetAgentByDepartmentAndRole`), T003 (seed rows exist for integration tests)
  - **Files**: [supervisor/internal/agents/agents.go](../../supervisor/internal/agents/agents.go), [supervisor/internal/agents/agents_test.go](../../supervisor/internal/agents/agents_test.go)
  - **Completion condition**: exports `NewCache(ctx, queries) (*Cache, error)` which loads all `status='active'` agents into a map keyed by `(departmentID, roleSlug)`, and `(*Cache).GetForDepartmentAndRole(ctx, deptID, role) (Agent, error)` which returns the cached `Agent` struct (`ID`, `DepartmentID`, `Role`, `AgentMD`, `Model`, `ListensFor`, `PalaceWing`) or a typed `ErrAgentNotFound`. Hot-reload is explicitly not implemented — documented in a comment citing this plan's "Deferred" section. Two unit tests: `TestCachePopulatesFromQuerier` (stub querier returns two rows; both are accessible), `TestCacheReturnsNotFoundForMissing`. An integration test (`//go:build integration`) against the T003 seed verifies the engineer row loads with the expected `agent_md` length and `model='claude-haiku-4-5-20251001'`.
  - **Out of scope for this task**: reloading on SIGHUP (deferred); any UI for editing agents (M3+); using the cache from spawn.go (T013).

- [ ] T011 Implement `killProcessGroup` helper in `internal/spawn/pgroup.go` with a subprocess-child unit test
  - **Depends on**: M1 shipped
  - **Files**: [supervisor/internal/spawn/pgroup.go](../../supervisor/internal/spawn/pgroup.go), [supervisor/internal/spawn/pgroup_test.go](../../supervisor/internal/spawn/pgroup_test.go)
  - **Completion condition**: the file exports `killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error` that wraps `syscall.Kill(-cmd.Process.Pid, sig)` with ESRCH-tolerance per [plan.md](./plan.md) §"Process-group termination". A unit test `TestKillProcessGroupTerminatesChildren` spawns `sh -c 'sleep 30 & echo $! > /tmp/garrison-pgroup-test-<rand>; wait'` with `Setpgid: true`, reads the child's PID from the file, calls `killProcessGroup(cmd, syscall.SIGTERM)`, and verifies within 1 second that (a) the parent process is gone and (b) the child PID (the backgrounded `sleep`) is also gone (`syscall.Kill(childPid, 0)` returns `syscall.ESRCH`). A second test `TestKillProcessGroupTolerantOfMissingGroup` calls the helper on an already-exited cmd and asserts the returned error is either nil or a `debug`-logged `ESRCH` — not a panic and not a bubble-up error. Tests tagged Linux-only (`//go:build linux`). `go test ./internal/spawn/... -run TestKillProcessGroup -count=1` passes on Linux.
  - **Out of scope for this task**: integrating this helper into `spawn.Spawn` (T013); tests that exercise Claude or mock-claude (T015+).

- [ ] T012 Implement `internal/spawn/pipeline.go` — NDJSON reader, router implementation, adjudicator
  - **Depends on**: T005, T006
  - **Files**: [supervisor/internal/spawn/pipeline.go](../../supervisor/internal/spawn/pipeline.go), [supervisor/internal/spawn/pipeline_test.go](../../supervisor/internal/spawn/pipeline_test.go)
  - **Completion condition**: exports `Run(ctx, stdout io.Reader, instanceID pgtype.UUID, logger *slog.Logger, onBail func(reason string)) (Result, error)` that reads stdout line-by-line with a 1 MiB buffer, calls `claudeproto.Route` on each non-empty line, and returns a `Result` struct with fields `TotalCostUSD string`, `TerminalReason string`, `IsError bool`, `SessionID string`, `ResultSeen bool`, `AssistantSeen bool`, `MCPBailed bool`, `MCPOffenderName string`, `MCPOffenderStatus string`, `ParseError bool`. Also exports `Adjudicate(result Result, wait WaitDetail, helloTxtOK bool) (status, exitReason string)` as a pure function implementing the decision table in [plan.md](./plan.md) §`pipeline.Adjudicate`. Six unit tests from [plan.md](./plan.md) §"Unit tests → spawn/pipeline" pass: `TestAdjudicateSuccess`, `TestAdjudicateClaudeError`, `TestAdjudicateNoResult`, `TestAdjudicateAcceptanceFailed`, `TestAdjudicateTimeout`, `TestAdjudicateShutdown`. An additional test `TestPipelineRunRoutesAllEvents` feeds a `bytes.Buffer` containing a canned NDJSON stream (init → assistant → user → rate_limit → result) and asserts the returned `Result` matches expectations. `go test ./internal/spawn/... -run "TestAdjudicate|TestPipelineRun" -count=1` passes.
  - **Out of scope for this task**: the `Spawn` orchestration (T013); process-group termination (T011 owns the helper; T013 uses it); any test that exercises a real subprocess.

- [ ] T013 Rewrite `internal/spawn/spawn.go` for the real-Claude path (dual-mode with fake-agent escape hatch)
  - **Depends on**: T002, T005, T006, T007, T009, T010, T011, T012
  - **Files**: [supervisor/internal/spawn/spawn.go](../../supervisor/internal/spawn/spawn.go) (rewritten body), [supervisor/internal/spawn/template.go](../../supervisor/internal/spawn/template.go) (retained; fake-agent path only now), [supervisor/internal/spawn/lifecycle_test.go](../../supervisor/internal/spawn/lifecycle_test.go) (M1 tests updated for the dual-mode split)
  - **Completion condition**: the rewritten `Spawn` follows the 12-step sequence in [plan.md](./plan.md) §`internal/spawn`. Both paths coexist: if `deps.UseFakeAgent` is true, the existing M1 flow runs unchanged; otherwise the real-Claude flow runs — resolve agent via `agents.Cache`, write MCP config via `mcpconfig.Write`, build the argv from [../_context/m2.1-context.md](../_context/m2.1-context.md) §"Spawn contract" exactly (including `--permission-mode bypassPermissions`), set `SysProcAttr.Setpgid = true`, `cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }`, `cmd.WaitDelay = 5 * time.Second` (for timeout-path escalation), start the subprocess, `UpdatePID`, run `pipeline.Run`, adjudicate, write the widened terminal transaction (`UpdateInstanceTerminalWithCost` + `InsertTicketTransition` (succeeded path only) + `UpdateTicketColumnSlug` (succeeded path only) + `MarkEventProcessed`, all in one tx), `defer mcpconfig.Remove(path)`. The MCP-bail path signals via `killProcessGroup(cmd, SIGTERM)`, waits 2 s, SIGKILL-group if still alive — NFR-106 budget. Spawn-failed path (MCP config write error) writes the terminal row with `exit_reason='spawn_failed'`, marks the event processed, logs at error, returns nil. All existing M1 `spawn` unit tests pass unchanged. A new `TestSpawnDispatchesOnFakeAgentFlag` test confirms the dual-mode routing.
  - **Out of scope for this task**: running a real `claude` binary (T015+); wiring `Spawn` into `cmd/supervisor/main.go` (T014); any end-to-end assertion beyond the dual-mode routing.

---

## Phase 4: Wire-up

- [ ] T014 Add `supervisor mcp-postgres` subcommand and wire M2.1 into `cmd/supervisor/main.go`
  - **Depends on**: T008, T009, T010, T013
  - **Files**: [supervisor/cmd/supervisor/mcp_postgres.go](../../supervisor/cmd/supervisor/mcp_postgres.go), [supervisor/cmd/supervisor/main.go](../../supervisor/cmd/supervisor/main.go), [supervisor/integration_test.go](../../supervisor/integration_test.go) (channel-name and seed-row updates for M1 tests), [supervisor/chaos_test.go](../../supervisor/chaos_test.go) (channel-name updates), [supervisor/test_helpers_test.go](../../supervisor/test_helpers_test.go) (add department-slug seeding helper for any M1 test that inserts a ticket)
  - **Completion condition**: when `os.Args[1] == "mcp-postgres"`, the binary delegates to `pgmcp.Serve(ctx, os.Stdin, os.Stdout, os.Getenv("GARRISON_PGMCP_DSN"))` and exits with its return value. Manual check: `echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | GARRISON_PGMCP_DSN="$TEST_DSN" ./bin/supervisor mcp-postgres` returns a JSON-RPC result on stdout within 1 second. In normal run mode, `main.go` now: loads the extended `config.Config` and validates it (including the `exec.LookPath("claude")` resolution), `os.MkdirAll`s `cfg.MCPConfigDir` and the engineering workspace path (read from the seeded department row after pgdb.Connect), constructs `agents.NewCache`, passes the extended `spawn.Deps` into the dispatcher, registers the handler under `work.ticket.created.engineering.todo` (not the M1 unqualified channel). `./bin/supervisor` with valid env vars against a T001-migrated Postgres starts cleanly, `/health` returns 200, and slog shows LISTEN on the qualified channel. All M1 integration tests that used the M1 unqualified channel fail fast with a clear "migration schema mismatch" error — which is correct because T001's migration switched the channel; those M1 tests are updated to reflect the new channel name (one-line change per test).
  - **Out of scope for this task**: running a real Claude subprocess (T015 does that); any new integration tests (T015+); the M2.1 retro (T020).

---

## Phase 5: Integration tests

- [ ] T015 Build mock-claude test binary and land the golden-path integration test
  - **Depends on**: T013, T014
  - **Files**: [supervisor/internal/spawn/mockclaude/main.go](../../supervisor/internal/spawn/mockclaude/main.go), [supervisor/internal/spawn/mockclaude/scripts/helloworld.ndjson](../../supervisor/internal/spawn/mockclaude/scripts/helloworld.ndjson), [supervisor/integration_test.go](../../supervisor/integration_test.go) (extend), [supervisor/test_helpers_test.go](../../supervisor/test_helpers_test.go) (extend), [supervisor/internal/testdb/testdb.go](../../supervisor/internal/testdb/testdb.go) (add `SeedM21` helper)
  - **Completion condition**: a Go-built `mockclaude` binary emits a canned NDJSON stream from a chosen script file, drains flags identical to the real `claude` CLI (so the supervisor's argv works against it unmodified), and actually performs the file-write side effect (writes `hello.txt` with the ticket ID it reads out of its system prompt or task description argv) so acceptance criterion A7 — "hello.txt exists and contains the ticket ID" — passes against the mock. `testdb.SeedM21` helper creates the engineering department and engineer agent rows from the T001+T003 migration output. `TestM21HelloWorldEndToEnd` (tag `integration`) inserts a ticket against a testcontainers Postgres, waits up to 10 s, and asserts: hello.txt exists in the workspace with the ticket ID as contents; `agent_instances` has one row with `status='succeeded'`, `exit_reason='completed'`, `pid` populated, `total_cost_usd=0.003` (the value hard-coded into the canned result event); one `ticket_transitions` row `todo→done` with `hygiene_status=NULL`; the `event_outbox` row has `processed_at` set. This single test covers acceptance criteria A1–A9. `go test -tags integration ./... -run TestM21HelloWorldEndToEnd -count=1` passes.
  - **Out of scope for this task**: failure-path tests (T016); chaos tests (T018); the acceptance-evidence document (T019).

- [ ] T016 Integration tests for Claude failure paths
  - **Depends on**: T015
  - **Files**: [supervisor/integration_test.go](../../supervisor/integration_test.go) (extend), [supervisor/internal/spawn/mockclaude/scripts/](../../supervisor/internal/spawn/mockclaude/scripts/) (add scripts: `mcp-bail.ndjson`, `parse-error.ndjson`, `no-result.ndjson`, `result-error.ndjson`, `hello-missing.ndjson`, `hello-wrong-contents.ndjson`, `spawn-failed.setup.sh`)
  - **Completion condition**: six new `TestM21...` tests pass under `-tags integration`, each verifying one failure mode from [plan.md](./plan.md) §"Testing plan → Integration tests":
    - `TestM21UnknownMCPStatusFailsClosed` — mock emits `init` with `mcp_servers=[{postgres,"banana"}]`; expected `exit_reason='mcp_postgres_banana'`, process-group terminated.
    - `TestM21ParseErrorBails` — mock emits `init` then a malformed line; expected `exit_reason='parse_error'`.
    - `TestM21NoResultEventFailsClosed` — mock emits `init` then exits with code 0 with no `result`; expected `exit_reason='no_result'`, `total_cost_usd` NULL, no transition (clarify-session Q3).
    - `TestM21ClaudeErrorResult` — mock emits `result` with `is_error=true`; expected `exit_reason='claude_error'`, cost captured, no transition.
    - `TestM21AcceptanceFailedWhenHelloTxtMissing` — mock emits successful `init+result` but does not write the file; expected `exit_reason='acceptance_failed'`.
    - `TestM21AcceptanceFailedWhenHelloTxtContentsWrong` — mock writes the file with `"oops"` contents; expected `exit_reason='acceptance_failed'`.
    - `TestM21SpawnFailedOnConfigWriteError` — test points `GARRISON_MCP_CONFIG_DIR` at a chmod-0500 dir; expected `exit_reason='spawn_failed'`, dispatcher continues, a subsequent ticket with a repaired config dir runs normally (clarify-session Q2).
    Dispatcher-continuation property is asserted in the final test. `go test -tags integration ./... -run "TestM21(UnknownMCPStatus|ParseError|NoResult|ClaudeError|AcceptanceFailed|SpawnFailed)" -count=1` passes.
  - **Out of scope for this task**: supervisor-observed behaviours not tied to a Claude failure (T017); chaos scenarios (T018).

- [ ] T017 Integration tests for supervisor-observed behaviours
  - **Depends on**: T015
  - **Files**: [supervisor/integration_test.go](../../supervisor/integration_test.go) (extend), [supervisor/internal/spawn/mockclaude/scripts/](../../supervisor/internal/spawn/mockclaude/scripts/) (add `ratelimit.ndjson`, `timeout.ndjson`)
  - **Completion condition**: six new `TestM21...` tests pass under `-tags integration`:
    - `TestM21TimeoutFiresProcessGroupKill` — mock emits `init` then sleeps past `SubprocessTimeout` (test overrides to 2 s); expected `exit_reason='timeout'`, process group terminated (the mock's child `sleep` subprocess also exits). Verifies NFR-101 "from cmd.Start()" semantics — the 2 s budget runs from spawn, not from first assistant event.
    - `TestM21PIDBackfilled` — happy-path run, after terminal, `agent_instances.pid IS NOT NULL` (M1 retro §4 fix, A4 support).
    - `TestM21RateLimitEventLogged` — mock script emits a `rate_limit_event` between `init` and `result`; a slog-capturing handler records the event's six fields; run succeeds (SC-109).
    - `TestM21ConcurrencyCapOneSerializes` — two tickets inserted within 100 ms, cap=1; first runs, second defers per M1 cap-check, fallback poll picks up the second after the first terminates; both succeed; in-flight count never exceeds 1 (SC-106).
    - `TestM21SessionPersistenceSideEffectAbsent` — HOME-scoped for the test; after the happy path completes, `<home>/.claude/projects/*/sessions/` has no files (NFR-110, spike §2.8).
    - `TestM21GracefulShutdownWithInflight` — start supervisor, insert ticket, wait for init to land, send SIGTERM to the supervisor, assert: (a) mock claude's process group received SIGTERM (verified via mock-claude self-instrumentation that writes a marker file on receipt), (b) the terminal row was written (either `succeeded` if the mock flushed `result` in time, or `failed/supervisor_shutdown` otherwise), (c) zero rows remain with `status='running'`, (d) supervisor exits within shutdown grace, (e) no orphan child processes of the former claude subprocess. Acceptance criterion A10.
    `go test -tags integration ./... -run "TestM21(Timeout|PIDBackfilled|RateLimit|ConcurrencyCap|SessionPersistence|GracefulShutdown)" -count=1` passes.
  - **Out of scope for this task**: chaos / failure-mode tests (T018); running the real `claude` binary (deferred to acceptance, T019).

---

## Phase 6: Chaos tests

- [ ] T018 Chaos tests — broken MCP config (A11), pgmcp dies mid-run, external SIGKILL on the Claude subprocess
  - **Depends on**: T014, T015
  - **Files**: [supervisor/chaos_test.go](../../supervisor/chaos_test.go) (extend; build tag `chaos`)
  - **Completion condition**: three new chaos tests pass under `-tags chaos`:
    - `TestM21BrokenMCPConfigBailsWithin2Seconds` — acceptance criterion A11. Test fixture writes an MCP config pointing the `postgres` server's `command` at `/bin/does-not-exist`, forces that config into a spawn (either by seeding a second test-only department with a bad config or by injecting via a test-only hook on `mcpconfig.Write`), inserts a ticket, and measures wall-clock time from the real claude binary's init emission to the moment the process group receives SIGTERM. Assertion: ≤ 2 000 ms (NFR-106). Terminal row: `status='failed'`, `exit_reason='mcp_postgres_failed'`. No `hello.txt`. No `ticket_transitions` row. `event_outbox.processed_at` set. This test uses the **real** `claude` binary pinned in T004's Dockerfile — or skips with a clear message if `claude` is not on `$PATH` in the test environment.
    - `TestM21ChaosPgmcpDiesMidRun` — happy-path scaffold with the real `claude` binary and the real `pgmcp` subprocess; after init lands, the test externally kills the pgmcp PID (discoverable via `/proc/<claude-pid>/task/*/children` scraping or via a test-only `pgmcp` build that writes its PID to a known path). Assertion: Claude either emits an errored tool_result or exits; supervisor records either `exit_reason='claude_error'` or `exit_reason='no_result'` — the test accepts both and documents which one was observed (the spike did not characterize this; this test is how M2.1 pins the behaviour).
    - `TestM21ChaosClaudeSigkilledExternally` — external SIGKILL to the Claude subprocess while it is running. Assertion: `status='failed'`, `exit_reason='signaled_KILL'` (or the exact string `FormatSignalled` emits), no stale `running` row, `event_outbox.processed_at` set. This is the M1 chaos test's real-Claude variant.
    `go test -tags chaos ./... -run "TestM21(BrokenMCPConfig|ChaosPgmcpDies|ChaosClaudeSigkilled)" -count=1` passes when the real `claude` binary is on `$PATH`; tests skip cleanly otherwise with a message instructing the operator how to install it.
  - **Out of scope for this task**: any test that does not exercise a failure mode; the scripted 11-step acceptance run (T019).

---

## Phase 7: Acceptance + retro

- [ ] T019 Execute the 11 M2.1 acceptance steps against the release image and record evidence
  - **Depends on**: T016, T017, T018
  - **Files**: [specs/003-m2-1-claude-invocation/acceptance-evidence.md](./acceptance-evidence.md) (new), no code changes
  - **Completion condition**: `make docker` builds `garrison/supervisor:m2-1` cleanly; a run script stands up a fresh `postgres:17` container, applies migrations (including T001), sets `GARRISON_AGENT_RO_PASSWORD` to a test value, launches the release image, inserts the test ticket, and observes the 11 acceptance criteria from [../_context/m2.1-context.md](../_context/m2.1-context.md) §"Acceptance criteria for M2.1" in order. `acceptance-evidence.md` records, per step, the exact command run and the exact observation (log lines, SQL query output, file contents) — one section per A1–A11. After A1–A11 are verified on a single ticket, the script inserts ten additional sequential tickets (each waiting for the previous to reach terminal status) and appends a section `Cost sanity sample (SC-107)` to the evidence document recording the output of `SELECT COUNT(*), SUM(total_cost_usd), AVG(total_cost_usd), MAX(total_cost_usd) FROM agent_instances WHERE status = 'succeeded';` — the sum is the observed aggregate spend for ten hello-world runs. SC-107 is a soft gate per the spec; values under $0.50 pass the gate, values over $0.50 are logged in the retro per the spec's own framing. The real `claude` binary (2.1.117 per T004) is used end-to-end; no mock claude in this run. If any step fails, the task does not open a new task to fix — it opens a focused patch against the relevant prior task's files, commits, and re-runs all 11 from the top until every step passes cleanly. The evidence document's final line reads `All 11 acceptance criteria pass. Ship gate cleared.` or is rejected.
  - **Out of scope for this task**: writing new code features (any failure is fixed in the prior task that owns the relevant files); the retro (T020); any M2.2 work (MemPalace).

- [ ] T020 Write the M2.1 retro at `docs/retros/m2-1.md`
  - **Depends on**: T019
  - **Files**: [docs/retros/m2-1.md](../../docs/retros/m2-1.md)
  - **Completion condition**: the retro follows the structure established by [docs/retros/m1.md](../../docs/retros/m1.md): sections for "What shipped", "What the spec/plan got wrong (and what we learned)", "Dependencies added outside the locked list" (expected to be empty for M2.1 per plan commitments — any addition must be justified), "Plan-level open questions — resolved", "Open questions deferred to M2.2". Per the M2.1 prompt, the retro MUST additionally answer: (a) did the M2 spike pay off — count of M2.1 issues prevented by spike findings vs. discovered during implement, validation-rate ≥ 50 % target from `m2.1-context.md` §"What we're watching for at ship"; (b) did init-event parsing hold up under real load — any deviations from spike §A behaviour; (c) did the in-tree Go Postgres MCP server design hold up — any crashes, auth surprises, read-only bypasses; (d) did `total_cost_usd` match Anthropic's billing dashboard when cross-checked; (e) did process-group termination work cleanly — zombie-process count and grace-period edge cases observed; (f) surprises from running real Claude Code at volume that the spike missed. Retro is plain markdown (MemPalace arrives in M2.2; M2.1 retros cannot dogfood it). The document ends with a "Ready to start M2.2" marker or an explicit "M2.1 has unresolved issues blocking M2.2" flag. `git log` shows the commit landing this file on the M2.1 branch before merge.
  - **Out of scope for this task**: any code changes (the retro is a ship artifact, not a fix vehicle); any M2.2 work; editing [docs/retros/m1.md](../../docs/retros/m1.md) beyond appending a cross-reference if one proves warranted.
