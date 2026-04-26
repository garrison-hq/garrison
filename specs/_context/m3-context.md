# M3 context — operator dashboard (read-only)

**Status**: Context for `/garrison-specify m3`. M3 is the active milestone per `AGENTS.md`. Prior milestone: M2.3 (Infisical secret vault, shipped 2026-04-24, retro at `docs/retros/m2-3.md`). This is the first frontend-bearing milestone; M1 → M2.3 were all supervisor-side.

**Binding inputs** (read these before writing the spec; full annotations in §Binding inputs below):

- `ARCHITECTURE.md` §Dashboard surfaces, §M3, §Open questions for later
- `AGENTS.md` §M3 activation, §Scope discipline, §What agents should not do
- `RATIONALE.md` §3 (memory thesis), §5 (soft gates), §7 (dashboard surfaces hygiene), §8 (web UI is the operator console)
- `specs/_context/m2.3-context.md` §"Deferred to M3 (dashboard read-only)"
- `docs/issues/cost-telemetry-blind-spot.md`, `docs/issues/agent-workspace-sandboxing.md`
- `docs/retros/m2-1.md` (5–10 assistant-events-per-run observation)
- `.workspace/m3-mocks/garrison-reference/` (visual-language reference only, NOT feature scope — see §Why this milestone now)

---

## Why M3 now

After M2.3, every supervisor surface in the M-arc is functional end-to-end: agents spawn, MCP servers attach, the finalize tool commits transitions, the palace mirrors them, and the vault feeds secrets through with audit rows. None of it is visible. The operator is reading `psql` queries and `tail -F` to know what's happening.

`RATIONALE.md` §8 names the dashboard as the operator console — "you live here day-to-day." Until the operator can see the system behave, every observation is mediated through ad-hoc SQL, and bugs in event payload shape, hygiene evaluation, and audit log content go uncaught. M3 closes that loop with read views over the data the M2 arc already produces.

The milestone is intentionally read-only. `ARCHITECTURE.md` §M3 is explicit: "No mutations yet. Read-only-first forces you to actually watch the system behave for a few days before giving yourself (and agents) the ability to change state — which is when you catch the 'oh, the event payload shape is wrong' class of bugs cheaply." M4 takes the write surface; M3 is the observation milestone.

M3 also discharges three M2-arc commitments that were deferred to "the dashboard":

1. **Vault read views** (M2.3 context §"Deferred to M3"): secrets list, audit log, agent-role-to-secret matrix, rotation status — accessed via a dashboard-scoped Postgres role, not any agent-facing role.
2. **Hygiene-status surface** (M2.2 onward): the three failure modes — sandbox-escape "artifact claimed vs on disk", `finalize_never_called`, `suspected_secret_emitted` — are all stored on `ticket_transitions.hygiene_status` but never displayed. M3 makes them reviewable.
3. **Cost-telemetry blind-spot caveat** (`docs/issues/cost-telemetry-blind-spot.md`): clean-finalize runs record `$0.00`. M3 must surface this caveat alongside any UI showing `agent_instances.total_cost_usd`. The fix lands in a later supervisor patch, not in M3.

---

## In scope for M3

Seven read-only surfaces, gated by an operator-auth layer, served from a Next.js 16 + React 19 app reading from the existing Postgres DB through Drizzle, with one streamed surface (activity feed) over Server-Sent Events.

### 1. Org overview

The landing surface. KPI strip across the top (totals: open tickets, active agents, recent transitions in last 24h, hygiene warnings count). Below: one row per department (engineering, qa-engineer for now) showing open ticket count per Kanban column, active agent count vs cap, last transition timestamp, hygiene-warning count for that department. A small recent-transitions table and a live-spawns list round out the surface.

The mocks (`screen-overview.jsx`) communicate the density and component vocabulary; do not lift the specific KPI labels or numbers — they are illustrative.

### 2. Department Kanban (read view)

Per-department page showing all four Kanban columns with ticket cards. **No drag-to-move; no inline edits.** Cards show priority edge color, ticket title/ID, assigned agent if any, age. Clicking a card opens the ticket detail surface.

### 3. Ticket detail

Full ticket view. Metadata block (id, dept, column, created/updated, priority, status). History block: every `ticket_transitions` row in chronological order, with hygiene_status per transition. Agent-instances block: every `agent_instances` row that touched this ticket, with exit_reason, total_cost_usd (with blind-spot caveat icon when the value is `$0.00` on a `finalize_committed` exit), duration. Palace-links block: the diary entry and KG triples written for this ticket, with a one-line excerpt and a copy-id button (the palace UI is out of scope; the link is informational).

### 4. Agent activity feed

The single streamed surface. Live event feed of agent spawns, completions, transitions, finalize results. Streamed via SSE from a backend route that subscribes to relevant `pg_notify` channels (`work.ticket.created.*`, finalize-related notifications, agent-instance lifecycle events) — exact channel set and bridging mechanism are spec-level decisions.

**Critical constraint** (from `docs/retros/m2-1.md`): real Claude Code emits 5–10 `assistant` events per run, not the 2 the early mock fixtures showed. The activity feed must render a 10-event run without visual bloat. Grouping/collapsing strategy is a spec decision (see Open questions). Dropping events silently is not acceptable.

### 5. Memory hygiene

Table of `ticket_transitions` rows with non-clean `hygiene_status`. All three current failure modes must be filterable and visible:

- **`finalize_never_called`** and adjacent finalize-path statuses (M2.2.1 vocabulary)
- **Sandbox-escape "artifact claimed vs artifact on disk"** (per M2.2.x retro and `docs/issues/agent-workspace-sandboxing.md`)
- **`suspected_secret_emitted`** (M2.3 finalize-path scanner hook)

Click-through to ticket detail. The "jump to palace wing to backfill" affordance described in `ARCHITECTURE.md` is M6 work, not M3 — M3 just shows the row and lets the operator copy the relevant IDs.

### 6. Vault read views

Three sub-views, all reading via a dedicated dashboard-scoped Postgres role with explicit SELECT grants on `agent_role_secrets`, `vault_access_log`, `secret_metadata`. **Do not reuse `garrison_agent_ro`, `garrison_agent_mempalace`, or any other agent-facing role** (per AGENTS.md §M3 and the M2.3 vault threat model).

- **Secrets list**: rows of `secret_metadata` joined to `allowed_role_slugs`, showing path, allowed role slugs, last rotated, rotation status. **No secret values displayed anywhere.**
- **Audit log**: `vault_access_log` with filters by role, by ticket, by time window. Surfaces both successful fetches and the fail-closed paths (since INSERT is required for the spawn to proceed, every spawn that fetched a secret has a row).
- **Agent-role-to-secret matrix**: `agent_role_secrets` rendered as a matrix view (roles × secrets), letting the operator see at a glance which agent role can fetch which secret.

Rotation, grant edits, and any vault writes are M4 — M3 only reads.

### 7. Agents registry

Table of all configured agents (department, role, model, concurrency, last-spawned-at, total spawns this week). Static read; settings edits are M4.

### Cross-cutting: auth, theming, layout

- **Auth**: better-auth, Postgres-backed (using the existing Garrison Postgres instance, not a separate auth DB). Single operator pool — multi-user is out of scope. First-run flow for operator account creation is a spec decision (seed migration vs CLI command vs first-run wizard).
- **Theme**: dark and light themes both. Dark is the primary, modeled on the mocks (`.workspace/m3-mocks/garrison-reference/styles.css`). Light is a parity treatment — same density, same component vocabulary, same semantic-color meanings. Theme switcher in the topbar with system-preference default. Tokens defined as CSS variables with a `[data-theme]` toggle (or equivalent) so adding a third theme later is a token-file edit.
- **Responsive**: every surface must be usable on viewports from ≥768px (tablet portrait) up. The dashboard is still an operator desktop tool first — no mobile-first rewrite — but a wide tablet, a small laptop, and a half-screen browser all need to work. Layout strategies (sidebar collapse, table reflow, Kanban column horizontal scroll) are spec decisions.
- **i18n**: every user-facing string must go through an i18n layer. English is the only locale shipped in M3, but no string is hardcoded in JSX — all flow through the locale machinery. Library choice (`next-intl`, `react-i18next`, etc.) is a spec decision; the rule is "no English-string-shaped JSX literals in component code."
- **Layout**: sidebar + topbar shell shared across surfaces, per `shell.jsx` in the mocks.
- **Drizzle**: introduced this milestone. SQL migrations remain canonical (consumed by both `sqlc` for the supervisor and Drizzle on the dashboard side). Drizzle does **not** run its own migrations — it generates types from the existing migration output. Coexistence with goose-driven supervisor migrations is a spec decision.
- **Routing**: Next.js 16 App Router. Server Components for static reads; Client Components only where SSE/interactivity demand it (activity feed, any filter UI).
- **Deployment**: production Docker build is in scope. Multi-stage Dockerfile producing a minimal runtime image for the Next.js app, alongside the existing `supervisor/Dockerfile` and `Dockerfile.mempalace`. Compose wiring (or whatever orchestration the operator uses) is a spec decision; M3 ships at minimum a self-contained `dashboard/Dockerfile` that runs the app in production mode.

---

## Out of scope for M3 (explicit deferrals)

### Deferred to M4 (dashboard mutations)

- Drag-to-move on Kanban — the mocks show this; it is M4 work
- Ticket creation in UI
- Agent settings editor (agent.md, model, concurrency, listens_for, skills)
- Vault grant edits, secret rotation triggering, secret value reveal flows
- Hygiene "backfill" actions — clicking through to fix a thin/missing palace write

### Deferred to M5 (CEO chat)

- CEO chat surface (the `screen-ceo.jsx` mock)
- Conversation panel, tool-call traces, thread history

### Deferred to M6 (CEO ticket decomposition + hygiene backfill)

- Hygiene → palace-wing jump-and-backfill workflow
- Aggregate cost view across tickets and agents
- Rate-limit back-off acting on `rate_limit_event`

### Deferred to M7 (hiring + skills)

- Hiring queue / proposal review (`screen-hiring.jsx` mock)
- Skill library — installed + skills.sh browse + SkillHub integration

### Explicitly out of scope for M3 entirely (not a deferral, a non-goal)

- **Fixing the cost-telemetry blind spot.** M3 surfaces the caveat; the supervisor signal-handling fix is its own work item.
- **Fixing workspace-sandbox escape.** M3 displays the failure mode in hygiene + ticket detail; the per-agent Docker container fix is post-M3.
- **Multi-user / multi-tenant.** Operator-only; better-auth gates a small operator pool, not public access.
- **Mobile-first / sub-768px design.** Responsive down to tablet portrait is in scope (see §In scope cross-cutting); phone-sized breakpoints, touch-first interactions, and offline modes are not.
- **Additional locales beyond English in M3.** The i18n machinery ships; only the English locale is populated. Adding a second locale is a follow-up — the test of M3 is that doing so requires only translation files, no component changes.
- **Theme customization beyond dark + light.** Two themes via tokens; no per-user theme builder, no third theme, no high-contrast mode.
- **Replacing or revising any sealed M2-arc surface** — supervisor spawn semantics, finalize tool schema, vault rules, `garrison_agent_*` roles, MemPalace MCP wiring are read-only inputs to M3.

---

## Binding inputs

Read each before writing the spec. Annotations name the load-bearing decision each input carries.

| Input | What it binds |
|---|---|
| `ARCHITECTURE.md` §M3 | "Read-only" framing; the three carried-forward notes (high-volume events, cost blind-spot surfacing, hygiene status surfacing). |
| `ARCHITECTURE.md` §Dashboard surfaces | The 9-surface map; M3 takes 7 of them in read-only form. |
| `ARCHITECTURE.md` §Sandboxing | The "artifact claimed vs artifact on disk" failure mode the hygiene table must surface. |
| `AGENTS.md` §M3 activation | Vault dashboard-scoped read role; hygiene_status surface; cost-telemetry caveat; high-volume event rendering; locked-deps soft-rule context. |
| `AGENTS.md` §Scope discipline | The M3 out-of-scope list (CEO, hiring, skills, MCP registry, sandbox fix, cost-telemetry fix, multi-dept wire-up, mutation of M2 surfaces). |
| `AGENTS.md` §What agents should not do | Concurrency rule 8 applies to any backend SSE bridge that spawns Postgres LISTEN goroutines (if implemented in Go) or to subprocess-piping in any tooling. |
| `RATIONALE.md` §5 | Soft gates: hygiene is reviewed weekly, not blocked. The dashboard must make weekly review feasible — i.e., filterable, sortable, with enough context per row to triage without `psql`. |
| `RATIONALE.md` §7 | Hygiene-warning surface is a primary product surface, not a debug page. |
| `RATIONALE.md` §8 | The dashboard *is* the operator console. Spec must treat it as a primary product, not internal tooling. |
| `specs/_context/m2.3-context.md` §"Deferred to M3" | The exact vault read-view list, the dashboard-scoped role requirement, the no-secret-values-displayed rule. |
| `docs/issues/cost-telemetry-blind-spot.md` | Surfacing requirements: any `total_cost_usd` display must caveat clean-finalize zero-cost rows. |
| `docs/issues/agent-workspace-sandboxing.md` | Sandbox-escape failure mode is real and triagable — the hygiene table and ticket detail must show it. |
| `docs/retros/m2-1.md` | 5–10 `assistant` events per real run; the activity feed must absorb that without visual collapse. |
| `docs/retros/m2-2.md`, `m2-2-1.md`, `m2-2-2.md`, `m2-2-x-compliance-retro.md` | Hygiene-status vocabulary evolution; M3 must render the full vocabulary, not just the M2.2 set. |
| `.workspace/m3-mocks/garrison-reference/` | **Visual language only.** Tokens, density, type, component vocabulary, dark-UI tone. The README in that folder is explicit: "Don't use this for feature scope." Mocks include screens for M4/M5/M7 surfaces — ignore those. |

If any of these are missing or unreadable when the spec is written, stop and surface the gap. Do not proceed with a partial input set.

---

## Open questions the spec must resolve

The context bounds these — it does not decide them.

### Activity feed bridging

How does the dashboard receive live events? Two main shapes:

- **(a) Direct LISTEN from Next.js**: a Node-side Postgres client opens a `LISTEN` and pushes events to clients via SSE. Simple, but couples connection-pool concerns to the dashboard process.
- **(b) Supervisor-emitted stream**: the supervisor (or a small helper binary) exposes an HTTP/SSE endpoint that the dashboard subscribes to. Decouples the dashboard from direct Postgres LISTEN responsibility.

Trade-offs (reconnection, backpressure, ordering, scaling beyond a single dashboard process) belong in the spec.

### Activity feed rendering strategy for high-volume runs

10 assistant events per run × concurrent agents = significant event volume. Strategies the spec must pick from:

- Group-by-agent-instance with collapsed sub-events
- Virtualized list with dense rows
- Time-windowed batching (events within N seconds collapse to "N events")
- Filter chips (per-event-type, per-agent, per-ticket)

The constraint is "no visual bloat with a 10-event run" — the spec defines what "no bloat" measures look like.

### Cost blind-spot UI surface

How does `agent_instances.total_cost_usd = $0.00` on a `finalize_committed` row appear in the UI? Options: caveat icon with hover tooltip, inline italic note, separate "actual cost unavailable" status. Whatever the choice, every place the column is shown applies the caveat consistently. The spec must name the visual treatment and the trigger predicate (likely `exit_reason = 'finalize_committed' AND total_cost_usd = 0`).

### Better-auth schema integration

There's no existing user model in Garrison — `agents` and `companies` exist; operator users do not. The spec must decide:

- Better-auth tables live in the Garrison Postgres DB (yes — confirmed) but in which schema? `public` alongside the existing tables, or a `auth` schema?
- Tables managed by better-auth migrations (run on dashboard startup) or by the Garrison migration story (added to the SQL migration sequence)?
- First-run operator-account creation: seed migration with a default operator (insecure), CLI command on the supervisor (`supervisor admin create-operator`), or first-run wizard route (`/setup` accessible only when no operator exists)?

### Drizzle and goose coexistence

`sqlc` generates Go types from `migrations/*.sql`. The spec must define:

- Where Drizzle config lives (likely `dashboard/drizzle/`)
- Drizzle schema-generation strategy: introspect the live DB, parse the SQL files, or hand-maintain a parallel schema file
- That Drizzle does NOT run migrations — goose remains the sole migrator
- The CI/local-dev story for keeping Drizzle types in sync with new migrations

### Vault dashboard role

Spec must name the role (e.g. `garrison_dashboard_ro` or similar), enumerate exact GRANT statements, and add a migration introducing the role. Password lifecycle goes in `docs/ops-checklist.md`, mirroring the M2.3 pattern for `garrison_agent_mempalace`.

### Static-surface refresh story

For the six non-streamed surfaces (everything except activity feed), what's the refresh model? Options: manual refresh button only, soft polling on a timer (and per-surface cadence), Server Components revalidating on navigation, a global "live" toggle that escalates static surfaces to subscribe to relevant `pg_notify`. The spec picks one.

### Pagination and filtering across tables

Every table surface (tickets, hygiene, audit log, activity, agents, vault) needs a pagination story. Cursor-based vs offset, server-side vs client-side, filter persistence in URL. The spec defines a consistent pattern.

### Time zones and timestamp display

Operator-local? UTC with a relative-time hint? Both? The spec picks one and applies it consistently.

### Empty-state handling

A fresh-install dashboard has zero tickets, zero transitions, zero audit rows. Each surface needs an empty state that does not look broken. The spec names the empty-state copy and visual treatment.

### Server-Component vs Client-Component split per surface

Activity feed must be a Client Component (SSE). Filters and sortable tables need client-side reactivity. Static metadata blocks (ticket detail header, agents registry) can be Server Components. The spec defines the split per surface.

### Drizzle/Better-auth dependency-justification entries

Both are dashboard-side adds. The supervisor's locked-deps streak (M1 → M2.2.2 zero deps, broken at M2.3 with two principled adds) does not technically apply — but both deps still warrant a one-paragraph justification per AGENTS.md "soft rule on the list," to be captured in the M3 retro. The spec should note this discipline carries forward to dashboard deps.

---

## Acceptance criteria framing

The spec writes the full criteria. The framing the spec must hit:

- **Functional**: all 7 surfaces render against real M2-arc data (not fixtures), with the operator able to navigate from sidebar to any surface and back without 404s or empty-data crashes.
- **Auth**: every route except the login (and any first-run setup route) requires a valid better-auth session. Logged-out access redirects to login.
- **Activity feed performance**: a 10-`assistant`-event single-agent run renders in the feed without losing events and without visual collapse. Spec defines the precise observable.
- **Vault isolation**: vault views read exclusively through the dashboard-scoped role; this is verifiable by inspecting the connection-string DSN in dashboard runtime config and the Postgres `pg_roles` GRANT state.
- **Hygiene completeness**: all three hygiene_status failure modes (sandbox-escape, finalize-never-called, suspected_secret_emitted) appear in the hygiene table when present in the underlying data. Verifiable by seeding rows and checking display.
- **Cost-blind-spot caveat**: every `total_cost_usd` display position carries the caveat treatment when the trigger predicate matches. Verifiable by inspection.
- **Visual fidelity (dark)**: the styles.css token system from the mocks is adopted; the dark surface tones, mono ID treatment, status-dot/chip vocabulary, and density match. Verifiable by side-by-side with the mock screens (visual-language match, not pixel-perfect).
- **Theme parity**: light theme renders every surface without broken contrast, with the same density and component vocabulary as dark. Theme switcher works; preference persists per operator.
- **Responsive**: every surface usable at 768px / 1024px / 1280px+ viewports without overflow, broken layouts, or hidden controls.
- **i18n**: a code-level audit shows zero English-string JSX literals outside the locale catalog; swapping the locale (even to a stub second locale used only for verification) re-renders surfaces with translated copy and no missing keys.
- **Production build**: `dashboard/Dockerfile` produces a runnable production image; the image starts, serves the dashboard, connects to Postgres, authenticates an operator, and renders the org overview with real M2-arc data.
- **Drizzle/Better-auth justifications**: dependency add justifications captured in the M3 retro per AGENTS.md soft-rule discipline.

---

## What this milestone is NOT

- It is not a writable dashboard. Every M3 surface is a read view. The mocks include drag-to-move and editor surfaces; those are M4.
- It is not a real-time dashboard everywhere. Only the activity feed is streamed; everything else is static-with-refresh.
- It is not the CEO chat (M5).
- It is not the hiring or skill-browser flows (M7).
- It is not a fix for the cost-telemetry blind spot or the workspace-sandbox issue. M3 surfaces both; the fixes are separate work items.
- It is not a multi-user product. Better-auth is gating a single-operator pool, not a public app.
- It is not multi-tenant or multi-company.
- It is not a mobile or responsive design exercise.
- It is not a redesign milestone. The visual language is provided by the mocks; M3 implements that language, it does not invent a new one.
- It is not a packaging milestone in the broad sense. A production Docker build for the dashboard is in scope; orchestrating fleet-wide deployment, CI/CD, and image-registry tooling is not.

---

## Spec-kit flow

When M3 specification opens:

1. **Step 0 (precondition)**: cut the M3 branch via `/speckit-git-feature` (or `git checkout -b 008-m3-dashboard`). Do not let M3 work land on the M2.3 branch — `AGENTS.md` §Spec-kit workflow is explicit about this after the M2.3 branch-naming drift.
2. `/garrison-specify m3` against this context.
3. `/speckit.clarify` — resolve the open questions above before planning.
4. `/garrison-plan m3` — Next.js 16 + React 19 + Drizzle + better-auth + SSE bridge + dashboard-scoped Postgres role.
5. `/garrison-tasks m3` — break the plan into tasks. Expect a substantial scaffolding chunk up front (Next app, Drizzle config, better-auth wiring, sidebar shell) before per-surface work begins.
6. `/speckit.analyze` — cross-artifact consistency check.
7. `/garrison-implement m3`.
8. **Retro** — both markdown (`docs/retros/m3.md`) and MemPalace `wing_company / hall_events` drawer mirror per the M3-onwards retro policy. This is the first milestone where the dual-deliverable retro is non-historical.

---

*Context written 2026-04-26 on branch `006-m2-2-2-compliance-calibration`. The M3 branch will be cut at `/speckit-git-feature` time.*
