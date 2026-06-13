# Implementation Plan: M11 — Action Broker (outbound external actions, gated by policy)

**Branch**: `023-m11-action-broker` | **Date**: 2026-06-12 | **Spec**: [spec.md](./spec.md)
**Input**: [spec.md](./spec.md) (approved), [m11-context.md](../_context/m11-context.md) (binding), ARCHITECTURE.md §M11, `docs/security/{vault,chat,agent-sandbox,hiring,ingress}-threat-model.md`

**Producing-agent note**: this plan was produced non-interactively (operator absent). The garrison-plan skill's review checkpoint normally waits for operator approval of a decision slate before the full draft; with no operator present, the slate is recorded inline (§Decision slate) and every decision is made, not deferred. Each decision is overridable at `/garrison-tasks` or implementation time. No genuinely blocking ambiguity was found — the spec resolves all ten context open questions plus five verify-phase clarifications; this plan resolves the structural/code decisions those leave to the plan.

## Summary

M11 ships the **action-broker framework** plus **GitHub public comment-back** as the first concrete action type riding it. The framework is four threads, all composed from in-production substrate:

1. **Thread 1** — a 12th sealed verb `request_external_action` joins the `Verbs` registry in `supervisor/internal/garrisonmutate`, callable by agents, that writes exactly one immutable `work.pending_actions` row and acts on nothing. Tier is assigned at write time by the policy table; the verb returns a typed queued/at-tier `Result`.
2. **Thread 2** — a tier policy keyed on action type classifies each type into `auto`/`notify`/`approve`/`human_only`, with a structural permanent-Approve floor (public-facing types cannot be `auto`/`notify`) and an `approve` default for unclassified types. The policy is **code constants** (a deploy-time map) plus a `pending_actions.tier` CHECK; no operator edit surface ships.
3. **Thread 3** — a new supervisor-side reactive `internal/actionbroker` dispatcher worker (the `mcpserverwork.Worker` shape — LISTEN on a `pg_notify` channel + `processed_at`-style poll fallback, `SELECT … FOR UPDATE` claim, terminal status transition) executes `auto`/`notify`/approved-`approve` actions with a vault-scoped GitHub PAT agents never see, posts the comment via stdlib `net/http`, and appends an immutable `work.pending_action_outcomes` row. `human_only` is never dispatched. No auto-retry; a failed call marks the row `failed`.
4. **Thread 4** — an Outbox dashboard surface (`/admin/outbox`) is the operator approval queue: pull-only Server Component listing `approve`-tier pending rows, with Approve / Reject / mark-as-done Server Actions anchored like M7's `approve_hire`.

A `docs/security/action-broker-threat-model.md` lands in git history **before** the first dispatcher-code commit (the M9 SC-007 / M10 FR-800 git-log discipline). The milestone adds one sealed verb, two `work`-schema tables, the tier policy, the dispatcher package, the Outbox surface, and a threat model — and changes no sealed surface (SC-009).

This plan invents no new mechanism: it reuses the sealed-verb registry (M5.3), the `Deps.AgentInstanceID` anchor (M8), the immutable-history table shape (`scheduled_task_runs` → `scheduled_tasks`, M9), the `FOR UPDATE SKIP LOCKED` exactly-once discipline (M1/M9/M10), the supervisor-side-worker-outside-the-agent-network shape (`mcpserverwork`, M8; ingress, M10), the vault scoped-credential discipline (M2.3), and the `ServerActionVerbs` approve-path precedent (M7/M9).

## Technical Context

**Language/Version**: Go 1.x (supervisor), single static binary (`CGO_ENABLED=0`). Dashboard is Next.js (Server Components + Server Actions); per the standing repo convention, **tests are Go-side only**.
**Primary Dependencies**: no new dependency. GitHub REST comment-create is stdlib `net/http` (FR-020; M10 spike F5: PAT auth needs only a bearer header). pgx/v5, sqlc, goose, testify, testcontainers-go, infisical-go-sdk — all already locked (AGENTS.md).
**Storage**: PostgreSQL. Two new tables in the existing `work` schema: `work.pending_actions`, `work.pending_action_outcomes`. New `pg_notify` channel `work.action.dispatch_requested`. The immutable-outcome shape mirrors M9 `scheduled_task_runs`.
**Testing**: Go unit tests (`testify`) + testcontainers-go integration + chaos tests (concurrent-claim, restart-mid-dispatch). Acceptance script git-log check for threat-model-precedes-code.
**Target Platform**: Linux server (supervisor errgroup; dispatcher is a new errgroup goroutine in `cmd/supervisor/main.go`).
**Project Type**: web-service (Go supervisor + Next.js dashboard), single repo.
**Constraints**: exactly-once dispatch under restart + race (no double-post); credential never in agent context; permanent-Approve floor structural, not config; egress allow-list stays Anthropic-only (FR-007). Coverage target ≥82% on new Go code (m11-context §Spec-kit flow step 7).
**Scale/Scope**: single-project (no `project_id` on the tier policy — parked for M13). One action type this ship.

## Decision slate

The garrison-plan skill requires every structural decision the context/spec leaves to the plan be made explicitly. The context §Open questions are resolved in the spec's Clarifications; the decisions **this plan** owns are the code/schema/lifecycle shapes the spec defers to "a plan decision". One line each; full treatment in the body.

| # | Decision | Resolution |
|---|---|---|
| D1 | New package vs. fold into existing | New `supervisor/internal/actionbroker` package owns the dispatcher worker; the verb handler lives in `garrisonmutate/verbs_actions.go` (per-domain-file convention). |
| D2 | `pending_actions` schema + namespace | New `work.pending_actions` table (per Q-A). Columns named in §Data model; status enum + tier enum as CHECKs. |
| D3 | Outcome-history table | New `work.pending_action_outcomes`, append-only, mirrors `scheduled_task_runs` → `scheduled_tasks`. Anchored to `agent_instance_id`. |
| D4 | Tier policy representation | Code constants — a `map[string]Tier` deploy-time table in `actionbroker/policy.go` — that is the single classification authority; the `pending_actions.tier` column carries the **resolved** tier with a CHECK. Not operator-editable in alpha (FR-009). |
| D5 | Permanent-Approve floor enforcement | Three layers: (a) the policy map's floor set forces `approve` regardless of any lower entry, with a `TestFloorCannotBeLowered` guard; (b) `Classify` ignores agent-supplied tier; (c) a partial CHECK on `pending_actions` rejects any row whose `action_type ∈ floor` and `tier ≠ 'approve'` (defence in depth at the DB). |
| D6 | Unclassified-type default | `Classify` returns `approve` for any action type absent from the map (FR-015). |
| D7 | Verb caller model | Agent-callers only (Q-D). `request_external_action` is added to `agentVerbNames()`; chat-CEO path is out of scope. Anchored on `Deps.AgentInstanceID`. |
| D8 | Verb registry placement | Joins `Verbs` (chat-callable registry) as the 12th entry, ReversibilityClass 3, `AffectedResourceType: "pending_action"`. Handler `verbs_actions.go`. |
| D9 | Dispatcher lifecycle | New errgroup goroutine `actionbroker.RunLoop` in `cmd/supervisor/main.go`, registered on the M1 dispatcher for `work.action.dispatch_requested` + a `processed_at`-style poll fallback for at-least-once. |
| D10 | Exactly-once claim | `SELECT … FOR UPDATE` (claim the row inside a tx) + status transition to a terminal state; a re-claim against a non-`pending`/non-`approved` status is a no-op. Matches `mcpserverwork`'s `FOR UPDATE NOWAIT` precedent, extended with the M9 status guard. |
| D11 | GitHub credential | Vault PAT at path `actions/GITHUB_PAT` (Q-E), fetched supervisor-side at dispatch time via the existing `vault.Client.Fetch` seam (a `VaultFetcher` interface like `mcpserverwork`'s). Sent `Authorization: token <PAT>`. Fail-closed if vault is unavailable (FR-023). |
| D12 | Failure posture | No auto-retry (FR-022). A recoverable 5xx/rate-limit/network failure records a `failed` outcome + marks the row `failed`; operator re-requests. |
| D13 | Outbox route + surface | `/admin/outbox` (avoids `event_outbox` collision). Pull-only Server Component, re-fetches on navigation/Server-Action completion (M7 pattern, not SSE; Q-C). |
| D14 | Approve / Reject / done verbs | Three new entries in `ServerActionVerbs` (Server-Action-only, both anchors NULL): `approve_action`, `reject_action`, `mark_action_done`. Registry + audit-CHECK alignment only; execution is dashboard-side (the M9 `serverActionRegistryOnlyHandler` precedent) **except** the status transition the dispatcher reacts to, which the Server Action writes drizzle-direct. |
| D15 | Migration version | `supervisor/cmd/supervisor/migrations/20260612000001_m11_action_broker.sql` — canonical embed location (the M10 pattern); `make copy-migrations` mirrors to `migrations/`. Advances past M10's `20260612000000` on the same date (M9 gotcha 5 / M7.1 collision lesson). Added to `supervisor/sqlc.yaml` schema list. |
| D16 | sqlc query file | `migrations/queries/m11_action_broker.sql`; generated into `supervisor/internal/store/m11_action_broker.sql.go`. This covers the Go-side queries (the supervisor and dispatcher). The Outbox page's `approve`-tier queue read, `human_only` prepared-action display, and `notify` post-hoc feed are **dashboard-side drizzle queries** in `dashboard/lib/queries/outbox.ts` — following the M9/M10 pattern where the dashboard layer uses drizzle directly and does not go through a Go HTTP endpoint. The sqlc-generated functions are the Go/supervisor path only. |
| D17 | Outcome / status vocabulary | Status enum: `pending`, `approved`, `rejected`, `executed`, `failed`, `done`. Outcome enum: `requested`, `approved`, `rejected`, `executed`, `failed`, `notified`, `done`, `skipped_human_only`. (`classified` is NOT in the enum — no handler writes it; the tier is set on the `pending_actions` row at write time, not as a separate outcome.) Exact strings in §Data model. |
| D18 | dispatch-requested notify emission | The verb (for `auto`/`notify`) and the approve Server Action (for `approve`) each emit `work.action.dispatch_requested` post-commit. The `pg_notify` **payload is the `pending_actions.id` UUID string** (matching the `mcpserverwork` precedent: `Handle(ctx, eventID pgtype.UUID)` looks up the specific row by that ID). The poll fallback passes a zero UUID; `Handle` falls back to querying for any unprocessed dispatchable row when the payload is zero (the M9/M10 at-least-once poll shape). |
| D19 | leak-scan coverage | The `internal/leakscan` `github_pat` pattern (`ghp_…`) already exists; no new pattern needed. The credential path is covered by the SC-005 leak-scan assertion test, not new analyzer code. |
| D20 | No agent-network / egress change | `supervisor/egress/squid.conf` is not touched (FR-007). A regression test asserts the dispatcher's HTTP client is constructed supervisor-side, never wired into any agent container env. |

## Constitution Check

*GATE: passes. M11 introduces no new constitutional decision; it extends RATIONALE §6's propose→approve→execute posture (context §Scope-mismatch #3).*

- **Principle I (Postgres source of truth, `pg_notify` bus)**: the dispatcher is reactive on `work.action.dispatch_requested` with a poll fallback; `pending_actions` is the source of truth; exactly-once via `FOR UPDATE` + terminal status. ✔
- **Principle III (agents ephemeral)**: the pending row carries everything needed to dispatch and audit; the requesting agent may be gone by dispatch time (Edge Cases). ✔
- **Principle VI (UI-driven approval, not git)**: Approve/Reject/done are dashboard Server Actions; no git PR. ✔
- **Mechanism-over-prompt (M2.2.1)**: tier lives in the policy table/code, never in prompts or the immutable preamble (FR-011). ✔
- **Sealed-surface discipline**: one sealed verb added the M5.3/M8/M9 way; threat model precedes code; no existing sealed surface mutated (SC-009). ✔
- **Locked-deps**: zero new dependencies; GitHub call is stdlib `net/http` (FR-020). ✔

No Complexity-Tracking entries required.

## Project Structure

### Documentation (this feature)

```text
specs/023-m11-action-broker/
├── spec.md          # approved (input)
├── plan.md          # this file
└── tasks.md         # /garrison-tasks output (not created here)
```

### Source Code (repository root)

```text
supervisor/
├── internal/
│   ├── garrisonmutate/
│   │   ├── verbs.go                 # +1 Verbs entry (12th), +1 handler var
│   │   ├── verbs_actions.go         # NEW — request_external_action handler
│   │   ├── verbs_actions_test.go    # NEW
│   │   ├── server_action_verbs.go   # +3 ServerActionVerbs entries
│   │   ├── tool.go                  # agentVerbNames() += request_external_action
│   │   └── verbs_test.go            # registry/disjoint/enumeration test updates
│   ├── actionbroker/                # NEW package — Thread 3 dispatcher + Thread 2 policy
│   │   ├── policy.go                # tier policy map + Classify + floor set
│   │   ├── policy_test.go
│   │   ├── dispatcher.go            # RunLoop + Handle (claim → dispatch → outcome)
│   │   ├── dispatcher_test.go
│   │   ├── github.go               # POST .../issues/{n}/comments via net/http
│   │   ├── github_test.go
│   │   ├── chaos_test.go           # concurrent-claim + restart-mid-dispatch
│   │   └── integration_test.go     # testcontainers end-to-end
│   └── store/
│       └── m11_action_broker.sql.go # GENERATED by sqlc
├── cmd/supervisor/main.go           # wire actionbroker.RunLoop into errgroup + dispatcher route
└── sqlc.yaml                        # + the new migration in schema list

supervisor/cmd/supervisor/migrations/
└── 20260612000001_m11_action_broker.sql   # NEW (canonical embed location; make copy-migrations mirrors to migrations/)

migrations/
└── queries/m11_action_broker.sql           # NEW (sqlc source)

dashboard/app/[locale]/(app)/
└── admin/outbox/                    # NEW — Outbox Server Component + Server Actions

docs/security/
├── action-broker-threat-model.md    # NEW — lands BEFORE dispatcher code
└── chat-threat-model.md             # amend §5 reversibility table (12th verb)
```

**Structure Decision**: web-service. The dispatcher and tier policy land in a new `internal/actionbroker` package (D1) — it owns external-action execution and is the package the squid-only-door invariant protects. The verb handler stays in `garrisonmutate` (the sealed-registry home). The Outbox is dashboard-side; its row shapes are covered by Go integration tests (D13, repo test convention).

---

## Phase 0 — research (no milestone-gating spike)

Per m11-context §"No spike required" and spec Q1: **no spike**. The GitHub comment-create path is well understood — the M10 spike (F5) already mapped the PAT/App/token landscape and established PAT-with-bearer-header as sufficient for alpha. The only empirical unknown the plan resolves directly:

- **GitHub REST comment-create contract** (FR-020): `POST https://api.github.com/repos/{owner}/{repo}/issues/{issue_number}/comments`, header `Authorization: token <PAT>`, `Accept: application/vnd.github+json`, body `{"body": "<comment text>"}`. Success is `201 Created` returning the comment JSON (id + html_url, recorded on the `executed` outcome). `403`/`429` with `Retry-After` and `5xx` are the recoverable-failure class (→ `failed`, no retry, D12). `404` (issue/repo gone) and `422` (validation) are non-recoverable → `failed` with the body recorded. This is the same `gitHubEnvelope`-style minimal-struct parse used in `ingress/github.go` — no new dependency (FR-020).
- **Idempotency note**: GitHub comment-create is **not** natively idempotent (no idempotency key). Exactly-once is enforced **Garrison-side** by the claim + terminal-status transition (D10) so the POST is attempted at most once per row; a re-claim never reaches the POST. This is why D12 forbids auto-retry — a retry of an in-doubt POST could double-post.

No `research.md` artifact is produced; the above is the complete Phase 0 record.

---

## Phase 1 — data model

### `work.pending_actions` (D2)

The immutable request row. One row per `request_external_action` call. Status transitions in place (the M9 task-vs-run split: the row's *status* advances, but the audit anchor and request fields are never rewritten; transition evidence is appended to the outcome table).

```sql
CREATE TABLE work.pending_actions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    action_type        TEXT NOT NULL,                 -- e.g. 'github_issue_comment'
    target             JSONB NOT NULL,                -- {owner, repo, issue_number} for github
    rendered_payload   TEXT NOT NULL,                 -- the comment body, prepared by the agent
    agent_instance_id  UUID NOT NULL REFERENCES agent_instances(id),  -- M8 anchor (FR-002)
    ticket_id          UUID NULL REFERENCES tickets(id),              -- serving ticket/context
    tier               TEXT NOT NULL CHECK (tier IN ('auto','notify','approve','human_only')),
    tier_reason        TEXT NOT NULL,                 -- FR-012 ("permanent-Approve floor")
    status             TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','approved','rejected','executed','failed','done')),
    approved_by        TEXT NULL,                     -- operator identity for approve-tier (FR-026)
    dispatched_at      TIMESTAMPTZ NULL,              -- set when the dispatcher claims+completes
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(rendered_payload) > 0),
    -- D5 layer (c): floor action types can never be stored below 'approve'
    CONSTRAINT pending_actions_floor_is_approve
        CHECK (action_type NOT IN ('github_issue_comment') OR tier = 'approve')
);

-- dispatcher claim index: rows the dispatcher should consider
CREATE INDEX idx_pending_actions_dispatchable
    ON work.pending_actions (created_at)
    WHERE status IN ('pending','approved') AND tier <> 'human_only';
```

Notes:
- `tier` is written by the verb from `Classify(action_type)` (D4/D6), never from agent input (FR-005/FR-013).
- The floor CHECK (D5c) is the DB backstop: even a hand-edited insert cannot store `github_issue_comment` at `auto`/`notify`. The floor action-type list in the CHECK is extended in lockstep with the policy map's floor set (a `TestFloorCheckMatchesPolicy` test asserts the two enumerations agree — no drift).
- `target` is JSONB so a second action type rides without a migration (Assumption: single action type, genuine table).
- Grants: `garrison_dashboard_app` gets `SELECT, UPDATE` (Outbox reads + the approve/reject/done status transition); `garrison_agent_ro` gets `SELECT` via the existing `work.*` grant (Q-A — no separate grant statement needed for read; the verb writes through the supervisor's pool, not the agent RO role).

### `work.pending_action_outcomes` (D3)

Append-only immutable history; mirrors `scheduled_task_runs` (M9). One row per transition/dispatch attempt. The request row is never mutated beyond `status`; everything reconstructable from here.

```sql
CREATE TABLE work.pending_action_outcomes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pending_action_id  UUID NOT NULL REFERENCES work.pending_actions(id),  -- no CASCADE
    agent_instance_id  UUID NOT NULL REFERENCES agent_instances(id),       -- originating anchor (FR-024)
    outcome            TEXT NOT NULL CHECK (outcome IN (
        'requested','approved','rejected',
        'executed','failed','notified','done','skipped_human_only')),
        -- note: 'classified' is NOT a valid outcome; tier is set on the pending_actions row, not as an outcome
    detail             TEXT NULL,            -- failure reason / operator note / external comment URL
    structured_outcome JSONB NULL,           -- e.g. {comment_id, comment_url} on 'executed'
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pending_action_outcomes_action
    ON work.pending_action_outcomes (pending_action_id, created_at);
```

Notes:
- No `ON DELETE CASCADE` (M9 immutability rule — history survives).
- `agent_instance_id` is the **originating** anchor copied from the pending row, so a reconstruction (SC-007) yields agent + payload + tier + approver + outcome without a live agent.
- Grants: `garrison_dashboard_app` gets `SELECT, INSERT` — `SELECT` for Outbox reads (history reconstruction / audit); `INSERT` because the mark-as-done and reject Server Actions write `done`/`rejected` outcome rows directly (see D14). The supervisor writes `executed`/`failed`/`requested` rows; both paths use the same grant.

### Migration ordering (D15)

`supervisor/cmd/supervisor/migrations/20260612000001_m11_action_broker.sql` — canonical embed location, same date as M10, next sequence (advances past `20260612000000`); `make copy-migrations` mirrors to `migrations/` (the M10 pattern — T002 uses this path). The migration also extends the `chat_mutation_audit` CHECKs **up front** (M8-retro lesson — land them in the migration, not at first integration-test failure):

- `verb` CHECK gains `request_external_action`, `approve_action`, `reject_action`, `mark_action_done`.
- `affected_resource_type` CHECK gains `pending_action`.
- `outcome` CHECK gains no new value (the verb's failure modes map to the existing `validation_failed`; the dispatcher's outcomes live in `pending_action_outcomes`, not `chat_mutation_audit`).

Down migration deletes M11-era `chat_mutation_audit` rows before reverting the CHECKs (the M9 down-migration shape), then drops the two tables (outcomes before pending — FK).

### sqlc queries (D16) — `migrations/queries/m11_action_broker.sql`

| Query (`:one`/`:many`/`:exec`) | Purpose |
|---|---|
| `InsertPendingAction` | the verb's single write (FR-003); returns the row id + tier for the Result. |
| `InsertPendingActionOutcome` | append one outcome row (FR-024). |
| `ClaimDispatchablePendingAction` | `SELECT … FOR UPDATE SKIP LOCKED` one row by id where `status IN ('pending'(auto/notify),'approved')` AND `tier <> 'human_only'` (D10) — the exactly-once claim. Note: allowing `status IN ('pending','approved')` for all non-human_only tiers means a theoretically impossible `auto`-tier row at `status='approved'` would be dispatched (there is no bug — `auto` rows are dispatched directly from `pending`, so an `approved` auto-tier row is an impossible state under normal operation; the claim permits it rather than guarding against it, which is intentional). |
| `MarkPendingActionExecuted` / `MarkPendingActionFailed` | terminal status transition + `dispatched_at`. |
| `GetPendingActionByID` | dispatcher row read inside the claim tx. |
| `ListPendingApproveActions` | Outbox queue read (approve-tier, status pending). |
| `ListPendingActionOutcomes` | history reconstruction (SC-007). |

Generated into `supervisor/internal/store/m11_action_broker.sql.go`; the new migration is appended to `supervisor/sqlc.yaml`'s schema list (D15).

---

## Phase 1 — interfaces and state machines

### Thread 1 — the verb (`garrisonmutate`)

`verbs.go` gains the 12th `Verbs` entry and a `handleRequestExternalAction HandlerFunc = stubHandler` var (the existing forwarding-closure + init-replacement pattern):

```go
{
    Name: "request_external_action",
    Handler: func(ctx, deps, args) (Result, error) { return handleRequestExternalAction(ctx, deps, args) },
    ReversibilityClass:   3,   // queues an effect on the outside world; Tier 3 per the threat-model amendment
    AffectedResourceType: "pending_action",
    Description:          "Request an external action (e.g. a GitHub issue comment). Writes a pending-action row gated by the tier policy; does NOT act. Public-facing actions are Approve-tier and require operator approval.",
}
```

`tool.go` `agentVerbNames()` becomes `[]string{"create_ticket", "request_external_action"}` (D7) — the agent-caller surface widens by one, which is itself a chat-threat-model Rule 1 amendment (recorded in the threat-model doc).

`verbs_actions.go` — `RequestExternalActionArgs{ ActionType, Target (json.RawMessage), Payload string, TicketID *string }`, and `realRequestExternalActionHandler`:

1. Reject if `!deps.AgentInstanceID.Valid` → `validationFailure("request_external_action: callable only by agents")` (D7 — agent-only; defence-in-depth, the reverse of M9's chat-only guard).
2. Parse + trim + presence-check `action_type`, `target`, `payload`.
3. `tier, reason := actionbroker.Classify(args.ActionType)` (D4/D5/D6) — **ignores any agent-supplied tier field** (FR-005); an agent-supplied `tier` JSON key is simply not in the struct.
4. In one tx: `InsertPendingAction` (status `pending`, the resolved tier) → `InsertPendingActionOutcome(outcome='requested')` → `WriteAudit` (chat_mutation_audit, agent-anchored, `affected_resource_type='pending_action'`, reversibility 3) → commit.
5. Post-commit: if `tier ∈ {auto, notify}`, emit `pg_notify('work.action.dispatch_requested', <id>::text)` where `<id>` is the newly-inserted `pending_actions.id` UUID (D18) so the dispatcher's `Handle(ctx, eventID)` receives the specific row's UUID and looks it up directly (the `mcpserverwork` precedent). For `approve`/`human_only`, emit nothing — the approve Server Action emits the same channel+UUID on operator click, and `human_only` rows are never dispatched.
6. Return `Result{Success:true, AffectedResourceID: id, AffectedResourceURL: "/admin/outbox", Message: "Action queued at the <tier> tier, pending <operator approval | execution | manual completion>. Nothing has reached the outside world yet."}` (FR-004 — never implies "done").

The verb never makes an external call (FR-008): it only writes the row.

### Thread 2 — the tier policy (`actionbroker/policy.go`)

```go
type Tier string
const ( TierAuto Tier = "auto"; TierNotify Tier = "notify"; TierApprove Tier = "approve"; TierHumanOnly Tier = "human_only" )

// floor is the permanent-Approve set: these action types are 'approve'
// no matter what policy says. Public-facing per ARCHITECTURE §M11.
var floor = map[string]struct{}{ "github_issue_comment": {} }

// policy is the deploy-time classification table (D4). Keyed on action type.
// Entries for floor types may be omitted (the floor forces approve) or present
// as 'approve'; an entry attempting a lower tier for a floor type is overridden.
var policy = map[string]Tier{ "github_issue_comment": TierApprove }

// Classify is the SINGLE authority (FR-011). Floor wins; unknown → approve (FR-015).
func Classify(actionType string) (Tier, string)
```

`Classify` logic (D5a/D6): if `actionType ∈ floor` → `(TierApprove, "permanent-Approve floor (public-facing)")`; else if present in `policy` → that tier with reason `"deploy-time tier policy"`; else `(TierApprove, "unclassified action type — safe-by-construction default")`. No path returns `auto`/`notify` for a floor type. `TestFloorCannotBeLowered` constructs a contrived `policy["github_issue_comment"] = TierAuto` and asserts `Classify` still returns `approve` (the floor map is consulted first) — SC-003. `TestFloorCheckMatchesPolicy` asserts the migration's floor CHECK list equals `floor`'s keys (D5c, no drift).

No operator edit surface ships (FR-009/D4); the map is a compile-time constant.

### Thread 3 — the dispatcher (`actionbroker/dispatcher.go`)

Shape: the `mcpserverwork.Worker` precedent — a reactive handler registered on the M1 dispatcher, plus a `processed_at`-style poll fallback for at-least-once, with `FOR UPDATE` claiming for exactly-once.

```go
const Channel = "work.action.dispatch_requested"   // dot-delimited; LISTEN double-quotes it

type VaultFetcher interface { Fetch(ctx, []vault.GrantRow) (map[string]vault.SecretValue, error) }
type GitHubPoster interface { PostComment(ctx, target Target, body, pat string) (commentURL string, err error) }

type Deps struct {
    Pool   *pgxpool.Pool
    Queries *store.Queries
    Vault  VaultFetcher          // fetches actions/GITHUB_PAT (D11)
    GitHub GitHubPoster          // github.go; tests inject a fake
    Logger *slog.Logger
    PATPath string               // "actions/GITHUB_PAT" (config-driven default)
    Now    func() time.Time      // test seam
}

func RunLoop(ctx, deps) error    // errgroup-managed; poll fallback ticker; returns nil on ctx.Done
func (w *Worker) Handle(ctx, eventID pgtype.UUID) error   // M1 events.Handler signature
```

**Per-row state machine** (`Handle`, the exactly-once heart — D10):

1. Begin tx. `ClaimDispatchablePendingAction(id)` = `SELECT … FOR UPDATE SKIP LOCKED` filtering `status IN ('pending'(auto/notify) OR 'approved') AND tier <> 'human_only'`. If no row (already claimed/terminal/human_only) → commit no-op (FR-021 second-claim is a no-op; this is the restart/replay guard — SC-006).
2. Resolve the dispatchable predicate by tier:
   - `auto` / `notify`: dispatchable from `status='pending'`.
   - `approve`: dispatchable only from `status='approved'` (the operator click already happened — FR-017; the dispatcher never executes a `pending` approve row — US4 #3).
   - `human_only`: **never reaches this point via the LISTEN path** — `ClaimDispatchablePendingAction` filters `tier <> 'human_only'` so no `human_only` row is ever claimed. The defence-in-depth code path `if tier==human_only → skipped_human_only outcome, no execute` (FR-017/US4 #4) is present for robustness but is dead code under normal operation. **Test note**: `TestHandleNeverExecutesHumanOnly` (T009) MUST inject the row directly into `Handle` bypassing the claim query (e.g. via a stub `ClaimDispatchablePendingAction` that returns the row) to exercise this path — calling `Handle` the normal way will return a no-op because the claim filter excludes the row.
3. Fetch the PAT: `deps.Vault.Fetch(ctx, [{EnvVarName:"GITHUB_PAT", SecretPath: deps.PATPath}])`. On error (vault unavailable) → `MarkPendingActionFailed` + `InsertPendingActionOutcome('failed', detail="vault unavailable")`, commit, **no execution** (FR-023, fail-closed — never an unscoped credential). `SecretValue.Zero()` is called after use (vaultlog discipline).
4. `commentURL, err := deps.GitHub.PostComment(ctx, target, payload, pat)` (Thread 3 external call, supervisor-side — FR-008/FR-019).
5. On success: `MarkPendingActionExecuted(id, dispatched_at=now)` + `InsertPendingActionOutcome('executed', structured_outcome={comment_url})` for `auto`/`approve`; for `notify` the same plus the Outbox feed item is the `notified` outcome (D17 — `notify` emits both `executed` and `notified`, or a single `executed` row that the Outbox renders as a post-hoc feed entry; chosen: single `executed` + `notified` outcome rows so US4 #2's "executes then surfaces post-hoc" is explicit). Commit.
6. On recoverable failure (5xx/429/network): `MarkPendingActionFailed` + `InsertPendingActionOutcome('failed', detail)`, commit. **No retry** (FR-022/D12). The row surfaces in the Outbox for operator re-request.

`RunLoop` runs the poll-fallback ticker: every interval, `SELECT id FROM work.pending_actions WHERE status IN ('pending','approved') AND tier <> 'human_only' AND dispatched_at IS NULL` and feed each through `Handle` (the `processed_at`-style at-least-once backstop — the M9/M10 poll shape; `dispatched_at IS NULL` is the `processed_at` analogue). LISTEN provides low latency; the poll guarantees delivery if a notify is missed.

Every dispatch attempt, vault-fetch failure, and (future) rate-cap hit logs via `slog` structured fields; the durable record is the outcome row (Q-B — no new `throttle_events` kind).

### Thread 3 — the GitHub poster (`actionbroker/github.go`)

`PostComment` builds the REST request (Phase 0 contract) with stdlib `net/http` (FR-020), an injected `*http.Client` (timeout-bounded), parses `201` → `html_url`, classifies `403`/`429`/`5xx` as recoverable (returns a typed `ErrRecoverable`) and `404`/`422` as terminal-failure. `Target` is `{Owner, Repo, IssueNumber}` unmarshalled from `pending_actions.target` JSONB. The PAT is passed as a parameter (never stored on the struct, never logged — vaultlog discipline applies to `vault.SecretValue`; the plaintext `string` PAT is used only for the header and not passed to any logger).

### Thread 4 — the Outbox (`dashboard/app/[locale]/(app)/admin/outbox/`) + Server-Action verbs

- **Server Component** `page.tsx`: pull-only (D13). Lists `ListPendingApproveActions` rows: action type, target (rendered repo#issue), full `rendered_payload`, requesting agent + serving ticket, tier + `tier_reason`. Separate sections render `human_only` prepared actions (with mark-as-done) and a `notify` post-hoc feed (rows whose outcomes include `notified`). Re-fetches on navigation / Server-Action completion (M7 hiring-queue pattern; no SSE, no new `pg_notify` for live-push — Q-C/FR-028).
- **Server Actions** (drizzle-direct, the M9 decision-11 shape): `approveAction(id)` → UPDATE `pending_actions SET status='approved', approved_by=<operator>` + INSERT outcome `approved` + emit `pg_notify('work.action.dispatch_requested', id::text)` (D18 — payload is the row UUID so `Handle` looks up the specific row) + a `chat_mutation_audit` row (`verb='approve_action'`, both anchors NULL, `affected_resource_type='pending_action'`). `rejectAction(id)` → `status='rejected'` + outcome `rejected` + audit `reject_action`; the dispatcher never claims a `rejected` row (FR-026/US5 #1). `markActionDone(id, note?)` → `status='done'` + outcome `done` (detail=note) + audit `mark_action_done` (FR-027/US5 #2) — for `human_only` rows the dispatcher never executes.
- **`ServerActionVerbs` registry** (D14): three new entries (`approve_action` RC1, `reject_action` RC1, `mark_action_done` RC1) with `serverActionRegistryOnlyHandler` handlers (no supervisor-side dispatch path; registry + audit-CHECK alignment only). `TestVerbsSlicesDisjoint` and `TestVerbsRegistryMatchesEnumeration` updated.

### Supervisor wiring (`cmd/supervisor/main.go`, D9/D20)

- Build `actionbroker.Worker` after vault + pool are ready (the `mcpserverwork` build-site precedent). Register `actionbroker.Channel → worker.Handle` in the dispatcher's `workerExtras` map alongside `mcpserverwork.Channel`.
- Add `g.Go(func() error { return actionbroker.RunLoop(gctx, abDeps) })` to the errgroup (the `schedule.RunLoop` precedent) for the poll fallback.
- The dispatcher's HTTP client and PAT path are constructed here, supervisor-side; **never** threaded into any agent container env (FR-007/SC-004). `supervisor/egress/squid.conf` is untouched (D20).

---

## Phase 1 — threat model (precedes code)

`docs/security/action-broker-threat-model.md` is committed **before the first `internal/actionbroker` commit** (FR-030/SC-008; the M9 SC-007 / M10 FR-800 git-log discipline — the acceptance script asserts the threat-model commit's timestamp precedes the first dispatcher-code commit). It covers (FR-030 minimum set):

- **Agent-cannot-act-directly invariant**: the egress allow-list (squid, Anthropic-only) + supervisor-side dispatcher is the structural enforcement; the broker is the only door.
- **Tier-bypass / tier-downgrade**: tier is the policy-table lookup keyed on action type, never agent-supplied; the floor is structural (`Classify` floor-first + the DB CHECK).
- **Credential isolation**: the PAT is vault-fetched supervisor-side; vault Rule 1 extended to outbound action credentials; never in any agent env/prompt/context.
- **Approval-queue integrity**: approve is an operator Server Action anchored like `approve_hire`; an attacker-controlled agent cannot auto-approve its own pending action (it has no Server-Action surface).
- **Permanent-Approve floor as a structural property**: public-facing types are `approve` by construction, not current config; cannot be reclassified down.
- **Blast radius of a misbehaving dispatcher**: it executes approved/auto/notify rows only; it cannot self-approve, cannot mutate the policy (compile-time constant), cannot reach the vault beyond `actions/GITHUB_PAT`.
- **Idempotency of dispatch**: exactly-once across restart via `FOR UPDATE` claim + terminal status (no double-post).

The same pass amends `chat-threat-model.md` §5 (the 12th verb's reversibility class — Tier 3, "queues an attacker-influenceable effect on the outside world; an executed external action does not reverse") and records the `agentVerbNames` widening (D7) as a Rule-1 amendment. **`AGENTS.md` is updated in the same T001 commit**: (a) the binding-documents table's "Active milestone context" row advances to M11 and the "Current milestone: M5.3" line advances to M11; (b) the sealed-surfaces list in the Scope discipline section gains a note for the new outbound surface (`request_external_action` sealed verb, the `work.pending_actions` + `work.pending_action_outcomes` tables, and the dispatcher) so future milestone authors know what M11 sealed.

---

## Phase 1 — test strategy (function-level)

Go-side only (repo convention). Coverage target ≥82% on new Go code.

### Thread 1 — verb (`garrisonmutate/verbs_actions_test.go`)

- `TestRequestExternalActionWritesExactlyOnePendingRow` — agent caller, `github_issue_comment`; asserts one `work.pending_actions` row, anchored on the caller's `agent_instance_id`, status `pending`, tier `approve` (from the policy table), one `requested` outcome row, one `chat_mutation_audit` row; **no** external call (verb has no HTTP client) — SC-001 / US1 #1.
- `TestRequestExternalActionReturnsQueuedResult` — asserts the `Result` says "queued at the approve tier", never implies "done" — FR-004 / US1 #2.
- `TestRequestExternalActionIgnoresAgentSuppliedTier` — args JSON carries a stray `"tier":"auto"`; asserts the stored tier is `approve` (the struct has no tier field; the policy table decides) — FR-005 / US1 #4 / US3 #3.
- `TestRequestExternalActionRejectsChatCaller` — `AgentInstanceID` invalid → `validation_failed` "callable only by agents" — D7 / Q-D.
- `TestRequestExternalActionAutoTierEmitsDispatchNotify` — a hypothetical `auto` type emits `work.action.dispatch_requested`; an `approve` type does not — D18.

### Thread 2 — policy (`actionbroker/policy_test.go`)

- `TestClassifyFloorIsApprove` — `Classify("github_issue_comment") == approve` with the floor reason — US3 #1 / FR-014.
- `TestFloorCannotBeLowered` — contrived `policy["github_issue_comment"]=auto`; `Classify` still returns `approve` — SC-003 / US3 #2.
- `TestClassifyUnknownDefaultsApprove` — `Classify("never_registered") == approve` — FR-015 / US3 #4.
- `TestFloorCheckMatchesPolicy` — the migration floor-CHECK action-type list equals `floor`'s keys (no drift) — D5c.

### Thread 3 — dispatcher (`actionbroker/dispatcher_test.go`, `github_test.go`)

- `TestHandleApprovedActionPostsAndRecordsExecuted` — an `approved` row; fake `GitHub.PostComment` returns a URL; asserts status→`executed`, `dispatched_at` set, one `executed` outcome with `structured_outcome.comment_url`, fake called exactly once — US2 #2 / SC-007.
- `TestHandleNeverExecutesPendingApprove` — an `approve` row still at `status='pending'`; `Handle` claims nothing (predicate requires `approved`), no `PostComment` call — US4 #3.
- `TestHandleNeverExecutesHumanOnly` — `human_only` row; no `PostComment`, no terminal transition by the dispatcher — US4 #4 / FR-017.
- `TestHandleAutoTierExecutesWithoutGate` — `auto` row at `pending`; executes + `executed` outcome, no approval gate — US4 #1.
- `TestHandleNotifyTierExecutesThenNotifies` — `notify` row; executes + both `executed` and `notified` outcomes (post-hoc feed) — US4 #2 / FR-028.
- `TestHandleVaultUnavailableFailsClosed` — `Vault.Fetch` errors; asserts status→`failed`, `failed` outcome "vault unavailable", **no** `PostComment` call — FR-023 / SC-005 (no unscoped fallback).
- `TestHandleRecoverableFailureMarksFailedNoRetry` — `PostComment` returns `ErrRecoverable`; asserts one `failed` outcome, status `failed`, `PostComment` called exactly once (no retry) — FR-022 / Edge Cases.
- `TestPostCommentBuildsCorrectRequest` (github_test.go, httptest server) — asserts URL, `Authorization: token <PAT>`, `Accept`, JSON body; `201`→URL; `404`/`422`→terminal; `429`/`5xx`→`ErrRecoverable` — FR-020.
- `TestPostCommentNeverLogsPAT` — captures the slog output during a PostComment call and asserts the PAT string never appears — SC-005.

### Chaos (`actionbroker/chaos_test.go`)

- `TestConcurrentClaimDispatchesExactlyOnce` — N goroutines call `Handle` on the same `approved` row concurrently; the `FOR UPDATE SKIP LOCKED` claim yields exactly one `PostComment` call and one `executed` outcome — SC-006 / US2 #3.
- `TestRestartMidDispatchNoDoublePost` — simulate a crash after claim+POST but before commit (the tx rolls back, row stays `approved`), then re-run `Handle`; assert the POST happens at most twice in the test harness but the **terminal-status guard** means a committed `executed` row makes the second claim a no-op. (Test design: the fake `GitHub` counts calls; the assertion is exactly-once *given a committed executed status*, and a documented at-most-once-extra window only when the first attempt's commit was lost — the standard exactly-once-on-success contract M9/M10 ship.) — SC-006 / Edge Cases (supervisor restart mid-dispatch).

### Integration (`actionbroker/integration_test.go`, testcontainers)

- `TestEndToEndApproveTierGitHubCommentBack` — full path against a real Postgres + an httptest GitHub stand-in: agent calls the verb → one pending row, tier `approve` → Outbox query lists it → simulate the approve Server Action (UPDATE→`approved` + notify) → dispatcher claims, posts, records `executed` → reconstruct the history (agent_instance_id, payload, tier, approving operator, outcome) — US1+US2 end-to-end / SC-001/SC-007.
- `TestRejectNeverDispatches` — approve-tier row, simulate reject → status `rejected`, dispatcher claims nothing — US5 #1.
- `TestHumanOnlyMarkDone` — `human_only` row, simulate mark-as-done with a note → `done` status + `done` outcome with the note, dispatcher never executed — US5 #2.

### Regression / sealed-surface (`garrisonmutate/verbs_test.go` updates)

- `TestVerbsRegistryMatchesEnumeration` — updated to expect the 12th verb — FR-001.
- `TestVerbsSlicesDisjoint` — the three new Server-Action verbs are disjoint from `Verbs` — D14.
- Existing M1–M10 tests run unchanged (SC-009; the sealed-surface diff is additive only).
- Acceptance-script git-log check: `action-broker-threat-model.md` commit precedes the first `internal/actionbroker` commit — SC-008.

---

## Phase 1 — deployment changes

- **Migration**: `supervisor/cmd/supervisor/migrations/20260612000001_m11_action_broker.sql` (canonical embed location) added to the goose set and to `supervisor/sqlc.yaml`'s schema list (D15); `make copy-migrations` mirrors to `migrations/`. `make`/`just` migrate targets pick it up automatically (no target change).
- **Config / env**: a new env var for the PAT vault path, defaulting to `actions/GITHUB_PAT` (D11) — `GARRISON_ACTION_GITHUB_PAT_PATH`. The dispatcher poll interval reuses an existing interval default pattern (a `GARRISON_ACTION_POLL_INTERVAL`, default mirroring the M9/M10 poll cadence). Both land in the typed config struct (no `viper` — AGENTS.md).
- **No Dockerfile change**: zero new dependency; stdlib `net/http`. The dispatcher is supervisor-side, inside the existing binary.
- **No squid / network change** (D20/FR-007).
- **Vault**: the operator provisions `actions/GITHUB_PAT` in Infisical before enabling the dispatcher; absent it, the dispatcher fails-closed per-row (FR-023) rather than at boot (a missing PAT is a per-action failure, not a startup abort — the verb still queues rows; only dispatch fails until the secret exists).

---

## Open questions for the plan

None blocking. Two minor judgment calls, made and noted:

- **`notify`-tier outcome rows (D17/§Thread 3 step 5)**: chosen to emit both an `executed` and a `notified` outcome row so US4 #2's "executes then surfaces post-hoc" is explicit in the history, rather than a single `executed` row the Outbox reinterprets. Overridable at tasks time; the table shape supports either.
- **Restart-mid-dispatch test contract (chaos)**: the exactly-once guarantee is "exactly-once on a committed success; at-most-once-extra only in the lost-commit window," matching the M9/M10 ships. This is the strongest contract achievable without a natively-idempotent GitHub endpoint (Phase 0); it is why auto-retry is forbidden (D12). Documented, not deferred.

No new Go dependency is proposed; if the GitHub comment-create path turns out to need probing at implementation, that is the narrow action-type spike the spec's Q1 names — not a re-plan.
