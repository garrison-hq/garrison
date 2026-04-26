# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
once it reaches 1.0. Until then, version numbers correspond to
milestones (M1, M2, ...).

## [Unreleased]

Nothing yet. M4 scope: dashboard mutations — create tickets in UI,
drag between columns, edit agent configs. See
[`ARCHITECTURE.md`](./ARCHITECTURE.md) §"Build plan — milestones".

> **Note**: M2.1, M2.2, M2.2.1, M2.2.2, and M2.3 shipped between
> M1 and M3 but are not currently entered in this changelog;
> their canonical retros live at `docs/retros/m2-1.md`,
> `docs/retros/m2-2.md`, `docs/retros/m2-2-1.md`,
> `docs/retros/m2-2-2.md`, and `docs/retros/m2-3.md`.
> Architecture-level summaries are in `ARCHITECTURE.md`.

---

## [M3] — 2026-04-26

The operator dashboard, read-only. Next.js 16 + React 19 + Tailwind
v4 served by a single container behind better-auth. Eight read
surfaces (org overview, department Kanban, ticket detail, hygiene
table, vault list/audit/role-secret-matrix, agents registry,
operator-invite admin) plus a real-time SSE activity feed. No
mutations — read-only-first by design.

### Added

- `dashboard/` Next.js app: 8 routes, 10 reusable UI primitives
  (Chip, StatusDot, Kbd, Tbl, KpiCard, ExpandRow, ThemeSwitcher,
  LocaleSwitcher, EmptyState, CostCaveatIcon), Sidebar + Topbar
  shell, dark/light/system theme persisted to
  `users.theme_preference`.
- Two new Postgres roles via goose migrations
  (`20260426000010_m3_dashboard_roles.sql` +
  `20260426000011_m3_dashboard_dept_grants.sql`):
  `garrison_dashboard_app` (operational reads on M2-arc tables +
  better-auth dashboard tables) and `garrison_dashboard_ro` (vault
  sub-views ONLY, plus the joinable read tables FR-021 needs).
- Drizzle ORM with a partitioned schema —
  `drizzle/schema.supervisor.ts` (introspected, never hand-edited)
  and `drizzle/schema.dashboard.ts` (hand-written, drizzle-kit
  source-of-truth for the five dashboard-owned tables: better-auth
  core + `operator_invites`).
- better-auth 1.6.9 with the drizzle adapter, `themePreference`
  additionalField, email/password as the only provider. First-run
  `/setup` wizard returns 404 once any user exists; thereafter
  accounts are created only via `/api/invites/redeem`. Atomic
  invite redemption (`UPDATE … WHERE redeemed_at IS NULL …
  RETURNING id`, MVCC-serialised) — proven by
  `lib/auth/invites.test.ts`.
- Singleton SSE listener (`lib/sse/listener.ts`) with a 5-state
  machine (dormant/connecting/live/backoff/idle-grace), 100ms→30s
  exponential backoff, 60s idle-grace before closing. Subscribes
  to `KNOWN_CHANNELS = ['work.ticket.created']` plus a
  `work.ticket.transitioned.<dept>.<from>.<to>` pattern, with an
  `event_outbox` poll fallback for events that aren't pg_notify'd.
- next-intl 4.9.1 with `localePrefix: 'as-needed'`. English shipped;
  the i18n machinery accepts a stub `zz` locale in test mode for
  exercising the locale-swap path.
- `dashboard/Dockerfile` two-stage build (oven/bun:1-alpine →
  node:22-alpine standalone). Final image: 217 MB.
  `supervisor/docker-compose.yml` gains a fourth service.
- `docs/ops-checklist.md` M3 section: goose ordering, role
  passwords + LOGIN, `BETTER_AUTH_SECRET` generation, Drizzle
  migration application, image digest pinning, first-run
  walkthrough.
- Golden-path Playwright spec covering the full operator journey
  (first-run → invite → second operator → all read surfaces).
  Acceptance evidence at `specs/008-m3-dashboard/acceptance-evidence.md`.

### Surfaced (NOT fixed in M3)

- **Cost-telemetry blind spot** — `agent_instances.total_cost_usd`
  reads $0.00 on clean finalizes. Now visible in ticket detail with
  an explicit caveat icon. Fix is the supervisor signal-handling
  change tracked at `docs/issues/cost-telemetry-blind-spot.md`.
- **Workspace-sandbox-escape** — "artifact claimed vs artifact on
  disk" appears as a hygiene-status row in the dashboard's hygiene
  table. Fix lands as Docker-per-agent post-M3, tracked at
  `docs/issues/agent-workspace-sandboxing.md`.

### Resolved

- **`activityCatchup` cursor must be a string, not `new Date()`** —
  drizzle's `appDb.execute(sql\`…\`)` passes params straight through
  to postgres-js, which can't serialize JS Date objects to wire
  format and throws `ERR_INVALID_ARG_TYPE`. Caught by the integration
  test added during the post-merge SonarCloud quality pass; production
  catch-up flow would have thrown the same error on any reconnect
  with a Last-Event-ID header. Fix: pass the ISO string directly
  with an explicit `::timestamptz` cast.
- **`<html data-theme=…>` hoisting** — first attempt placed it inside
  `[locale]/layout.tsx`; Next.js's hoisting flattened it. Moved to
  the root `app/layout.tsx`; `[locale]` layer wraps children in
  `NextIntlClientProvider` only.
- **Drizzle `inet` rejects `""`** — better-auth passes empty-string
  IPs in test contexts. Switched `sessions.ip_address` to `text` to
  match better-auth's reference schema.
- **Drizzle `uuid` rejects nanoid `id`s from better-auth** — fixed
  with `advanced.database.generateId: false` so the Postgres default
  `gen_random_uuid()` takes over.
- **Drizzle `timestamp` mode `'string'` vs better-auth Date objects**
  — switched all dashboard timestamps to `mode: 'date'`.

### Quality gate

- SonarCloud quality gate green on PR #7: 0 open issues (down from
  71), 0 bugs / 0 hotspots TO_REVIEW, new-code coverage 81.5 %
  (above the 80 % gate). Coverage scope tightened to exclude
  `dashboard/components/**` + `dashboard/app/**` +
  `dashboard/lib/queries/vault.ts` + `dashboard/lib/i18n/request.ts`
  (behaviourally covered by the Playwright integration suite); they
  remain in scope for bugs / hotspots / duplication. Dashboard CI
  added to `.github/workflows/ci.yml` (typecheck, test+coverage,
  production-build); SonarCloud reads dashboard lcov via
  `sonar.javascript.lcov.reportPaths` /
  `sonar.typescript.lcov.reportPaths`.

### Dependencies

13 direct + ~150 transitive new dashboard-side TS/JS deps. The
supervisor's locked-deps streak is supervisor-scoped (Go); the
dashboard side adopts the same justification discipline. Each
dep is justified in `docs/retros/m3.md` §"Dependencies added
outside the locked list". Headline additions: `next 16.2.4`,
`react 19.2.5`, `tailwindcss 4.2.4`, `drizzle-orm 0.45.2`,
`postgres 3.4.9`, `better-auth 1.6.9`, `next-intl 4.9.1`,
`@tanstack/react-query 5.100.5`, `@tanstack/react-virtual 3.13.24`,
`vitest 4.1.5`, `playwright 1.59.1`, `testcontainers 11.14.0`.

### Deferred to M4+

- Drizzle CLI in the production runtime image (currently
  ops-shell only, per CL-004).
- Pattern-category extraction for `suspected_secret_emitted`
  (M3 shows a generic "secret-shape" label only; M4 gains the
  schema-mutation rights to thread the M2.3 scanner's pattern
  label set through).
- Activity-feed run grouping for cross-instance flows (today
  keyed by `agent_instance_id`; M5's CEO chat will produce events
  spanning multiple instances).
- Migrating the test harness from `next start` to the standalone
  runtime for closer production parity.

### MemPalace mirror

First non-historical-exception milestone for the dual-deliverable
retro policy. Retro filed both as `docs/retros/m3.md` and as
MemPalace drawer `drawer_garrison_retros_bc3751a24c0070171cb3188b`,
with five KG triples linking M3 to two-role-DB-isolation,
cost-telemetry-blind-spot, workspace-sandbox-escape, the
Drizzle/goose migration split, and the operator-invite atomic
redemption pattern.

---

## [M1] — 2026-04-22

The first shipped milestone. Event bus and supervisor core. Fake
agent via `sh -c`; no real Claude invocation yet.

### Added

- `supervisor` Go binary. Go 1.25, `CGO_ENABLED=0`, ~18 MB static.
  Subcommands: `--version`, `--migrate`, default run mode.
- Postgres 17 schema under `migrations/` (2 goose migrations):
  `departments`, `tickets`, `event_outbox`, `agent_instances`,
  partial indexes for the concurrency-cap query and the
  fallback-poll query, the `ticket_created_emit` trigger, and the
  `emit_ticket_created` function.
- sqlc-generated typed query layer in `supervisor/internal/store/`.
- `internal/pgdb` — `pgxpool` + dedicated LISTEN `*pgx.Conn`, the
  FR-017 100 ms → 30 s backoff dialer, and `AcquireAdvisoryLock`
  wrapping the fixed FR-018 key.
- `internal/events` — handler registry (`Dispatcher`), LISTEN loop,
  fallback poll, reconnect state machine.
- `internal/spawn` — per-event dedupe transaction, concurrency-cap
  check, `InsertRunningInstance`, `exec.CommandContext` with
  per-stream line-scanning goroutines, NFR-005 SIGTERM → 5 s → SIGKILL
  shutdown, single terminal transaction.
- `internal/recovery` — one-shot reconcile of stale `running` rows
  on startup (NFR-006 5-minute window).
- `internal/concurrency` — two-query cap check; cap = 0 is the
  FR-003 pause signal.
- `internal/health` — `/health` HTTP server returning 200 iff a
  500 ms `SELECT 1` succeeds and `time.Since(LastPollAt) ≤ 2·PollInterval`.
- `cmd/supervisor` — `errgroup.WithContext`, SIGTERM/SIGINT/SIGHUP
  handling, exit codes per contract.
- Tests — ~35 unit tests, an `integration`-tagged suite covering
  US1–US3 and the 100-ticket volume path, a `chaos`-tagged suite
  for reconnect, external SIGKILL, and graceful-shutdown-with-inflight.
- Runtime Docker image at `garrison/supervisor:dev`, built from
  `alpine:3.20` + `ca-certificates` (24 MB).
- Acceptance evidence doc documenting all 10 acceptance steps
  against `make docker` + a fresh `postgres:17`.

### Changed

- Dockerfile runtime base switched from
  `gcr.io/distroless/static-debian12` to `alpine:3.20` during T017
  acceptance. Distroless has no `/bin/sh`; the M1 fake-agent command
  is `sh -c "..."` and cannot exec without a shell. Trade-off: +7 MB.
  See [`docs/retros/m1.md`](./docs/retros/m1.md) §5.

### Resolved

- **LISTEN/poll in-flight race.** `sync.Map` on `Dispatcher`, keyed
  by event ID, gates a second poll-driven spawn while the first
  handler is still running. See retro §1.
- **Terminal-tx write cancelled on graceful shutdown.** Introduced
  `TerminalWriteGrace = 5s` with `context.WithTimeout(context.WithoutCancel(ctx), ...)`.
  See retro §2.
- **Reconnect-path nil-conn deref.** Inner `for conn == nil { ... }`
  dial-retry loop in `internal/events/reconnect.go`. See retro §3.

### Known issues (non-blocking)

- `agent_instances.pid` is never written. `InsertRunningInstance`
  runs before `cmd.Start()` so the pid isn't known at insert time;
  `UpdateInstanceTerminal` doesn't `SET pid`. Observability is fine
  (pid is emitted per subprocess log line); DB column is dead.
  Deferred to M2. See retro §4.
- Acceptance step 9 is under-specified after a clean shutdown.
  T017 interpreted the step as "demonstrate recovery works" by
  injecting a synthetic 10-min-stale row via direct SQL. The
  acceptance doc should be amended in M2 prep. See retro §6.

### Dependencies

Exactly one direct dependency added outside the locked list in
`AGENTS.md` §"Supervisor (Go) — Locked dependency list":

- `github.com/google/shlex` — POSIX-like argv splitter for
  `GARRISON_FAKE_AGENT_CMD`. Justified in the T008 commit; flagged
  in the retro. Single-file, stdlib-only, no transitive deps.

### Deferred to M2+

- Per-agent-type concurrency sub-caps.
- `/metrics` endpoint.
- `agent_instances.pid` backfill.
- Blue/green and rolling deploys (disallowed in M1 by FR-018's
  advisory-lock serialization).
- Operator-tunable fallback-poll batch size.
- Real `claude` CLI integration in the lifecycle manager.

### Open questions resolved

- **`github.com/google/shlex` — accept or implement in-tree?** →
  Accepted. See retro §"Plan-level open questions — resolved".
- **SIGHUP semantics — separate handling or treat as SIGTERM?** →
  Treated as SIGTERM. Single `signal.Notify` registers all three.

---

[Unreleased]: https://github.com/garrison-hq/garrison/compare/m3...HEAD
[M3]: https://github.com/garrison-hq/garrison/releases/tag/m3
[M1]: https://github.com/garrison-hq/garrison/releases/tag/m1
