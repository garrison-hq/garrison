# Implementation plan: M2.2.1 — Structured completion via `finalize_ticket` tool

**Branch**: `005-m2-2-1-finalize-ticket` | **Date**: 2026-04-23 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/005-m2-2-1-finalize-ticket/spec.md`
**Binding context**: [`specs/_context/m2-2-1-context.md`](../_context/m2-2-1-context.md), [`AGENTS.md`](../../AGENTS.md) §§"Activate before writing code" + "Concurrency discipline" + "Stack and dependency rules", [`RATIONALE.md`](../../RATIONALE.md) §§3 (memory thesis — this milestone revises it) + §5 (soft gates), [`ARCHITECTURE.md`](../../ARCHITECTURE.md) "MemPalace write contract" (modified by this milestone), [`docs/retros/m2-2.md`](../../docs/retros/m2-2.md) live-run append (empirical justification), [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md). The shipped M1 + M2.1 + M2.2 supervisor (`supervisor/`) is the foundation this plan extends; its code and commit log are prerequisite reading.

## Summary

M2.2.1 is a surgical patch on top of shipped M2.2. It introduces one new in-tree MCP server (`internal/finalize`, subcommand `supervisor mcp finalize`) exposing a single `finalize_ticket` tool with a strict input schema (`garrison.finalize_ticket.v1`). The agent's successful `finalize_ticket` call triggers the supervisor's stream-json parser to execute an atomic Postgres transaction that writes the diary + KG triples to MemPalace (through the existing M2.2 sidecar) and commits the ticket transition + terminal `agent_instances` row — all under a 30-second wall-clock ceiling. Retry semantics cap at 3 attempts via a supervisor-side counter in `internal/spawn/pipeline.go`. The `hygiene_status` vocabulary is redefined to a supervisor-observable enum (`clean`, `finalize_failed`, `finalize_partial`, `stuck`, `pending`); legacy M2.2 values are preserved on historical rows. Two seed agent.md files (`engineer.md`, `qa-engineer.md`) are rewritten shorter (3000–4000 chars) with a single-paragraph completion protocol pointing at `finalize_ticket`, mid-turn `mempalace_*` usage preserved for `hall_discoveries`. No MemPalace container, version, API, or MCP-tool-wiring changes. No new dependencies. One migration updates the two seed rows and adds no schema columns.

The compliance justification is empirical: M2.2's live-run append recorded real Claude haiku-4-5-20251001 executing the ticket work but skipping the MANDATORY palace writes. This plan implements the RATIONALE §3 revision: agents produce structured reflection via `finalize_ticket`; the supervisor commits it atomically; mid-turn flexibility via the existing `mempalace` MCP server is preserved.

## Technical context

**Language/Version**: Go 1.25 (inherited from M1/M2.1/M2.2).
**Primary dependencies**: inherited — `github.com/jackc/pgx/v5`, `github.com/jackc/pgx/v5/pgxpool`, `golang.org/x/sync/errgroup`, `log/slog`, `github.com/pressly/goose/v3`, `github.com/stretchr/testify`, `github.com/testcontainers/testcontainers-go`. **No new Go dependencies.** The finalize MCP server speaks JSON-RPC 2.0 over stdio via `encoding/json` following the `internal/pgmcp` pattern. Schema validation is handled by custom Go code (strict type-switch over the decoded payload); no JSON-Schema library is introduced.
**Storage**: PostgreSQL 17+ (unchanged); MemPalace SQLite + ChromaDB (unchanged; M2.2 sidecar reused).
**Testing**: stdlib `testing` + `testify` + `testcontainers-go`; build tags `integration`, `chaos`, `live_acceptance` reused from M2.2; mockclaude fixtures extended with six new NDJSON files under `internal/spawn/mockclaude/fixtures/m2_2_1/`.
**Target platform**: Linux server (Hetzner + Coolify); single static Go binary for the supervisor; the `finalize` subcommand is always built into the main binary.
**External binaries**: `claude` (2.1.117, unchanged from M2.2) in the supervisor image; MemPalace sidecar, docker-proxy sidecar, docker CLI — all unchanged from M2.2.
**Project type**: CLI/daemon. The supervisor binary gains one new subcommand (`supervisor mcp finalize`); no new processes; no new containers.
**Performance goals**: inherited M1/M2.1/M2.2 NFRs unchanged. New constraint from clarify Q5: atomic-write wall-clock ≤ 30 s (enforced via context timeout). Per-invocation budget cap remains $0.10.
**Constraints**: locked dependency list preserved; every goroutine threads `ctx`; atomic write runs under `context.WithoutCancel` so supervisor SIGTERM doesn't abort an in-flight commit; process-group termination on cap-exhaustion path (AGENTS.md rule 7); pipeline-drain-before-Wait (AGENTS.md rule 8); hygiene checker simplified to pure Go over Postgres (no MemPalace queries on M2.2.1 rows).
**Scale/scope**: single operator; one department (`engineering`) with cap=1; two roles (engineer + qa-engineer) that share the finalize tool; sequential two-agent ticket flow.

## Constitution check

*Gate: must pass before tasks. Re-checked before `/garrison-implement`.*

| Principle | Compliance |
|-----------|------------|
| I. Postgres is sole source of truth; pg_notify is the bus | Pass — the atomic write uses the same Postgres tx + event_outbox pattern M1/M2.1/M2.2 established. No new event bus. |
| II. MemPalace is sole memory store | Pass — reinforced, not altered. Agents still read/write mid-turn via `mempalace_*`; completion writes move from agent-driven to supervisor-driven against the same MemPalace sidecar. |
| III. Agents are ephemeral | Pass — every Claude invocation is fresh; the finalize MCP server is spawned per-agent-turn and exits on EOF. |
| IV. Soft gates on memory hygiene | **Revised by this milestone, consistent with the principle.** The `hygiene_status` column remains observational; the completion MECHANISM (finalize_ticket) is now hard-enforced because the agent cannot exit with a valid transition any other way. This is stricter than M2.2's "MANDATORY in prompt" but still soft at the ticket-workflow level (a ticket without a transition simply stays at its entry column; no human escalation; hygiene dashboard surfaces it). |
| V. Skills from skills.sh | N/A — M7. Both seed rows keep `skills=[]`. |
| VI. Hiring is UI-driven | N/A — M7. Both roles seeded via migration (unchanged from M2.2). |
| VII. Go supervisor with locked deps | Pass — no new Go dependencies. |
| VIII. Every goroutine accepts context | Pass — atomic write runs under context.WithTimeout(context.WithoutCancel(ctx), 30s); finalize server's stdio loop threads ctx. |
| IX. Narrow specs per milestone | Pass — scope limited to the compliance-architecture pivot. No dashboard, no vault, no CEO, no hiring. |
| X. Per-department concurrency caps | Pass — engineering cap remains 1. Sequential two-agent flow. |
| XI. Self-hosted on Hetzner | Pass — no new services. |

No violations → Complexity tracking intentionally empty.

## Project structure

### Documentation (this feature)

```text
specs/005-m2-2-1-finalize-ticket/
├── spec.md        # locked after clarify
├── plan.md        # this file
└── tasks.md       # produced by /garrison-tasks
```

No separate research.md — M2.2.1 inherits M2.2's spike findings unchanged. No contracts/ or data-model.md — the JSON schema lives in `internal/finalize/tool.go` and is citable by code path.

### Source code (repository root) — M2.2.1 delta

```text
garrison/
├── supervisor/
│   ├── cmd/supervisor/
│   │   ├── main.go                        # +1 subcommand registration: `supervisor mcp finalize`
│   │   └── mcp_finalize.go                # NEW — subcommand entrypoint (~30 LOC)
│   ├── internal/
│   │   ├── finalize/                      # NEW package
│   │   │   ├── server.go                  # MCP protocol loop (init, tools/list, tools/call) — ~150 LOC
│   │   │   ├── tool.go                    # schema definition + validator + error shapes — ~150 LOC
│   │   │   ├── handler.go                 # receiver: validates payload, reads agent_instance state — ~100 LOC
│   │   │   ├── server_test.go             # MCP protocol tests
│   │   │   ├── tool_test.go               # happy-path validation
│   │   │   └── schema_test.go             # field-by-field negative tests
│   │   ├── spawn/
│   │   │   ├── finalize.go                # NEW — supervisor-side atomic writer (WriteFinalize helper)
│   │   │   ├── finalize_test.go           # NEW — unit tests for writer + adjudicate integration
│   │   │   ├── pipeline.go                # extended — retry counter map, finalize tool_use observer
│   │   │   ├── pipeline_test.go           # extended — retry counter, cap enforcement tests
│   │   │   ├── spawn.go                   # extended — Adjudicate precedence rows + acceptance gate override
│   │   │   ├── spawn_test.go              # extended — precedence tests
│   │   │   ├── exitreason.go              # +6 constants
│   │   │   └── mockclaude/
│   │   │       └── fixtures/m2_2_1/       # NEW — 6 NDJSON fixtures
│   │   │           ├── finalize_happy_path.ndjson
│   │   │           ├── finalize_retry_then_success.ndjson
│   │   │           ├── finalize_retry_exhausted.ndjson
│   │   │           ├── finalize_never_called.ndjson
│   │   │           ├── finalize_atomic_chaos.ndjson
│   │   │           └── finalize_midturn_then_finalize.ndjson
│   │   ├── mempalace/
│   │   │   ├── writes.go                  # NEW — AddDrawer + AddTriples methods on Client
│   │   │   └── writes_test.go             # NEW — dockerexec-seam unit tests
│   │   ├── mcpconfig/
│   │   │   └── mcpconfig.go               # extended — FinalizeParams struct + third mcpServers entry
│   │   ├── config/
│   │   │   └── config.go                  # +1 env: GARRISON_FINALIZE_WRITE_TIMEOUT (default 30s)
│   │   ├── hygiene/
│   │   │   ├── evaluator.go               # extended — EvaluateFinalizeOutcome function
│   │   │   └── evaluator_test.go          # extended — new vocabulary assertions
│   │   └── store/
│   │       └── *.sql.go                   # regenerated after sqlc run (2 new queries)
│   ├── migrations/
│   │   ├── 20260424000005_m2_2_1_finalize_ticket.sql   # NEW — UPDATE agents seed content
│   │   ├── queries/
│   │   │   ├── tickets.sql                # +1 query: SelectTicketObjective
│   │   │   └── agent_instances.sql        # +1 query: SelectAgentInstanceFinalizedState
│   │   └── seed/
│   │       ├── engineer.md                # REWRITTEN — shorter, finalize-focused
│   │       └── qa-engineer.md             # REWRITTEN — shorter, finalize-focused
│   ├── integration_m2_2_1_happy_path_test.go      # NEW — US1 + US2 end-to-end
│   ├── integration_m2_2_1_retry_test.go           # NEW — US3 both paths
│   ├── integration_m2_2_1_stuck_test.go           # NEW — US5
│   ├── integration_m2_2_1_midturn_test.go         # NEW — SC-256
│   ├── integration_m2_2_1_compliance_test.go      # NEW — SC-261 (live; live_acceptance tag)
│   ├── chaos_m2_2_1_test.go                       # NEW — US4 atomic-write kill mid-tx
│   └── integration_m2_2_1_shared_test.go          # NEW — shared helpers for M2.2.1 tests
└── docs/retros/
    └── m2-2-1.md                          # written by the retro task
```

**Structure decision**: M2.2.1 lands one new in-tree package (`internal/finalize`), one new file inside `internal/spawn` (`finalize.go`), extensions to three existing packages (`mempalace`, `mcpconfig`, `hygiene`, `config`), two new sqlc queries, one migration, two rewritten seed files, and six new integration/chaos test files. No new packages elsewhere; no new binaries; no new containers.

## Decisions baked into this plan

From `specs/_context/m2-2-1-context.md` (architectural decisions 1–5), `spec.md` (§Clarifications Session 2026-04-23 items 1–6), and the operator-approved structural decision slate preceding this plan:

1. **Package**: `internal/finalize` with `server.go`, `tool.go`, `handler.go`. Stateless server; state in Postgres + supervisor-side pipeline counter.
2. **Subcommand**: `supervisor mcp finalize`. Single-tool MCP server. Exits on stdin EOF.
3. **MCP config order**: `postgres`, `mempalace`, `finalize` — third entry. `cmd=<supervisor_bin>`, `args=["mcp","finalize"]`, `env={GARRISON_AGENT_INSTANCE_ID=<uuid>, GARRISON_DATABASE_URL=<dsn>}`.
4. **Schema version**: `garrison.finalize_ticket.v1` in the tool's `description`. Server refuses non-v1 (no version field in M2.2.1 payloads — all payloads are v1 implicitly; future versions will carry an explicit `version` field).
5. **Schema shape**: `{ticket_id, outcome (10-500), diary_entry{rationale (50-4000), artifacts[], blockers[], discoveries[]}, kg_triples[{subject, predicate, object, valid_from}]}` with array caps 50/50/50/100 and per-element-string caps 500.
6. **Error vocabulary (tool response)**: `{ok: false, error_type, field, message, attempt}` where `error_type ∈ {"schema", "palace_write", "transition_write", "budget_exhausted"}`. `attempt` is informational (1-based); supervisor is authority for cap enforcement.
7. **Retry cap**: 3 attempts. Supervisor-side counter in `pipeline.go`. On 3rd failed tool_result, supervisor SIGTERMs the process group with `exit_reason='finalize_invalid'`. Counter does NOT increment on post-commit rejected calls.
8. **Atomic write boundary**: one Postgres tx containing MemPalace `AddDrawer` + N× `AddTriples` + INSERT ticket_transitions + UPDATE tickets + UPDATE agent_instances + UPDATE event_outbox. Wrapped in `context.WithTimeout(context.WithoutCancel(parent), 30s)`.
9. **Diary serialization**: body = `<ticket_objective>\n\n---\n<yaml_frontmatter>\n---\n\n<rationale>`. Objective prose prepended so `mempalace_search(query=ticket.objective)` returns the drawer (semantic-similarity works on prose; fails on UUID).
10. **Finalize trigger**: supervisor acts on first successful `finalize_ticket` tool_use immediately, before subprocess exit. No waiting for `result` event.
11. **Post-commit calls**: rejected by the finalize server with `{error_type: "schema", field: null, message: "finalize_ticket already succeeded for this agent_instance"}`. Server detects by querying `agent_instances.status = 'succeeded'` AND existence of `ticket_transitions` row.
12. **Hygiene status vocabulary**: new rows use `clean | finalize_failed | finalize_partial | stuck | pending`. Legacy values (`missing_diary`, `missing_kg`, `thin`) remain valid on M2.2-era rows. No retroactive UPDATE. No CHECK constraint added (legacy coexistence forbids it).
13. **Exit reason additions**: `finalize_invalid`, `finalize_palace_write_failed`, `finalize_commit_failed`, `finalize_write_timeout`, `finalize_never_called`, `finalize_transition_conflict`.
14. **Subprocess lifecycle post-finalize**: natural exit. Supervisor observes but takes no DB action. M2.1's $0.10 budget cap handles runaway post-commit generation.
15. **MemPalace client extensions**: the M2.2 `Client` type (currently in `internal/hygiene/palace.go` because M2.2's only palace caller was the hygiene checker) is relocated to `internal/mempalace/client.go` as part of T003; `internal/hygiene/palace.go` imports the type thereafter. Two new methods on `*Client`: `AddDrawer(ctx, wing, room, content) error` and `AddTriples(ctx, triples []Triple) error`. Uses existing docker-exec transport via `DockerExec` seam.

## Changes to existing M2.2 packages

### `internal/config`

Adds one env var:

```go
// config.go additions
FinalizeWriteTimeout time.Duration // default 30s; parsed via time.ParseDuration
```

Env: `GARRISON_FINALIZE_WRITE_TIMEOUT`. Default: `30s`. Parsed in `Load()` alongside the other timeout fields. Exposed via `cfg.FinalizeWriteTimeout`. Validation: must be positive; values ≤ 0 return a config error at startup.

No other supervisor-level config changes. The MCP-config-writer's env injection for `GARRISON_AGENT_INSTANCE_ID` is not a supervisor env var — it's set per-spawn by `internal/mcpconfig`.

### `internal/mcpconfig`

New struct + builder extension:

```go
// mcpconfig.go additions
type FinalizeParams struct {
    SupervisorBin    string
    AgentInstanceID  string // uuid as string
    DatabaseURL      string // garrison_agent_ro DSN
}

// Existing BuildConfig signature grows an optional finalize param
// (or new BuildConfigWithFinalize — decided in T003)
```

The builder's output's `mcpServers` map gains a third key `"finalize"` with:
- `command`: `<SupervisorBin>`
- `args`: `["mcp", "finalize"]`
- `env`: `{"GARRISON_AGENT_INSTANCE_ID": <id>, "GARRISON_DATABASE_URL": <DSN>}`

Order is not guaranteed by Go maps but the MCP JSON spec doesn't require ordered `mcpServers`. M2.2's tests assert keys, not order; M2.2.1 test assertions follow the same pattern.

### `internal/spawn/pipeline.go`

Adds a per-agent-instance retry counter:

```go
// pipeline.go additions
type finalizeState struct {
    attempts        int    // 0-3
    committed       bool   // true after atomic write commits
}

// State is attached to the existing per-spawn pipeline struct that
// already holds the stream-json parsing state. New field:
// `finalizeState finalizeState`
```

Stream-json event hook: on observing `tool_use.name == "finalize_ticket"`:
1. If `finalizeState.committed`: ignore (supervisor has already committed; server will return error to agent).
2. Increment `finalizeState.attempts`.
3. Pair with subsequent `tool_result`:
   - On `tool_result.ok == true`: call `spawn.WriteFinalize(...)` (new helper in `spawn/finalize.go`). On success, set `finalizeState.committed = true` and terminate the event loop for this agent (the atomic write has written the terminal row; further events are logged only).
   - On `tool_result.ok == false`:
     - If `attempts == 3`: signal the process group per M2.1's SIGTERM discipline, with `finalizeState.exitReason = "finalize_invalid"`. Supervisor's subsequent terminal-write path uses this value.
     - Otherwise: observe, log, continue reading stream.

### `internal/spawn/spawn.go`

Two changes:

1. **Adjudicate precedence additions**: extend the precedence table with:
   - `budget_exceeded` wins over `finalize_invalid`
   - `timeout` wins over `finalize_never_called`
   - New `finalize_*` exit reasons at priority lower than `budget_exceeded` and `timeout` but higher than `completed`
2. **Acceptance gate override**: when `finalizeState.committed == true`, skip M2.1's `checkHelloTxt` path entirely (the atomic write has already written `status='succeeded'`; no acceptance-gate re-evaluation needed). The existing `acceptanceGateSatisfied(roleSlug, fromColumn)` helper (M2.2.1-era) stays; `WriteFinalize` is on a separate terminal path than `writeTerminalCostAndWakeup`.

### `internal/spawn/exitreason.go`

Six new constants, all lower-snake-case:

```go
ExitFinalizeInvalid            = "finalize_invalid"
ExitFinalizePalaceWriteFailed  = "finalize_palace_write_failed"
ExitFinalizeCommitFailed       = "finalize_commit_failed"
ExitFinalizeWriteTimeout       = "finalize_write_timeout"
ExitFinalizeNeverCalled        = "finalize_never_called"
ExitFinalizeTransitionConflict = "finalize_transition_conflict"
```

### `internal/mempalace`

First: **relocate** the M2.2 `Client` type from `internal/hygiene/palace.go` to a new file `internal/mempalace/client.go`. The M2.2 `Client` (fields: `DockerBin`, `MempalaceContainer`, `PalacePath`, `DockerHost`, `Timeout`, `Exec`) and its `Query` method move verbatim; hygiene's `palace.go` retains the `TimeWindow`, `PalaceDrawer`, `PalaceTriple`, `ErrPalaceQueryFailed`, the JSON-RPC helpers (`buildPalaceRequests`, `parsePalaceResponses`) that are hygiene-specific, but imports `Client` from `internal/mempalace`. The type/method identities are preserved so hygiene's existing tests continue to compile; only the import path changes.

Second, two new methods on `*Client`:

```go
// client.go (new home for Client) — additions
type Triple struct {
    Subject   string
    Predicate string
    Object    string
    ValidFrom time.Time
}

func (c *Client) AddDrawer(ctx context.Context, wing, room, content string) error
func (c *Client) AddTriples(ctx context.Context, triples []Triple) error
```

Both use the same `docker exec -i <container> python -m mempalace.mcp_server --palace <path>` transport the M2.2 `Query` method established. Each method spawns a short-lived subprocess per call: `initialize` → `tools/call mempalace_add_drawer` (or N× `mempalace_kg_add`) → close stdin → parent reads stdout, returns on first error.

Note: `AddTriples` issues N tool_calls per MemPalace 3.3.2 semantics (no batch tool on the server side). The supervisor holds the Postgres tx open across these N calls — FR-261's atomic tx includes all of them.

`DockerExec` seam is unchanged from M2.2; unit tests inject a fake that returns canned JSON-RPC responses and asserts on the request stream.

### `internal/hygiene/evaluator.go`

Extend with a new function:

```go
func EvaluateFinalizeOutcome(instance store.AgentInstance, hasTransition bool) string
```

Returns the new hygiene_status vocabulary value derived purely from `exit_reason` + transition presence, per spec FR-269:
- `completed` + transition exists → `clean`
- `finalize_invalid` → `finalize_failed`
- `finalize_palace_write_failed` / `finalize_commit_failed` / `finalize_write_timeout` → `finalize_partial`
- no transition AND `exit_reason ∈ {finalize_never_called, timeout}` → `stuck`
- else → `pending`

The existing `*Client.Query` palace-query path is preserved but not invoked for M2.2.1 rows. Legacy-value rows (`missing_diary`, etc.) pass through unchanged; readers already tolerate both vocabularies.

The listener + sweep goroutines (M2.2) are unchanged except for dispatching to `EvaluateFinalizeOutcome` instead of the M2.2 drawer/triple-counting evaluator for rows whose `exit_reason` starts with `finalize_` or equals `completed` + has finalize-shaped related data.

## New package `internal/finalize`

### `server.go`

MCP protocol loop over stdin/stdout. Handles three methods:

- `initialize` — returns `{protocolVersion: "2024-11-05", capabilities: {tools: {}}, serverInfo: {name: "garrison-finalize", version: <build>}}`.
- `tools/list` — returns the single tool descriptor from `tool.go`'s `ToolDescriptor` function.
- `tools/call` with `name == "finalize_ticket"` — calls `handler.Handle(ctx, params)`; returns the structured result.
- Any other method → `-32601 Method not found`.

Reads `GARRISON_AGENT_INSTANCE_ID` from env at startup. Opens a `pgxpool.Pool` via `GARRISON_DATABASE_URL`. On stdin EOF, exits cleanly.

### `tool.go`

Schema definition:

```go
func ToolDescriptor() ToolSchema {
    // Returns MCP tool-schema JSON:
    // name: "finalize_ticket"
    // description: "... garrison.finalize_ticket.v1 ..."
    // input_schema: JSON-schema object per spec FR-253
}

func Validate(raw json.RawMessage) (*FinalizePayload, *ValidationError)
```

`FinalizePayload` is a Go struct mirroring the schema. `ValidationError` carries `{ErrorType, Field, Message}`.

Validation is a hand-written type-switch over the decoded payload — no JSON-Schema library dependency. Each constraint (min/max lengths, array sizes, UUID shape, ISO timestamp or "now") is a focused check returning on first failure.

**`valid_from` literal substitution**: the `Validate` function is the sole substitution point for the `"now"` literal. If a triple's `valid_from` equals `"now"` (case-sensitive), `Validate` replaces it with `time.Now().UTC()` before returning the populated `FinalizePayload`. All downstream code (Handler's already-committed check, WriteFinalize's AddTriples call) sees concrete `time.Time` values only — no "now" strings propagate past the validator. This decision keeps substitution deterministic at validation time and avoids coupling the MemPalace client to a clock.

Constants for the caps live here: `OutcomeMin=10`, `OutcomeMax=500`, `RationaleMin=50`, `RationaleMax=4000`, `ArtifactArrayMax=50`, `KGTripleArrayMin=1`, `KGTripleArrayMax=100`, `TripleFieldMin=3`, `TripleFieldMax=500`.

### `handler.go`

```go
type Handler struct {
    Pool            *pgxpool.Pool
    AgentInstanceID pgtype.UUID
    Logger          *slog.Logger
}

func (h *Handler) Handle(ctx context.Context, rawParams json.RawMessage) (result json.RawMessage, err error)
```

Steps per call:
1. Validate payload via `tool.Validate`. On failure → `{ok: false, error_type: "schema", field, message, attempt: <locally-tracked>}`.
2. Query Postgres via `queries.SelectAgentInstanceFinalizedState(ctx, h.AgentInstanceID)`. Returns `(status, hasTransition bool)`.
3. If `status == "succeeded"` AND `hasTransition`: return `{ok: false, error_type: "schema", field: null, message: "finalize_ticket already succeeded for this agent_instance", attempt: ...}`.
4. Else: return `{ok: true, attempt: <locally-tracked>}`.

The local attempt counter inside the handler is a simple incrementing int in the handler struct (reset on server start). It's informational — the supervisor's counter is authoritative.

Note: the handler does NOT perform the atomic write. The supervisor's stream-json parser sees the successful tool_use and calls `spawn.WriteFinalize` directly. The handler's sole responsibility is validation + already-committed check + returning the tool result.

### `mcp_finalize.go` (in `cmd/supervisor/`)

Minimal subcommand entry point:

```go
func runMCPFinalize(args []string) int {
    cfg := readFinalizeConfig()  // reads env vars
    pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
    if err != nil { /* log, exit 1 */ }
    defer pool.Close()
    
    srv := finalize.NewServer(finalize.Deps{
        Pool: pool,
        AgentInstanceID: cfg.AgentInstanceID,
        Logger: slog.Default(),
    })
    return srv.Run(ctx, os.Stdin, os.Stdout)
}
```

Parallel to M2.1's `mcp_postgres.go`.

## Subsystem state machines

### Finalize attempt state machine (supervisor-side, per agent_instance)

```
     spawn                                 
       │                                   
       ▼                                   
   ┌──────────┐                            
   │ attempts=0,                           
   │ committed=false                       
   └──────────┘                            
       │                                   
       │  [tool_use(finalize_ticket) seen]  
       │  attempts += 1                    
       ▼                                   
   ┌──────────┐                            
   │  wait    │                            
   │  for     │                            
   │  result  │                            
   └──────────┘                            
       │                                   
    ┌──┴──────────────────────────┐        
    │                             │        
  [ok=true]                  [ok=false]    
    │                             │        
    ▼                             ▼        
WriteFinalize             attempts < 3?    
    │                    ┌──┴──┐           
 ┌──┴─────┐              │     │           
 │  OK    │            yes     no          
 │  FAIL  │              │     │           
 └────────┘              │     ▼           
    │                    │   SIGTERM       
 committed=true          │   exit_reason=  
 (or partial)            │   finalize_invalid
    │                    │                 
    ▼                    ▼                 
  done             back to "wait"          
```

### Atomic-write state machine (inside `spawn.WriteFinalize`)

```
  enter (ctx=WithTimeout(WithoutCancel(parent), 30s))
       │
       ▼
  BEGIN Postgres tx
       │
       ▼
  SELECT tickets.objective
       │
       ▼
  MemPalace.AddDrawer(wing, room="hall_events", content=<serialized>)
       │
    ┌──┴─────┐
    │        │
   ok      err
    │        │
    │        ▼
    │    ROLLBACK + exit_reason=finalize_palace_write_failed
    │        
    ▼
  MemPalace.AddTriples(triples)   ←─ loops N times internally
       │
    ┌──┴─────┐
    │        │
   ok      err
    │        │
    │        ▼
    │    ROLLBACK + exit_reason=finalize_palace_write_failed (orphan from AddDrawer noted in log)
    │        
    ▼
  INSERT ticket_transitions (hygiene_status='clean')
  UPDATE tickets SET column_slug=<to>
  UPDATE agent_instances SET status='succeeded', ...
  UPDATE event_outbox SET processed_at=NOW()
       │
       ▼
  COMMIT
       │
    ┌──┴─────┐
    │        │
   ok      err
    │        │
    │        ▼
    │    log palace_write_orphaned + exit_reason=finalize_commit_failed
    │
    ▼
  hygiene_status='clean', done

  [timeout fires at any step]
       │
       ▼
  cancel ctx → Postgres auto-rollback → log finalize_write_timeout
  exit_reason=finalize_write_timeout; hygiene_status='finalize_partial'
```

`WriteFinalize` returns either `nil` (success) or a typed error indicating which branch fired. The supervisor's caller (pipeline.go's stream-parser handler) logs outcome + writes any additional terminal row state per the error branch.

## Data model + migration

### Migration `migrations/20260424000005_m2_2_1_finalize_ticket.sql`

Content (no schema DDL):

```sql
-- M2.2.1 migration: finalize_ticket tool wiring.
-- Updates the two seed agent_md values via the +embed-agent-md tooling.
-- No schema changes: hygiene_status and exit_reason are already TEXT.
-- Legacy M2.2 hygiene_status values coexist with new M2.2.1 values; no
-- CHECK constraint is added (see plan §"Decisions baked into this plan"
-- item 12 for rationale).

-- +goose Up

UPDATE agents
SET agent_md =
-- +embed-agent-md:engineer:begin
  $engineer_md$ ... $engineer_md$
-- +embed-agent-md:engineer:end
WHERE role_slug = 'engineer';

UPDATE agents
SET agent_md =
-- +embed-agent-md:qa-engineer:begin
  $qa_engineer_md$ ... $qa_engineer_md$
-- +embed-agent-md:qa-engineer:end
WHERE role_slug = 'qa-engineer';

-- +goose Down
-- Reverting to M2.2 seed content is equivalent to re-running M2.2's
-- migration's embed blocks; handled by operator rollback discipline, not
-- by a Down block (AGENTS.md: Down blocks are optional for seed-only
-- migrations that the operator would reverse by re-deploying the prior
-- milestone's image).
```

### New sqlc queries

**`migrations/queries/tickets.sql` additions:**

```sql
-- name: SelectTicketObjective :one
SELECT objective FROM tickets WHERE id = $1;
```

**`migrations/queries/agent_instances.sql` additions:**

```sql
-- name: SelectAgentInstanceFinalizedState :one
SELECT
    ai.status,
    EXISTS(
        SELECT 1 FROM ticket_transitions tt
        WHERE tt.triggered_by_agent_instance_id = ai.id
    ) AS has_transition
FROM agent_instances ai
WHERE ai.id = $1;
```

Regenerate `internal/store/` via `sqlc generate` after editing.

### Role and DSN for the finalize MCP server

The finalize server reads `agent_instances` and `ticket_transitions`. Both are SELECT operations. The existing `garrison_agent_ro` role (M2.1) has `SELECT` on both tables; the finalize server reuses this role. `GARRISON_DATABASE_URL` in the MCP config entry carries the `garrison_agent_ro` DSN (identical to what `internal/pgmcp` uses).

No new Postgres role. No new grants.

## Error vocabulary

### Tool response error_type enum

| Value | Triggered by | Retry-counted? |
|-------|-------------|----------------|
| `"schema"` | Validate() failure; also post-commit rejection | Yes, EXCEPT post-commit (committed=true short-circuits increment) |
| `"palace_write"` | MemPalace `AddDrawer`/`AddTriples` errored during atomic write | N/A — tool call already succeeded; this error is emitted by the supervisor's WriteFinalize failure path, not the server |
| `"transition_write"` | Postgres `INSERT ticket_transitions` errored during atomic write | N/A — same as above |
| `"budget_exhausted"` | Budget cap fired during retry loop before tool_call completed | N/A — M2.1 path handles this |

The `palace_write` / `transition_write` / `budget_exhausted` error_types are emitted as supervisor-side log events, not as tool_call responses (the tool call already returned ok=true before these failures surface). This is a subtle but important distinction: the agent sees `{ok: true}` from the server; the supervisor then encounters the failure during the atomic write and writes a different terminal row than the agent expected. The agent may issue further tool_calls post-response; those are observed but not acted on.

### Exit reason additions

Per §"Changes to existing M2.2 packages > exitreason.go" above. All six are new. No existing exit reasons are removed or renamed.

### Hygiene status vocabulary

New rows (M2.2.1+): `clean | finalize_failed | finalize_partial | stuck | pending`.
Legacy rows (M2.2): `clean | missing_diary | missing_kg | thin | pending` preserved as-is.

Both vocabularies coexist in the `hygiene_status` TEXT column. No CHECK constraint. Reader code in `internal/hygiene` and any future dashboard (M3) must accept both.

## Seed content

Seed files `migrations/seed/engineer.md` and `migrations/seed/qa-engineer.md` are rewritten. Target 3000–4000 chars each per spec FR-270/FR-271 and context §"Implementation notes." The actual content is drafted as a task deliverable (T005); this plan commits to:

**Structure (both files):**
```
# <Role> (M2.2.1)

## Role
<one paragraph>

## Wake-up context
<one paragraph; M2.2 wake-up mechanism unchanged>

## Work loop
<bullets: read ticket, do work, optional mid-turn mempalace_add_drawer to hall_discoveries>

## Mid-turn MemPalace usage (optional)
<one paragraph; explains hall_discoveries pattern; preserved from M2.2>

## Completion
<one paragraph; instructs agent to call finalize_ticket with full payload; names the fields; says "this is the only way to complete">

## Tools available
<list>

## What you do not do
<bullets: no direct tickets table writes, no mempalace_add_drawer for completion diary, etc.>

## Failure modes
<brief; references what the supervisor does when finalize fails>
```

Both files embedded into the migration via `+embed-agent-md:<role>:begin|end` markers consumed by `supervisor/internal/tools/embed-agent-md/` (unchanged M2.2 tooling).

## Test strategy

### Unit tests

| File | Function | Verifies |
|------|----------|----------|
| `internal/finalize/server_test.go` | `TestServerInitResponse` | `initialize` returns valid MCP response with `protocolVersion` and `serverInfo.name == "garrison-finalize"` |
| `internal/finalize/server_test.go` | `TestServerToolsList` | `tools/list` returns exactly one tool named `finalize_ticket`; `description` contains `garrison.finalize_ticket.v1` |
| `internal/finalize/server_test.go` | `TestServerExitsOnStdinEOF` | Server exits cleanly when stdin EOFs; return code 0 |
| `internal/finalize/server_test.go` | `TestServerRejectsUnknownMethod` | `tools/call` with `name="something_else"` returns `-32601` |
| `internal/finalize/tool_test.go` | `TestValidateHappyPath` | Valid minimum payload passes; returns populated `FinalizePayload` struct |
| `internal/finalize/tool_test.go` | `TestToolDescriptorIsV1` | Descriptor's description contains schema version tag |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsMissingTicketID` | Missing `ticket_id` → `ValidationError{ErrorType:"schema", Field:"ticket_id"}` |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsOutcomeTooShort` | `outcome` ≤ 9 chars rejected |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsOutcomeTooLong` | `outcome` ≥ 501 chars rejected |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsRationaleTooShort` | `diary_entry.rationale` < 50 chars rejected |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsRationaleTooLong` | `diary_entry.rationale` > 4000 chars rejected |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsEmptyKGTriples` | `kg_triples: []` rejected (min 1) |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsTooManyKGTriples` | `kg_triples` > 100 rejected |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsTripleFieldTooShort` | triple subject/predicate/object < 3 chars rejected |
| `internal/finalize/schema_test.go` | `TestSchemaRejectsMalformedUUID` | `ticket_id` not UUID-shaped rejected |
| `internal/finalize/schema_test.go` | `TestSchemaAcceptsValidFromNowLiteral` | `valid_from: "now"` accepted, substituted to wall clock at validate time |
| `internal/finalize/schema_test.go` | `TestSchemaAcceptsValidFromISO` | `valid_from: "2026-04-23T12:00:00Z"` accepted |
| `internal/spawn/pipeline_test.go` | `TestFinalizeAttemptCounterIncrementsOnEachToolUse` | Counter goes 0→1→2→3 across three consecutive finalize tool_use events |
| `internal/spawn/pipeline_test.go` | `TestFinalizeAttemptCapTriggersSIGTERM` | 3rd failed tool_result triggers process-group SIGTERM with `exit_reason=finalize_invalid` |
| `internal/spawn/pipeline_test.go` | `TestFinalizeAttemptCounterIgnoresPostCommitCalls` | 4th tool_use after committed=true does NOT increment counter |
| `internal/spawn/finalize_test.go` | `TestWriteFinalizeHappyPathCommitsAtomically` | Given a fake DockerExec + testcontainer Postgres, `WriteFinalize` commits all 6 DML ops; post-commit queries return expected state |
| `internal/spawn/finalize_test.go` | `TestWriteFinalizeRollsBackOnPalaceError` | DockerExec returns error on AddDrawer; tx rolls back; no `ticket_transitions` row; exit_reason=`finalize_palace_write_failed` |
| `internal/spawn/finalize_test.go` | `TestWriteFinalizeRollsBackOnTripleError` | DockerExec succeeds on AddDrawer, fails on first AddTriples; tx rolls back; log notes orphan AddDrawer |
| `internal/spawn/finalize_test.go` | `TestWriteFinalizeTimeoutFiresAt30s` | Fake DockerExec sleeps 35s; ctx timeout fires at 30s; tx rolls back; exit_reason=`finalize_write_timeout` |
| `internal/spawn/finalize_test.go` | `TestWriteFinalizeSerializesDiaryWithObjectivePrepend` | Asserts the AddDrawer `content` argument starts with `ticket.objective`, followed by `---`, YAML, `---`, then rationale |
| `internal/spawn/spawn_test.go` | `TestAdjudicateBudgetWinsOverFinalizeInvalid` | When both conditions hold, exit_reason=`budget_exceeded` |
| `internal/spawn/spawn_test.go` | `TestAdjudicateTimeoutWinsOverNeverCalled` | When subprocess times out without finalize, exit_reason=`timeout` (not `finalize_never_called`) |
| `internal/mempalace/writes_test.go` | `TestAddDrawerIssuesCorrectJSONRPC` | Fake DockerExec captures the stream; request is valid JSON-RPC with `tools/call` + `mempalace_add_drawer` + correct args |
| `internal/mempalace/writes_test.go` | `TestAddTriplesIssuesNCalls` | 3 triples → 3 `tools/call` requests with `mempalace_kg_add`; each triple surfaced separately |
| `internal/mempalace/writes_test.go` | `TestAddDrawerPropagatesExitError` | DockerExec non-zero exit → `AddDrawer` returns wrapped error |
| `internal/hygiene/evaluator_test.go` | `TestEvaluateFinalizeOutcomeClean` | `completed` + transition exists → `clean` |
| `internal/hygiene/evaluator_test.go` | `TestEvaluateFinalizeOutcomeFinalizeFailed` | `finalize_invalid` → `finalize_failed` |
| `internal/hygiene/evaluator_test.go` | `TestEvaluateFinalizeOutcomeFinalizePartial` | Each of the three triggering exit_reasons → `finalize_partial` |
| `internal/hygiene/evaluator_test.go` | `TestEvaluateFinalizeOutcomeStuck` | no transition + `finalize_never_called` or `timeout` → `stuck` |
| `internal/hygiene/evaluator_test.go` | `TestEvaluateFinalizeOutcomeLegacyPassthrough` | Row with `missing_diary` passes through unchanged |
| `internal/mcpconfig/mcpconfig_test.go` | `TestBuildConfigIncludesFinalizeEntry` | Third entry `finalize` present; env carries both `GARRISON_AGENT_INSTANCE_ID` and `GARRISON_DATABASE_URL` |
| `internal/config/config_test.go` | `TestConfigParsesFinalizeWriteTimeout` | Env `GARRISON_FINALIZE_WRITE_TIMEOUT=45s` → `cfg.FinalizeWriteTimeout == 45*time.Second` |
| `internal/config/config_test.go` | `TestConfigRejectsNegativeFinalizeTimeout` | Env `GARRISON_FINALIZE_WRITE_TIMEOUT=-1s` → startup error |

### Integration tests (build tag `integration`)

All under `supervisor/integration_m2_2_1_*_test.go`. Reuse the M2.2 spike stack (`spike-mempalace` + `spike-docker-proxy`) via the existing `requireSpikeStack` helper.

| File | Function | Verifies (spec criterion) |
|------|----------|----------------------------|
| `integration_m2_2_1_happy_path_test.go` | `TestM221FinalizeHappyPath` | US1 + US2: single ticket routed in_dev → qa_review → done; two clean atomic writes; two wings populated; combined cost < $0.20 (criteria 3, 4, 5) |
| `integration_m2_2_1_retry_test.go` | `TestM221FinalizeRetryThenSuccess` | Fixture `finalize_retry_then_success.ndjson`: 2 invalid + 1 valid; clean commit; 2 failed attempts in logs only (criterion 7, SC-252) |
| `integration_m2_2_1_retry_test.go` | `TestM221FinalizeFailsAfterThreeRetries` | Fixture `finalize_retry_exhausted.ndjson`: 3 invalid; exit_reason=finalize_invalid; hygiene_status=finalize_failed; no transition row (criterion 8, SC-253) |
| `integration_m2_2_1_stuck_test.go` | `TestM221FinalizeStuckWhenNeverCalled` | Fixture `finalize_never_called.ndjson`: result event without any finalize call; hygiene_status=stuck; no transition (criterion 9, SC-254) |
| `integration_m2_2_1_midturn_test.go` | `TestM221MidTurnWritesPreserved` | Fixture `finalize_midturn_then_finalize.ndjson`: mid-turn hall_discoveries write + successful finalize; both exist in palace post-run (criterion 10, SC-256) |
| `integration_m2_2_1_compliance_test.go` | `TestM221ComplianceModelIndependent` | **live_acceptance tag** — runs happy path twice with `GARRISON_CLAUDE_MODEL=claude-haiku-4-5-20251001` and `claude-opus-4-7`; identical outcomes (criterion 6, SC-261) |

### Chaos test (build tag `chaos`)

| File | Function | Verifies |
|------|----------|----------|
| `chaos_m2_2_1_test.go` | `TestM221AtomicWriteChaosPalaceKillMidTransaction` | Valid finalize; MemPalace sidecar `docker stop`'d between `AddDrawer` and commit; observed outcomes: no transition row, hygiene_status=`finalize_partial`, exit_reason either `finalize_palace_write_failed` or `finalize_commit_failed`, no panic, supervisor continues (criterion 11, SC-255) |

### Regression check

All M1, M2.1, M2.2 integration and chaos tests MUST pass unchanged against the M2.2.1 branch. The M2.2 hygiene evaluator's legacy-value path is exercised by M2.2's existing tests; those tests' fixtures continue to use `missing_diary`/`thin`/`missing_kg` and assertions remain valid. SC-257 codifies this.

### Mock claude fixtures

Six new NDJSON files under `internal/spawn/mockclaude/fixtures/m2_2_1/`. Each fixture emits the init event + a sequence of assistant events + (for each case) a different mix of `finalize_ticket` tool_use / tool_result pairs + the terminal result event. Mockclaude's per-role dispatch (M2.2) gains a new directive shape recognising the M2.2.1 fixtures; the dispatcher key is the ticket_id prefix (same pattern M2.2 uses).

## Open questions (flagged; not resolved)

None blocking implementation. The following are noted for future milestones:

1. **Dashboard surfacing of `finalize_partial` orphans (M3)**: when `exit_reason=finalize_commit_failed` (palace has an orphan drawer/triples but Postgres tx rolled back), operator reconciliation is manual in M2.2.1. M3's dashboard work should offer a "reconcile orphan palace writes" action.
2. **Automated compliance matrix**: SC-261's haiku-vs-opus test is operator-invoked (`live_acceptance` tag). Automating it in CI requires parameterizing the Anthropic API key and budget allocation per run; deferred to operator choice.
3. **`mempalace_search` query shape**: the objective-prose-prepend (FR-263) makes search work for engineer diaries written by the supervisor. Agent-written mid-turn `hall_discoveries` drawers are NOT objective-prepended by the supervisor (those are agent-composed). The agent is responsible for making its mid-turn drawers searchable. This is consistent with M2.2's retro note about query-shape tuning being M3 work.

---

**End of plan.** Ready for `/garrison-tasks m2-2-1`.
