# Feature Specification: M5.2 — CEO chat dashboard surface (read-only)

**Feature Branch**: `011-m5-2-ceo-chat-frontend`
**Created**: 2026-04-28
**Status**: Draft
**Input**: M5.2 context (`specs/_context/m5-2-context.md`); M5.1 spec (`specs/010-m5-1-ceo-chat-backend/spec.md`); M5.1 retro (`docs/retros/m5-1.md`); M5 spike (`docs/research/m5-spike.md`).
**Scope marker**: frontend half of M5. Sits on M5.1's substrate (server actions, SSE producer, transcript reads, idle + restart sweeps) without changing it. M5.3 takes chat-driven mutations + threat-model amendment; M5.4 takes the knowledge-base "WHAT THE CEO KNOWS" pane.

## Clarifications

### Session 2026-04-28 (operator-approved during /garrison-specify)

Eighteen binding decisions resolved before spec drafting; they shape the FRs below rather than appearing as inline `[NEEDS CLARIFICATION]` markers.

- Q: Where does thread history live in the M5.2 layout? → A: Left-rail subnav under "CEO chat" (collapsible, last 10 + "view all" link). Matches M3 sidebar pattern; keeps right pane clean for the M5.4 placeholder.
- Q: How are threads titled? → A: Numbered (`thread #N`) only in v1. Manual rename + LLM-titling are post-M5 polish.
- Q: What happens when the operator clicks "+ New thread"? → A: Click → create empty session row → route to `/chat/<uuid>` with focused composer. Empty session has no spawn (no cost cap risk per M5.1 FR-061's reactive-only check).
- Q: What is the confirmation tier for hard-delete a thread? → A: Single-click `ConfirmDialog`. Personal data, not shared state; M4's typed-name tier is reserved for cross-cutting destruction (vault secret in use).
- Q: How is "archive thread" represented in the schema? → A: New `chat_sessions.is_archived BOOLEAN NOT NULL DEFAULT false` column. Does NOT extend the M5.1 FR-040 `status` enum — archive is a display flag, not a lifecycle state.
- Q: What feeds the composer's "palace live" indicator? → A: Time-since-last-successful-tool-call from `chat_messages.raw_event_envelope`. Reuses existing data; no new supervisor endpoint.
- Q: What threshold flips the topbar idle pill? → A: Pill reads directly off `chat_sessions.status` — `active` → green ("active"), `ended` (per M5.1 FR-081 idle sweep) → yellow ("idle"), `aborted` → red ("aborted"). No client-side clock; single source of truth.
- Q: How do tokens render during streaming? → A: Append-as-arrived; blinking cursor at end of in-flight bubble (steady when `prefers-reduced-motion: reduce`); sticky-bottom unless operator scrolled up (then "↓ N new" pill).
- Q: When the SSE stream disconnects mid-turn, how does the consumer reconcile? → A: Reuse M3's 5-state listener machine pattern. Deltas are not replayable post-disconnect (per M5.1 FR-052). Render partial accumulated buffer + wait for terminal `result` event.
- Q: Does the M5.2 chaos test extend beyond M5.1's container-kill case? → A: No. Stays minimal — only SC-006 (SIGTERM cascade) + FR-101 (external docker-kill). Network-blip / Postgres-loss / pg_notify-buffer cases are M5.3 scope when mutation chaos compounds.
- Q: Where does the mid-disconnect SSE assertion live? → A: T020 Playwright sub-scenario. Drive one mid-stream client disconnect + reconnect; assert no double-render and terminal `result` received.
- Q: i18n strategy for chat copy in M5.2? → A: English-only literal strings on day 1; migrate to `messages/en.json` in a polish round. Matches M3's literal-then-polished pattern caught by M4.
- Q: Operator clicks "End thread" while a turn is streaming. What happens? → A: Accept immediately (`UPDATE status='ended'`). The in-flight turn finishes its terminal write naturally; subsequent operator messages on the ended session surface as `error_kind='session_ended'` per M5.1 FR-081. More user-friendly than rejecting.
- Q: The wireframe's `$0.42/hr · 14.2k tok/min` widget — in or out of M5.2? → A: Out. The base `<liveAgents> live · <liveAgents>/<totalCapacity>` widget already exists in M3 (`dashboard/components/layout/Sidebar.tsx:99-112`); the wireframe's rate-fields extension is operator-dashboard chrome, not chat-specific. Defer to a separate operator-tooling milestone.
- Q: When does the M5 architecture line at `:574` get amended? → A: As part of this M5.2 spec's scope. One-line replacement enumerating M5.1/M5.2/M5.3/M5.4. Keeps committed docs aligned with reality before the spec ships.
- Q: How is the wireframe's "Hiring" left-rail entry handled? → A: Omit entirely from M5.2. No disabled placeholder. Hiring is M7 territory; rendering an inert affordance would create a hiring-shaped surface that signals scope creep.
- Q: Cost rendering precision? → A: 4 decimals (`$0.0234`) per message in the assistant bubble footer; 2 decimals (`$0.14`) at session header. Small turns round to $0 at 2 decimals.
- Q: Empty-state shapes? → A: Three EmptyState variants — (a) no threads ever, (b) empty current thread, (c) ended/errored thread with per-`ChatErrorKind` copy. All built on the M3 `EmptyState.tsx` primitive.

### Session 2026-04-28 (/speckit.clarify pass)

- Q: When the operator opens a thread in `chat_sessions.status='aborted'`, what does the UI render? → A: Render the failed assistant bubble inline showing `error_kind` reason; composer stays enabled; next operator turn proceeds as a new message in the same session. Honors M5.1 SC-009's contract that the next message on an aborted session starts cleanly. No retry-spawn affordance — the operator types a fresh message instead.
- Q: What does SC-200 measure for the M5.2 wall-clock budget? → A: 5s end-to-end (operator click → first DOM-rendered delta), inclusive of the dashboard render hop. Mirrors M5.1 SC-001's 5s budget exactly; one wall-clock guard catches regressions across the whole stack.
- Q: Do `vault_access_log` rows cascade-delete when the operator deletes a chat thread? → A: No. `vault_access_log` rows survive thread deletion as an immutable forensic trail. The `metadata.chat_session_id` reference becomes dangling by design — "this token was revealed at <time> in a now-deleted chat" stays queryable. `garrison_dashboard_app` keeps its M4 INSERT-only grant on vault tables; no DELETE grant added.
- Q: What accessibility posture does M5.2 ship? → A: WCAG 2.1 AA target for the chat surface. Explicit FRs for chat-specific a11y concerns: `aria-live="polite"` + `aria-busy` on the in-flight assistant bubble, color-not-only state for status indicators (idle pill, palace-live chip), keyboard-only thread switching. One Playwright axe-core assertion added to T020 against golden-path renders. (FR-330 through FR-334.)

---

## User Scenarios & Testing

### User Story 1 — First chat session against current state (Priority: P1)

The operator opens the dashboard's CEO chat surface for the first time. The left rail shows a "CEO chat" entry; clicking it lands on an empty-state ("Start a thread with the CEO"). The operator clicks "+ New thread", lands in a fresh session route with a focused composer, types *"how many tickets are stuck in qa-review for engineering?"*, hits send (or `⌘↵`), and sees the assistant's tokens stream into the message thread within seconds. When the response completes, the per-session cost badge updates and the topbar idle pill stays "active" (green). All of this against the M5.1 substrate without the operator ever touching SQL.

**Why this priority**: load-bearing. Without this story working, every other M5.2 story is moot. Ships an MVP that lets the operator actually use the chat.

**Independent test**: with an authenticated better-auth session and a real `CLAUDE_CODE_OAUTH_TOKEN` seeded in Infisical (or `garrison-mockclaude:m5` per M5.1 FR-100), navigate to the chat route, click "+ New thread", type a message, submit. Assert: composer disables during streaming, deltas append-as-arrived, terminal commit lands, per-session cost badge shows non-zero `$X.XX`, per-message cost shows non-zero `$0.XXXX` in the assistant bubble footer, idle pill is green/"active", `chat_messages` rows for both turn-0 (operator) and turn-1 (assistant) exist in DB.

**Acceptance scenarios**:

1. **Given** an authenticated operator with no prior chat threads, **When** they navigate to the chat route, **Then** they see an empty-state with copy "Start a thread with the CEO" and a primary "+ New thread" CTA.
2. **Given** the empty-state is visible, **When** the operator clicks "+ New thread", **Then** a new `chat_sessions` row is created via the M5.1 substrate (`startChatSession` with empty content allowed, or a new lighter `createEmptySession` server action — implementation choice per /garrison-plan), the operator routes to `/chat/<uuid>`, and the composer is focused.
3. **Given** the operator types a message and presses `⌘↵`, **When** the message commits to `chat_messages` (role='operator'), **Then** the SSE consumer receives `delta` events and renders them append-as-arrived with a blinking cursor at end of bubble, until the terminal `result` event commits the assistant row.
4. **Given** streaming completes, **Then** the cursor disappears, the per-message cost (4 decimals) renders in the bubble footer, the per-session cost badge in the thread header (2 decimals) updates, and the composer re-focuses for the next turn.
5. **Given** the operator scrolls up during streaming, **When** new deltas arrive, **Then** the page does NOT auto-scroll; instead a "↓ N new" pill appears anchored to the message stream that, when clicked, scrolls to the bottom.

---

### User Story 2 — Multi-turn continuity in the same thread (Priority: P1)

The operator follows up with a second message in the same thread: *"and which agent has been working on those?"* — without restating context. The chat surface renders the new turn beneath the prior one in `(turn_index, role)` order; the M5.1 substrate replays the full transcript to Claude (per M5.1 FR-013), so context is preserved across turns. The operator sees the second assistant turn stream in the same thread without losing the first turn's content.

**Why this priority**: P1 because single-turn alone is a one-shot query box; multi-turn is what makes chat conversational. The M5.1 substrate already supports multi-turn replay; M5.2's job is to render it correctly.

**Independent test**: send turn 1 via the UI, wait for terminal commit. Send turn 2 referencing turn 1's content. Assert: the message stream shows both turns in chronological order, the assistant's response to turn 2 demonstrates context awareness (e.g., references "those tickets" from turn 1), the second result event's `cache_read_input_tokens > 0` per M5.1 SC-002 (verifiable via `chat_messages.raw_event_envelope`), no operator action was needed beyond typing the message and sending.

**Acceptance scenarios**:

1. **Given** turn 0 (operator) and turn 1 (assistant) are committed in the current session, **When** the operator types and sends turn 2 (operator), **Then** turn 2 appends to the message stream, the SSE consumer attaches a fresh stream for turn 3, and the assistant's response references content from turns 0-1.
2. **Given** an N-turn session with N≥3, **When** the operator scrolls through the message stream, **Then** all N turns render in `(turn_index, role)` order with operator messages right-aligned and assistant messages left-aligned, no gaps, no duplicates.
3. **Given** an N-turn session, **When** the operator reloads the page, **Then** the full transcript loads via `getSessionWithMessages` and renders identically; the SSE consumer attaches to the latest session_id and waits for the next operator message.

---

### User Story 3 — Switching between threads (multi-session) (Priority: P1)

The operator has three threads from prior sessions. Opening the chat surface shows the active thread; the left rail's collapsible "CEO chat" subnav lists the recent threads (numbered `thread #142`, `thread #141`, ...). Clicking another thread loads its transcript, detaches the SSE stream from the previous session, and re-attaches to the new session_id. No deltas leak between threads.

**Why this priority**: P1 because the chat is multi-session by design (the M5.1 substrate ships `listSessionsForUser` and per-session SSE keying). Without thread switching, only the most-recent thread is reachable, which makes the multi-session schema pointless.

**Independent test**: seed three `chat_sessions` rows owned by the test operator. Navigate to the chat route; assert the left-rail subnav shows the three thread entries in `started_at DESC` order. Click each thread; assert the transcript loads correctly (via `getSessionWithMessages`) and the URL updates to `/chat/<session-uuid>`. Send a message in thread A; switch to thread B mid-stream; assert no delta from thread A's stream renders in thread B.

**Acceptance scenarios**:

1. **Given** the operator owns N>0 chat threads, **When** they open the left-rail "CEO chat" subnav, **Then** they see the latest 10 threads in `started_at DESC` order with thread titles (numbered) + per-thread last-activity timestamp; if N>10 a "view all" link opens a full list view.
2. **Given** the operator is on thread A, **When** they click thread B in the subnav, **Then** the URL changes to `/chat/<thread-B-uuid>`, the transcript reloads via `getSessionWithMessages`, the SSE EventSource for thread A closes cleanly, and a new EventSource for thread B opens.
3. **Given** thread A has an in-flight assistant message (status='streaming'), **When** the operator switches to thread B, **Then** thread A's terminal write still commits server-side (the supervisor doesn't observe the client-side switch), but no further deltas render in the dashboard's thread-A view; switching back to thread A loads the now-completed transcript via `getSessionWithMessages`.
4. **Given** thread A is active, **When** the operator switches threads, **Then** no `delta` event payload from thread A's `pg_notify('chat.assistant.delta', ...)` reaches the thread-B render path. The SSE route already keys on `session_id` per M5.1 FR-052; the consumer must respect that.

---

### User Story 4 — Session lifecycle: end, archive, delete (Priority: P2)

The operator wants to close a thread they're done with. Opening the thread overflow menu reveals "End thread" (closes the session — supervisor's idle sweep would have done this in 30 min anyway, but the operator gets it now), "Archive" (hides from default list; reachable via "Archived" sub-view), and "Delete" (hard-removes the thread + transcript; single-click confirmation). Ending a thread mid-stream is accepted immediately; the in-flight turn finishes naturally, but subsequent operator messages bounce with `error_kind='session_ended'`.

**Why this priority**: P2 because the chat works without these affordances (M5.1's idle sweep handles abandoned sessions automatically), but they close FR-082's deferred end-action and give the operator agency over their own thread inventory. Archive + delete also close the M5.1 retro's open-question on operator-driven session housekeeping.

**Independent test**: create a thread, send a message, wait for terminal commit. Click "End thread"; assert `chat_sessions.status='ended'` within 1s, composer disables, ended-state empty-state renders, `pg_notify('work.chat.session_ended', ...)` lands. For archive: click "Archive"; assert thread filters out of default list, appears in "Archived" sub-view, `chat_sessions.is_archived=true`. For delete: click "Delete", confirm via single-click `ConfirmDialog`; assert `chat_sessions` row gone, `chat_messages` rows cascade-deleted, `pg_notify('work.chat.session_deleted', ...)` lands, thread gone from list.

**Acceptance scenarios**:

1. **Given** an active thread with no in-flight turn, **When** the operator clicks "End thread" in the overflow menu and confirms via single-click `ConfirmDialog`, **Then** within 1s the `endChatSession` server action UPDATEs `chat_sessions.status='ended', ended_at=NOW()`, the composer disables, an ended-state empty-state ("This thread is ended. Start a new one to keep talking.") renders below the message stream, and the topbar idle pill flips to yellow ("idle").
2. **Given** an active thread with an in-flight assistant message (status='streaming'), **When** the operator clicks "End thread", **Then** the `endChatSession` action accepts immediately, `chat_sessions.status` flips to `ended`, the in-flight turn continues streaming and commits its terminal row naturally, and any subsequent operator INSERT on this session bounces with `error_kind='session_ended'` per FR-081.
3. **Given** any thread (active, ended, or aborted), **When** the operator clicks "Archive" in the overflow menu, **Then** `chat_sessions.is_archived` flips to `true` via `archiveChatSession` server action, the thread filters out of the default left-rail subnav, and remains visible only in the "Archived" sub-view.
4. **Given** any archived thread, **When** the operator clicks "Unarchive" in the Archived view's per-thread overflow, **Then** `is_archived` flips to `false` and the thread reappears in the default subnav.
5. **Given** any thread, **When** the operator clicks "Delete" and confirms via single-click `ConfirmDialog` with copy *"Delete this thread? Transcript and cost rollup will be permanently removed."*, **Then** within 1s the `deleteChatSession` server action DELETEs the `chat_sessions` row (cascading to `chat_messages` via FK), emits `pg_notify('work.chat.session_deleted', chat_session_id)`, and the thread vanishes from all left-rail views.
6. **Given** the operator deletes the currently-open thread, **When** the deletion completes, **Then** the dashboard routes to the no-threads-ever empty-state OR the next-most-recent thread (implementation choice — matches whichever the M3 navigation precedent uses for "current item deleted" cases).

---

### User Story 5 — Cost telemetry + idle pill visible (Priority: P2)

After 10 turns of conversation, the operator wants to see what the chat cost. The thread header carries a per-session cost badge (2 decimals, e.g., `$0.14`) that updates after each terminal commit. Each assistant bubble footer carries the per-message cost (4 decimals, e.g., `$0.0234`). The topbar idle pill reflects the supervisor-side `chat_sessions.status`: green when active, yellow when the supervisor's idle sweep marks the session ended (per FR-081), red when aborted.

**Why this priority**: P2 because the chat works without visible cost numbers, but the operator's claude.ai bill is real money — without a visible cost, runaway sessions or injection attacks could burn through silently. The idle pill provides a low-overhead read of session lifecycle state without the operator having to query the DB.

**Independent test**: drive a session through N turns. After each turn, assert the per-session cost badge equals `chat_sessions.total_cost_usd` (rounded to 2 decimals). After each terminal commit, assert each assistant bubble footer shows the per-message cost matching `chat_messages.cost_usd` (rounded to 4 decimals). Set `GARRISON_CHAT_SESSION_IDLE_TIMEOUT=10s`, wait 12s, assert the supervisor flipped `status='ended'` and the topbar pill flipped to yellow within the next page poll cycle.

**Acceptance scenarios**:

1. **Given** an active thread with N completed assistant turns, **Then** the thread header cost badge shows `$X.XX` matching `chat_sessions.total_cost_usd` rounded to 2 decimals.
2. **Given** an assistant bubble for turn N, **Then** its footer shows `$0.XXXX` matching `chat_messages.cost_usd` for that row at 4 decimals; the value uses `font-tabular` mono per the M3 design language.
3. **Given** the supervisor's idle sweep flips `chat_sessions.status` from `active` to `ended`, **Then** the topbar idle pill updates from green to yellow within one page render cycle (no separate client-side timer required).
4. **Given** the supervisor terminal-writes an assistant row with `error_kind ∈ {'container_crashed', 'subprocess_timeout', 'supervisor_restart'}` and flips `chat_sessions.status='aborted'`, **Then** the topbar pill flips to red ("aborted"), the failed assistant bubble renders inline with the error reason, AND the composer stays enabled — the operator can type a fresh turn which proceeds as a new message in the same session per M5.1 SC-009.
5. **Given** the M5.2 surface does NOT render an "all systems nominal" topbar chip — that ships paired with the warm-pool / Claude pool optimization in a future milestone (per context out-of-scope).

---

### User Story 6 — Stream resilience under disconnect (Priority: P2)

The operator's network blips mid-stream — the EventSource connection drops between two `delta` events. The 5-state listener machine reconnects with exponential backoff; on reconnect, the consumer renders the partial accumulated buffer it already has and waits for the terminal `result` event (deltas are not replayable per M5.1 FR-052 — only terminal events are). The operator sees the stream resume cleanly without duplicate text and without losing the terminal commit.

**Why this priority**: P2 because mid-stream disconnects are real (browser tab-suspend, mobile-network blip, dashboard server restart) and a regression here breaks the chat experience entirely. The M3 SSE listener already handles this for the activity feed; chat reuses the same machine.

**Independent test**: in T020 Playwright, drive a chat turn. Mid-stream, force the EventSource into a disconnect (close it programmatically, simulate offline). Assert: the listener flips through `live → backoff → connecting → live`, the partial accumulated assistant bubble buffer remains rendered (no clear-and-redraw), no `delta` text gets duplicated on reconnect, the terminal `result` event arrives and commits the bubble cleanly.

**Acceptance scenarios**:

1. **Given** an in-flight assistant message with N partial deltas rendered, **When** the EventSource connection drops, **Then** the listener flips state to `backoff`, the partial bubble buffer stays visible (cursor stops blinking), and a low-key reconnect indicator surfaces (e.g., a small dot or microcopy).
2. **Given** the listener is in `backoff`, **When** reconnect succeeds within the 100ms-30s exponential backoff window, **Then** the listener flips to `live`, no previous deltas render again, and the cursor resumes blinking until the terminal `result` arrives.
3. **Given** reconnect happens after the supervisor has already terminal-committed the assistant row, **Then** the consumer reads the terminal state via the SSE route's terminal-event branch (the route emits the terminal event read from `chat_messages` row state per M5.1 FR-052), and the bubble locks with the full final content + cost.
4. **Given** reconnect fails for >30s and enters `idle-grace`, **When** the operator returns and triggers any interaction, **Then** the listener attempts a fresh connect; if Postgres is unreachable a clear error empty-state surfaces with a manual retry CTA.

---

### Edge Cases

- **Operator opens a thread that's currently in `aborted` state.** Per M5.1 SC-009 the session is recoverable — the failed assistant bubble renders inline with the `error_kind` reason, the composer stays enabled, and the next operator turn proceeds as a new message in the same session (FR-271 + FR-215). The topbar idle pill shows red ("aborted") for awareness, but does not block input.
- **Vault token expires mid-session.** The supervisor surfaces `error_kind='token_expired'` on the next-turn assistant row; the message stream renders the error in-line with copy directing the operator to the M4 vault edit surface; the composer stays enabled (operator can rotate token via M4 surface, then send the next turn — token rotation is transparent per M5.1 FR-005).
- **Per-session cost cap hit mid-session.** The supervisor refuses the next spawn per M5.1 FR-061 and writes an assistant row with `error_kind='session_cost_cap_reached'`. The dashboard renders the error inline; composer disables; ended-state empty-state surfaces with copy clarifying the cap was hit and a "Start new thread" CTA.
- **Two browser tabs on the same thread.** Both tabs subscribe to the same SSE channel keyed on `session_id`; both render deltas. No locking or last-writer-wins on the read side. The operator INSERT path is serialized at the DB layer (M5.1 FR-050's `UNIQUE(session_id, turn_index)`), so concurrent sends from two tabs race the constraint; one wins, the other retries with the new max turn_index.
- **Operator clicks "+ New thread" rapidly multiple times.** N empty `chat_sessions` rows land. No spawn, no cost. The thread-history list de-clutters via the archive/delete affordances.
- **Browser back/forward navigation inside chat surface.** Standard Next.js client-side navigation re-attaches SSE on the new session_id; the composer state resets to idle.
- **Operator deletes the currently-open thread.** Routes to no-threads-ever empty-state if it was the last thread; otherwise routes to the next-most-recent thread.
- **Operator's better-auth session expires while chat tab is open.** The next server action call (send message, end thread, etc.) returns a 401-shaped error; the dashboard's existing M3+ auth boundary handles redirect to /login. SSE route closes the connection on auth failure.
- **Long-running thread accumulates 100+ turns.** Message stream uses virtualisation (matching M3's `@tanstack/react-virtual` pattern from the activity feed) OR pagination. /garrison-plan picks; both are acceptable.
- **Operator pastes a multi-MB string into the composer.** Composer enforces a max-length limit (spec-implementation-defined, default ~10KB); paste exceeding the limit truncates with a non-blocking warning.
- **Stale cache on first page load.** The chat list query reads via `getRunningCost` + `listSessionsForUser`; both are stateless reads with no client-side cache. First render is server-rendered (Server Component); no flash-of-stale-content.

---

## Requirements

### Functional Requirements

#### Chat surface route + layout

- **FR-200**: A new chat surface route MUST exist under `dashboard/app/[locale]/`. Path is implementation-defined (`/chat/[[...sessionId]]` is the conventional candidate); /garrison-plan picks the exact shape including the catch-all segment for new vs existing sessions.
- **FR-201**: The chat surface MUST be a three-pane layout: existing left-rail Sidebar (`dashboard/components/layout/Sidebar.tsx`) with one new "CEO chat" entry, a center chat pane (header + message stream + composer), and a right pane occupied by an EmptyState placeholder for M5.4.
- **FR-202**: The chat surface MUST be responsive at ≥768px, ≥1024px, and ≥1280px breakpoints (matching M3 + M4's responsive integration suite). Sub-768px is out of scope.
- **FR-203**: The left-rail "CEO chat" entry MUST have an active-state indicator (filled `StatusDot` or per-state badge from M3's design language) when the operator is on the chat route, and a collapsible thread-history subnav showing the latest 10 threads (numbered `thread #N` per FR-220) sourced from `listSessionsForUser` ordered `started_at DESC` filtered to `is_archived=false`.
- **FR-204**: A "view all" link in the thread-history subnav MUST route to a full-list view of all of the operator's threads (active + archived, with archived filtered to a sub-view).
- **FR-205**: The right pane MUST render a static `EmptyState`-based placeholder for M5.4 with copy clarifying the knowledge-base view ships in M5.4. Exact copy is /garrison-plan territory; the visual treatment matches M3's `EmptyState.tsx` primitive.
- **FR-206**: The wireframe's "Hiring" left-rail entry MUST NOT be added to the Sidebar in M5.2. M7 ships hiring; M5.2 omits it entirely (no disabled placeholder).
- **FR-207**: The wireframe's "Skills" left-rail entry MUST also be omitted in M5.2 — same reasoning as FR-206.
- **FR-208**: The existing M3 sidebar widget at `dashboard/components/layout/Sidebar.tsx:99-112` (`<liveAgents> live · <liveAgents>/<totalCapacity>`) MUST remain unchanged in M5.2. The wireframe's expanded form (`$0.42/hr · 14.2k tok/min`) is out of scope.

#### Center pane: thread header

- **FR-210**: Thread header MUST display: thread title (numbered `thread #N` where N is `chat_sessions.id`-derived or a monotonically-increasing operator-thread counter — /garrison-plan picks), `started <relative-time>` of `chat_sessions.started_at`, `<turn_count> turns` derived from `chat_messages` count, per-session cost badge displaying `chat_sessions.total_cost_usd` rounded to 2 decimals as `$X.XX`.
- **FR-211**: Thread header MUST carry an overflow menu (icon-button) with these items: "Rename thread" (DEFERRED — non-functional in v1, post-M5 polish — /garrison-plan decides whether to render disabled or omit), "Archive thread", "Unarchive thread" (visible only when `is_archived=true`), "End thread" (visible only when `status='active'`), "Delete thread".
- **FR-212**: A "+ New thread" button MUST be reachable from the chat surface — placement is /garrison-plan territory (top-right of center pane, topbar action area, or both). Clicking it MUST create a new empty `chat_sessions` row via a server action (`createEmptyChatSession` or extension to `startChatSession`) and route the operator to the new session's URL.

#### Center pane: composer

- **FR-213**: Composer MUST be a multi-line textarea with a footer area carrying chips and a send affordance.
- **FR-214**: Composer footer MUST include: a palace-live indicator chip (per FR-283), a send button labelled "send" with `⌘↵` rendered via the M3 `Kbd.tsx` primitive, and an explanatory caption ("CEO will be summoned when you send. Each message spawns a fresh process." or equivalent — /garrison-plan writes final copy).
- **FR-215**: Composer MUST disable when any of: `chat_sessions.status='ended'`; OR the latest `chat_messages` row in the session has `status ∈ ('pending', 'streaming')` (in-flight turn). The composer MUST stay enabled when `chat_sessions.status='aborted'` — per M5.1 SC-009 the session is recoverable, and the operator's next turn proceeds as a new message in the same session. Disabled state shows a non-interactive textarea with greyed-out send button and a status-specific caption.
- **FR-216**: Composer MUST submit on `⌘↵` (macOS) / `Ctrl+↵` (Win/Linux) or click of the send button. `Enter` alone inserts a newline (multi-line by default).
- **FR-217**: Composer MUST enforce a maximum input length (default ~10KB; /garrison-plan picks). Pasting beyond the limit truncates with a non-blocking warning.
- **FR-218**: Composer MUST auto-focus on session-open and after each terminal commit (so the operator can continue typing without re-clicking).

#### Center pane: message stream + streaming UX

- **FR-220**: Message stream MUST render `chat_messages` rows for the current session ordered by `(turn_index, role)`. No gaps; no duplicates.
- **FR-221**: Operator messages MUST right-align in a darker bubble (matching the wireframe's visual treatment); assistant messages MUST left-align with a CEO avatar header — `C` initial-avatar + `CEO · <model>` where `<model>` is the cosmetic display badge per FR-216.
- **FR-222**: Per-message cost MUST render in the assistant bubble footer at 4 decimals (`$0.XXXX`) using `font-tabular` mono. Operator bubbles do not show a cost (operator messages have `cost_usd=NULL`).
- **FR-223**: Streaming UX: deltas MUST append-as-arrived to the in-flight assistant bubble; a blinking cursor MUST render at the end of the bubble while streaming. The cursor MUST be steady (no blink) when `prefers-reduced-motion: reduce` is set.
- **FR-224**: Auto-scroll MUST be sticky-bottom by default during streaming; if the operator scrolls up, auto-scroll MUST disable and a "↓ N new" pill MUST appear that scrolls to the bottom on click.
- **FR-225**: The assistant model badge (`haiku`, `sonnet`, `opus`) MUST be cosmetic display only — derived from `chat_messages.raw_event_envelope` (or session-level model field if /garrison-plan adds one). Clicking the badge MUST be a no-op. M5.2 does not ship a model picker.
- **FR-226**: Long-running threads (100+ turns) MUST render without performance degradation. Implementation strategy is /garrison-plan territory: virtualisation via `@tanstack/react-virtual` (matching M3 activity feed pattern) OR pagination + lazy-load. Either is acceptable.

#### Multi-session UX

- **FR-230**: Thread history list (left-rail subnav per FR-203 + full-list view per FR-204) MUST source from `listSessionsForUser`, filtered to `started_by_user_id = current operator's user_id`.
- **FR-231**: Default view MUST filter out `is_archived=true` threads. Archived sub-view MUST show only `is_archived=true` threads.
- **FR-232**: Switching from session A to session B MUST: close session A's EventSource cleanly (no error events); reload the message stream via `getSessionWithMessages(session_B.id)`; open a new EventSource at `/api/sse/chat?session_id=<session_B.id>`.
- **FR-233**: A new migration MUST add `chat_sessions.is_archived BOOLEAN NOT NULL DEFAULT false`. Existing rows backfill to `false`. The column does NOT extend the FR-040 `status` enum.
- **FR-234**: A new `archiveChatSession(sessionId)` server action MUST UPDATE `chat_sessions.is_archived=true` after verifying caller owns the session. No `pg_notify` channel — archive is a display flag only, not an activity-feed-worthy lifecycle event.
- **FR-235**: A new `unarchiveChatSession(sessionId)` server action MUST UPDATE `chat_sessions.is_archived=false` after caller-ownership verification.
- **FR-236**: A new `deleteChatSession(sessionId)` server action MUST: verify caller owns the session; DELETE FROM `chat_sessions` where id = $1 (cascading to `chat_messages` via FK ON DELETE CASCADE — /garrison-plan confirms whether the M5.1 FK already specifies this or the migration needs to add it); emit `pg_notify('work.chat.session_deleted', chat_session_id)`. `vault_access_log` rows referencing the deleted session via `metadata.chat_session_id` MUST NOT cascade-delete — they survive as an immutable forensic trail. The `garrison_dashboard_app` Postgres role MUST NOT be granted DELETE on `vault_access_log` (preserves M4's INSERT-only grant set on vault tables).
- **FR-237**: Delete confirmation MUST use `ConfirmDialog tier='single-click'` (M4 primitive) with copy *"Delete this thread? Transcript and cost rollup will be permanently removed."*

#### Explicit-close action (M5.1 FR-082 follow-through)

- **FR-240**: A new `endChatSession(sessionId)` server action MUST be added to `dashboard/lib/actions/chat.ts`.
- **FR-241**: `endChatSession` MUST verify the caller owns the session (better-auth user_id matches `chat_sessions.started_by_user_id`).
- **FR-242**: `endChatSession` MUST UPDATE `chat_sessions SET status='ended', ended_at=NOW() WHERE id=$1 AND status='active'`. No-op for already-ended/aborted sessions (returns the current row state).
- **FR-243**: `endChatSession` MUST emit `pg_notify('work.chat.session_ended', chat_session_id)` per M5.1 FR-070's existing channel inventory. No new channel.
- **FR-244**: When `endChatSession` is invoked while an in-flight assistant message exists: the action accepts immediately. The in-flight turn finishes its terminal write naturally (the supervisor doesn't observe the dashboard's state transition). Subsequent operator messages on the ended session bounce with `error_kind='session_ended'` per M5.1 FR-081's existing rejection path.
- **FR-245**: UI affordance: "End thread" item in the thread overflow menu, gated by `ConfirmDialog tier='single-click'`. Only visible when `chat_sessions.status='active'`.

#### Idle pill + cost telemetry surface

- **FR-250**: A topbar idle pill MUST render directly off `chat_sessions.status` for the currently-open session: `active` → green tone with "active" copy, `ended` → yellow tone with "idle" copy, `aborted` → red tone with "aborted" copy. Visual treatment uses M3's `StatusDot` + `Chip` primitives.
- **FR-251**: Per-session cost badge in the thread header MUST read `chat_sessions.total_cost_usd`, formatted as `$X.XX` (2 decimals).
- **FR-252**: Per-message cost in the assistant bubble footer MUST read `chat_messages.cost_usd`, formatted as `$0.XXXX` (4 decimals). Small turns round to non-zero values at 4 decimals; 2 decimals would round to $0.00 and be misleading.
- **FR-253**: M5.2 MUST NOT ship the wireframe's "all systems nominal" topbar chip. That chip is paired with the warm-pool / Claude pool optimization (post-M5).

#### SSE consumer + reconnect

- **FR-260**: The chat SSE consumer MUST reuse the M3 5-state listener machine pattern (`dormant/connecting/live/backoff/idle-grace`) from `dashboard/lib/sse/listener.ts`. Implementation may be a chat-specific instance of the same pattern OR a parameterised version of the M3 listener — /garrison-plan picks.
- **FR-261**: On reconnect mid-stream, the SSE consumer MUST resume from the most recent *terminal* event for the current session_id. The mechanism is a server-side row-state read on subscribe (the M5.1 `/api/sse/chat` route reads the terminal `chat_messages` row state when a new EventSource connects and emits a synthetic terminal event for any committed terminal turn the consumer has not yet seen) — there is no server-side `Last-Event-ID` header parse. Deltas are not replayable post-disconnect (M5.1 FR-052); only terminal events are recoverable. The browser's automatic `Last-Event-ID` header on EventSource reconnect is informational only and not relied upon by the route.
- **FR-262**: On reconnect mid-stream, the consumer MUST render the partial accumulated buffer it already has and wait for the terminal `result` event. The consumer MUST NOT clear-and-redraw the partial buffer.
- **FR-263**: Switching sessions (FR-232) MUST close the prior EventSource cleanly via the standard SSE close protocol; the listener machine flips to `dormant` for that connection before opening the new one.
- **FR-264**: The chat SSE consumer MUST NOT wildcard-subscribe to channels (mirroring M3 FR-060). The chat SSE route already keys on `session_id`; the consumer subscribes only to the current session.

#### Empty + error states

- **FR-270**: The chat surface MUST render three EmptyState variants built on M3's `EmptyState.tsx` primitive:
  - (a) **No threads ever**: copy "Start a thread with the CEO" + brief explanation + primary "+ New thread" CTA. Visible when `listSessionsForUser` returns 0 rows.
  - (b) **Empty current thread**: copy "Ask the CEO anything" rendered above the composer. Visible when current session has 0 `chat_messages` rows.
  - (c) **Ended thread**: copy "This thread is ended. Start a new one to keep talking." + "Start a new thread" CTA. Visible when `chat_sessions.status='ended'` (composer disabled per FR-215). NOT shown for `aborted` sessions — those render the error inline (FR-271) with the composer enabled.
- **FR-271**: For each `ChatErrorKind` value (existing in `dashboard/lib/actions/chat.ts`), a specific user-facing message MUST render in the failed assistant bubble (inline within the message stream, not as an EmptyState replacement). When the latest assistant row carries a terminal `error_kind`, the bubble MUST display: a clear error indicator, the human-readable reason mapped from `error_kind`, and (where applicable, e.g. `token_expired`) a deep-link to the M4 vault edit surface. The composer remains enabled below the failed bubble so the operator can type a fresh turn — per M5.1 SC-009, the session is recoverable. No "Retry last turn" button in v1; the operator types a new message which proceeds as a new turn in the same session.
- **FR-272**: Vault token expired mid-session: the most recent assistant row carries `error_kind='token_expired'`. The dashboard MUST render the error in-line within the message stream (assistant bubble shaped as an error variant) AND keep the composer enabled (rotation is transparent per M5.1 FR-005). The error bubble MUST link to `/vault/edit/.../CLAUDE_CODE_OAUTH_TOKEN` (M4 surface).
- **FR-273**: Per-session cost cap hit: the most recent assistant row carries `error_kind='session_cost_cap_reached'`. The dashboard MUST render the error inline AND disable the composer with copy clarifying the cap was hit and a "Start new thread" CTA.

#### Composer state machine + palace-live indicator

- **FR-280**: Composer logical states: `idle` | `typing` | `sending` | `disabled-session-not-active` | `disabled-in-flight`. Transitions: `idle → typing` on first keystroke; `typing → sending` on submit; `sending → idle` on terminal commit; `* → disabled-*` on session-state changes per FR-215.
- **FR-281**: The send button MUST be disabled when composer is empty (no submit on whitespace-only content).
- **FR-282**: A loading state MUST render on the send button during the brief window between submit and SSE first-delta.
- **FR-283**: The palace-live indicator chip data source: time-since-last-successful-tool-call against the MemPalace MCP server, derived from the most recent assistant message's `chat_messages.raw_event_envelope` for the current session. Thresholds: chip shows "live" (green) if last successful MemPalace tool-call ≤ 5 min ago; "stale" (yellow) if 5-30 min; "unavailable" (red) if >30 min OR no successful MemPalace tool-call yet in the current session. /garrison-plan refines threshold values; the source-of-truth is the existing event envelope, no new supervisor endpoint.

#### Supervisor-side carryover: orphan-row sweep mitigation

- **FR-290**: The M5.1 boot-time restart sweep at `internal/chat/restart.go` (per M5.1 FR-083) MUST be extended to also detect: any `chat_sessions` row with `status='active'` whose newest `chat_messages` row has `role='operator'` AND `created_at` older than 60 seconds AND no `chat_messages` row exists with `role='assistant'` AND `session_id = $1` AND `turn_index = $newest_operator_turn + 1`.
- **FR-291**: For each matched orphan-operator row, the supervisor MUST terminal-write a synthetic assistant row with `status='aborted'`, `error_kind='supervisor_restart'`, `turn_index = $newest_operator_turn + 1`, and `content` set to a deterministic placeholder string (e.g., "[supervisor restarted before this turn could complete]" — exact copy is /garrison-plan).
- **FR-292**: The mitigation MUST be a small extension to the existing M5.1 sweep — no new package, no new schema column, no new pg_notify channel. The dashboard surface renders the synthetic aborted row via the standard ended/errored EmptyState path (FR-270c).

#### Test harness extensions (closes M5.1's deferred SCs)

- **FR-300**: The Playwright integration suite MUST gain T020 — a chat-flavoured golden-path spec extending `dashboard/tests/integration/_chat-harness.ts` (or successor) to boot the full M5.1 + M5.2 stack: testcontainer Postgres + Infisical + `garrison-mockclaude:m5` chat image + supervisor process + standalone dashboard bundle.
- **FR-301**: T020 MUST include sub-scenarios for: (a) golden-path single-turn (User Story 1), (b) multi-turn continuity (User Story 2), (c) multi-session switching (User Story 3), (d) end-thread action (User Story 4), (e) archive + delete (User Story 4), (f) stream-disconnect-and-reconnect (User Story 6) asserting no double-render and the terminal `result` event arrives exactly once.
- **FR-302**: T020 MUST close M5.1 SC-010 (round-trip ≤10s + full M3 + M4 + M5.1 + M5.2 suite under 12 minutes total) and SC-011 (token rotation E2E with no supervisor restart).
- **FR-303**: A new `chaos_m5_1_test.go` MUST extend the supervisor's chaos test pattern with a chat case: real `docker kill` against an in-flight chat container; assert `chat_messages` terminal write commits with `error_kind='container_crashed'`, `chat_sessions.status='aborted'`, the SSE typed-error event emits, and the next operator message on the same session (after re-creating it) starts cleanly.
- **FR-304**: `chaos_m5_1_test.go` MUST close M5.1 SC-006 (live SIGTERM cascade) and FR-101 (external-kill scenario).
- **FR-305**: The chaos test scope stays minimal — only the container-kill case. Network-blip / Postgres-loss / pg_notify-buffer cases are deferred to M5.3 chaos (where mutation chaos compounds the read-only chaos).

#### Architecture amendment

- **FR-310**: As part of the M5.2 spec implementation, `ARCHITECTURE.md:574` MUST be amended. The current single-line M5 entry replaces with an enumeration covering M5.1 (read-only chat backend), M5.2 (CEO chat dashboard surface), M5.3 (chat-driven mutations + threat-model amendment), and M5.4 (knowledge-base "WHAT THE CEO KNOWS" pane). The amendment is additive — does not remove or revise the existing read-only stance.

#### Accessibility

- **FR-330**: The chat surface MUST target WCAG 2.1 AA conformance. The chat-specific FRs below cover the streaming-text and multi-session patterns that have no precedent in M3/M4; semantic HTML + the existing `components/ui` primitives cover the rest.
- **FR-331**: The in-flight assistant bubble MUST set `aria-live="polite"` + `aria-busy="true"` while streaming so screen readers announce incoming deltas. On terminal commit, `aria-busy` flips to `false`. Operator messages and completed assistant messages render in a non-live container — no announcement re-trigger on transcript reload.
- **FR-332**: Status indicators (topbar idle pill per FR-250, composer palace-live chip per FR-283, left-rail "CEO chat" active-state indicator) MUST NOT convey state by color alone. Each state MUST combine color + icon (or label text) so a colorblind operator can disambiguate.
- **FR-333**: Keyboard navigation MUST cover: tabbing through the left-rail thread-history subnav; activating a thread via `Enter` or `Space`; tabbing through the composer textarea and send button; opening + navigating the thread overflow menu via keyboard; activating overflow menu items via `Enter`. The composer's `⌘↵` / `Ctrl+↵` shortcut MUST be discoverable via `aria-keyshortcuts` or visible `Kbd` rendering.
- **FR-334**: T020 Playwright (per FR-300/FR-301) MUST include an axe-core assertion run against the chat surface in a typical thread state (post-User Story 1 golden-path render). Assertion: zero serious or critical axe-core violations on the chat route. Moderate or minor violations are not gating in M5.2 (matches M3/M4 a11y precedent).

#### Audit + activity

- **FR-320**: The new `pg_notify('work.chat.session_deleted', chat_session_id)` channel emitted by FR-236 MUST be added to the M3 activity feed's known channel inventory at `dashboard/lib/sse/channels.ts`. Adding a variant to the activity feed's discriminated union is an explicit code change (FR-060 from M3).
- **FR-321**: The `work.chat.session_deleted` payload MUST carry only `chat_session_id` and `actor_user_id`. No message content. (Mirrors M5.1 FR-072's audit-channel discipline; Rule 6 from the vault threat model continues to apply.)
- **FR-322**: The activity feed's discriminated union variant for `session_deleted` MUST render via a new chat-flavoured `EventRow` branch that follows the M3 audit-event design language (icon + actor + concise predicate + relative time). M5.1's `session_started` / `message_sent` / `session_ended` channels are emitted by the supervisor but do NOT have dashboard `EventRow` rendering yet; M5.2 ships only the `session_deleted` rendering. Wiring `EventRow` rendering for the existing M5.1 chat channels is a separate scope item (deferred — operator-tooling polish, post-M5.2).

### Key entities

- **chat_sessions** (extended) — adds `is_archived BOOLEAN NOT NULL DEFAULT false` column. The M5.1 lifecycle (`active → ended | aborted`) stays unchanged. Archive is an orthogonal display flag, not a status transition.
- **chat_messages** (M5.1, no schema change) — message stream renders these rows ordered by `(turn_index, role)`. Operator messages remain dashboard-INSERTed; assistant messages remain supervisor-INSERTed. The new orphan-row sweep (FR-290) writes synthetic aborted assistant rows when supervisor crash leaves an operator row stranded.
- **vault_access_log** (M5.1 reuse, no schema change, immutable on chat-thread deletion) — chat fetches continue to write rows with `metadata.actor='supervisor_chat'` per M5.1 FR-002. M5.2 does not introduce new vault interactions. Rows referencing a `metadata.chat_session_id` that is later deleted via `deleteChatSession` (FR-236) survive as a forensic trail; the dangling reference is intentional. `garrison_dashboard_app` retains M4's INSERT-only grant on this table — no DELETE grant added.
- **event_outbox** (extended) — adds one new channel: `work.chat.session_deleted`. Three M5.1 chat channels (`session_started`, `message_sent`, `session_ended`) continue unchanged.

---

## Success Criteria

### Measurable Outcomes

- **SC-200**: An operator clicking send in the chat surface produces a first-rendered SSE delta within 5s wall-clock end-to-end (operator click → first DOM-rendered delta, inclusive of the dashboard render hop — mirrors M5.1 SC-001's 5s budget). Measured against `garrison-mockclaude:m5` per M5.1 FR-100, on a developer-grade laptop with images pre-pulled.
- **SC-201**: Multi-turn context fidelity in the rendered view: an N≥3-turn session in the dashboard shows all turns in chronological order with operator messages right-aligned and assistant messages left-aligned, no gaps, no duplicates. Re-rendering after a hard page reload reproduces the same state via `getSessionWithMessages` exactly.
- **SC-202**: Session switching: with three threads A/B/C owned by the test operator, navigating A→B→C→A loads each transcript correctly, the SSE consumer detaches and re-attaches per FR-232/FR-263, and no `delta` event payload from one session reaches another session's render path. Verifiable via DOM assertion across the switching sequence.
- **SC-203**: End thread action: clicking "End thread" updates `chat_sessions.status='ended'`, emits `pg_notify('work.chat.session_ended', ...)`, disables the composer, and renders the ended-state EmptyState within 1s of click.
- **SC-204**: Archive thread: archived threads filter out of the default left-rail subnav and the full-list default view; "Archived" sub-view shows them; un-archive restores to default. `chat_sessions.is_archived` flips correctly in the DB. No `pg_notify` emitted for archive (FR-234).
- **SC-205**: Delete thread: `deleteChatSession` removes the `chat_sessions` row, cascades to `chat_messages`, emits `pg_notify('work.chat.session_deleted', ...)`, and the thread vanishes from all left-rail views within 1s. The activity-feed event renders via the M3 SSE consumer with the chat-flavoured variant.
- **SC-206**: Per-session cost badge updates after each assistant terminal commit within 1s; the rendered value matches `chat_sessions.total_cost_usd` rounded to 2 decimals.
- **SC-207**: Per-message cost in the assistant bubble footer matches `chat_messages.cost_usd` rounded to 4 decimals for every assistant turn. Operator turns show no cost (NULL).
- **SC-208**: Topbar idle pill flips: with `GARRISON_CHAT_SESSION_IDLE_TIMEOUT=10s`, after 12s of session inactivity the supervisor flips `chat_sessions.status='ended'` per FR-081, and within one page render cycle the topbar pill flips green → yellow.
- **SC-209**: Mid-stream SSE disconnect + reconnect: forced disconnect during streaming followed by reconnect within the backoff window produces zero double-rendered deltas, the terminal `result` event arrives exactly once, and the assistant bubble locks with full final content. Asserted in T020 (FR-301f).
- **SC-210**: Mobile/responsive: 768px / 1024px / 1280px viewport layouts render the three-pane chat surface without overflow, scroll-trapping, or focus-stealing. The M3 + M4 + M5.2 responsive integration suite stays green.
- **SC-211**: Composer disables when `chat_sessions.status='ended'` OR latest `chat_messages.status ∈ ('pending', 'streaming')`. Composer stays enabled when `chat_sessions.status='aborted'` (recoverable per M5.1 SC-009 — operator's next turn proceeds as a new message in the same session). Submit attempts on disabled composer no-op.
- **SC-212**: Empty-state shapes — (a) no threads ever, (b) empty current thread, (c) ended-thread — render with distinct copy (per FR-270 + FR-271). Visual treatment passes manual review against the M3 `EmptyState.tsx` primitive's design language.
- **SC-213**: T020 Playwright passes including all six sub-scenarios (FR-301a-f). Full M3 + M4 + M5.1 + M5.2 suite runs under 12 minutes total in CI per M5.1 SC-010.
- **SC-214**: `chaos_m5_1_test.go` passes — `docker kill` against in-flight chat container produces: container gone within 5s, `chat_messages` terminal write committed with `error_kind='container_crashed'`, `chat_sessions.status='aborted'`, SSE typed-error event emitted, next operator message on a fresh session starts cleanly. Closes M5.1 SC-006 + FR-101.
- **SC-215**: Orphan-operator-row sweep: a `chat_sessions` row with `status='active'`, newest `chat_messages` row `role='operator'` with `created_at > 60s` and no assistant pair, gets resolved within 60s of supervisor boot via FR-290's extension. Synthetic assistant row commits with `error_kind='supervisor_restart'`, status='aborted'.
- **SC-216**: Vault leak-scan parity carry-forward from M5.1 SC-005: a grep of (a) dashboard server logs during a test session, (b) browser-side console + network tab payloads, (c) SSE event payloads received by the consumer, MUST find zero substrings of `CLAUDE_CODE_OAUTH_TOKEN` value AND zero `sk-`-shaped sentinel substrings (test plants in palace contents).
- **SC-217**: Architecture amendment lands: `ARCHITECTURE.md:574` enumerates M5.1 / M5.2 / M5.3 / M5.4 in the M5 entry. Amendment is additive — does not remove or revise the existing read-only stance.
- **SC-218**: Zero new direct dependencies in `dashboard/package.json`, OR every new dependency is justified in the M5.2 implementation commit messages and recapped in the M5.2 retro per AGENTS.md soft-rule. The supervisor's `go.mod` MUST gain zero new direct dependencies (M5.2 supervisor-side work is the FR-290 sweep extension, no new code paths needing external libs).
- **SC-219**: `goose up` against a fresh testcontainer Postgres applies the M5.2 migration cleanly (the `is_archived` column add). `bun run drizzle:pull` regenerates `schema.supervisor.ts` to include the column. `bun run typecheck` passes.
- **SC-220**: `vaultlog` go-vet analyzer continues to pass on the FR-290 supervisor extension. No `slog`/`fmt`/`log` call accepts a `vault.SecretValue` argument anywhere in the chat code paths.
- **SC-221**: Long-running thread (≥100 turns) renders without performance degradation per FR-226. Specific metric: time-to-interactive on initial page load with a 100-turn thread under 2s on developer-grade laptop. /garrison-plan refines if virtualisation or pagination is chosen.
- **SC-222**: WCAG 2.1 AA conformance — the T020 Playwright axe-core run (FR-334) reports zero serious or critical violations on the chat route in a typical thread state. Manual screen-reader sanity check (operator-driven, recorded in retro) confirms streaming text is announced via `aria-live` per FR-331.

---

## Assumptions

- The M5.1 substrate (server actions in `dashboard/lib/actions/chat.ts`, queries in `dashboard/lib/queries/chat.ts`, SSE producer at `dashboard/app/api/sse/chat/route.ts`, listener helper at `dashboard/lib/sse/chatListener.ts`, supervisor-side `internal/chat/` package, idle/restart sweeps, audit channels) is shipped on `main` and stable. M5.2 reads against this substrate; it does not change supervisor-side chat code except for the FR-290 sweep extension.
- The Tailwind v4 + `dashboard/components/ui/` library from M3 (Chip, ConfirmDialog, EmptyState, Kbd, KpiCard, Sparkline, StatusDot, Tbl, etc.) and `dashboard/components/layout/{Sidebar,Topbar}.tsx` are reusable primitives for M5.2. No new UI library or design-system overhaul.
- The M3 5-state SSE listener machine pattern at `dashboard/lib/sse/listener.ts` is reusable as a pattern for chat SSE consumption (per FR-260). /garrison-plan picks whether to instantiate it directly, parameterise it, or factor a shared module.
- The M4 `ConfirmDialog` primitive is available with both `single-click` and `typed-name` tiers; M5.2 uses only `single-click` per FR-237 + FR-245.
- Garrison remains single-tenant single-operator (per `RATIONALE.md` and M3's setup wizard's single-account model). The `started_by_user_id` ownership check in M5.2 server actions (FR-241, FR-234, FR-236) is forward-compatible with multi-operator OAuth but currently scopes to the single operator.
- The `chat_messages.session_id` foreign key from M5.1 specifies `ON DELETE CASCADE` OR is amended to do so during the M5.2 migration (`is_archived` add) per FR-236. /garrison-plan verifies the M5.1 schema and adds the cascade if missing.
- The M5.1 SSE route at `/api/sse/chat?session_id=<uuid>` keys per-session correctly per M5.1 FR-052, so multi-session switching at FR-232 doesn't require route changes — only consumer-side EventSource lifecycle management.
- The M3 `@panel` parallel-route intercept pattern (M3 retro §"What got established as reusable design language") is available if /garrison-plan opts for an inline thread-detail drawer in any sub-flow. M5.2 may or may not use it.
- Existing M3 + M4 Playwright harness `dashboard/tests/integration/_chat-harness.ts` (or analogous) exists and is extensible. If not, T020 lays the harness; /garrison-plan confirms.
- The `garrison-mockclaude:m5` image from M5.1 already supports the multi-turn cache_read_input_tokens emission needed for SC-201's verification; M5.2 does not extend the mockclaude binary.
- The dashboard's existing better-auth session boundary handles auth-expiry redirects without M5.2-specific code; M5.2's server actions inherit the M3+ auth gate.
- The retention policy on `chat_messages.raw_event_envelope` is "keep within the M5.1-defined window" — M5.2 reads the envelope (per FR-283 palace-live indicator) but doesn't extend or trim it.
- Cost rendering uses JavaScript Number formatting (`Intl.NumberFormat` with `currency: 'USD'`); /garrison-plan confirms the formatter shape and locale handling.
- The architecture amendment per FR-310 is a single-commit doc edit; no spec/plan/tasks downstream concerns.
