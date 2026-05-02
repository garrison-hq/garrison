# Implementation plan — M6 (CEO ticket decomposition + hygiene + cost-throttle)

**Branch**: `015-m6-decomposition-hygiene-throttle` (cuts off post-M5.4-merge HEAD; the spec workshop on `014-m6-m7-spikes` lands first)
**Date**: 2026-05-02
**Spec**: [`specs/015-m6-decomposition-hygiene-throttle/spec.md`](spec.md)
**Context**: [`specs/_context/m6-context.md`](../_context/m6-context.md)
**Spike**: [`docs/research/m6-spike.md`](../../docs/research/m6-spike.md)

---

## Summary

M6 ships three loosely-coupled threads atop the M5.x substrate: (A) CEO ticket decomposition with parent/child linkage and a per-turn ticket-creation cap; (B) hygiene-status vocabulary extension with a `thin_diary` predicate and `missing_kg_facts` evaluator activation; (C) a cost-throttle actuator pair — per-company daily budget defer and per-company rate-limit pause — backed by a new `throttle_events` audit table with live SSE rendering on `/hygiene`. Thread C requires a foundation deliverable: closing the cost-telemetry blind spot at `docs/issues/cost-telemetry-blind-spot.md` so `agent_instances.total_cost_usd` reads honestly on clean finalizes (US2/FR-020). Schema is additive — three nullable columns and one append-only audit table; zero destructive migrations; locked-deps streak continues.

---

## Technical context

**Language/Version**: Go 1.22+ (supervisor); Node 22 + TypeScript 5 (dashboard, dependency-locked at the bun.lock).
**Primary Dependencies**: pgx/v5, sqlc, slog, errgroup, goose (supervisor); Next.js 16 + react-markdown + minio-go/v7 + @uiw/react-codemirror (dashboard, all from M5.4 ship). **No new dependencies in M6** — locked-deps streak continues from M3 + M4 + M5.x.
**Storage**: Postgres (single source of truth per Constitution I); MemPalace for diary/KG (read by hygiene evaluator only). MinIO from M5.4 untouched.
**Testing**: Go testing + testcontainers-go + chaos build tag (supervisor); Vitest + Playwright (dashboard, tests added only if the existing Go-only memory rule is overridden — see §Test strategy).
**Target Platform**: Linux server (Hetzner via Coolify), single static binary deploy.
**Project Type**: Monorepo — `supervisor/` (Go) + `dashboard/` (Next.js).
**Performance Goals**: `prepareSpawn` transaction overhead from throttle gate ≤ 10ms p95 (the gate adds one indexed read on `companies` + one rolling-24h sum on `agent_instances`, both small queries on indexed columns). SSE update latency for throttle events ≤ 1s from supervisor write to dashboard render.
**Constraints**: Throttle decisions atomic with the spawn-prep tx (single transaction, no goroutine). pg_notify writes in the same tx as the audit-row INSERT. Cost-telemetry fix MUST NOT extend the existing M1 `TerminalWriteGrace` shutdown window (FR-022).
**Scale/Scope**: Solo-operator deployment with a small N of companies (single-digit at M6 ship; design accommodates ~hundreds without rework via the new `idx_throttle_events_company_fired` index).

---

## Constitution check

| Principle | M6 status |
|---|---|
| I. Postgres is the sole source of truth | ✅ All M6 state lands in Postgres tables; pg_notify on `work.throttle.event` fires in-tx with the INSERT |
| II. MemPalace for cross-agent memory | ✅ Hygiene evaluator reads palace via `mempalace_kg_query` (existing M2.2 surface); no new memory layer |
| III. Agents are ephemeral | ✅ No long-running agents introduced; throttle gate runs synchronously in spawn-prep tx |
| IV. Soft gates over hard gates | ✅ `thin_diary` and `missing_kg_facts` are dashboard-visible audit, not workflow blockers; matches RATIONALE §5 |
| V. Skills come from skills.sh | ✅ M6 doesn't touch skills (M7 territory) |
| VI. Hiring is UI-driven | ✅ M6 doesn't touch hiring (M7 territory) |
| VII. Supervisor is Go with locked deps | ✅ Zero new Go dependencies |
| VIII. Every goroutine accepts a context | ✅ All new code paths run inside existing context-aware paths (`prepareSpawn`, `Run`, evaluator); no new goroutines |
| IX. Specs narrow per milestone, end-to-end functional | ✅ M6 ships three threads end-to-end; M7+ deferred per "Out of scope" |
| X. Per-department concurrency caps | ✅ The new daily budget is **orthogonal** to the concurrency cap (different axis: spend over time vs. parallelism at any moment). Per the M6 context's Q12 reconciliation; concurrency caps stay as the primary dial. |
| XI. Self-hosted on Hetzner | ✅ No cloud dependencies |

No violations. No complexity-tracking entries.

---

## Project structure

### Documentation (this feature)

```text
specs/015-m6-decomposition-hygiene-throttle/
├── spec.md          # /garrison-specify output (clarified)
├── plan.md          # this file (/garrison-plan output)
└── tasks.md         # /garrison-tasks output (next)
```

### Source code (repository root)

New / changed paths:

```text
supervisor/
├── cmd/supervisor/
│   ├── main.go                             # CHANGE — wire env-var loads (G1) + result-grace timing
│   └── migrations/
│       └── 20260503000000_m6_decomposition_hygiene_throttle.sql   # NEW (single migration; goose up+down)
├── internal/
│   ├── throttle/                           # NEW package
│   │   ├── throttle.go                     # public surface: Check, EmitEvent, Decision struct
│   │   ├── budget.go                       # budget-predicate impl
│   │   ├── pause.go                        # pause-predicate impl
│   │   ├── events.go                       # InsertThrottleEvent + pg_notify in-tx
│   │   ├── throttle_test.go                # unit tests (I1)
│   │   └── integration_test.go             # //go:build integration (I2)
│   ├── spawn/
│   │   ├── spawn.go                        # CHANGE — prepareSpawn calls throttle.Check
│   │   └── pipeline.go                     # CHANGE — post-finalize result-grace window (US2/FR-020)
│   ├── garrisonmutate/
│   │   ├── verbs_tickets.go                # CHANGE — parent_ticket_id on create_ticket
│   │   └── verbs_tickets_test.go           # CHANGE — extend with parent tests (I4)
│   ├── chat/
│   │   ├── policy.go                       # CHANGE — maybeFireTicketCreationCeiling
│   │   ├── transport.go                    # CHANGE — ChatErrorTicketCreationCeilingReached const
│   │   └── policy_m6_test.go               # NEW — TestTicketCreationCeilingFires (I5)
│   ├── hygiene/
│   │   ├── evaluator.go                    # CHANGE — thin_diary + missing_kg_facts paths
│   │   └── evaluator_test.go               # CHANGE — extend (I6)
│   └── store/                              # AUTO-GENERATED via sqlc generate
│       ├── throttle.sql.go                 # NEW (from queries/throttle.sql)
│       ├── companies.sql.go                # CHANGE (extend)
│       ├── tickets.sql.go                  # CHANGE (parent_ticket_id arg)
│       └── agent_instances.sql.go          # CHANGE (rolling-24h sum)
├── chaos_test.go                           # CHANGE — TestRateLimitPausesSpawnsForCompany (I7)
└── sqlc.yaml                               # CHANGE — add migration to schema list

migrations/queries/
├── throttle.sql                            # NEW (3 queries)
├── companies.sql                           # CHANGE (extend if exists; create if not)
├── tickets.sql                             # CHANGE (parent_ticket_id support)
└── agent_instances.sql                     # CHANGE (cost-window sum)

dashboard/
├── app/
│   └── api/sse/
│       └── throttle/
│           └── route.ts                    # NEW SSE bridge
├── components/features/
│   ├── hygiene-table/
│   │   ├── HygieneTabStrip.tsx             # NEW — three-tab strip
│   │   ├── ThrottleEventsTable.tsx         # NEW — sub-table
│   │   ├── HygieneTable.tsx                # CHANGE — wrap with TabStrip
│   │   └── HygieneTabStrip.test.tsx        # NEW (only if frontend tests are unblocked, see §Test strategy)
│   └── kanban/
│       └── TicketCard.tsx                  # CHANGE — parent-id chip
├── lib/
│   ├── queries/
│   │   ├── hygiene.ts                      # CHANGE — throttle-events query
│   │   └── throttle.ts                     # NEW — list-throttle-events helper
│   └── sse/
│       └── throttleStream.ts               # NEW — useThrottleStream hook
└── drizzle/
    └── schema.supervisor.ts                # CHANGE — drizzle pulls the new columns + table from migration

docs/
├── retros/m6.md                            # NEW (post-ship)
└── ARCHITECTURE.md                         # CHANGE — M6 paragraph annotated; schema section + 3 columns + throttle_events
```

**Structure decision**: existing monorepo shape (supervisor + dashboard) stays. M6 introduces one new Go package (`internal/throttle/`) and one new dashboard SSE route; everything else extends existing files. No rename, no relocation.

---

## Subsystem walkthroughs

### 1. `internal/throttle/` — new package (FR-030..FR-045)

Owns the predicate evaluation, audit row insert, and pg_notify emit for the budget + rate-limit gates. Called synchronously from `internal/spawn::prepareSpawn` inside the spawn-prep tx.

**Public surface** (`throttle.go`):

```go
package throttle

type Decision struct {
    Allowed   bool
    Kind      string          // "" | "company_budget_exceeded" | "rate_limit_pause"
    Payload   json.RawMessage // throttle_events.payload value (forensic detail)
}

type Deps struct {
    Pool         *pgxpool.Pool
    Queries      *store.Queries
    Logger       *slog.Logger
    DefaultSpawnCostUSD pgtype.Numeric  // GARRISON_DEFAULT_SPAWN_COST_USD parsed
    RateLimitBackOff    time.Duration   // GARRISON_RATE_LIMIT_BACK_OFF_SECONDS parsed
    Now func() time.Time                // injection seam for tests
}

// Check evaluates pause then budget for the supplied company.
// Pause wins over budget when both fire (D2 / B1 ordering).
// Caller is responsible for passing the spawn-prep tx; Check uses
// q.Queries directly so it composes inside that tx.
func Check(ctx context.Context, deps Deps, q *store.Queries, companyID pgtype.UUID) (Decision, error)

// FirePause writes a throttle_events row + pg_notify in the supplied tx.
// Called from spawn/pipeline.go OnRateLimit — the rate-limit observer
// flips into actuator mode by calling this.
func FirePause(ctx context.Context, deps Deps, q *store.Queries, companyID pgtype.UUID, eventDetail RateLimitDetail) error

// FireBudgetDefer writes a throttle_events row + pg_notify in the
// supplied tx. Called from Check when the budget predicate defers.
func FireBudgetDefer(ctx context.Context, deps Deps, q *store.Queries, companyID pgtype.UUID, current, estimated, budget pgtype.Numeric) error
```

**Internal predicates** (`budget.go`, `pause.go`):

```go
// budget.go
func evaluateBudget(state CompanyThrottleState, estimatedNext pgtype.Numeric) (Decision, error)
//   Decision{Allowed: true} if budget is NULL OR (current + estimated_next <= budget)
//   Decision{Allowed: false, Kind: "company_budget_exceeded", Payload: ...} otherwise

// pause.go
func evaluatePause(state CompanyThrottleState, now time.Time) Decision
//   Decision{Allowed: true} if pause_until is NULL OR pause_until <= now
//   Decision{Allowed: false, Kind: "rate_limit_pause", Payload: {pause_until}} otherwise
```

`CompanyThrottleState` is the row shape returned by sqlc's `GetCompanyThrottleState` — see §"sqlc queries". Carries `daily_budget_usd`, `pause_until`, and the rolling-24h cost sum from a single read.

**Atomicity contract**: every public function takes a `*store.Queries` instance bound to the caller's tx. The throttle package never opens its own tx. Callers (spawn-prep + the rate-limit observer) own the boundary.

**Pg_notify shape** (`events.go`):

```go
func emitNotify(ctx context.Context, q *store.Queries, payload ThrottleEventPayload) error
//   q.Notify(ctx, "work.throttle.event", json-encoded {event_id, company_id, kind, fired_at})
```

Matches the M5.x `work.chat.*` and `work.ticket.*` shape so the SSE bridge uses the existing handler pattern.

### 2. `internal/spawn/` extensions

#### 2a. `prepareSpawn` (spawn.go) — throttle gate (FR-031, FR-041)

Existing `prepareSpawn` opens a tx, validates the event, claims an `agent_instances` row. M6 adds a single call to `throttle.Check` before the agent_instances INSERT:

```go
// pseudocode inside the existing prepareSpawn tx
ticket := q.GetTicketByID(ctx, eventPayload.TicketID)
dept   := q.GetDepartmentByID(ctx, ticket.DepartmentID)
companyID := dept.CompanyID

decision, err := throttle.Check(ctx, deps.Throttle, q, companyID)
if err != nil { return ErrThrottleCheckFailed }
if !decision.Allowed {
    // record audit row + emit pg_notify in this same tx
    _ = throttle.FireBudgetDefer(ctx, deps.Throttle, q, companyID, ...)
    // do NOT mark event_outbox.processed_at — let the next poll re-attempt
    return ErrSpawnDeferred
}
// existing path: claim agent_instances row, etc.
```

`ErrSpawnDeferred` is a new sentinel; `events/dispatcher.go` treats it as "leave the row unprocessed; do not log a hard error" (similar to existing concurrency-cap-deferred handling at `internal/concurrency/cap.go`).

#### 2b. `OnRateLimit` (pipeline.go) — actuator mode (FR-040, FR-042, FR-043)

Today `pipeline.go::OnRateLimit` logs and sets `p.rateLimitOverage=true`. M6 adds the actuator path. The change runs inside the existing event-handling goroutine; no new goroutine. The supervisor opens a SHORT independent tx (not the spawn tx — the spawn might be mid-stream-read; we don't want to wedge it):

```go
func (p *FinalizePolicy) OnRateLimit(ctx context.Context, e claudeproto.RateLimitEvent) {
    p.logger.Warn("claude rate_limit_event", ...) // existing
    if e.Status != "rejected" { return }
    if p.deps.Throttle == nil { return } // chaos/test-bypass guard

    err := pgx.BeginFunc(ctx, p.deps.Pool, func(tx pgx.Tx) error {
        q := store.New(tx)
        // FirePause sets pause_until + writes throttle_events + pg_notify
        return throttle.FirePause(ctx, p.deps.Throttle, q, p.companyID, ...)
    })
    if err != nil { p.logger.Warn("throttle FirePause failed", "err", err) }
    // p.rateLimitOverage flag remains set for terminal classification
}
```

In-flight spawn continues — FR-043 is satisfied by the fact that `FirePause` only mutates `companies.pause_until`; the running subprocess isn't signaled.

#### 2c. Cost-telemetry result-grace (pipeline.go) — US2 / FR-020..FR-022

The fix lives in `Run`'s post-finalize-commit branch. Today: finalize.OnCommit fires → some seconds later the event-bus loop closes → the supervisor signal-kills claude.

The fix: after OnCommit lands, the pipeline waits up to `GARRISON_FINALIZE_RESULT_GRACE` (default 3s) for `result.ResultSeen=true` before allowing terminal classification:

```go
// pseudocode added inside Run
if finalize.Committed && !result.ResultSeen {
    deadline := time.Now().Add(deps.FinalizeResultGrace)
    for time.Now().Before(deadline) && !result.ResultSeen {
        select {
        case <-ctx.Done(): break
        case ev := <-streamEvents:  // existing channel
            // route the event normally; will set result.ResultSeen if applicable
        case <-time.After(50 * time.Millisecond):
            // re-check loop
        }
    }
}
// existing terminal classification fires
```

**Compatibility with existing `TerminalWriteGrace`**: the result-grace runs BEFORE the SIGTERM is sent, so it's additive to startup-to-terminal latency on the success path. The existing M1 `TerminalWriteGrace` (5s) governs the post-SIGTERM window — unchanged. Open question J1 confirms the math: typical happy-path adds ~1-3s to the success-path runtime; the chaos suite verifies failure modes don't regress.

### 3. `internal/garrisonmutate/verbs_tickets.go` — parent_ticket_id (FR-001..FR-003)

Existing `CreateTicketArgs`:

```go
type CreateTicketArgs struct {
    Objective          string
    AcceptanceCriteria *string
    DepartmentSlug     string
    Metadata           json.RawMessage
}
```

Extension:

```go
type CreateTicketArgs struct {
    Objective          string
    AcceptanceCriteria *string
    DepartmentSlug     string
    Metadata           json.RawMessage
    ParentTicketID     *string  // NEW — UUID format; validated in handler
}
```

Handler additions:

1. If `ParentTicketID` is set, parse to UUID; on parse failure return `validation_failed` with detail `"parent_ticket_id is not a valid UUID"`.
2. Read parent ticket via existing `q.GetTicketByID`. Reject if not found (`validation_failed`).
3. Reject if `parent.DepartmentID != child.DepartmentID` (cross-dept disallowed per spec edge cases).
4. Reject if `parent.ColumnSlug == 'done'` (parent already closed).
5. Pass parent_ticket_id through to the existing `InsertChatTicket` sqlc call (which gets a new optional arg).

The existing audit row in `chat_mutation_audit` records the parent linkage in the `affected_resource` JSONB so forensic queries can join on it.

### 4. `internal/chat/policy.go` — per-turn ticket-creation cap (FR-004, FR-005)

Mirror of the existing M5.3 tool-call ceiling pattern. Add fields + helpers:

```go
type ChatPolicy struct {
    ...existing fields...
    ticketCreationCount int
    TicketCreationCeiling int  // GARRISON_CHAT_MAX_TICKETS_PER_TURN
}

// New helper, called from handleToolUseBlockStart when the tool name
// is "mcp__garrison-mutate__create_ticket" — count this invocation and
// fire the ceiling if exceeded.
func (p *ChatPolicy) maybeFireTicketCreationCeiling(ctx context.Context) {
    p.ticketCreationCount++
    if p.ticketCreationCount <= p.TicketCreationCeiling || p.ticketCreationCeilingFired {
        return
    }
    p.ticketCreationCeilingFired = true
    p.bailReason = ChatErrorTicketCreationCeilingReached
    EmitAssistantError(ctx, p.Pool, p.MessageID,
        ChatErrorTicketCreationCeilingReached,
        fmt.Sprintf("per-turn ticket-creation ceiling (%d) exceeded; terminating turn",
            p.TicketCreationCeiling))
}
```

Integration point: `handleToolUseBlockStart` (existing) extracts `toolName`. After the existing `maybeFireToolCallCeiling`, check if the tool is the create_ticket verb and call `maybeFireTicketCreationCeiling`. Both ceilings can fire in the same turn; tool-call ceiling wins on order (it's checked first; once fired, the turn ends regardless of ticket count).

`ChatErrorTicketCreationCeilingReached = "ticket_creation_ceiling_reached"` (transport.go const).

### 5. `internal/hygiene/evaluator.go` — thin_diary + missing_kg_facts (FR-010, FR-011)

Existing `Evaluate(ctx, e Evaluator, ...) (Status, error)` returns one of the M2.2.2 vocabulary values. Extension order — the evaluator runs predicates in this sequence and returns the first non-clean status that fires:

```
1. existing missing_diary check
2. NEW: thin_diary check (length < threshold)
3. NEW: missing_kg_facts check (mempalace_kg_query returns 0 triples)
4. existing suspected_secret_emitted (set by the M2.3 leak-scan, not the evaluator)
5. fallthrough: clean
```

The thin_diary predicate is deterministic — `len(diary.Body) < threshold`. No mempalace call; no LLM call. Threshold from `Deps.ThinDiaryThreshold` (parsed from `GARRISON_HYGIENE_THIN_DIARY_THRESHOLD`).

The missing_kg_facts predicate calls the existing `mempalace_kg_query` proxy. The query keys on the ticket's UUID (concrete kg_query payload TBD by the existing palace contract; reuse the M5.4 `getRecentKGFacts` pattern but ticket-scoped). Returns 0 triples → `missing_kg_facts`.

Order matters: thin_diary catches before missing_kg_facts so a thin diary doesn't get hidden by an absent KG (which would be an even worse outcome — the operator would chase the wrong fix).

### 6. `cmd/supervisor/main.go` lifecycle wiring

Single new `throttle.Deps` value built in `runDaemon` from the parsed config:

```go
throttleDeps := throttle.Deps{
    Pool:                pool,
    Queries:             queries,
    Logger:              logger,
    DefaultSpawnCostUSD: cfg.DefaultSpawnCostUSD,  // parsed from env
    RateLimitBackOff:    cfg.RateLimitBackOff,     // parsed from env
    Now:                 time.Now,
}

// passed into:
spawnDeps.Throttle = throttleDeps
```

No new errgroup goroutines. No new LISTEN channels at the supervisor side (the supervisor only EMITS `work.throttle.event`; the dashboard SSE bridge subscribes).

### 7. Configuration (`internal/config`)

Five new fields on `config.Config`:

| Field | Env var | Type | Default |
|---|---|---|---|
| `MaxTicketsPerTurn` | `GARRISON_CHAT_MAX_TICKETS_PER_TURN` | int | 10 |
| `ThinDiaryThreshold` | `GARRISON_HYGIENE_THIN_DIARY_THRESHOLD` | int | 200 |
| `DefaultSpawnCostUSD` | `GARRISON_DEFAULT_SPAWN_COST_USD` | numeric (string parsed) | "0.05" |
| `RateLimitBackOff` | `GARRISON_RATE_LIMIT_BACK_OFF_SECONDS` | time.Duration (parsed from int seconds) | 60s |
| `FinalizeResultGrace` | `GARRISON_FINALIZE_RESULT_GRACE` | time.Duration | 3s |

All five wired into the matching subsystems via `Deps` structs. Validation: `MaxTicketsPerTurn >= 1`; `ThinDiaryThreshold >= 0`; `DefaultSpawnCostUSD >= 0`; `RateLimitBackOff > 0`; `FinalizeResultGrace >= 0`.

### 8. Migration — `20260503000000_m6_decomposition_hygiene_throttle.sql`

```sql
-- +goose Up

-- 1. tickets: parent_ticket_id linkage
ALTER TABLE tickets
  ADD COLUMN parent_ticket_id UUID NULL REFERENCES tickets(id);
CREATE INDEX idx_tickets_parent
  ON tickets (parent_ticket_id)
  WHERE parent_ticket_id IS NOT NULL;

-- 2. companies: budget + pause columns
ALTER TABLE companies
  ADD COLUMN daily_budget_usd NUMERIC(10,2) NULL,
  ADD COLUMN pause_until TIMESTAMPTZ NULL;

-- 3. throttle_events table
CREATE TABLE throttle_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id UUID NOT NULL REFERENCES companies(id),
  kind TEXT NOT NULL CHECK (kind IN ('company_budget_exceeded','rate_limit_pause')),
  fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  payload JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX idx_throttle_events_company_fired
  ON throttle_events (company_id, fired_at DESC);

-- 4. dashboard role grants
GRANT SELECT ON throttle_events TO garrison_dashboard_app;
-- (supervisor role already has full grants at the schema-default level)

-- +goose Down

DROP INDEX IF EXISTS idx_throttle_events_company_fired;
DROP TABLE IF EXISTS throttle_events;
ALTER TABLE companies DROP COLUMN IF EXISTS pause_until;
ALTER TABLE companies DROP COLUMN IF EXISTS daily_budget_usd;
DROP INDEX IF EXISTS idx_tickets_parent;
ALTER TABLE tickets DROP COLUMN IF EXISTS parent_ticket_id;
```

Open item J2 — verify `garrison_dashboard_app` is the actual dashboard role name; M5.x schema uses it consistently.

### 9. sqlc queries

#### `migrations/queries/throttle.sql` (new)

```sql
-- name: InsertThrottleEvent :one
INSERT INTO throttle_events (company_id, kind, payload)
VALUES ($1, $2, $3)
RETURNING id, company_id, kind, fired_at, payload;

-- name: ListThrottleEventsByCompany :many
SELECT id, company_id, kind, fired_at, payload
FROM throttle_events
WHERE company_id = $1
ORDER BY fired_at DESC
LIMIT $2;

-- name: GetCompanyThrottleState :one
-- One read returns budget + pause + rolling-24h cost.
-- Exposed for throttle.Check; uses lateral subquery for the sum so
-- companies with no agent_instances rows return 0 cleanly.
SELECT
  c.id AS company_id,
  c.daily_budget_usd,
  c.pause_until,
  COALESCE(
    (SELECT SUM(ai.total_cost_usd)
       FROM agent_instances ai
       JOIN tickets t       ON t.id = ai.ticket_id
       JOIN departments d   ON d.id = t.department_id
      WHERE d.company_id = c.id
        AND ai.started_at >= NOW() - INTERVAL '24 hours'),
    0
  ) AS cost_24h_usd
FROM companies c
WHERE c.id = $1;
```

#### `migrations/queries/companies.sql` (extend)

```sql
-- name: UpdateCompanyPauseUntil :exec
UPDATE companies SET pause_until = $2 WHERE id = $1;
```

#### `migrations/queries/tickets.sql` (extend `InsertChatTicket`)

The existing `InsertChatTicket` query gets one new column passthrough for `parent_ticket_id`:

```sql
-- name: InsertChatTicket :one
INSERT INTO tickets (
  department_id, objective, acceptance_criteria, column_slug,
  metadata, origin, created_via_chat_session_id, parent_ticket_id   -- NEW
)
VALUES (
  @department_id, @objective, @acceptance_criteria, @column_slug,
  @metadata, 'ceo_chat', @created_via_chat_session_id, @parent_ticket_id  -- NEW
)
RETURNING id, department_id, objective, created_at, column_slug,
          acceptance_criteria, metadata, origin, created_via_chat_session_id,
          parent_ticket_id;
```

The dashboard `lib/actions/tickets.ts::createTicket` (operator-side) also gets a `parentTicketId` arg passthrough; drizzle picks it up from the regenerated schema after migration.

#### `migrations/queries/agent_instances.sql` (extend)

The cost-window sum is already a subquery inside `GetCompanyThrottleState`, so no separate query is needed. Skipping the per-call `SumCostUSDForCompanyInWindow` from the slate (D-level decision deferred): the joined sum is one read, cheaper than a second roundtrip.

### 10. Dashboard hygiene UI extension

Three new components:

#### `dashboard/components/features/hygiene-table/HygieneTabStrip.tsx`

Three radio-buttons-styled tabs:

```tsx
type Tab = 'failures' | 'audit' | 'all';
// renders: [agent failures] [operator audit] [all]
// active highlight: bg-accent/15 text-accent (matches the M5.4 polish pattern)
// click → calls onChange(tab); parent re-fetches with the new filter
```

State stays local to the parent (`/hygiene/page.tsx`); no URL routing.

#### `dashboard/components/features/hygiene-table/ThrottleEventsTable.tsx`

Sub-table below the existing `HygieneTable`:

```tsx
interface Props {
  events: ThrottleEventRow[];
}
// columns: fired_at (relative + tooltip with iso) | company name | kind chip | payload preview
// kind chip: muted Chip with tone='warn' for rate_limit_pause, tone='err' for company_budget_exceeded
// auto-updates from useThrottleStream() → prepends new rows
```

#### `dashboard/lib/sse/throttleStream.ts`

New hook mirroring `useChatStream`:

```ts
export function useThrottleStream(): { events: ThrottleEvent[]; lastError: string | null }
// Internally subscribes to /api/sse/throttle, listens for the
// 'throttle_event' SSE event, prepends to the in-memory buffer
// (cap at 100 events, FIFO drop on overflow).
```

#### `dashboard/app/api/sse/throttle/route.ts`

New SSE bridge (parallel to `/api/sse/chat/route.ts`):

```ts
// GET /api/sse/throttle
// Auth: getSession() → 401 on missing
// Subscribes to LISTEN 'work.throttle.event'
// Forwards each notify as SSE event 'throttle_event'
// Heartbeat 25s; close on req.signal.abort
```

Same pattern as the chat SSE route fix (no per-frame DB lookup; client handles orphan IDs by ignoring).

### 11. Dashboard kanban TicketCard extension

Add to the existing header row (id + age):

```tsx
// inside TicketCard — alongside the existing 8-char id chip
{ticket.parentTicketId ? (
  <Link
    href={`/tickets/${ticket.parentTicketId}`}
    className="font-mono text-[10.5px] text-text-3 hover:text-text-1 tracking-tight"
    onClick={(e) => e.stopPropagation()}  // don't bubble to the card click
  >
    parent: {ticket.parentTicketId.slice(0, 8)}
  </Link>
) : null}
```

Drizzle schema regen pulls `parentTicketId`; `lib/queries/kanban.ts::TicketCardRow` extends the row type with the field.

### 12. ARCHITECTURE.md amendment

Three edits in the same PR as M6 ship:

1. **M6 paragraph** — replace "M6 — CEO ticket decomposition + hygiene checks. CEO writes tickets from conversation. Hygiene dashboard shows thin/missing writes. Rate-limit back-off and cost-based throttling land here..." with the post-ship status form ("✅ Shipped 2026-MM-DD. Retro: docs/retros/m6.md. <one-line summary of the three threads + the cost-telemetry-blind-spot closure>").
2. **Schema section** — add the three new columns + the `throttle_events` table to the documented schema block. Pattern: same as the M5.4 amendment that added `companies.minio_company_md` references.
3. **Substring-match assertion test** — extend `dashboard/tests/architecture-amendment.test.ts` with a new `describe('ARCHITECTURE.md M6 amendment')` block asserting the three substrings: `'**M6 — '`, `'parent_ticket_id'`, `'throttle_events'`. Matches the M5.4 pattern verbatim.

---

## Lifecycle + state machines

### Throttle gate (per-spawn)

```
event_outbox row arrives
  ↓
prepareSpawn opens tx
  ↓
GetTicketByID, GetDepartmentByID → companyID
  ↓
throttle.Check (one read of companies + rolling-24h cost)
  ↓ Decision.Allowed?
  ├─ true  → continue: InsertRunningInstance, mark event_outbox.processed_at, COMMIT
  └─ false → throttle.FireBudgetDefer (insert audit + pg_notify in tx) → ROLLBACK *event_outbox.processed_at*
              event_outbox row stays unprocessed; next poll re-attempts; may succeed if budget freed
```

**Idempotency**: a deferred event_outbox row can be re-attempted N times. Each re-attempt that defers writes another `throttle_events` row. This is by design — the audit shows attempted spawns, not just successful defers. Rate is bounded by the supervisor's poll interval (default 5s) so the worst case is ~720 deferral rows per company per hour during a sustained pause; acceptable for the audit purpose.

### Rate-limit pause window

```
mid-spawn: rate_limit_event with status='rejected' arrives
  ↓
pipeline.OnRateLimit: short independent tx
  ↓
UpdateCompanyPauseUntil(now() + RateLimitBackOff)
  ↓
throttle.FirePause (insert audit + pg_notify in same tx)
  ↓
in-flight spawn continues to its own terminal state
```

Subsequent spawn-prep tx for the same company hits the throttle gate's pause predicate → defer. Other companies' spawns unaffected (`pause_until` is per-row).

### Cost-telemetry result-grace

```
finalize.OnCommit lands (existing)
  ↓
result.ResultSeen?
  ├─ true → existing terminal classification fires immediately
  └─ false → wait up to FinalizeResultGrace
              ↓
              streamEvents channel gets the result event → result.ResultSeen=true
              ↓
              break + terminal classification fires
              (timeout: classification fires anyway; total_cost_usd may still be NULL)
```

### Per-turn ticket-creation ceiling

Mirrors the M5.3 tool-call ceiling state machine exactly:

```
each garrison-mutate.create_ticket invocation
  ↓
ticketCreationCount++
  ↓
count > ceiling?
  ├─ no  → continue
  └─ yes (first time) → ceilingFired=true; bailReason=ChatErrorTicketCreationCeilingReached
                          EmitAssistantError SSE frame
                          turn terminal commits with error_kind set
                          first N tickets persist (FR-005)
```

---

## Test strategy

Per the user's standing memory rule (`feedback_test_scope_go_only.md`): tests for Go only. Frontend changes are verified manually + by the existing Playwright integration test if a regression is suspected. Open question J3 surfaces this for explicit confirmation; if the operator overrides for M6, the bracketed Vitest tests at the end of this section come back in scope.

### Unit tests (Go, default-tag)

- `internal/throttle/throttle_test.go`:
  - `TestBudgetPredicate_AllowsBelowCap` — budget=$1.00, cost_24h=$0.50, estimated=$0.10 → Allowed
  - `TestBudgetPredicate_DefersAtCap` — budget=$1.00, cost_24h=$0.95, estimated=$0.10 → Defer with `kind='company_budget_exceeded'`
  - `TestBudgetPredicate_NullBudgetIsAlwaysAllow` — budget=NULL → Allowed regardless of cost
  - `TestBudgetPredicate_ZeroBudgetDefersAlways` — budget=$0, any cost → Defer
  - `TestPausePredicate_DefersDuringWindow` — pause_until=now+30s → Defer
  - `TestPausePredicate_AllowsAfterWindow` — pause_until=now-30s → Allowed
  - `TestPausePredicate_NullIsAlwaysAllow` — pause_until=NULL → Allowed
  - `TestDecisionComposition_PauseWinsOverBudget` — both fire → Decision carries `kind='rate_limit_pause'` (pause is the harder block)
  - `TestEmitNotifyPayloadShape` — `emitNotify` produces the canonical `{event_id, company_id, kind, fired_at}` JSON

- `internal/garrisonmutate/verbs_tickets_test.go` (extend existing file):
  - `TestCreateTicketWithParent_HappyPath` — child links to parent in same dept; persisted with parent_ticket_id
  - `TestCreateTicketWithParent_RejectsCrossDept` — parent in `engineering`, child in `qa-engineer` → validation_failed
  - `TestCreateTicketWithParent_RejectsClosedParent` — parent.column_slug='done' → validation_failed
  - `TestCreateTicketWithParent_RejectsMissingParent` — parent UUID points to nonexistent ticket → validation_failed
  - `TestCreateTicketWithParent_NilParentSucceeds` — null parent → existing happy-path unchanged

- `internal/chat/policy_m6_test.go` (new file):
  - `TestTicketCreationCeilingFires_OnEleventhCall` — 11 simulated create_ticket tool_uses → 11th fires ceiling, first 10 audited
  - `TestTicketCreationCeilingDoesNotFireOnOtherTools` — 50 mempalace_search calls → no ceiling fired (ticket counter is verb-scoped)
  - `TestTicketCreationCeiling_DefaultIsTen` — env unset → ceiling = 10
  - `TestTicketCreationCeiling_EnvOverride` — env=3 → ceiling = 3

- `internal/hygiene/evaluator_test.go` (extend existing file):
  - `TestThinDiaryPredicate_BelowThreshold` — diary=50 chars, threshold=200 → status='thin_diary'
  - `TestThinDiaryPredicate_AtThreshold` — diary=200 chars → NOT thin_diary (>= boundary)
  - `TestThinDiaryPredicate_AboveThreshold` — diary=500 chars → NOT thin_diary
  - `TestThinDiaryPredicate_OverridesBy_MissingKgFacts` — diary=50 chars AND kg_query empty → thin_diary wins (order)
  - `TestMissingKgFactsEvaluator_FiresWhenKgEmpty` — diary present + threshold-met, kg_query returns 0 → status='missing_kg_facts'
  - `TestMissingKgFactsEvaluator_DoesNotFireWhenKgPopulated` — kg_query returns 1+ triple → status falls through to `clean`

- `internal/spawn/pipeline_test.go` (extend):
  - `TestFinalizeResultGracePostsHonestCost` — mockclaude script that emits `result` AFTER finalize tool_result (mirrors how real claude orders these); assert `agent_instances.total_cost_usd` non-zero post-terminal
  - `TestFinalizeResultGraceTimesOutGracefully` — script that NEVER emits `result`; assert classification fires within `FinalizeResultGrace + 1s` and total_cost_usd remains NULL (no regression)

- `internal/config/config_test.go` (extend):
  - `TestM6EnvVarsParseDefaults` — all five new env vars unset → expected defaults
  - `TestM6EnvVarsParseOverrides` — all five set → parsed values

### Integration tests (Go, `//go:build integration`)

- `internal/throttle/integration_test.go` (new):
  - `TestSpawnDeferredOnBudgetExceeded` — testcontainers Postgres + supervisor; insert company with `daily_budget_usd=$1.00`; pre-load `agent_instances` rows summing $0.95; insert event_outbox row whose estimated cost is $0.10; observe row stays unprocessed; observe `throttle_events` row written with `kind='company_budget_exceeded'`; observe `pg_notify('work.throttle.event')` payload

  - `TestSpawnAllowedAfterBudgetWindowExpires` — same setup, but pre-loaded rows are 25h old; current spawn proceeds normally

  - `TestSpawnDeferredDuringRateLimitPause` — set `companies.pause_until = now + 60s`; insert event_outbox row; observe deferral; advance time (test-only `Now` injection); re-poll → spawn proceeds

- `integration_test.go::TestM6DecompositionEndToEnd` (extend existing M2.x test file):
  - Operator chat: send objective; CEO calls create_ticket 3x with parent_ticket_id; verify three tickets persist linked to a parent; verify `chat_mutation_audit` rows reference the parent in `affected_resource`

### Chaos tests (Go, `//go:build chaos`)

- `chaos_test.go::TestRateLimitPausesSpawnsForCompany`:
  - Real claude path with a mocked rate_limit_event injection (stream-json fixture inserts a `rate_limit_event` mid-stream with `status='rejected'`)
  - Assert `companies.pause_until` set within one event-loop tick
  - Assert subsequent event_outbox row for the same company defers
  - Assert event_outbox row for a DIFFERENT company proceeds

### Architecture amendment test

`dashboard/tests/architecture-amendment.test.ts` extension:

```ts
describe('ARCHITECTURE.md M6 amendment', () => {
  it('contains the M6 shipped status header', () => {
    expect(arch).toContain('**M6 — ');
    expect(arch).toContain('Shipped 2026-');  // post-ship date pin
    expect(arch).toContain('docs/retros/m6.md');
  });
  it('contains the parent_ticket_id schema reference', () => {
    expect(arch).toContain('parent_ticket_id');
  });
  it('contains the throttle_events table reference', () => {
    expect(arch).toContain('throttle_events');
  });
});
```

### Regression check

- All M2.1 / M2.2 / M2.3 / M3 / M4 / M5.1 / M5.2 / M5.3 / M5.4 tests pass unchanged.
- Specifically: the M2.1 chaos suite's `TestEndToEndTicketFlow` keeps working under the env-gated back-compat dispatch (M5.4 retro fix; not touched by M6).
- The cost-telemetry fix (US2) MUST NOT regress failure-mode cost recording. Existing tests that assert `total_cost_usd` on `finalize_never_called` / `budget_exceeded` / `claude_error` keep passing.

### [BRACKETED — only if frontend test scope is unblocked per J3]

- `dashboard/components/features/hygiene-table/HygieneTabStrip.test.tsx` — tab switching state
- `dashboard/components/features/hygiene-table/ThrottleEventsTable.test.tsx` — row rendering + chip tone mapping
- `dashboard/lib/sse/throttleStream.test.ts` — useThrottleStream behavior on connect / message / disconnect
- `dashboard/components/features/kanban/TicketCard.test.tsx` — parent chip render presence/absence on null/non-null parent_ticket_id

---

## Open questions remaining for /garrison-tasks

- **J1**: TerminalWriteGrace × FinalizeResultGrace interaction — confirm the math doesn't extend the M1 shutdown contract. Probably resolved by reading `internal/spawn/pgroup.go` carefully but flagged as a "test before commit" item.
- **J2**: Confirm `garrison_dashboard_app` is the correct role name in the migration GRANT line. Cross-check against `migrations/20260426000010_m3_dashboard_roles.sql` and `migrations/20260427000010_m4_supervisor_schema_extensions.sql` GRANT statements.
- **J3**: Frontend test scope — Go-only memory rule stays for M6, OR the kanban + hygiene UI changes warrant Vitest extensions. Operator confirms during /garrison-tasks.
- **J4**: M5.4 retro hasn't shipped yet (M5.4 is mid-CI). M6 plan references it as a binding input; if anything in the retro changes the substrate (e.g. discovers a hygiene quirk), M6 plan adapts before /garrison-tasks runs.
- **J5**: For the cost-telemetry fix's mockclaude fixture (`TestFinalizeResultGracePostsHonestCost`), confirm the existing mockclaude binary supports a script ordering directive — emit-result-AFTER-finalize. If not, the fixture extension lands as part of M6 (small).

---

## What this plan does not pre-decide

- Per-role cost-estimate refinement (closed by /speckit-clarify Q2 — flat default for M6).
- Multi-tenant company-admin UI for budgets (out of scope per spec).
- LLM-judged hygiene quality (separate research thread).
- Cross-department decomposition (parent + children must share department; out of scope per spec edge cases).
- Auto-merge of completed children to the parent (M8 territory).
- Per-thread context-token counter on chat (deferred from M5.4; not M6 scope).

---

## Spec-kit flow next

1. **`/garrison-tasks m6`** — break the plan into ordered tasks with task IDs, file paths, and verifications. Tasks consume this plan; they do not re-decide its choices.
2. **`/speckit.analyze`** — cross-artifact consistency between spec, plan, and tasks before code.
3. **Operator branches**: `git checkout -b 015-m6-decomposition-hygiene-throttle main` (after PR #17 — M5.4 — merges, and after the M6 spike + context branch `014-m6-m7-spikes` merges or rebases).
4. **`/garrison-implement m6`** — execute.
5. **Then**: M6 retro at `docs/retros/m6.md`, palace mirror, ARCHITECTURE.md amendment + test pin landed in the same PR. M7 starts from this substrate.
