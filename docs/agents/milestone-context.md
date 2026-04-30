# Per-milestone agent context

Activated by AGENTS.md's "Activate before writing code" rule. Read the entries for the **current milestone plus all prior milestones whose code remains in the codebase**. Future-milestone entries are listed only so the operator can plan ahead — agent sessions should not pre-activate them.

---

## M1 — event bus + supervisor core (shipped)

- **Go 1.23+ idioms**: `context.Context` threaded through every goroutine; no bare `go func()`; `errgroup.WithContext` for "run N subsystems, cancel all if one fails" at the top level; channels with explicit sender/receiver responsibility; buffered channels only with a documented reason; stdlib `log/slog` for structured logging.
- **`jackc/pgx/v5` patterns**: the LISTEN connection is dedicated (a plain `*pgx.Conn` or manually hijacked `*pgxpool.Conn`), never from the pool; reconnect with exponential backoff (100ms → 30s cap); on reconnect, run the `processed_at` fallback poll before re-LISTENing.
- **Postgres LISTEN/NOTIFY**: payload size is limited (8KB); channel names are dot-delimited (`work.ticket.created.<dept>.<column>`); NOTIFY fires inside the same transaction as the state change; missed notifications are expected and handled via `processed_at` + periodic poll.
- **`sqlc`**: SQL migrations are the source of truth; query types are generated, not hand-written; both Go (`sqlc`) and TypeScript (Drizzle, later) derive from the same SQL.
- **`os/exec` with `CommandContext`**: every subprocess has a timeout context; cancellation sends SIGTERM then SIGKILL after a grace period; stdout/stderr piped per the pipeline-drain rule (concurrency rule 8).
- **`testcontainers-go`**: integration tests spin ephemeral Postgres containers; no mocking of the DB layer.

## M2.1 — Claude Code invocation (shipped)

- **Claude Code non-interactive invocation contract**: exact flags, input format, stdout/stderr shape, exit code vocabulary. Specifics from `docs/research/m2-spike.md` Part 1. Do not activate assumptions — activate the spike's findings.
- **stream-json event routing**: every invocation uses `--output-format stream-json --verbose`. The first event is `system`/`init`; subsequent events are `assistant`, `user` (tool_result), `rate_limit_event`, and a terminal `result`. The supervisor parses NDJSON line-by-line and routes by `type`.
- **MCP health via init event**: the supervisor parses the `mcp_servers[]` array in the `system`/`init` event. A server with `status == "connected"` is healthy; anything else (`failed`, `needs-auth`, unknown value) means the supervisor kills the Claude process group immediately and records the agent_instance as failed with `exit_reason = "mcp_<server>_<status>"`. Unknown status values are treated as failures (fail-closed).
- **`internal/pgmcp`**: in-tree Go Postgres MCP server (~300 LOC), stdio JSON-RPC, `query` + `explain` tools. Read-only enforced via Postgres role (`garrison_agent_ro`) plus protocol-layer filter. Works as `supervisor mcp postgres` subcommand.

## M2.2 — MemPalace MCP wiring (shipped)

- **MemPalace MCP server lifecycle and concurrent access semantics.** From `docs/research/m2-spike.md` Part 2.
- **MemPalace `init --yes` discipline.** Runs against a dedicated non-git-tracked directory. Never against the Garrison repo or supervisor workspace. T001 finding F1: the init is idempotent in 3.3.2; the `chroma.sqlite3` marker-file heuristic is unreliable.
- **Diary entry and KG triple write contracts** as described in `ARCHITECTURE.md` "MemPalace write contract" section.
- **Hygiene check query patterns** — post-transition async checks for expected writes. Two evaluation paths now coexist (see M2.2.1).
- **`garrison_agent_mempalace` Postgres role** — SELECT-only on `ticket_transitions + agent_instances + tickets + agents`; password set via ops checklist post-migrate; composed into `AgentMempalaceDSN()` at supervisor startup (parallels M2.1's `garrison_agent_ro`).
- **Three-container deployment topology**: supervisor + `Dockerfile.mempalace` (python:3.11-slim with chromadb wheels — alpine fails) + `linuxserver/socket-proxy` on `tcp://garrison-docker-proxy:2375` (NOT a unix-socket proxy, despite the name).
- **`emit_ticket_transitioned` Postgres trigger** with `department_id` resolved via subquery — the M2.2 retro flagged the original payload-omission bug.

## M2.2.1 — Structured completion via `finalize_ticket` tool (shipped)

- **`internal/finalize` MCP server**: in-tree, single-tool (`finalize_ticket`), the only path that commits a ticket transition. Schema-strict (`outcome ≥ 10 chars`, `rationale ≥ 50`, `kg_triples ≥ 1` with each field ≥ 3 chars).
- **`spawn.WriteFinalize` atomic transaction**: brackets two MemPalace sidecar calls (AddDrawer + N× AddTriples) and four DML writes (`ticket_transitions`, `tickets` update, `agent_instances` terminal, `event_outbox` mark-processed). 30-second wall-clock ceiling via `GARRISON_FINALIZE_WRITE_TIMEOUT`. Retries cap at 3 in `internal/spawn/pipeline.go`.
- **Hygiene-status vocabulary extensions**: `finalize_failed`, `finalize_partial`, `stuck` alongside the M2.2 vocabulary.
- **Two hygiene evaluation paths coexist**: pure-Go `EvaluateFinalizeOutcome` for finalize-shaped rows; palace-query `Evaluate` for legacy M2.2 rows. Routed by `agent_instances.exit_reason`. No retroactive UPDATE; no CHECK constraint on the column during the transition window.
- **The objective-prose-prepend pattern** for diary bodies — `mempalace_search` is vector-similarity, not substring; raw UUIDs return zero matches. Anything you want findable by ticket-id needs prose around it.

## M2.2.2 — Compliance calibration (shipped)

- **Richer structured error responses** in `internal/finalize`: `Failure`/`Constraint` typed enums; `ValidationError` with `line/column/excerpt/constraint/expected/actual/hint` fields. `decodePosition` helper sanitises ASCII control chars in excerpts to `·`.
- **Adjudicate precedence fix**: `isBudgetTerminalReason` checked BEFORE `result.IsError` when `ResultSeen=true` (one-line swap in `internal/spawn/pipeline.go`). Budget-cap events no longer masked as `claude_error`. See M2.2.1 retro §"Bonus finding" for the surfacing run.
- **Seed agent.md shape**: front-loaded goal sentence in `## Wake-up context`; palace-search calibration as 3 bullets (Skip-if-trivial / Search-if / In-doubt-skip); example payload as fenced JSON with angle-bracket placeholders; retry framing in `## Failure modes` naming the `hint` field. Files at `migrations/seed/{engineer,qa-engineer}.md`, embedded into migrations via `+embed-agent-md` markers.
- **Live-matrix caveat**: M2.2.2's retro originally read the calibration thesis as falsified on live models. The M2.2.x arc retro (`docs/retros/m2-2-x-compliance-retro.md`) revises that read against the contaminated-data root cause uncovered in the post-ship pgmcp investigation. The richer-error infra is correct and ready; whether it moves model behaviour is partially observed (one positive retry in the post-fix matrix).

## M2.3 — Secret vault / Infisical (shipped)

- **`vault.SecretValue` opaque type**: `LogValue()` returns `[REDACTED]`; `UnsafeBytes()` is grep-auditable (only 2 production call sites: spawn env injection, Rule 1 leak scan); `Zero()` clears the backing slice after subprocess start. **The `tools/vaultlog` go vet analyzer rejects slog/fmt/log calls with `SecretValue` arguments at build time.**
- **Four vault rules**: (1) no agent.md may contain a raw secret value (Rule 1 leak scan against fetched grant set); (2) zero-grants spawns get zero secrets (no implicit fetch); (3) the agent's MCP config cannot reference the vault MCP — vault is opaque to agents, `mcpconfig.CheckExtraServers` runs BEFORE vault fetch; (4) every fetch writes a `vault_access_log` row inside the spawn tx, **fail-closed if INSERT fails**.
- **Seven-step spawn ordering** in `spawn.go` (D4.5 / FR-416): grants → mcpconfig pre-check → vault fetch → leak scan → audit row → env build + `cmd.Start` (with `defer val.Zero()`) → existing M2.2.x subprocess pipeline.
- **Three new tables + first production trigger**: `agent_role_secrets`, `vault_access_log`, `secret_metadata`; trigger `rebuild_secret_metadata_role_slugs` (AFTER INSERT/DELETE on `agent_role_secrets`) rebuilds the denorm `allowed_role_slugs` array. Use this trigger pattern for denorm columns derived from another table where the rebuild is cheap; avoid for cross-schema joins or expensive computation.
- **Finalize-path scanner hook** (`scanAndRedactPayload` in `finalize.go`): non-blocking pattern scan over `DiaryEntry.Rationale` and `KGTriples[*].{Subject,Predicate,Object}` before MemPalace write; sets `hygiene_status='suspected_secret_emitted'` on any match. 10 patterns: sk-prefix, xoxb, AWS AKIA, PEM header, GitHub PAT/App/User/Server/Refresh, bearer-shape.
- **Infisical SDK quirks** (caught at integration-test time, not in the spike): SDK caches access tokens eagerly and renews on 401 — auth-expired tests need a short-lived ML (`accessTokenTTL=1`, `numUsesLimit=1`), not a rotated client_secret. 403/429 testing requires an HTTP proxy injecting status codes; testcontainers can't reproduce them natively.

## M3 — operator dashboard, read-only (shipped)

- Next.js 16 App Router patterns, React 19 (Server Components, Server Actions where appropriate).
- Tailwind v4 and shadcn/ui conventions.
- Real Claude Code emits 5–10 `assistant` events per run (M2.1 retro). Activity feed UI must render high-volume event streams without visual bloat.
- M3 reads from M2.3 vault tables (`agent_role_secrets`, `vault_access_log`, `secret_metadata`) via a dashboard-scoped read role with explicit grants — never via `garrison_agent_ro` or any agent-facing role.
- Surfaces both `hygiene_status` failure modes (sandbox-escape "artifact claimed vs artifact on disk", finalize-never-called, suspected_secret_emitted) and the cost-telemetry blind spot — see `docs/issues/`.

## M4 — operator dashboard mutations (shipped)

- Server-Action mutation surface for the M3 read-only dashboard. Optimistic UI + rollback via dedicated route handlers; Drizzle for write paths.
- See `docs/retros/m4.md` for what shipped, including any Path B coverage-wiring decisions.

## M5.1 — CEO chat backend (shipped)

- Chat-session + chat-message tables + `pg_notify` on `work.chat.*` channels.
- Spawned chat subprocess uses the same M2.x supervisor pipeline shape, with chat-specific identity (no agent_role_slug).
- See `docs/research/m5-spike.md` for the chat-runtime behaviour findings.

## M5.2 — CEO chat dashboard surface (shipped)

- `/chat` three-pane layout: thread list, message stream, knows-about pane.
- SSE chat stream `lib/sse/chatStream.ts` — handles assistant/user/result event dispatch + the visible-buffer scrub on tool_use start.
- M5.2 amended FR-322 chat-flavoured concise predicates for the activity feed (chat events render as "Chat thread <short> deleted by operator" etc.).

## M5.3 — chat-driven mutations under autonomous-execution posture (active)

- Eight sealed verbs in `internal/garrisonmutate/`: create_ticket, edit_ticket, transition_ticket, pause_agent, resume_agent, spawn_agent, edit_agent_config, propose_hire. Registry pinned by `TestVerbsRegistryMatchesEnumeration`.
- Autonomous-execution posture: no per-call gate. Three observability layers — chip surface (live tool-call rendering), activity feed (post-commit `work.chat.*` events), audit row (`chat_mutation_audit`).
- Per-turn tool-call ceiling (default 50, `GARRISON_CHAT_MAX_TOOL_CALLS_PER_TURN`).
- Chat-namespaced channels: `work.chat.<entity>.<action>`. Rule 6 backstop: IDs only in payloads, never raw chat content.
- See `docs/security/chat-threat-model.md` for the binding threat-model document (Rules 1–6, per-verb reversibility-tier table).

## M7 — hiring (not yet active)

When M7 becomes active, additionally activate:
- MCP server authoring patterns (using `anthropics/skills/mcp-builder`).
- `skills.sh` registry semantics for discovery and install.
- **SkillHub (iflytek)** as the target-state private-skills component alongside the public `skills.sh` feed; see `docs/skill-registry-candidates.md` and `docs/architecture-reconciliation-2026-04-24.md` §2 for decision provenance.

## M8 — MCP server registry (not yet active)

When M8 becomes active, additionally activate:
- **MCPJungle** as the leading candidate for the M8-era MCP server registry (self-hosted, Go-based, Postgres-backed, combines registry + runtime proxy). Maturity re-check at M7 kickoff. See `docs/mcp-registry-candidates.md`.
