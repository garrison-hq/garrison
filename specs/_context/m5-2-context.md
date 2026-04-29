# M5.2 — CEO chat dashboard surface (read-only)

**Status**: Context for `/speckit.specify`. Frontend half of M5; sits directly on the M5.1 substrate (server actions, SSE endpoint, transcript reads, idle/restart sweeps) without changing it.
**Prior milestone**: M5.1 (read-only chat backend) shipped on branch `010-m5-1-ceo-chat-backend`. Retro at [`docs/retros/m5-1.md`](../../docs/retros/m5-1.md).
**M5 decomposition (updated)**: M5.1 → **M5.2** → M5.3 → M5.4. M5 closes after M5.4 (was M5.3 before this context). M5.4 is a new milestone split out from M5.2 during context-write to hold the "WHAT THE CEO KNOWS" knowledge-base / context-observability pane.

**Binding inputs** (read first, in order):

1. **M5.1 retro** ([`docs/retros/m5-1.md`](../../docs/retros/m5-1.md)) — load-bearing. Names what shipped, what's deferred to M5.2 (FR-082 explicit-close, orphan-operator-row mitigation, T020 Playwright e2e, `chaos_m5_1_test.go`), and what's tracked-but-deferred (warm-pool optimization). The "Ready to start M5.2" section (lines 437–456) explicitly lists the M5.1 surface M5.2 builds against.
2. **M5.1 spec** ([`specs/010-m5-1-ceo-chat-backend/spec.md`](../010-m5-1-ceo-chat-backend/spec.md)) — FR/SC vocabulary M5.2 inherits. Notably FR-082 ("explicit close endpoint deferred to M5.2"), FR-083 (restart sweep), FR-081 (idle timeout), SC-006 (SIGTERM cascade), SC-009 (container-crashed chaos), SC-010 (Playwright runtime), SC-011 (token rotation E2E), SC-014 (vault fetch failure UX). Sections cited by FR/SC number throughout.
3. **M5.1 plan + tasks + acceptance evidence** ([`specs/010-m5-1-ceo-chat-backend/`](../010-m5-1-ceo-chat-backend/)) — implementation shape M5.2 reads against, especially `acceptance-evidence.md` for the four un-pinned SCs (SC-006, SC-010, SC-011, SC-015) the chaos + Playwright work closes.
4. **M5.1 context doc** ([`specs/_context/m5-1-context.md`](./m5-1-context.md)) — establishes the read-only stance and the M5.3 mutation deferral. M5.2 does not amend that stance.
5. **M5 spike** ([`docs/research/m5-spike.md`](../../docs/research/m5-spike.md)) — §3 ephemeral-per-turn, §6 attack-class-1 framing, §8.1 wire-shape NDJSON the test fixtures mirror. Warm-pool measurements in §3 inform the tracked-but-deferred line.
6. **`ARCHITECTURE.md:574`** — "M5 — CEO chat (summoned, read-only). Conversation panel, summon-per-message pattern, Q&A only." The architecture commits to one M5 entry; the new M5.4 split-out predates an architecture amendment, which the operator may choose to land at M5.2 spec time or after.
7. **`ARCHITECTURE.md:69`** — supervisor summons Claude per-message; chat UI is the operator's seat at that loop.
8. **`RATIONALE.md:117-130`** — summon-per-message framing and the warm-pool deferral rationale that backs M5.2's "tracked but not addressed" line.
9. **`AGENTS.md`** — Rule 6 (audit-row-discipline applies to chat surfaces too: no message content in pg_notify payloads, no token substrings in client logs), §Retros (dual-deliverable), the locked-deps soft-rule (extends to dashboard side per M3 retro).
10. **M3 retro** ([`docs/retros/m3.md`](../../docs/retros/m3.md)) — design-system precedents M5.2 reuses: Tailwind v4 token system, the polish-round design language (type scale 5 levels, `font-tabular`, `columnTone()`, status chip conventions, card pattern, `@panel` parallel-route intercept, `garrison-slide-in-right` + `garrison-fade-in` animation primitives, `prefers-reduced-motion` honour), `/api/sse/activity` shape M5.1's `/api/sse/chat` mirrors, the 5-state listener machine (`lib/sse/listener.ts`).
11. **M4 retro** ([`docs/retros/m4.md`](../../docs/retros/m4.md)) — operator-mutation patterns M5.2 reuses for the FR-082 explicit-close action: server-action shape, audit row, `pg_notify` discipline, optimistic locking primitives in `lib/locks/`, `ConfirmDialog` (single-click vs typed-name tiers), the Rule 6 backstop pattern in `lib/audit/`. Importantly: M5.2 ships *no* chat-driven mutations — only the operator-driven action of closing their own thread, which mirrors M4 ticket-edit shape, not chat-content shape.
12. **M5.1 dashboard substrate** (reference, not binding):
    - [`dashboard/lib/actions/chat.ts`](../../dashboard/lib/actions/chat.ts) — `startChatSession(message)` + `sendChatMessage(sessionId, message)` + `ChatErrorKind` enum
    - [`dashboard/lib/queries/chat.ts`](../../dashboard/lib/queries/chat.ts) — `listSessionsForUser`, `getSessionWithMessages`, `getRunningCost`
    - [`dashboard/app/api/sse/chat/route.ts`](../../dashboard/app/api/sse/chat/route.ts) — SSE producer; emits `delta` + `terminal` events keyed on `session_id`
    - [`dashboard/lib/sse/`](../../dashboard/lib/sse/) — channel allowlist, listener machine, event discriminated union (extending the union for chat is FR-060-shaped, NOT a wildcard subscribe)
13. **The M5.2 wireframe** (operator-side reference; not committed). Describing in prose so the spec can decide UX details independently of any single sketch — see "Wireframe in prose" subsection in §In scope below.

If this context contradicts any of the above, the binding inputs win. The architecture line at `:574` mentions only one M5 entry; the operator may choose to amend it during /speckit.specify, or defer the architecture amendment to a later commit.

---

## Why this milestone now

M5.1 ships a full server-side chat substrate — server actions, SSE producer, transcript reads, idle + restart sweeps, audit/cost rollup — but no UI. The retro's "Ready to start M5.2" section pins this explicitly: the M5.2 binding interface is `startChatSession` + `sendChatMessage` + `/api/sse/chat?session_id=…` + `getSessionWithMessages` + `listSessionsForUser`. Each is tested and stable. M5.2 ships the dashboard surface that lets the operator actually use the chat without driving it via SQL INSERT.

Splitting M5 into M5.1 (backend) + M5.2 (frontend) follows the M3/M4 pattern of bounding cross-stack milestones; it let M5.1 land an integration-tested runtime before any chrome was committed. M5.2 inherits two specific carryovers from M5.1's deferred list:

1. **FR-082 (explicit-close action)** was deferred at the M5.1 spec layer with the comment *"deferred to M5.2"* — the supervisor's idle-sweep handles abandoned sessions, but an operator-driven "End thread" affordance is a UI-coupled feature that only exists once a UI exists.
2. **The orphan-operator-row sweep mitigation** (M5.1 retro §Open Q 6) — the polish-round mitigation for the corner case where the supervisor crashes between operator INSERT and assistant pending-row creation. The sweep can land in M5.2 as a polish-round backend tweak; the UI side renders the resulting cleared state without a custom path.

M5.2 also closes the four un-pinned M5.1 SCs by extending the test harness:

- **T020** — Playwright golden-path against the full M5.1 stack (testcontainer Postgres + Infisical + `garrison-mockclaude:m5` chat image + supervisor + standalone dashboard). Closes SC-010 (12-min runtime guard + 10s round-trip wall-clock from operator INSERT to assistant terminal commit) and SC-011 (token rotation E2E with no supervisor restart).
- **`chaos_m5_1_test.go`** — real docker-kill chaos against the supervisor + docker-proxy + `garrison-mockclaude:m5` end-to-end. Closes SC-006 (live SIGTERM cascade) and FR-101 (external-kill scenario). The retro phrased these as "before or in parallel with M5.2 UI work"; bundling into M5.2 is a deliberate call (they share the harness with T020 and they pin M5.1 SCs that would otherwise stay un-pinned through the full M5.2 cycle).

The "read-only" scope (per `ARCHITECTURE.md:574`) carries over from M5.1 unchanged. M5.2 does not introduce a `garrison-mutate` MCP server, does not add any chat-driven server action that mutates state outside the chat domain, and does not amend the threat model. Mutations remain M5.3 territory behind the threat-model amendment that milestone takes on.

---

## In scope

### Three-pane layout sitting in the existing dashboard shell

The chat surface ships as a route under the existing `dashboard/app/[locale]/` tree (path is spec-implementation territory; conventional candidates are `/chat`, `/ceo`, or `/ceo-chat`). The page is a three-pane composition:

- **Left rail** — the existing `components/layout/Sidebar.tsx`. Adds one new entry, `CEO chat`, with an active-state indicator (filled dot or per-state badge) when the operator is on the chat route. The other left-rail items (Org overview, Memory hygiene, Agents, Activity) are M3-existing; M5.2 does not modify them.
- **Center pane** — the chat thread. Top: thread header. Middle: scrolling message stream. Bottom: composer.
- **Right pane** — a static placeholder for M5.4. The pane is rendered (so layout is stable) but its contents are an empty-state component naming "WHAT THE CEO KNOWS" with copy clarifying that the knowledge-base view ships in M5.4. Spec decides exact copy and visual treatment (consistent with `EmptyState.tsx` from M3 is the obvious default).

Mobile/responsive posture is the same ≥768px stance M3 + M4 hold (per M3 retro §"What this milestone is NOT" and M4 retro §ditto). The chat UI is desktop-shaped; tablet width works because of the responsive breakpoints, but M5.2 does not redesign for narrow viewports.

### Center pane: chat thread surface

**Thread header** carries the thread label (auto-titled — open question §Open Questions), `started <time>`, `<turns>` count, and the running per-session cost (`$0.14`-shaped). A `+ New thread` button sits in the top-right of the center pane (or the top bar — placement is a UX call). A right-aligned overflow menu carries the thread-level actions: rename, archive, delete, **and "End thread" (FR-082)**.

**Message stream** renders `chat_messages` rows in `(turn_index, role)` order. Operator messages right-aligned in a darker bubble; assistant messages left-aligned with a CEO avatar (the persona framing — "C  CEO · haiku") and the model name as a cosmetic badge (the badge displays whichever model the chat image runs; M5.2 ships no model-picker — the badge is read-only display only). Per-message cost rendered in the message footer in `font-tabular` mono per the M3 design language.

**Streaming UX**: the SSE consumer reads `delta` events and appends to the in-flight assistant bubble; on the terminal `result` event, the bubble locks and final cost lands. Reconnect via `Last-Event-ID` mid-stream resumes from the next *terminal* event for that message (FR-052 in M5.1 spec) — the consumer that reconnects mid-stream renders the partial accumulated buffer it already has and waits for the terminal result.

**Composer** is a textarea with a footer hint. Footer carries:
- A **palace-live indicator** (chip rendering whether the MemPalace MCP server is reachable from the supervisor's perspective). The supervisor doesn't currently expose MCP-server-health on a per-session basis to the dashboard; M5.2 either reads from a new health endpoint, derives from the supervisor's existing `agents.changed`-style notify pattern, or defers the chip's data source to the spec. Chip default state is "live" with a fallback to "stale" / "unavailable" when the supervisor cannot confirm reachability — exact thresholds are a spec call.
- A **send affordance** (`send  ⌘↵`) reusing the existing `Kbd.tsx` primitive for the keyboard hint.
- An **explanatory caption** ("CEO will be summoned when you send. Each message spawns a fresh process.") — copy is approximate; the spec writes final copy.

**Idle pill** in the topbar reflects the chat session's idle state, surfaced from `chat_sessions.status` (`active` → "active" pill, recent-but-quiet → "idle" pill, ended/aborted → "ended"/error pill). The threshold for "active → idle" rendering is tied to the supervisor's `GARRISON_CHAT_SESSION_IDLE_TIMEOUT` (default 30 min per FR-081) — the UI shows the pill state derived from session row state, not a separate UI clock. **Tracked-but-deferred**: when the warm-pool / Claude pool optimization lands (post-M5), the topbar gains a parallel "all systems nominal" chip showing pool health; M5.2 does not ship that chip — only the per-session idle pill.

### Multi-session UX

The thread history list shows the operator's recent threads, sourced from `listSessionsForUser` (already implemented in M5.1's substrate). Layout placement of the list is a spec decision (the wireframe shows it in the right pane, but the right pane is M5.4-bound; candidates are: left-rail subnav under "CEO chat", a dropdown anchored on the thread header, a modal opened from the `+ New thread` adjacent menu, or a collapsible region in the right-pane placeholder).

Per-thread affordances:
- **Switch** — clicking another thread loads its transcript via `getSessionWithMessages` and re-attaches the SSE stream to the new `session_id`.
- **Auto-titling** — the spec picks a mechanism (derive from first operator message via deterministic truncation, ask Claude for a one-line title via a separate spawn, manual rename, or numbered `#142`-shaped). The wireframe shows numbered (`thread #142`); the spec confirms.
- **Archive** — soft-delete (set `chat_sessions.status='archived'` with M5.2 schema delta if the column doesn't exist; or filter via a tag column).
- **Delete** — hard-delete a thread the operator owns. Mirrors the M4 vault-`deleteSecret` shape (server action with audit). **Confirmation tier is a spec decision** — single-click feels wrong for hard-delete (loses transcript permanently); typed-name feels heavy for a personal thread. The M4 `ConfirmDialog` `tier='single-click'` is the lighter default; the spec picks.

The supervisor's existing restart/idle sweeps (FR-081, FR-083) keep the session lifecycle self-healing without UI involvement; M5.2 does not need to handle the "operator left tab open for 4 hours" case in client code.

### FR-082 explicit-close server action + UI affordance

A new `endChatSession(sessionId)` server action in `dashboard/lib/actions/chat.ts`. Behavior:
- Verify the calling operator owns the session (better-auth user_id matches `chat_sessions.started_by_user_id`).
- UPDATE `chat_sessions SET status='ended', ended_at=NOW() WHERE id=$1 AND status='active'`.
- Emit `pg_notify('work.chat.session_ended', chat_session_id)` so the activity feed surfaces the event.
- Refuse if there's an in-flight assistant message (`status IN ('pending','streaming')` on the latest row) or wait for it to terminate first — spec picks. The latter is more user-friendly; the former is simpler.
- Return the updated session row so the UI can re-render.

UI affordance: an "End thread" item in the thread overflow menu, gated by the M4 `ConfirmDialog` (single-click tier — hard-close doesn't delete data, only marks the session ended). Once ended, the composer disables and an empty-state hint surfaces ("This thread is ended. Start a new one to keep talking."). Subsequent operator INSERTs on an ended session already surface as `error_kind='session_ended'` per FR-081.

### Orphan-operator-row sweep mitigation (M5.1 polish carryover)

The M5.1 retro §Open Q 6 names this corner case: if the supervisor crashes between operator INSERT and the assistant `pending` row creation, the operator row sits at `status='completed'` with no assistant counterpart, and the M5.1 restart sweep (FR-083) doesn't catch it because the sweep filters on `status IN ('pending','streaming')` of the *newest* row.

The mitigation is supervisor-side: the boot-time sweep also looks for "session whose newest message is `role='operator'` without an assistant pair, > 60s old" and either re-emits `chat.message.sent` to retry the spawn or terminal-writes a synthetic assistant row with `error_kind='supervisor_restart'`. Implementation choice (retry vs error-write) is a spec call; the UI side renders the resulting cleared state without a custom path. The change is a `~10-line` sweep extension in `internal/chat/restart.go`, not a new package.

### Cost telemetry surface

Two visible elements for the M5.1-rolled `total_cost_usd`:
- **Per-session badge** in the thread header (`$0.14`-shaped, mono tabular).
- **Per-message cost** in the assistant bubble footer (`$0.0234` per turn, mono tabular).

The data is already in `chat_sessions.total_cost_usd` and `chat_messages.cost_usd` — M5.2 reads both via `getSessionWithMessages` (cost per row) and `getRunningCost` (the rolled-up session total). No new query work.

### Test coverage extending M5.1

**T020 Playwright e2e** — extends `dashboard/tests/integration/_chat-harness.ts` (or its M5.2 successor) to boot the full stack: testcontainer Postgres + Infisical + the `garrison-mockclaude:m5` chat image + the supervisor process + the standalone dashboard bundle. Asserts SC-010 (round-trip ≤10s) and SC-011 (token rotation flow) via real DOM interactions. The chat-image build step is idempotent (cached after first run); first-run cost is a one-time CI infra step. The full Playwright suite (M3 + M4 + M5.1 + M5.2) must stay under 12 minutes total per SC-010.

**`chaos_m5_1_test.go`** — real docker-kill against the supervisor + docker-proxy + `garrison-mockclaude:m5` end-to-end. Closes SC-006 (live SIGTERM cascade — container gone within 5s, terminal write committed with `error_kind='supervisor_shutdown'`, SSE typed error frame) and FR-101 (external-kill scenario — container killed via `docker kill` from outside; supervisor detects, cleans up, terminal-writes `error_kind='container_crashed'`). Lives in `supervisor/internal/chat/` next to the existing chat package tests; uses the same testcontainer harness M5.1 already established.

The new M5.2 dashboard tests follow the M3/M4 pattern: Vitest unit tests for the SSE consumer, the message-stream component, the composer, and the `endChatSession` action; Playwright integration tests for the user-facing flows (open thread, send message, see streaming response, end thread, switch thread, see cost update, see idle pill flip).

### Wireframe in prose (operator reference)

The operator shared a wireframe screenshot during context-write. Describing here so the spec can decide UX details without depending on a single sketch. Prose only — the wireframe is operator-side reference, not committed to the repo.

The wireframe shows a three-pane dark-themed layout. The left rail (~190px) is the existing dashboard sidebar with a search bar at top (out of M5.2 scope — this is the global ⌘K palette, not chat search), an `Anton` company switcher header, and a vertical nav: `Org overview`, `Departments` (count 6), `CEO chat` (active, dotted indicator), `Hiring` (count 3 — out of M5.2; M7), `Memory hygiene` (count 14), `Skills` (out of M5.2; M7), `Agents` (count 17), `Activity`. Bottom-left has a global widget showing `7 agents live · $0.42/hr · 14.2k tok/min` (operator-dashboard chrome — not chat-specific; either pre-existing or a separate concern) and the operator user-menu (`anton@local · operator`).

The center pane has a top breadcrumb (`Anton / CEO chat`) plus topbar actions: `+ New thread`, the **idle pill** with a status dot, an `all systems nominal` chip with a green dot (this chip is the "all systems go" indicator that ships *with the warm-pool optimization* — M5.2 does NOT ship it; only the idle pill), and two icon buttons (notification + telemetry — out of M5.2 if they're not chat-specific). Below that, the thread header: `C  CEO · thread #142  started 13:58 · 4 turns · $0.14` with an overflow menu. The message stream alternates operator messages (right-aligned dark bubble: *"Why is growth amber on the overview?"*) and assistant messages (left-aligned, `C  CEO · haiku` header, body text, then **inline tool-call chips** showing what tools the assistant called — `queried tickets.growth where column = analyzing and age > 24h · 2 results`, `read palace/gro/diary/2026-04-20.*.md · 0 matches`, `inspected agent_instance.gro/analyst.01.exit_log · 142 lines`). **Tool-call chips are deferred to v1.1** — M5.2 does not surface them. After the assistant body, the wireframe shows a green pill action (`Queue hire`) — **that's hiring (M7 territory) and is OUT of M5.2 entirely**. Composer at bottom: `Message the CEO...` placeholder, footer hint `ctx: 2 files · palace live`, send button with `⌘↵` keyboard shorthand.

The right pane shows `WHAT THE CEO KNOWS · context · 12.4k tok` with three tabs (`Company.md` / `Recent palace writes` / `KG recent facts`), a `company.md · always in context · 842 lines · edit` row, and an inline rendered company.md preview. **The right pane content is M5.4** — M5.2 ships a static placeholder in that pane only. Below the knowledge-base section, the wireframe shows `THREAD HISTORY` with prior threads (`#142` active, `#141`, `#140`, `#139`). The thread-history surface is M5.2-scope; its placement is a UX call (right pane, left rail, dropdown, etc.) — see Open Questions §1.

---

## Out of scope

### Chat-driven mutations (M5.3)

The `BuildChatConfig` precheck enforces `postgres + mempalace` only — no `garrison-mutate` server, no `finalize` server. M5.2 does not introduce a mutation MCP server, does not add any tool the chat container can call to write state, and does not add a confirmation-gate UI for chat-driven mutations. The threat-model amendment for chat-driven mutations is M5.3 territory.

### "WHAT THE CEO KNOWS" knowledge-base pane (M5.4)

Three tabs (Company.md / Recent palace writes / KG recent facts), the always-in-context preview of `company.md`, the in-dashboard edit affordance for `company.md`, and the per-thread context-token counter all ship in M5.4 — a new milestone split out from M5.2 during context-write. M5.2 ships only the right-pane shell with a static placeholder.

### Inline tool-call chips → v1.1

Surfacing `tool_use` / `tool_result` events in the message stream as chips (`queried ...`, `read palace/...`, `inspected agent_instance/...`) is deferred to v1.1 (post-v1 release polish). M5.1's SSE only emits `delta` + `terminal` events; the parser changes to surface tool events live in M5.1's `claudeproto.OnStreamEvent` extension hook but are not wired through to the SSE in M5.1, and M5.2 does not wire them through either.

### "All systems nominal" chip — paired with warm-pool

The topbar chip showing pool/system health ships *with* the warm-pool / Claude pool optimization (post-M5). M5.2 ships only the per-session idle pill. The pairing is intentional: the chip's data source is pool state, which doesn't exist until the pool does.

### Warm-pool / Claude pool optimization — tracked, not addressed

Per the M5.1 retro §Open Q 4 (RATIONALE 127 follow-up): each turn spawns a fresh container; cold-start adds measurable wall-clock to the first delta. The spike measured ~1.4s on warm cache (§8.1). If operator-week-of-use surfaces this as friction, M5.3 or post-M5 may layer in a warm-pool pattern. **M5.2 does not address it but explicitly tracks it in the M5.2 retro forward-look** so it doesn't drop off the radar between M5.2 and M5.3.

### Hiring affordances — M7

The wireframe's `Hiring` left-rail entry (count 3), `Skills` left-rail entry, and the `Queue hire` action chip in the assistant message are all M7 territory. M5.2 does not surface a hiring affordance, does not enable a `Queue hire` button, and does not include the hiring page. The left-rail entry can render as disabled/placeholder with a "ships in M7" hover affordance (mirroring M3's pattern for the disabled activity-feed filter chips), or simply be omitted in M5.2 — spec picks.

### Global ⌘K command palette

The `Search... ⌘K` input at the top of the left rail in the wireframe is a global command palette pattern. Out of M5.2. No chat-specific search ships either — chat-history search is post-M5 (M5.1 retro §Out of scope).

### Multi-operator / multi-CEO

Single-operator single-tenant continues from M3. Per-operator chat isolation, multi-CEO coordination, parallel-operator chat sessions — all post-M5.

### Mobile-first design

Same ≥768px stance as M3 and M4. M5.2 is responsive at ≥768px and ≥1024px and ≥1280px breakpoints (matching the M3 polish round); sub-768px is out of scope.

### Cost-telemetry blind-spot fix and workspace-sandbox-escape fix

Both still surfaced from M3, still not fixed. M5.2 does not address either. Tracked at `docs/issues/cost-telemetry-blind-spot.md` and `docs/issues/agent-workspace-sandboxing.md` respectively.

### Per-day / per-month cost caps

M5.1 ships only the per-session soft cap (FR-061). FR-062 (per-day, per-month) stays out of scope; M5.2 does not add a date-windowed cap.

### Model picker

The `haiku` badge on the assistant message is **cosmetic display only** of whichever model the chat image runs (`GARRISON_CHAT_CONTAINER_IMAGE` per FR-010). M5.2 does not ship a model picker; M5.2 does not let the operator switch models per turn or per session.

### Threat-model amendment

Read-only chat does not require a threat-model amendment — the existing models stay valid. The amendment lands when M5.3 ships chat-driven mutations.

### Chat-history search

Operator-side cross-session search is post-M5. The M5.1 schema persists everything (`raw_event_envelope` JSONB); the search surface comes later.

### Replacing or revising any sealed M2-arc, M3, M4, or M5.1 surface

M5.2 is additive. No changes to existing tables, columns, server actions, queries, or routes outside the chat domain.

---

## Open questions the spec must resolve

These are decisions deliberately left for `/speckit.specify` + `/speckit.clarify` to make.

1. **Where does thread history live?** Right-pane (alongside the M5.4 placeholder) keeps the wireframe layout but mixes M5.2 + M5.4 content. Left-rail subnav under "CEO chat" matches the M3 sidebar pattern. A dropdown anchored on the thread header is compact but discovers worse. A modal opened from `+ New thread` adjacent menu is heaviest. Spec picks.

2. **Auto-titling mechanism.** Numbered (`thread #142`) is deterministic and free. Derive-from-first-message via deterministic truncation is more useful but requires a transformation. Ask-Claude-for-a-title via a separate background spawn is informative but spends $. Manual rename is a fallback. Spec picks (and the column on `chat_sessions.claude_session_label` already exists per FR-040 — it's nullable, so deferring rename UX to a polish round is also valid).

3. **`+ New thread` interaction.** Two shapes: (a) clicking immediately creates an empty session row and routes to `/chat/<new-uuid>` with a focused composer, or (b) opens a modal asking for the first message before creating the row. (a) is friendlier for "I'll figure it out as I type"; (b) avoids creating rows the operator never uses (and potentially breaches the cost cap with rapid empty-session churn under stress). Spec picks.

4. **Delete-thread confirmation tier.** Single-click (M4 `ConfirmDialog tier='single-click'`) is lightweight; typed-name (`tier='typed-name'`) is heavy. A personal thread isn't shared state, but a transcript with cost rollup is forensically valuable. Spec picks. Default recommendation: single-click, with the deleted row staying in `chat_sessions` for N days at `status='archived'` rather than being hard-deleted on day 1 — but that's a spec call.

5. **Palace-live chip data source.** The supervisor doesn't currently expose MCP-server-health on a per-session basis. Options: (a) supervisor adds a `/health/chat-mcp` endpoint the dashboard polls, (b) the chip flips to "stale" purely on time-since-last-successful-tool-call from the existing `chat_messages.raw_event_envelope`, (c) the chip is informational only and always displays "live" when a session is active. Spec picks.

6. **Idle pill thresholds.** The M5.1 schema gives `chat_sessions.status` (`active|ended|aborted`) and `chat_messages.created_at`. The pill's "active → idle" rendering threshold is a UI call — does the pill flip at 5 min of silence, at the supervisor's `GARRISON_CHAT_SESSION_IDLE_TIMEOUT - 5min`, at the moment the supervisor flips `status='ended'`, or based on operator-tab-focus? Spec picks.

7. **SSE reconnect semantics on tab-switch / browser-suspend.** The browser suspends EventSource connections under some power conditions; the dashboard's M3 listener already handles reconnect with `Last-Event-ID`. M5.2 confirms the chat SSE consumer reuses M3's 5-state listener machine (`dormant/connecting/live/backoff/idle-grace`) or builds a chat-specific variant; the spec also confirms the `Last-Event-ID` semantics for chat (FR-052: deltas are not replayable post-disconnect; only terminal events are).

8. **Empty-state shapes.** Three shapes the spec writes copy + visual treatment for: (a) no chat threads ever (first-time operator), (b) current thread with no messages yet (just clicked `+ New thread`), (c) current thread is ended/errored (`session_ended`, `vault_token_expired`, `session_cost_cap_reached`, etc.). The M3 `EmptyState.tsx` primitive is the obvious base.

9. **Streaming UX detail.** Token-by-token typewriter? Append-as-arrived (current pattern, simpler)? A blinking cursor at end of in-flight bubble? Auto-scroll to bottom or sticky-scroll-on-newline? Spec picks; the M3 polish-round taste favors "append, no typewriter, sticky-bottom unless operator scrolled up".

10. **Internationalization scope.** M3 set up next-intl with `localePrefix: 'as-needed'`. Does M5.2 add chat-related strings to `messages/en.json` on day 1, or ship English-only literal-string and migrate them in a future polish round (M3 itself shipped some literals that the M4 polish round caught — same pattern is acceptable)? Spec picks.

11. **Wireframe-shown bottom-left widget (`7 agents live · $0.42/hr · 14.2k tok/min`).** The widget is operator-dashboard chrome, not chat-specific — is it pre-existing M3/M4 work that M5.2 inherits unchanged, or net-new in M5.2? Quick repo check or operator confirmation; if net-new, spec confirms whether it lands in M5.2 or splits.

12. **Architecture amendment for the M5 split.** `ARCHITECTURE.md:574` describes M5 as one milestone. With the M5.4 split-out introduced in this context-write, the architecture line wants an amendment to enumerate M5.1 + M5.2 + M5.3 + M5.4. Operator decides timing: (a) amend now alongside this context, (b) amend during /speckit.specify, (c) defer to retro time. Spec acknowledges either way.

13. **Chaos test scenarios beyond container kill.** `chaos_m5_1_test.go` covers SC-006 + FR-101 (SIGTERM + external-kill). Adjacent failure modes worth asserting: network blip from chat container to docker-proxy mid-stream, Postgres connection loss mid-stream, pg_notify channel buffer overflow under burst. M5.1's spec only requires the container-kill case; the spec confirms whether M5.2's chaos test extends to the adjacent cases or stays minimal.

14. **`endChatSession` while a turn is in-flight.** Two reasonable behaviors: (a) reject the close call until the in-flight turn terminates, OR (b) accept, mark `status='ended'`, let the in-flight turn finish writing its terminal row but ignore subsequent messages from that operator on this session. Spec picks; (b) is more user-friendly ("I want out now, finish what you were saying").

15. **Test fixture for the chat SSE mid-disconnect / mid-reconnect.** The M3 listener has `Last-Event-ID` reconnect; chat needs the same fixture pattern. M5.2's Playwright must drive at least one mid-stream disconnect to assert the consumer doesn't double-render or lose the terminal event. Spec confirms whether T020 includes this assertion or whether it's a separate Vitest unit test.

---

## Acceptance criteria framing

The spec writes the full SC list. Framing for the spec to fill in:

- A live operator opens the CEO chat surface in the dashboard, types a message, hits send, and sees the first assistant delta render within `<bound>` seconds against the full M5.1 stack (real `garrison-claude:m5` or `garrison-mockclaude:m5` per FR-100).
- The message stream renders operator + assistant turns in `(turn_index, role)` order with no gaps and no duplicates across an N-turn session.
- The per-session cost badge in the thread header updates after each assistant terminal commit; the per-message cost in each assistant bubble matches `chat_messages.cost_usd`.
- The composer's palace-live indicator reflects MemPalace MCP reachability per the spec-resolved data source.
- Clicking `+ New thread` produces a new empty thread (or a modal — per Open Q 3) the operator can immediately type into.
- The thread history surface lets the operator switch between threads; switching loads the new transcript via `getSessionWithMessages` and re-attaches the SSE stream to the new `session_id`. No SSE stream from the previous thread leaks deltas to the new thread.
- `endChatSession(sessionId)` server action: the "End thread" affordance closes the session, marks `chat_sessions.status='ended'`, emits `pg_notify('work.chat.session_ended', ...)`, and disables the composer with an end-state empty-state hint. Subsequent operator messages on the ended session surface as `error_kind='session_ended'`.
- The orphan-operator-row sweep mitigation: a session with a `role='operator'` newest message older than 60s with no assistant counterpart gets resolved within 60s of supervisor boot (re-spawn or synthetic-error-write per spec).
- Topbar idle pill flips between active / idle / ended states per the spec-resolved threshold; the visual matches the M3 design language (`StatusDot`, `Chip` primitives).
- The cosmetic CEO model badge displays the chat image's model name; clicking it does nothing (read-only display).
- Mobile/responsive: 768/1024/1280 layout pass, mirroring the M3 + M4 responsive integration suites.
- T020 Playwright: full M5.1 + M5.2 stack passes; SC-001 (5s first-delta), SC-010 (10s round-trip + 12-min suite), SC-011 (token rotation E2E) all CI-pinned.
- `chaos_m5_1_test.go`: docker-kill mid-stream → container gone within 5s, terminal write committed with `error_kind='container_crashed'`, SSE typed error event emitted, next operator message on the same session starts cleanly. SC-006 + SC-009 + FR-101 all CI-pinned.
- Vault leak-scan parity: a grep of dashboard logs, browser-side logs, and SSE event payloads during a test session finds zero `CLAUDE_CODE_OAUTH_TOKEN` substring and zero `sk-`-shaped sentinel values planted in test palace contents (M5.1 SC-005 carryover; M5.2 doesn't add any new path that could leak, but the test runs against the full stack).
- The chat MCP config still contains exactly the M5.1-allowed `postgres` + `mempalace` entries — verified by `BuildChatConfig` test passing; the M5.2 surface does not introduce a third entry (defensive: SC-007 carryover from M5.1).
- Zero new direct dependencies in `dashboard/package.json` OR every new dependency is justified in commit messages and recapped in the M5.2 retro per the M3-established dependency discipline.
- Vault-log analyzer (`vaultlog`) passes on any new chat code paths.

---

## What this milestone is NOT

- **Not a chat-driven mutation surface.** No `garrison-mutate` MCP server. No tool the assistant can call to write state. No typed-name confirmation gates for chat-content actions. Mutations are M5.3.
- **Not a knowledge-base / context-observability pane.** The right pane is a static placeholder; the Company.md / palace-writes / KG-facts content is M5.4.
- **Not a hiring page.** No `Queue hire` button, no `/hiring` route, no skill-browser. M7 territory.
- **Not a global ⌘K command palette.** Out of scope.
- **Not a chat-history search surface.** Persistence-only; search is post-M5.
- **Not a model picker.** Cosmetic badge only.
- **Not a warm-pool optimization.** Tracked in the M5.2 retro forward-look; not addressed.
- **Not the "all systems nominal" topbar chip.** That ships paired with the warm-pool when it lands.
- **Not a multi-operator system.** Single-operator continues.
- **Not a mobile-first redesign.** ≥768px continues.
- **Not a fix for the cost-telemetry blind spot or the workspace-sandbox-escape issue.** Both still surfaced from M3.
- **Not a per-day or per-month cost cap.** Per-session soft cap continues from M5.1.
- **Not a threat-model amendment.** Read-only stance keeps the existing models valid; the amendment lands when M5.3 ships mutations.
- **Not a tool-call inline chip surface.** Deferred to v1.1.
- **Not a replacement or revision of any sealed M2-arc, M3, M4, or M5.1 surface.** Additive only.

---

## Spec-kit flow

1. **Now**: `/speckit.specify` against this context. Spec resolves the open questions inline (or flags the ones genuinely needing a clarify round). Specifically: thread-history placement, auto-titling mechanism, `+ New thread` shape, delete-thread confirmation tier, palace-live chip data source, idle-pill threshold, SSE reconnect semantics, empty-state copy, streaming UX detail, i18n scope, the bottom-left widget question, the `endChatSession` in-flight behavior, chaos scenario coverage, mid-disconnect test fixture, and the architecture-amendment timing.
2. **Then**: `/speckit.clarify` only if the spec leaves residual ambiguity (the operator's wireframe + the M5.1 substrate's stable interface mean a lot is already pinned).
3. **Then**: `/speckit.plan` — picks the directory layout for `dashboard/components/features/ceo-chat/` (or analogous), the SSE consumer hook shape, the message-stream component composition (operator/assistant bubble variants, tool-result chip stub for v1.1), the composer state machine (idle/typing/sending/streaming/error), the thread-history surface placement, the `endChatSession` server action shape, the orphan-row sweep extension in `internal/chat/restart.go`, the T020 Playwright harness extension, and the `chaos_m5_1_test.go` chaos fixture.
4. **Then**: `/speckit.tasks` — turns the plan into ordered tasks with completion conditions. Expected shape: schema/migration deltas (if any — likely zero or one for archived-status), backend orphan-row sweep, `endChatSession` action + audit + notify, dashboard SSE consumer hook, message-stream component, composer component, thread-history surface, idle pill, cost telemetry surface, right-pane placeholder, T020 Playwright extension, `chaos_m5_1_test.go` extension, `messages/en.json` migration if i18n scope is in.
5. **Then**: `/speckit.analyze` — checks for spec/plan/tasks consistency, flags any FR/SC inconsistencies between M5.1 and M5.2.
6. **Then**: `/garrison-implement` (or `/speckit.implement`) — task-by-task execution.
7. **Then**: M5.2 retro at `docs/retros/m5-2.md`, MemPalace mirror per AGENTS.md §Retros, and the M5.3 context (chat-driven mutations + threat-model amendment) can start.
