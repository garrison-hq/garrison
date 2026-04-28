# Implementation plan: M5.1 — CEO chat backend (read-only)

**Branch**: `010-m5-1-ceo-chat-backend` | **Date**: 2026-04-28 | **Spec**: [spec.md](./spec.md)
**Input**: [spec.md](./spec.md) (post-clarify), [m5-1-context.md](../_context/m5-1-context.md), [m5-spike.md](../../docs/research/m5-spike.md) (post-§8 extension), [vault-threat-model.md](../../docs/security/vault-threat-model.md), AGENTS.md, RATIONALE.md, M2.1 retro, M2.2 retro, M2.3 retro, M3 retro, M4 retro, existing supervisor + dashboard codebases.

---

## Summary

M5.1 ships the supervisor-resident chat runtime that M5.2's dashboard surface will consume. On every operator chat message the supervisor: fetches the operator's `CLAUDE_CODE_OAUTH_TOKEN` from Infisical, spawns an ephemeral `garrison-claude:m5` Docker container via the existing docker-proxy, replays the session's prior turns plus the new turn into the container via stdin (`--input-format stream-json`, no `--resume`), pipes streaming token deltas to the dashboard via `pg_notify('chat.assistant.delta', …)`, persists the assembled assistant turn at terminal commit, and tears the container down. Conversation state lives entirely in Postgres (`chat_sessions`, `chat_messages`); the supervisor reconstructs the transcript from row state on every turn. Anthropic's prompt cache makes the replay-per-turn approach affordable (spike §8.1 confirmed `cache_read_input_tokens > 0` on turn 2).

The plan extends the existing supervisor: a new `internal/chat/` package owns the runtime; `internal/spawn/pipeline.go` is refactored to host both the existing finalize-shaped policy and the new chat policy via a `Policy` interface; `internal/claudeproto/router.go` gains `OnStreamEvent` for token-level deltas; `internal/mempalace/dockerexec.go` is promoted to a shared `internal/dockerexec/` package; one supervisor migration (`20260428000015_m5_1_chat_backend.sql`) creates the two tables, indexes, and dashboard grants; `supervisor/Dockerfile.claude` produces the chat-container image; the docker-proxy compose service gains `CREATE: 1`. The dashboard gains one new SSE route (`/api/sse/chat`), one server-action module (`lib/actions/chat.ts`), one query module (`lib/queries/chat.ts`), and Drizzle-pulls the two new tables into `schema.supervisor.ts`. Zero supervisor-side mutation tools land — chat in M5.1 is strictly read-only, mounting only the existing `postgres` (RO via `garrison_agent_ro`) and `mempalace` MCP servers.

---

## Technical Context

**Language/Version**: Go 1.23 (supervisor); TypeScript 5.6+ / Next.js 16 / React 19 (dashboard); SQL (one new migration).
**Primary Dependencies (supervisor)**: existing locked list. M5.1 adds **zero** new direct dependencies — `pgx/v5` (LISTEN/NOTIFY + tx), `slog` (logging), `errgroup` (subsystem lifecycle), `os/exec` (subprocess), `syscall` (Setpgid + signals), `bufio` (NDJSON scan), Infisical SDK (already direct since M2.3), testcontainers-go (already direct since M2.3). Reuses the existing `internal/spawn/pipeline.go` parser, `internal/claudeproto` router, `internal/vault` client, `internal/mcpconfig` per-spawn config writer, `internal/mempalace/dockerexec.go` (renamed, see §Project Structure), and `internal/store` sqlc queries.
**Primary Dependencies (dashboard)**: existing locked list. M5.1 adds **zero** new direct dependencies — reuses Drizzle ORM (introspection regenerates `schema.supervisor.ts` to add the two new tables), `postgres-js` (LISTEN for SSE bridge), better-auth (session validation), the `@infisical/sdk` (already direct since M4 — but the chat OAuth token is fetched supervisor-side, so the dashboard SDK isn't exercised by M5.1).
**Storage**: Postgres (shared with M2/M3/M4). Two new supervisor-domain tables (`chat_sessions`, `chat_messages`) join the existing schema. `garrison_dashboard_app` Postgres role gains `INSERT, SELECT, UPDATE` on `chat_sessions` and `INSERT, SELECT` on `chat_messages`. `garrison_agent_ro` (existing M2.1 role) gains no new grants — chat-container access is identical to ticket-spawn access. `vault_access_log` (existing) extends in *use* not *schema* — the M4-added `metadata JSONB` column carries `{actor: 'supervisor_chat', chat_session_id, chat_message_id}` for chat-token reveal rows.
**Testing**: Go test (`go test ./...`) for supervisor unit + chaos tests. Testcontainers-go (Postgres + Infisical, already wired in M2.3) for supervisor integration tests. Vitest for dashboard unit tests (Drizzle types, server-action validation). Playwright (existing M3/M4 harness against the dashboard standalone bundle) for the end-to-end chat backend test. CI runs both real-claude tests (where an OAuth token is available) and mockclaude:m5 tests (always — no credential required).
**Target Platform**: Linux container for both supervisor and dashboard. Chat container runs `node:22-slim` + `@anthropic-ai/claude-code@2.1.86` pinned. The chat container is *spawned by* the supervisor (via the existing `garrison-docker-proxy` linuxserver/socket-proxy service) at runtime — it is not a long-lived compose service.
**Project Type**: Web application — supervisor backend service + dashboard web app + ephemeral Claude Code subprocess containers. The chat runtime is the third subprocess pattern in the supervisor (after `internal/spawn` for ticket execution and `internal/mempalace/dockerexec` for MemPalace tool calls).
**Performance Goals**: First SSE `text_delta` reaches the dashboard ≤ 5s wall-clock from operator INSERT (real claude, pre-pulled image, dev-grade laptop) per SC-001. Token-level streaming throughout the turn (SSE delta latency ≤ 1s after claude emits) per FR-051. Cold-start budget (vault fetch + container start + MCP boot) target ≤ 1.5s of the 5s; inference dominates the rest. Per-turn timeout default 5 minutes; idle session timeout default 30 minutes.
**Constraints**: AGENTS.md Concurrency rules 1, 2, 4, 5, 6, 7, 8 bind throughout (every goroutine has a context, root-ctx-cancellation cascades, subprocess uses CommandContext + Setpgid + process-group SIGTERM, terminal writes via `context.WithoutCancel`, pipes drained before `cmd.Wait`). Threat model Rule 6 (no secrets in audit metadata) extends to chat content — message text never appears in `event_outbox.payload` or any `pg_notify` payload, only opaque IDs. Per Constitution III the supervisor stores no chat state outside Postgres+MemPalace — Claude's local session store is bypassed via stdin replay rather than `--resume`. The chat container's MCP surface is exactly `postgres` + `mempalace` + zero built-in agentic tools (`--tools ""` zeroes the default Bash/Edit/Write/etc per spike §8.2). The `garrison-docker-proxy` allow-list adds one verb (`CREATE: 1`) for `POST /containers/create` — no other proxy-side broadening.
**Scale/Scope**: Single operator, single concurrent chat session per Constitution X. Per-session per-turn serial processing (in-process mutex, not advisory lock — single-operator low contention). Cost telemetry: per-message + per-session roll-up; reactive soft cap at $1.00 default (clarify Q5). Persistence retention "keep forever within the row window of M5.1" — no trim job in M5.1 (deferred per spec).

---

## Constitution Check

Garrison constitution (`.specify/memory/constitution.md`) gates:

- **Principle I (Postgres + pg_notify)**: M5.1 honors it. Operator → supervisor signal is `pg_notify('chat.message.sent', message_id)` (FR-050); supervisor → dashboard delta transport is `pg_notify('chat.assistant.delta', json_payload)` (FR-051); supervisor → activity-feed audit is `pg_notify('work.chat.session_started|message_sent|session_ended', json_payload)` (FR-070-071). All notifies emit inside the same transaction as the row write (mirrors M2.1 commit pattern). No parallel state stores; no separate HTTP/RPC port for chat (FR-053).
- **Principle II (MemPalace as memory layer)**: chat *reads* MemPalace via the existing `mempalace` MCP server (no schema change). Chat does *not* write to MemPalace — the post-session retro is the human-driven path that mirrors a chat highlight to MemPalace if useful. M5.1 ships the producer (chat_messages); future operator-driven mirror is out of scope.
- **Principle III (ephemeral agents)**: chat is the canonical example. Each turn spawns a fresh ephemeral container, runs to completion, terminates. No long-lived chat process; no in-memory conversation state. Multi-turn context is reconstructed from Postgres `chat_messages` row state on every turn (FR-013, clarify Q2). Claude's local session store (`~/.claude/projects/`) is *not* mounted; `--resume` is *not* used.
- **Principle IV (soft gates)**: M5.1 introduces zero hard gates on the chat itself. The session cost cap (FR-061) is a soft cap that refuses the *next* spawn rather than aborting an in-flight turn. The MCP-config precheck (FR-022) rejects mutation-server names by *name* (decision Q-E) — strict enough for M5.1 since `garrison-mutate` doesn't exist yet, broadens to tool-surface check at M5.3.
- **Principle V (skills.sh)**: not applicable — chat doesn't load skills; the chat container has `--tools ""` (no built-in tools, no skills runtime).
- **Principle VI (UI-driven hiring)**: not applicable — chat does not create agents.
- **Principle VII (Go-only supervisor with locked deps)**: M5.1 adds **zero** new Go dependencies. The chat container is a runtime artefact that runs `node:22-slim` + claude-code, but the supervisor binary that *spawns* it stays pure Go (per operator's Q-C confirmation). Runtime containers that the supervisor manages have always been allowed (mempalace is python-in-container, postgres is c-in-container) — the rule is about the supervisor binary's own sources, not its subprocess targets.
- **Principle VIII (every goroutine has a context)**: every new goroutine in `internal/chat/` accepts the supervisor's root context. The chat listener goroutine, per-message worker goroutines, idle-sweep ticker, restart-sweep one-shot all join the main errgroup or are scoped to the listener's child errgroup. Subprocess invocation uses `exec.CommandContext` with `Setpgid: true` and `Cancel = killProcessGroup(SIGTERM)` (mirrors `internal/spawn/spawn.go:613-621`). Pipes drained before `cmd.Wait()` (rule 8). Terminal writes via `context.WithoutCancel` + `TerminalWriteGrace` (rule 6).
- **Principle IX (narrow specs per milestone)**: M5.1 ships exactly the read-only chat backend. Out-of-scope per spec/context: dashboard chat UI (M5.2), mutation tools (M5.3+ post-amendment), long-lived chat containers, multi-operator, OAuth refresh, mutation MCP server, chat history search, threat-model amendment, chat-driven MemPalace writes.
- **Principle X (per-department concurrency caps)**: not applicable — chat is a single global resource per Constitution X. One chat container per session at a time, serial within a session. No new `concurrency_cap` rows. Multiple sessions can run concurrently *across operators* — but Garrison is single-operator, so M5.1 ships with the in-process per-session mutex as the serialization primitive.
- **Principle XI (self-hosted)**: chat depends on Anthropic's API (api.anthropic.com) at runtime — but only inside the per-message ephemeral container, billed against the operator's claude.ai account. The supervisor itself adds zero cloud dependencies. Infisical (vault) continues to run inside the Coolify project. The OAuth token's destination service (Anthropic) was an existing dependency from RATIONALE.md §"Claude Code subprocesses" — M5.1 makes it explicit but doesn't add it.

**Concurrency discipline §AGENTS.md "non-negotiable" rules**:

- **Rule 1** (every goroutine has a context): the new chat listener, per-message workers, idle-sweep ticker each accept the chat subsystem's `gctx`. No bare `go func()`.
- **Rule 2** (root context owns SIGTERM cascade): supervisor SIGTERM cancels the root context; chat subsystem cancels via the errgroup; per-message workers see ctx-done and signal their docker subprocess via process group.
- **Rule 4** (subprocess uses CommandContext): `exec.CommandContext(execCtx, dockerBin, dockerArgs...)` — same pattern as `internal/spawn/spawn.go:613`.
- **Rule 5** (channel sender/receiver discipline): the supervisor → SSE delta path uses `pg_notify` not Go channels; no new buffered channels in chat code.
- **Rule 6** (terminal write via WithoutCancel): chat terminal writes (assistant turn commit, session ended/aborted) use `context.WithoutCancel(ctx)` + `TerminalWriteGrace` mirroring M2.x.
- **Rule 7** (process group lifecycle): `cmd.SysProcAttr.Setpgid = true`; cancel signals via `syscall.Kill(-pgid, SIGTERM)` (reuse `internal/spawn/pgroup.go`'s `killProcessGroup` helper — promoted to a shared helper if `internal/spawn` keeps it package-private; otherwise copy-pasta with attribution).
- **Rule 8** (drain pipes before Wait): the chat parser scans stdout to EOF before `cmd.Wait()` returns. Same pattern as M2.1.

**Scope discipline (AGENTS.md §)**: M5.1 stays read-only per `ARCHITECTURE.md:574`. The plan does *not* design for M5.3 mutations; the FR-022 precheck is name-based to keep the M5.1 surface narrow. No threat-model amendment lands with M5.1 — palace-injected text in chat output is the worst case, and the read-only tool surface bounds the blast radius.

**Constitutional violations to track**: zero. Zero new Go dependencies in the supervisor. Zero new TS dependencies in the dashboard. The chat container's `node:22-slim` base image is a runtime artefact, not a Go-side import.

---

## Project Structure

### Documentation (this feature)

```text
specs/010-m5-1-ceo-chat-backend/
├── spec.md                # /speckit.specify + /speckit.clarify output
├── plan.md                # this file
├── tasks.md               # /speckit.tasks output (NOT created by /speckit.plan)
└── acceptance-evidence.md # post-implementation, mirrors M3/M4 pattern
```

The spike + context live outside the milestone dir per Garrison convention:

```text
docs/research/m5-spike.md            # binding for tool-behavior questions
specs/_context/m5-1-context.md       # binding for scope decisions
```

### Source Code (repository structure for M5.1)

```text
supervisor/
├── cmd/supervisor/
│   ├── main.go                                    # MODIFIED: thread chat.Deps + chat.RunListener into the errgroup
│   └── migrations/
│       └── 20260428000015_m5_1_chat_backend.sql   # NEW: chat_sessions + chat_messages + grants
├── internal/
│   ├── chat/                                      # NEW PACKAGE
│   │   ├── deps.go                                # chat.Deps struct + constructor
│   │   ├── errorkind.go                           # chat.ErrorKind typed string + vocabulary
│   │   ├── idle.go                                # chat.RunIdleSweep — 60s ticker, marks active→ended on timeout
│   │   ├── idle_test.go                           # unit
│   │   ├── listener.go                            # chat.RunListener — LISTEN chat.message.sent, dispatch to worker
│   │   ├── listener_test.go                       # unit
│   │   ├── persistence.go                         # session/message CRUD wrappers over store.Queries
│   │   ├── persistence_test.go                    # unit
│   │   ├── policy.go                              # ChatPolicy implements spawn.Policy + claudeproto.Router
│   │   ├── policy_test.go                         # unit
│   │   ├── restart.go                             # chat.RunRestartSweep — one-shot at boot, marks pending/streaming → aborted
│   │   ├── restart_test.go                        # unit
│   │   ├── spawn.go                               # chat.spawnTurn — vault fetch + docker run + parser wiring
│   │   ├── spawn_test.go                          # unit (worker-level, mocks dockerexec + vault)
│   │   ├── transcript.go                          # chat.AssembleTranscript — replay rows → stdin NDJSON
│   │   ├── transcript_test.go                     # unit (golden multi-turn fixtures)
│   │   ├── transport.go                           # chat.EmitDelta + chat.EmitTerminal — pg_notify wrappers
│   │   ├── transport_test.go                      # unit
│   │   └── worker.go                              # chat.handleMessage — per-message orchestration (cost-cap → spawn → commit)
│   ├── claudeproto/
│   │   ├── events.go                              # MODIFIED: add StreamEvent type with content_block_delta decode
│   │   ├── events_test.go                         # MODIFIED: add OnStreamEvent dispatch coverage
│   │   └── router.go                              # MODIFIED: Router interface gains OnStreamEvent; routeStreamEvent added
│   ├── dockerexec/                                # NEW PACKAGE (renamed from internal/mempalace/dockerexec.go)
│   │   ├── dockerexec.go                          # MOVED: RealDockerExec + DockerExec interface
│   │   └── dockerexec_test.go                     # MOVED: existing tests
│   ├── mcpconfig/
│   │   ├── mcpconfig.go                           # MODIFIED: add BuildChatConfig — postgres + mempalace only, runs CheckExtraServers
│   │   └── mcpconfig_test.go                      # MODIFIED: add coverage for chat config shape
│   ├── mempalace/
│   │   └── ...                                    # MODIFIED: imports updated to use internal/dockerexec instead of local file
│   └── spawn/
│       ├── pipeline.go                            # MODIFIED: extract Policy interface; existing pipelineRouter becomes FinalizePolicy; Run accepts Policy
│       ├── pipeline_test.go                       # MODIFIED: existing finalize tests preserved; new test asserts dual-policy compile-shape
│       └── pgroup.go                              # MODIFIED: export killProcessGroup as ProcessGroup.Kill (chat reuses)
├── tests (in package supervisor/_root):
│   ├── chaos_m5_1_test.go                         # NEW: kill chat container mid-stream chaos case
│   ├── integration_m5_1_chat_happy_path_test.go   # NEW: single-turn end-to-end against mockclaude:m5
│   ├── integration_m5_1_chat_multi_turn_test.go   # NEW: turn replay context fidelity
│   ├── integration_m5_1_chat_cost_cap_test.go     # NEW: soft cap reached → next refused
│   ├── integration_m5_1_chat_idle_timeout_test.go # NEW: idle → ended → next message rejected
│   └── integration_m5_1_chat_vault_test.go        # NEW: token absent / 401 / 5xx paths
├── docker-compose.yml                             # MODIFIED: docker-proxy env gains CREATE: 1; supervisor service gains GARRISON_CHAT_* env passthrough
├── Dockerfile.claude                              # NEW: produces garrison-claude:m5
├── Dockerfile.mockclaude.chat                     # NEW: produces garrison-mockclaude:m5 (extends existing mockclaude)
├── Makefile                                       # MODIFIED: add chat-image + mockclaude-chat-image targets
└── tools/vaultlog/                                # UNCHANGED: continues to bind on chat code paths

dashboard/
├── app/
│   └── api/
│       └── sse/
│           └── chat/
│               └── route.ts                       # NEW: SSE bridge — LISTEN chat.assistant.delta + read terminal state
├── lib/
│   ├── actions/
│   │   ├── chat.ts                                # NEW: startChatSession + sendChatMessage server actions
│   │   └── chat.test.ts                           # NEW: unit (validation, role enforcement)
│   ├── queries/
│   │   ├── chat.ts                                # NEW: list-sessions, get-session-with-messages, get-running-cost
│   │   └── chat.test.ts                           # NEW: unit
│   └── sse/
│       └── chatListener.ts                        # NEW: postgres LISTEN wrapper for chat.assistant.delta
├── drizzle/
│   ├── _introspected/                             # AUTO-REGENERATED via bun run drizzle:pull
│   └── schema.supervisor.ts                       # AUTO-REGENERATED to include chatSessions + chatMessages
├── tests/
│   └── integration/
│       └── m5-1-chat-backend.spec.ts              # NEW: Playwright end-to-end (FR-102)
└── messages/en.json                               # MODIFIED: add chat error vocabulary keys for M5.2 to consume

docs/
├── ops-checklist.md                               # MODIFIED: new "M5.1 — chat backend" section (FR-092)
└── retros/m5-1.md                                 # NEW: post-implementation retro
```

**Structure decision**: M5.1 extends both supervisor and dashboard, with the supervisor side carrying the bulk of the work (~80% of new code). The new `internal/chat/` package is the single largest addition; everything else is targeted modifications to existing files. The `internal/dockerexec/` rename is the one cross-package refactor — accepted as in-scope per operator's Q-D answer ("fold into M5.1") since both mempalace and chat depend on `RealDockerExec` and the renamed package keeps imports honest. Existing M2.x callers (mempalace) update their import path in the same commit; no behavior change.

---

## Phase 0 — Research

Phase 0 is **complete** before plan-drafting. The research lives in two committed documents:

1. `docs/research/m5-spike.md` — the original spike plus §8 extension (commit `8d0e097`). Findings binding for the plan:
    - **§3** ephemeral-per-turn shape is the recommended posture (~1s/turn cost vs long-lived; matches existing `Spawn()` lifecycle).
    - **§5** stream-json `--include-partial-messages` produces token-level `text_delta` events under `stream_event` envelopes; existing parser routes them to `OnUnknown` today.
    - **§8.1** stdin stream-json multi-turn replay works; `cache_read_input_tokens > 0` on turn 2 against the spike's mock test, validating prompt-cache amortization.
    - **§8.2** `--tools ""` zeroes built-in agentic tools — load-bearing for read-only chat surface.
    - **§8.3** `--include-partial-messages` requires `--verbose` when paired with `--output-format stream-json`.
2. `specs/_context/m5-1-context.md` — scope, binding inputs, and the 12 open questions the spec resolved (clarify-round committed in `a7f4e69`).

No additional Phase 0 research is needed before implementation. The spec's 17 success criteria all map onto observations either confirmed in the spike or specified in the clarify round.

---

## Phase 1 — Design

### Subsystem state machines

**Chat session lifecycle**:

```text
                                     ┌──────────────┐
                                     │   (no row)   │
                                     └──────┬───────┘
                                            │ dashboard server action
                                            │   startChatSession()
                                            ▼
                ┌────────────┐           ┌────────┐
   idle 30min ──┤   ended    │ ◄─────────│ active │
                └────────────┘           └────┬───┘
                                              │ supervisor SIGTERM / crash
                                              │   restart-sweep  /
                                              │   in-flight kill
                                              ▼
                                          ┌──────────┐
                                          │ aborted  │
                                          └──────────┘
```

**Chat message lifecycle (assistant rows only — operator rows always INSERT at status='completed')**:

```text
                ┌──────────────┐
                │  (no row)    │
                └──────┬───────┘
                       │ supervisor receives chat.message.sent notify;
                       │   INSERTs assistant row (turn_index = operator+1)
                       ▼
                  ┌─────────┐
                  │ pending │  status at INSERT, before container is started
                  └────┬────┘
                       │ first text_delta event arrives
                       ▼
                ┌────────────┐
                │ streaming  │  status while deltas flow
                └────┬───────┘
                     │ result event commits OR error path
                     │
       ┌─────────────┼──────────────┬─────────────────────────┐
       ▼             ▼              ▼                         ▼
  ┌────────┐    ┌────────┐    ┌──────────┐              ┌─────────┐
  │complete│    │ failed │    │ aborted  │              │(unchanged
  │   d    │    │        │    │          │              │ after term)
  └────────┘    └────────┘    └──────────┘              └─────────┘
   normal       MCP fail      SIGTERM/turn-timeout
   exit         vault fail    cost-cap / idle / restart
                claude err
```

**Per-message worker lifecycle**:

```text
chat.message.sent notify received
  │
  ▼
acquire per-session in-process mutex (sync.Mutex map keyed by session_id)
  │
  ▼
load session row → check status='active' (else error_kind='session_ended')
  │
  ▼
check total_cost_usd >= GARRISON_CHAT_SESSION_COST_CAP_USD (else error_kind='session_cost_cap_reached')
  │
  ▼
INSERT assistant row at status='pending' (turn_index = operator.turn_index + 1)
  │
  ▼
fetch CLAUDE_CODE_OAUTH_TOKEN from vault (vault.Fetch with synthesized GrantRow)
  │  └─ writes vault_access_log row in same tx
  │  └─ on error: terminal-write assistant with token_not_found / token_expired / vault_unavailable
  ▼
write per-message MCP config to ${MCP_DIR}/chat-${chat_message_id}.json
  │  └─ runs mcpconfig.CheckExtraServers (FR-022 — name-based reject mutation servers)
  ▼
assemble transcript NDJSON from prior chat_messages rows (operator + completed assistant)
  │
  ▼
docker run --rm -i -e CLAUDE_CODE_OAUTH_TOKEN=$TOKEN \
    -v $MCP_PATH:/etc/garrison/mcp.json:ro \
    --network garrison-net \
    $GARRISON_CHAT_CONTAINER_IMAGE \
    -p --verbose --input-format stream-json --output-format stream-json \
    --include-partial-messages --tools "" \
    --mcp-config /etc/garrison/mcp.json --strict-mcp-config \
    --permission-mode bypassPermissions --model <default>
  │
  │  (cmd.SysProcAttr.Setpgid = true; cmd.Cancel = killProcessGroup(SIGTERM); cmd.WaitDelay = ShutdownSignalGrace)
  │
  ▼
write transcript to cmd.StdinPipe(); close stdin to signal EOF
  │
  ▼
spawn.Run(stdout, ChatPolicy{...})
  │  └─ ChatPolicy.OnInit: validate MCP health, transition assistant row to status='streaming'
  │  └─ ChatPolicy.OnStreamEvent: pg_notify('chat.assistant.delta', payload) with seq counter
  │  └─ ChatPolicy.OnResult: terminal commit assistant row (content + cost + raw_envelope) IN TX:
  │      - UPDATE chat_messages SET status='completed', content=..., cost_usd=..., raw_event_envelope=...
  │      - UPDATE chat_sessions SET total_cost_usd = total_cost_usd + cost_usd
  │      - pg_notify('work.chat.message_sent', ...)
  ▼
cmd.Wait() (after pipeline scanner returns)
  │
  ▼
remove per-message MCP config file
  │
  ▼
SecretValue.Zero() on the OAuth token
  │
  ▼
release per-session mutex
```

### Public interfaces

**`internal/chat/deps.go`**:

```go
type Deps struct {
    Pool                  *pgxpool.Pool
    Queries               *store.Queries
    VaultClient           vault.Fetcher
    DockerExec            dockerexec.DockerExec
    Logger                *slog.Logger
    CustomerID            pgtype.UUID
    OAuthVaultPath        string        // e.g. "/operator/CLAUDE_CODE_OAUTH_TOKEN"
    ChatContainerImage    string        // e.g. "garrison-claude:m5"
    MCPConfigDir          string        // e.g. "/var/lib/garrison/mcp"
    DockerNetwork         string        // e.g. "garrison-net"
    TurnTimeout           time.Duration // default 5min
    SessionIdleTimeout    time.Duration // default 30min
    SessionCostCapUSD     decimal.Big   // default 1.00
    TerminalWriteGrace    time.Duration // reuses spawn.TerminalWriteGrace
    ShutdownSignalGrace   time.Duration // reuses spawn.ShutdownSignalGrace
    ClaudeBinInContainer  string        // default "/usr/local/bin/claude" — claude binary path inside the container
    DefaultModel          string        // default "claude-sonnet-4-6"
}
```

**`internal/chat/listener.go`**:

```go
// RunListener LISTENs on chat.message.sent and dispatches each notify
// to a per-message worker. Returns when ctx is cancelled or the listen
// connection fails non-recoverably. Joins the supervisor's main errgroup.
func RunListener(ctx context.Context, deps Deps) error
```

**`internal/chat/restart.go`**:

```go
// RunRestartSweep marks any pending/streaming chat_messages older than
// 60s as aborted with error_kind='supervisor_restart', and rolls their
// sessions to status='aborted'. Runs once at boot, before RunListener.
func RunRestartSweep(ctx context.Context, deps Deps) error
```

**`internal/chat/idle.go`**:

```go
// RunIdleSweep ticks every 60s and marks active sessions whose newest
// chat_messages.created_at is older than SessionIdleTimeout as 'ended'.
// Joins the chat subsystem's errgroup; respects ctx cancellation.
func RunIdleSweep(ctx context.Context, deps Deps) error
```

**`internal/chat/policy.go`**:

```go
// ChatPolicy implements spawn.Policy (and therefore claudeproto.Router).
// Each per-message worker constructs a fresh ChatPolicy bound to the
// (sessionID, messageID) pair it's processing.
type ChatPolicy struct {
    SessionID    pgtype.UUID
    MessageID    pgtype.UUID
    Pool         *pgxpool.Pool
    Queries      *store.Queries
    Logger       *slog.Logger
    deltaSeq     int                  // monotonic per-message
    contentBuf   strings.Builder      // accumulates text_delta payloads
    rawEvents    []json.RawMessage    // accumulates the full envelope sequence
    bailReason   string               // populated by OnInit on MCP health failure
}

// OnInit, OnAssistant, OnUser, OnRateLimit, OnResult, OnTaskStarted,
// OnUnknown, OnStreamEvent — see spec.md FR-030 to FR-033.
// OnTerminate(reason) — called by spawn.Run on parse error / bail / ctx-done.
```

**`internal/spawn/pipeline.go` Policy refactor**:

```go
// Policy abstracts the parser's lifecycle hooks so both finalize-shaped
// (existing M2.2.1+) and chat-shaped (M5.1+) flows share Run.
//
// All methods inherit from claudeproto.Router. OnTerminate is the new
// hook for parse errors / bail / ctx-done; existing finalize callers
// pass a no-op for backward compat.
type Policy interface {
    claudeproto.Router
    OnTerminate(ctx context.Context, reason string)
}

// FinalizePolicy is the existing pipelineRouter, repackaged as a Policy.
// Construction is identical to the old newPipelineRouter call.
func NewFinalizePolicy(...) *FinalizePolicy

// Run accepts any Policy. Existing call sites in spawn.go pass
// NewFinalizePolicy(...); chat callers pass *chat.ChatPolicy.
func Run(ctx context.Context, stdout io.Reader, policy Policy, logger *slog.Logger) (Result, error)
```

The signature change is backwards-compatible at the call-site level (`internal/spawn/spawn.go` updates one line: pass `NewFinalizePolicy(...)` instead of `FinalizeDeps`). The existing test suite stays green; finalize behaviour is byte-for-byte unchanged. Justification for the refactor in the T### task commit message; included here so reviewers see the abstraction as load-bearing for chat, not speculative.

**`internal/claudeproto/router.go` extension**:

```go
type Router interface {
    OnInit(ctx context.Context, e InitEvent) RouterAction
    OnAssistant(ctx context.Context, e AssistantEvent)
    OnUser(ctx context.Context, e UserEvent)
    OnRateLimit(ctx context.Context, e RateLimitEvent)
    OnResult(ctx context.Context, e ResultEvent)
    OnStreamEvent(ctx context.Context, e StreamEvent)  // NEW
    OnTaskStarted(ctx context.Context, e TaskStartedEvent)
    OnUnknown(ctx context.Context, e UnknownEvent)
}

// StreamEvent decodes the wrapper around content_block_delta (text_delta)
// and message_start/message_stop. Other inner event types (cache info,
// thinking blocks) decode into the Raw field for ad-hoc handling.
type StreamEvent struct {
    SessionID string
    Inner     StreamInner       // discriminated by InnerType
    InnerType string            // "content_block_delta" | "message_start" | "message_stop" | "message_delta" | "content_block_start" | "content_block_stop"
    Raw       []byte
}

type StreamInner struct {
    Index             int             // for content_block_*
    DeltaType         string          // for content_block_delta — usually "text_delta"
    DeltaText         string          // for text_delta — the appended characters
    StopReason        string          // for message_delta
    InputTokens       int             // for message_start.usage
    OutputTokens      int             // for message_delta.usage
    CacheReadInput    int             // for message_start.usage.cache_read_input_tokens
    CacheCreationInp  int             // for message_start.usage.cache_creation_input_tokens
}
```

The existing `pipelineRouter.OnStreamEvent` is added as a no-op (existing finalize behaviour unchanged — finalize ignores stream_event lines, same as today's `OnUnknown` handling). `chat.ChatPolicy.OnStreamEvent` does the work: extracts text_delta, batches into the `pg_notify` payload (≤7KB chunks per FR-051), increments `deltaSeq`, appends to `contentBuf` for terminal commit.

**`internal/dockerexec/dockerexec.go`** — moved verbatim from `internal/mempalace/dockerexec.go`. Test file moves with it. The mempalace package's existing call sites change one import line. No behaviour change.

**`internal/mcpconfig/mcpconfig.go`** — adds:

```go
// BuildChatConfig returns the MCP config JSON for a chat-message spawn.
// Includes only the postgres (RO) and mempalace servers; calls
// CheckExtraServers on the result to defend against accidental mutation
// server inclusion (FR-022). Returns the bytes ready to write to
// /var/lib/garrison/mcp/chat-<message_id>.json.
func BuildChatConfig(ctx context.Context, deps ChatConfigDeps) ([]byte, error)
```

**`dashboard/app/api/sse/chat/route.ts`** — mirrors `/api/sse/activity` shape:

- GET handler: validates better-auth session (401 on missing); reads `?session_id=<uuid>` query param; opens a ReadableStream; LISTENs on `chat.assistant.delta` for that session_id; forwards each notify payload as an SSE `delta` event; on `pg_notify('work.chat.message_sent')` for the session, reads the terminal `chat_messages` row and emits a terminal SSE event; on `pg_notify('work.chat.session_ended')`, emits `session_ended` and closes the stream; respects `req.signal.abort` for client disconnect; heartbeat every 25s; `Last-Event-ID` resumes from the next terminal event (FR-052: deltas are not replayable post-disconnect).

**`dashboard/lib/actions/chat.ts`**:

```typescript
'use server';

// Creates a chat_sessions row + first chat_messages row in one tx.
// Returns the new sessionId. Emits pg_notify('chat.message.sent', message_id).
export async function startChatSession(content: string): Promise<{sessionId: string; messageId: string}>;

// INSERTs a subsequent chat_messages row in the existing session.
// turn_index = COALESCE(MAX, -1) + 1 inside the tx; UNIQUE conflict
// retry once with the new max (single-operator collision is rare).
// Emits pg_notify('chat.message.sent', message_id).
export async function sendChatMessage(sessionId: string, content: string): Promise<{messageId: string}>;
```

Both server actions enforce: better-auth session present (else throw `AuthError(NoSession)`); content is non-empty + ≤ 100KB (rough sanity bound, prevents accidentally giant pastes from runaway-allocating); session must be `status='active'` for `sendChatMessage` (else throw `ChatError(SessionEnded)`).

### Data model changes

**Migration `migrations/20260428000015_m5_1_chat_backend.sql`** (single file, goose-flagged):

```sql
-- +goose Up
-- +goose StatementBegin

-- chat_sessions: one row per CEO conversation.
CREATE TABLE chat_sessions (
    id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    started_by_user_id    UUID         NOT NULL REFERENCES users(id),
    started_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    ended_at              TIMESTAMPTZ  NULL,
    status                TEXT         NOT NULL DEFAULT 'active'
                                       CHECK (status IN ('active','ended','aborted')),
    total_cost_usd        NUMERIC(20,10) NOT NULL DEFAULT 0,
    claude_session_label  TEXT         NULL
);
CREATE INDEX idx_chat_sessions_user_started ON chat_sessions (started_by_user_id, started_at DESC);
CREATE INDEX idx_chat_sessions_active ON chat_sessions (status) WHERE status='active';

-- chat_messages: one row per turn (operator and assistant alike).
CREATE TABLE chat_messages (
    id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id            UUID         NOT NULL REFERENCES chat_sessions(id),
    turn_index            INTEGER      NOT NULL,
    role                  TEXT         NOT NULL
                                       CHECK (role IN ('operator','assistant')),
    status                TEXT         NOT NULL
                                       CHECK (status IN ('pending','streaming','completed','failed','aborted')),
    content               TEXT         NULL,  -- non-NULL on terminal commit; NULL during pending/streaming
    tokens_input          INTEGER      NULL,
    tokens_output         INTEGER      NULL,
    cost_usd              NUMERIC(20,10) NULL,
    error_kind            TEXT         NULL,
    raw_event_envelope    JSONB        NULL,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    terminated_at         TIMESTAMPTZ  NULL,
    UNIQUE (session_id, turn_index)
);
CREATE INDEX idx_chat_messages_inflight
    ON chat_messages (session_id, status)
    WHERE status IN ('pending','streaming');

-- Dashboard role grants (FR-042).
GRANT INSERT, SELECT, UPDATE ON chat_sessions TO garrison_dashboard_app;
GRANT INSERT, SELECT          ON chat_messages TO garrison_dashboard_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
REVOKE INSERT, SELECT          ON chat_messages FROM garrison_dashboard_app;
REVOKE INSERT, SELECT, UPDATE  ON chat_sessions FROM garrison_dashboard_app;
DROP TABLE chat_messages;
DROP TABLE chat_sessions;
-- +goose StatementEnd
```

The migration is one file, mirrors the M4 multi-statement pattern. Drizzle pull regenerates `schema.supervisor.ts` to add `chatSessions` + `chatMessages` exports; `bun run typecheck` validates the regenerated types. No new sqlc queries land in this migration's commit; the supervisor's queries against the new tables go in the per-task commits that wire up `internal/chat/persistence.go` + `migrations/queries/chat.sql` (added in T-persistence task).

**No DB triggers**. Per decision 3.4, audit `pg_notify` calls happen in the supervisor's commit transaction, not via `AFTER INSERT/UPDATE` triggers. This keeps the channel-name vocabulary supervisor-owned, supports payload composition, and matches M2.x's commit-pattern idioms.

### Error vocabulary

Exact strings (from `internal/chat/errorkind.go`):

```go
type ErrorKind = string

const (
    // Vault path
    ErrorTokenNotFound       ErrorKind = "token_not_found"
    ErrorTokenExpired        ErrorKind = "token_expired"
    ErrorVaultUnavailable    ErrorKind = "vault_unavailable"

    // MCP-health path (status string is whatever Claude reports — failed/error/disabled/etc.)
    // Stored as "mcp_postgres_failed", "mcp_mempalace_disabled", etc.
    // Built via fmt.Sprintf("mcp_%s_%s", server, status).

    // Spawn / runtime path
    ErrorContainerCrashed         ErrorKind = "container_crashed"
    ErrorDockerProxyUnreachable   ErrorKind = "docker_proxy_unreachable"
    ErrorRateLimitExhausted       ErrorKind = "rate_limit_exhausted"
    ErrorClaudeRuntimeError       ErrorKind = "claude_runtime_error"
    ErrorTurnTimeout              ErrorKind = "turn_timeout"

    // Lifecycle / quota path
    ErrorSessionCostCapReached    ErrorKind = "session_cost_cap_reached"
    ErrorSessionEnded             ErrorKind = "session_ended"
    ErrorSessionNotFound          ErrorKind = "session_not_found"

    // Shutdown / restart path
    ErrorSupervisorShutdown       ErrorKind = "supervisor_shutdown"
    ErrorSupervisorRestart        ErrorKind = "supervisor_restart"
)
```

A `errorkind_test.go` table-test asserts every ErrorKind string is exactly one snake_case word per category — this is the closest analogue to M2.x's `ExitReason` discipline and prevents typos creeping in.

### File and config management

**Per-message MCP config file**:

- Path: `${GARRISON_MCP_CONFIG_DIR}/chat-${chat_message_id}.json` (default dir `/var/lib/garrison/mcp/`).
- Content shape: `{"mcpServers": {"postgres": {...}, "mempalace": {...}}}` with M2.x-compatible inner structures.
- Lifecycle: `internal/mcpconfig.WriteFile(path, BuildChatConfig(...))` before container spawn; `os.Remove(path)` after `cmd.Wait()` returns (whether success or failure). Cleanup is not Postgres-tx-bound; failures are logged at warn level but don't block the spawn or the terminal commit.
- Bind-mount into container: `-v ${path}:/etc/garrison/mcp.json:ro` (read-only by operator decision Q-A).

**Container `docker run` argv** (constructed in `internal/chat/spawn.go`):

```text
docker run --rm -i \
    -e CLAUDE_CODE_OAUTH_TOKEN=$TOKEN \
    -v $MCP_HOST_PATH:/etc/garrison/mcp.json:ro \
    --network $DOCKER_NETWORK \
    --name garrison-chat-$MESSAGE_ID \
    $CHAT_IMAGE \
    -p \
    --verbose \
    --input-format stream-json \
    --output-format stream-json \
    --include-partial-messages \
    --tools "" \
    --mcp-config /etc/garrison/mcp.json \
    --strict-mcp-config \
    --permission-mode bypassPermissions \
    --model $DEFAULT_MODEL
```

Goes through `dockerexec.RealDockerExec.Run`-shape but with `cmd.StdinPipe()` for transcript injection (the existing `Run` method captures stdin from a single io.Reader; chat needs to write multiple NDJSON lines then close stdin to signal EOF). The plan extends `dockerexec.DockerExec` with a new `RunStream(ctx, args, stdin, stdoutHandler) error` method that returns the `*exec.Cmd` setup so the chat caller can: write to `cmd.StdinPipe()`, close stdin, scan stdout via the pipeline parser, drain pipes, `cmd.Wait()`. `mempalace`'s existing one-shot `Run(ctx, args, stdin) (stdout, stderr []byte, err error)` stays unchanged.

**Environment variables** added to the supervisor compose service:

```yaml
GARRISON_CHAT_CONTAINER_IMAGE:        garrison-claude:m5
GARRISON_CHAT_TURN_TIMEOUT:           5m
GARRISON_CHAT_SESSION_IDLE_TIMEOUT:   30m
GARRISON_CHAT_SESSION_COST_CAP_USD:   1.00
GARRISON_CHAT_OAUTH_VAULT_PATH:       /operator/CLAUDE_CODE_OAUTH_TOKEN
GARRISON_CHAT_DOCKER_NETWORK:         garrison-net   # joins the existing compose network
GARRISON_CHAT_DEFAULT_MODEL:          claude-sonnet-4-6
```

`GARRISON_CHAT_OAUTH_VAULT_PATH` is the path **suffix** (e.g. `/operator/CLAUDE_CODE_OAUTH_TOKEN`). At fetch time, the supervisor composes the full `vault.GrantRow.SecretPath` as `"/" + cfg.CustomerID.String() + suffix` to match the existing M2.3 path convention (`splitSecretPath` at `supervisor/internal/vault/client.go:226` expects `/<customer_id>/folder/key` shape). `vault.GrantRow.CustomerID` is also populated as a separate field — used by the audit row writer (`internal/vault/audit.go:WriteAuditRow`) and downstream `vault_access_log.customer_id` column write, **not** for path composition (the path prefix is the M2.3 fetch-side convention; the separate UUID field is for relational integrity on the audit side).

### Subsystem boot order in `cmd/supervisor/main.go`

Existing order (after M4):

1. config load
2. pool + queries
3. agents cache + listener
4. events.Run
5. health server
6. hygiene listener + sweep

M5.1 adds chat after hygiene, before health:

1. config load (extended: parse new `GARRISON_CHAT_*` env vars)
2. pool + queries
3. agents cache + listener
4. events.Run
5. hygiene listener + sweep
6. **chat.RunRestartSweep (one-shot, before listener)**
7. **chat.RunListener** (joins errgroup)
8. **chat.RunIdleSweep** (joins errgroup)
9. health server

The chat subsystem runs in the same root errgroup so SIGTERM cascades cleanly. `chat.RunListener` owns its own `pgx.Conn` for LISTEN (matches `internal/agents.StartChangeListener` pattern). Per-message workers are kicked off via `go func()` *inside* `chat.RunListener`'s ctx, but they're tracked via a child `errgroup.Group` so the listener can wait for in-flight workers on shutdown (with `TerminalWriteGrace` cap).

### Build and tooling

**`supervisor/Dockerfile.claude`**:

```dockerfile
# stage 1 — install + verify claude-code
FROM node:22-slim AS install
ARG CLAUDE_CODE_VERSION=2.1.86
ARG CLAUDE_CODE_INTEGRITY=  # optional sha256 pin; populated at deploy time
RUN npm install -g --no-fund --no-audit @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}
RUN claude --version | grep "${CLAUDE_CODE_VERSION}" >/dev/null || (echo "version mismatch" && exit 1)

# stage 2 — minimal runtime
FROM node:22-slim
COPY --from=install /usr/local/lib/node_modules/@anthropic-ai/claude-code /usr/local/lib/node_modules/@anthropic-ai/claude-code
RUN ln -s /usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js /usr/local/bin/claude && chmod +x /usr/local/bin/claude
ENTRYPOINT ["/usr/local/bin/claude"]
```

Build via `make chat-image` (Makefile target). Tag is `garrison-claude:m5`. Deploy-time pinning via `--build-arg CLAUDE_CODE_INTEGRITY=sha256:...` once npm publishes integrity manifests we can verify (M2.1 used GPG fingerprint + manifest signature for the host-installed claude binary; the chat-container path uses npm registry signatures).

**`supervisor/Dockerfile.mockclaude.chat`**:

Extends the existing `internal/spawn/mockclaude/` binary. The mockclaude binary today emits a fixed sequence of NDJSON lines via Go templates (`internal/spawn/mockclaude/main.go`); for chat the template needs minor extensions: read stdin to detect transcript shape, vary canned response per-turn (last-user-message echo, "purple" detection for the multi-turn test fixture), emit `text_delta` events between init and result so the SSE bridge has something to forward. Two-stage Dockerfile: stage 1 builds the Go binary statically (existing `mockclaude/Makefile` target); stage 2 is `node:22-slim` (so the container's filesystem layout matches `garrison-claude:m5` for compatibility) with the binary symlinked at `/usr/local/bin/claude`.

Build via `make mockclaude-chat-image`. Tag `garrison-mockclaude:m5`. CI sets `GARRISON_CHAT_CONTAINER_IMAGE=garrison-mockclaude:m5` for chat tests.

**docker-proxy compose amendment** — single-line change to `supervisor/docker-compose.yml`:

```diff
   docker-proxy:
     image: ghcr.io/linuxserver/socket-proxy:latest
     container_name: garrison-docker-proxy
     environment:
       POST: 1
       EXEC: 1
       CONTAINERS: 1
+      CREATE: 1   # M5.1 — supervisor spawns chat containers via POST /containers/create
```

The amendment lands in the same git commit as the supervisor code that exercises it (per spec FR-090 + M5.1 context "deploy-time dependency atomic with the runtime that needs it").

---

## Phase 2 — Test strategy

### Unit tests (Go, supervisor)

Per file, with named tests verifying named behaviours. Tests are colocated with the package they test.

**`internal/claudeproto/router_test.go`** (extend existing):

- `TestRouter_OnStreamEvent_TextDelta`: feeds a captured `stream_event` line with `content_block_delta` + `text_delta`; asserts `OnStreamEvent` is called with `InnerType="content_block_delta"`, `DeltaType="text_delta"`, `DeltaText` matches the line's payload.
- `TestRouter_OnStreamEvent_MessageStart`: asserts cache token usage fields populated correctly.
- `TestRouter_OnStreamEvent_NoOpFinalize`: asserts `pipelineRouter.OnStreamEvent` is a no-op (existing finalize behaviour preserved).

**`internal/spawn/pipeline_test.go`** (extend existing):

- `TestPolicy_FinalizeUnchanged`: existing finalize tests pass against the refactored Run signature with `NewFinalizePolicy(...)`.
- `TestPolicy_DualImplementationsCompile`: `var _ Policy = (*FinalizePolicy)(nil)` and `var _ Policy = (*chat.ChatPolicy)(nil)` (build-time interface conformance check).

**`internal/dockerexec/dockerexec_test.go`** (moved from mempalace):

- All existing tests preserved verbatim.
- New: `TestRealDockerExec_RunStream_ClosesStdinOnReturn`: asserts the new `RunStream` shape closes stdin when caller returns from the stdin-write callback.

**`internal/mcpconfig/mcpconfig_test.go`** (extend existing):

- `TestBuildChatConfig_OnlyPostgresAndMempalace`: asserts the JSON has exactly two server entries.
- `TestBuildChatConfig_RejectsExtraServer`: synthesizes a config with a third server; asserts `BuildChatConfig` returns an error from `CheckExtraServers`.
- `TestBuildChatConfig_RejectsMutationServer`: simulates accidental garrison-mutate inclusion; asserts the name-based reject fires.

**`internal/chat/policy_test.go`**:

- `TestChatPolicy_OnInitMCPHealthBail`: feeds an init event with `mcp_servers[0].status="failed"`; asserts `OnInit` returns `RouterActionBail`, sets internal `bailReason="mcp_postgres_failed"`.
- `TestChatPolicy_OnStreamEvent_DeltaPgNotify`: mocks the pool, feeds three `text_delta` events; asserts three `pg_notify('chat.assistant.delta', ...)` calls with monotonic seq 0,1,2 and the expected `delta_text` payloads.
- `TestChatPolicy_OnResult_TerminalCommit`: feeds a `result.success` event after a stream of deltas; asserts a single tx that updates `chat_messages` (status='completed', content=accumulated buffer, cost_usd, tokens_input, tokens_output, raw_event_envelope=JSON array of all events) and `chat_sessions.total_cost_usd += cost_usd`, then emits `pg_notify('work.chat.message_sent', ...)`.
- `TestChatPolicy_OnTerminate_AbortPath`: simulates parse error mid-stream; asserts terminal write happens with `status='failed', error_kind='claude_runtime_error'` via WithoutCancel + TerminalWriteGrace.

**`internal/chat/transcript_test.go`** (golden multi-turn fixtures):

- `TestAssembleTranscript_FirstTurn`: session with one operator row at turn_index=0; asserts NDJSON output is one user message line with the operator's content.
- `TestAssembleTranscript_MultiTurnReplay`: session with operator/assistant/operator at indices 0,1,2; asserts NDJSON has user/assistant/user lines in order.
- `TestAssembleTranscript_SkipsFailedAssistant`: assistant row at index 1 with `status='failed'` is excluded from replay; the next operator turn is index 2 → user message comes after the operator row at index 0 (no assistant gap).
- `TestAssembleTranscript_SkipsAbortedAssistant`: same as above for `status='aborted'`.

**`internal/chat/persistence_test.go`** (testcontainers Postgres):

- `TestSessionCreate_StatusActiveByDefault`: INSERT a chat_sessions row with no status; asserts CHECK constraint sets it to 'active'.
- `TestSessionStatusCheck_RejectsInvalidValue`: INSERT with status='draft'; asserts CHECK error.
- `TestMessageTurnIndex_UniqueConstraint`: INSERT two messages with same `(session_id, turn_index)`; asserts UNIQUE conflict.
- `TestMessageStatus_TerminalTransitions`: walks pending→streaming→completed; asserts each UPDATE succeeds.
- `TestVaultAccessLog_ChatMetadataShape`: INSERT a chat-flavoured vault_access_log row; asserts metadata.actor='supervisor_chat', metadata.chat_session_id, metadata.chat_message_id all round-trip.

**`internal/chat/restart_test.go`** (testcontainers):

- `TestRestartSweep_MarksPendingAborted`: seed an active session with a `pending` chat_messages row 90s old; run sweep; assert message → aborted, session → aborted, error_kind='supervisor_restart'.
- `TestRestartSweep_LeavesYoungPendingAlone`: pending row 10s old; sweep leaves it untouched.
- `TestRestartSweep_LeavesCompletedAlone`: completed row 90s old; sweep leaves it untouched.

**`internal/chat/idle_test.go`** (testcontainers):

- `TestIdleSweep_MarksTimedOutSessionEnded`: session with newest message 31min old + idle timeout 30min; sweep marks session ended.
- `TestIdleSweep_LeavesActiveSessionAlone`: session with newest message 5min old; sweep leaves untouched.

**`internal/chat/errorkind_test.go`**:

- `TestErrorKindVocabularyMatchesSpec`: table-test that every `ErrorKind*` constant exactly matches the spec FR-002, FR-003, FR-031, FR-061, FR-081, FR-083, FR-016, FR-091 enumerations. New error kinds require explicit code change here (mirrors `dashboard/lib/vault/outcomes.test.ts`).

**`internal/chat/spawn_test.go`** (testcontainers Postgres + mock dockerexec + mock vault):

- `TestSpawnTurn_HappyPath`: INSERT operator message; mock vault returns OAuth token; mock dockerexec accepts argv and a stdin reader, returns a canned NDJSON stream; asserts the assistant chat_messages row terminal-writes, vault_access_log row lands, all three audit pg_notifies fire.
- `TestSpawnTurn_VaultFailure_TokenNotFound`: mock vault returns ErrSecretNotFound; asserts no docker run happens, assistant row terminal-writes with error_kind='token_not_found'.
- `TestSpawnTurn_VaultFailure_TokenExpired`: mock vault returns ErrAuthExpired; assistant row → error_kind='token_expired'.
- `TestSpawnTurn_VaultFailure_5xx`: mock vault returns transient error; asserts one retry with 500ms backoff before failing → error_kind='vault_unavailable'.
- `TestSpawnTurn_CostCapReached`: session.total_cost_usd=$1.50 with cap $1.00; assert spawn refused, no vault fetch, error_kind='session_cost_cap_reached'.
- `TestSpawnTurn_MCPConfigBuildError`: mock mcpconfig returns CheckExtraServers err; assert no docker run, error_kind plumbed through.

### Integration tests (Go, supervisor — testcontainers + mockclaude:m5 image)

Tests live at the package root (`supervisor/integration_m5_1_chat_*_test.go`) per the existing M2.x pattern. Each test boots: testcontainers Postgres + Infisical, applies all migrations (1-15), seeds OAuth token in vault, builds the mockclaude:m5 image once per package, runs the chat subsystem against `mockclaude:m5`. Each test takes 5-15s on a warm cache.

**`integration_m5_1_chat_happy_path_test.go`**:

`TestM5_1_HappyPath_SingleTurn`: dashboard server-action equivalent INSERTs a chat_sessions + chat_messages row + emits `chat.message.sent`; chat subsystem processes; assertions:

- assistant `chat_messages` row lands with status='completed', content non-empty, cost_usd > 0
- `vault_access_log` row landed with metadata.actor='supervisor_chat', outcome='value_revealed'
- Three `event_outbox` rows: `work.chat.session_started`, `work.chat.message_sent`, no `session_ended` yet
- No chat content in any event_outbox.payload
- Rule 6 backstop: grep raw_event_envelope JSONB content for `sk-ant-oat01` → zero results

**`integration_m5_1_chat_multi_turn_test.go`**:

`TestM5_1_MultiTurn_ContextFidelity`: turn 1 ("favorite color = purple"); turn 2 ("what is my favorite color"); mockclaude:m5 detects "purple" in stdin transcript replay and emits "Purple." in the canned response; assertions:

- assistant turn 2 content contains "purple" (case-insensitive)
- `chat_messages` turn_index sequence is 0 (operator), 1 (assistant), 2 (operator), 3 (assistant)
- `chat_sessions.total_cost_usd ≈ sum(chat_messages.cost_usd)` within $0.000001

**`integration_m5_1_chat_cost_cap_test.go`**:

`TestM5_1_CostCap_RefusesNextTurn`: seed session with total_cost_usd=$1.50 and cap=$1.00; INSERT operator message; assertions:

- No docker run happens (verifiable via mock dockerexec call count)
- Assistant row lands with status='failed', error_kind='session_cost_cap_reached'
- No `vault_access_log` row (vault fetch never attempted — refusal happens before)
- `event_outbox` has `work.chat.message_sent` but session is still active

`TestM5_1_CostCap_RaisedCapAllowsNext`: same setup but raise the cap env var and re-run; turn proceeds normally.

**`integration_m5_1_chat_idle_timeout_test.go`**:

`TestM5_1_IdleTimeout_SessionEnded`: set `GARRISON_CHAT_SESSION_IDLE_TIMEOUT=5s`; create session and one message; wait 7s; sweep ticks; session marked ended; INSERT new operator message on the ended session; assistant row → error_kind='session_ended'.

**`integration_m5_1_chat_vault_test.go`**:

`TestM5_1_VaultPath_AllErrorKinds`: parameterized over (token absent, token expired, vault 5xx); assertions verify error_kind + no container spawn + assistant row terminal write for each path.

### Chaos test

**`supervisor/chaos_m5_1_test.go`**:

`TestChaos_KillChatContainerMidStream`: start a chat turn against a slow-mock that emits 5 deltas over 10s; after 2s, `docker kill` the chat container externally; assertions:

- supervisor's `cmd.Wait()` returns within `ShutdownSignalGrace`
- `chat_messages` row terminal-writes with status='failed', error_kind='container_crashed'
- `chat_sessions.status` stays 'active' (the session is recoverable; only the turn fails)
- next operator message on the same session starts cleanly

`TestChaos_SupervisorSIGTERM_MidStream`: same setup but send the supervisor SIGTERM instead of killing the container directly; assertions:

- container is gone within 5s (process-group SIGTERM propagates from supervisor → docker CLI → docker daemon → container)
- `chat_messages` row → status='aborted', error_kind='supervisor_shutdown'
- `chat_sessions` → status='aborted'
- terminal write completed via WithoutCancel + TerminalWriteGrace
- supervisor shutdown returns exit code 0

### Dashboard tests

**`dashboard/lib/actions/chat.test.ts`** (Vitest, mocks DB):

- `startChatSession_RejectsEmptyContent`: empty string → throws `ChatError(EmptyContent)`.
- `startChatSession_RejectsOversizedContent`: >100KB → throws `ChatError(ContentTooLarge)`.
- `startChatSession_NoSession_ThrowsAuthError`: no better-auth session → throws `AuthError(NoSession)`.
- `sendChatMessage_EndedSession_ThrowsChatError`: target session has status='ended' → throws `ChatError(SessionEnded)`.
- `sendChatMessage_TurnIndexCollision_RetriesOnce`: simulate UNIQUE conflict on first INSERT; assert retry succeeds.

**`dashboard/lib/queries/chat.test.ts`** (Vitest):

- `listSessionsForUser_OrdersByStartedAtDesc`: seed 3 sessions; assertion.
- `getSessionWithMessages_ReturnsMessagesInTurnOrder`: seed multi-turn session; assertion.
- `getRunningCost_SumsCorrectly`: assertion.

### Playwright integration

**`dashboard/tests/integration/m5-1-chat-backend.spec.ts`**:

`m5.1 backend → SSE round-trip` (FR-102):

1. Boot dashboard standalone bundle + supervisor + Postgres + Infisical + mockclaude:m5 (all via existing `_harness.ts` extended with chat-image build step).
2. Seed OAuth token in Infisical via the M4 server action.
3. Open the SSE endpoint at `/api/sse/chat?session_id=<new>` *before* sending the message (so we don't miss deltas).
4. POST to a test-only route (or use the server action directly) that calls `startChatSession("hello")`.
5. Assert: SSE emits `delta` events within 5s, terminal `result` event arrives, `chat_messages` rows present (operator + assistant), `vault_access_log` row with chat metadata.
6. Send a second message via `sendChatMessage`; assert turn 2 round-trip.

### Regression check

After all M5.1 tasks land:

- `cd supervisor && go test ./...` — all M2.1/M2.2.x/M2.3 unit + integration tests still pass unchanged.
- `cd supervisor && go vet ./tools/vaultlog ./internal/chat/...` — vaultlog analyzer green on the chat code paths.
- `cd dashboard && bun run typecheck && bun test && bun run test:integration` — M3 + M4 + M5.1 Playwright suites all green; total runtime under 12 minutes per SC-010.
- `cd supervisor && go test -tags chaos ./...` — chaos suite (M2.1 SIGKILL + M2.2 mempalace + M5.1 chat) all pass.

---

## Phase 3 — Deployment changes

### `supervisor/docker-compose.yml`

Single addition to docker-proxy env (`CREATE: 1`); supervisor service env passthrough for new `GARRISON_CHAT_*` vars; no new compose services (the chat-image is built out-of-band via `make chat-image` and tagged locally before `compose up`).

### `supervisor/Dockerfile.claude` + `supervisor/Dockerfile.mockclaude.chat`

New files. Built via `make chat-image` and `make mockclaude-chat-image` Makefile targets respectively. Production deploy invokes both before `compose up -d`.

### `supervisor/Makefile`

```makefile
.PHONY: chat-image mockclaude-chat-image

chat-image:
	docker build -t garrison-claude:m5 -f Dockerfile.claude .

mockclaude-chat-image: ## Build the mock chat image for CI / local integration tests
	docker build -t garrison-mockclaude:m5 -f Dockerfile.mockclaude.chat .
```

### `docs/ops-checklist.md`

New section "M5.1 — chat backend" covering:

- Seeding `CLAUDE_CODE_OAUTH_TOKEN` via the M4 vault create flow at path `/<customer_id>/operator/CLAUDE_CODE_OAUTH_TOKEN`
- Setting `GARRISON_CHAT_*` env vars in Coolify (defaults documented + when to override)
- Verifying docker-proxy `CREATE: 1` flag landed (`docker inspect garrison-docker-proxy | jq '.[0].Config.Env'`)
- Image build + tag procedure (`make chat-image`); deploy-time SHA pin via `--build-arg`
- Mockclaude image for integration / dev (`make mockclaude-chat-image`)
- Token-rotation procedure: edit via `/vault/edit/.../CLAUDE_CODE_OAUTH_TOKEN`; takes effect on next chat-message spawn; no supervisor restart needed (FR-005)

---

## Phase 4 — Risks and rollback

### Risks

1. **Anthropic API outage during a chat turn** → assistant turn → `error_kind='claude_runtime_error'`. Per-message retry is operator-driven (operator re-sends); no automatic retry in M5.1. The session stays active.
2. **OAuth token revocation** → assistant turn → `error_kind='token_expired'` (Infisical returns 401-equivalent on revealed-but-revoked tokens) or `error_kind='claude_runtime_error'` (Anthropic side detects). Operator re-seeds the token via M4 vault edit; next turn proceeds.
3. **Docker daemon outage** → `error_kind='docker_proxy_unreachable'`. Operator-actionable: check `garrison-docker-proxy` health.
4. **Cost-cap stampede** (operator types many messages quickly, each refusing because cap reached) → no automatic recovery; operator raises cap or starts new session. Acceptable as written; cap is meant to be a soft fence.
5. **Container leak on supervisor crash** — possible if SIGTERM doesn't propagate cleanly. Restart sweep (FR-083) catches the row-state side; container-side cleanup relies on the docker daemon's `--rm` flag, which fires on container exit even if the supervisor is gone. A leftover-container chaos test is *not* in scope for M5.1 — `--rm` plus daemon-side cleanup is the existing M2.x posture for mempalace containers.
6. **Anthropic prompt-cache miss** (model behavior change, account-level cache disable) → multi-turn cost rises by the prefix's input-token cost on every turn. Soft cap catches runaway cost; operator notices in the first $1 of usage. No M5.1 mitigation needed.
7. **MCP server boot timing** — postgres MCP starts inside the chat container's claude subprocess; takes ~100ms. The `OnInit` health check is the gate. Acceptable.

### Rollback

The migration's down-direction drops both new tables (preserving FK order: chat_messages first, then chat_sessions). Dashboard schema introspection is regenerated post-rollback. The supervisor binary is rolled back to pre-M5.1 via the existing Coolify image revert. The docker-proxy `CREATE: 1` flag remains harmless if no chat code uses it; alternatively, revert the compose file. The `garrison-claude:m5` image is left in place (cheap; future M5.1 retry uses it).

---

## Phase 5 — Open work (post-plan, pre-tasks)

This plan answers every structural decision the spec / context / clarify left open. The following are explicitly *not* decided here (they belong to `/speckit.tasks` or downstream):

- Per-task ordering and dependencies — `/speckit.tasks` derives them from this plan.
- Exact sqlc query SQL files (`migrations/queries/chat.sql`) — the task that wires `internal/chat/persistence.go` writes the queries; signatures are what's specified above.
- Specific mockclaude:m5 NDJSON template content — the task that builds the image authors them to satisfy the integration tests.
- `messages/en.json` chat error-vocabulary keys — the task that ships error-surface translations seeds them; M5.2 adds the rendered strings.
- Retro deliverables (`docs/retros/m5-1.md` + MemPalace mirror) — the final task per the M3-onwards retro policy.

---

## Constitution Check (post-design)

Re-checked after Phase 1 design. No new violations introduced. The Phase 1 design honors every principle Phase 0 already cleared. Specifically:

- **Principle I**: every event-bus channel name is named explicitly (FR-050, 051, 070, 071); supervisor-driven pg_notify in commit transactions; no triggers; no parallel state stores.
- **Principle III**: ephemeral container per turn; no `--resume`; transcript replay from Postgres rows on every turn.
- **Principle VII**: zero new Go deps; chat container is a runtime artefact, not a supervisor-side import.
- **Principle VIII**: every new goroutine accepts a context; subprocess uses CommandContext + Setpgid + process-group SIGTERM; pipes drained before Wait; terminal writes via WithoutCancel.

**Constitutional violations to track**: zero. Plan is ready for `/speckit.tasks`.

---

## Complexity tracking

> No constitutional violations to justify. Section intentionally empty.
