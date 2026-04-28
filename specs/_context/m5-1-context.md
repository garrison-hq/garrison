# M5.1 — CEO chat backend (read-only)

**Status**: Context for `/speckit.specify`. Backend half of M5; M5.2 will add the dashboard surface.
**Prior milestone**: M4 (operator dashboard mutations) merged on main as of `79f0e38`.
**Branch (research)**: `research/m5-spike` carries the runtime-mechanics findings the spec cites by section.

**Binding inputs** (read first, in order):
1. `docs/research/m5-spike.md` — runtime-mechanics spike. Six sections (existing spawn ground truth, OAuth injection result, container shape comparison, MCP surface needs, streaming + transport, threat-model sketch) plus ten open questions. Sections cited by number throughout this context.
2. `RATIONALE.md` lines 117-130 — "summon-per-message" framing. Commits the M5 process model to ephemeral-per-turn with cold-start cost as an accepted trade-off; long-lived containers are a *future mitigation*, not a starting point.
3. `ARCHITECTURE.md:574` — "M5 — CEO chat (summoned, read-only). Conversation panel, summon-per-message pattern, Q&A only. CEO can query state and the palace but cannot create tickets yet." This context honors that scope; mutations are deferred to M5.3 or later, post a separate architecture amendment.
4. `ARCHITECTURE.md:69` — "Not a daemon. When you send a message in the CEO chat: ..." (the supervisor summons Claude per-message, runs it to completion, tears down).
5. `ARCHITECTURE.md:207` — `tickets.origin TEXT NOT NULL, -- 'ui', 'ceo_chat', 'agent_spawned'`. Note: the `ceo_chat` value is committed in the schema for future use; M5.1 doesn't write tickets, but the column already anticipates the M5.3 path.
6. **AGENTS.md** §"Activate before writing code" for M5, §Concurrency rules (especially rule 6: SIGTERM-without-cancel for terminal writes; rule 7: process-group lifecycle), §"What agents should not do", and the locked dependency list.
7. **M2.1 retro** (`docs/retros/m2-1.md`) — Claude Code subprocess invocation patterns, stream-json parsing, finalize state machine. M5.1's spawn extends this directly.
8. **M2.2 retro** + `internal/mempalace/` — MemPalace as the chat's source of long-term context (palace contents the CEO queries against).
9. **M2.3 retro** + `docs/security/vault-threat-model.md` — vault threat model. M5.1 reads palace contents *which may transitively contain redacted secret references*; Rule 6 ("audit everything, log no values") applies to chat audit rows the same way it applies to `vault_access_log`.
10. **M3 retro** (`docs/retros/m3.md`) — SSE pattern (`/api/sse/activity`) the M5.1 chat transport will mirror.
11. **M4 retro** (`docs/retros/m4.md`) — operator-initiated mutation patterns, audit row shape, Path A coverage tooling. M5.1 doesn't add mutations but inherits the audit + cost-telemetry idioms.

If this context contradicts any of the above, the binding inputs win.

---

## Why this milestone now

Garrison's mutation paths (M4) and read paths (M3) ship as dashboard-driven surfaces — the operator clicks something, a server action runs, a row writes. The CEO chat is conceptually different: the operator types a question in natural language, a Claude Code subprocess decides what to query, and the answer streams back. None of the existing surfaces match that shape. The chat is a new runtime — Claude-Code-as-subprocess gated on the operator's `CLAUDE_CODE_OAUTH_TOKEN` rather than per-agent vault credentials, summoned per message instead of running as a daemon, talking to the dashboard through SSE rather than server actions. The spike confirmed all the pieces work in isolation; the milestone wires them into a supervisor-resident runtime the dashboard can call.

Splitting M5 into M5.1 (backend) + M5.2 (frontend) follows the M3/M4 pattern of not letting cross-stack milestones get too big, and it lets us validate the chat runtime end-to-end via integration tests (Playwright + supervisor + Claude container + Postgres + MemPalace) before committing dashboard chrome that would have to be reworked if the runtime shape changes.

The "read-only" scope (per `ARCHITECTURE.md:574`) is deliberate — chat-driven mutations introduce a prompt-injection blast radius the existing threat models don't cover (spike §6). M5.3 or later picks up mutations behind a separate threat-model amendment.

---

## In scope

### Chat runtime: summoned-per-message Claude Code subprocesses

The supervisor exposes a "send message" entry point. On every operator message:
- compose the per-session MCP config (postgres-RO + mempalace, no mutation tools);
- start a fresh Docker container (`garrison-claude:m5` image, pre-baked with claude 2.1.86 on `node:22-slim`) via the existing `garrison-docker-proxy`;
- inject the operator's `CLAUDE_CODE_OAUTH_TOKEN` as an env var at spawn time (never logged, never persisted to Postgres or MemPalace, never echoed in stream output);
- exec `claude -p "<message>" --output-format stream-json --include-partial-messages --mcp-config <per-session> --resume <session-id-if-not-first-turn>`;
- pipe stdout (NDJSON) through the supervisor's stream-json parser;
- on `result` event, persist the message + cost roll-up, kill the container.

Per the spike (§3), ephemeral-per-turn matches the existing `Spawn()` lifecycle (`exec.CommandContext` → `cmd.Wait()` → cleanup) most closely; the ~1s latency cost vs. long-lived is invisible next to the 2-3s inference time. The summon-per-message pattern is committed by `RATIONALE.md:127`.

### MCP tool surface: postgres-RO + mempalace, nothing new

Reuse the existing `postgres` and `mempalace` MCP servers (`internal/pgmcp/`, `internal/mempalace/dockerexec.go`). The chat needs to read tickets, ticket transitions, agent instances, the audit log, and palace contents to answer status questions. No new MCP server; no new tools. Mutation MCP server (`garrison-mutate`) is explicitly deferred (M5.3 / later).

The `finalize` MCP server (`internal/finalize/`) is **not** mounted for chat. Chat doesn't have a `finalize_ticket`-shaped terminal event; it ends when Claude emits `result.subtype: success`. The pipeline parser's adjudicate policy needs a new chat-flavoured branch that doesn't require finalize.

### Stream-json parser reuse

The M2.1 pipeline parser (`internal/spawn/pipeline.go:Run()`) is extensible: it dispatches `init`, `assistant`, `user`, `rate_limit_event`, `result`, `task_started`, `unknown` to handler callbacks. M5.1 introduces a `ChatPolicy` that supplies different handlers — token-level deltas (`text_delta` events from `--include-partial-messages`) flow to the SSE bridge as they arrive instead of being buffered into a `Result`. The parser stays one piece of code; the policy encapsulates what's chat-specific.

### Conversation state via `claude --resume <session-id>`

Spike §5 confirmed every stream-json event carries a `session_id`. The Claude Code binary supports `--resume <session-id>` to continue a prior conversation from the same session store. With summon-per-message + ephemeral containers, the session store has to live somewhere durable across container instances. The spec resolves *which* somewhere (the open question in §7 below); the supervisor's job is to wire it.

### Supervisor → dashboard transport: SSE endpoint

A new endpoint on the supervisor (or its HTTP layer — the spec confirms the surface) emits parsed chat events as SSE. The shape mirrors M3's `/api/sse/activity`: long-lived HTTP connection, `event:` + `data:` frames, automatic reconnect via `Last-Event-ID`. M5.2 will consume; M5.1 ships the producer.

### Schema: `chat_sessions` + `chat_messages` tables

The supervisor's Postgres gains two tables. `chat_sessions` carries one row per CEO session (operator id, started_at, ended_at, total_cost_usd, claude_session_id, status); `chat_messages` carries one row per message turn (session_id FK, role 'operator'|'assistant', content TEXT, tokens_input/output, cost_usd, created_at, raw_event_envelope JSONB). Drizzle-pulled into the dashboard for read access via `garrison_dashboard_app`.

The exact column set is spec territory; the context's claim is only that the persistence layer lives in supervisor-owned tables (consistent with M2.x ticketing data) rather than dashboard-owned (better-auth-style users/sessions tables stay where they are).

### Cost telemetry per session

Spike §2 confirmed `total_cost_usd` is in every `result` event. The supervisor rolls cost up to the session level (`chat_sessions.total_cost_usd`) and per-message (`chat_messages.cost_usd`). Mirrors `agent_instances.total_cost_usd` on the agent side. No cap in M5.1 — the spec confirms whether a session-level cap ships or is deferred (open question §7).

### Audit trail: pg_notify + chat_messages

Every chat session writes a `work.chat.session_started` and `work.chat.session_ended` pg_notify; every message turn writes a `work.chat.message_sent` notify. M3's activity feed should pick these up unchanged (channel-name is the only contract).

Rule 6 applies: chat audit rows record session id, operator id, message timestamps, cost — never message *content*. Message content lives in `chat_messages.content` for operator-side review but is excluded from the activity feed payload (the audit row holds an opaque message_id reference).

### docker-proxy allow-list amendment

The current allow-list is `POST=1, EXEC=1, CONTAINERS=1` (`docker-compose.yml:60-73`). Spawning chat containers requires `POST /containers/create` (= `CREATE` flag in linuxserver/socket-proxy). M5.1 adds the `CREATE=1` allow flag. The amendment lands as part of the supervisor's compose changes, NOT as a separate ops PR — keeps the deploy-time dependency atomic with the runtime that needs it.

### CLI / supervisor-side surfaces

The supervisor gains a new `mcp chat-runtime` (or analogous) subcommand path — exact name in the spec — that the chat container's claude subprocess does NOT call (chat doesn't get its own MCP tool); rather, the supervisor's main process owns the spawn, parser, SSE bridge, and persistence. Existing supervisor binary, additive routes only.

### Test coverage

The Playwright integration suite already runs supervisor-driven tests against testcontainers (M2.x integration tests). M5.1's chat runtime integration test boots Postgres + MemPalace + the supervisor + a `garrison-claude:m5` testcontainer with a stub `CLAUDE_CODE_OAUTH_TOKEN`, sends a synthetic message, asserts the SSE stream emits the expected events and the `chat_messages` row lands. Cost cap testing optional in M5.1 if the spec defers the cap itself.

The supervisor's existing chaos test pattern (`test (chaos)` CI job) gains a chat-flavored case: kill the chat container mid-stream, assert the supervisor cleans up and writes a `session_ended` row with status `aborted`. Mirrors the M2.1 SIGKILL-mid-spawn case.

---

## Out of scope

### Mutations (deferred per `ARCHITECTURE.md:574`)

CEO cannot create tickets, edit agents, rotate secrets, or trigger any server-action-equivalent flow through the chat in M5.1 (or M5.2). The chat is Q&A only. M5.3 (or wherever the architecture amendment ends up) takes mutations.

The `tickets.origin = 'ceo_chat'` column value is in the M2.x schema for future use; M5.1 never writes that value.

### Dashboard chat UI

The conversation panel, message rendering, SSE consumer wiring, typed-name confirmation gates (when mutations land later), kbd shortcuts, etc. — all M5.2.

### Long-lived chat containers

`RATIONALE.md:127` commits to summon-per-message. If cold-start latency becomes painful in practice, the *first* mitigation considered is "keep the chat process warm for N minutes after the last message". M5.1 ships ephemeral; the warm-pool optimisation is a follow-up if needed.

### Multi-operator / multi-CEO

Garrison is single-tenant single-operator (per `RATIONALE.md` and the M3 setup wizard's single-account model). Per-operator OAuth token plumbing, per-CEO session isolation, role-based chat access — all post-M5.

### OAuth token auto-refresh

The token in `CLAUDE_CODE_OAUTH_TOKEN` is the operator's claude.ai access token. When it expires, M5.1's response is fail-loud (the supervisor returns a 401-shaped chat error to the dashboard, the operator sees a "rotate token" message, an ops-checklist entry tells them how). Auto-refresh via a refresh token is post-M5.

### Mutation MCP server (`garrison-mutate`)

Sketched in spike §4 as the recommended design when mutations land later. Out of scope for M5.1 because the milestone is read-only.

### Chat history search

Operator-side cross-session search ("when did the CEO last ask about engineer concurrency?") is post-M5. The schema persists everything; the search surface comes later.

### Threat-model amendment

The chat introduces a new attack surface (spike §6) but the *read-only* shape limits the blast radius significantly — palace-injected text can't trigger mutations because there are no mutation tools. The amendment lands when M5.3 ships chat-driven mutations.

---

## Open questions the spec must resolve

These are decisions deliberately left for `/speckit.specify` + `/speckit.clarify` to make. Most carry over from spike §7.

1. **Where does the operator's `CLAUDE_CODE_OAUTH_TOKEN` live at deploy time?** Coolify env vars (mirrors `GARRISON_INFISICAL_*`)? Infisical itself (with the same single-tenant assumption)? Both? The spec picks. The token never lives in committed env files.

2. **Where does the Claude session store live across summon-per-message turns?** The `--resume <session-id>` primitive needs Claude's session files at `~/.claude/projects/<session>/` to be readable on each summon. Three plausible designs: (a) named volume mounted into every chat container for the same session, garbage-collected on session end; (b) volume *exported* between containers (start a fresh container with the prior container's session-state tarball restored); (c) skip `--resume`, pass the full transcript on each turn (lossy for token usage, robust against state mismatch). Spike §5 leaned toward (a) but didn't experimentally confirm; spec resolves.

3. **MCP config persistence shape.** Currently per-spawn MCP config is written to a tmp file and cleaned up (`internal/mcpconfig/mcpconfig.go`). Chat reuses the per-message lifecycle but the config is identical across messages of the same session — write-once-per-session vs. write-per-message. Spec picks.

4. **`chat_sessions` + `chat_messages` exact schema.** Column types, indexes, FK shape, PK strategy (UUID? incrementing?), nullable columns, retention/archival policy. Context only commits to "supervisor-owned tables Drizzle-pulled into dashboard"; spec writes the migration.

5. **Supervisor entry-point for sending a message.** HTTP endpoint? gRPC? pg_notify-driven (the way ticket spawns work)? The dashboard needs *some* way to say "operator sent this string"; spec picks the transport. Note that the dashboard already has Postgres write access (`garrison_dashboard_app` per M3/M4); routing chat messages through `chat_messages.INSERT` + a `pg_notify('chat.message.sent', ...)` watched by the supervisor would mirror the M2.1+ event-bus shape and avoid introducing a new RPC primitive.

6. **SSE endpoint location: dashboard or supervisor?** The dashboard already exposes `/api/sse/activity`. Adding `/api/sse/chat?session_id=...` there means the dashboard relays from a supervisor-owned source (HTTP? Postgres LISTEN? Redis pubsub?). Alternative: supervisor exposes its own SSE port the dashboard reverse-proxies. Spec picks; the M3 activity-feed precedent is a strong nudge toward the dashboard owning the SSE surface.

7. **Cost cap shape.** Per-session cap, per-day cap, both, none-in-M5.1? Operator-facing or hard-stop? Spec picks; the spike noted that the per-spawn `--max-budget-usd` model doesn't fit multi-turn shape cleanly.

8. **Chat-flavoured stream parser policy.** Does the existing pipeline parser get a new `Adjudicate` branch (one parser, two policies), or does chat get its own parser? Decoupling cost vs. duplication trade-off. Spec picks.

9. **Per-message timeout / abort semantics.** Operator typing "stop" mid-stream, supervisor SIGTERM mid-stream, container OOM, network blip from the chat container to the docker-proxy — each is a distinct failure mode. Spec enumerates and picks the response per case.

10. **Persistence policy for raw stream events.** `chat_messages.raw_event_envelope JSONB` retention: keep forever (debug + audit), trim to N days, or summary-only? Spec picks; cost vs. forensic-value trade-off.

11. **Chat session lifecycle: explicit close vs idle timeout.** When does a chat session end? Operator clicks "end chat", supervisor sees N minutes of silence, the dashboard tab closes, or never (sessions accumulate)? Spec picks.

12. **Test fixture for `CLAUDE_CODE_OAUTH_TOKEN` in CI.** Real OAuth token can't go to CI (single-operator's claude.ai bill). Options: a stub `claude` binary that emits canned NDJSON without real inference, a recorded-replay layer over the real binary, or the same path the supervisor uses (`mockclaude` from M2.x). Spec picks.

---

## Acceptance criteria framing

The spec writes the full SC list. Framing for the spec to fill in:

- A live operator can send a message via the supervisor's entry point (whatever the spec picks per §5) and receive the assistant's streaming response within `<bound>` seconds, end-to-end, against a real `garrison-claude:m5` testcontainer with a real (test-account) `CLAUDE_CODE_OAUTH_TOKEN`.
- Multi-turn conversation maintains context — turn N can reference what was said in turn N-1 — via the resolved session-state design.
- The supervisor's chat audit trail records session-level metadata (no message content) on every turn; the activity feed surfaces session-started / message-sent / session-ended events using the existing M3 SSE infrastructure.
- Cost rolls up correctly into `chat_sessions.total_cost_usd` after a multi-turn session ends.
- Operator's OAuth token is never written to logs, audit rows, MemPalace, or any persistence tier.
- Supervisor SIGTERM mid-chat-stream cleans up the container, writes a session-ended row with status='aborted', and propagates a 5xx-shaped event on the SSE stream so the dashboard can surface it (in M5.2).
- The chat does *not* — and *cannot* — call any mutation tool. Verifiable: the per-session MCP config has no `garrison-mutate` server entry, full stop.
- Supervisor unit + integration tests cover the chat-flavoured parser policy, summon-per-message lifecycle, OAuth-token-stripping audit shape, and session-state across turns.
- New supervisor migrations apply cleanly through `goose up` + the dashboard's `bun run drizzle:pull` regenerates `schema.supervisor.ts` with `chat_sessions` + `chat_messages` types.

---

## What this milestone is NOT

- Not a chat UI. Dashboard surface is M5.2.
- Not a mutation surface. CEO cannot edit Garrison state via chat in this milestone.
- Not a multi-operator system. Single CEO, single OAuth token.
- Not an OAuth-refresh integration. Tokens that expire fail loud.
- Not a long-running daemon. Summon-per-message is canon (`RATIONALE.md:127`).
- Not a threat-model amendment. Read-only scope keeps the existing models valid; the amendment lands when mutations do.
- Not a chat-history-search feature. Persistence-only.
- Not a multi-CEO coordination layer (CEOs paginating each other, parallel sessions, etc.).
- Not a TUI / standalone CLI. Chat is operator-driven, dashboard-mediated.

---

## Spec-kit flow

1. **Now**: `/speckit.specify` against this context. Spec resolves the ten open questions inline (or flags the ones genuinely needing a clarify round).
2. **Then**: `/speckit.clarify` only if the spec leaves residual ambiguity (it shouldn't if the spec adopts spike §3's recommendation for ephemeral-per-turn and spike §5's recommendation for `--resume`-with-volume-mount).
3. **Then**: `/speckit.plan` — picks the directory layout for `internal/chat/`, the parser policy abstraction, the schema migration, the docker-proxy allow-list change, the SSE endpoint, the testcontainer harness for the chat container.
4. **Then**: `/speckit.tasks` — turns the plan into ordered tasks with completion conditions.
5. **Then**: `/speckit.analyze` — checks for spec/plan/tasks consistency.
6. **Then**: `/garrison-implement` (or `/speckit.implement`) — task-by-task execution.
7. **Then**: M5.1 retro at `docs/retros/m5-1.md`, MemPalace mirror, and the M5.2 context can start (frontend reads against the SSE surface this milestone produces).
