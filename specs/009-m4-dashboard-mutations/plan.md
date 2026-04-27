# Implementation plan: M4 — operator dashboard mutations

**Branch**: `009-m4-dashboard-mutations` | **Date**: 2026-04-27 | **Spec**: [spec.md](./spec.md)
**Input**: [spec.md](./spec.md), [m4-context.md](../_context/m4-context.md), [vault-threat-model.md](../../docs/security/vault-threat-model.md) (post-amendment), AGENTS.md, RATIONALE.md, M3 retro, M2.3 retro, existing supervisor + dashboard codebases.

---

## Summary

M4 turns Garrison's operator dashboard from observational console into operational console. Three mutation surfaces ship together (vault writes, ticket mutations, agent settings editor), backed by a unified audit / activity-feed extension and a small set of cross-cutting primitives (mutation confirm dialog, diff view, conflict-resolution UI for optimistic locking, secret reveal modal). Two supervisor-side changes fold in: an `internal/agents` cache invalidator listening on a new `agents.changed` channel (FR-100), and a `scanAndRedactPayload` extension that writes the matched suspected-secret pattern label to a new `ticket_transitions` column (FR-115). Three M3-retro carryovers land alongside (Drizzle CLI stays out of runtime, test harness migrates to standalone, pattern-category surfacing in hygiene table).

The plan extends the existing repo: `dashboard/` gains new routes, components, server actions, and TS-side leak-scan parity; `supervisor/internal/agents` and `supervisor/internal/finalize` gain narrowly-scoped extensions; one cross-boundary goose migration adds the new `ticket_transitions.suspected_secret_pattern_category` column and the `hygiene_status` enum value `operator_initiated`; one dashboard-domain Drizzle migration adds the `secret_metadata.rotation_provider` column, the `vault_access_log` `outcome` enum extension, and the `vault_access_log.metadata` JSONB column. The parked `garrison-dashboard` Machine Identity from M2.3 retro Q4 activates here.

---

## Technical Context

**Language/Version**: TypeScript 5.6+ (dashboard); Go 1.23 (supervisor extensions); SQL (migrations).
**Primary Dependencies (dashboard)**: Next.js 16, React 19, Drizzle ORM, better-auth, next-intl, TanStack Query, Tailwind v4, postgres-js, `@infisical/sdk` (NEW direct dep; see §Constitution Check + Phase 0 research item 5), Vitest, Playwright. Target zero net new direct deps; the Infisical TS SDK is the one principled exception for vault writes through the parked `garrison-dashboard` ML.
**Primary Dependencies (supervisor)**: existing locked list. M4 adds nothing — both supervisor-side extensions reuse `pgx`, `slog`, `errgroup`, the existing `internal/finalize` scanner, and the existing `internal/agents` cache.
**Storage**: Postgres (shared). Two new dashboard-scoped roles continue from M3 (`garrison_dashboard_app` for app reads + most writes; `garrison_dashboard_ro` for vault reads). M4 introduces no new Postgres roles. Vault writes route through the dashboard's `garrison-dashboard` Infisical Machine Identity (new at runtime, parked in Infisical since M2.3).
**Testing**: Vitest (colocated unit) + Playwright (integration against testcontainer Postgres + testcontainer Infisical + built dashboard standalone bundle). Supervisor extensions: existing `go test ./...` covers them.
**Target Platform**: Linux container (dashboard). Single static binary (supervisor). No platform changes from M3 + M2.3.
**Project Type**: Web application — dashboard owns its own backend via Server Actions + route handlers; supervisor extensions are additive in-place.
**Performance Goals**: Mutation flows complete and surface in the activity feed within 2 seconds of commit (real-time via `pg_notify`) and within 30 seconds (poll fallback). Vault create / edit / rotate p95 ≤ 500ms against a localhost Infisical. Agent settings save propagates to the next spawn ≤ 100ms after `pg_notify` round-trip.
**Constraints**: Image size cap continues at ≤250MB (M3 hit 217MB; M4 must not regress past the cap). Two-role DB isolation continues. i18n: zero English-string JSX literals outside the catalog. Responsive ≥768px continues. Threat model rules 1-7 bind throughout; the vaultlog-equivalent TS discipline is added at CI time (lib/vault-discipline-check or grep + ESLint).
**Scale/Scope**: 1-2 operators concurrent (M3 baseline); operator-mutation cadence is bounded by human keystrokes (small). Vault dataset typically <100 secrets at solo-operator scale; rotation cadence weekly per RATIONALE §5.

---

## Constitution Check

Garrison constitution (`.specify/memory/constitution.md`) gates:

- **Principle I (Postgres + pg_notify)**: M4 honors it. Mutation events land in `event_outbox` (existing supervisor-domain table; dashboard becomes a second writer per FR-011) or extend `vault_access_log` per FR-012. New `agents.changed` channel is a `pg_notify` channel emitted from inside the same SQL transaction as the `agents` row write. No parallel state stores.
- **Principle II (MemPalace as memory layer)**: M4 does not write to MemPalace. The supervisor's existing finalize-path MemPalace writes continue unchanged.
- **Principle III (ephemeral agents)**: unchanged. The `agents.changed` cache invalidator preserves the spawn-time-fresh-read semantics for agents whose config is edited.
- **Principle IV (soft gates)**: M4 introduces no hard gates on mutations. The leak-scan rejection at agent.md save (FR-088) is the one save-time block, and it's defending against a Rule 1 threat model violation, not against ergonomic friction. Operator-initiated transitions are not flagged as failures (FR-030 excludes them from triage metrics by default).
- **Principle V (skills.sh)**: no skill-library invention. Agent settings editor edits the `skills` slug array against `.claude/skills/` directory listing; M7 still owns the registry browser.
- **Principle VI (UI-driven hiring)**: M4 stays on the editing-existing-agents side; hiring (creating new agents) stays at M7.
- **Principle VII (locked deps)**: dashboard-side. M4 targets zero net new direct deps. The `@infisical/sdk` TS SDK is the one principled addition — the alternative (raw HTTP against Infisical's REST API) reproduces the supervisor M2.3 decision in TS without the SDK's ML auth refresh loop and rotation API surface; rejected for the same reasons. Justified in retro per AGENTS.md soft-rule.
- **Principle VIII (every goroutine has a context)**: the new supervisor LISTEN goroutine for `agents.changed` accepts the supervisor's root context, exits on cancellation, drains the LISTEN connection cleanly. No bare `go func()`.
- **Principle IX (specs narrow per milestone)**: M4 ships exactly the three mutation surfaces + three carryovers + two supervisor-side extensions. Out-of-scope items per spec §Assumptions remain out.
- **Principle X (per-department concurrency caps)**: agent settings editor edits the cap. The supervisor's enforcement code is unchanged; it reads the new value at next spawn after cache invalidation.
- **Principle XI (self-hosted)**: no cloud dependencies added. Infisical continues to run inside the Coolify project.

**Concurrency discipline §rule 1** (no bare `go func()`): the new supervisor `internal/agents` LISTEN goroutine inherits the existing pattern from `internal/events`. **Rule 7** (process group): not applicable (no new subprocess spawn). **Rule 8** (subprocess pipe drain): not applicable.

**Scope discipline (AGENTS.md §)**: M4 stays within spec. No CEO chat, no hiring, no skill library, no MCP registry, no fixes for cost-telemetry or sandbox-escape (just continue M3's surfacing).

**No mutation of sealed M2-arc surfaces**: the supervisor extensions are additive — `internal/agents` gains a cache invalidator without changing existing cache semantics for the no-edits case; `scanAndRedactPayload` gains a column write without changing detection logic. The four M2.3 vault rules + the `tools/vaultlog` analyzer continue to bind unchanged. The threat-model amendment (commit `11b0dfb`) updated the milestone banding only; rules 1-7 substance is unchanged.

**Constitutional violations to track**: zero. The Infisical TS SDK addition follows the same justified-addition pattern M2.3 set; documented in §Phase 0 research item 5 and in the M4 retro per FR-130 / SC-012.

---

## Project Structure

### Documentation (this feature)

```text
specs/009-m4-dashboard-mutations/
├── plan.md              ← this file (Phase 0 + 1 outputs inline below)
├── spec.md              ← approved + clarified (commit 093e867)
└── tasks.md             ← Phase 2 output, produced by /garrison-tasks
```

Phase 0 (research) and Phase 1 (data model + contracts + quickstart) outputs are kept inline per M3's pattern. M4's scope is comparable to M3's; the inline shape matches.

### Source code

```text
dashboard/
├── app/
│   ├── (app)/
│   │   ├── tickets/
│   │   │   ├── new/page.tsx                        ← NEW: create-ticket form (US2)
│   │   │   └── [id]/page.tsx                       ← EXTENDED: inline edits + drag-aware
│   │   ├── departments/
│   │   │   └── [slug]/page.tsx                     ← EXTENDED: drag-to-move (US2)
│   │   ├── agents/
│   │   │   ├── page.tsx                            ← EXTENDED: edit affordances per row (US3)
│   │   │   └── [slug]/edit/page.tsx                ← NEW: agent settings editor (US3)
│   │   ├── vault/
│   │   │   ├── page.tsx                            ← EXTENDED: create / edit / delete affordances (US1)
│   │   │   ├── new/page.tsx                        ← NEW: secret create form
│   │   │   ├── [path*]/edit/page.tsx               ← NEW: secret edit form (path is multi-segment)
│   │   │   ├── audit/page.tsx                      ← EXTENDED: extended outcome enum filtering
│   │   │   ├── matrix/page.tsx                     ← EXTENDED: grant add/remove (US1)
│   │   │   ├── rotation/page.tsx                   ← NEW: dedicated rotation dashboard (FR-065, FR-066)
│   │   │   └── tree/page.tsx                       ← NEW: env / path tree management (FR-067-069)
│   │   ├── hygiene/page.tsx                        ← EXTENDED: pattern-category column + filter (US4)
│   │   └── @panel/                                 ← EXTENDED: edit-aware ticket detail panel
│   ├── api/
│   │   └── (existing routes from M3 unchanged)
│   └── globals.css                                 ← EXTENDED: mutation-primitive animations (modal, conflict UI)
├── components/
│   ├── ui/
│   │   ├── ConfirmDialog.tsx                       ← NEW: typed-name confirm + single-click confirm tiers
│   │   ├── DiffView.tsx                            ← NEW: secret-edit diff (no values shown)
│   │   ├── ConflictResolutionModal.tsx             ← NEW: optimistic-lock conflict UI
│   │   ├── RevealModal.tsx                         ← NEW: secret reveal with auto-hide
│   │   ├── PathTreeView.tsx                        ← NEW: vault path tree visualization
│   │   └── (M3 primitives unchanged)
│   └── features/
│       ├── ticket-create/                          ← NEW (US2)
│       ├── ticket-inline-edit/                     ← NEW (US2)
│       ├── kanban-drag/                            ← NEW (US2)
│       ├── vault-secret-form/                      ← NEW (US1)
│       ├── vault-grant-edit/                       ← NEW (US1)
│       ├── vault-rotation/                         ← NEW (US1)
│       ├── vault-path-tree/                        ← NEW (US1)
│       ├── vault-reveal/                           ← NEW (US1)
│       └── agent-settings-edit/                    ← NEW (US3)
├── lib/
│   ├── actions/                                    ← NEW: Server Actions home (split by domain)
│   │   ├── tickets.ts                              ← createTicket, moveTicket, editTicket
│   │   ├── vault.ts                                ← createSecret, editSecret, deleteSecret, addGrant, removeGrant, initiateRotation, completeRotation, revealSecret, movePath, renamePath
│   │   └── agents.ts                               ← editAgent (concurrent-edit aware)
│   ├── vault/                                      ← NEW: dashboard-side vault primitives
│   │   ├── infisicalClient.ts                      ← bound to garrison-dashboard ML credentials
│   │   ├── leakScan.ts                             ← TS port of internal/vault/scan.go patterns (FR-088)
│   │   └── outcomes.ts                             ← extended outcome enum + helpers
│   ├── audit/                                      ← NEW: mutation event audit primitives
│   │   ├── eventOutbox.ts                          ← write helper for ticket/agent mutation events
│   │   ├── vaultAccessLog.ts                       ← write helper for vault mutation events
│   │   ├── pgNotify.ts                             ← typed pg_notify emission inside transactions
│   │   └── diff.ts                                 ← field-level before/after diff builder
│   ├── locks/                                      ← NEW: optimistic locking helpers
│   │   ├── version.ts                              ← updated_at-based conflict detection
│   │   └── conflict.ts                             ← AuthError-style ConflictError with typed kind
│   ├── queries/
│   │   ├── (existing M3 read queries unchanged)
│   │   └── …                                       ← may grow to support new write surfaces' reads
│   └── (M3 lib structure unchanged otherwise)
├── tests/
│   ├── integration/
│   │   ├── (M3 specs unchanged — extended where M4 adds surfaces)
│   │   ├── vault-create-edit-delete.spec.ts        ← NEW (US1)
│   │   ├── vault-grant-edit.spec.ts                ← NEW (US1)
│   │   ├── vault-rotation.spec.ts                  ← NEW (US1)
│   │   ├── vault-reveal.spec.ts                    ← NEW (US1)
│   │   ├── vault-path-tree.spec.ts                 ← NEW (US1)
│   │   ├── ticket-create.spec.ts                   ← NEW (US2)
│   │   ├── ticket-drag.spec.ts                     ← NEW (US2)
│   │   ├── ticket-inline-edit.spec.ts              ← NEW (US2)
│   │   ├── agent-settings-edit.spec.ts             ← NEW (US3)
│   │   ├── agent-leak-scan-rejection.spec.ts       ← NEW (US3, FR-088)
│   │   ├── pattern-category-display.spec.ts        ← NEW (US4)
│   │   ├── concurrent-edit-conflict.spec.ts        ← NEW (FR-101)
│   │   ├── multi-step-rotation-failure.spec.ts     ← NEW (FR-094)
│   │   └── session-expiry-mid-mutation.spec.ts     ← NEW (FR-005)
│   └── (Vitest unit tests colocated next to their source files)
├── drizzle/
│   ├── schema.dashboard.ts                         ← UNCHANGED for the dashboard-domain part; auth/invites stable
│   ├── schema.supervisor.ts                        ← REGENERATED via drizzle:pull after the goose migration lands
│   └── migrations/                                 ← drizzle-kit generate output extending the M3 base
└── (Dockerfile / config / etc. unchanged except for next standalone harness migration FR-122)

migrations/                                         ← existing goose-managed dir
└── 20260427000010_m4_supervisor_schema_extensions.sql   ← NEW — single cross-boundary migration:
                                                      ─ ticket_transitions.suspected_secret_pattern_category
                                                      ─ hygiene_status enum: add 'operator_initiated'
                                                      ─ secret_metadata.rotation_provider
                                                      ─ vault_access_log.outcome enum extension (9 new values)
                                                      ─ vault_access_log.metadata JSONB

supervisor/
├── internal/
│   ├── agents/
│   │   └── cache.go                                ← EXTENDED: LISTEN agents.changed + Reset(roleSlug)
│   └── finalize/
│       └── scanAndRedactPayload                    ← EXTENDED: write pattern label to ticket_transitions
└── docker-compose.yml                              ← AMENDED: dashboard env vars for garrison-dashboard ML

docs/ops-checklist.md                               ← AMENDED: M4 section (ML credentials, rotation_provider seed)
docs/retros/m4.md                                   ← Phase 3 deliverable, written at retro time
```

**Structure decision**: Web-app layout continues from M3. M4 is purely additive on the dashboard side; the supervisor extensions are narrowly-scoped in-place edits to two existing packages. One cross-boundary goose migration carries the schema additions (column adds + enum extensions) to keep the supervisor and dashboard schema in sync; Drizzle re-introspects after the goose migration lands. No new top-level directories.

---

## Phase 0 — research outputs (inline)

The following research items must be confirmed before implementation. Each is uncertain enough that the plan correctness depends on the answer; everything else is documented surface.

1. **Server actions calling Infisical SDK at request time**. Server actions in Next.js 16 run server-side per request. The `@infisical/sdk` Node SDK requires ML credential bootstrap at process start (not per request) and caches access tokens with eager refresh. Confirm: a singleton `infisicalClient` constructed at module-init time and shared across server-action invocations works correctly; per-request credential isolation is not required (single ML serves all operators).

2. **`pg_notify` from inside a Drizzle / postgres-js transaction is atomic with the SQL writes**. The activity-feed real-time guarantee (FR-015 / SC-005) requires that mutation events become visible to LISTEN subscribers in the same atomic moment as the row write. Confirm: a `notify` issued via `sql\`SELECT pg_notify(...)\`` inside a `db.transaction(...)` block fires exactly when the transaction commits, not before. Postgres docs are clear; verify in TS test before relying on it.

3. **Optimistic locking in Drizzle**. FR-101 / FR-084 use `updated_at` (or equivalent versioning column) for conflict detection on agent.md and secret edits. Confirm: a `WHERE updated_at = $1 ... RETURNING *` pattern in Drizzle correctly returns zero rows on stale-version writes (not an exception). The fallback is a hand-written SQL conflict check via `db.execute`; both work.

4. **`event_outbox` writes from a second process**. The `event_outbox` table is supervisor-domain (M3 reads it; the supervisor writes finalize / lifecycle events to it). Adding the dashboard as a second writer requires checking: (a) PK / sequence strategy supports concurrent writers; (b) the supervisor's read-side LISTEN catches dashboard-emitted notifications; (c) any existing supervisor invariants around `event_outbox` ordering are preserved. Outcome resolves whether `event_outbox` is the right home for ticket / agent mutations or whether a sibling `dashboard_event_outbox` is safer.

5. **`@infisical/sdk` TS SDK shape and ML auth refresh**. The supervisor uses `infisical/go-sdk v0.7.1`; the dashboard needs the TS equivalent. Confirm: (a) the TS SDK supports Universal Auth with ML credentials; (b) automatic auth refresh on 401 (matches the M2.3 supervisor behavior so write paths don't need hand-rolled retry); (c) rotation API surface for supported backends (Postgres, MySQL, AWS IAM per threat model §5); (d) license / size footprint reasonable for a justified addition. Outcome resolves the one new direct dep + the rotation flow shape.

6. **Drizzle migration ordering with the goose cross-boundary migration**. The supervisor schema changes (column adds, enum extensions) land via goose. Drizzle introspects after. The dashboard's own Drizzle migrations (any new tables or audit-table extensions) need a clear ordering: goose first (supervisor schema), drizzle:pull (regenerate `schema.supervisor.ts`), then drizzle:migrate (apply dashboard-domain migrations). Confirm: `docs/ops-checklist.md` M4 section captures this ordering as a ramp-up step.

7. **TS-side `vaultlog`-equivalent CI check**. The supervisor's `tools/vaultlog` analyzer rejects slog/fmt/log calls with `vault.SecretValue` arguments. The dashboard needs a TS-side discipline (FR-017). Confirm: an ESLint rule (using `eslint-plugin-functional` or a custom rule) plus a CI grep step that rejects `console.log(secret*)` and similar patterns is sufficient. Falls back to documented code-review discipline if the lint rule proves brittle.

These seven items resolve during Phase 0; if any answer differs from the plan's default, the plan is updated before tasks land.

---

## Phase 1 — design outputs

### Data model (inline)

#### Goose-owned migration: `20260427000010_m4_supervisor_schema_extensions.sql`

Single migration carrying all cross-boundary schema changes. Atomic apply / rollback.

```sql
-- Up

-- 1. New ticket_transitions column for suspected-secret pattern label (FR-115)
ALTER TABLE ticket_transitions
  ADD COLUMN suspected_secret_pattern_category text;

CREATE INDEX ticket_transitions_pattern_category_idx
  ON ticket_transitions(suspected_secret_pattern_category)
  WHERE suspected_secret_pattern_category IS NOT NULL;

-- Optional CHECK constraint enumerating the 10 known categories + 'unknown';
-- soft-enforced because the supervisor scanner is the source of truth.
ALTER TABLE ticket_transitions
  ADD CONSTRAINT ticket_transitions_pattern_category_check
  CHECK (suspected_secret_pattern_category IS NULL
         OR suspected_secret_pattern_category IN (
           'sk-prefix','xoxb','aws-akia','pem-header',
           'github-pat','github-app','github-user','github-server','github-refresh',
           'bearer-shape','unknown'));

-- 2. hygiene_status enum extension: 'operator_initiated' (FR-027)
ALTER TYPE hygiene_status ADD VALUE 'operator_initiated';

-- 3. secret_metadata.rotation_provider (FR-072)
ALTER TABLE secret_metadata
  ADD COLUMN rotation_provider text NOT NULL DEFAULT 'manual_paste'
  CHECK (rotation_provider IN ('infisical_native','manual_paste','not_rotatable'));

-- Backfill: existing rows default to manual_paste; the operator can re-classify
-- via the M4 vault-edit UI. Path-prefix-based reclassification is opt-in and not
-- automated here.

-- 4. vault_access_log.outcome enum extension (FR-012)
ALTER TYPE vault_outcome ADD VALUE 'secret_created';
ALTER TYPE vault_outcome ADD VALUE 'secret_edited';
ALTER TYPE vault_outcome ADD VALUE 'secret_deleted';
ALTER TYPE vault_outcome ADD VALUE 'grant_added';
ALTER TYPE vault_outcome ADD VALUE 'grant_removed';
ALTER TYPE vault_outcome ADD VALUE 'rotation_initiated';
ALTER TYPE vault_outcome ADD VALUE 'rotation_completed';
ALTER TYPE vault_outcome ADD VALUE 'rotation_failed';
ALTER TYPE vault_outcome ADD VALUE 'value_revealed';

-- 5. vault_access_log.metadata JSONB (FR-013)
-- Carries write-specific context (which fields changed, target path, rotation
-- step that failed, etc.). Rule 6 invariant continues: NEVER carries secret
-- values. The TS-side leak-scan discipline (FR-017) enforces.
ALTER TABLE vault_access_log
  ADD COLUMN metadata jsonb;

-- Down: REVERSE the above. Note: Postgres ALTER TYPE ... ADD VALUE is not
-- reversible without a CREATE TYPE + UPDATE + DROP TYPE cycle; the down
-- migration captures this carefully (see migration body comments).
```

**Notes**:

- Pattern-category constraint is a `CHECK` rather than a separate enum because the supervisor scanner can emit `'unknown'` for future patterns without a new migration. Future scanner extensions add to the CHECK, not to a strict enum.
- `hygiene_status` is extended (not replaced); existing values stay valid.
- `vault_outcome` extension uses `ADD VALUE`; rollback requires the careful CREATE TYPE pattern documented in the migration body.
- `secret_metadata.rotation_provider` defaults to `manual_paste` for existing rows — the safe choice if the operator hasn't re-classified.
- `metadata jsonb` is nullable; only mutation rows populate it.

#### Drizzle re-introspection

After goose applies the migration, run `bun run drizzle:pull` to regenerate `dashboard/drizzle/schema.supervisor.ts`. The hand-written `schema.dashboard.ts` is unchanged in M4 (the auth/invites tables are stable). New `*.ts` typing for `vaultOutcome` and `hygieneStatus` enums lifts automatically from the introspection.

#### Drizzle-owned migrations

M4 introduces no new dashboard-owned tables. The auth + invites schema continues from M3 unchanged. Optional new dashboard-side audit columns (e.g., `users.last_mutation_at`) are not in scope.

### Contracts (inline)

Three contract surfaces, all formalized as TypeScript types:

#### 1. Server action signatures (`lib/actions/*.ts`)

```ts
// lib/actions/tickets.ts
export async function createTicket(params: {
  title: string;
  description: string;
  priority: TicketPriority;
  deptSlug: string;
  targetColumn: string;
  assignedAgentRoleSlug?: string;
}): Promise<{ ticketId: string }>;

export async function moveTicket(params: {
  ticketId: string;
  fromColumn: string;
  toColumn: string;
}): Promise<{ transitionId: string }>;

export async function editTicket(params: {
  ticketId: string;
  changes: Partial<{ title: string; description: string; priority: TicketPriority; assignedAgentRoleSlug: string | null }>;
  versionToken: string;  // updated_at snapshot for last-write-wins audit reconstruction
}): Promise<{ accepted: true; ticketId: string }>;

// lib/actions/vault.ts
export async function createSecret(params: {
  name: string;
  value: string;
  path: string;
  provenance: SecretProvenance;
  rotationCadence: string;
  rotationProvider: RotationProvider;
  customerId?: string;
}): Promise<{ secretPath: string }>;

export async function editSecret(params: {
  secretPath: string;
  versionToken: string;
  changes: Partial<{ name: string; value: string; rotationCadence: string }>;
}): Promise<{ accepted: true } | { conflict: true; serverVersion: VaultSecretSnapshot }>;

export async function deleteSecret(params: {
  secretPath: string;
  confirmationName: string;  // typed-name confirm
}): Promise<void>;

export async function addGrant(params: {
  roleSlug: string;
  envVarName: string;
  secretPath: string;
}): Promise<{ grantId: string }>;

export async function removeGrant(params: {
  grantId: string;
}): Promise<void>;

export async function initiateRotation(params: {
  secretPath: string;
  newValue?: string;  // required when rotation_provider = 'manual_paste'
}): Promise<{ rotationId: string; status: 'initiated' | 'completed' | 'failed' }>;

export async function revealSecret(params: {
  secretPath: string;
}): Promise<{ value: string; revealedAt: string }>;

export async function movePath(params: {
  fromPath: string;
  toPath: string;
  affectedSecrets: string[];
  confirmationName: string;  // typed-name confirm for multi-secret moves
}): Promise<{ moved: number }>;

export async function renamePath(params: {
  oldPath: string;
  newPath: string;
}): Promise<{ renamed: number }>;

// lib/actions/agents.ts
export async function editAgent(params: {
  roleSlug: string;
  versionToken: string;
  changes: Partial<{ agentMd: string; model: ModelTier; concurrencyCap: number; listensFor: string[]; skills: string[] }>;
}): Promise<{ accepted: true } | { conflict: true; serverVersion: AgentSnapshot }>;
```

Every server action emits the corresponding mutation event row inside the same SQL transaction as the data write. `pg_notify` fires on the appropriate channel before transaction commit.

#### 2. Mutation event types (`lib/audit/events.ts`)

```ts
export type MutationEvent =
  | { kind: 'ticket.created'; ticketId: string; deptSlug: string; targetColumn: string; ts: string }
  | { kind: 'ticket.moved'; ticketId: string; fromColumn: string; toColumn: string; transitionId: string; origin: 'agent' | 'operator'; ts: string }
  | { kind: 'ticket.edited'; ticketId: string; diff: Record<string, { before: unknown; after: unknown }>; ts: string }
  | { kind: 'agent.edited'; roleSlug: string; diff: Record<string, { before: unknown; after: unknown }>; ts: string }
  | { kind: 'vault.secret_created'; secretPath: string; provenance: SecretProvenance; ts: string }
  | { kind: 'vault.secret_edited'; secretPath: string; changedFields: string[]; ts: string }
  | { kind: 'vault.secret_deleted'; secretPath: string; affectedRoles: string[]; ts: string }
  | { kind: 'vault.grant_added'; grantId: string; roleSlug: string; envVarName: string; secretPath: string; ts: string }
  | { kind: 'vault.grant_removed'; grantId: string; roleSlug: string; envVarName: string; secretPath: string; ts: string }
  | { kind: 'vault.rotation_initiated'; secretPath: string; rotationId: string; ts: string }
  | { kind: 'vault.rotation_completed'; secretPath: string; rotationId: string; ts: string }
  | { kind: 'vault.rotation_failed'; secretPath: string; rotationId: string; failedStep: 'generate' | 'write' | 'revoke' | 'audit'; ts: string }
  | { kind: 'vault.value_revealed'; secretPath: string; ts: string };
```

Mutation event payloads NEVER include secret values. The TS-side leak-scan CI check (FR-017) verifies that no `MutationEvent` constructor receives a `value` field outside the explicit reveal modal's transient render.

The activity feed extends M3's `ActivityEvent` discriminated union with these new variants; the merge in `lib/queries/activityCatchup.ts` and the SSE listener fan-out in `lib/sse/listener.ts` handle both event sources.

#### 3. Conflict / error vocabulary (`lib/locks/conflict.ts` + `lib/vault/outcomes.ts`)

```ts
export const ConflictKind = {
  StaleVersion: 'stale_version',          // optimistic lock failed
  AlreadyExists: 'already_exists',        // create with conflicting key
  InUse: 'in_use',                        // delete blocked by FK or grant ref
  RotationStepFailed: 'rotation_step_failed',
  PathCollision: 'path_collision',
} as const;
export type ConflictKind = typeof ConflictKind[keyof typeof ConflictKind];

export class ConflictError extends Error {
  constructor(public kind: ConflictKind, public serverState?: unknown, message?: string) {
    super(message ?? kind);
  }
}

export const VaultErrorKind = {
  Unavailable: 'vault_unavailable',
  AuthExpired: 'vault_auth_expired',
  PermissionDenied: 'vault_permission_denied',
  RateLimited: 'vault_rate_limited',
  SecretNotFound: 'vault_secret_not_found',
  PathAlreadyExists: 'vault_path_already_exists',
  ValidationRejected: 'vault_validation_rejected',
  RotationUnsupported: 'vault_rotation_unsupported',
  GrantConflict: 'vault_grant_conflict',
  SecretInUseCannotDelete: 'vault_secret_in_use_cannot_delete',
} as const;

export class VaultError extends Error {
  constructor(public kind: VaultErrorKind, public detail?: Record<string, unknown>, message?: string) {
    super(message ?? kind);
  }
}
```

Each `VaultErrorKind` has an i18n catalog key under `errors.vault.<kind>`; each `ConflictKind` under `errors.conflict.<kind>`.

### Quickstart (inline)

End-to-end operator flow on a freshly-built M4 dashboard against the existing M3 + M2.3 infrastructure:

1. From a clean checkout, run `goose up` (applies the M4 schema-extension migration).
2. From `dashboard/`, run `bun install && bun run drizzle:pull && bun run drizzle:migrate` (regenerates supervisor types, applies dashboard-side migrations if any).
3. Provision the `garrison-dashboard` Machine Identity credentials in Infisical and persist them in the operator's secret store (Coolify env or restricted file per threat model §6 Q7).
4. Set the new env vars `INFISICAL_DASHBOARD_ML_CLIENT_ID` and `INFISICAL_DASHBOARD_ML_CLIENT_SECRET` on the dashboard runtime.
5. Restart the supervisor to pick up the new `internal/agents` LISTEN goroutine (or rolling-restart the supervisor and dashboard together).
6. From `dashboard/`, run `bun run build` then `node .next/standalone/server.js` (or use the production Docker image which already targets standalone).
7. Visit the dashboard URL; existing operator session continues from M3.
8. Navigate to `/vault`, click "create secret"; fill the form; submit. Secret appears in the list within 2 seconds (real-time `pg_notify`).
9. Navigate to `/agents/<role>/edit`; change the model from `sonnet` to `haiku`; submit. Banner shows "next-spawn-only" reminder.
10. Trigger a spawn for the role (e.g., create a ticket the role listens for); verify the spawned subprocess runs with the new model.
11. Navigate to `/vault/rotation`; identify a stale secret; click "rotate now"; confirm. Rotation completes (or surfaces a hygiene-critical alert on partial failure).
12. Navigate to `/departments/engineering`; drag a ticket from `in_dev` to `in_review`. Activity feed renders the operator-initiated transition with distinct visual treatment.

The same steps are exercised by the M4 Playwright integration suite.

---

## Subsystem state machines and lifecycles

### Server action mutation flow (canonical)

Every mutation server action follows the same shape:

```text
1. Authenticate: read better-auth session via lib/auth/session.ts
   - No session → throw 401 (FR-001)

2. Authorize: any-logged-in-operator passes (FR-002); no per-action gating

3. Validate input:
   - Field-level (required, format, enum membership)
   - Reference integrity (FK-equivalent checks against current state)

4. Open transaction (db.transaction async block):
   a. Optimistic lock check (where applicable, FR-101 / FR-084):
      SELECT ... WHERE updated_at = $versionToken FOR UPDATE
      Empty result → throw ConflictError(StaleVersion, serverState)
   b. Write the mutation (INSERT / UPDATE / DELETE on the appropriate table)
   c. Build the audit row:
      - vault: vault_access_log row with extended outcome + metadata JSONB
      - ticket / agent: event_outbox row with field-level diff
   d. Issue pg_notify on the appropriate channel:
      - work.vault.<kind> for vault mutations
      - work.ticket.<kind> for ticket mutations
      - work.agent.<kind> for agent mutations
      - agents.changed for agent edits (consumed by supervisor cache invalidator)
   e. Commit transaction (atomic with the pg_notify per Phase 0 research item 2)

5. Return the typed result (or throw VaultError / ConflictError as appropriate)
```

The activity feed (M3's listener) sees the `pg_notify` and renders the event within 2 seconds. The reconnect / catch-up flow continues to read `event_outbox` and `vault_access_log` rows ordered by ID after `Last-Event-ID`.

### Optimistic-locking conflict resolution lifecycle

```text
state: editing  (operator has the form open)
  on save-attempt → checking-version

state: checking-version  (server action validates updated_at)
  on version-match → committing
  on version-mismatch → conflict-resolution

state: committing  (transaction in progress)
  on commit-success → done
  on commit-failure → editing  (with error toast)

state: conflict-resolution  (modal showing operator's draft + server version)
  on operator-overwrite → checking-version  (with new versionToken from server state)
  on operator-merge-manually → editing  (merged content loaded into form)
  on operator-discard → idle  (form closed)

state: done  (terminal)
state: idle  (terminal)
```

The conflict-resolution modal renders a side-by-side diff of the operator's draft vs. the latest saved version. For agent.md this is a markdown diff; for secret edits it's a field-level diff (values redacted).

### Multi-step rotation lifecycle

```text
state: initiating  (operator clicked rotate-now)
  on infisical_native + supported → calling-infisical-api
  on manual_paste → modal-open (waiting for new value)
  on not_rotatable → rejected (UI disabled this anyway)

state: calling-infisical-api  (Infisical's rotation API in flight)
  on success → updating-metadata
  on failure → rotation_failed (audit + alert)

state: modal-open  (operator typing new value)
  on submit → writing-new-value-to-infisical
  on cancel → idle

state: writing-new-value-to-infisical
  on success → updating-metadata
  on failure → rotation_failed (audit + alert)

state: updating-metadata  (secret_metadata.last_rotated_at update + audit row)
  on success → completed (audit + activity feed)
  on failure → rotation_failed_post_rotation
                   (Infisical has new value, metadata stale —
                    hygiene-critical alert with re-sync action,
                    operator drives recovery)

state: completed  (terminal)
state: rotation_failed  (terminal — operator-recoverable)
state: rotation_failed_post_rotation  (terminal — desync state, alert)
```

`vault_access_log` carries `outcome='rotation_failed'` rows with `metadata.failed_step` populated for each transient failure path; the `outcome='rotation_completed'` row fires only on the successful end-to-end commit.

### Secret reveal modal lifecycle

```text
state: idle  (operator on a secret view)
  on reveal-click → confirm-prompt

state: confirm-prompt  (single-click confirmation in the modal)
  on confirm → fetching-value
  on cancel → idle

state: fetching-value  (server action revealSecret() in flight)
  on success → rendered (value visible, auto-hide timer starts)
  on failure → idle (with error toast)

state: rendered  (value visible in modal)
  on auto-hide-timer-elapsed (30s) → hidden
  on operator-close-modal → hidden (immediate)
  on operator-copy-to-clipboard → rendered (timer continues)

state: hidden  (modal closed; value purged from DOM)
  → idle
```

The `value_revealed` audit row is written at the `fetching-value → rendered` transition, not at the click. Failed reveals (network error, permission denied at the Infisical layer) do not write audit rows (FR-022). The DOM purge on hide / close MUST destroy the React component tree (no orphaned refs) — verified by Playwright with a `getComputedStyle` + `document.body.textContent.includes(value)` check post-close.

### Supervisor `internal/agents` cache invalidation lifecycle

```text
[supervisor side]

state: cache-warm  (M2.1 startup-once cache populated)
  on agents.changed notify (role_slug: X) → invalidating-X

state: invalidating-X  (cache.Reset(X) callback running)
  on reset-complete → cache-warm  (next spawn for role X re-reads from agents)

[dashboard side, transactional with the agents-row write]

state: writing-agents-row
  on transaction-prepare → emitting-pg-notify
  on emitting-pg-notify → committing
  on committing-success → done
```

Wiring: `supervisor/internal/agents/cache.go` gains a `LISTEN agents.changed` goroutine, fed by the supervisor's existing `internal/events` LISTEN infrastructure (or a private LISTEN connection if event mux isolation matters). The goroutine receives a `role_slug` payload, calls `cache.Reset(roleSlug)`, and continues. On the dashboard side, `lib/actions/agents.ts:editAgent` issues `SELECT pg_notify('agents.changed', $roleSlug)` inside the same transaction as the `agents` UPDATE.

### Drag-to-move ticket transition lifecycle (operator-initiated)

```text
state: idle  (Kanban view rendered)
  on drag-start → dragging

state: dragging  (operator holding the card)
  on drag-end-on-source-column → idle (no-op; FR-036)
  on drag-end-on-different-column → optimistic-update

state: optimistic-update  (UI reflects the new column immediately)
  on server-action-success → committed
  on server-action-failure → reverting

state: committed  (ticket_transitions row written, pg_notify fired)
  → idle (with activity-feed event rendered)

state: reverting  (UI snaps back to source column)
  → idle (with toast "couldn't save")
```

The server action `moveTicket` writes a `ticket_transitions` row with `hygiene_status='operator_initiated'`, `agent_instance_id=NULL`, then issues `pg_notify('work.ticket.transitioned.<dept>.<from>.<to>', payload)`. Per FR-029 (clarified), agents listening on the channel spawn as they would on agent-driven transitions. The `event_outbox` is also written (the channel-based `pg_notify` is the activity-feed signal; the `event_outbox` row is the audit-trail durable record consumed by reconnect catch-up).

---

## Concrete interfaces

### `lib/vault/infisicalClient.ts`

```ts
import { InfisicalClient } from '@infisical/sdk';

const client = new InfisicalClient({
  auth: {
    universalAuth: {
      clientId: process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID!,
      clientSecret: process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET!,
    },
  },
  siteUrl: process.env.INFISICAL_SITE_URL,
});

export const dashboardVault = client;
```

Module-init singleton. The SDK handles ML auth refresh on 401 (per Phase 0 research item 5). All vault server actions import `dashboardVault` and call its typed methods.

### `lib/vault/leakScan.ts`

```ts
const PATTERNS: { name: PatternName; re: RegExp }[] = [
  { name: 'sk-prefix', re: /\bsk-[A-Za-z0-9]{20,}\b/ },
  { name: 'xoxb', re: /\bxoxb-[A-Za-z0-9-]+\b/ },
  { name: 'aws-akia', re: /\bAKIA[0-9A-Z]{16}\b/ },
  { name: 'pem-header', re: /-----BEGIN [A-Z ]+-----/ },
  { name: 'github-pat', re: /\bghp_[A-Za-z0-9]{36}\b/ },
  // ... full set per AGENTS.md §M2.3
];

export function scanForLeaks(content: string, fetchableValues: string[]): { match: PatternName; offset: number }[] {
  // Two-pass scan:
  // 1. Pattern match on shape
  // 2. Verbatim match against fetchable secret values (Rule 1 enforcement)
  // Returns all matches; consumers reject save if non-empty.
}
```

Mirrors `internal/vault/scan.go` in TS. Used by `lib/actions/agents.ts:editAgent` before persisting agent.md.

### `lib/audit/eventOutbox.ts` and `lib/audit/vaultAccessLog.ts`

```ts
export async function writeMutationEventToOutbox(
  tx: DrizzleTransaction,
  event: MutationEvent
): Promise<{ id: bigint }> {
  const row = await tx.insert(eventOutbox).values({
    channel: channelForEvent(event),
    payload: event,
    created_at: sql`now()`,
  }).returning({ id: eventOutbox.id });
  return row[0];
}

export async function emitPgNotify(
  tx: DrizzleTransaction,
  channel: string,
  payload: string
): Promise<void> {
  await tx.execute(sql`SELECT pg_notify(${channel}, ${payload})`);
}

export async function writeVaultMutationLog(
  tx: DrizzleTransaction,
  params: { outcome: VaultOutcome; secretPath?: string; metadata: Record<string, unknown>; agentInstanceId?: string; ticketId?: string }
): Promise<void> {
  await tx.insert(vaultAccessLog).values({
    outcome: params.outcome,
    secret_path: params.secretPath,
    metadata: params.metadata,
    agent_instance_id: params.agentInstanceId,
    ticket_id: params.ticketId,
    timestamp: sql`now()`,
  });
}
```

Both helpers are called inside a `db.transaction` block. The `pg_notify` is issued LAST in the transaction (just before commit) so subscribers see committed state.

### `lib/locks/version.ts`

```ts
export async function checkAndUpdate<T>(
  tx: DrizzleTransaction,
  table: PgTable,
  primaryKey: string,
  expectedVersionToken: string,
  changes: Partial<T>
): Promise<{ accepted: true; newVersionToken: string } | { accepted: false; serverState: T }> {
  const result = await tx.update(table)
    .set({ ...changes, updated_at: sql`now()` })
    .where(and(eq(table.id, primaryKey), eq(table.updated_at, expectedVersionToken)))
    .returning();

  if (result.length === 0) {
    const current = await tx.select().from(table).where(eq(table.id, primaryKey)).limit(1);
    return { accepted: false, serverState: current[0] };
  }
  return { accepted: true, newVersionToken: result[0].updated_at };
}
```

### `app/api/sse/activity/route.ts` (extension)

The M3 SSE route is extended to subscribe to the additional `work.vault.*`, `work.ticket.transitioned.*`, `work.agent.*` channel patterns plus the `event_outbox` poll for catch-up. The extension is additive — the existing `KNOWN_CHANNELS` and `KNOWN_CHANNEL_PATTERNS` lists grow.

```ts
// lib/sse/channels.ts (extended)
export const KNOWN_CHANNELS = [
  'work.ticket.created',
  // M4 additions
  'work.ticket.created',
  'work.ticket.edited',
];

export const KNOWN_CHANNEL_PATTERNS = [
  /^work\.ticket\.transitioned\.[a-z_-]+\.[a-z_-]+\.[a-z_-]+$/,
  // M4 additions
  /^work\.vault\.[a-z_-]+$/,
  /^work\.agent\.[a-z_-]+$/,
];
```

`agents.changed` is NOT in the SSE channel set — it's supervisor-internal, not a feed event.

---

## Test strategy

### Unit tests (Vitest, colocated)

- `lib/vault/leakScan.test.ts`:
  - `detects all 10 M2.3 patterns by shape`
  - `detects verbatim secret values from fetchableValues array regardless of shape`
  - `returns empty array for clean content`
  - `tolerates UTF-8 / unicode boundary cases`
- `lib/audit/eventOutbox.test.ts`:
  - `writeMutationEventToOutbox writes a row with the correct channel`
  - `emitPgNotify issues a SELECT pg_notify call inside the transaction`
- `lib/audit/vaultAccessLog.test.ts`:
  - `writeVaultMutationLog rejects payloads containing apparent secret values`
  - `metadata field stores JSONB without coercing to string`
- `lib/locks/version.test.ts`:
  - `checkAndUpdate returns accepted on matching versionToken`
  - `checkAndUpdate returns serverState on mismatched versionToken`
- `lib/vault/outcomes.test.ts`:
  - `VaultOutcome enum matches the goose migration's enum extension exactly`
- `lib/actions/tickets.test.ts`:
  - `createTicket validates required fields`
  - `createTicket emits work.ticket.created pg_notify inside transaction`
  - `moveTicket no-ops when fromColumn === toColumn`
  - `moveTicket emits work.ticket.transitioned.<dept>.<from>.<to>`
  - `editTicket uses last-write-wins (no optimistic lock)`
- `lib/actions/vault.test.ts`:
  - `createSecret rejects path violating Rule 4 conventions`
  - `editSecret returns conflict on stale versionToken`
  - `deleteSecret rejects without typed-name confirmation`
  - `revealSecret writes value_revealed audit row`
  - `initiateRotation dispatches by rotation_provider value`
- `lib/actions/agents.test.ts`:
  - `editAgent rejects agent.md containing a fetchable secret value`
  - `editAgent emits agents.changed pg_notify`
  - `editAgent returns conflict on stale versionToken`

### Integration tests (Playwright, against testcontainer Postgres + Infisical + standalone dashboard)

Each test bootstraps fresh containers, applies migrations, seeds fixtures, then exercises the built standalone bundle.

- `tests/integration/vault-create-edit-delete.spec.ts`:
  - `operator creates a secret end-to-end with audit row`
  - `operator edits a secret with diff-view confirmation`
  - `operator deletes a secret with typed-name confirmation`
  - `delete reveals affected role list before confirmation`
  - `delete fails gracefully if Infisical-level constraint blocks`
- `tests/integration/vault-grant-edit.spec.ts`:
  - `operator adds a grant via the matrix view`
  - `trigger rebuild_secret_metadata_role_slugs fires automatically`
  - `next spawn for the role injects the new env var`
  - `operator removes a grant via the matrix view`
- `tests/integration/vault-rotation.spec.ts`:
  - `infisical_native rotation completes via API`
  - `manual_paste rotation requires new value in modal`
  - `not_rotatable secrets show disabled rotation UI`
  - `rotation failure mid-flow surfaces hygiene-critical alert`
- `tests/integration/vault-reveal.spec.ts`:
  - `reveal modal renders value after confirm`
  - `auto-hide fires after 30 seconds`
  - `value purged from DOM after modal close`
  - `value_revealed audit row written exactly once per reveal`
- `tests/integration/vault-path-tree.spec.ts`:
  - `operator creates a new path with valid Rule 4 prefix`
  - `path rename updates secret_metadata atomically`
  - `multi-secret path move requires typed-name confirm`
- `tests/integration/ticket-create.spec.ts`:
  - `operator creates a ticket from the Kanban view`
  - `new ticket appears in source column without page reload`
  - `event_outbox row + activity-feed render within 2 seconds`
- `tests/integration/ticket-drag.spec.ts`:
  - `operator drags ticket from in_dev to in_review`
  - `transition row hygiene_status='operator_initiated'`
  - `agents listening on the channel spawn as expected`
  - `drag back to source column is a no-op`
  - `drag during connection drop reverts cleanly`
  - `concurrent drag and supervisor finalize serialize correctly`
- `tests/integration/ticket-inline-edit.spec.ts`:
  - `operator edits title / description / priority / assigned-agent inline`
  - `last-write-wins on concurrent inline edits`
  - `inline edit does not create a transition row`
- `tests/integration/agent-settings-edit.spec.ts`:
  - `operator edits agent.md / model / concurrency / listens_for / skills`
  - `banner shows running-instance count with prior config`
  - `agents.changed pg_notify reaches supervisor`
  - `next spawn picks up the new config`
- `tests/integration/agent-leak-scan-rejection.spec.ts`:
  - `agent.md containing a verbatim fetchable secret is rejected at save`
  - `agent.md containing only a pattern shape (no fetchable value) is accepted`
- `tests/integration/pattern-category-display.spec.ts`:
  - `seeded suspected_secret_emitted row with category renders correctly`
  - `pre-M4 row with NULL category renders as 'unknown'`
  - `category filter narrows hygiene table`
- `tests/integration/concurrent-edit-conflict.spec.ts`:
  - `two operator sessions editing same agent.md surface conflict UI`
  - `two operator sessions editing same secret surface conflict UI`
  - `operator can overwrite, merge, or discard from conflict modal`
- `tests/integration/multi-step-rotation-failure.spec.ts`:
  - `Infisical succeeds, local audit fails — desync alert`
  - `re-sync action restores consistency`
- `tests/integration/session-expiry-mid-mutation.spec.ts`:
  - `expired session save attempt returns 401`
  - `form state preserved on re-auth`
  - `submit succeeds after re-auth`

### Supervisor-side tests (Go, existing `go test ./...`)

- `supervisor/internal/agents/cache_test.go`:
  - `LISTEN agents.changed receives notification and resets cache for role_slug`
  - `concurrent reset and read serializes correctly`
  - `cache reset preserves entries for unaffected roles`
- `supervisor/internal/finalize/scan_test.go` (extended):
  - `scanAndRedactPayload writes pattern category to ticket_transitions row`
  - `unknown patterns get 'unknown' category`

### Regression discipline

The M3 Playwright suite continues to pass unchanged. The supervisor's existing M2.1 / M2.2 / M2.2.1 / M2.2.2 / M2.3 test suites continue to pass. The M3 unit and integration test suites pass under M4's data shapes (verified by re-running them in the M4 acceptance pass).

---

## Deployment changes

### `dashboard/Dockerfile`

Unchanged from M3 except confirming the standalone runtime continues (FR-122). The test harness already targets standalone in the production path; M4 migrates the test runner to standalone too:

```diff
# package.json scripts
-    "test:integration": "playwright test --config playwright.config.ts",
+    "test:integration": "next build && playwright test --config playwright.config.ts",
```

`playwright.config.ts` switches its `webServer.command` from `bun run start` to `node .next/standalone/server.js`.

### `supervisor/docker-compose.yml`

Add three env vars to the `dashboard` service block:

```yaml
  dashboard:
    environment:
      # ... existing M3 vars ...
      INFISICAL_DASHBOARD_ML_CLIENT_ID: ${INFISICAL_DASHBOARD_ML_CLIENT_ID}
      INFISICAL_DASHBOARD_ML_CLIENT_SECRET: ${INFISICAL_DASHBOARD_ML_CLIENT_SECRET}
      INFISICAL_SITE_URL: ${INFISICAL_SITE_URL}
```

The supervisor block is unchanged; the `internal/agents` cache invalidator uses the existing `GARRISON_DATABASE_URL`.

### `docs/ops-checklist.md` — new M4 section

Subsections:

1. **Goose migration ordering**: `goose up` lands the M4 schema-extension migration; `bun run drizzle:pull` regenerates supervisor types.
2. **`garrison-dashboard` Machine Identity**: provisioning checklist (Infisical UI walkthrough or CLI), credential storage discipline (per threat model §6 Q7).
3. **Dashboard env-var pinning**: `INFISICAL_DASHBOARD_ML_CLIENT_ID` and `INFISICAL_DASHBOARD_ML_CLIENT_SECRET` set via Coolify env or restricted file.
4. **Image digest pinning**: post-build, capture and record (continues M3 pattern).
5. **`secret_metadata.rotation_provider` reclassification**: existing rows default to `manual_paste`; operator reclassifies via the M4 vault-edit UI for any Infisical-rotatable backends.
6. **Supervisor restart**: required once after deploying the M4 supervisor binary so the new `internal/agents` LISTEN goroutine starts. Subsequent agents-row writes propagate via `pg_notify` without restart.

---

## Open questions for plan execution

These are not blocking for plan-write but resolve during implementation. None contradict spec decisions.

1. **`event_outbox` writer ownership**: Phase 0 research item 4 confirms whether the supervisor and dashboard sharing `event_outbox` works without invariant breakage, or whether a sibling `dashboard_event_outbox` is needed. Default: shared.
2. **`@infisical/sdk` TS SDK shape**: Phase 0 research item 5 confirms the SDK's ML auth + rotation API surface. Fallback: hand-rolled HTTP client against Infisical's REST API (rejected for the same reasons the supervisor uses the Go SDK).
3. **TS-side leak-scan CI check**: Phase 0 research item 7 confirms whether ESLint custom rule + grep CI step is enough, or whether a dedicated TS analyzer is needed.
4. **Drizzle migration DSL for enum extensions**: Drizzle's `ALTER TYPE ... ADD VALUE` support is limited; the goose migration is hand-written SQL (correct), but the Drizzle introspection downstream may need a follow-up `drizzle:pull` after each enum change.
5. **Test fixtures for Infisical write flows**: extending the existing testcontainer Infisical seed pattern (M2.3) to cover M4's write paths. Operators of the test container need write privileges; spec confirms (US1 acceptance scenarios all go end-to-end).

---

## Forward-compatibility hooks (one-line each, no design)

- The mutation event types (`MutationEvent` discriminated union) extend to M5 (CEO chat) by adding `ceo.message.*` variants.
- The optimistic-locking pattern (`lib/locks/version.ts`) extends to M7 (hiring approve / reject) and any future single-fire transition surface.
- The conflict resolution modal extends to any multi-actor edit surface.
- The reveal modal pattern extends to any future credential or secret-adjacent surface.
- The TS-side leak-scan discipline extends to any future write surface that touches secret-shaped values.
- The dashboard's `garrison-dashboard` Machine Identity activates additional Infisical write capabilities in future milestones (e.g., bulk operations, automated rotation policies) without requiring a new ML.

These are mentions only. M4 does not design for them.

---

*Plan written 2026-04-27 on branch `009-m4-dashboard-mutations`. Next: `/garrison-tasks m4` to break this plan into tasks.*
