# Feature specification: M2.1 — Claude Code invocation

**Feature branch**: `003-m2-1-claude-invocation`
**Created**: 2026-04-22
**Status**: Draft
**Input**: `/speckit.specify` — "Produce the M2.1 specification for Garrison: real Claude Code invocation with Postgres MCP, shipping end-to-end functional."

This spec is bound by `specs/_context/m2.1-context.md`. Decisions settled there — the spawn contract, the stream-json event routing table, the termination contract, the scope exclusions, and the spike-derived bindings — are inputs, not questions. Where this spec cites a constraint, the authoritative text lives in that file; consult it rather than the paraphrase here.

Tool behaviour of the `claude` CLI and of the Postgres MCP surface is characterized in `docs/research/m2-spike.md`. Where this spec references a specific behaviour (e.g. "the init event reports `status: connected`"), the citation is into the spike. The spec does not re-derive behaviour; it translates the context file's binding constraints into testable, user-story-shaped requirements.

M2.1 builds on the M1 event-bus core. The supervisor, event outbox, dispatcher, LISTEN/poll machinery, concurrency accounting, graceful shutdown, and recovery query all remain as shipped in M1 (see `specs/m1-event-bus/spec.md` and `docs/retros/m1.md`). M2.1 replaces the M1 fake-agent subprocess with a real `claude` invocation and adds Postgres MCP wiring; everything else about the supervisor's architecture is inherited.

## Clarifications

### Session 2026-04-22

- Q: When does the 60-second subprocess timeout clock start? → A: At `cmd.Start()`. A single timeout context covers MCP boot, init-event handshake, and LLM work together; NFR-106's 2-second MCP-bail budget fires inside this window.
- Q: What does the supervisor do when the per-invocation MCP config file cannot be written (disk full, permission denied, read-only filesystem)? → A: Record and continue — write a failed `agent_instances` row with `exit_reason='spawn_failed'`, set `event_outbox.processed_at`, log at error level, and keep the dispatcher serving other events. No ticket transition; no retry. One bad event does not take down the supervisor.
- Q: What does the supervisor record when the subprocess exits without the NDJSON parser having seen a terminal `result` event? → A: Fail closed. `agent_instances.status='failed'`, `exit_reason='no_result'`, regardless of exit code; `total_cost_usd` stays NULL; no ticket transition. Matches FR-108's fail-closed stance on unknown MCP status and AGENTS.md's "do not rely on Claude Code's exit code" rule.

## User scenarios and testing (mandatory)

### User story 1 — real Claude Code agent writes hello.txt end-to-end (priority: P1)

The operator inserts an engineering ticket. The supervisor spawns a real Claude Code subprocess wired to a Postgres MCP server. The agent reads the ticket, writes `hello.txt` containing the ticket id to its workspace, and exits successfully. The supervisor records the transition to `done`, writes a succeeded `agent_instances` row with captured cost data, and marks the event processed.

**Why this priority**: this is the entire thesis of M2.1 made concrete. It is the smallest slice that proves (a) Claude Code can be driven non-interactively by the supervisor, (b) the stream-json protocol can be parsed reliably, (c) a Postgres MCP server can be wired per invocation, (d) the supervisor-as-sole-writer pattern holds when the worker is a real LLM. If this story ships, M2.1 ships.

**Independent test**: against a freshly-migrated Postgres with the seeded engineering department and engineer agent, insert one ticket with objective `"write hello"`. Observe: within the subprocess timeout, `hello.txt` exists in the engineering workspace with the ticket id as its contents; one `agent_instances` row has `status='succeeded'`, `exit_reason='completed'`, a non-null `pid`, and a populated `total_cost_usd`; one `ticket_transitions` row records `todo → done`.

**Acceptance scenarios**:

1. **Given** a clean database with M1+M2.1 migrations applied, the engineering department and engineer agent seeded, the `claude` binary on `$PATH`, and the supervisor running, **When** the operator inserts a ticket targeting `engineering` at column `todo`, **Then** within 2 seconds the supervisor emits a spawn event, starts a `claude` subprocess with the contract from "Spawn contract" in `m2.1-context.md`, and inserts an `agent_instances` row with `status='running'` and a non-null `pid`.
2. **Given** the Claude subprocess has started, **When** the supervisor reads the first NDJSON line from its stdout, **Then** it parses the `system`/`init` event, confirms `mcp_servers` contains one entry named `postgres` with `status == "connected"`, and allows the subprocess to proceed. The `query` tool (or equivalently named Postgres MCP tool) is present in the init event's `tools[]` per spike §A.
3. **Given** the MCP health check passed and the agent executes, **When** Claude emits its terminal `result` event with `terminal_reason == "completed"` and `is_error == false`, **Then** the supervisor captures `total_cost_usd` from the result, verifies `hello.txt` exists in the engineering workspace containing the ticket id (exact string match; trailing newline permitted), and writes a single terminal transaction that: updates `agent_instances` to `status='succeeded'`, `exit_reason='completed'`, sets `finished_at`, populates `total_cost_usd`; inserts a `ticket_transitions` row for `todo → done` with `triggered_by_agent_instance_id` set and `hygiene_status` NULL; sets `event_outbox.processed_at`.
4. **Given** the supervisor also observes non-terminal events on the stream (`assistant`, `user` tool_result, any `rate_limit_event`), **When** those events are received, **Then** the supervisor routes each per "Stream-json event routing" in `m2.1-context.md` — structured slog lines with `agent_instance_id`, warn level for `rate_limit_event` and tool-result errors — and does not interpret them for dispatch decisions.

---

### User story 2 — supervisor bails fast on broken MCP configuration (priority: P2)

The Postgres MCP server for an invocation fails to start (bad command, missing binary, mis-keyed connection string). The supervisor detects this from the `system`/`init` event's `mcp_servers[].status`, kills the Claude subprocess's entire process group within 2 seconds of receiving the init event, and records a failed `agent_instances` row with a diagnostic `exit_reason`. No ticket transition is written.

**Why this priority**: the entire MCP health model for Garrison rests on init-event parsing. Per spike §2.5 and §A, `--strict-mcp-config` with a broken server still exits 0 and Claude tolerates the failure silently; without the init-event check the supervisor would allow an agent to run without the tools it needs, burn tokens, and produce confusing failures. Making this story pass is what validates the "init-parse is the contract" decision from the M2 follow-up experiment. It is lower priority than User Story 1 only because the happy path must work first; functionally it is a ship blocker equal to US1.

**Independent test**: configure the supervisor to inject a deliberately-broken per-invocation MCP config (e.g. the command points at `/bin/does-not-exist`, or the Postgres connection URL is wrong) for a test ticket. Insert the ticket. Observe: the supervisor parses the init event, sees `mcp_servers` contains an entry with `status != "connected"`, signals the Claude process group within 2 seconds of receiving the init event, and writes an `agent_instances` row with `status='failed'` and an `exit_reason` of the form `mcp_<server>_<status>` (e.g. `mcp_postgres_failed`). No `hello.txt` is written. No ticket transition row appears. The `event_outbox` row is marked processed so the dispatcher does not retry.

**Acceptance scenarios**:

1. **Given** a test spawn wired to a broken per-invocation MCP config, **When** Claude emits the `system`/`init` event and any `mcp_servers[]` entry has `status != "connected"`, **Then** the supervisor sends SIGTERM to the Claude process group (`syscall.Kill(-pgid, SIGTERM)`) within 2 seconds of init-event receipt, waits a 2-second grace, then sends SIGKILL to the same group if the process has not exited, per "Termination contract" in `m2.1-context.md`.
2. **Given** the process group has been terminated on MCP health failure, **When** the supervisor writes the terminal transaction, **Then** the `agent_instances` row has `status='failed'`, `exit_reason` of the form `mcp_<server>_<status>` matching the offending server's reported status, `finished_at` set, and `total_cost_usd` is NULL (no successful cost capture occurred).
3. **Given** an `mcp_servers[]` entry has a status value outside the observed `{connected, failed, needs-auth}` enum (forward-compatibility case), **When** the supervisor evaluates it, **Then** it fails closed — the same termination path runs and `exit_reason` records the unknown status string verbatim as `mcp_<server>_<status>`. See AGENTS.md "what agents should not do" on MCP exit codes.
4. **Given** a broken MCP config is detected, **When** the terminal transaction completes, **Then** the associated `event_outbox.processed_at` is set so the dispatcher does not retry the same bad event on the next fallback poll cycle.

---

### User story 3 — graceful shutdown and termination integrity with real subprocesses (priority: P2)

The operator sends SIGTERM to the supervisor while a Claude Code agent is mid-run. The supervisor stops accepting new events, signals each in-flight Claude subprocess by process group (not PID), allows a grace window for the subprocess to flush its terminal `result`, forces SIGKILL after grace, and writes terminal rows via a context that survives the root cancellation. Separately, on subprocess-timeout expiry, the same process-group termination applies with a distinct `exit_reason`.

**Why this priority**: M1 established the supervisor shutdown path with `context.WithoutCancel` (M1 retro §2) and a 5-second per-subprocess grace, but the fake agent was a trivial `sh -c`. Real Claude Code subprocesses spawn child processes of their own (MCP servers and tool-execution helpers per spike §2.7), so PID-level signals are insufficient and the process-group termination rule from AGENTS.md concurrency rule 7 becomes a correctness requirement, not a nicety. This story confirms the M1 shutdown machinery behaves correctly under M2.1's heavier subprocesses.

**Independent test**: start the supervisor. Insert a ticket. Wait until the Claude subprocess is observed running (an `agent_instances` row with `status='running'` and a non-null `pid`). Send SIGTERM to the supervisor. Observe: the entire Claude process group receives SIGTERM, the supervisor waits its configured grace, sends SIGKILL to the group if still alive, writes a terminal `agent_instances` row (`status='failed'` with `exit_reason='supervisor_shutdown'`, or `status='succeeded'` with `exit_reason='completed'` if Claude emitted its result within the grace), and marks the `event_outbox` row processed. No child process of the Claude subprocess survives the supervisor.

**Acceptance scenarios**:

1. **Given** a running supervisor with at least one Claude subprocess in-flight, **When** the operator sends SIGTERM, **Then** the supervisor cancels the root context, sends SIGTERM to each Claude process group via `syscall.Kill(-pgid, SIGTERM)` — never to the bare PID — and waits the per-subprocess grace (5 seconds per "Termination contract" in `m2.1-context.md`) before escalating to SIGKILL on the same process group.
2. **Given** the supervisor's root context is cancelled, **When** the terminal transaction runs, **Then** it uses `context.WithoutCancel(ctx)` plus `TerminalWriteGrace` to succeed against a live database connection, and the `agent_instances` row reaches a terminal status — no row is left in `status='running'` after the supervisor exits. See M1 retro §2 and AGENTS.md concurrency rule 6.
3. **Given** a Claude subprocess exceeds its configured timeout (default 60 seconds; see NFR-101), **When** the context expires, **Then** the same process-group termination path executes and the `agent_instances` row records `exit_reason='timeout'`.
4. **Given** the Claude subprocess exits cleanly within the shutdown grace after receiving SIGTERM, **When** its terminal `result` event was the last thing emitted to stdout, **Then** the supervisor records `status='succeeded'` with `exit_reason='completed'`. Otherwise — SIGTERM-then-SIGKILL was required, or no `result` event was seen — the supervisor records `status='failed'` with `exit_reason='supervisor_shutdown'`.

---

### Edge cases

- **Claude emits `stderr` output alongside the NDJSON stream**: captured to slog with `stream="claude_stderr"` structured field. Not parsed. Stderr lines do not trigger dispatch behaviour.
- **Claude emits a malformed JSON line on stdout** (line is not valid JSON): the supervisor bails the subprocess immediately via process-group termination; `exit_reason='parse_error'`. Per "Stream-json event routing" in `m2.1-context.md`.
- **Claude emits an `assistant` event containing a `tool_use` for a tool that is not the Postgres `query` or a Claude-native file write**: logged observationally; no supervisor dispatch change. The agent succeeding or failing is determined by the terminal `result` event and by whether `hello.txt` exists with the expected content at terminal-transaction time, not by enumerating the tool calls.
- **Claude emits a `user` tool_result event with `is_error=true`**: logged at warn level with the error detail. The supervisor does not bail — the agent may recover on the next turn. Only the terminal `result` event decides outcome.
- **Claude emits a `rate_limit_event` mid-run**: logged at warn level with all fields (`utilization`, `resetsAt`, `rateLimitType`, `surpassedThreshold`). No supervisor-side backoff in M2.1; backoff logic is M6.
- **Claude emits an event `type` not present in the routing table**: per "Stream-json event routing" in `m2.1-context.md` the default is "log at warn level with the raw line; do not crash". Specific forward-compatibility behaviour flagged in Q3 below.
- **The terminal `result` event reports `is_error=true`**: `agent_instances.status='failed'`, `exit_reason='claude_error'`, `total_cost_usd` captured from the result (Claude populates this even on errored results). No `ticket_transitions` row is written; the ticket remains at `todo`.
- **`hello.txt` is not present in the workspace when the terminal `result` reports success**: the supervisor treats this as acceptance-criterion failure for the ticket. `agent_instances.status='failed'`, `exit_reason='acceptance_failed'`, no ticket transition. `total_cost_usd` still captured. Future milestones may extend expected-artifact verification; M2.1 checks exactly one artifact.
- **`hello.txt` contents do not match the ticket id (exact string match, trailing newline permitted)**: same outcome as the missing-file case — `exit_reason='acceptance_failed'`.
- **Two tickets for `engineering` are inserted in quick succession and the department cap is 1**: the second event defers via the M1 cap-check path (FR-003 in `specs/m1-event-bus/spec.md`); fallback poll picks it up once the first subprocess exits. No in-flight overlap.
- **Per-invocation MCP config file write fails** (disk full, permission denied on the supervisor state dir, read-only filesystem): spawn does not proceed. Handled per FR-103 — `status='failed'`, `exit_reason='spawn_failed'`, event marked processed, dispatcher continues.
- **Per-invocation MCP config file is deleted by an external process before subprocess exit**: Claude's MCP server may still be connected for the remainder of the run; the init event already locked in configuration. On subprocess exit, the supervisor's `defer os.Remove(...)` is a no-op for the already-missing file.
- **The `claude` binary is missing from `$PATH` at spawn time**: `exec.CommandContext` returns an error before subprocess start; `agent_instances.status='failed'`, `exit_reason='spawn_failed'`, `pid` NULL. No Claude process group exists to terminate.
- **An external actor SIGKILLs the Claude subprocess**: the supervisor observes the exit via `cmd.Wait()` returning an error; terminal row records `status='failed'`, `exit_reason='signaled_<signal>'`. Process-group cleanup is a no-op because the group is already gone. Inherits the M1 chaos-test behaviour.
- **Claude returns before the supervisor finishes parsing all buffered stdout**: the NDJSON parser drains remaining lines before the terminal transaction runs. The `result` event is the signal for successful completion, not subprocess exit.
- **Subprocess exits cleanly but no `result` event was parsed** (future Claude version emits a new terminal event shape; or the parser missed the `result` due to a routing bug): handled per FR-110a — `status='failed'`, `exit_reason='no_result'`, regardless of exit code. `total_cost_usd` stays NULL. No ticket transition. The operator is expected to inspect captured NDJSON and reconcile the routing table.
- **The engineering workspace directory does not exist at supervisor startup**: the supervisor creates it idempotently (`mkdir -p` on `workspace_path`). Per "Workspace (M2.1-minimal)" in `m2.1-context.md`.

## Requirements (mandatory)

### Functional requirements

M2.1 inherits every functional requirement from the M1 spec. The requirements below are additions and refinements specific to M2.1.

#### Spawn and process lifecycle

- **FR-101**: The supervisor MUST spawn each Claude Code invocation with the exact argv, env inheritance, `Dir`, `SysProcAttr.Setpgid`, stdin/stdout/stderr wiring, and per-invocation MCP config file handling specified in "Spawn contract" of `m2.1-context.md`. It MUST NOT deviate from that contract without updating the context file.
- **FR-102**: The supervisor MUST substitute the concrete per-invocation values — task description, agent system prompt (the engineer's `agent.md` content), model, budget cap, MCP config path, workspace path — at spawn time, and MUST NOT hard-code them into the binary.
- **FR-103**: The supervisor MUST write a per-invocation MCP config file keyed to the `agent_instances.id` before `cmd.Start()`, and MUST delete the file on subprocess exit via `defer os.Remove(...)`. The file MUST live under the supervisor-owned state directory (see NFR-105), not in the workspace, and MUST NOT be reused across invocations. If the write fails (disk full, permission denied, read-only filesystem, or any other I/O error), the supervisor MUST NOT call `cmd.Start()` for that invocation, MUST write a terminal `agent_instances` row with `status='failed'` and `exit_reason='spawn_failed'`, MUST set `event_outbox.processed_at` on the originating event to prevent retries, MUST log the underlying I/O error at error level with the attempted path and the `agent_instances.id`, and MUST keep the dispatcher serving other events. The supervisor MUST NOT retry the same failed spawn; operator intervention is expected.
- **FR-104**: The supervisor MUST populate `agent_instances.pid` immediately after `cmd.Start()` returns, resolving the M1 retro §4 dead-column finding.
- **FR-105**: Every Claude subprocess MUST be spawned with `SysProcAttr.Setpgid = true` (AGENTS.md concurrency rule 7). When terminating a subprocess — on MCP health failure, timeout, or supervisor shutdown — the supervisor MUST signal the process group (`syscall.Kill(-pgid, SIGTERM)` then SIGKILL after grace), NEVER the bare PID. Per spike §2.7.

#### Stream-json parsing and event routing

- **FR-106**: The supervisor MUST parse Claude's stdout as NDJSON, one JSON object per line. A line that fails JSON parsing MUST terminate the subprocess via process-group SIGTERM-then-SIGKILL and write a terminal row with `exit_reason='parse_error'`.
- **FR-107**: The supervisor MUST route each parsed event by its `type` (and `subtype` where applicable) exactly as defined in "Stream-json event routing" of `m2.1-context.md`. Unknown event `type` values MUST be logged at warn level and MUST NOT crash or bail the subprocess.
- **FR-108**: On receipt of the `system`/`init` event, the supervisor MUST iterate `mcp_servers[]` and verify every entry has `status == "connected"`. Any other value — including `failed`, `needs-auth`, or any unknown string (fail-closed) — MUST cause immediate process-group SIGTERM and a failed `agent_instances` row with `exit_reason = "mcp_<name>_<status>"`. See AGENTS.md "what agents should not do" on MCP exit codes.
- **FR-109**: The supervisor MUST capture `session_id`, `tools[]`, and `cwd` from the init event and attach them to the slog context for subsequent event-routing log lines, so that per-invocation traces can be reconstructed.
- **FR-110**: On the terminal `result` event, the supervisor MUST capture `duration_ms`, `total_cost_usd`, `terminal_reason`, `is_error`, and `permission_denials`, and MUST use these values to populate the `agent_instances` terminal row.
- **FR-110a**: If the Claude subprocess exits (`cmd.Wait()` returns) without the NDJSON parser having seen a terminal `result` event, the supervisor MUST fail closed regardless of the subprocess exit code: write an `agent_instances` terminal row with `status='failed'`, `exit_reason='no_result'`, `finished_at` set, and `total_cost_usd` NULL; set `event_outbox.processed_at`; and MUST NOT write a `ticket_transitions` row. This prevents a future Claude Code terminal-event shape change from silently producing corrupt success data (cf. spike §2.5 on `--strict-mcp-config` returning exit 0 despite MCP failure). A `no_result` outcome is a signal for the operator to inspect the captured NDJSON and update the routing table.

#### Outcome adjudication and state writes

- **FR-111**: The supervisor, not the agent, is the sole writer of `tickets`, `ticket_transitions`, and `agent_instances`. The agent's MCP access to Postgres is read-only (see FR-115 and NFR-104). An agent signals success by exiting with a successful `result` event and leaving `hello.txt` in the workspace; the supervisor adjudicates that signal.
- **FR-112**: The supervisor MUST treat the invocation as successful for the purposes of writing a `todo → done` transition only if all of the following hold: the terminal `result` event has `is_error == false` and `terminal_reason == "completed"`; the MCP health check passed; `hello.txt` exists in the engineering workspace; `hello.txt` content matches the ticket id exactly (trailing newline permitted). If any condition fails, the supervisor MUST NOT write a `ticket_transitions` row and MUST record the `agent_instances` row with the corresponding failure `exit_reason` from `m2.1-context.md`'s "Agent-instance state machine", extended by this spec to include `acceptance_failed`, `spawn_failed`, and `no_result` (`claude_error`, `acceptance_failed`, `mcp_<server>_<status>`, `parse_error`, `timeout`, `supervisor_shutdown`, `spawn_failed`, or `no_result`).
- **FR-113**: On a successful invocation, the supervisor MUST write in a single terminal transaction (reusing the M1 pattern): the `agent_instances` update to `status='succeeded'`, `exit_reason='completed'`, `finished_at`, `total_cost_usd`; the `ticket_transitions` insert (`from_column='todo'`, `to_column='done'`, `triggered_by_agent_instance_id`, `hygiene_status=NULL`); and the `event_outbox.processed_at` set for the originating event. `hygiene_status` is NULL in M2.1 because M2.1 has no expected-writes contract yet (MemPalace arrives in M2.2).
- **FR-114**: On MCP health failure, parse error, timeout, supervisor shutdown, `claude_error`, or `acceptance_failed`, the supervisor MUST still write an `agent_instances` terminal row and MUST still set `event_outbox.processed_at` so the dispatcher does not retry. The supervisor MUST NOT write a `ticket_transitions` row for these cases.

#### Postgres MCP

- **FR-115**: The supervisor MUST configure every Claude invocation with a Postgres MCP server that exposes read-only query access to the Garrison database. The server MUST reject any SQL statement that is not `SELECT` or `EXPLAIN` at the MCP protocol layer, and MUST additionally connect to Postgres as a role whose database privileges permit only `SELECT` on the required tables (defense in depth; see NFR-104).
- **FR-116**: The supervisor MUST ship a minimal in-tree Go Postgres MCP server (stdio transport, JSON-RPC 2.0, exposing at minimum a `query` tool accepting a SQL string and returning result rows). The MCP server MUST build from the supervisor's Go module, use the already-locked `jackc/pgx/v5` driver, and MUST NOT introduce a Node or Python runtime dependency. Rationale for this choice is in "Binding questions for /speckit.specify" of `m2.1-context.md` and in the Assumptions section below.
- **FR-117**: The per-invocation MCP config file MUST reference the supervisor-shipped Postgres MCP entrypoint (either the supervisor binary with an MCP-server subcommand/flag, or a companion binary built from the same Go module) and MUST provide a read-only Postgres connection string. The connection string MUST NOT appear in Claude Code's argv (only in the MCP config file, which is `--mcp-config`'d and ephemeral).

#### Agent definition and engineer role

- **FR-118**: The engineer agent's `agent.md` MUST be delivered to spawns by reading the `agents.agent_md` column from Postgres at spawn time. The repository MUST also include an `examples/agents/engineer.md` file (or equivalent path) for operator bootstrap and open-source replication, but the supervisor's runtime source is the database, not the filesystem.
- **FR-119**: The seeded engineer `agent.md` MUST instruct the agent to: (a) read its ticket via the Postgres MCP `query` tool using the ticket id injected into its task prompt; (b) write `hello.txt` to its current working directory containing exactly the ticket id as its content; (c) exit. It MUST NOT instruct the agent to perform any other action. The M2.1 scope is the literal hello-world acceptance; broadening the engineer's task is explicitly out of scope for M2.1.

#### Data model additions

- **FR-120**: A single forward migration MUST add `total_cost_usd NUMERIC(10,6)` (nullable) to `agent_instances`. The migration MUST NOT rewrite any existing column.
- **FR-121**: The migration MUST seed one `departments` row for `engineering` with `slug='engineering'`, a `workspace_path` (operator-configurable via env var or migration parameter), `concurrency_cap=1`, and a `workflow` JSONB declaring only `todo` and `done` columns with `todo → done` as the permitted transition. See "Data model for M2.1" in `m2.1-context.md`.
- **FR-122**: The migration MUST seed one `agents` row for the engineer role, with `role_slug='engineer'`, `agent_md` populated from the repository's `examples/agents/engineer.md` content at bootstrap time, `model` set to the operator-configurable default (see NFR-102), `listens_for=["work.ticket.created.engineering.todo"]`, `palace_wing=NULL` (MemPalace arrives in M2.2), and `skills=[]` (skills.sh arrives in M7).
- **FR-123**: The engineering `workspace_path` MUST be created idempotently on supervisor startup (`mkdir -p`). The supervisor MUST fail startup loudly if the path is configured but cannot be created or is not writable.

#### Event channel and dispatch

- **FR-124**: The supervisor MUST LISTEN on `work.ticket.created.engineering.todo` specifically (the M1 spec listened on the generic `work.ticket.created`; M2.1 narrows dispatch to the department-column-qualified channel to match the seeded agent's `listens_for`). The M1 dispatcher's channel-based handler registration accommodates this as a static registration change, not a redesign.
- **FR-125**: The M1 dedupe mechanisms (event-outbox `processed_at` plus the in-memory dedupe `sync.Map` added per M1 retro §1) MUST remain in force. M2.1 does not relax idempotency.

### Non-functional requirements

- **NFR-101** (subprocess timeout default): 60 seconds per Claude Code invocation, measured from `cmd.Start()`. The single timeout window covers MCP boot, the `system`/`init` handshake, and all subsequent LLM work. Configurable via environment variable. NFR-106's 2-second MCP-bail budget runs inside this window (it is a constraint on supervisor responsiveness after the init event is received, not a separate pre-init timeout).
- **NFR-102** (model default): `claude-haiku-4-5-20251001` is the operator-configurable default for M2.1, chosen for cost during validation. Stored in the seeded `agents.model` column; editable in the database.
- **NFR-103** (per-invocation budget cap): `--max-budget-usd` defaults to `0.05` (five cents) per invocation. Configurable per invocation via the agent row's model-related config in future milestones; for M2.1 the cap is a single supervisor-wide default.
- **NFR-104** (Postgres read-only enforcement): primary guarantee is a Postgres role (e.g. `garrison_agent_ro`) with `GRANT SELECT` on the relevant `work` and `org` tables and no other grants. The in-tree MCP server additionally rejects non-`SELECT`/`EXPLAIN` statements at the protocol layer as defense in depth. If the two disagree, the DB role wins — a successful DML over the MCP server would still be refused by Postgres.
- **NFR-105** (per-invocation MCP config file location): supervisor-owned state directory, default `/var/lib/garrison/mcp/` (operator-configurable via environment variable, e.g. `GARRISON_MCP_CONFIG_DIR`). The directory MUST be writable by the supervisor's user. Files are named `mcp-config-<agent_instance_id>.json` and are removed on subprocess exit via `defer os.Remove(...)`.
- **NFR-106** (MCP health check bail latency): the supervisor MUST send SIGTERM to the Claude process group within 2 seconds of receiving the `system`/`init` event when that event reports a non-`connected` MCP server. This includes init-event parsing, `mcp_servers[]` iteration, and signalling — not the subprocess's subsequent exit, which gets a further 2-second grace before SIGKILL per "Termination contract".
- **NFR-107** (M2.1 concurrency cap for engineering): 1. Deterministic test timing; bumped to the production target in M3 or M4. A cap of 1 does not change the cap-check code path — it is a data value on the seeded department row.
- **NFR-108** (cost data fidelity): `total_cost_usd` captured in `agent_instances` MUST be the exact value Claude reports in the terminal `result` event, as a `NUMERIC(10,6)`. The supervisor MUST NOT round, accumulate, or synthesize cost. M6 adds aggregation across invocations; M2.1 is per-invocation authoritative.
- **NFR-109** (log stream labelling): Claude subprocess `stderr` MUST be piped to slog with the structured field `stream="claude_stderr"`; each parsed NDJSON event MUST log with structured fields including `event_id`, `ticket_id`, `agent_instance_id`, `session_id` (from the init event), and the event's `type`/`subtype`.
- **NFR-110** (no session persistence side effect): the supervisor MUST verify that `--no-session-persistence` is always passed (per spike §2.8). M2.1 does not tolerate accidental `~/.claude/projects/…/sessions/` artefacts.
- **NFR-111** (secret handling scope): M2.1 uses Claude Code's default OAuth-via-keychain auth path (`~/.claude/`). `--bare` and `ANTHROPIC_API_KEY` handling are deferred to M6. No API key secrets appear in Claude Code's argv or in the per-invocation MCP config file.
- **NFR-112** (runtime base image review trigger): per M1 retro §5, introducing the real `claude` binary is the documented trigger for reviewing the runtime container base image. The spec does not decide the final base image; the plan phase and the retro both revisit it.

### Key entities

Deltas from M1. Full schema lives in "Data model for M2.1" in `m2.1-context.md`; do not duplicate it here.

- **Department** (existing from M1; seeded in M2.1): the engineering department row — workspace path, concurrency cap 1, a workflow JSONB with exactly `todo` and `done`.
- **Agent** (existing from M1's schema sketch in ARCHITECTURE.md; first row seeded in M2.1): the engineer role — `agent_md` body, `model`, `listens_for`, empty skills, null palace wing.
- **Agent instance** (existing; adds `total_cost_usd NUMERIC(10,6)`, backfills `pid`, widens `exit_reason` vocabulary): one row per Claude invocation, with a terminal status and — on success — captured cost.
- **Ticket transition** (existing): one row per `todo → done` successful handoff. `hygiene_status` is always NULL in M2.1 because there is no expected-writes contract yet.
- **Per-invocation MCP config file** (new, filesystem-only, not a database entity): a short-lived JSON file describing the Postgres MCP server entrypoint and connection string. Created at spawn, deleted on subprocess exit.
- **Engineer `agent.md`**: the system prompt delivered to every engineer spawn. Authoritative source is `agents.agent_md` in Postgres; the repository ships `examples/agents/engineer.md` as a bootstrap/example.

## Success criteria (mandatory)

### Measurable outcomes

- **SC-101**: All eleven acceptance criteria in "Acceptance criteria for M2.1" of `m2.1-context.md` pass, in order and unmodified, against a freshly-migrated Postgres and the release Docker image.
- **SC-102**: A ticket inserted for the engineering department at column `todo` produces, within the subprocess timeout, exactly one `hello.txt` file in the engineering workspace containing the ticket id (exact string match; trailing newline permitted), exactly one `agent_instances` row with `status='succeeded'`, exactly one `ticket_transitions` row for `todo → done`, and a non-null `total_cost_usd` that matches the `result` event's value.
- **SC-103**: A ticket spawned with a deliberately broken per-invocation MCP config causes the supervisor to signal the Claude process group within 2 seconds of init-event receipt, producing exactly one `agent_instances` row with `status='failed'` and `exit_reason` of the form `mcp_<server>_<status>`, zero `hello.txt` writes, and zero `ticket_transitions` rows.
- **SC-104**: A ticket whose Claude subprocess exceeds the configured timeout produces exactly one `agent_instances` row with `status='timeout'` (via the `exit_reason` vocabulary), the entire Claude process group terminated (no surviving child processes), zero `ticket_transitions` rows, and `event_outbox.processed_at` set.
- **SC-105**: SIGTERM to the supervisor with at least one Claude subprocess in-flight results in: the subprocess's process group receiving SIGTERM (not PID-level), terminal writes completing via `context.WithoutCancel(...)`, zero `agent_instances` rows left in `status='running'` after the supervisor exits, and zero orphan child processes of the former Claude subprocess.
- **SC-106**: Over a run of at least ten sequential tickets against a cap-1 engineering department (each ticket inserted after the previous one completes), each ticket produces exactly one `hello.txt`, one `agent_instances` row, and one `ticket_transitions` row. No in-flight overlap occurs; the second-inserted-while-first-running ticket is deferred and picked up by the fallback poll as in M1.
- **SC-107**: The aggregate spend across ten sequential hello-world tickets, as reported by `SUM(total_cost_usd)` in `agent_instances`, is less than $0.50 with the default model (`claude-haiku-4-5-20251001`) under normal cache-hit conditions. This is a cost-sanity gate, not a hard contract; if real cache behaviour pushes this above $0.50 the retro investigates.
- **SC-108**: All M1 chaos tests still pass unchanged (LISTEN reconnect recovers missed events; external SIGKILL on the subprocess marks `status='failed'`; graceful shutdown with in-flight subprocess leaves no stale rows). One new chaos test — the broken-MCP-config test — passes as described in SC-103.
- **SC-109**: A `rate_limit_event` received mid-run is logged at warn level with all of its fields (`utilization`, `resetsAt`, `rateLimitType`, `surpassedThreshold`, `status`, `isUsingOverage`), and does not change dispatch behaviour (the invocation runs to completion or terminal failure via the same paths as without the event). M2.1 does not implement rate-limit backoff.
- **SC-110**: No Claude Code invocation in M2.1 leaves filesystem artefacts in `~/.claude/projects/…/sessions/` (per spike §2.8 and NFR-110). Supervisor runs are observably side-effect-free on Claude's own session-log path.

## Assumptions

- The operator installs the `claude` binary on the supervisor host such that it is discoverable on `$PATH` by the supervisor process. Installation mechanics (package manager, symlink, container mount) are operator-owned and out of scope.
- Claude Code authentication uses the default OAuth-via-keychain path at `~/.claude/` on the supervisor host. `--bare` mode and `ANTHROPIC_API_KEY` injection are deferred to M6 (NFR-111). The supervisor's user has read access to the Claude keychain materials.
- The `claude` CLI's non-interactive contract — argv flags, stream-json event shapes, exit behaviours, process-group signalling — is as characterized in `docs/research/m2-spike.md` (Part 1). If real M2.1 behaviour deviates, the supervisor fails loudly (FR-106, FR-107) and the retro documents the deviation.
- The MemPalace MCP is explicitly absent from M2.1 (it arrives in M2.2). Agents do not read from or write to MemPalace. No `mempalace_*` tools appear in any M2.1 MCP config. Hygiene checks remain NULL in `ticket_transitions.hygiene_status`.
- The supervisor is the sole writer to `tickets`, `ticket_transitions`, and `agent_instances`. Agents read via Postgres MCP (read-only) and write artefacts to the workspace filesystem. Agent-initiated Postgres state changes are architecturally prohibited (RATIONALE §1, §4; constitution principle I; and the "What is explicitly out of scope" of `m2.1-context.md`).
- The in-tree Go Postgres MCP server is the commit point for the Postgres-MCP-implementation question (FR-116). Rationale: it preserves the single-static-binary deployment model, introduces no Node or Python runtime in the supervisor's deploy image (AGENTS.md stack rules), reuses the already-locked `jackc/pgx/v5` dependency, and keeps the read-only surface small (one or two tools). If the plan phase discovers that an in-tree server is substantially harder than the spike's "~150 lines" estimate, the plan can surface that as a replan signal — but the default committed here is in-tree Go.
- Postgres role-based read-only enforcement (a dedicated role with `GRANT SELECT` only, used by the MCP server) is the primary guarantee against agent writes, with protocol-layer statement filtering as defense in depth (NFR-104). Rationale: role-based auth is independent of MCP-server behaviour and is the kind of guarantee the operator can audit by inspecting `pg_roles` rather than by reading Go code.
- Subprocess timeout defaults to 60 seconds (NFR-101) and per-invocation budget defaults to $0.05 (NFR-103). Both are single supervisor-wide defaults for M2.1; per-agent overrides are explicitly deferred (`m2.1-context.md` "Binding questions" 3 and 4).
- The operator runs a single supervisor instance per database (inherited from M1 FR-018 advisory lock; unchanged by M2.1). Blue/green and parallel supervisor processing remain out of scope.
- Concurrency cap of 1 for engineering in M2.1 is a seed value on the `departments` row (NFR-107). Increasing the cap in later milestones is a data change, not a code change.
- The engineer's `agent.md`, delivered inline via `agents.agent_md`, describes a task no broader than "read the ticket, write `hello.txt` with the ticket id, exit" (FR-119).
- The runtime container base image for M2.1 may need to be revisited versus M1's `alpine:3.20` to accommodate the `claude` binary and its dependencies. The plan phase decides; the spec does not.

## Open questions for `/speckit.clarify`

None outstanding. The three ambiguities flagged when this spec was first drafted — subprocess timeout start-of-clock, per-invocation MCP config write failure, and subprocess exit without a parsed `result` — were resolved in the clarify session above. The seven binding questions from `m2.1-context.md` were committed in the initial draft (in-tree Go MCP server; role-based read-only; 60s timeout default; $0.05 budget cap default; supervisor-owned state dir for MCP configs; inline DB delivery of `agent.md`; engineering cap of 1).

---

This spec is intentionally narrow. All of the following are deferred to later milestones and MUST NOT be scoped into M2.1's plan or implementation: MemPalace integration (M2.2); agents writing to Postgres state; multiple departments or roles; hygiene checks; agent-spawned tickets; the dashboard (M3); CEO chat (M5); skills.sh integration (M7); rate-limit backoff logic (M6); cost-based throttling (M6); multi-model support (M4+); secret management including `--bare` and `ANTHROPIC_API_KEY` (M6); tool permission confirmation policies (M4+). If a subsequent artifact — plan, tasks, or implementation — pulls any of these in, the artifact is wrong. See "What is explicitly out of scope" in `m2.1-context.md` for the binding list.
