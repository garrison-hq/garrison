# Implementation plan: M5.3 — Chat-driven mutations (autonomous-execution posture)

**Branch**: `012-m5-3-chat-driven-mutations` | **Date**: 2026-04-29 | **Spec**: [spec.md](./spec.md)
**Input**: [spec.md](./spec.md) (FR-400 … FR-541, SC-300 … SC-320), [m5-3-context.md](../_context/m5-3-context.md), M5.2 [retro](../../docs/retros/m5-2.md) + [plan](../011-m5-2-ceo-chat-frontend/plan.md), M5.1 [spec](../010-m5-1-ceo-chat-backend/spec.md) + [retro](../../docs/retros/m5-1.md), [M5 spike](../../docs/research/m5-spike.md), [vault threat model](../../docs/security/vault-threat-model.md), M2.2.1 [retro](../../docs/retros/m2-2-1.md) + `internal/finalize`, [AGENTS.md](../../AGENTS.md), [RATIONALE.md](../../RATIONALE.md), [ARCHITECTURE.md](../../ARCHITECTURE.md), existing supervisor + dashboard codebases.

---

## Summary

M5.3 adds chat-driven mutations under an autonomous-execution posture. The chat container runs with `--permission-mode bypassPermissions` (already true from M5.1) and gains a third MCP server, `garrison-mutate`, exposing nine sealed verbs across three domains: tickets (`create_ticket`, `edit_ticket`, `transition_ticket`), agents (`pause_agent`, `resume_agent`, `spawn_agent`, `edit_agent_config`), and hiring (`propose_hire`). Vault is explicitly NOT in the verb set; M2.3 Rule 3 carries to chat.

The autonomous posture is justified by a new chat threat-model amendment (`docs/security/chat-threat-model.md`) that lands BEFORE any verb code commits, mirroring the M2.3 vault-threat-model-first pattern. The amendment enumerates assets, adversaries, threats addressed (sealed verb set, vault opacity, audit + notify per call, cost cap, per-turn tool-call ceiling), and threats explicitly accepted with named runtime mitigations. It carries six numbered architectural rules (N through N+5) and a per-verb reversibility-tier table.

The new supervisor package `supervisor/internal/garrisonmutate/` mirrors the `internal/finalize/` precedent: in-tree Go MCP server, `supervisor mcp garrison-mutate` subcommand, in-process JSON-RPC over stdio, atomic Postgres transactions per call with audit row + post-commit `pg_notify`. The verb registry is a single Go slice in `verbs.go` that both the runtime and the sealed-allow-list test read; no runtime registration, no plugin shape.

A single migration adds two new tables (`chat_mutation_audit`, `hiring_proposals`) plus a nullable `tickets.created_via_chat_session_id` FK column. New chat-namespaced `pg_notify` channels (`work.chat.<entity>.<action>`) follow the M5.1/M5.2 `work.chat.session_*` precedent; the M3 ticket-transition listener stays unchanged.

Dashboard side, M5.3 ships a `ToolCallChip.tsx` component that renders inline in the message stream for every `tool_use` event (read tools low-emphasis, mutation tools higher-emphasis, failures in M5.2's error palette). The `useChatStream` hook gains `tool_use` / `tool_result` event-union variants. The activity feed gets new `EventRow` branches for every chat-mutation channel plus the three M5.1-deferred chat lifecycle channels (closing the M5.2 retro carryover). A new read-only `/hiring/proposals` page lets the operator review chat-driven hiring proposals before M7 ships.

Per-turn tool-call ceiling enforcement lives in `internal/chat/policy.go` (counts ALL tool calls including reads); concurrency conflict resolution uses Postgres `SELECT … FOR UPDATE NOWAIT` mapped to `error_kind='ticket_state_changed'`.

`ARCHITECTURE.md` gains two amendments pinned by substring-match assertion test (extending M5.2's pattern). Zero new direct dependencies in either `supervisor/go.mod` or `dashboard/package.json`.

---

## Technical Context

**Language/Version**: Go 1.23 (supervisor — new package + mcpconfig extension + chat policy extension); TypeScript 5.6+ / Next.js 16 / React 19 (dashboard — new components + hook extensions + new route + new actions/queries); SQL (one new migration, `migrations/queries/garrisonmutate.sql` + `migrations/queries/hiring.sql`).

**Primary Dependencies (supervisor)**: existing locked list. M5.3 adds **zero new dependencies**. Reuses `pgx/v5` for Postgres + transactions, `slog` for structured logging, `testify` + `testcontainers-go` for tests, `internal/store` (sqlc-generated) for query types, `internal/claudeproto` for stream-json routing, `internal/concurrency` for capacity caps. The `garrisonmutate` MCP server uses stdlib `encoding/json` + `bufio` for stdio JSON-RPC, mirroring `internal/finalize`.

**Primary Dependencies (dashboard)**: existing locked list. M5.3 adds **zero new dependencies**. Reuses Drizzle ORM, `postgres-js` for Postgres LISTEN, better-auth, the `@axe-core/playwright` dev dep already approved in M5.2, the existing M3/M4 design-system primitives (`Chip`, `StatusDot`, `EmptyState`), the M5.2 `useChatStream` hook (extended).

**Storage**: shared Postgres. Two new tables (`chat_mutation_audit`, `hiring_proposals`). One new column (`tickets.created_via_chat_session_id` UUID NULL FK → `chat_sessions(id)`). New CHECK constraints on `chat_mutation_audit.outcome` and `hiring_proposals.status` / `proposed_via`. New grants: `SELECT ON chat_mutation_audit TO garrison_dashboard_app`; `SELECT, INSERT ON hiring_proposals TO garrison_dashboard_app`. Supervisor's own role retains write access to both tables.

**Testing**:
- Go test for `internal/garrisonmutate/` (unit + per-verb + integration + chaos), `internal/mcpconfig/` extensions, `internal/chat/policy.go` extensions.
- Vitest for `dashboard/lib/sse/chatStream.ts` extensions, `dashboard/lib/queries/hiring.ts`, `dashboard/lib/actions/hiring.ts`, the new `ToolCallChip.tsx` static-pin tests.
- Playwright for the chat-mutation golden-path e2e (`dashboard/tests/integration/m5-3-chat-mutations.spec.ts`) extending the M5.2 `_chat-harness.ts` fixture set.
- testcontainer-driven supervisor integration tests for verb atomicity, concurrent-mutation lock semantics, leak-scan rejection, cost-cap + tool-call-ceiling chaos, three prompt-injection chaos fixtures (AC-1/2/3).
- Architecture amendment substring test (`dashboard/tests/architecture-amendment.test.ts`) extended for two new substrings.

**Target Platform**: Linux container for supervisor and dashboard. Browser baseline matches M5.2 (`>=Chrome 130, >=Firefox 130, >=Safari 17.5`). The chat container `garrison-claude:m5` is unchanged; M5.3 extends `garrison-mockclaude:m5` with new fixture scripts.

**Project Type**: Web application — supervisor extension (new package + existing-package extensions) + dashboard extension (component + hook + route + action additions) + one migration.

**Performance Goals**:
- Operator chat instruction → first post-call chip render ≤ SC-010's 10s round-trip budget against the full M5.1 + M5.2 + M5.3 stack (per SC-300).
- First post-call chip renders within 1s of the corresponding `tool_result` SSE event arriving (per SC-320).
- The Playwright suite (M3 + M4 + M5.1 + M5.2 + M5.3) stays under 12 minutes total per the M5.1 SC-010 ceiling (per SC-319).
- Per-verb commit transactions complete within the existing M2.x ticket-write latency envelope; no per-verb performance bound is added beyond what `internal/finalize` already meets.

**Constraints**:
- AGENTS.md concurrency rules 1, 2, 6 bind on the new `internal/garrisonmutate/` package and the `internal/chat/policy.go` ceiling-enforcement extension.
- Threat-model amendment must land BEFORE any verb code commits to the branch (FR-400; M2.3 vault-threat-model-first pattern).
- Sealed MCP allow-list (FR-430 / FR-431 / FR-432) enforced at config-build time with CI-pinned test (FR-433).
- Rule 6 backstop applies to all new chat-namespaced `pg_notify` channels: payloads carry IDs only, never raw chat content (FR-463).
- M2.3 vault rules continue: Rule 1 (no secrets in prompts) extends to `edit_agent_config` arg; Rule 3 (vault opacity) extends to chat (verb set excludes vault).
- Single-operator single-CEO posture continues; the threat-model amendment's autonomous-execution justification depends on this.
- `tools/vaultlog` go vet analyzer must pass on all new `internal/garrisonmutate/` code paths (FR-541).

**Scale/Scope**: single operator, single concurrent chat session per Constitution X (carryover from M5.1). The verb set is sealed at 9 verbs; future verb additions require code change + threat-model amendment update + test update. Per-turn tool-call ceiling defaults to 50 (configurable via `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN`).

---

## Constitution Check

Garrison constitution (`.specify/memory/constitution.md`) gates:

- **Principle I (Postgres + pg_notify)**: M5.3 honours it. Eight new chat-namespaced channels (`work.chat.ticket.created`, `work.chat.ticket.edited`, `work.chat.ticket.transitioned`, `work.chat.agent.paused`, `work.chat.agent.resumed`, `work.chat.agent.spawned`, `work.chat.agent.config_edited`, `work.chat.hiring.proposed`) follow the M5.1/M5.2 `work.chat.session_*` precedent. Notify fires post-commit (after the verb's data + audit transaction commits, per D20). No HTTP-only side channels; no in-memory state shared between supervisor and dashboard for mutation outcomes.
- **Principle II (MemPalace as memory layer)**: chat continues to read MemPalace via the chat container's `mempalace` MCP server. M5.3 changes nothing here. The new mutation verbs do NOT write to MemPalace — diary entries continue to be agent-spawn-side via `internal/finalize.WriteFinalize`. If a future verb wants palace writes, it requires a threat-model amendment update.
- **Principle III (ephemeral agents)**: chat container lifecycle is unchanged from M5.1 (one container per turn, `--rm`). M5.3 verbs run inside the existing per-turn lifecycle.
- **Principle IV (soft gates)**: M5.3 introduces zero hard gates on chat content. The autonomous-execution posture is the deliberate inversion of "hard gate per call" — observability layers (chip + activity feed + audit row) replace per-call approval. The threat-model amendment justifies why this is acceptable.
- **Principle V (skills.sh)**: not applicable — M5.3 verbs do not interact with the skills layer. M7 owns skills-related work.
- **Principle VI (UI-driven hiring)**: `propose_hire` writes a `hiring_proposals` row; the review/approve/spawn flow is M7. The stopgap `/hiring/proposals` page is read-only and aligns with the constitution's "UI-driven, not git-driven" framing once M7 extends the page with edit/approve affordances. M5.3's stopgap page is a viewing affordance only.
- **Principle VII (Go-only supervisor with locked deps)**: M5.3 adds **zero** Go dependencies. The new `internal/garrisonmutate/` package uses only stdlib + already-locked deps. Dashboard side adds zero TypeScript deps.
- **Principle VIII (every goroutine has a context)**: the `garrison-mutate` MCP server runs as a subprocess of Claude (started per turn), driven by stdio JSON-RPC; no goroutines persist beyond the parent's lifecycle. The chat-policy ceiling-enforcement extension runs inside the existing session-worker goroutine that already accepts the supervisor's root context. No new goroutines.
- **Principle IX (narrow specs per milestone)**: M5.3 stays inside the FR-400 → FR-541 envelope. Out of scope per spec/context: per-call confirmation gate (rejected), vault mutations from chat (Rule 3 carryover), knowledge-base pane (M5.4), full hiring flow (M7), undo/rollback affordances, multi-operator, warm-pool, cost-telemetry blind spot, mobile-first.
- **Principle X (per-department concurrency caps)**: `spawn_agent` verb respects the per-department cap. The verb's implementation calls into the existing `internal/concurrency/cap.go` `prepareSpawn` shape with the same +1 race + advisory-lock-free pattern documented in M2.1. No new caps; no new lock primitives.
- **Principle XI (self-hosted)**: M5.3 introduces no cloud dependencies. All work is in-binary on the supervisor and self-hosted Next.js on the dashboard.

**Concurrency discipline §AGENTS.md "non-negotiable" rules**:

- **Rule 1** (every goroutine has a context): all new code in `internal/garrisonmutate/` accepts ctx. The MCP server's stdin loop reads in the parent's ctx; verb handlers accept ctx and pass it through to the `pgx` transaction. No bare `go func()`.
- **Rule 2** (root context owns SIGTERM cascade): if the supervisor receives SIGTERM mid-verb, the chat container's parent ctx cancels; in-flight verbs see ctx cancellation and the transaction either commits (within `TerminalWriteGrace` per Rule 6) or rolls back. The new audit-row write inside the same transaction inherits this guarantee.
- **Rule 6** (terminal write via WithoutCancel): post-commit `pg_notify` emits use `context.WithoutCancel(ctx)` + `TerminalWriteGrace` matching the M5.1 / `internal/finalize.WriteFinalize` pattern. Rationale: if the supervisor receives SIGTERM after the data commit but before the notify, the notify should still fire so the activity feed reflects the truth in Postgres.
- **Rule 7** (process-group lifecycle): the chat container Claude subprocess already runs in its own process group (M5.1). The `garrison-mutate` JSON-RPC subprocess spawned by Claude inherits that group. M5.3 changes nothing here.
- **Rule 8** (drain pipes before Wait): the `mcp garrison-mutate` subcommand reads stdin to EOF and writes stdout responses; standard JSON-RPC over stdio. Mirrors `internal/finalize`'s pipe handling. M5.3 introduces no new pipe shape.

**Scope discipline (AGENTS.md §)**: M5.3 introduces a new authority surface (chat-driven mutations) backed by a threat-model amendment. The amendment's "Threats accepted" section enumerates what the autonomous posture explicitly accepts. The verb set is sealed; the MCP allow-list is sealed; vault is opaque to chat. No expansion beyond the 9 verbs in M5.3; future verbs require explicit threat-model amendment updates before code lands.

**Constitutional violations to track**: zero. M5.3 ships zero new direct dependencies (FR-540). The autonomous-execution posture, while novel, is consistent with constitution Principle IV's soft-gates framing extended into a new domain via the threat-model amendment. No deviations from the locked-deps streak in either supervisor or dashboard.

---

## Project Structure

### Documentation (this feature)

```text
specs/012-m5-3-chat-driven-mutations/
├── spec.md                # /speckit.specify + /speckit.clarify output (committed)
├── plan.md                # this file
├── tasks.md               # /garrison-tasks output (NOT created by /garrison-plan)
└── acceptance-evidence.md # post-implementation (matches M3 + M4 + M5.1 + M5.2 pattern)
```

The context lives outside the milestone dir per Garrison convention:

```text
specs/_context/
└── m5-3-context.md
```

The threat-model amendment lives at:

```text
docs/security/
├── vault-threat-model.md   # M2.3, unchanged by M5.3
└── chat-threat-model.md    # M5.3, NEW — lands BEFORE verb code per FR-400
```

### Source code (repository root) — supervisor side

```text
supervisor/
├── cmd/supervisor/
│   └── main.go                       # Extended: new `mcp garrison-mutate` subcommand case
├── internal/
│   ├── chat/                         # M5.1/M5.2 substrate
│   │   ├── policy.go                 # Extended: per-turn tool-call ceiling enforcement; OnStreamEvent forwards tool_use/tool_result
│   │   ├── policy_test.go            # Extended: TestToolCallCeilingTerminatesContainer
│   │   ├── transport.go              # Extended: emit `event: tool_use` and `event: tool_result` SSE frames
│   │   └── transport_test.go         # Extended: tool_use/tool_result frame shape
│   ├── garrisonmutate/               # NEW: M5.3 mutation MCP server
│   │   ├── server.go                 # NEW: subcommand entry + stdio JSON-RPC loop
│   │   ├── server_test.go            # NEW: dispatch unit tests
│   │   ├── tool.go                   # NEW: verb dispatcher
│   │   ├── tool_test.go              # NEW: dispatch routing
│   │   ├── verbs.go                  # NEW: verb registry (single source of truth)
│   │   ├── verbs_test.go             # NEW: TestVerbsRegistryMatchesEnumeration (sealed-allow-list)
│   │   ├── verbs_tickets.go          # NEW: create_ticket, edit_ticket, transition_ticket
│   │   ├── verbs_tickets_test.go     # NEW: per-verb unit + atomicity tests
│   │   ├── verbs_agents.go           # NEW: pause_agent, resume_agent, spawn_agent, edit_agent_config
│   │   ├── verbs_agents_test.go      # NEW: per-verb unit + leak-scan parity tests
│   │   ├── verbs_hiring.go           # NEW: propose_hire
│   │   ├── verbs_hiring_test.go      # NEW: per-verb unit
│   │   ├── audit.go                  # NEW: chat_mutation_audit row writer
│   │   ├── audit_test.go             # NEW: audit-row write semantics
│   │   ├── notify.go                 # NEW: post-commit pg_notify emitter
│   │   ├── notify_test.go            # NEW: channel-name + payload shape
│   │   ├── validation.go             # NEW: per-verb arg validation helpers
│   │   ├── validation_test.go        # NEW: validation rule pins
│   │   ├── errors.go                 # NEW: MutateErrorKind enum + typed errors
│   │   ├── errors_test.go            # NEW: error-kind enumeration coverage
│   │   ├── integration_test.go       # NEW: testcontainer Postgres + mockclaude end-to-end
│   │   └── chaos_test.go             # NEW: AC-1/2/3 prompt-injection chaos + cost-cap + ceiling chaos
│   ├── mcpconfig/
│   │   ├── mcpconfig.go              # Extended: BuildChatConfig adds `garrison-mutate`; CheckExtraServers extends to vault rejection
│   │   └── mcpconfig_test.go         # Extended: TestBuildChatConfigSealsThreeEntries, TestBuildChatConfigRejectsVaultEntries
│   └── store/                        # sqlc-generated (do not hand-edit)
│       ├── chat_mutation_audit.sql.go  # NEW: generated from migrations/queries/garrisonmutate.sql
│       ├── hiring_proposals.sql.go     # NEW: generated from migrations/queries/hiring.sql
│       └── tickets.sql.go              # Regenerated: tickets.created_via_chat_session_id column included
└── mockclaude/                       # NEW or extended (M5.1/M5.2 baseline)
    └── fixtures/
        ├── m5_3_create_ticket_happy.ndjson         # NEW
        ├── m5_3_transition_ticket_happy.ndjson     # NEW
        ├── m5_3_edit_agent_config_leak_fail.ndjson # NEW
        ├── m5_3_propose_hire_happy.ndjson          # NEW
        ├── m5_3_compound_two_verbs.ndjson          # NEW
        ├── m5_3_chaos_ac1_palace_inject.ndjson     # NEW
        ├── m5_3_chaos_ac2_composer_inject.ndjson   # NEW
        ├── m5_3_chaos_ac3_feedback_loop.ndjson     # NEW
        └── m5_3_chaos_ceiling_breach.ndjson        # NEW
```

### Source code (repository root) — dashboard side

```text
dashboard/
├── app/[locale]/(app)/
│   ├── chat/                          # M5.2 routes (unchanged)
│   └── hiring/                        # NEW: stopgap surface
│       └── proposals/
│           └── page.tsx               # NEW: read-only Server Component
├── components/
│   ├── features/
│   │   ├── activity-feed/
│   │   │   └── EventRow.tsx           # Extended: 8 new chat-mutation branches + 3 deferred chat-lifecycle branches
│   │   └── ceo-chat/
│   │       ├── MessageStream.tsx      # Extended: render ToolCallChip between text content blocks
│   │       ├── MessageBubble.tsx      # Extended: tool-call chip composition
│   │       ├── ToolCallChip.tsx       # NEW: branched component (read vs mutate, pre vs post vs failure)
│   │       └── ToolCallChip.test.tsx  # NEW: renderToString static pin
│   └── layout/
│       └── Sidebar.tsx                # Extended: under "CEO chat" subnav, add "Hiring proposals" link (if D6 placement holds)
├── drizzle/
│   └── schema.supervisor.ts            # Regenerated post-migration
├── lib/
│   ├── actions/
│   │   ├── chat.ts                    # M5.2 actions (unchanged)
│   │   └── hiring.ts                  # NEW: read-only query wrappers (no mutations in M5.3)
│   ├── queries/
│   │   ├── chat.ts                    # M5.2 queries (unchanged)
│   │   └── hiring.ts                  # NEW: getProposalsForCurrentUser, getProposalById
│   └── sse/
│       ├── channels.ts                # Extended: 8 new chat-mutation channels + 3 deferred chat-lifecycle channels
│       ├── channels.test.ts           # Extended: assertion of new channels
│       ├── chatStream.ts              # Extended: tool_use + tool_result event union variants; toolCalls return field
│       ├── chatStream.test.ts         # Extended: tool variant handling tests
│       └── events.ts                  # Extended: 8 new ActivityEvent chat-mutation variants
└── tests/
    ├── architecture-amendment.test.ts # Extended: M5.3 substring assertions
    └── integration/
        ├── _chat-harness.ts           # Extended: garrison-mutate fixture support
        └── m5-3-chat-mutations.spec.ts # NEW: Playwright golden-path
```

### Migrations

```text
migrations/
├── 20260430000000_m5_3_chat_driven_mutations.sql  # NEW: 2 tables + 1 column + grants
└── queries/
    ├── garrisonmutate.sql             # NEW: verb writes + audit writes + audit reads
    └── hiring.sql                     # NEW: hiring_proposals reads + writes
```

### Architecture amendment

```text
ARCHITECTURE.md                         # Extended: line 574 substring + line 103 substring (FR-500)
```

**Structure decision**: extends the existing `supervisor/internal/` and `dashboard/` trees; introduces one new supervisor package (`internal/garrisonmutate/`) mirroring `internal/finalize/`. No restructuring of existing packages.

---

## §1. Package boundaries

### 1.1 New supervisor package: `internal/garrisonmutate/`

Owner: this milestone. Lifecycle: in-tree Go MCP server, supervisor subcommand. Scope: every chat-driven mutation verb implementation. No imports from `internal/chat/` (the chat package owns the per-turn lifecycle and ceiling enforcement; the mutate package is invoked downstream of that).

Public surface (consumed by `cmd/supervisor/main.go`):

```go
package garrisonmutate

// Server is the MCP JSON-RPC server. It runs over stdio, reads initialize +
// tools/list + tools/call requests, dispatches to the verb registry, and
// writes responses. Mirrors finalize.Server.
type Server struct { /* deps: db, slog */ }

func NewServer(deps Deps) *Server
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error
```

Internal surface (consumed only within the package):

```go
type Verb struct {
    Name                 string                       // "create_ticket", "edit_ticket", etc.
    Handler              HandlerFunc
    ReversibilityClass   int                          // 1, 2, or 3
    AffectedResourceType string                       // "ticket", "agent_role", "hiring_proposal"
}

type HandlerFunc func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error)

type Result struct {
    Success            bool        `json:"success"`
    AffectedResourceID string      `json:"affected_resource_id,omitempty"`
    AffectedResourceURL string     `json:"affected_resource_url,omitempty"`
    ErrorKind          string      `json:"error_kind,omitempty"`
    Message            string      `json:"message,omitempty"`
    Data               interface{} `json:"data,omitempty"`
}

var Verbs = []Verb{
    {Name: "create_ticket", Handler: handleCreateTicket, ReversibilityClass: 3, AffectedResourceType: "ticket"},
    {Name: "edit_ticket", Handler: handleEditTicket, ReversibilityClass: 2, AffectedResourceType: "ticket"},
    {Name: "transition_ticket", Handler: handleTransitionTicket, ReversibilityClass: 1, AffectedResourceType: "ticket"},
    {Name: "pause_agent", Handler: handlePauseAgent, ReversibilityClass: 1, AffectedResourceType: "agent_role"},
    {Name: "resume_agent", Handler: handleResumeAgent, ReversibilityClass: 1, AffectedResourceType: "agent_role"},
    {Name: "spawn_agent", Handler: handleSpawnAgent, ReversibilityClass: 3, AffectedResourceType: "agent_role"},
    {Name: "edit_agent_config", Handler: handleEditAgentConfig, ReversibilityClass: 2, AffectedResourceType: "agent_role"},
    {Name: "propose_hire", Handler: handleProposeHire, ReversibilityClass: 3, AffectedResourceType: "hiring_proposal"},
}
```

The `Verbs` slice is the SINGLE SOURCE OF TRUTH for the registered verb set. Adding a verb requires editing this file + the threat-model amendment + the per-verb test + the registry test. Removing a verb requires the same. The runtime registers tools dynamically from this slice in `tool.go`.

### 1.2 Existing-package extensions

**`supervisor/cmd/supervisor/main.go`** — gain a new switch case for `mcp garrison-mutate`:

```go
case "garrison-mutate":
    deps := garrisonmutate.Deps{ /* db, logger */ }
    server := garrisonmutate.NewServer(deps)
    return server.Run(ctx, os.Stdin, os.Stdout)
```

Mirrors the existing `mcp finalize` and `mcp postgres` cases.

**`supervisor/internal/chat/policy.go`** — extend with per-turn tool-call ceiling enforcement:

- Add a counter field to the per-session policy state (counts ALL `tool_use` events including reads — read tools count too because feedback loops can be triggered by `postgres.query` results).
- On each `tool_use` arrival, increment + check against `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN` (default 50).
- On breach: terminate the chat container with the existing termination shape, write a synthetic terminal row with `error_kind='tool_call_ceiling_reached'`, emit the SSE typed-error frame.
- Counter resets on each new turn (per-turn, not per-session).

Extend `OnStreamEvent` to forward `tool_use` and `tool_result` events to the SSE bridge in addition to the existing `delta` routing.

**`supervisor/internal/chat/transport.go`** — extend SSE frame emitter with `event: tool_use\ndata: {...}\n\n` and `event: tool_result\ndata: {...}\n\n` shapes. Frame payloads carry `messageId`, `toolUseId`, `toolName`, `args` (for tool_use) and `messageId`, `toolUseId`, `isError`, `result` (for tool_result).

**`supervisor/internal/mcpconfig/mcpconfig.go`** — extend `BuildChatConfig` to include the `garrison-mutate` MCP server entry alongside `postgres` and `mempalace`. Extend `CheckExtraServers` (Rule 3) to explicitly reject vault-named entries (`vault`, `infisical`, `garrison-vault`) with a typed `ErrVaultEntryRejected` error citing M2.3 Rule 3 carryover.

**`supervisor/internal/store/`** — sqlc-regenerates from the new `migrations/queries/*.sql` files. `tickets.sql.go` regenerates with the new `created_via_chat_session_id` column.

### 1.3 Dashboard package layout

**New components**: `dashboard/components/features/ceo-chat/ToolCallChip.tsx` — single component branching on tool name + result state. Pseudocode:

```tsx
function ToolCallChip({ toolUse, toolResult }: Props) {
  const isMutation = toolUse.toolName.startsWith('garrison-mutate.');
  const isFailure = toolResult?.isError;
  const isPreCall = !toolResult;

  if (isFailure) return <FailureChip toolUse={toolUse} toolResult={toolResult} />;
  if (isPreCall && isMutation) return <MutateChipPreCall toolUse={toolUse} />;
  if (!isPreCall && isMutation) return <MutateChipPostCall toolUse={toolUse} toolResult={toolResult} />;
  if (isPreCall && !isMutation) return <ReadChipPreCall toolUse={toolUse} />;
  return <ReadChipPostCall toolUse={toolUse} toolResult={toolResult} />;
}
```

Sub-components are local to the file (not exported). One component file, four internal variants. Avoids file proliferation per D7.

**`MessageStream.tsx`** — extended to render chips between text content blocks. The `useChatStream` hook return now includes `toolCalls: Map<messageId, ToolCallEntry[]>`; the renderer interleaves text deltas and chips per the `tool_use_id` arrival order.

**`MessageBubble.tsx`** — extended to accept a `toolCalls` prop and render them in stream-event order alongside text content blocks within the in-flight assistant bubble.

**New hook fields** in `dashboard/lib/sse/chatStream.ts`:

```ts
type ChatStreamEvent =
  | { type: 'delta', messageId: string, text: string }
  | { type: 'terminal', messageId: string, ... }
  | { type: 'tool_use', messageId: string, toolUseId: string, toolName: string, args: unknown }   // NEW
  | { type: 'tool_result', messageId: string, toolUseId: string, isError: boolean, result: unknown }; // NEW

type UseChatStreamReturn = {
  state: ChatStreamState,
  partialDeltas: Map<string, string>,
  terminals: Map<string, Terminal>,
  toolCalls: Map<string, ToolCallEntry[]>,    // NEW: keyed by messageId
  sessionEnded: boolean,
  lastError: ChatErrorKind | null,
};
```

`ToolCallEntry` is a tuple of `{toolUse, toolResult?}` representing the pre-call event and the (optional) corresponding result. The renderer sees both states and renders accordingly.

**New activity feed branches** in `dashboard/components/features/activity-feed/EventRow.tsx`: 11 new branches total — 8 chat-mutation channels + 3 deferred chat-lifecycle channels (M5.2 retro carryover closure):

- `chat.ticket.created` → `<ActorIcon /> chat created ticket #<id>`
- `chat.ticket.edited` → `<ActorIcon /> chat edited ticket #<id>`
- `chat.ticket.transitioned` → `<ActorIcon /> chat transitioned #<id>: <from> → <to>`
- `chat.agent.paused` → `<ActorIcon /> chat paused <role_slug>`
- `chat.agent.resumed` → `<ActorIcon /> chat resumed <role_slug>`
- `chat.agent.spawned` → `<ActorIcon /> chat spawned <role_slug> on #<ticket_id>`
- `chat.agent.config_edited` → `<ActorIcon /> chat edited <role_slug> config`
- `chat.hiring.proposed` → `<ActorIcon /> chat proposed hire: <role_title>`
- `chat.session_started` (M5.2 deferred) → `<SessionIcon /> chat session started`
- `chat.message_sent` (M5.2 deferred) → `<SessionIcon /> chat message sent`
- `chat.session_ended` (M5.2 deferred) → `<SessionIcon /> chat session ended`

**New stopgap page**: `dashboard/app/[locale]/(app)/hiring/proposals/page.tsx` — Server Component, better-auth gated via existing `(app)/layout.tsx`, reads via `getProposalsForCurrentUser` from new `dashboard/lib/queries/hiring.ts`.

**New queries file**: `dashboard/lib/queries/hiring.ts`:

```ts
export async function getProposalsForCurrentUser(): Promise<Proposal[]>
export async function getProposalById(id: string): Promise<Proposal | null>
```

**New actions file**: `dashboard/lib/actions/hiring.ts` — thin wrapper around the queries (no mutations in M5.3; M7 will add `approveProposal`, `rejectProposal`, etc.).

**Channel allowlist extension** in `dashboard/lib/sse/channels.ts`: 11 new channel literals added to `KNOWN_CHANNELS`. Test extended.

**ActivityEvent variants** in `dashboard/lib/sse/events.ts`: 11 new discriminated-union variants matching the channel set, with payload types carrying only IDs (Rule 6 backstop).

---

## §2. Type system

### 2.1 Verb registry

Per §1.1 above. Single Go slice. Sealed by CI-pinned test that compares the registered verb names to the enumerated set in `internal/garrisonmutate/verbs_test.go::TestVerbsRegistryMatchesEnumeration`.

### 2.2 Verb dispatch

JSON-RPC request → `tools/call` handler in `tool.go`:

1. Parse the `tools/call` request.
2. Look up by `params.name` in `Verbs` (linear search; 9 entries).
3. If not found: return JSON-RPC error `-32601` ("Method not found").
4. If found: invoke `verb.Handler(ctx, deps, params.arguments)`.
5. Wrap `Result` in MCP `tools/call` response envelope.

`tools/list` handler reads `Verbs` and emits the corresponding tool descriptors with JSON-Schema arg shapes (per-verb schemas defined in each handler's companion `*ArgsSchema()` helper function).

### 2.3 Error vocabulary

`internal/garrisonmutate/errors.go`:

```go
type MutateErrorKind string

const (
    ErrValidationFailed       MutateErrorKind = "validation_failed"
    ErrLeakScanFailed         MutateErrorKind = "leak_scan_failed"
    ErrTicketStateChanged     MutateErrorKind = "ticket_state_changed"
    ErrConcurrencyCapFull     MutateErrorKind = "concurrency_cap_full"
    ErrInvalidTransition      MutateErrorKind = "invalid_transition"
    ErrResourceNotFound       MutateErrorKind = "resource_not_found"
    ErrToolCallCeilingReached MutateErrorKind = "tool_call_ceiling_reached"
)

func (k MutateErrorKind) String() string { return string(k) }
```

`chat_mutation_audit.outcome` text values: `success` plus all the above. CHECK constraint on the column enforces the set.

`internal/chat/errorkind.go` (existing) gains `ErrToolCallCeilingReached` for the per-turn ceiling termination path. Mirrors existing `ErrSessionCostCapReached`.

### 2.4 SSE event union

Per §1.3. Discriminated union in TypeScript; matching Go-side struct types in `internal/chat/transport.go` for serialization.

### 2.5 Reversibility class

Plain `int` (1, 2, or 3) at the audit table layer. Stored in `chat_mutation_audit.reversibility_class`. Threat-model amendment carries the human-readable mapping. Rationale: avoids a Go enum type for a 3-value space the audit table doesn't query through application code (operator-side forensic queries use direct SQL).

---

## §3. Data model

### 3.1 Migration

`migrations/20260430000000_m5_3_chat_driven_mutations.sql`:

```sql
-- +goose Up

-- chat_mutation_audit: forensic record of every garrison-mutate verb call
CREATE TABLE chat_mutation_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_session_id UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    chat_message_id UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    verb TEXT NOT NULL CHECK (verb IN (
        'create_ticket', 'edit_ticket', 'transition_ticket',
        'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
        'propose_hire'
    )),
    args_jsonb JSONB NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN (
        'success', 'validation_failed', 'leak_scan_failed', 'ticket_state_changed',
        'concurrency_cap_full', 'invalid_transition', 'resource_not_found',
        'tool_call_ceiling_reached'
    )),
    reversibility_class SMALLINT NOT NULL CHECK (reversibility_class IN (1, 2, 3)),
    affected_resource_id TEXT,                              -- nullable: failures may not have one
    affected_resource_type TEXT CHECK (affected_resource_type IN ('ticket', 'agent_role', 'hiring_proposal')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_cma_session ON chat_mutation_audit (chat_session_id, created_at DESC);
CREATE INDEX idx_cma_resource ON chat_mutation_audit (affected_resource_type, affected_resource_id);

-- hiring_proposals: M5.3 ships table + read-only stopgap view; M7 extends
CREATE TABLE hiring_proposals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    role_title TEXT NOT NULL,
    department_slug TEXT NOT NULL REFERENCES departments(slug) ON DELETE RESTRICT,
    justification_md TEXT NOT NULL,
    skills_summary_md TEXT,
    proposed_via TEXT NOT NULL CHECK (proposed_via IN ('ceo_chat', 'dashboard', 'agent')),
    proposed_by_chat_session_id UUID REFERENCES chat_sessions(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'superseded')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_hp_status_dept ON hiring_proposals (status, department_slug, created_at DESC);
CREATE INDEX idx_hp_chat_session ON hiring_proposals (proposed_by_chat_session_id) WHERE proposed_by_chat_session_id IS NOT NULL;

-- tickets.created_via_chat_session_id: simplifies forensic queries
ALTER TABLE tickets
    ADD COLUMN created_via_chat_session_id UUID REFERENCES chat_sessions(id) ON DELETE SET NULL;
CREATE INDEX idx_tickets_chat_session ON tickets (created_via_chat_session_id) WHERE created_via_chat_session_id IS NOT NULL;

-- Grants
GRANT SELECT ON chat_mutation_audit TO garrison_dashboard_app;
GRANT SELECT, INSERT ON hiring_proposals TO garrison_dashboard_app;
-- Note: garrison_dashboard_app's existing tickets grants cover the new column.

-- +goose Down
DROP INDEX IF EXISTS idx_tickets_chat_session;
ALTER TABLE tickets DROP COLUMN IF EXISTS created_via_chat_session_id;
DROP INDEX IF EXISTS idx_hp_chat_session;
DROP INDEX IF EXISTS idx_hp_status_dept;
DROP TABLE IF EXISTS hiring_proposals;
DROP INDEX IF EXISTS idx_cma_resource;
DROP INDEX IF EXISTS idx_cma_session;
DROP TABLE IF EXISTS chat_mutation_audit;
```

ON DELETE behavior choices:
- `chat_mutation_audit.chat_session_id`: CASCADE — audit row dies with the session. Mirrors M5.2's chat_messages cascade.
- `chat_mutation_audit.chat_message_id`: CASCADE — audit row dies with the message.
- `hiring_proposals.department_slug`: RESTRICT — can't drop a department with pending proposals. M7 will refine.
- `hiring_proposals.proposed_by_chat_session_id`: SET NULL — proposal survives chat session deletion (forensic value: the proposal was made, even if the session is gone).
- `tickets.created_via_chat_session_id`: SET NULL — ticket survives chat session deletion. Existing tickets without chat origin keep NULL.

### 3.2 sqlc query files

`migrations/queries/garrisonmutate.sql`:

```sql
-- name: InsertChatMutationAudit :one
INSERT INTO chat_mutation_audit (
    chat_session_id, chat_message_id, verb, args_jsonb, outcome,
    reversibility_class, affected_resource_id, affected_resource_type
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at;

-- name: GetChatMutationAuditByID :one
SELECT * FROM chat_mutation_audit WHERE id = $1;

-- name: ListChatMutationAuditForSession :many
SELECT * FROM chat_mutation_audit
WHERE chat_session_id = $1
ORDER BY created_at DESC
LIMIT $2;
```

`migrations/queries/hiring.sql`:

```sql
-- name: InsertHiringProposal :one
INSERT INTO hiring_proposals (
    role_title, department_slug, justification_md, skills_summary_md,
    proposed_via, proposed_by_chat_session_id
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at, status;

-- name: GetHiringProposalByID :one
SELECT * FROM hiring_proposals WHERE id = $1;

-- name: ListHiringProposals :many
SELECT * FROM hiring_proposals
ORDER BY created_at DESC
LIMIT $1;

-- name: ListHiringProposalsByStatus :many
SELECT * FROM hiring_proposals
WHERE status = $1
ORDER BY created_at DESC
LIMIT $2;
```

Verb-side ticket/agent INSERTs/UPDATEs reuse existing query files (`migrations/queries/tickets.sql`, `migrations/queries/agents.sql`) extended with the new chat-origin-aware writes. Specifically:

- `tickets.sql`: extend `InsertTicket` to accept `created_via_chat_session_id` (nullable). Existing dashboard call sites pass NULL.
- `tickets.sql`: add `UpdateTicketFields :exec` for partial-update from `edit_ticket` if not already present (M4 may have shipped this; verify during implementation).
- `tickets.sql`: add `LockTicketForUpdate :one` using `SELECT ... FROM tickets WHERE id = $1 FOR UPDATE NOWAIT` for the concurrency lock.
- `agents.sql`: add `UpdateAgentPaused :exec`, `UpdateAgentConfig :exec` (or reuse existing).

### 3.3 Drizzle schema

Post-migration, run `bun run drizzle:pull` (or hand-edit byte-identical per M5.2 pattern). New types in `dashboard/drizzle/schema.supervisor.ts`:

```ts
export const chatMutationAudit = pgTable('chat_mutation_audit', { ... });
export const hiringProposals = pgTable('hiring_proposals', { ... });
// tickets: add created_via_chat_session_id field
```

---

## §4. Lifecycle management

### 4.1 `garrison-mutate` MCP server lifecycle

The server is invoked per-turn by Claude (via the chat container's MCP config). Lifecycle:

1. Claude spawns `supervisor mcp garrison-mutate` as a subprocess (per the chat container's mcp config).
2. The supervisor binary's `main.go` switch case for `garrison-mutate` calls `garrisonmutate.Server.Run(ctx, stdin, stdout)`.
3. The server reads JSON-RPC messages from stdin (`initialize`, `tools/list`, `tools/call`).
4. On `tools/call`: dispatch to `Verbs[i].Handler`; handler opens a Postgres tx, performs the work, commits, emits notify, returns `Result`.
5. Server writes the JSON-RPC response to stdout.
6. Claude reads the response, continues the turn.
7. On EOF (Claude exits): server returns from `Run`; subprocess exits.

This is exactly the `internal/finalize` shape. No daemon, no shared state across turns, no in-memory caching of mutation history (everything's in Postgres).

### 4.2 Per-turn tool-call ceiling enforcement

State machine (lives in `internal/chat/policy.go`):

```text
[turn-start]
  ToolCallCount = 0
  → on tool_use event:
      ToolCallCount++
      if ToolCallCount > GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN:
          → terminate chat container
          → write synthetic terminal row {error_kind: tool_call_ceiling_reached}
          → emit SSE typed-error frame
          → end turn
      else:
          → forward tool_use to SSE bridge (existing routing)
[turn-end] (on result event or termination)
  ToolCallCount = 0  (reset for next turn)
```

Read tools count too — feedback loops can be triggered by `postgres.query` results carrying injection-shaped text (spike §6 attack-class-3). The ceiling bounds total tool-call depth per turn regardless of read/write split.

### 4.3 Concurrent-mutation conflict resolution

Each verb that mutates `tickets` (or any other shared state) opens a transaction with the existing pgx transaction primitive, runs the lock query, then performs the write:

```text
BEGIN;
SELECT id FROM tickets WHERE id = $1 FOR UPDATE NOWAIT;
  -- on lock_not_available (SQLSTATE 55P03): return ErrTicketStateChanged
UPDATE tickets SET ... WHERE id = $1;
INSERT INTO chat_mutation_audit (...) VALUES (...);
COMMIT;
-- post-commit:
SELECT pg_notify('work.chat.ticket.<action>', $payload);
```

`SELECT … FOR UPDATE NOWAIT` returns immediately with PostgreSQL error 55P03 if another transaction holds the row lock. The verb maps this error to `ErrTicketStateChanged` (`error_kind='ticket_state_changed'`). The dashboard-side M4 mutation flow uses optimistic-update + rollback semantics; both sides race for the row lock; whichever acquires first commits, loser receives a typed error.

### 4.4 Atomic write shape per verb (success path)

```text
BEGIN;
  <data INSERT/UPDATE per-verb>
  INSERT INTO chat_mutation_audit (chat_session_id, chat_message_id, verb, args_jsonb, outcome='success', reversibility_class, affected_resource_id, affected_resource_type) VALUES (...);
COMMIT;
-- post-commit (with context.WithoutCancel + TerminalWriteGrace per Rule 6):
SELECT pg_notify('work.chat.<entity>.<action>', $payload);
```

### 4.5 Failure path

```text
BEGIN;
  <data INSERT/UPDATE attempt>
  -- failure detected (validation, leak-scan, lock_not_available, etc.)
ROLLBACK;
-- separate audit-only transaction:
BEGIN;
  INSERT INTO chat_mutation_audit (..., outcome='<error_kind>', affected_resource_id NULL or partial) VALUES (...);
COMMIT;
-- no pg_notify on failure (operator sees the failure chip; activity feed only surfaces successes).
```

Rationale for separate-transaction failure audit: the data-side ROLLBACK invalidates the audit-row INSERT inside the same tx, so we have to re-open. Best-effort failure-audit acceptable: if the second tx fails (DB unreachable), the chat surface still renders the failure chip from the `tool_result` event; the audit row is forensic redundancy that survives in 99%+ of cases. Mirrors how the existing `vault_access_log` handles audit-row best-effort vs spawn-fail behavior.

### 4.6 `propose_hire` lifecycle

Simpler than ticket/agent verbs — `hiring_proposals` is a fresh row, no concurrency conflict possible:

```text
BEGIN;
  INSERT INTO hiring_proposals (role_title, department_slug, justification_md, skills_summary_md, proposed_via='ceo_chat', proposed_by_chat_session_id) VALUES (...) RETURNING id, created_at, status;
  INSERT INTO chat_mutation_audit (..., affected_resource_id=<new hiring_proposal_id>, affected_resource_type='hiring_proposal') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.hiring.proposed', $payload);
```

### 4.7 `spawn_agent` lifecycle

Wraps the existing `internal/concurrency/cap.go` `prepareSpawn` shape:

```text
BEGIN;
  -- Per-department concurrency cap check (existing primitive).
  -- If cap full: ROLLBACK; return ErrConcurrencyCapFull.
  -- If cap available: INSERT agent_instances row; mark as running.
  INSERT INTO chat_mutation_audit (..., affected_resource_id=<agent_role_slug>, affected_resource_type='agent_role') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.agent.spawned', $payload);
-- The actual subprocess spawn is handled by the existing M2.x spawn loop;
-- garrison-mutate.spawn_agent only writes the agent_instances row.
```

**Important**: `spawn_agent` does NOT directly invoke `internal/spawn.Spawn`. It writes the `agent_instances` row + emits the `pg_notify`; the supervisor's existing event-bus listener picks up the notify and runs the M2.x spawn flow. This keeps the verb thin and the spawn lifecycle unified across chat-driven and dashboard-driven spawn paths.

### 4.8 `edit_agent_config` leak-scan

Per spec FR-421 (clarify-resolved):

```text
-- BEFORE the transaction opens:
leak_scan_result := scanForSecrets(args.system_prompt_md, args.mcp_config, ...)
if leak_scan_result.HasMatch:
    -- No agents row mutation. Audit row records the rejected diff with
    -- offending values redacted to [REDACTED].
    BEGIN;
      INSERT INTO chat_mutation_audit (..., args_jsonb=<diff with redactions>, outcome='leak_scan_failed') VALUES (...);
    COMMIT;
    return Result{Success: false, ErrorKind: ErrLeakScanFailed, Message: "..."}

-- Otherwise:
BEGIN;
  UPDATE agents SET ... WHERE role_slug = $1;
  INSERT INTO chat_mutation_audit (...) VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.agent.config_edited', ...);
```

Reuses M2.3's secret-pattern scanner from `internal/finalize/scanAndRedactPayload` (the 10-pattern set: sk-, xoxb-, AWS AKIA, PEM headers, GitHub PAT/App/User/Server/Refresh, bearer-shape).

---

## §5. SSE event union extension

### 5.1 Supervisor side

`internal/chat/transport.go` extension. The existing SSE writer sends `event: delta\n` and `event: terminal\n` frames. M5.3 adds:

```go
// EmitToolUse emits a tool_use SSE frame with the structured envelope.
func (w *Writer) EmitToolUse(messageID, toolUseID, toolName string, args json.RawMessage) error
// EmitToolResult emits a tool_result SSE frame with the structured envelope.
func (w *Writer) EmitToolResult(messageID, toolUseID string, isError bool, result json.RawMessage) error
```

Frame shape:

```text
event: tool_use
data: {"messageId":"<uuid>","toolUseId":"<uuid>","toolName":"garrison-mutate.create_ticket","args":{"title":"...","department":"..."}}

event: tool_result
data: {"messageId":"<uuid>","toolUseId":"<uuid>","isError":false,"result":{"success":true,"affected_resource_id":"<uuid>","affected_resource_url":"/tickets/<uuid>"}}
```

`Last-Event-ID` reconnect semantics: tool_use and tool_result events that have terminal-committed (their containing assistant message has reached `result` event) are recoverable from `chat_messages.raw_event_envelope` JSONB on reconnect — same M5.2 amended FR-261 row-state-read mechanism. Mid-flight tool calls (assistant message still streaming) are NOT replayed; consumer renders the partial accumulated state on reconnect and waits for terminal.

### 5.2 Dashboard side

`dashboard/lib/sse/chatStream.ts` extension. The existing `useChatStream(sessionId)` hook returns `{state, partialDeltas, terminals, sessionEnded, lastError}`. M5.3 extends:

```ts
type ToolCallEntry = {
  toolUseId: string;
  toolName: string;
  args: unknown;
  result?: { isError: boolean; result: unknown };
};

type UseChatStreamReturn = {
  state: ChatStreamState;
  partialDeltas: Map<string, string>;
  terminals: Map<string, Terminal>;
  toolCalls: Map<string, ToolCallEntry[]>;  // keyed by messageId, ordered by arrival
  sessionEnded: boolean;
  lastError: ChatErrorKind | null;
};
```

The hook's internal state machine (5-state per M5.2) is unchanged; only the event-routing extends. New event types route to a new internal `applyToolUse` / `applyToolResult` reducer:

- `tool_use`: append a new `ToolCallEntry` to `toolCalls.get(messageId)`.
- `tool_result`: find the entry by `toolUseId` and set its `result` field.

Dedupe on `(messageId, toolUseId)` keys to handle reconnect-replay (per the consumer's existing `(messageId, seq)` dedupe pattern from M5.2).

The `MessageStream.tsx` renderer reads `toolCalls.get(messageId)` for each in-flight assistant message and interleaves chips between text content blocks per the arrival order. The renderer must respect the ordering of text deltas vs tool_use events as they arrived in the SSE stream — both share the same `messageId` namespace.

### 5.3 Reconnect / replay

Per M5.2 amended FR-261: on reconnect, the SSE route reads `chat_messages.raw_event_envelope` JSONB for committed messages and emits any tool-use/tool-result events the consumer missed. M5.3 extends the supervisor's reconnect-replay logic to include tool events (currently it replays terminal events for committed assistant rows). The replay events are emitted as `tool_use` / `tool_result` SSE frames identical to the live frames; the consumer's dedupe handles deduplication.

---

## §6. Activity feed integration

### 6.1 Channel allowlist (`dashboard/lib/sse/channels.ts`)

Add 11 channels to `KNOWN_CHANNELS`:

```ts
export const KNOWN_CHANNELS = [
  // ... existing
  // M5.3 chat-driven mutations
  'work.chat.ticket.created',
  'work.chat.ticket.edited',
  'work.chat.ticket.transitioned',
  'work.chat.agent.paused',
  'work.chat.agent.resumed',
  'work.chat.agent.spawned',
  'work.chat.agent.config_edited',
  'work.chat.hiring.proposed',
  // M5.2 deferred chat lifecycle (carryover closure)
  'work.chat.session_started',
  'work.chat.message_sent',
  'work.chat.session_ended',
] as const;
```

Test in `channels.test.ts` extended to assert the new entries.

### 6.2 ActivityEvent variants (`dashboard/lib/sse/events.ts`)

11 new variants in the discriminated union:

```ts
export type ActivityEvent =
  // ... existing
  | { type: 'chat.ticket.created', chatSessionId: string, chatMessageId: string, ticketId: string, actorUserId: string }
  | { type: 'chat.ticket.edited', chatSessionId: string, chatMessageId: string, ticketId: string, actorUserId: string }
  | { type: 'chat.ticket.transitioned', chatSessionId: string, chatMessageId: string, ticketId: string, fromStatus: string, toStatus: string, actorUserId: string }
  | { type: 'chat.agent.paused', chatSessionId: string, chatMessageId: string, agentRoleSlug: string, actorUserId: string }
  | { type: 'chat.agent.resumed', chatSessionId: string, chatMessageId: string, agentRoleSlug: string, actorUserId: string }
  | { type: 'chat.agent.spawned', chatSessionId: string, chatMessageId: string, agentRoleSlug: string, ticketId: string | null, actorUserId: string }
  | { type: 'chat.agent.config_edited', chatSessionId: string, chatMessageId: string, agentRoleSlug: string, actorUserId: string }
  | { type: 'chat.hiring.proposed', chatSessionId: string, chatMessageId: string, hiringProposalId: string, actorUserId: string }
  | { type: 'chat.session_started', chatSessionId: string, actorUserId: string }
  | { type: 'chat.message_sent', chatSessionId: string, chatMessageId: string, role: 'operator' | 'assistant', actorUserId: string }
  | { type: 'chat.session_ended', chatSessionId: string, actorUserId: string };
```

Rule 6 backstop: every payload contains only IDs. No raw chat content text. The `transition_ticket` variant carries `fromStatus`/`toStatus` because they're enum values (not chat content) and the activity feed's render needs them.

### 6.3 EventRow branches

`dashboard/components/features/activity-feed/EventRow.tsx` extended with 11 new branches matching the variants. Each branch renders verb-specific copy with the M3 audit-event design language. Per spec FR-461 / FR-462 / FR-463:

```tsx
case 'chat.ticket.created':
  return <Row icon={<ChatIcon />}>
    <span>chat created ticket #{event.ticketId.slice(0, 8)}</span>
    <Link href={`/tickets/${event.ticketId}`}>view</Link>
  </Row>;
// ... etc per variant
```

### 6.4 pg_notify emitters (supervisor side)

Each verb's `notify.go` emit shape:

```go
func emitTicketCreatedNotify(ctx context.Context, db *pgx.Conn, sessionID, messageID, ticketID, actorUserID string) error {
    payload := struct {
        ChatSessionID string `json:"chatSessionId"`
        ChatMessageID string `json:"chatMessageId"`
        TicketID      string `json:"ticketId"`
        ActorUserID   string `json:"actorUserId"`
    }{sessionID, messageID, ticketID, actorUserID}
    return pgNotify(ctx, db, "work.chat.ticket.created", payload)
}
```

Pattern per verb. Payloads stay under the 8KB pg_notify limit by carrying only IDs. The audit table holds the full args.

---

## §7. Tool-call chip surface

### 7.1 Component composition

Single `ToolCallChip.tsx` file per D7. Internal branch logic per §1.3 pseudocode. Sub-components are local to the file.

Visual treatment (per spec FR-442 / FR-443 / FR-444):

- **Read chips** (low-emphasis): subdued tone (gray `text-muted-foreground`), small icon (search/eye glyph), single-line summary `<verb-progressive> <subject> · <result-summary>`. Examples:
  - `searched palace for "growth ticket" · 3 matches`
  - `queried tickets where dept=growth · 4 results`
- **Mutation chips** (higher-emphasis): saturated tone (text-foreground), verb-icon glyph, two-line layout (line 1: verb + arg summary; line 2: result + deep-link). Examples:
  - Pre-call: `creating ticket "fix kanban drag bug" in growth/backlog…`
  - Post-call: `created ticket #142 → /tickets/142`
- **Failure chips**: error palette from M5.2 (red foreground, alert icon), error_kind-specific copy. Example:
  - `failed to transition ticket #142 — ticket_state_changed`

Chips are informative-only (FR-445): no undo, cancel, retry, approve, reject affordances. Click target on mutation post-call chips opens the dashboard route for the affected resource (FR-446).

### 7.2 ARIA semantics

- Pre-call chips: `aria-live="polite"` + `aria-busy="true"` so screen readers announce "creating ticket" but don't interrupt the operator.
- Post-call chips: `aria-busy="false"` once the result lands.
- Failure chips: `role="alert"` + descriptive text including the error_kind.

Reuses the M5.2 accessibility posture (FR-330–334 carryover). Axe-core assertion in T020 Playwright test extends to chips.

### 7.3 Chip styling

Reuses M3 design-language primitives:
- `Chip` from `dashboard/components/ui/chip.tsx`
- `StatusDot` for the pre-call/post-call/failure state indicator
- `font-tabular` for IDs and result counts
- Reduced-motion handling inherited from M5.2 (`@media (prefers-reduced-motion: reduce)` → no animation on chip transitions)

### 7.4 MessageStream integration

`MessageStream.tsx` renders text content blocks and tool-call chips interleaved per arrival order:

```text
[in-flight assistant message bubble]
  text content block 1 (operator's question being answered)
  tool_use chip (pre-call): "querying tickets where dept=growth"
  tool_result chip (post-call): "queried 4 tickets"
  text content block 2 (assistant's response based on the query)
  tool_use chip (pre-call): "creating ticket 'fix kanban drag bug'"
  tool_result chip (post-call): "created #142 → /tickets/142"
  text content block 3 (assistant confirms the action)
[message ends with terminal event]
```

Ordering is preserved by the SSE stream's event arrival order; the renderer reads from `useChatStream`'s `partialDeltas.get(messageId)` (text) and `toolCalls.get(messageId)` (chips) and interleaves per the event log. Implementation detail: each tool_use entry carries an `arrivalOrdinal` index (incremented per SSE frame within the message); text deltas carry the same; renderer sorts by ordinal.

---

## §8. Stopgap hiring page

### 8.1 Route

`dashboard/app/[locale]/(app)/hiring/proposals/page.tsx` — Server Component, async, reads via `getProposalsForCurrentUser` from `dashboard/lib/queries/hiring.ts`.

Layout: matches the M3 dashboard surface conventions. Single page, no sub-routes.

### 8.2 Access control

Inherits the existing `(app)/layout.tsx` better-auth gate. Unauthenticated → redirect to `/login` per M3 conventions. Per FR-492.

### 8.3 Read query

`dashboard/lib/queries/hiring.ts`:

```ts
export async function getProposalsForCurrentUser(): Promise<Proposal[]> {
  const session = await getSession();
  if (!session) throw new Error('unauthenticated');
  // Single-operator single-tenant: returns all proposals visible to the operator.
  // M7 may extend with per-operator scoping if multi-operator lands.
  return db.select().from(hiringProposals).orderBy(desc(hiringProposals.createdAt)).limit(100);
}

export async function getProposalById(id: string): Promise<Proposal | null> {
  const session = await getSession();
  if (!session) throw new Error('unauthenticated');
  const [row] = await db.select().from(hiringProposals).where(eq(hiringProposals.id, id));
  return row ?? null;
}
```

### 8.4 UI

Single table view per FR-491:

| role_title | department | proposed_via | created_at | status |
|---|---|---|---|---|

No edit / approve / reject / spawn affordances per FR-493. Status displays as a chip ("pending" / "approved" / "rejected" / "superseded") matching M3 design language.

If proposal count is 0: render `EmptyState` from M3 (`dashboard/components/ui/empty-state.tsx`) with copy `"No hiring proposals yet. The CEO can propose hires through chat."`

### 8.5 Sidebar placement

Per D6: under "CEO chat" subnav in `Sidebar.tsx`, add a "Hiring proposals" sublink. Rationale: chat originates these proposals; the operator's mental model groups them with chat. Alternative top-level "Hiring" entry deferred to M7 when the full hiring flow ships.

---

## §9. Threat-model amendment outline

The threat-model amendment doc is implementation-phase output (T001), not plan-phase output. The plan supplies the binding outline; T001 writes the doc body.

**File**: `docs/security/chat-threat-model.md`

**Required sections** (per spec FR-401):

1. **Status** — landed before any verb code commits per FR-400.
2. **Scope of this document** — what the doc covers and doesn't cover.
3. **Milestone banding** — M5.3 ships the autonomous-execution posture; future verb additions or scope changes require amendment updates before implementation.
4. **Assets** — what chat mutations can affect: ticket state, agent state, hiring proposal state. Explicitly NOT vault, auth, user data.
5. **Adversaries** (ranked by realistic probability):
   - The operator, making mistakes (carryover from vault threat model framing)
   - Prompt injection via chat composer (operator-typed text the assistant misinterprets — spike §6 attack-class-2)
   - Prompt injection via palace contents (spike §6 attack-class-1)
   - Prompt injection via tool-result feedback loops (spike §6 attack-class-3)
   - Explicitly deprioritized: host compromise, multi-operator (post-M5), nation-state.
6. **Threats addressed** — each of the four threats with the M5.3 mitigation:
   - Sealed verb set (`Verbs` slice + CI-pinned test) — no dynamic tool discovery
   - Vault opacity (M2.3 Rule 3 carryover; CheckExtraServers test)
   - Audit row + `pg_notify` per call (Rule 6 backstop on payloads)
   - Per-session cost cap (M5.1 FR-061)
   - Per-turn tool-call ceiling (default 50, configurable)
   - Bounded chat container network (compose network isolation)
   - `--tools "" --strict-mcp-config` (spike §8.2 — built-in tools sealed off)
7. **Threats explicitly accepted** — what the autonomous posture accepts:
   - Successful prompt injection that triggers a registered verb with attacker-influenced args (mitigated by audit + activity feed observability, not blocked)
   - Operator-typed instructions that the assistant interprets differently than the operator intended (mitigated by chip surface visibility, not blocked)
   - Tool-result feedback loops up to the per-turn ceiling depth (mitigated by ceiling, not depth-zero blocked)
   - Single-operator single-CEO assumption breakage if multi-operator lands (mitigation: re-amend threat model before multi-operator)
   - Cost burn up to the per-session cap (mitigated by cap, not zero-cost)
8. **Architectural rules** (numbered, binding, with Consequence paragraphs):
   - **Rule N**: Chat mutation verbs are sealed at config time and CI-pinned. Consequence: adding a verb requires `verbs.go` + threat-model amendment + test update.
   - **Rule N+1**: Vault is opaque to chat (M2.3 Rule 3 carryover). Consequence: `BuildChatConfig` rejects vault-named MCP server entries; `garrison-mutate` verb registry contains zero `vault_*` verb names.
   - **Rule N+2**: Every chat-driven mutation writes a `chat_mutation_audit` row in the same transaction as the data write; emits a `pg_notify` post-commit. Consequence: forensic reconstruction of every successful and failed verb call is queryable from a single table.
   - **Rule N+3**: Tool-result feedback loops bounded by per-session cost cap (M5.1 FR-061) plus per-turn tool-call ceiling (default 50). Consequence: runaway loops self-terminate within bounded depth.
   - **Rule N+4**: Chat container has no outbound network beyond supervisor + docker-proxy. Consequence: chat-side data exfiltration via network is bounded by supervisor's egress posture.
   - **Rule N+5**: Mutation reversibility is classified per verb (Tier 1 / 2 / 3); audit row records the class. Consequence: future undo / replay tooling has the metadata it needs without runtime re-derivation.
9. **Per-verb reversibility tier table** (M5.3 verbs):
   - Tier 1 (Reversible): `transition_ticket`, `pause_agent`, `resume_agent`
   - Tier 2 (Semi-irreversible / diff-in-audit): `edit_ticket`, `edit_agent_config`
   - Tier 3 (Effectively-irreversible / pre-state snapshot in audit): `create_ticket`, `spawn_agent`, `propose_hire`
10. **Per-attack-class mitigation summary** — the table mapping spike §6 AC-1/2/3 to specific runtime mitigations (Rule N through N+5 references).
11. **Open questions for the M5.3 retro** (mirroring vault threat model §7):
    - Did Rule N hold? (Any verb call that bypassed the sealed allow-list? Any test that should have caught a leak that didn't?)
    - Did the autonomous-execution posture feel right operationally? (Operator-week-of-use feedback)
    - Were any of the threats-accepted realized in practice? (Successful injection? Cost-cap fire?)
    - Did the chip surface adequately surface what the chat was doing? (Operator's ability to spot problematic mutations)
    - Per-verb reversibility classification: were any classifications wrong in retrospect? (Tier 1 verbs that turned out to be hard to reverse, etc.)
12. **What this milestone's retro must answer** — same shape as vault threat model §7.

The amendment doc's body will fall in the 1500-3500 word range matching the vault threat model precedent.

---

## §10. Architecture amendment

Per spec FR-500 and FR-501. Two substring pins:

**Line :574 area** — replace the existing M5 line with:

```text
**M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)**: ships `garrison-mutate` MCP server with sealed 8-verb set across tickets/agents/hiring; threat-model amendment lands first per M2.3 vault-threat-model-first pattern; tool-call chips surface every assistant tool call informatively (no per-call gates); concurrency conflicts resolve via `SELECT ... FOR UPDATE NOWAIT` mapped to `error_kind='ticket_state_changed'`; vault remains opaque to chat (M2.3 Rule 3 carryover).
```

The substring asserted by the test: `"M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)"`

**Line :103 area** — extend the existing diagram:

```text
Agent      ──► finalize_ticket MCP          ✓  single tool, transactional, only commit path
Chat       ──► garrison-mutate MCP          ✓  sealed verb set, transactional per call, autonomous-execution posture
```

The substring asserted by the test: `"Chat ──► garrison-mutate MCP"`

Test in `dashboard/tests/architecture-amendment.test.ts` extended:

```ts
describe('M5.3 architecture amendment', () => {
  it('contains the autonomous-execution posture substring', async () => {
    const content = await fs.readFile('ARCHITECTURE.md', 'utf-8');
    expect(content).toContain('M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)');
  });

  it('contains the chat-mutate diagram substring', async () => {
    const content = await fs.readFile('ARCHITECTURE.md', 'utf-8');
    expect(content).toContain('Chat ──► garrison-mutate MCP');
  });
});
```

Test failure blocks merge per FR-501.

---

## §11. Mockclaude fixtures

Per D23. Fixture extension shape: env-var-driven selection. `GARRISON_MOCKCLAUDE_FIXTURE=m5_3_create_ticket_happy` selects scripted streams.

**Fixture script files** under `supervisor/mockclaude/fixtures/`:

1. **`m5_3_create_ticket_happy.ndjson`** — operator message → assistant streams text → assistant emits `tool_use` for `garrison-mutate.create_ticket` → mockclaude waits for `tool_result` (delivered by the actual `garrison-mutate` server in integration tests) → assistant streams confirmation text → terminal `result` event.
2. **`m5_3_transition_ticket_happy.ndjson`** — same shape with `transition_ticket`.
3. **`m5_3_edit_agent_config_leak_fail.ndjson`** — assistant emits `tool_use` for `edit_agent_config` with a planted `sk-`-shaped secret in the proposed `system_prompt_md`; mockclaude expects `tool_result` carrying `error_kind='leak_scan_failed'`; assistant streams an apology text; terminal.
4. **`m5_3_propose_hire_happy.ndjson`** — `tool_use` for `propose_hire` → result with `affected_resource_id` of the new hiring proposal → terminal.
5. **`m5_3_compound_two_verbs.ndjson`** — single turn emits two `tool_use` events (transition_ticket + pause_agent); assistant text after each. Tests per-verb atomicity.
6. **`m5_3_chaos_ac1_palace_inject.ndjson`** — assistant calls `mempalace.search`, receives a planted malicious payload in the result, attempts to call `garrison-mutate.create_ticket` with arg text influenced by the injection. Tests that the verb fires (per the threat-model-defined posture) and the audit row records the chain.
7. **`m5_3_chaos_ac2_composer_inject.ndjson`** — operator message contains a prompt injection (e.g., a customer email pasted into chat); assistant interprets the injection as an instruction. Same posture as AC-1 (verb fires; audit captures the chain).
8. **`m5_3_chaos_ac3_feedback_loop.ndjson`** — assistant calls `garrison-mutate.create_ticket`; the result text is shaped to look like a tool-call instruction; assistant attempts to call `create_ticket` again; loops until per-turn ceiling fires.
9. **`m5_3_chaos_ceiling_breach.ndjson`** — minimal fixture that emits 51 sequential `tool_use` events; ceiling fires at 50; supervisor terminates the container; chaos test asserts the synthetic terminal row + SSE typed-error frame.

Fixture file format: NDJSON matching the existing M5.1/M5.2 mockclaude fixtures (one JSON event per line, `system/init` first, then `assistant` / `user` / `result` events in order).

The mockclaude image's fixture-selection logic: read `GARRISON_MOCKCLAUDE_FIXTURE` env var at startup; load the corresponding NDJSON file from `/fixtures/` inside the container; replay events to stdout per the existing M5.1 mockclaude shape.

---

## §12. Error vocabulary (consolidated)

`MutateErrorKind` values (in `internal/garrisonmutate/errors.go`):

| Constant | String | Used by | Description |
|---|---|---|---|
| `ErrValidationFailed` | `validation_failed` | all verbs | Input args don't match schema (missing required, wrong type, etc.) |
| `ErrLeakScanFailed` | `leak_scan_failed` | `edit_agent_config` | Proposed config contains a secret-shaped value |
| `ErrTicketStateChanged` | `ticket_state_changed` | `edit_ticket`, `transition_ticket` | Concurrent mutation acquired the row lock first |
| `ErrConcurrencyCapFull` | `concurrency_cap_full` | `spawn_agent` | Per-department cap doesn't allow another spawn |
| `ErrInvalidTransition` | `invalid_transition` | `transition_ticket` | Target column not valid for ticket's department |
| `ErrResourceNotFound` | `resource_not_found` | all verbs that operate on existing resources | Target resource doesn't exist |
| `ErrToolCallCeilingReached` | `tool_call_ceiling_reached` | not a verb error — chat policy emits this | Per-turn tool-call ceiling exceeded |

Chat-side `ChatErrorKind` (existing in `internal/chat/errorkind.go`) gains `ErrToolCallCeilingReached` for the synthetic terminal row written when the ceiling fires.

`chat_mutation_audit.outcome` CHECK constraint enumerates `success` plus all of the above except `ErrToolCallCeilingReached` (which doesn't generate audit rows — the chat-side terminal row carries it).

---

## §13. Verb-by-verb specifications

### 13.1 `create_ticket`

**Input args** (JSON):
```json
{
  "title": "string, 1..200 chars",
  "description": "string, 0..10000 chars",
  "department_slug": "string, must exist in departments table",
  "priority": "string?, one of 'low'/'medium'/'high'/'urgent', default 'medium'",
  "labels": "string[]?, default []",
  "parent_ticket_id": "uuid?, must exist in tickets table if provided"
}
```

**Validation**: title length, description length, department exists, priority enum, parent_ticket_id exists (if provided).

**Lock acquisition**: none — fresh row creation.

**Atomic write**:
```sql
BEGIN;
INSERT INTO tickets (title, description, department_id, status, priority, labels, origin, created_via_chat_session_id, parent_ticket_id) VALUES (...);
INSERT INTO chat_mutation_audit (..., affected_resource_id=<new_ticket_id>, affected_resource_type='ticket') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.ticket.created', $payload);
```

**Result on success**:
```json
{ "success": true, "affected_resource_id": "<uuid>", "affected_resource_url": "/tickets/<uuid>" }
```

**Reversibility tier**: 3 (effectively-irreversible — deletable but with side effects).

**Audit `args_jsonb`**: full input args including title and description (operator-typed text passed via the chat).

### 13.2 `edit_ticket`

**Input args**:
```json
{
  "ticket_id": "uuid, must exist",
  "title": "string?",
  "description": "string?",
  "priority": "string?",
  "labels": "string[]?"
}
```

At least one of `title`/`description`/`priority`/`labels` MUST be present.

**Validation**: ticket exists, at-least-one-field, type-check.

**Lock acquisition**: `SELECT ... FROM tickets WHERE id = $1 FOR UPDATE NOWAIT`. On lock failure: return `ErrTicketStateChanged`.

**Atomic write**:
```sql
BEGIN;
SELECT id FROM tickets WHERE id = $1 FOR UPDATE NOWAIT;  -- or fail
UPDATE tickets SET title = COALESCE($2, title), description = COALESCE($3, description), ... WHERE id = $1;
INSERT INTO chat_mutation_audit (..., args_jsonb=<diff before/after>, affected_resource_id=$1, affected_resource_type='ticket') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.ticket.edited', $payload);
```

**Audit `args_jsonb`**: the diff — `{before: {...}, after: {...}}` for each changed field. Per spec FR-417 Tier 2.

**Reversibility tier**: 2.

### 13.3 `transition_ticket`

**Input args**:
```json
{
  "ticket_id": "uuid",
  "to_status": "string, must be a valid column on the ticket's department",
  "reason": "string?, free-form note"
}
```

**Validation**: ticket exists, to_status valid for ticket's department (per `departments.columns` config). Invalid to_status → `ErrInvalidTransition`.

**Lock acquisition**: same NOWAIT pattern. On failure: `ErrTicketStateChanged`.

**Atomic write**: hooks into the existing `ticket_transitions` event-bus per spec FR-418. Uses the existing M2.x `ticket_transitions` row + trigger that emits `work.ticket.transitioned.<dept>.<from>.<to>` (operator-and-agent-driven path) AND emits `work.chat.ticket.transitioned` (chat-driven path) for the activity feed:

```sql
BEGIN;
SELECT id FROM tickets WHERE id = $1 FOR UPDATE NOWAIT;
INSERT INTO ticket_transitions (ticket_id, from_status, to_status, reason, actor_kind, actor_chat_session_id) VALUES (...);
UPDATE tickets SET status = $to_status WHERE id = $1;  -- existing trigger fires work.ticket.transitioned.*
INSERT INTO chat_mutation_audit (..., args_jsonb=<{from, to, reason}>, affected_resource_id=$1, affected_resource_type='ticket') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.ticket.transitioned', $payload);  -- additional chat-namespaced notify for chat-driven audit
```

Note: `ticket_transitions.actor_kind` may need extending if M2.x didn't anticipate a `'chat_session'` actor value. Verify during implementation; if needed, add the value via the migration's CHECK constraint amendment OR add a new `actor_chat_session_id` nullable column. Defer the choice to implementation; both work.

**Reversibility tier**: 1 (move back).

### 13.4 `pause_agent`

**Input args**:
```json
{
  "agent_role_slug": "string, must exist",
  "reason": "string?, free-form note"
}
```

**Validation**: agent exists.

**Idempotency**: if already paused, no-op (return success); audit row records the no-op.

**Atomic write**:
```sql
BEGIN;
UPDATE agents SET is_paused = true WHERE role_slug = $1;
INSERT INTO chat_mutation_audit (..., affected_resource_id=$1, affected_resource_type='agent_role') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.agent.paused', $payload);
```

**Reversibility tier**: 1 (paired with `resume_agent`).

### 13.5 `resume_agent`

Symmetric to `pause_agent`. Sets `is_paused = false`. Idempotent. Reversibility 1.

### 13.6 `spawn_agent`

**Input args**:
```json
{
  "agent_role_slug": "string, must exist",
  "ticket_id": "uuid?, optional — if not provided, picks next eligible ticket from the role's queue"
}
```

**Validation**: agent exists, ticket exists (if provided).

**Concurrency cap check**: invoke existing `internal/concurrency/cap.go` `prepareSpawn` shape. If cap full → `ErrConcurrencyCapFull`.

**Atomic write**: see §4.7. Writes `agent_instances` row; the existing supervisor spawn-loop listener picks up the notify and runs the M2.x spawn flow.

**Reversibility tier**: 3 (the agent runs, costs money, may write palace).

### 13.7 `edit_agent_config`

**Input args**:
```json
{
  "agent_role_slug": "string, must exist",
  "model": "string?",
  "system_prompt_md": "string?",
  "mcp_config": "object?",
  "concurrency_cap": "integer?"
}
```

**Pre-tx leak scan**: per §4.8. Scans `system_prompt_md` and stringified `mcp_config` against the M2.3 secret-pattern set. On match: separate-tx audit row with redacted diff; return `ErrLeakScanFailed`.

**Atomic write**:
```sql
BEGIN;
UPDATE agents SET model = COALESCE($2, model), system_prompt_md = COALESCE($3, system_prompt_md), ... WHERE role_slug = $1;
INSERT INTO chat_mutation_audit (..., args_jsonb=<diff>, affected_resource_id=$1, affected_resource_type='agent_role') VALUES (...);
COMMIT;
SELECT pg_notify('work.chat.agent.config_edited', $payload);
```

**Reversibility tier**: 2 (diff in audit).

### 13.8 `propose_hire`

**Input args**:
```json
{
  "role_title": "string, 1..100 chars",
  "department_slug": "string, must exist",
  "justification_md": "string, 1..10000 chars",
  "skills_summary_md": "string?, 0..10000 chars"
}
```

**Validation**: role_title length, department exists, justification length.

**Atomic write**: see §4.6. Writes `hiring_proposals` row.

**Result on success**:
```json
{
  "success": true,
  "affected_resource_id": "<uuid>",
  "affected_resource_url": "/hiring/proposals/<uuid>"
}
```

**Reversibility tier**: 3 (M7 status changes don't undo the proposal-row creation).

---

## §14. Test strategy

Test files + function-level enumeration. Every test file is named `<feature>_test.go` (Go) or `<feature>.test.ts` / `<feature>.test.tsx` (dashboard).

### 14.1 Supervisor unit tests

**`supervisor/internal/garrisonmutate/server_test.go`**:
- `TestServerInitializeReturnsCorrectInfo` — JSON-RPC `initialize` returns the expected MCP info envelope.
- `TestServerToolsListReturnsAllVerbs` — `tools/list` returns descriptors for every entry in `Verbs`, no more no less.
- `TestServerToolsCallRejectsUnknownVerb` — `tools/call` with an unregistered name returns JSON-RPC error -32601.
- `TestServerToolsCallDispatchesToHandler` — `tools/call` with a registered name routes to the verb's `Handler`.

**`supervisor/internal/garrisonmutate/verbs_test.go`**:
- `TestVerbsRegistryMatchesEnumeration` — the `Verbs` slice contains exactly the expected 9 verb names. Sealed-allow-list test. Adding a verb without updating this test fails CI.
- `TestVerbsRegistryHasNoVaultEntries` — defense-in-depth: assert no verb name starts with `vault_` or contains `vault`.
- `TestVerbsRegistryReversibilityClassesValid` — every entry has a class in {1, 2, 3}.

**`supervisor/internal/garrisonmutate/verbs_tickets_test.go`**:
- `TestCreateTicketHappyPath` — given valid args + a clean DB, the verb writes the ticket row + audit row in one tx.
- `TestCreateTicketRejectsMissingTitle` — validation error for empty title.
- `TestCreateTicketRejectsUnknownDepartment` — validation error for non-existent department_slug.
- `TestCreateTicketCommitsAuditAtomically` — uses a tx-rollback fixture to assert audit row + ticket row commit together.
- `TestEditTicketHappyPath` — given valid args, updates fields, captures diff in audit.
- `TestEditTicketReturnsTicketStateChangedOnLockConflict` — concurrent tx holds the lock; verb returns `ErrTicketStateChanged`.
- `TestTransitionTicketHooksIntoEventBus` — asserts the `ticket_transitions` row + the dual notify (work.ticket.transitioned.* + work.chat.ticket.transitioned) both fire.
- `TestTransitionTicketRejectsInvalidTargetColumn` — to_status not in ticket's department's columns → `ErrInvalidTransition`.

**`supervisor/internal/garrisonmutate/verbs_agents_test.go`**:
- `TestPauseAgentIdempotent` — calling pause twice returns success both times; second call's audit row records the no-op.
- `TestResumeAgentIdempotent` — symmetric.
- `TestSpawnAgentRespectsConcurrencyCap` — cap full → `ErrConcurrencyCapFull`; cap available → row writes.
- `TestEditAgentConfigRejectsLeakScanFailure` — planted `sk-`-shaped sentinel in `system_prompt_md` triggers `ErrLeakScanFailed`; no `agents` row mutation; audit row records the rejected diff with values redacted.
- `TestEditAgentConfigPassesCleanScan` — clean config commits successfully.

**`supervisor/internal/garrisonmutate/verbs_hiring_test.go`**:
- `TestProposeHireHappyPath` — valid args writes a `hiring_proposals` row with `proposed_via='ceo_chat'`.
- `TestProposeHireRejectsUnknownDepartment` — validation error.
- `TestProposeHireDuplicatesAllowed` — same role_title + department twice → both rows land (M7 dedupes).

**`supervisor/internal/garrisonmutate/audit_test.go`**:
- `TestInsertAuditCapturesArgsJsonbVerbatim` — `args_jsonb` round-trips unchanged.
- `TestInsertAuditEnforcesOutcomeCheckConstraint` — invalid outcome value rejected at DB level.
- `TestInsertAuditEnforcesReversibilityClassRange` — class 0 or 4 rejected.
- `TestInsertAuditCascadesOnSessionDelete` — deleting the chat_sessions row cascades to chat_mutation_audit.

**`supervisor/internal/garrisonmutate/notify_test.go`**:
- `TestEmitTicketCreatedNotifyPayloadShape` — payload contains expected ID fields and no chat content.
- `TestEmitTicketCreatedNotifyChannelName` — channel is exactly `work.chat.ticket.created`.
- `TestNotifyPayloadStaysUnder8KB` — payload fits the pg_notify limit.

**`supervisor/internal/garrisonmutate/validation_test.go`**:
- `TestValidateCreateTicketArgs` — table-driven tests covering missing fields, type mismatches, length boundaries.
- (analogous per verb)

**`supervisor/internal/garrisonmutate/errors_test.go`**:
- `TestMutateErrorKindCoversAllOutcomes` — every `outcome` enum value in the migration's CHECK constraint maps to a `MutateErrorKind` (or to `success`).

### 14.2 Supervisor integration tests

**`supervisor/internal/garrisonmutate/integration_test.go`** (testcontainer Postgres + scripted mockclaude):
- `TestEndToEndCreateTicketFromMockClaude` — full path: mockclaude emits scripted stream → supervisor routes to garrison-mutate → row lands + audit row lands + pg_notify fires.
- `TestEndToEndCompoundTwoVerbsPerTurn` — single turn issues two tool_use events; both verbs commit.
- `TestEndToEndPartialFailureContinuesTurn` — second verb fails (e.g., lock conflict); first verb's effects persist; failure surfaces as `tool_result.is_error=true`.

### 14.3 Supervisor chaos tests

**`supervisor/internal/garrisonmutate/chaos_test.go`**:
- `TestPalaceInjectionAttackClass1` — plant a malicious palace entry; trigger a chat turn that retrieves it; assert the threat-model-defined outcome (verb fires; audit captures full args including the injection text).
- `TestComposerInjectionAttackClass2` — operator message contains a prompt injection; same posture.
- `TestToolResultFeedbackLoopAttackClass3` — feedback-loop fixture; per-turn ceiling fires before unbounded rows land.
- `TestCostCapTerminatesSession` — runaway loop hits per-session cost cap; `error_kind='session_cost_cap_reached'` in synthetic terminal; bounded mutation rows.
- `TestToolCallCeilingTerminatesContainer` — 51st `tool_use` event triggers ceiling fire; `error_kind='tool_call_ceiling_reached'`; container terminated.

### 14.4 mcpconfig tests

**`supervisor/internal/mcpconfig/mcpconfig_test.go`** (extending existing):
- `TestBuildChatConfigSealsThreeEntries` — output has exactly `{postgres, mempalace, garrison-mutate}`.
- `TestBuildChatConfigRejectsVaultEntries` — passing a vault-named entry returns `ErrVaultEntryRejected`.
- `TestBuildChatConfigRejectsFourthEntry` — passing a fourth entry of any name returns the rejection error.
- `TestCheckExtraServersExtendsToVault` — the existing M2.3 Rule 3 check now also rejects vault names from chat-bound configs.

### 14.5 Chat policy tests

**`supervisor/internal/chat/policy_test.go`** (extending existing):
- `TestToolCallCeilingTerminatesContainer` — synthesized 51 tool_use events; assert termination + synthetic terminal row.
- `TestToolUseEventForwardedToSSE` — tool_use event arrives, SSE bridge receives it.
- `TestToolResultEventForwardedToSSE` — tool_result event arrives, SSE bridge receives it.
- `TestPerTurnCounterResetsBetweenTurns` — turn 1 counts to 30, turn 2 starts at 0.

### 14.6 Dashboard unit tests

**`dashboard/lib/sse/chatStream.test.ts`** (extending existing):
- `useChatStream surfaces tool_use variants` — tool_use SSE frame → toolCalls map populated.
- `useChatStream surfaces tool_result variants` — tool_result SSE frame → matching entry's `result` field populated.
- `useChatStream dedupes tool_use by toolUseId on reconnect` — replayed events don't double-render.
- `useChatStream preserves arrival order across deltas and tool calls within a message` — interleaved events maintain order.

**`dashboard/lib/sse/channels.test.ts`** (extending existing):
- `KNOWN_CHANNELS contains all M5.3 chat-mutation channels` — assertion of the 8 + 3 new entries.

**`dashboard/components/features/ceo-chat/ToolCallChip.test.tsx`** (NEW, renderToString static pin):
- `ToolCallChip renders ReadChipPreCall for postgres.query tool_use without result` — snapshot.
- `ToolCallChip renders ReadChipPostCall for postgres.query with result` — snapshot.
- `ToolCallChip renders MutateChipPreCall for garrison-mutate.create_ticket without result` — snapshot.
- `ToolCallChip renders MutateChipPostCall for garrison-mutate.create_ticket with result` — snapshot includes the deep-link.
- `ToolCallChip renders FailureChip for tool_result.isError=true` — snapshot includes error_kind.
- `ToolCallChip mutation chips have aria-busy on pre-call` — accessibility pin.
- `ToolCallChip failure chips have role=alert` — accessibility pin.

**`dashboard/lib/queries/hiring.test.ts`** (NEW):
- `getProposalsForCurrentUser returns rows ordered by created_at desc` — testcontainer Postgres seed.
- `getProposalsForCurrentUser rejects unauthenticated calls` — auth gate.
- `getProposalById returns null for missing id` — non-existent UUID → null.

### 14.7 Dashboard integration tests

**`dashboard/tests/integration/m5-3-chat-mutations.spec.ts`** (NEW Playwright):
- `create_ticket golden path: operator instruction → chip → ticket exists → activity feed event` — round-trip under SC-300's 10s budget.
- `transition_ticket round-trip with chip transitions and activity-feed event` — pre-call chip → post-call chip → /tickets/<id> reflects new status → activity feed shows the transition.
- `propose_hire round-trip including stopgap page render` — chat instruction → chip → /hiring/proposals shows the new row.
- `compound two-verb single-turn instruction renders both chips` — operator says "do X and Y"; both chips render in the assistant bubble.
- `failure chip renders with error_kind` — fixture forces a leak-scan failure; assert chip copy + ARIA.
- `mid-stream disconnect then reconnect renders no duplicate chips` — driven via Playwright's network manipulation.
- `chat-mutation chips do not carry undo/cancel/approve affordances` — DOM assertion: no buttons on chips beyond the deep-link.

### 14.8 Architecture amendment test

**`dashboard/tests/architecture-amendment.test.ts`** (extending existing M5.2 file):
- `M5.3 amendment: line 574 substring present` — `expect(content).toContain('M5.3 — chat-driven mutations under autonomous-execution posture (no per-call operator approval)')`.
- `M5.3 amendment: line 103 substring present` — `expect(content).toContain('Chat ──► garrison-mutate MCP')`.

### 14.9 Regression coverage

The full Playwright suite (M3 + M4 + M5.1 + M5.2 + M5.3) MUST stay under 12 minutes total per SC-319. Existing M5.1/M5.2 tests pass unchanged. Existing M2.x supervisor integration tests pass unchanged.

Specifically: `supervisor/internal/chat/integration_*_test.go` (M5.1 multi-turn, idle, vault-helper) — all pass without modification because M5.3's tool-call ceiling counter is initialized to 0 per turn and the existing fixtures don't approach the 50-call ceiling.

---

## §15. Implementation ordering

Per AGENTS.md scope discipline + the threat-model-first ordering bound by FR-400:

**Wave 1 — threat-model amendment**:
1. T001: write `docs/security/chat-threat-model.md` per §9 outline.

**Wave 2 — schema + sealed-allow-list**:
2. T002: migration `20260430000000_m5_3_chat_driven_mutations.sql` (§3.1) + sqlc query files (§3.2) + drizzle pull.
3. T003: extend `BuildChatConfig` + `CheckExtraServers` (§1.2). Sealed-allow-list test passes BEFORE any verb code lands. (FR-433 binding ordering.)

**Wave 3 — garrison-mutate scaffolding**:
4. T004: new package `internal/garrisonmutate/` skeleton — `server.go`, `tool.go`, `verbs.go` (with empty Handler stubs that return ErrValidationFailed), `audit.go`, `notify.go`, `validation.go`, `errors.go`. CI-pinned `TestVerbsRegistryMatchesEnumeration` passes (against the empty stubs).
5. T005: `cmd/supervisor/main.go` subcommand wiring.

**Wave 4 — verb implementations** (per-verb, parallelizable):
6. T006: `verbs_tickets.go::handleCreateTicket` + tests.
7. T007: `verbs_tickets.go::handleEditTicket` + tests.
8. T008: `verbs_tickets.go::handleTransitionTicket` + tests.
9. T009: `verbs_agents.go::handlePauseAgent` + `handleResumeAgent` + tests.
10. T010: `verbs_agents.go::handleSpawnAgent` + tests.
11. T011: `verbs_agents.go::handleEditAgentConfig` + leak-scan + tests.
12. T012: `verbs_hiring.go::handleProposeHire` + tests.

**Wave 5 — chat policy extension**:
13. T013: extend `internal/chat/policy.go` with per-turn tool-call ceiling enforcement + OnStreamEvent forwarding for tool_use/tool_result.
14. T014: extend `internal/chat/transport.go` with new SSE frame types.

**Wave 6 — dashboard chip surface**:
15. T015: extend `dashboard/lib/sse/chatStream.ts` with tool_use/tool_result variants.
16. T016: new `dashboard/components/features/ceo-chat/ToolCallChip.tsx` + tests.
17. T017: extend `MessageStream.tsx` + `MessageBubble.tsx` to render chips.

**Wave 7 — activity feed integration**:
18. T018: extend `dashboard/lib/sse/channels.ts` allowlist + `events.ts` variants.
19. T019: extend `EventRow.tsx` with 11 new branches (8 mutation + 3 deferred lifecycle).

**Wave 8 — stopgap hiring page**:
20. T020: new `dashboard/lib/queries/hiring.ts` + `lib/actions/hiring.ts`.
21. T021: new `app/[locale]/(app)/hiring/proposals/page.tsx` + Sidebar entry.

**Wave 9 — architecture amendment**:
22. T022: amend `ARCHITECTURE.md` lines 574 + 103 per §10.
23. T023: extend `dashboard/tests/architecture-amendment.test.ts` with M5.3 substring assertions.

**Wave 10 — mockclaude fixtures + chaos**:
24. T024: extend `garrison-mockclaude:m5` with per-verb fixture flags + 9 new fixture scripts (§11).
25. T025: chaos tests — `internal/garrisonmutate/chaos_test.go` per §14.3.

**Wave 11 — Playwright integration**:
26. T026: `dashboard/tests/integration/_chat-harness.ts` extended for garrison-mutate fixture support.
27. T027: `dashboard/tests/integration/m5-3-chat-mutations.spec.ts` per §14.7.

**Wave 12 — retro**:
28. T028: write `docs/retros/m5-3.md` + MemPalace mirror per AGENTS.md §Retros.

The strict ordering dependency: T001 (threat-model amendment) lands BEFORE T004 (verb scaffolding) which lands BEFORE T006-T012 (verb implementations). Other waves have softer dependencies (e.g., dashboard chip surface needs T013-T014 supervisor SSE extension to be testable end-to-end, but the chip component itself can be drafted against fixture data).

---

## §16. Forward-compat hooks

One-line each:

- **`hiring_proposals` columns**: append-only forward-compat per spec FR-480; M7 ADDs review/approve/spawn columns, never renames or removes.
- **`chat_mutation_audit.reversibility_class` column**: exists for future undo-feature work; M5.3 ships the column but does not consume it (no undo verbs).
- **`tickets.created_via_chat_session_id` FK column**: simplifies forensic queries; ON DELETE SET NULL means chat session deletion doesn't cascade-destroy tickets.
- **Verb registry shape**: adding a 10th verb requires a `verbs.go` edit + a threat-model amendment update + a test update; no plugin shape, no runtime registration. Future verb additions are explicitly architectural, not config.
- **`mempalace.write` and `mempalace.add_drawer` verbs**: explicitly NOT in M5.3. If M5.4+ wants chat-driven palace writes, requires threat-model amendment update.
- **Per-verb cost-multiplier**: spec resolved as out-of-scope (Open Q 6 → no). If post-M5 dogfooding shows specific verbs are unusually expensive, future cost-cap shape can layer multipliers.
- **Multi-operator extension**: threat-model amendment's autonomous-execution justification depends on single-operator. Multi-operator requires re-amendment (called out in the amendment's "Threats accepted" section).
- **Per-turn tool-call ceiling configurability**: env var (`GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN`) lets the operator tune at deploy time without code change. Default 50.

---

## §17. Open work / Phase 5 deferred items

These items remain open at plan-completion time and need resolution before or during implementation:

**Q1**: `ticket_transitions.actor_kind` enum — does M2.x's existing CHECK constraint already accept a `'chat_session'` (or `'ceo_chat'`) value? If not, the M5.3 migration adds it. **Resolution path**: implementation reads the existing constraint; if extension needed, adds to migration.

**Q2**: Failure-chip text + link target — spec deferred this (clarify-deferred items list). Plan default lean: failure chip carries the verbatim `error_kind` text (`failed to transition ticket #142 — ticket_state_changed`), no deep-link (failure chips stay informative-only). If operator-week-of-use surfaces friction, polish round adds a forensic-surface deep-link.

**Q3**: Concurrent-mutation conflict chip copy — spec deferred. Plan default: `failed to transition ticket #142 — another mutation got there first` (more conversational than the raw error_kind). Implementation uses this default unless operator pushes back.

**Q4**: `/hiring/proposals` left-rail placement — spec deferred. Plan default per D6: under "CEO chat" subnav. Implementation reads this default.

**Q5**: M5.2 retro EventRow rendering for `chat.session_started` / `chat.message_sent` / `chat.session_ended` channels — does the M5.2 retro carryover need any payload-shape adjustments to the existing M5.1 emitters, or just dashboard-side EventRow branches? **Resolution path**: implementation reads M5.1 emitter code; if payload reshape needed, scope the change.

**Q6**: Architecture amendment line 574 verbatim text — plan provides the substring (§10), but the surrounding paragraph may need light editing to keep the M5 line coherent with the M5.1/M5.2/M5.4 enumeration. **Resolution path**: implementation reads the current line, drafts the replacement, ensures the substring is present.

**Q7**: Mockclaude fixture script content — plan enumerates 9 fixtures (§11) but doesn't write the NDJSON. **Resolution path**: implementation writes per-fixture NDJSON matching the existing M5.1/M5.2 mockclaude format, validates against the actual mockclaude binary's parse.

**Q8**: Threat-model amendment doc body content — plan provides the outline (§9); T001 writes the body. **Resolution path**: T001 implementation phase.

---

## Complexity tracking

No constitutional violations to track. Zero new direct dependencies in either supervisor or dashboard. The autonomous-execution posture is novel but justified by the threat-model amendment landing as a binding input; the constitution's Principle IV (soft gates) extends naturally into chat via the amendment.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | — | — |

---

## Plan completion checklist

- [x] Every binding question from the spec resolved or surfaced as deferred-with-default
- [x] Package boundaries decided (D1–D8)
- [x] Type system decisions made (D9–D13)
- [x] Data model fully specified (D14–D17 + §3)
- [x] Lifecycle management explicit (D18–D20 + §4)
- [x] Error vocabulary enumerated (D21 + §12)
- [x] Test strategy at test-function granularity (D22 + §14)
- [x] Forward-compat hooks named (D23 + §16)
- [x] Implementation ordering with dependency chain (§15)
- [x] Threat-model amendment outline binding (§9)
- [x] Architecture amendment substrings pinned (§10)
- [x] Mockclaude fixtures enumerated (D23 + §11)
- [x] No new dependencies; locked-deps streak preserved (FR-540)
- [x] Constitution Check passes; concurrency rules respected
- [x] Plan compiles into a `/garrison-tasks` input without further structural decisions
