# Implementation plan: M3 — operator dashboard (read-only)

**Branch**: `008-m3-dashboard` | **Date**: 2026-04-26 | **Spec**: [spec.md](./spec.md)
**Input**: [spec.md](./spec.md), [m3-context.md](../_context/m3-context.md), AGENTS.md, RATIONALE.md, M2.3 retro, existing supervisor codebase

---

## Summary

M3 introduces Garrison's first frontend: a Next.js 16 + React 19 dashboard, read-only over M2-arc data, with better-auth-gated access for a small operator pool (1–2 typical), dual themes, full i18n machinery (English-only ships), responsive ≥768px, and a production Docker build that joins the existing supervisor + mempalace + socket-proxy compose topology as a fourth container.

Seven user-facing surfaces ship: org overview, department Kanban (read view), ticket detail, agent activity feed (live SSE), memory hygiene table, vault read views (3 sub-views isolated to `garrison_dashboard_ro`), agents registry — plus an admin-side invite flow (US-7) for adding a second operator.

The plan extends the existing repo: it does not touch supervisor Go code beyond adding one cross-boundary goose migration for the dashboard roles. All new code lives under `dashboard/`. Drizzle owns dashboard-side schema migrations (auth/invites/theme); goose owns supervisor-side schema and the cross-boundary role+GRANT migration.

---

## Technical Context

**Language/Version**: TypeScript 5.6+, Node.js 22 LTS (runtime), bun 1.x (build/install).
**Primary Dependencies**: Next.js 16 (App Router), React 19, Drizzle ORM + drizzle-kit, better-auth (with Postgres adapter), next-intl, TanStack Query, Tailwind CSS v4, postgres-js (or `pg`) for the LISTEN connection. Test: Vitest + Playwright.
**Storage**: Postgres (shared with supervisor). Two new dashboard-scoped roles: `garrison_dashboard_app` (auth + read on M2-arc tables) and `garrison_dashboard_ro` (vault tables only).
**Testing**: Vitest for unit (`*.test.ts` colocated); Playwright for end-to-end against a Postgres testcontainer + a built dashboard image.
**Target Platform**: Linux container, served behind the operator's reverse proxy or directly. Runtime image based on `node:22-alpine` with Next.js standalone output.
**Project Type**: Web application — frontend (Next.js, owns its own backend via App Router server components + route handlers).
**Performance Goals**: Activity feed renders 10 `assistant`-events runs without bloat (per spec SC-002). Static surfaces refresh in ≤200ms p95 against a localhost Postgres. SSE catch-up after a reconnect completes in ≤2s for the trailing 100 events.
**Constraints**: Image ≤250MB. Two-role DB isolation enforceable via grep on the codebase (vault queries only import `vaultRoDb`). i18n: zero English-string JSX literals outside `messages/en.json`. Responsive ≥768px (no mobile-first, no sub-768px breakpoints).
**Scale/Scope**: 1–2 operators concurrent, single-tenant, single Postgres. The dashboard is the operator console — designed for daily use, not for scaling beyond.

---

## Constitution Check

The Garrison constitution (`.specify/memory/constitution.md`) and the spec-kit conventions are honored as follows:

- **Specs first, code second**: spec is approved + clarified; this plan derives only from it.
- **Locked dependency discipline**: M3 introduces a substantial dashboard-side dep set (Next.js, React, Drizzle, better-auth, next-intl, TanStack Query, Tailwind, Vitest, Playwright). The locked-deps streak is supervisor-scoped (Go); dashboard deps follow the same justification discipline but on the dashboard side. Each dep is justified in the M3 retro per AGENTS.md soft-rule (SC-011).
- **Concurrency rule 8 (subprocess pipe drain)**: not directly applicable to the dashboard, but the SSE listener's connection lifecycle follows the same "drain readers before close" discipline by design (see §SSE listener lifecycle below).
- **Scope discipline (AGENTS.md §Scope discipline)**: M3 stays within the spec's seven surfaces + invite flow. No CEO chat, no hiring, no skill library, no mutation surfaces beyond auth itself, no fixes for the cost-telemetry or sandbox-escape issues (just surfaces them).
- **No mutation of sealed M2-arc surfaces**: the dashboard is purely additive. Supervisor Go code is untouched. The only schema additions to supervisor's purview are the cross-boundary roles+GRANTs migration, which adds new roles without modifying existing tables.

No constitutional violations to track.

---

## Project Structure

### Documentation (this feature)

```text
specs/008-m3-dashboard/
├── plan.md              ← this file (Phase 1 outputs are inline — see below)
├── spec.md              ← approved + clarified
└── tasks.md             ← Phase 2 output, produced by /garrison-tasks
```

Phase 1 outputs (data model, contracts, quickstart, research) are kept inline
in this plan rather than split into separate files: see §"Phase 1 — design
outputs" (data-model + contracts + quickstart) and §"Phase 0 — research outputs"
(research notes embedded as a numbered list). The single-file layout matches
M3's small surface area; if a future milestone's design grows past one file's
worth of content, we'll re-split.

### Source code

```text
dashboard/                           ← new top-level workspace
├── app/                             ← Next.js App Router
│   ├── (auth)/                      ← unauthenticated route group
│   │   ├── login/page.tsx
│   │   ├── setup/page.tsx           ← first-run wizard, 404 if users table populated
│   │   └── invite/[token]/page.tsx  ← invite redemption
│   ├── (app)/                       ← authenticated route group, shared layout = sidebar+topbar
│   │   ├── layout.tsx
│   │   ├── page.tsx                 ← org overview (route: /)
│   │   ├── departments/
│   │   │   └── [slug]/page.tsx      ← Kanban read view
│   │   ├── tickets/
│   │   │   └── [id]/page.tsx        ← ticket detail
│   │   ├── activity/page.tsx        ← live activity feed
│   │   ├── hygiene/page.tsx         ← memory hygiene table
│   │   ├── vault/
│   │   │   ├── page.tsx             ← vault landing → secrets list
│   │   │   ├── audit/page.tsx       ← audit log
│   │   │   └── matrix/page.tsx      ← role-secret matrix
│   │   ├── agents/page.tsx          ← agents registry
│   │   └── admin/
│   │       └── invites/page.tsx     ← pending invites + generate new
│   ├── api/
│   │   ├── auth/[...all]/route.ts   ← better-auth handler
│   │   ├── invites/
│   │   │   ├── route.ts             ← POST = generate; GET = list
│   │   │   └── [id]/revoke/route.ts ← POST = revoke
│   │   ├── theme/route.ts           ← PUT = update operator's theme_preference
│   │   └── sse/activity/route.ts    ← SSE endpoint
│   ├── globals.css                  ← Tailwind directives + CSS variables under [data-theme]
│   └── layout.tsx                   ← root layout, theme + locale providers
├── components/
│   ├── ui/                          ← chip, status-dot, kbd, tbl, kpi-card, expand-row, theme-switcher, locale-switcher, empty-state, cost-caveat-icon
│   ├── layout/                      ← Sidebar, Topbar
│   └── features/
│       ├── org-overview/
│       ├── kanban/
│       ├── ticket-detail/
│       ├── activity-feed/
│       ├── hygiene-table/
│       ├── vault/
│       ├── agents-registry/
│       └── invites/
├── lib/
│   ├── db/
│   │   ├── appClient.ts             ← Drizzle bound to DASHBOARD_APP_DSN
│   │   ├── vaultRoClient.ts         ← Drizzle bound to DASHBOARD_RO_DSN
│   │   └── listenClient.ts          ← dedicated postgres-js client for LISTEN (SSE)
│   ├── queries/
│   │   ├── orgOverview.ts
│   │   ├── kanban.ts
│   │   ├── ticketDetail.ts
│   │   ├── hygiene.ts
│   │   ├── vault.ts                 ← imports only vaultRoClient — grep-enforced
│   │   ├── agents.ts
│   │   └── invites.ts
│   ├── auth/
│   │   ├── index.ts                 ← better-auth instance
│   │   ├── invites.ts               ← generate / redeem / revoke / list
│   │   ├── errors.ts                ← AuthErrorKind enum + helpers
│   │   └── session.ts               ← Server-Component session reader
│   ├── sse/
│   │   ├── listener.ts              ← singleton LISTEN connection lifecycle
│   │   ├── channels.ts              ← KNOWN_CHANNELS + KNOWN_CHANNEL_PATTERNS + parsers
│   │   └── events.ts                ← discriminated-union event types
│   ├── i18n/
│   │   ├── config.ts                ← next-intl config
│   │   └── routing.ts               ← path-based locale routing
│   ├── time/
│   │   └── format.ts                ← UTC-stored / local-display helpers
│   └── theme/
│       └── resolve.ts               ← system-pref + operator-preference resolution
├── drizzle/
│   ├── schema.ts                    ← partitioned: supervisor (introspected) + dashboard (hand-written)
│   ├── migrations/                  ← drizzle-kit generate output, dashboard tables only
│   └── meta/                        ← drizzle-kit metadata
├── messages/
│   └── en.json                      ← English locale catalog (only locale shipped in M3)
├── styles/                          ← (if separated from globals.css) component-level CSS modules
├── public/                          ← static assets (logo SVGs from .workspace/m3-mocks/garrison-reference/brand/)
├── tests/
│   ├── fixtures/                    ← test-only seed data (NOT committed as production seeds)
│   └── integration/                 ← Playwright e2e specs
│       ├── first-run.spec.ts
│       ├── invite-flow.spec.ts
│       ├── activity-feed.spec.ts
│       ├── theme-parity.spec.ts
│       ├── responsive.spec.ts
│       ├── i18n.spec.ts
│       ├── vault-isolation.spec.ts
│       ├── cost-blind-spot.spec.ts
│       ├── sandbox-escape.spec.ts
│       └── concurrent-operators.spec.ts
├── Dockerfile                       ← multi-stage build/runtime
├── drizzle.config.ts
├── next.config.ts
├── tailwind.config.ts
├── tsconfig.json
├── package.json
└── bun.lock                         ← (or pnpm-lock.yaml if operator overrides)

migrations/                          ← existing goose-managed dir
└── 20260426000010_m3_dashboard_roles.sql   ← NEW — single cross-boundary migration

supervisor/docker-compose.yml        ← amended to add 4th service: dashboard
docs/ops-checklist.md                ← amended with M3 section (role passwords, BETTER_AUTH_SECRET, image digest)
```

**Structure Decision**: Web application layout (Next.js full-stack — server components + route handlers replace a separate backend). All new dashboard code under `dashboard/`. One new goose migration under `migrations/`. Existing supervisor Go code is untouched.

---

## Phase 0 — research outputs (inline)

The following four research items are recorded here in lieu of a separate `research.md`:

1. **better-auth invite-extension status** — quick check whether better-auth has a first-class invite plugin in the version we'll use; if not, the invite flow is hand-rolled against the `operator_invites` table using better-auth's session/user APIs only. Outcome binds slate item D-22 / `lib/auth/invites.ts`.
2. **next-intl App Router integration shape** — confirms the path-based locale routing approach (`/en/...`) is the supported pattern in next-intl with Next.js 16 App Router, including middleware setup.
3. **postgres-js LISTEN behavior under Next.js Node runtime** — confirms a singleton LISTEN connection survives across Next.js's per-request boundary in the App Router server runtime; documents reconnect behavior on connection drop.
4. **Drizzle-kit pull + generate coexistence in one schema.ts** — confirms it is possible to mark sections of `drizzle/schema.ts` as introspection-only and have `drizzle-kit generate` ignore them during diff. If not directly supported, falls back to two schema files (`schema.supervisor.ts` introspected + `schema.dashboard.ts` hand-written).

These four are the only items where empirical tool behavior could deviate from the docs in ways that affect plan correctness. Everything else is documented surface.

---

## Phase 1 — design outputs

### Data model (inline)

Captures the exact schema for the new tables and columns, plus the GRANT matrix.

**Drizzle-owned tables** (created by drizzle-kit migrations):

```text
users (better-auth core, extended)
  id              uuid PK
  email           text NOT NULL UNIQUE
  email_verified  boolean NOT NULL DEFAULT false
  name            text
  image           text
  created_at      timestamptz NOT NULL DEFAULT now()
  updated_at      timestamptz NOT NULL DEFAULT now()
  theme_preference text NOT NULL DEFAULT 'system'
                  CHECK (theme_preference IN ('dark','light','system'))

sessions (better-auth core)
  id              uuid PK
  user_id         uuid FK → users(id) ON DELETE CASCADE
  expires_at      timestamptz NOT NULL
  token           text NOT NULL UNIQUE
  created_at      timestamptz NOT NULL DEFAULT now()
  ip_address      inet
  user_agent      text

accounts (better-auth core)
  id              uuid PK
  user_id         uuid FK → users(id) ON DELETE CASCADE
  account_id      text NOT NULL
  provider_id     text NOT NULL
  password        text          -- hashed; null when provider != email
  ...             -- standard better-auth columns

verifications (better-auth core)
  id              uuid PK
  identifier      text NOT NULL
  value           text NOT NULL
  expires_at      timestamptz NOT NULL
  created_at      timestamptz NOT NULL DEFAULT now()

operator_invites (Garrison-defined)
  id                    uuid PK DEFAULT gen_random_uuid()
  token                 text NOT NULL UNIQUE
  created_by_user_id    uuid FK → users(id) ON DELETE SET NULL
  created_at            timestamptz NOT NULL DEFAULT now()
  expires_at            timestamptz NOT NULL
  revoked_at            timestamptz
  redeemed_at           timestamptz
  redeemed_by_user_id   uuid FK → users(id) ON DELETE SET NULL
  CONSTRAINT exactly_one_terminal_state
    CHECK (NOT (revoked_at IS NOT NULL AND redeemed_at IS NOT NULL))

  -- created_by_user_id is nullable + ON DELETE SET NULL to satisfy spec edge case
  -- "Inviter account deletion before redemption": if an inviter is deleted via
  -- direct SQL while their invite is pending, the invite remains valid until
  -- expiry or revocation by another operator.

  INDEX operator_invites_token_idx ON token
  INDEX operator_invites_pending_idx
        ON expires_at WHERE revoked_at IS NULL AND redeemed_at IS NULL
```

**Goose-owned migration** (`20260426000010_m3_dashboard_roles.sql`):

```text
-- Up
CREATE ROLE garrison_dashboard_app NOINHERIT;
CREATE ROLE garrison_dashboard_ro NOINHERIT;

-- App role: read-only on M2-arc tables
GRANT SELECT ON tickets, ticket_transitions, agents, agent_instances, event_outbox
  TO garrison_dashboard_app;

-- Vault read-only role: vault tables + the joinable read tables required for
-- audit-log filters by ticket / role and the role-secret matrix render
-- (per spec FR-021).
GRANT SELECT ON agent_role_secrets, vault_access_log, secret_metadata,
                tickets, ticket_transitions, agents, agent_instances
  TO garrison_dashboard_ro;

-- Down: REVOKE then DROP ROLE
```

**Drizzle migrations grant write access to their own tables to `garrison_dashboard_app`** in each generated migration's tail (the GRANT is inserted by a Drizzle migration hook or a hand-edit before the migration is committed).

### Contracts (inline)

Two contracts, both formalized as TypeScript types under `dashboard/lib/sse/events.ts` and `dashboard/lib/auth/errors.ts` (see §"Concrete interfaces" below):

- **SSE events** — discriminated-union schema for every event variant the activity feed produces. One variant per allowlisted channel: `work.ticket.created`, `work.ticket.transitioned.<dept>.<from>.<to>`, plus finalize-related and agent-instance lifecycle variants sourced via the `event_outbox` poll (per FR-060). The exact set is frozen at task-start in T015 against the supervisor's current emit sites.
- **Auth errors** — `AuthErrorKind` enum values + their i18n catalog keys + their HTTP status codes (mostly 401 / 410 / 403). Enumerated in §"Concrete interfaces > `lib/auth/errors.ts`" below.

### Quickstart (inline)

End-to-end operator flow:

1. From a clean checkout, run `goose up` (creates supervisor schema + the M3 roles).
2. Set role passwords via `psql` (procedure documented in the M3 section of `docs/ops-checklist.md`).
3. Set `BETTER_AUTH_SECRET` env var.
4. From `dashboard/`, run `bun install` then `bun run drizzle:migrate` (applies dashboard-side schema).
5. Run `bun run build` then `bun run start` (or use the production Docker image).
6. Visit the dashboard URL; first-run wizard appears; create the inaugural operator account.
7. Log in; org overview renders.
8. From admin/invites, generate an invite for the second operator; share the link out-of-band.
9. Second operator opens the link, sets name + password, lands logged in.

The same steps are exercised by Playwright integration tests.

---

## Subsystem state machines and lifecycles

### SSE listener lifecycle

```text
state: dormant  (no SSE clients connected)
  on first-client-connect → connecting

state: connecting  (LISTEN connection being established)
  on success → live
  on failure → backoff (start at 100ms, cap at 30s, double each attempt)

state: live  (LISTEN connection healthy, broadcasting to subscribers)
  on connection-lost → backoff
  on last-client-disconnect → idle-grace (60s)

state: backoff  (waiting before retry)
  on backoff-elapsed → connecting

state: idle-grace  (no clients, but holding LISTEN open for 60s)
  on new-client-connect → live
  on grace-elapsed → close-connection → dormant
```

The connection itself is owned by `lib/sse/listenClient.ts`: a single `postgres` client (postgres-js) configured with `prepare: false` and an explicit `LISTEN` invocation per channel in `KNOWN_CHANNELS`. Per-client SSE subscribers register a callback into a process-scope event emitter; the listener fans out to all callbacks. On `connection-lost`, all subscribers are notified (`sse_connection_lost`) and the catch-up flow re-runs on reconnect.

**Catch-up on reconnect**: when an SSE client reconnects and presents `Last-Event-ID`, the route handler queries `event_outbox` for `id > Last-Event-ID` ordered ascending, emits each row as an SSE event, then resumes live broadcast. The `event_outbox` SELECT requires `garrison_dashboard_app` to have SELECT on it (added in the goose role migration).

### Auth session lifecycle

better-auth defaults: cookie-based sessions, 7-day rolling expiry. No per-route revalidation beyond what better-auth provides. The first-run wizard route (`/setup`) is gated by a server-side check: `await db.select({ count: count() }).from(users)`. If `count > 0`, return 404. Otherwise serve the form. The check runs on every request (no caching).

### Invite redemption lifecycle

```text
state: created
  on revoke → revoked
  on expires-at-elapsed → expired
  on token-presented → consuming

state: consuming  (form rendered, waiting for submission)
  on submit-success → redeemed
  on submit-fail → consuming (form re-renders with error)
  on revoke → revoked  (race-handled in the consuming → redeemed transition)

state: redeemed  (terminal)
state: revoked   (terminal)
state: expired   (terminal)
```

The `created → redeemed` transition is enforced atomically in SQL:

```sql
UPDATE operator_invites
   SET redeemed_at = now(), redeemed_by_user_id = $1
 WHERE token = $2
   AND revoked_at IS NULL
   AND redeemed_at IS NULL
   AND expires_at > now()
RETURNING id;
```

If `RETURNING` is empty, the redemption is rejected with the appropriate `AuthErrorKind` (`invite_expired`, `invite_revoked`, `invite_already_redeemed`, or `invite_not_found`). The user creation happens in the same transaction.

### Soft-poll lifecycle (per-surface)

TanStack Query with `refetchInterval`:

- 30s: hygiene table, vault audit log, activity-summary KPIs (the count on the org overview).
- 60s: org-overview department rows.
- Manual refresh button on every paginated/streamed surface invokes the hook's `refetch()` directly.
- `refetchIntervalInBackground: false` — polling pauses when the tab is hidden.

---

## Concrete interfaces

### `lib/db/`

```ts
// appClient.ts
export const appDb = drizzle(postgres(process.env.DASHBOARD_APP_DSN!, { ... }), { schema: dashboardSchema });

// vaultRoClient.ts
export const vaultRoDb = drizzle(postgres(process.env.DASHBOARD_RO_DSN!, { ... }), { schema: vaultSchema });
```

The `vault.ts` queries module imports ONLY from `vaultRoClient.ts`. Enforcement is grep-based (a CI step rejects any `vaultRoDb` import outside `lib/queries/vault.ts`). Optional follow-up: ESLint custom rule.

### `lib/auth/invites.ts`

```ts
export type InviteRow = typeof operatorInvites.$inferSelect;

export async function generateInvite(creatorUserId: string): Promise<{ token: string; expiresAt: Date }>;
export async function listPendingInvites(): Promise<InviteRow[]>;
export async function revokeInvite(inviteId: string): Promise<void>;
export async function redeemInvite(token: string, name: string, password: string): Promise<{ userId: string; sessionToken: string }>;
```

`redeemInvite` is the atomic-transaction function described in the lifecycle. It throws a typed `AuthError` with one of the `AuthErrorKind` values on failure.

### `lib/sse/events.ts`

```ts
export type ActivityEvent =
  | { kind: 'ticket.created'; ticketId: string; dept: string; column: string; ts: string }
  | { kind: 'ticket.transitioned'; ticketId: string; dept: string; from: string; to: string; ts: string }
  | { kind: 'agent_instance.spawned'; instanceId: string; ticketId: string; ts: string }
  | { kind: 'agent_instance.completed'; instanceId: string; ticketId: string; exitReason: string; ts: string }
  | { kind: 'finalize.committed'; ticketId: string; instanceId: string; ts: string }
  | { kind: 'finalize.failed'; ticketId: string; instanceId: string; reason: string; ts: string };
```

The exact list pairs 1:1 with `lib/sse/channels.ts:KNOWN_CHANNELS` + `KNOWN_CHANNEL_PATTERNS`. Plan-execution confirms the precise channel name set against the supervisor's current emit sites (already enumerated in §Migrations: `work.ticket.created`, `work.ticket.created.<dept>.<column>`, `work.ticket.transitioned.<dept>.<from>.<to>`).

### `lib/auth/errors.ts`

```ts
export const AuthErrorKind = {
  NoSession: 'no_session',
  InviteNotFound: 'invite_not_found',
  InviteExpired: 'invite_expired',
  InviteRevoked: 'invite_revoked',
  InviteAlreadyRedeemed: 'invite_already_redeemed',
  FirstRunLocked: 'first_run_locked',
  EmailAlreadyExists: 'email_already_exists',
} as const;
export type AuthErrorKind = typeof AuthErrorKind[keyof typeof AuthErrorKind];

export class AuthError extends Error {
  constructor(public kind: AuthErrorKind, message?: string) { super(message ?? kind); }
}
```

Each `AuthErrorKind` has an i18n catalog key under `errors.auth.<kind>`.

---

## Test strategy

Test functions named to spec, grouped by file. Names use the `it`/`describe` Vitest pattern internally; the strings below are the test descriptions.

### Unit tests (Vitest, colocated)

- `lib/auth/invites.test.ts`:
  - `generateInvite produces a token of expected length and a future expires_at`
  - `redeemInvite accepts a valid unredeemed unrevoked unexpired token`
  - `redeemInvite rejects an expired token with InviteExpired`
  - `redeemInvite rejects a revoked token with InviteRevoked`
  - `redeemInvite rejects an already-redeemed token with InviteAlreadyRedeemed`
  - `redeemInvite rejects an unknown token with InviteNotFound`
  - `redeemInvite is atomic — concurrent redemptions of the same token produce exactly one success`
  - `revokeInvite is idempotent — revoking a revoked invite is a no-op`
  - `listPendingInvites excludes revoked, redeemed, and expired invites`
- `lib/sse/channels.test.ts`:
  - `KNOWN_CHANNELS list matches the supervisor's emit sites at plan-time` (against a checked-in fixture; CI fail if mismatch)
  - `parseChannel("work.ticket.created", payload) yields a TicketCreated event`
  - `parseChannel("work.ticket.transitioned.engineering.in_progress.review", payload) yields a TicketTransitioned event with parsed dept/from/to`
- `lib/sse/listener.test.ts`:
  - `listener starts in dormant and transitions to connecting on first subscriber`
  - `listener falls back to backoff after a connection error and retries with doubling delay capped at 30s`
  - `listener fans out a notification to multiple subscribers in subscription order`
  - `listener returns to dormant after last-subscriber + 60s grace`
- `lib/queries/vault.test.ts`:
  - `every export in vault.ts uses vaultRoDb` (static analysis test — reads the source file via fs and asserts no `appDb` import)
- `lib/auth/session.test.ts`:
  - `session reader returns null for unauthenticated requests`
  - `session reader returns the session for authenticated requests`
- `lib/time/format.test.ts`:
  - `format renders a UTC timestamp as relative-time in the operator's locale`
  - `format hover-value matches the exact ISO UTC representation`
- `lib/theme/resolve.test.ts`:
  - `resolves system preference when operator's theme_preference is "system"`
  - `resolves explicit preference over system when operator's theme_preference is "dark" or "light"`

### Integration tests (Playwright, against a Postgres + dashboard image)

Each test bootstraps a fresh Postgres testcontainer + applies goose + Drizzle migrations + seeds the relevant fixture, then exercises the real built dashboard image.

- `tests/integration/first-run.spec.ts`:
  - `first-run wizard creates the inaugural operator account on a fresh DB`
  - `first-run wizard returns 404 once the users table is non-empty`
  - `every protected route redirects to /login when no session exists`
- `tests/integration/invite-flow.spec.ts`:
  - `authenticated operator can generate an invite link from /admin/invites`
  - `pending invite appears in the operator's invite list`
  - `invitee can redeem the link and lands logged in as a peer operator`
  - `revoked invite link returns InviteRevoked on redemption attempt`
  - `expired invite link returns InviteExpired on redemption attempt`
  - `concurrent redemptions of the same invite produce exactly one successful account`
- `tests/integration/activity-feed.spec.ts`:
  - `single 10-assistant-event run renders without dropping events and without overflow on 1280px viewport`
  - `SSE reconnect catches up via Last-Event-ID and resumes live broadcast`
  - `event-type filter persists in URL and survives a hard reload`
  - `per-agent filter narrows the feed without losing the SSE subscription`
- `tests/integration/theme-parity.spec.ts`:
  - `every primary surface renders in dark theme without missing contrast`
  - `every primary surface renders in light theme without missing contrast`
  - `theme switch persists across browser sessions for the same operator`
- `tests/integration/responsive.spec.ts`:
  - `every primary surface renders without horizontal page scroll at 768px`
  - `every primary surface renders without horizontal page scroll at 1024px`
  - `every primary surface renders without horizontal page scroll at 1280px+`
- `tests/integration/i18n.spec.ts`:
  - `swapping the active locale to a stub second locale renders translated copy with zero missing-key warnings`
  - `falling back to English on a missing key does not surface raw catalog keys`
- `tests/integration/vault-isolation.spec.ts`:
  - `vault sub-views fail with operator-visible error when vaultRoDb is misconfigured`
  - `inspecting the vault DSN at runtime confirms it resolves to garrison_dashboard_ro`
  - `attempting to read agent_role_secrets via appDb fails with permission denied`
  - `no UI surface anywhere exposes a path to read or copy a secret value`
- `tests/integration/cost-blind-spot.spec.ts`:
  - `clean-finalize zero-cost row in ticket detail shows the caveat icon with i18n tooltip`
  - `non-zero cost rows do not show the caveat icon`
- `tests/integration/sandbox-escape.spec.ts`:
  - `sandbox-escape transition row in ticket detail shows the icon and is expandable`
  - `expanded row reveals "claimed: X / on-disk: Y" detail`
- `tests/integration/concurrent-operators.spec.ts`:
  - `two distinct operator accounts can be logged in simultaneously without collision`
  - `both operators see identical data`

### Regression discipline

The supervisor Go test suite is unaffected — no Go file is modified by M3 except via the new role-creating SQL migration, which only adds GRANTs and roles. A new `make test` target in the dashboard runs `bun test` (Vitest); the existing `cd supervisor && go test ./...` continues to pass without modification. Both go into the M3 retro acceptance evidence.

---

## Deployment changes

### `dashboard/Dockerfile`

```text
# stage 1: build
FROM oven/bun:1-alpine AS build
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build   # produces .next/standalone

# stage 2: runtime
FROM node:22-alpine AS runtime
WORKDIR /app
RUN addgroup -S dashboard && adduser -S dashboard -G dashboard
COPY --from=build --chown=dashboard:dashboard /app/.next/standalone ./
COPY --from=build --chown=dashboard:dashboard /app/.next/static ./.next/static
COPY --from=build --chown=dashboard:dashboard /app/public ./public
USER dashboard
EXPOSE 3000
CMD ["node", "server.js"]
```

drizzle-kit is NOT included in the runtime image (per CL-004 deferral resolution).

### `supervisor/docker-compose.yml` amendment

Add a fourth service:

```yaml
  dashboard:
    build:
      context: ../dashboard
      dockerfile: Dockerfile
    depends_on:
      - postgres
    environment:
      DASHBOARD_APP_DSN: ${DASHBOARD_APP_DSN}
      DASHBOARD_RO_DSN: ${DASHBOARD_RO_DSN}
      BETTER_AUTH_SECRET: ${BETTER_AUTH_SECRET}
      BETTER_AUTH_URL: ${BETTER_AUTH_URL}
    ports:
      - "3000:3000"
```

The two DSNs use the operator-set passwords for the two new roles. `BETTER_AUTH_URL` is the operator-facing base URL used for invite link generation.

### `docs/ops-checklist.md` — new M3 section

Subsections:

1. **Goose migrations**: run `goose up` to land the role-creation migration before drizzle.
2. **Role passwords**: `ALTER ROLE garrison_dashboard_app PASSWORD '...'; ALTER ROLE garrison_dashboard_ro PASSWORD '...';` — set via the same procedure used for `garrison_agent_mempalace` in the M2.2 ops-checklist section.
3. **`BETTER_AUTH_SECRET` generation**: `openssl rand -hex 32` (or equivalent); persist to operator's secret store.
4. **Drizzle migrations**: from `dashboard/`, `bun install && bun run drizzle:migrate`.
5. **Dashboard image digest pinning**: post-build, capture and record the image digest in deployment notes (matches the M2.3 ops-checklist Infisical-image-pinning pattern).
6. **First-run setup**: visit dashboard URL → setup wizard → create inaugural account.

---

## Open questions for plan execution

These are flagged in this plan for resolution during plan-execution rather than baked in:

1. **Package manager final**: bun is proposed; if the operator overrides to pnpm or npm before tasks land, the package.json + lockfile naming changes but no other plan content does.
2. **better-auth invite plugin existence**: research item 1 from Phase 0 confirms whether to use a first-class plugin or hand-roll. Hand-rolled is the fallback and the safer assumption.
3. **drizzle-kit pull + generate coexistence in one schema.ts**: research item 4. Two-file fallback is on the table.
4. **Drizzle CLI inclusion in production image** (CL-004 from spec): the plan commits to NOT including it. Reconfirmed here.
5. **Exact channel-list freeze**: the supervisor's current emit sites are enumerated in this plan. If new channels land between plan-write and plan-execute, the channel allowlist gets a one-line update before tasks land.

---

## Forward-compatibility hooks (one-line each, no design)

- The `vaultRoDb` / `appDb` two-client pattern extends to M4 (writes against the vault) by introducing a third client `vaultWriteDb` bound to a future write-capable role.
- The discriminated-union `ActivityEvent` type extends to M5 (CEO chat) by adding a `ceo.message.*` channel family.
- The empty-state vocabulary in `components/ui/EmptyState.tsx` reuses for M7 (hiring queue) and M8 (skill registry).
- The invite flow's atomic-redemption pattern is the template for M4's "approve hire" or any other "single-fire transition" surface.

These are mentions only. M3 does not design for them.

---

*Plan written 2026-04-26 on branch `008-m3-dashboard`. Next: `/garrison-tasks m3` to break this plan into tasks.*
