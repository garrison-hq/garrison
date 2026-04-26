# Feature specification: M3 — operator dashboard (read-only)

**Feature branch**: `008-m3-dashboard`
**Created**: 2026-04-26
**Status**: Draft
**Input**: M3 milestone — operator dashboard, read-only over M2-arc data, with auth, dual themes, i18n, responsive, and a production Docker build. See `specs/_context/m3-context.md` for binding constraints.

---

## Clarifications

### Session 2026-04-26

- Q: Where does the operator's theme preference persist? → A: Per-operator column on the better-auth user record (cross-device, follows the operator).
- Q: How are additional operators added after first-run? → A: Existing authenticated operators can invite additional operators via a dashboard-internal invite flow. Real two-operator-on-one-product scenario; refines the context's "single-operator pool" to "small operator pool (1–2 typical, more allowed)."
- Q: How does the sandbox-escape failure mode appear in ticket detail? → A: Per-row icon + hover tooltip in the history table; click-to-expand reveals "claimed: X / on-disk: Y" detail. Avoids ticket-detail-wide warning banners as the failure mode becomes common.
- Q: How is the activity feed's `pg_notify` channel set determined? → A: Hardcoded explicit list in dashboard config — each entry maps to a known render path. Adding a new channel is an explicit code change; avoids wildcard subscriptions to unrenderable payloads.
- Q: Where does the dashboard run relative to the supervisor? → A: Same-host as a fourth container alongside supervisor + mempalace + socket-proxy. Single-host deployment matches the solo-operator model; separate-host can be added later via env-var flip without spec churn.
- Q: Who owns dashboard-side schema migrations — goose or Drizzle? → A: Drizzle owns migrations for dashboard-side schema (better-auth tables, `operator_invites`, `theme_preference` column on users). Goose remains the sole migrator for supervisor-owned tables and for cross-boundary migrations (the `garrison_dashboard_*` roles + GRANTs on supervisor-owned tables). Surfaced during `/garrison-plan` slate review; amends FR-020.

---

## User scenarios & testing *(mandatory)*

The user is a solo operator. They are not a customer, an admin, or a developer in the traditional sense — they are the one human in the Garrison loop, supervising a standing force of agents. Each story below maps to one of their daily or weekly jobs.

### User story 1 — Authenticate and see what's happening across the company (priority: P1)

The operator opens the dashboard in a browser, authenticates, and lands on an org overview that tells them — at a glance — what's running, what's blocked, what shipped recently, and where the warnings are.

**Why this priority**: This is the MVP slice. Without an auth-gated landing surface that renders against real M2-arc data, M3 has no operator value. Every other surface depends on the shell, the auth layer, the theme/i18n/responsive baseline, and a working production build.

**Independent test**: A fresh checkout, run through the production build, with a fresh Postgres seeded from real M2-arc activity, lets a never-seen-it operator complete first-run setup, log in, and read the org overview without consulting docs or `psql`.

**Acceptance scenarios**:

1. **Given** the dashboard is deployed and no operator account exists, **When** the operator visits any route, **Then** they are redirected to the first-run setup wizard and can create the inaugural operator account.
2. **Given** an operator account exists and the operator is logged out, **When** they visit a protected route, **Then** they are redirected to the login page.
3. **Given** an authenticated session, **When** the operator visits `/`, **Then** the org overview renders KPIs (open tickets, active agents, recent transitions, hygiene warnings) and one row per department with column counts, agent caps, and warning counts.
4. **Given** the org overview is rendered, **When** the operator clicks a department row, **Then** they navigate to that department's Kanban view (covered in story 3).
5. **Given** the dashboard is loaded in a 1280px / 1024px / 768px viewport, **When** the operator interacts with the org overview, **Then** all data and primary actions remain reachable without horizontal page scroll.
6. **Given** the operator toggles the theme switcher in the topbar, **When** they switch from dark to light (or vice versa), **Then** every surface re-renders with the new theme without visual breakage and without losing semantic-color meaning.

---

### User story 2 — Watch agents execute in real time (priority: P1)

The operator opens the agent activity feed and watches events stream in as agents spawn, work tickets, write to the palace, fetch vault secrets, and finalize. They can filter by agent or event type and expand a run to see all events from a single `agent_instance`.

**Why this priority**: Also P1. The activity feed is the operator's primary reason for M3 to exist — it closes the loop between "I started supervisor" and "something is happening." It also surfaces the high-volume-event observation from `docs/retros/m2-1.md` that prior fixtures didn't anticipate. Independently testable: with the org overview shipped, this story adds a single navigable surface that streams live events.

**Independent test**: Start the supervisor, spawn an engineering ticket, and observe the activity feed receive 5–10 `assistant` events per run plus the surrounding lifecycle events without dropping any and without visual collapse.

**Acceptance scenarios**:

1. **Given** the operator is on the activity feed and an agent is spawning, **When** events arrive, **Then** they appear in the feed grouped under their `agent_instance_id` with a one-line collapsed summary by default.
2. **Given** a run group is collapsed, **When** the operator clicks expand, **Then** all events from that run render in chronological order without truncation.
3. **Given** the SSE connection drops (e.g., page sleep, network blip), **When** the connection re-establishes, **Then** the feed catches up to the latest events without requiring a full page reload.
4. **Given** the operator applies a filter (per agent, per event type), **When** new events arrive, **Then** only events matching the filter appear; the filter persists in the URL.
5. **Given** a run produces 10 `assistant` events, **When** the run renders in the feed, **Then** the visual layout remains readable (no overflow, no overlap, no silent event drops).

---

### User story 3 — Drill into a specific ticket and see its full history (priority: P2)

The operator opens a department's Kanban, clicks a ticket card, and sees the ticket's full lifecycle: every transition with its hygiene status, every agent instance that touched it (with cost caveats where applicable), and the palace diary entry + KG triples written for it.

**Why this priority**: Drill-down is essential for triage but depends on the Kanban read surface. Independently testable: select a ticket from a real Kanban view and verify all three lifecycle blocks render correctly.

**Independent test**: For a ticket that has gone through ≥2 transitions, the ticket detail surface shows the full transition history, the full agent_instance list, and at least one palace diary entry with KG triples.

**Acceptance scenarios**:

1. **Given** a department's Kanban view, **When** it loads, **Then** all four columns render with their tickets as cards, no drag-to-move handle is exposed, and clicking a card navigates to ticket detail.
2. **Given** ticket detail for a ticket with multiple transitions, **When** it renders, **Then** the history block lists every `ticket_transitions` row in chronological order with hygiene status visible per row.
3. **Given** an `agent_instances` row with `exit_reason='finalize_committed' AND total_cost_usd=0`, **When** it renders in the agent-instances block, **Then** a cost-blind-spot caveat icon appears next to the cost value with a hover tooltip linking to `docs/issues/cost-telemetry-blind-spot.md`.
4. **Given** a ticket has palace writes, **When** the palace-links block renders, **Then** each diary entry and KG triple set shows a one-line excerpt and a copy-id button.
5. **Given** a ticket transition with sandbox-escape evidence (artifact-claimed-vs-on-disk failure mode), **When** the history block renders, **Then** the affected row shows a distinct icon with a hover tooltip naming the failure mode, and clicking the row's expand control reveals the "claimed: X / on-disk: Y" detail.

---

### User story 4 — Triage hygiene issues for the week (priority: P2)

The operator opens the hygiene table on a Friday afternoon, filters by failure mode, sorts by recency, and triages each row by clicking through to ticket detail. The full hygiene-status vocabulary is visible — they can find finalize-path failures, sandbox-escape evidence, and suspected secret emissions in one place.

**Why this priority**: The dashboard's stated reason-for-being per `RATIONALE.md` §7 includes hygiene review. Independently testable: seed rows in each of the three failure-mode classes and verify they all appear and click through correctly.

**Independent test**: A test fixture with one row per documented failure mode renders correctly, allows filter + sort, and links to ticket detail for each row.

**Acceptance scenarios**:

1. **Given** the hygiene surface is loaded, **When** it renders, **Then** all `ticket_transitions` rows with non-clean `hygiene_status` appear, paginated.
2. **Given** the operator applies a "filter by failure mode" filter, **When** the filter changes, **Then** only matching rows render and the URL updates to reflect the filter.
3. **Given** the filter is set to `suspected_secret_emitted`, **When** matching rows render, **Then** each row shows the offending pattern category (without showing the actual matched value).
4. **Given** the filter is set to sandbox-escape, **When** matching rows render, **Then** each row shows a clickable link to the relevant ticket detail with the failure mode highlighted.
5. **Given** the filter is set to `finalize_never_called` (and adjacent finalize-path statuses), **When** matching rows render, **Then** each row links to ticket detail and shows the relevant agent_instance's exit_reason inline.
6. **Given** the table has zero non-clean rows, **When** the surface renders, **Then** an empty state explains "no hygiene issues to triage" with a one-line caption describing what would populate the table.

---

### User story 5 — Audit vault access and grants (priority: P3)

The operator opens the vault surface, sees the configured secrets list, browses recent vault access events in the audit log, and inspects the agent-role-to-secret matrix to verify grants are correct. They never see a secret value.

**Why this priority**: M2.3's threat model and the M2.3 context's "Deferred to M3" subsection explicitly require the dashboard to surface this data. Independently testable with the M2.3-shipped tables as input.

**Independent test**: Against a Postgres seeded with `agent_role_secrets`, `vault_access_log`, and `secret_metadata` rows, verify all three sub-views render correctly via a `garrison_dashboard_ro` connection (and fail to render via any agent-facing role).

**Acceptance scenarios**:

1. **Given** the vault surface is opened, **When** the secrets-list sub-view renders, **Then** each `secret_metadata` row shows path, allowed role slugs, last-rotated, rotation status — and **never** a secret value.
2. **Given** the audit-log sub-view, **When** it loads, **Then** `vault_access_log` rows render with filters by role, by ticket, by time window. Both successful fetches and fail-closed paths are visible.
3. **Given** the matrix sub-view, **When** it renders, **Then** roles × secrets is shown as a grid with cell shading indicating grant presence.
4. **Given** the dashboard is configured with the wrong DB role, **When** any vault sub-view loads, **Then** the request fails (and the failure is operator-visible — not silent). *(Verifiable by inspection: the runtime DSN must resolve to `garrison_dashboard_ro`, not any agent-facing role.)*
5. **Given** the operator attempts to copy a secret value via the UI, **When** any UI surface for vault data renders, **Then** no surface anywhere exposes a path to read a secret value.

---

### User story 7 — Invite a second operator (priority: P3)

The primary operator occasionally collaborates with a second person on the product. From within the dashboard, the authenticated operator generates an invite link, shares it out-of-band, and the invitee redeems it by setting their name and password — landing them logged in as a peer operator.

**Why this priority**: Real operational need (the operator confirmed two-operator scenarios happen sometimes), but not blocking the core read-only observation surfaces. P3 because the dashboard delivers value with one operator, and the invite path is a small, contained surface that can be developed last.

**Independent test**: An authenticated operator generates an invite link; from a clean browser session, visiting the link allows account creation; the new account can log in and reach the org overview. Revoking a pending invite invalidates its link.

**Acceptance scenarios**:

1. **Given** an authenticated operator on a settings/admin surface, **When** they generate an invite, **Then** a one-time invite link is produced and the invite appears in a "pending invites" list.
2. **Given** an unredeemed invite link, **When** an invitee opens it in a clean browser, **Then** they see a setup form (name, password) and on submission their account is created and they are logged in.
3. **Given** a pending invite, **When** the inviting operator revokes it, **Then** subsequent attempts to redeem the link fail with a clear error.
4. **Given** an invite link that has been redeemed once, **When** the same link is opened again, **Then** redemption is rejected.
5. **Given** an invite link past its expiration window, **When** opened, **Then** redemption is rejected with an "expired" error.
6. **Given** two operator accounts exist, **When** either logs in, **Then** the dashboard renders identically — there is no role hierarchy or per-operator permission differentiation in M3.

---

### User story 6 — Inspect the agents registry (priority: P3)

The operator opens the agents page and sees a table of every configured agent with department, role, model, concurrency cap, last-spawned-at, and total spawns this week.

**Why this priority**: Static read; valuable for orientation but not load-bearing for daily work. Easy to ship, low risk.

**Independent test**: Seed agents in two departments and verify the table renders with correct per-agent statistics from `agent_instances`.

**Acceptance scenarios**:

1. **Given** the agents page loads, **When** it renders, **Then** every row in the `agents` table appears with its configured fields.
2. **Given** an agent has spawned ≥1 instance this week, **When** the table renders, **Then** the last-spawned-at and total-spawns-this-week columns reflect the live data.
3. **Given** an agent has zero spawns this week, **When** the row renders, **Then** the empty value displays as "—" or equivalent (consistent with the empty-state vocabulary across surfaces).

---

### Edge cases

- **First-run state**: zero tickets, zero transitions, zero agent_instances, zero vault rows. Every surface must render an empty state that does not look broken.
- **SSE connection drops mid-run**: activity feed must recover without losing in-flight runs (catch-up via cursor on reconnect).
- **Concurrent operator sessions**: two browser tabs from the same operator account — both must work simultaneously without session collision.
- **Concurrent operators**: two distinct operator accounts logged in simultaneously — both see the same data, no UI collision, no per-operator-private surface.
- **Invite redemption races**: two attempts to redeem the same invite within milliseconds of each other — exactly one succeeds, the other receives the "already redeemed" error.
- **Inviter account deletion before redemption**: if an inviter's account is deleted (via direct SQL — there is no UI delete in M3) while their invite is pending, the invite remains valid until expiry or revocation by another operator.
- **Time-zone switch**: operator on a different machine in a different TZ — UTC stored, local displayed. No surface should drift between the two.
- **Database role drift**: if `garrison_dashboard_ro` is missing the required GRANT for a vault table, the affected vault sub-view fails fast with an operator-visible error (not a silent empty render).
- **High-volume run**: 10 `assistant` events from a single `agent_instance` arrive within seconds; the activity feed groups, collapses, and renders without bloat.
- **Cost telemetry zero on clean finalize**: the `$0.00` value is shown but flagged with a caveat icon — never shown unflagged.
- **Theme persistence**: operator switches theme, closes browser, returns — preference is restored from the per-operator `theme_preference` column on the better-auth user record. The preference follows the operator across browsers and machines.
- **Locale fallback**: if a translation key is missing from a locale catalog, English is used as fallback (rather than the raw key being displayed).
- **DB connection sizing for SSE bridge**: the LISTEN connection consumes a dedicated Postgres connection. Connection-pool sizing accounts for one persistent LISTEN slot plus the operator pool's session/query traffic.
- **Production image build**: the dashboard image must build reproducibly and start cleanly against a real Postgres. Whether the production image includes the Drizzle CLI for ops-time schema introspection is deferred to `/garrison-plan` (see CL-004).

---

## Requirements *(mandatory)*

### Functional requirements

#### Auth and shell

- **FR-001**: System MUST gate every route except the login route, the first-run setup route, and unredeemed invite-redemption routes behind a valid better-auth session.
- **FR-002**: System MUST provide a first-run setup route that is accessible only when the `users` table is empty; once an operator account exists, the route returns 404 (or equivalent).
- **FR-002a**: An authenticated operator MUST be able to generate an invite for an additional operator. Each invite produces a one-time-redeemable link with a bounded expiration window.
- **FR-002b**: System MUST provide a "pending invites" view (within a small admin/settings surface) listing each unredeemed invite with its created-at, expires-at, and a revoke action.
- **FR-002c**: An invite-redemption route MUST accept the link's token, present a setup form (name, password) to the invitee, and on submission create the account and log the invitee in.
- **FR-002d**: System MUST reject redemption of expired, revoked, or already-redeemed invite links with an operator-comprehensible error.
- **FR-002e**: There MUST be no other path to create operator accounts post-first-run — no public signup, no admin-bypass route, no auto-create-on-login. All non-first-run accounts originate from a redeemed invite.
- **FR-002f**: All operators authenticated by better-auth have identical privileges in M3. There is no role hierarchy, no per-operator permission differentiation, no read-only operator class.
- **FR-003**: System MUST persist better-auth tables (users, sessions, accounts, verifications) and `operator_invites` in the existing Garrison Postgres database in the `public` schema. These dashboard-side tables are managed by Drizzle migrations per FR-020; goose retains ownership of supervisor-side schema and the cross-boundary role + GRANT migration.
- **FR-004**: System MUST render a sidebar + topbar shell consistent across all surfaces, modeled on the visual language defined in `.workspace/m3-mocks/garrison-reference/shell.jsx` and `styles.css`.
- **FR-005**: Topbar MUST include a theme switcher (dark / light, system-preference default) and a locale switcher (English-only in M3, but the switcher exists for future locales).

#### Cross-cutting (theme, i18n, responsive, deployment)

- **FR-010**: System MUST support both dark and light themes with parity — same density, same component vocabulary, same semantic-color meanings. Theme tokens defined as CSS variables under a `[data-theme]` (or equivalent) toggle.
- **FR-010a**: System MUST persist the operator's theme preference on the better-auth user record (a `theme_preference` column or equivalent). On login, the operator's saved preference loads; on switch, the preference writes back to the user record so it follows them across browsers and machines. System-preference detection applies only when the operator has not yet made an explicit choice.
- **FR-011**: System MUST be usable on viewports from ≥768px (tablet portrait) up through wide-desktop, without horizontal page scroll on primary content.
- **FR-012**: System MUST route every user-facing string through an i18n layer. Component code MUST NOT contain English-string-shaped JSX literals; all copy resolves through locale catalogs.
- **FR-013**: System MUST ship with English-only locale catalogs in M3, but adding a second locale MUST require only translation files — no component changes.
- **FR-014**: System MUST produce a working production Docker build via a `dashboard/Dockerfile` that runs the Next.js app in production mode and connects to Postgres for both data reads and better-auth session storage. The image MUST integrate as a fourth container in the existing single-host topology (supervisor + mempalace sidecar + socket-proxy + dashboard). The DB connection string is env-var-driven so a future separate-host deployment is configuration-only.

#### Data access discipline

- **FR-020**: Drizzle owns migrations for dashboard-side schema: better-auth tables (`users`, `sessions`, `accounts`, `verifications`), `operator_invites`, and the `theme_preference` column on `users`. Drizzle generates SQL from a hand-written schema file and applies via `drizzle-kit migrate`. Goose remains the sole migrator for supervisor-owned tables and for cross-boundary migrations that reference supervisor-owned tables (e.g., the `garrison_dashboard_*` role + GRANT migration). For supervisor-owned tables, Drizzle uses `drizzle-kit pull` to introspect types only — it MUST NOT emit diff-generated migrations targeting those tables. Deploy order: goose migrations apply before Drizzle migrations.
- **FR-021**: System MUST connect to Postgres via two roles: better-auth and operational reads use a dashboard-app role; vault sub-views use a separate `garrison_dashboard_ro` role with explicit SELECT grants on `agent_role_secrets`, `vault_access_log`, `secret_metadata`, and the joinable read tables (`tickets`, `ticket_transitions`, `agents`, `agent_instances`).
- **FR-022**: System MUST NOT use `garrison_agent_ro`, `garrison_agent_mempalace`, or any other agent-facing Postgres role for any dashboard query.
- **FR-023**: A migration introducing `garrison_dashboard_ro` MUST land as part of M3, and the role's password setup MUST be documented in `docs/ops-checklist.md` mirroring the M2.3 pattern for `garrison_agent_mempalace`.

#### Org overview

- **FR-030**: System MUST render a KPI strip showing: count of open tickets, count of active agents (vs cap), count of transitions in the trailing 24h, count of unresolved hygiene warnings.
- **FR-031**: System MUST render one row per department, showing per-column ticket counts, agent count vs cap, last transition timestamp, hygiene warning count for that department.
- **FR-032**: System MUST refresh the org overview KPIs and rows on a 60s soft-poll cadence and provide a manual refresh control.
- **FR-033**: System MUST link each department row to that department's Kanban surface.

#### Department Kanban (read view)

- **FR-040**: System MUST render four Kanban columns per department, each with the tickets currently in that column.
- **FR-041**: System MUST render each ticket as a card showing priority indicator, ticket id/title, assigned agent (if any), and age.
- **FR-042**: System MUST NOT expose any drag-to-move affordance, inline edit control, or other write surface on the Kanban.
- **FR-043**: System MUST link each ticket card to the ticket-detail surface.

#### Ticket detail

- **FR-050**: System MUST render ticket metadata: id, department, current column, created/updated timestamps, priority, status.
- **FR-051**: System MUST render a history block listing every `ticket_transitions` row chronologically, with the transition's `hygiene_status` visible per row.
- **FR-052**: System MUST render an agent-instances block listing every `agent_instances` row that touched this ticket, with `exit_reason`, `total_cost_usd`, and duration.
- **FR-053**: System MUST display a cost-blind-spot caveat icon next to any `total_cost_usd` value where the row matches `exit_reason = 'finalize_committed' AND total_cost_usd = 0`. The icon's hover tooltip MUST name the issue and link to `docs/issues/cost-telemetry-blind-spot.md`.
- **FR-054**: System MUST render a palace-links block showing the diary entry and KG triples written for this ticket, with a one-line excerpt per entry and a copy-id control.
- **FR-055**: System MUST visually distinguish `ticket_transitions` rows whose hygiene status indicates the sandbox-escape failure mode (artifact-claimed-vs-on-disk) via a per-row icon with a hover tooltip naming the failure mode. The affected row MUST be expandable to reveal the artifact-claimed-vs-on-disk detail (claimed path / on-disk path).

#### Agent activity feed (streamed)

- **FR-060**: System MUST stream agent-lifecycle events via Server-Sent Events from a backend route. The feed MUST cover at minimum: ticket-creation events, ticket-transition events, finalize-related events, and agent-instance lifecycle events.

    The feed sources events via two complementary mechanisms, both driven by a hardcoded, explicit, version-controlled list in dashboard configuration where each entry maps to a known render path:

    1. **Real-time `pg_notify` LISTEN** for channels the supervisor currently emits via `pg_notify` (presently `work.ticket.created` and `work.ticket.transitioned.<dept>.<from>.<to>`). Adding a new LISTEN channel is an explicit code change; the dashboard MUST NOT wildcard-subscribe.
    2. **`event_outbox` poll** for event variants that are written to `event_outbox` but not pg_notify'd (presently finalize-related and agent-instance lifecycle events). The poll cadence is bounded (≤30s) and the merged feed deduplicates by event id.

    M3 does not modify supervisor-side `pg_notify` emissions (per AGENTS.md scope discipline). If a future milestone adds new emit sites, the dashboard's allowlist gains a corresponding entry.
- **FR-061**: System MUST group events by `agent_instance_id` ("a run") and render each run as a one-line collapsed summary by default with an expand control to show all events.
- **FR-062**: System MUST render runs with up to 10 `assistant` events without losing events and without visual layout breakage (overflow, overlap, silent collapse).
- **FR-063**: System MUST provide filter chips for event-type and per-agent. Filters MUST persist in URL query params for shareability and back/forward navigation.
- **FR-064**: System MUST handle SSE reconnection: on connection drop and re-establishment, the feed MUST catch up to the latest events without requiring a page reload (cursor-based catch-up via `Last-Event-ID`, replaying events with `id > Last-Event-ID` in ascending order).
- **FR-065**: System MUST use a virtualized list strategy for the feed to keep render cost bounded as event volume grows.

#### Memory hygiene table

- **FR-070**: System MUST render every `ticket_transitions` row whose `hygiene_status` is non-clean, with offset+page-size pagination.
- **FR-071**: System MUST surface all three current failure-mode classes in the hygiene vocabulary: finalize-path statuses (M2.2.1+ vocabulary including `finalize_never_called`), sandbox-escape evidence, and `suspected_secret_emitted`.
- **FR-072**: System MUST provide filter and sort controls (by failure mode, by recency, by department), with filter state persisting in the URL.
- **FR-073**: System MUST link each row to the corresponding ticket detail.
- **FR-074**: For `suspected_secret_emitted` rows, the table MUST show the offending pattern category (e.g., "GitHub PAT-shape") but MUST NOT show the matched value itself.
- **FR-075**: System MUST refresh the hygiene table on a 30s soft-poll cadence and provide a manual refresh control.

#### Vault read views

- **FR-080**: System MUST render a secrets-list sub-view showing each `secret_metadata` row with path, allowed role slugs (from `agent_role_secrets.allowed_role_slugs`), last-rotated timestamp, and rotation status. The sub-view MUST NOT display any secret value.
- **FR-081**: System MUST render an audit-log sub-view showing `vault_access_log` rows with filters by role, by ticket, by time window. Both successful fetches and fail-closed paths MUST be visible, with offset+page-size pagination and 30s soft-poll refresh.
- **FR-082**: System MUST render a matrix sub-view showing roles × secrets with cell shading indicating grant presence (from `agent_role_secrets`).
- **FR-083**: All vault sub-views MUST connect via `garrison_dashboard_ro`. Failure of that role to authorize a query MUST surface as an operator-visible error, not a silent empty render.
- **FR-084**: No surface anywhere in the dashboard MUST expose a path to read or copy a secret value.

#### Agents registry

- **FR-090**: System MUST render every row in the `agents` table with department, role, model, concurrency cap, listens_for patterns (read-only display).
- **FR-091**: System MUST compute and display per-agent last-spawned-at and total-spawns-this-week from `agent_instances`.
- **FR-092**: Empty values (e.g., zero spawns) MUST display consistently with the empty-state vocabulary used across surfaces.

#### Universal table behavior

- **FR-100**: System MUST persist filter, sort, and pagination state in URL query params for every paginated/filterable surface, enabling shareable URLs and browser back/forward navigation.
- **FR-101**: System MUST display timestamps as relative-time hints with the full UTC ISO timestamp available on hover; relative-time strings MUST resolve through the i18n layer.
- **FR-102**: System MUST render an empty state for every surface that does not silently look broken when the underlying data is empty. Empty states include an icon, a translated one-line description, and a caption explaining what would populate the surface.

### Key entities

The dashboard reads (never writes) the following entities. All entities are owned by the M2 arc; M3 reads them.

- **Ticket** — represents a unit of work; lives in a department's Kanban board; transitions through columns. Source: `tickets` + `ticket_transitions`.
- **Ticket transition** — a single move of a ticket between columns, with associated hygiene status. Source: `ticket_transitions` (incl. the hygiene_status vocabulary defined across M2.2/M2.2.1/M2.3).
- **Agent** — a configured agent role within a department, with a model, concurrency cap, and listens_for patterns. Source: `agents`.
- **Agent instance** — a single spawn of an agent against a ticket, with cost, exit reason, duration. Source: `agent_instances` (cost telemetry has the documented blind spot for clean finalizes).
- **Diary entry** — a palace-side prose record of a transition; referenced by ticket_id. Source: MemPalace via the read-only references already on the Garrison side.
- **KG triple** — a (subject, predicate, object) record produced by an agent's `finalize_ticket` call. Source: MemPalace via the same read references.
- **Secret metadata** — a registered secret in the vault (path, allowed roles, rotation state). No values exposed. Source: `secret_metadata`.
- **Agent-role-secret grant** — a row in `agent_role_secrets` mapping a role to a secret. Source: `agent_role_secrets`.
- **Vault access log entry** — a row in `vault_access_log` recording a fetch (or fail-closed attempt). Source: `vault_access_log`.
- **Operator user** — a better-auth user record extended with a `theme_preference` field (or equivalent) for cross-device theme persistence. Writable in M3 via the auth subsystem (first-run setup, invite redemption) and the theme-switch path. Source: better-auth tables in `public`.
- **Operator invite** — a pending invitation for an additional operator, with a one-time-redeemable token, a created-at timestamp, an expires-at timestamp, a revoked flag, and a redeemed-at timestamp once consumed. Source: a new table colocated with the better-auth tables in `public`.

---

## Success criteria *(mandatory)*

### Measurable outcomes

- **SC-001**: A new operator can complete first-run setup, log in, and read the org overview against real M2-arc data within 5 minutes of receiving a deployed instance — without consulting docs or `psql`.
- **SC-002**: The activity feed renders a single 10-`assistant`-event run without dropping any event and without visual layout breakage on a 1280px viewport.
- **SC-003**: Every surface renders correctly on viewports of 768px, 1024px, and 1280px+ widths — verified by manual inspection across the seven surfaces.
- **SC-004**: Theme parity is observable: every surface in dark mode has a corresponding light-mode rendering with no broken contrast, no missing text, no semantic-color confusion. Verified by side-by-side screenshot comparison.
- **SC-005**: A code-level audit shows zero English-string JSX literals outside the locale catalog. Adding a stub second locale (e.g., `fr` with placeholder translations) re-renders surfaces with the new copy and zero missing-key errors.
- **SC-006**: The vault sub-views' runtime DSN resolves to `garrison_dashboard_ro` (not `garrison_agent_ro`, not `garrison_agent_mempalace`, not any other agent-facing role). Verified by runtime config inspection and by the Postgres `pg_roles` GRANT state.
- **SC-007**: All three documented hygiene-status failure modes are observable in the hygiene table when present in the underlying data. Verified by seeding one row per failure mode and confirming each appears, filterable, and click-throughs work.
- **SC-008**: Every display position of `total_cost_usd` carries the cost-blind-spot caveat treatment when the trigger predicate (`exit_reason='finalize_committed' AND total_cost_usd=0`) matches. Verified by inspection across ticket detail and any future surface that shows the column.
- **SC-009**: `dashboard/Dockerfile` produces a runnable production image; the image starts, connects to Postgres, authenticates an operator, and renders the org overview against real data — verified end-to-end against a deployed Postgres seeded from M2-arc activity.
- **SC-010**: The visual language of every surface matches the mocks at the design-system level: tokens, density, component vocabulary, mono-flavored ID treatment, status-dot/chip patterns. Verified by side-by-side review against the screens in `.workspace/m3-mocks/garrison-reference/`.
- **SC-011**: Drizzle and better-auth dependency justifications are captured in the M3 retro per `AGENTS.md` soft-rule discipline.

---

## Assumptions

- **A-001**: The dashboard runs as a fourth container in the existing single-host topology (supervisor + mempalace + socket-proxy + dashboard). Latency to Postgres is localhost-equivalent. Separate-host deployment is a future configuration flip, not an M3 deliverable.
- **A-002**: The operator pool is small (1–2 typical, more allowed via invite). Multi-tenant scenarios remain out of scope. Refines the context's "single-operator pool" framing to accommodate two-operator collaboration.
- **A-003**: M2-arc data shapes (`tickets`, `ticket_transitions`, `agent_instances`, `agents`, vault tables, hygiene_status vocabulary) are stable; no migration to M2-arc surfaces happens during M3.
- **A-004**: The MemPalace MCP is wired in the supervisor (M2.2-shipped) and the dashboard has read references to its content via Garrison-side tables; M3 does not query MemPalace directly via MCP.
- **A-005**: Production deployment topology is operator-controlled; the dashboard image must be runnable but does not prescribe orchestration (no fleet-wide CI/CD assumptions).
- **A-006**: The visual mocks in `.workspace/m3-mocks/garrison-reference/` are the design-language reference; mocks for surfaces deferred to M4/M5/M7 are explicitly ignored as feature scope.
- **A-007**: The cost-telemetry blind spot and the workspace-sandboxing failure mode are surfaced, not fixed, in M3. Their fixes land in subsequent work items.
- **A-008**: Better-auth's Postgres adapter is sufficient for the single-operator pool; no SSO, OAuth, or MFA requirements ship in M3.

---

## Open clarifications (for `/speckit.clarify`)

The following items require operator input before `/garrison-plan`:

- ~~**CL-001**: Sandbox-escape failure-mode visual treatment in ticket detail.~~ **Resolved 2026-04-26**: per-row icon + hover tooltip + click-to-expand for artifact-claimed-vs-on-disk detail (see Clarifications).
- ~~**CL-002**: Theme-preference persistence layer.~~ **Resolved 2026-04-26**: per-operator DB column on the better-auth user record (see Clarifications).
- ~~**CL-003**: Dashboard host topology relative to supervisor.~~ **Resolved 2026-04-26**: same-host fourth container in the existing topology; DSN env-var-driven so separate-host is a future config flip (see FR-014, A-001).
- **CL-004**: Drizzle CLI inclusion in the production Docker image — include for ops-time schema introspection vs exclude for image size. Affects FR-014 / SC-009. **Deferred to `/garrison-plan`** — plan-level packaging decision, not spec-blocking.
- ~~**CL-005**: Exact `pg_notify` channel set the activity feed subscribes to.~~ **Resolved 2026-04-26**: hardcoded explicit list in dashboard config covering at minimum `work.ticket.created.*`, finalize-related, and agent-instance lifecycle channels (see FR-060). The precise enumeration of channel names is a plan-level concern — the strategy is now spec-fixed.
