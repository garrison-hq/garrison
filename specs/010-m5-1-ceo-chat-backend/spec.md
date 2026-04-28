# Feature Specification: M5.1 — CEO chat backend (read-only)

**Feature Branch**: `010-m5-1-ceo-chat-backend`
**Created**: 2026-04-28
**Status**: Draft
**Input**: M5.1 context (`specs/_context/m5-1-context.md`) + chat-runtime spike (`docs/research/m5-spike.md`).
**Scope marker**: backend half of M5. M5.2 ships the dashboard chat surface; M5.3 (or wherever the architecture amendment lands) takes chat-driven mutations.

## Clarifications

### Session 2026-04-28

- Q: Should `chat_messages` carry an explicit `status` column, and if so, scoped to which roles? → A: Add `status` TEXT enum-checked `pending|streaming|completed|failed|aborted` applied uniformly to both roles. Operator rows are INSERTed at `completed`; assistant rows transition `pending → streaming → completed|failed|aborted`.
- Q: Who assigns `chat_messages.turn_index` and is `(session_id, turn_index)` UNIQUE? → A: Dashboard assigns operator turn_index inside the INSERT transaction as `COALESCE(MAX(turn_index), -1) + 1`; supervisor assigns assistant turn_index as `operator_row.turn_index + 1` inside its commit transaction. `UNIQUE(session_id, turn_index)` enforced.
- Q: Who creates the `chat_sessions` row on a fresh session? → A: Dashboard server action creates the `chat_sessions` row (status='active', total_cost_usd=0) and the first `chat_messages` row (role='operator', turn_index=0) inside one transaction; subsequent messages INSERT only into chat_messages. The supervisor never lazy-creates session rows.
- Q: How are streaming token deltas persisted and transported during an in-flight assistant turn? → A: Deltas are NOT persisted during streaming. The supervisor calls `pg_notify('chat.assistant.delta', json_build_object('message_id', $1, 'seq', $2, 'delta_text', $3))` per batch. The SSE bridge LISTENs and forwards the payload directly. Only the final assembled `content` and full `raw_event_envelope` land in `chat_messages` at terminal commit. Mid-stream SSE reconnect can pick up subsequent deltas or the terminal event, but missed mid-stream deltas are not replayable; FR-052's `Last-Event-ID` semantics narrow to terminal events only.
- Q: What heuristic determines whether the soft cost cap (`GARRISON_CHAT_SESSION_COST_CAP_USD`) refuses the next turn? → A: Purely reactive — refuse if `chat_sessions.total_cost_usd >= GARRISON_CHAT_SESSION_COST_CAP_USD`. No projection, no buffer, no per-turn estimate. Worst-case overshoot is bounded by one turn's cost (the turn that nudges the running total over the cap completes; the *next* turn is refused).

## User Scenarios & Testing

### User Story 1 — Single-turn question against current state (Priority: P1)

The operator sits down at the dashboard, opens a chat surface (whatever shape M5.2 ends up giving it), and sends one message: *"how many tickets are stuck in qa-review for engineering?"* The backend pulls the operator's `CLAUDE_CODE_OAUTH_TOKEN` from the vault, spawns a Claude Code container with read-only access to Postgres + MemPalace, streams the assistant's tokens back through SSE as they arrive, persists the full exchange in `chat_messages`, rolls cost up to the session, and tears the container down.

**Why this priority**: this is the load-bearing slice. Every other story builds on it. If single-turn doesn't work end-to-end, multi-turn, audit, and cost-telemetry can't land either. P1 ships an MVP — the operator can ask one question per session and get an honest answer.

**Independent test**: with a real `CLAUDE_CODE_OAUTH_TOKEN` seeded into the local Infisical via `/vault/new`, INSERT a row into `chat_messages` (role='operator') via the dashboard's app-role connection. Subscribe to the SSE endpoint. Assert: the SSE stream emits `text_delta` events within 5 seconds of the INSERT, the `result` event arrives, the corresponding `chat_messages` row (role='assistant') lands with non-empty content, `chat_sessions.total_cost_usd` is non-zero, and the `vault_access_log` table grew by exactly one row with `outcome='value_revealed'` and `metadata.actor='supervisor_chat'`.

**Acceptance scenarios**:
1. **Given** a fresh chat session, **When** the operator INSERTs an `chat_messages` row with role='operator' and content="ping", **Then** within 5s a corresponding role='assistant' row lands with non-empty content and `cost_usd > 0`.
2. **Given** the chat container is in flight, **When** the SSE consumer attaches via `Last-Event-ID`, **Then** it receives the `text_delta` events from where it left off without duplicates.
3. **Given** the vault contains no `CLAUDE_CODE_OAUTH_TOKEN` entry, **When** the operator INSERTs a message, **Then** the supervisor writes a role='assistant' row with `error_kind='token_not_found'` and the SSE emits a typed error event — no claude container is started.

---

### User Story 2 — Multi-turn conversation with stateless turn replay (Priority: P1)

The operator follows up: *"and which agent has been working on those?"* — without restating context. The chat backend reconstructs the full transcript from `chat_messages` ordered by `(session_id, turn_index)`, builds a synthetic prompt that prepends the prior turns, and spawns a fresh container. Claude sees the conversation as one continuous thread; Anthropic's prompt cache amortises the input-token cost across turns. No `claude --resume` session file, no mounted state — the supervisor's only state-of-truth is Postgres (Constitution III).

**Why this priority**: also P1 because single-turn alone isn't useful — operators ask follow-ups. Without multi-turn, the chat is just a one-shot query box. The constitutional decision (Q2 in the context) makes this a pure function of the persistence layer, not of Claude's local session state, so the test is straightforward.

**Independent test**: send turn 1 ("my favourite color is purple"), wait for the assistant row. Send turn 2 ("what is my favourite color?"). Assert: the assistant's response references "purple". Verify the second container's input-token cost reflects prompt-cache hits (`cache_read_input_tokens > 0` in the raw_event_envelope of the result event).

**Acceptance scenarios**:
1. **Given** turn 1 is committed, **When** the operator sends turn 2 referencing turn 1's content, **Then** the assistant's response demonstrates context awareness (asserted via test fixture matching).
2. **Given** a session with N turns, **When** the supervisor builds the spawn prompt, **Then** the container's stdin / argv carries the full transcript in turn order; no `--resume` flag is set.
3. **Given** the supervisor crashes between the operator's INSERT and the spawn, **When** the supervisor restarts, **Then** the in-flight message is detected (status='pending' on supervisor boot) and either re-spawned or marked `aborted` (spec implementation choice; test asserts a non-pending terminal state within 30s of restart).

---

### User Story 3 — Cost telemetry rolls up per session (Priority: P2)

After 10 turns of conversation, the operator wants to know what the chat cost. Each turn's `result` event carries `total_cost_usd`; the supervisor sums into `chat_sessions.total_cost_usd` on every assistant turn commit. A session-level soft cap (`GARRISON_CHAT_SESSION_COST_CAP_USD`, default $1.00) refuses the next turn if the running total exceeds it.

**Why this priority**: P2 because the chat works without it (P1) but the operator's claude.ai bill is real money — without a visible cost number, runaway sessions or injection attacks could burn through the operator's allowance silently.

**Independent test**: drive a session through N turns, verify `chat_sessions.total_cost_usd ≈ Σ chat_messages.cost_usd` after each turn (within floating-point tolerance). Set `GARRISON_CHAT_SESSION_COST_CAP_USD=0.001`, send a turn, assert the response is refused with `error_kind='session_cost_cap_reached'` and no container starts.

**Acceptance scenarios**:
1. **Given** a session with three completed assistant turns, **When** the test queries `chat_sessions.total_cost_usd`, **Then** it equals the sum of the three corresponding `chat_messages.cost_usd` values within $0.000001.
2. **Given** the soft cap is set lower than the next turn's projected cost, **When** the operator INSERTs the next operator-message row, **Then** the supervisor refuses to spawn and writes an assistant row with `error_kind='session_cost_cap_reached'`.
3. **Given** the cap is hit mid-session, **When** the operator subsequently starts a *new* session, **Then** the new session has its own (zero) cost counter and is allowed to run.

---

### User Story 4 — Audit + activity-feed surface for chat sessions (Priority: P2)

Every chat session emits `pg_notify` events that the M3 activity feed already knows how to render: `work.chat.session_started`, `work.chat.message_sent`, `work.chat.session_ended`. Each event carries an opaque message_id reference; message *content* never appears in the audit row payload (Rule 6 from the vault threat model applies — palace contents may transitively contain secret-shaped strings, so chat content is treated like vault values for audit purposes).

**Why this priority**: P2 because the chat works without it but the activity feed is the operator's existing single-pane-of-glass for "what's happening in Garrison right now"; chat events not appearing there would be a noticeable gap.

**Independent test**: drive a single-turn session, query `event_outbox` for channels matching `work.chat.*`, assert exactly one `session_started`, one `message_sent`, one `session_ended` row landed. Inspect the payloads — no `content`, no `text` substring of the assistant's response, no `CLAUDE_CODE_OAUTH_TOKEN` substring anywhere.

**Acceptance scenarios**:
1. **Given** a chat session runs to completion, **When** the test queries `event_outbox` filtered to chat channels, **Then** the three expected events land in order, each carrying `chat_session_id` and (for `message_sent`) `chat_message_id` only.
2. **Given** the activity feed's M3 SSE consumer is connected, **When** a chat session starts, **Then** the consumer receives the `work.chat.session_started` frame within 1s.
3. **Given** a chat message body contains a value-shaped string (e.g. `sk-fake-test`), **When** the audit row is inspected post-test, **Then** the string is absent from `event_outbox.payload`. Defensive backstop on top of the architectural rule.

---

### User Story 5 — SIGTERM cleanup mid-stream (Priority: P3)

The operator runs `docker compose down`, the supervisor receives SIGTERM mid-chat-stream. The spawned chat container is killed via process-group SIGTERM (AGENTS.md rule 7), the in-flight `chat_messages` row gets marked `aborted` via a `context.WithoutCancel`-bracketed terminal write (AGENTS.md rule 6), the session is marked `aborted`, the SSE stream emits a typed error event before the connection closes.

**Why this priority**: P3 because graceful shutdown is operationally important but not user-visible during normal operation. M2.x already implements the rule-6/rule-7 idioms; M5.1 reuses them.

**Independent test**: start a chat turn (long-running prompt that takes >5s of inference). After 1s, send SIGTERM to the supervisor. Assert: container is gone within 5s, `chat_messages` row is `aborted`, `chat_sessions` is `aborted`, an error frame appeared on the SSE stream before the connection closed.

**Acceptance scenarios**:
1. **Given** an in-flight chat turn, **When** the supervisor receives SIGTERM, **Then** the container is signalled SIGTERM-then-SIGKILL within `ShutdownSignalGrace` (5s), the `chat_messages` row terminal write completes via `context.WithoutCancel`, and the session is marked `aborted`.
2. **Given** the operator hits a "stop" UI control mid-stream (M5.2 surface, simulated here by INSERTing a row with `role='operator', is_stop_signal=true`), **When** the supervisor sees the signal, **Then** the in-flight container is killed and the message marked `aborted` — same termination path as supervisor SIGTERM.
3. **Given** the supervisor restarts after a crash with `active` sessions in the DB, **When** the supervisor's chat subsystem boots, **Then** any session whose `chat_messages` has a `pending` operator-message older than 60s is marked `aborted`.

---

### Edge Cases

- **Vault returns 401 (token expired)**: supervisor writes assistant row with `error_kind='token_expired'`, SSE emits typed error, ops-checklist instructs operator to rotate via `/vault/edit`. Vault grant on the chat token is `manual_paste` rotation provider; no auto-refresh.
- **Vault returns 5xx (Infisical down)**: assistant row with `error_kind='vault_unavailable'`, retry once with 500ms backoff before failing the turn. Mirrors M2.3 vault-down behaviour for agent spawns.
- **Infisical secret exists but is empty**: treated as `error_kind='token_not_found'` (same as missing entry).
- **Container OOM / SIGKILL by Docker daemon**: `cmd.Wait()` returns non-zero exit, message marked `failed` with `error_kind='container_crashed'` and the exit code in metadata.
- **Network failure between supervisor and docker-proxy**: same `container_crashed` path, `error_kind='docker_proxy_unreachable'`.
- **MCP server fails health check** (postgres-RO or mempalace `status != "connected"` in the `system/init` event): supervisor kills the chat container immediately, message marked `failed` with `error_kind='mcp_<server>_<status>'` (mirrors M2.1 behaviour).
- **Operator's claude.ai allowance exhausted** (`rate_limit_event` with `overageStatus='rejected'`): supervisor lets the current turn complete if a `result` event still arrives, marks subsequent turns as `error_kind='rate_limit_exhausted'` until the rate-limit window resets.
- **Stream parses successfully but `result.is_error=true`**: message marked `failed`, raw envelope retained, `error_kind='claude_runtime_error'`.
- **Turn exceeds `GARRISON_CHAT_TURN_TIMEOUT` (5min default)**: supervisor SIGTERMs the container, message marked `aborted` with `error_kind='turn_timeout'`.
- **Session exceeds `GARRISON_CHAT_SESSION_IDLE_TIMEOUT` (30min default)**: session marked `ended`; new operator messages on that session are rejected with `error_kind='session_ended'` and the dashboard is expected to start a new session.
- **Soft cost cap reached**: described in User Story 3.
- **Two operators send a message simultaneously to the same session** (single-CEO assumption violated): not protected against in M5.1; per-session serial processing handled by supervisor's chat-message worker, but no advisory lock. Documented as known property; multi-operator concurrency is post-M5.
- **Supervisor crashes between operator INSERT and assistant `pending` row creation**: the operator's `chat_messages` row sits committed at `status='completed'` (operator rows always commit at INSERT-time per clarify Q1) with no assistant counterpart. The restart sweep (FR-083) does NOT catch this case because it filters on `status IN ('pending','streaming')` and the operator row is `'completed'`. The next operator message on the session works correctly (turn_index assignment is monotonic on max+1), but the orphan operator row never gets a response. **M5.1 accepts this as a known property** — operator can re-send manually. A polish-round mitigation (sweep also detects "session whose newest message is `role='operator'` with no assistant pair, > 60s old" and either re-emits `chat.message.sent` or marks the session aborted) is out of scope for M5.1.
- **Palace returns text containing what looks like a tool-call instruction**: M5.1 read-only scope mitigates blast radius — no mutation tools mounted, so the worst case is misleading text in the assistant's response. Threat-model amendment for mutation-bearing chat lands with M5.3.

---

## Requirements

### Functional requirements

#### Token + vault

- **FR-001**: The supervisor MUST fetch the operator's `CLAUDE_CODE_OAUTH_TOKEN` from Infisical at vault path `/<customer_id>/operator/CLAUDE_CODE_OAUTH_TOKEN` once per chat-message spawn. The token MUST never live in the supervisor's process env, host filesystem outside the vault client's in-memory `vault.SecretValue`, log output, audit row payload, or persistence tier.
- **FR-002**: Each successful vault fetch MUST write a `vault_access_log` row with `agent_instance_id=NULL`, `outcome='value_revealed'`, `secret_path` matching the chat-token path, and `metadata` containing `chat_session_id`, `chat_message_id`, and `actor='supervisor_chat'`.
- **FR-003**: A failed vault fetch (token absent, Infisical 5xx, network) MUST surface as a typed assistant message with `error_kind ∈ {'token_not_found', 'token_expired', 'vault_unavailable'}` and MUST NOT spawn a chat container.
- **FR-004**: The token MUST be injected into the chat container's `docker run` env as `CLAUDE_CODE_OAUTH_TOKEN`. The supervisor's own process env MUST NOT carry the token at any point.
- **FR-005**: Token rotation MUST be transparent to the operator: updating the vault entry via `/vault/edit/.../CLAUDE_CODE_OAUTH_TOKEN` (M4 surface) takes effect on the next chat-message spawn, with no supervisor restart.

#### Spawn + runtime

- **FR-010**: On every operator chat message, the supervisor MUST spawn a fresh Docker container from a pinned image identifier (env-configurable `GARRISON_CHAT_CONTAINER_IMAGE`, default `garrison-claude:m5`) via the existing `garrison-docker-proxy`.
- **FR-011**: The container MUST be ephemeral — `--rm`-equivalent, no volume mounts beyond what MCP servers require, killed within `ShutdownSignalGrace` after the spawn's terminal event or supervisor SIGTERM.
- **FR-012**: The container's `claude` invocation MUST use `--output-format stream-json --include-partial-messages --mcp-config <per-message config> --strict-mcp-config --permission-mode bypassPermissions --model <agent-config-or-default>`.
- **FR-013**: The container MUST NOT receive `--resume` or any other flag that would persist conversation state outside Postgres+MemPalace. The supervisor MUST construct the per-turn prompt by replaying the session's transcript from `chat_messages` (ordered by `turn_index`).
- **FR-014**: The supervisor MUST set `SysProcAttr.Setpgid = true` on the host-side `docker` invocation and signal SIGTERM to the process group on cancellation (AGENTS.md rule 7).
- **FR-015**: Pipes from the container MUST be drained to completion before the host-side `cmd.Wait()` (AGENTS.md rule 8).
- **FR-016**: Per-turn wall-clock timeout MUST be configurable via `GARRISON_CHAT_TURN_TIMEOUT` (default 5 minutes). Timeout triggers a process-group SIGTERM and a message terminal write with `error_kind='turn_timeout'`.

#### MCP tool surface

- **FR-020**: The chat MCP config MUST mount exactly two MCP servers: the existing `postgres` server (read-only via `garrison_agent_ro`) and the existing `mempalace` server. No new MCP servers ship in M5.1.
- **FR-021**: The chat MCP config MUST NOT include the `finalize` MCP server. Chat does not have a `finalize_ticket`-shaped terminal event.
- **FR-022**: The chat MCP config MUST be rejected by the M2.3 `mcpconfig.CheckExtraServers` precheck if any mutation-shaped server appears (defensive: M5.3 will introduce a `garrison-mutate` server but it ships behind a separate threat-model amendment, not by accidentally being enabled in M5.1).

#### Stream parser

- **FR-030**: The supervisor's existing `internal/spawn/pipeline.go` parser MUST be extended via a `Policy` interface so a new `ChatPolicy` can supply chat-shaped event handling without forking the parser code path.
- **FR-031**: `ChatPolicy.OnInit` MUST validate MCP server health the same way M2.1's policy does — any server with `status != "connected"` triggers an immediate process-group SIGTERM and `error_kind='mcp_<server>_<status>'`.
- **FR-032**: `ChatPolicy.OnAssistant` and the `stream_event` `text_delta` events MUST flow incrementally to the SSE bridge as they arrive, not buffered until the `result` event.
- **FR-033**: `ChatPolicy.OnResult` MUST commit the assistant message terminal write (full content, cost, token counts, raw envelope) inside a single Postgres transaction with the corresponding `chat_messages` row update and `pg_notify('work.chat.message_sent', message_id)`.

#### Persistence

- **FR-040**: A new `chat_sessions` table MUST be created with at minimum: `id` (UUID PK), `started_by_user_id` (UUID FK to `users.id`), `started_at` (timestamptz), `ended_at` (timestamptz nullable), `status` (TEXT enum-checked: `active|ended|aborted`), `total_cost_usd` (NUMERIC default 0), `claude_session_label` (TEXT nullable — operator-facing label, optional in M5.1).
- **FR-041**: A new `chat_messages` table MUST be created with at minimum: `id` (UUID PK), `session_id` (UUID FK to `chat_sessions.id`), `turn_index` (INTEGER), `role` (TEXT enum-checked: `operator|assistant`), `status` (TEXT enum-checked: `pending|streaming|completed|failed|aborted`, NOT NULL), `content` (TEXT — never NULL on terminal commit), `tokens_input` (INTEGER nullable), `tokens_output` (INTEGER nullable), `cost_usd` (NUMERIC nullable), `error_kind` (TEXT nullable, with documented vocabulary), `raw_event_envelope` (JSONB nullable), `created_at` (timestamptz), `terminated_at` (timestamptz nullable). `UNIQUE(session_id, turn_index)` enforced. Operator rows INSERT with `status='completed'`; assistant rows transition `pending → streaming → completed|failed|aborted`. The supervisor restart sweep (FR-083) MUST select on `status IN ('pending','streaming')` rather than deriving from `terminated_at IS NULL`.
- **FR-042**: The `garrison_dashboard_app` Postgres role MUST be granted `INSERT, SELECT` on `chat_messages` and `INSERT, SELECT, UPDATE` on `chat_sessions`. The supervisor's primary DB role keeps full ownership.
- **FR-043**: Drizzle's introspection (`bun run drizzle:pull`) MUST regenerate `dashboard/drizzle/schema.supervisor.ts` to include the two new tables and their TypeScript types after the migration applies.

#### Transport

- **FR-050**: The dashboard MUST INSERT new operator messages into `chat_messages` (role='operator', status='completed', `turn_index = COALESCE((SELECT MAX(turn_index) FROM chat_messages WHERE session_id = $1), -1) + 1` computed inside the INSERT transaction) and emit `pg_notify('chat.message.sent', message_id)`. The supervisor LISTENs on `chat.message.sent` and processes messages in the order received. Concurrent INSERTs against the same session race the `UNIQUE(session_id, turn_index)` constraint; the loser retries with the new max — the single-operator assumption (Assumptions §) makes this rare.
- **FR-051**: The supervisor MUST emit `pg_notify('chat.assistant.delta', json_build_object('message_id', $1, 'seq', $2, 'delta_text', $3)::text)` per batch of token deltas, where `seq` is a monotonically increasing per-message integer starting at 0. The notify payload carries the delta directly; no per-batch UPDATE on `chat_messages` is performed during streaming. Per-batch payload size MUST be kept under 7KB to stay safely below Postgres's 8000-byte NOTIFY payload ceiling — the supervisor MUST coalesce or split text_delta events as needed. First delta MUST land within 1s of the container's first text_delta event.
- **FR-052**: A new dashboard SSE endpoint at `/api/sse/chat?session_id=<uuid>` MUST `LISTEN chat.assistant.delta` and forward each notify payload to the SSE consumer as a `delta` event, plus terminal `result` and `session_ended` events read from `chat_messages` / `chat_sessions` row state. Reconnect via `Last-Event-ID` resumes from the next *terminal* event for that message (deltas are transient and NOT replayable post-disconnect — the consumer that reconnects mid-stream renders the partial accumulated buffer it already has and waits for the terminal `result`).
- **FR-052a**: At assistant terminal commit (FR-033), the supervisor MUST write the full assembled `content` and the complete `raw_event_envelope` (a JSON array of all stream events received for the turn) in the same transaction that flips `status → completed|failed|aborted` and emits `pg_notify('work.chat.message_sent', message_id)`.
- **FR-053**: The supervisor MUST NOT expose a separate HTTP/RPC port for chat. All operator → supervisor communication for chat goes through the Postgres event bus (Constitution I).

#### Cost telemetry

- **FR-060**: The supervisor MUST roll the per-turn `total_cost_usd` (parsed from the `result` event) into `chat_sessions.total_cost_usd` inside the same transaction that commits the assistant `chat_messages` row.
- **FR-061**: A per-session soft cap configurable via `GARRISON_CHAT_SESSION_COST_CAP_USD` (default 1.00) MUST be enforced before spawn. The check is purely reactive: if `chat_sessions.total_cost_usd >= GARRISON_CHAT_SESSION_COST_CAP_USD` at spawn time, the supervisor MUST refuse the spawn and write an assistant row with `status='failed', error_kind='session_cost_cap_reached'`. No projection, no buffer, no estimate of the next turn's cost — the turn that nudged the running total over the cap is the last one that runs; the next turn is refused. Worst-case overshoot is bounded by one turn's cost.
- **FR-062**: Per-day and per-month cost caps are out of scope for M5.1.

#### Audit + activity

- **FR-070**: Every chat session MUST emit exactly one `pg_notify('work.chat.session_started', chat_session_id)` at session creation and exactly one `pg_notify('work.chat.session_ended', chat_session_id)` at session terminal write.
- **FR-071**: Every operator message MUST emit `pg_notify('work.chat.message_sent', chat_message_id)` after the supervisor commits the assistant row. Note: this is a separate channel from `chat.message.sent` (operator-INSERT signal); the `work.*` namespace is for activity-feed consumption.
- **FR-072**: Audit-channel payloads MUST carry only opaque IDs (`chat_session_id`, `chat_message_id`, `actor_user_id`). Message content MUST NOT appear in any `pg_notify` payload or `event_outbox.payload` JSONB.
- **FR-073**: The activity feed (M3 SSE consumer) MUST surface chat events using the existing channel-pattern subscription; M5.1 ships the producer side, M3's consumer side stays unchanged.

#### Lifecycle

- **FR-080**: The dashboard server action that handles a fresh-session operator message MUST create the `chat_sessions` row (status='active', total_cost_usd=0, started_by_user_id=session.user.id) and the first `chat_messages` row (role='operator', turn_index=0) inside a single transaction, then emit `pg_notify('chat.message.sent', message_id)`. There is no separate "open session" call. Subsequent operator messages on the same session INSERT into chat_messages only. The supervisor MUST NOT lazy-create `chat_sessions` rows; an unknown session_id on a `chat.message.sent` notify MUST be treated as a foreign-key error and surfaced as a typed assistant row with `error_kind='session_not_found'`.
- **FR-081**: A session idle for `GARRISON_CHAT_SESSION_IDLE_TIMEOUT` (default 30 min, measured from the most recent message) MUST be marked `ended`. Subsequent operator messages on an `ended` session MUST be rejected with `error_kind='session_ended'`.
- **FR-082**: An explicit close endpoint is deferred to M5.2.
- **FR-083**: On supervisor boot, any session with `status='active'` whose newest `chat_messages` row has `status IN ('pending','streaming')` and `created_at` older than 60s MUST be marked `aborted` and the in-flight message terminal-written with `status='aborted'` and `error_kind='supervisor_restart'`.

#### Compose / ops

- **FR-090**: `supervisor/docker-compose.yml` MUST add `CREATE: 1` to the `garrison-docker-proxy` service env so the supervisor can `POST /containers/create` for chat spawns. The amendment MUST land in the same migration as the supervisor code that exercises it.
- **FR-091**: A new `Dockerfile.claude` (path spec-implementation-defined) MUST produce the `garrison-claude:m5` image: `node:22-slim` base, `@anthropic-ai/claude-code@2.1.86` pinned via npm install. The Dockerfile MUST NOT bake the OAuth token in.
- **FR-092**: `docs/ops-checklist.md` MUST gain an "M5.1 — chat backend" section describing: (a) seeding the OAuth token in Infisical via `/vault/new`, (b) the `GARRISON_CHAT_*` env vars and their defaults, (c) docker-proxy `CREATE` flag verification, (d) image pinning procedure.

#### CI / testing

- **FR-100**: A `garrison-mockclaude:m5` image MUST be added with a stub claude binary that emits canned NDJSON responses (extending the M2.x `mockclaude` pattern). CI sets `GARRISON_CHAT_CONTAINER_IMAGE=garrison-mockclaude:m5` so integration tests run without a real OAuth token.
- **FR-101**: The supervisor's chaos test suite MUST gain a chat-flavoured case: kill the chat container mid-stream, assert the supervisor cleans up and writes a `session_ended` row with status='aborted'.
- **FR-102**: The Playwright integration suite MUST gain a chat backend test: INSERT an operator message, assert the SSE stream emits the expected events, assert `chat_messages` rows land, assert the `vault_access_log` audit row landed with the chat-shaped metadata.

### Key entities

- **chat_sessions** — one row per CEO conversation. Lifecycle: `active → ended` (idle timeout or explicit close in M5.2) | `active → aborted` (supervisor SIGTERM, crash, or restart). Carries the running cost roll-up. Owned by the supervisor; INSERT/UPDATE granted to `garrison_dashboard_app` for M5.2 surface.
- **chat_messages** — one row per turn. `role='operator'` rows are dashboard-INSERTed; `role='assistant'` rows are supervisor-INSERTed (terminal commit) plus updated incrementally in `raw_event_envelope` as deltas arrive. Index on `(session_id, turn_index)` for transcript reconstruction.
- **vault_access_log** (extended use) — chat fetches reuse the M4-relaxed nullable `agent_instance_id` column. New `metadata.actor='supervisor_chat'` value distinguishes chat fetches from M4 mutation fetches and from M2.3 agent fetches.
- **event_outbox** (existing) — three new channel names: `work.chat.session_started`, `work.chat.message_sent`, `work.chat.session_ended`. Payload is opaque IDs only; no message content.

---

## Success Criteria

### Measurable outcomes

- **SC-001**: An operator-INSERTed `chat_messages` row triggers a supervisor spawn whose first SSE `text_delta` event reaches the dashboard within 5 seconds wall-clock, against a real `garrison-claude:m5` container with a real (test-account) `CLAUDE_CODE_OAUTH_TOKEN` seeded in Infisical, on a developer-grade laptop with Docker images pre-pulled.
- **SC-002**: A multi-turn session of N≥3 turns MUST demonstrate context fidelity: turn N's prompt cache statistics (`cache_read_input_tokens > 0` in the `result` event's raw envelope) prove the supervisor replayed prior turns rather than starting fresh.
- **SC-003**: After a session of N turns ends, `chat_sessions.total_cost_usd ≈ Σ (chat_messages.cost_usd WHERE role='assistant' AND session_id=…)` within $0.000001.
- **SC-004**: For every chat-message spawn that successfully fetches the OAuth token, exactly one `vault_access_log` row exists with the chat-shaped metadata. Zero rows for spawns that fail before the fetch.
- **SC-005**: A grep of `event_outbox.payload`, `chat_messages.raw_event_envelope`, and supervisor stdout/stderr captured during a test session MUST find zero substrings of (a) the `CLAUDE_CODE_OAUTH_TOKEN` value and (b) the test-injected `sk-`/`xoxb-`/`AKIA`-shaped sentinel values planted in palace contents during the test.
- **SC-006**: Supervisor SIGTERM during an in-flight chat turn MUST result in: container gone within 5s, `chat_messages` terminal write committed (status='aborted', `error_kind='supervisor_shutdown'`), `chat_sessions` marked aborted, SSE stream emitted a typed error frame before the connection closed.
- **SC-007**: The chat container's per-spawn MCP config MUST contain exactly the `postgres` and `mempalace` server entries — verifiable via integration test that intercepts the config file before claude invocation. Any test that produces a config with a third entry fails.
- **SC-008**: `goose up` against a fresh testcontainer Postgres applies the M5.1 migration cleanly. `bun run drizzle:pull` regenerates `schema.supervisor.ts` to include `chat_sessions` + `chat_messages` types. The dashboard's `bun run typecheck` passes against the regenerated types.
- **SC-009**: The chaos test "kill chat container mid-stream" passes — supervisor detects the missing process within `ShutdownSignalGrace`, terminal-writes `chat_messages` with `error_kind='container_crashed'`, and the next operator message on the same session starts cleanly.
- **SC-010**: The full Playwright integration suite (M3 + M4 + M5.1) passes in CI, runtime under 12 minutes total. The new M5.1 chat backend test specifically asserts the round-trip from operator INSERT to assistant terminal commit lands within 10 seconds.
- **SC-011**: Token rotation flow: with the chat already in flight against token A, INSERT a vault edit via the M4 server action that changes the token to B, send the next chat turn, assert: turn N runs with token B (verifiable via the supervisor logging the vault fetch's `last_rotated_at` timestamp without revealing the token value), no supervisor restart, no in-flight turn corruption.
- **SC-012**: Soft cost cap enforces: with `GARRISON_CHAT_SESSION_COST_CAP_USD=0.001` and a session whose running cost is already at-or-above `$0.001`, the next operator message gets rejected with `status='failed', error_kind='session_cost_cap_reached'` and no container starts. With a session whose running cost is below the cap (e.g., $0.0009 against a $0.001 cap), the next message is allowed to run even if its outcome would push the total over (FR-061 is reactive, not predictive). With the cap reset to a higher value, subsequent messages run.
- **SC-013**: Idle timeout enforces: with `GARRISON_CHAT_SESSION_IDLE_TIMEOUT=10s`, a session with no activity for 12s gets marked `ended`. The next operator message on that session gets `error_kind='session_ended'`.
- **SC-014**: A vault fetch failure (token deleted from Infisical between two messages) MUST: not retry past the first 500ms backoff, write an assistant row with `error_kind='token_not_found'`, leave `chat_sessions.status='active'` (the session is recoverable; only the turn fails), and emit an SSE error event the dashboard can render.
- **SC-015**: The new `garrison-mockclaude:m5` image MUST run the same chat backend tests in CI without a real OAuth token. CI sets `GARRISON_CHAT_CONTAINER_IMAGE=garrison-mockclaude:m5` and the test asserts the SSE stream emits a deterministic canned response.
- **SC-016**: Zero new direct dependencies in the supervisor's `go.mod`, OR every new dependency is justified in the M5.1 implementation commit messages and recapped in the M5.1 retro per AGENTS.md soft-rule.
- **SC-017**: The vaultlog go-vet analyzer MUST continue to pass on the new chat code paths — no slog/fmt/log call accepts a `vault.SecretValue` argument anywhere in `internal/chat/` (or wherever the chat code lands per `/speckit.plan`).

---

## Assumptions

- The operator has provisioned an Anthropic claude.ai account with a long-lived OAuth token (`sk-ant-oat01...`, 108 chars) and is willing to seed it into Infisical at `/<customer_id>/operator/CLAUDE_CODE_OAUTH_TOKEN` before the first chat session.
- Garrison remains single-tenant single-operator. The `started_by_user_id` column on `chat_sessions` exists for audit clarity and future multi-operator readiness, but only one operator's user_id is ever expected to populate it in M5.1.
- Anthropic's prompt caching behaves as observed in the spike: re-passing the same transcript prefix across turns produces non-zero `cache_read_input_tokens`, making the stateless-replay approach affordable. If prompt caching changes behaviour or is disabled per-account, multi-turn cost rises but the architecture stays valid.
- The `garrison-docker-proxy` allow-listing `POST /containers/create` does not change Garrison's threat posture meaningfully — the proxy already allows arbitrary `POST /containers/*/exec` against existing containers (M2.2 mempalace path), so a supervisor compromise that wants to run arbitrary code already has a path.
- The dashboard's M4-installed `garrison_dashboard_app` Postgres role can be granted INSERT on `chat_messages` via the same migration shape as the M4 vault-table grants (commit `20260427000014_m4_dashboard_app_vault_read_grants.sql`).
- Existing M2.x `internal/spawn/pipeline.go` is extensible via a `Policy` interface without breaking M2.x callers (validated against the agent map in spike §1, but spec acknowledges this is a refactor that `/speckit.plan` will need to scope carefully).
- The M3 SSE transport (`/api/sse/activity`) handles `Last-Event-ID`-based reconnect well enough that a chat-flavoured endpoint can mirror its shape without infrastructure changes.
- Existing single-operator concurrency-cap model does not need extension for M5.1 — the chat is a single global resource; only one chat container per session at a time, serial within a session. No new `concurrency_cap` rows.
- The retention policy on `chat_messages.raw_event_envelope` is "keep forever within the retained-rows window of M5.1"; a separate per-session trim/summary cron is deferred to M5.2 polish or later.
