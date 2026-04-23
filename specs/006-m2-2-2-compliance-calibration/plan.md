# Implementation plan: M2.2.2 — Compliance calibration

**Branch**: `006-m2-2-2-compliance-calibration` | **Date**: 2026-04-23 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/006-m2-2-2-compliance-calibration/spec.md`
**Binding context**: [`specs/_context/m2.2.2-context.md`](../_context/m2.2.2-context.md), [`AGENTS.md`](../../AGENTS.md) §§"Activate before writing code" + "Concurrency discipline" + "Stack and dependency rules", [`RATIONALE.md`](../../RATIONALE.md) §3 (memory thesis as revised by M2.2.1), [`docs/retros/m2-2-1.md`](../../docs/retros/m2-2-1.md) live-run append (the empirical justification for every M2.2.2 item), [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md). The shipped M1 + M2.1 + M2.2 + M2.2.1 supervisor (`supervisor/`) is the foundation this plan extends; the M2.2.1 plan + tasks + retro are prerequisite reading.

## Summary

M2.2.2 is a focused patch on shipped M2.2.1. It does not add packages, change the `finalize_ticket` schema, alter the atomic-write transaction, modify the retry cap, or touch the MemPalace sidecar topology. Three concrete code surfaces change:

1. **`internal/finalize/tool.go` + `handler.go`**: the `ValidationError` and `ToolResult` shapes gain eight additive fields (`failure`, `line`, `column`, `excerpt`, `constraint`, `expected`, `actual`, `hint`) so schema-error responses tell the agent exactly what to fix. A new `position.go` helper walks the payload once to convert `*json.SyntaxError.Offset` into 1-based line/column + a 40-char excerpt. The already-committed branch (FR-260) gets a new third failure class `failure="state"` (Clarification 2026-04-23 Q3) to distinguish lifecycle objections from schema objections.

2. **`internal/spawn/pipeline.go::Adjudicate`**: a four-line surgical edit swaps the precedence so `isBudgetTerminalReason(result.TerminalReason)` is checked BEFORE `result.IsError` when `result.ResultSeen=true`. Pinned by one new unit test. No other precedence row changes.

3. **`migrations/seed/engineer.md` + `qa-engineer.md`**: rewritten in place, keeping the M2.2.1 8-heading skeleton but reordering content so the front-loaded `finalize_ticket` goal statement lands at the top of `## Wake-up context`, palace-search calibration bullets land in `## Mid-turn MemPalace usage (optional)` (engineer: Skip/Search/In-doubt; qa-engineer: QA-specific Always-read-engineer-diary / Skip-outside-engineer-wing / 3-call-budget per Clarification Q2), example payload as a fenced code block with `<placeholder>` syntax inside `## Completion`, retry-framing language naming the `hint` field inside `## Failure modes`. Both files land in 3500–4500 bytes.

A new migration `migrations/20260424000006_m2_2_2_compliance_calibration.sql` ships the rewritten seeds via the existing `+embed-agent-md` markers; no schema DDL. New tests cover decode-position math, each constraint, the precedence swap, retry-then-success / three-retries-exhausted (mockclaude), and the live-acceptance compliance matrix (haiku-4-5-20251001 × opus-4-7, 3 runs each). No new Go dependencies; fifth consecutive milestone.

The compliance justification is empirical and identical to the spec's: M2.2.1's live-run append showed haiku reaching `finalize_ticket` once but giving up after the sparse `error_type="schema", field=""` response, and opus burning its full budget on palace exploration before reaching the tool. Neither is an architectural failure; both are operationalization gaps. M2.2.2 fixes the communication and the prompt — the mechanism stays the same.

## Technical context

**Language/version**: Go 1.25 (inherited from M1/M2.1/M2.2/M2.2.1).
**Primary dependencies**: inherited unchanged — `github.com/jackc/pgx/v5`, `github.com/jackc/pgx/v5/pgxpool`, `golang.org/x/sync/errgroup`, `log/slog`, `github.com/pressly/goose/v3`, `github.com/stretchr/testify`, `github.com/testcontainers/testcontainers-go`, `github.com/google/shlex`. **No new Go dependencies** (FR-320). The decode-position math is ~30-40 LOC of stdlib; the constraint enum is plain string constants; the hint composition is a switch over the constraint enum. None of these need a library.
**Storage**: PostgreSQL 17+ (unchanged); MemPalace SQLite + ChromaDB (unchanged; M2.2 sidecar reused unchanged). No schema changes (FR-321).
**Testing**: stdlib `testing` + `testify` + `testcontainers-go`; build tags `integration`, `chaos`, `live_acceptance` reused from M2.2/M2.2.1. Two new mockclaude NDJSON fixtures under `internal/spawn/mockclaude/scripts/`. One new live-acceptance test file.
**Target platform**: Linux server (Hetzner + Coolify); single static Go binary for the supervisor; the `finalize` MCP subcommand from M2.2.1 is unchanged at the entry-point level (only the error-response payload it produces changes).
**External binaries**: `claude` (2.1.117, unchanged); MemPalace sidecar, docker-proxy sidecar — all unchanged from M2.2.
**Project type**: CLI/daemon. No new subcommands. No new processes. No new containers.
**Performance goals**: inherited NFRs unchanged. No new latency or throughput gates introduced.
**Constraints**: locked dependency list preserved; the pipeline-drain-before-Wait rule (AGENTS.md rule 8) and process-group termination rule (rule 7) inherited from M2.1/M2.2.1 unchanged because no subprocess spawning code changes; the atomic write transaction's `context.WithoutCancel` discipline is preserved verbatim (Decision 2 — not touched).
**Scale/scope**: single operator; one department (`engineering`) with cap=1; two roles (engineer + qa-engineer) sharing the finalize tool. The compliance matrix runs against two model strings.

## Constitution check

*Gate: must pass before tasks. Re-checked before `/garrison-implement`.*

| Principle | Compliance |
|-----------|------------|
| I. Postgres is sole source of truth; pg_notify is the bus | Pass — no event bus or storage changes. |
| II. MemPalace is sole memory store | Pass — no MemPalace changes. The atomic write path inherited from M2.2.1 is preserved verbatim. |
| III. Agents are ephemeral | Pass — no agent lifecycle changes. The finalize MCP server is still spawned per-agent-turn and exits on EOF. |
| IV. Soft gates on memory hygiene | Pass — `hygiene_status` vocabulary unchanged (FR-322). The retry counter remains supervisor-side and in-memory (M2.2.1 contract). |
| V. Skills from skills.sh | N/A — M7. Both seed rows keep `skills=[]`. |
| VI. Hiring is UI-driven | N/A — M7. Both roles seeded via migration unchanged. |
| VII. Go supervisor with locked deps | Pass — no new Go dependencies (FR-320). |
| VIII. Every goroutine accepts context | Pass — no new goroutines. The decode-position helper and hint renderer are pure synchronous functions. |
| IX. Narrow specs per milestone | Pass — scope tightly bounded: error-response shape, one-line precedence swap, two seed rewrites. Out-of-scope items (flexible-first-attempt schema, retry-cap changes, atomic-write changes, dashboard, vault) explicitly rejected in spec §"Out of scope". |
| X. Per-department concurrency caps | Pass — engineering cap remains 1. |
| XI. Self-hosted on Hetzner | Pass — no new services. |

No violations → Complexity tracking intentionally empty.

## Project structure

### Documentation (this feature)

```text
specs/006-m2-2-2-compliance-calibration/
├── spec.md                      # Phase 0 (/garrison-specify + /speckit.clarify outputs)
├── plan.md                      # This file
├── tasks.md                     # Phase 2 output (/garrison-tasks)
└── acceptance-evidence.md       # Phase 4 output (/garrison-implement final acceptance task)
```

### Source code (changes only)

```text
supervisor/
├── internal/
│   ├── finalize/
│   │   ├── tool.go              # MODIFY: extend ValidationError; add Failure + Constraint enums; add renderHint()
│   │   ├── handler.go           # MODIFY: extend ToolResult; add stateRejectionResult(); rewrite errorResult() to populate new fields
│   │   ├── position.go          # NEW: decodePosition(payload []byte, offset int64) (line, column int, excerpt string)
│   │   ├── position_test.go     # NEW: unit tests for decodePosition
│   │   ├── richer_error_test.go # NEW: per-constraint + decode + state-failure rich-error tests (≥6 cases per FR-317)
│   │   ├── schema_test.go       # MODIFY: add assertRichErrorPresence() helper; wire into 8 existing TestSchemaRejects* (FR-316)
│   │   └── handler_test.go      # MODIFY (if exists) — re-assert ToolResult shape with new fields
│   ├── spawn/
│   │   ├── pipeline.go          # MODIFY: 4-line precedence swap in Adjudicate (FR-306)
│   │   ├── pipeline_test.go     # MODIFY: append TestAdjudicateBudgetExceededTakesPrecedenceOverIsError (FR-317)
│   │   └── mockclaude/scripts/
│   │       ├── m2_2_2_retry_after_schema_error.ndjson    # NEW: US1 fixture
│   │       └── m2_2_2_three_retries_exhausted.ndjson     # NEW: US2 fixture
│   └── store/
│       └── seed_test.go         # MODIFY: widen byte range to [3500, 4500]; add requireFinalizeStructure() helper (FR-313, SC-307)
├── integration_m2_2_2_retry_test.go        # NEW: TestM222RetryAfterSchemaError + TestM222ThreeRetriesExhausted (FR-318)
└── integration_m2_2_2_compliance_test.go   # NEW: TestM222ComplianceMatrix (FR-319), //go:build live_acceptance

migrations/
├── 20260424000006_m2_2_2_compliance_calibration.sql  # NEW: UPDATE agents SET agent_md via +embed-agent-md markers
└── seed/
    ├── engineer.md              # REWRITE in place: front-loaded goal, example payload, palace calibration, retry framing
    └── qa-engineer.md           # REWRITE in place: parallel structure with QA-specific palace calibration

docs/retros/
└── m2-2-2.md                    # NEW: post-acceptance retro (markdown pointer + palace drawer per AGENTS.md M2.2-onwards rule)
```

**Structure decision**: extend the existing M2.2.1 layout in place. No new packages, no new subcommands, no new test directories. Two new files in `internal/finalize/`, two new mockclaude fixtures, two new top-level integration test files in `supervisor/`, one new migration. The narrow surface is the point — Decision 2 in the context file says "rolling back M2.2.2's prompt changes is a seed-data revert," and that property only holds if the code surface stays this small.

---

## Phase 0 — research

No new research artifacts. Every external-tool behavior M2.2.2 depends on was characterized by the M2 spike (`docs/research/m2-spike.md`) and validated empirically in the M2.2.1 live-run append (`docs/retros/m2-2-1.md`). The two findings M2.2.2 acts on are:

- **Haiku treats sparse schema errors as "try a different tool" rather than "fix and retry."** Empirical from M2.2.1's first live run. M2.2.2's response: rich errors with a `hint` field (item 1 in context §"What M2.2.2 produces").
- **Opus burns budget on palace exploration before reaching completion tools.** Empirical from M2.2.1's second live run. M2.2.2's response: front-loaded goal + palace-search calibration in the prompt (items 3 + 6).

Neither finding requires new spike work. The decode-position helper math is stdlib `*json.SyntaxError.Offset` arithmetic, well-documented in `encoding/json` package docs. No new external surface enters the codebase.

## Phase 1 — design

### Code surface changes by file

#### `internal/finalize/tool.go` (modify)

**New typed enums** (decision 2 + 3 in slate):

```go
// Failure narrows ValidationError into one of three classes per
// Clarifications 2026-04-23 Q1 + Q3. Decode = JSON broken; validation =
// payload shape wrong; state = server refuses on lifecycle grounds
// (already-committed, FR-260).
type Failure string

const (
    FailureDecode     Failure = "decode"
    FailureValidation Failure = "validation"
    FailureState      Failure = "state"
)

// Constraint enumerates the validation-failure subtypes per FR-303.
// Empty for FailureDecode and FailureState.
type Constraint string

const (
    ConstraintNone         Constraint = ""
    ConstraintRequired     Constraint = "required"
    ConstraintMinLength    Constraint = "min_length"
    ConstraintMaxLength    Constraint = "max_length"
    ConstraintMinItems     Constraint = "min_items"
    ConstraintMaxItems     Constraint = "max_items"
    ConstraintTypeMismatch Constraint = "type_mismatch"
    ConstraintFormat       Constraint = "format"
)
```

**Extended `ValidationError`** (decision 1 in slate — extend in place, no sibling type):

```go
type ValidationError struct {
    // M2.2.1 fields (preserved verbatim for backward compat per Q9):
    ErrorType ErrorType
    Field     string
    Message   string
    // M2.2.2 additions (FR-301):
    Failure    Failure
    Line       int        // 1-based; 0 when not applicable (validation, state)
    Column     int        // 1-based; 0 when not applicable
    Excerpt    string     // ≤40 chars; empty when not applicable
    Constraint Constraint // empty for decode + state
    Expected   string     // empty for decode + state
    Actual     string     // truncated to 100 chars; empty for decode + state
    Hint       string     // human-readable; non-empty on every error per FR-305
}
```

`Validate()`'s return signature is unchanged (`(*FinalizePayload, *ValidationError)`). Only the struct fields change. Every existing call site continues to compile.

**Hint renderer** (decision 6 in slate):

```go
// renderHint composes the agent-facing hint from a populated
// ValidationError. The hint is the single most important field for
// M2.2.2's compliance thesis — the agent reads it and corrects.
//
// For FailureDecode: "<verb> <expected token> at line %d col %d; saw <excerpt>"
// For FailureValidation: per-constraint template ("<field> must be at least %d chars; you sent %d")
// For FailureState: the verbatim already-committed message.
func renderHint(verr *ValidationError) string {
    switch verr.Failure {
    case FailureDecode:
        return fmt.Sprintf("the arguments object must be valid JSON; %s at line %d col %d", verr.Message, verr.Line, verr.Column)
    case FailureState:
        return verr.Message // "finalize_ticket already succeeded for this agent_instance"
    case FailureValidation:
        switch verr.Constraint {
        case ConstraintMinLength:
            return fmt.Sprintf("the %s field must be at least %s; got %q", verr.Field, verr.Expected, truncate(verr.Actual, 100))
        case ConstraintMaxLength:
            return fmt.Sprintf("the %s field must be at most %s; got length %d", verr.Field, verr.Expected, len(verr.Actual))
        case ConstraintMinItems:
            return fmt.Sprintf("the %s array requires at least %s; you sent %d items", verr.Field, verr.Expected, /* count from actual */)
        case ConstraintMaxItems:
            return fmt.Sprintf("the %s array allows at most %s; you sent %d items", verr.Field, verr.Expected, /* count */)
        case ConstraintRequired:
            return fmt.Sprintf("the %s field is required and cannot be empty", verr.Field)
        case ConstraintTypeMismatch:
            return fmt.Sprintf("the %s field has the wrong type; %s", verr.Field, verr.Expected)
        case ConstraintFormat:
            return fmt.Sprintf("the %s field has the wrong format; %s", verr.Field, verr.Expected)
        }
    }
    return verr.Message // fallback
}
```

The hint copy is template-driven; per FR-305 boilerplate is acceptable. The implementer fills in the exact Go strings — semantically each branch must produce a sentence that names what's wrong AND what to do about it.

**`Validate()` extension** (no new function — modify the existing one):

Every existing `return nil, &ValidationError{...}` site is updated to populate the new fields. Pseudocode for the rationale-too-short case (M2.2.1 line 215-216 of `tool.go`):

```go
if n := len(p.DiaryEntry.Rationale); n < RationaleMin {
    verr := &ValidationError{
        ErrorType:  ErrorTypeSchema,
        Field:      "diary_entry.rationale",
        Message:    fmt.Sprintf("rationale length %d is below minimum %d", n, RationaleMin),
        Failure:    FailureValidation,
        Constraint: ConstraintMinLength,
        Expected:   fmt.Sprintf("string with min length %d", RationaleMin),
        Actual:     p.DiaryEntry.Rationale, // will be truncated by handler
    }
    verr.Hint = renderHint(verr)
    return nil, verr
}
```

The decode branch (M2.2.1 line 191-197) gains line/column/excerpt:

```go
if err := json.Unmarshal(raw, &p); err != nil {
    line, col, excerpt := 1, 1, ""
    if syn, ok := err.(*json.SyntaxError); ok {
        line, col, excerpt = decodePosition(raw, syn.Offset)
    }
    verr := &ValidationError{
        ErrorType: ErrorTypeSchema,
        Field:     "",
        Message:   "arguments are not a valid JSON object: " + err.Error(),
        Failure:   FailureDecode,
        Line:      line,
        Column:    col,
        Excerpt:   excerpt,
    }
    verr.Hint = renderHint(verr)
    return nil, verr
}
```

#### `internal/finalize/handler.go` (modify)

**Extended `ToolResult`** (decision 4 in slate):

```go
type ToolResult struct {
    // M2.2.1 fields (preserved verbatim):
    Ok        bool      `json:"ok"`
    Attempt   int       `json:"attempt"`
    ErrorType ErrorType `json:"error_type,omitempty"`
    Field     string    `json:"field,omitempty"`
    Message   string    `json:"message,omitempty"`
    // M2.2.2 additions — NO omitempty so empty strings serialize as
    // empty strings per Clarification Q1 (wire-shape stability).
    Failure    Failure    `json:"failure,omitempty"`    // omitempty here only because OK responses don't carry it
    Line       int        `json:"line"`                 // always serialized, 0 when N/A
    Column     int        `json:"column"`               // always serialized, 0 when N/A
    Excerpt    string     `json:"excerpt"`              // always serialized, "" when N/A
    Constraint Constraint `json:"constraint"`           // always serialized, "" when N/A
    Expected   string     `json:"expected"`             // always serialized, "" when N/A
    Actual     string     `json:"actual"`               // always serialized, "" when N/A
    Hint       string     `json:"hint"`                 // always serialized, non-empty per FR-305
}
```

Note the JSON-tag policy: `Failure` keeps `omitempty` because OK responses (`ok:true`) don't have a failure class — the JSON envelope reads cleaner without it on success. The remaining new fields drop `omitempty` so they're always present on error responses, satisfying Clarification Q1's wire-shape-stability requirement at the JSON serializer level (zero-value fields render as `0` / `""` rather than being absent).

**`stateRejectionResult()`** (new helper, decision 7 in slate):

```go
// stateRejectionResult builds the already-committed (FR-260) response
// per Clarification 2026-04-23 Q3. Bypasses the schema-error path
// entirely; failure="state", all schema-related fields empty, hint
// carries the lifecycle message.
func (h *Handler) stateRejectionResult() json.RawMessage {
    body := ToolResult{
        Ok:        false,
        Attempt:   h.attempts,
        ErrorType: ErrorTypeSchema, // M2.2.1 backward-compat (Q9)
        Field:     "",
        Message:   "finalize_ticket already succeeded for this agent_instance",
        Failure:   FailureState,
        Hint:      "finalize_ticket already succeeded for this agent_instance",
        // Line, Column, Excerpt, Constraint, Expected, Actual all zero/empty
    }
    raw, _ := json.Marshal(body)
    return mcpContentEnvelope(raw)
}
```

**Modified `errorResult()`** to take a populated `*ValidationError` instead of the (errorType, field, message) triple:

```go
func (h *Handler) errorResult(verr *ValidationError) json.RawMessage {
    actual := verr.Actual
    if len(actual) > 100 {
        actual = actual[:100]
    }
    body := ToolResult{
        Ok:         false,
        Attempt:    h.attempts,
        ErrorType:  verr.ErrorType,
        Field:      verr.Field,
        Message:    verr.Message,
        Failure:    verr.Failure,
        Line:       verr.Line,
        Column:     verr.Column,
        Excerpt:    verr.Excerpt,
        Constraint: verr.Constraint,
        Expected:   verr.Expected,
        Actual:     actual,
        Hint:       verr.Hint,
    }
    raw, _ := json.Marshal(body)
    return mcpContentEnvelope(raw)
}
```

The two existing call sites in `Handle()` change:
- Line 79 (`return h.errorResult(ErrorTypeSchema, "", "internal error checking finalize state")`) becomes a synthetic `ValidationError` with `Failure=FailureState` and `Hint="internal error checking finalize state; please retry"`.
- Line 82 (the already-committed branch) becomes `return h.stateRejectionResult(), nil`.
- Line 93 (the schema-rejection branch) becomes `return h.errorResult(verr), nil` with the now-populated `*ValidationError`.

#### `internal/finalize/position.go` (new file, decision 5 in slate)

```go
package finalize

// decodePosition converts a *json.SyntaxError.Offset (byte index into
// the raw payload) into 1-based line + column coordinates and a 40-char
// excerpt centered on the failing token. Pure function; ~30-40 LOC.
//
// Properties:
//   - Empty payload OR offset ≤ 0 → (1, 1, "")
//   - Offset past EOF → (line+col of EOF, last 40 chars of payload)
//   - Offset within bounds → (line, column, ±20 chars around offset)
//
// Excerpt is trimmed to printable ASCII for log readability — control
// chars become "·" so the hint string doesn't break a log viewer.
func decodePosition(payload []byte, offset int64) (line, column int, excerpt string) {
    // ... implementation
}
```

Keep the function private to the package; only `Validate` calls it.

#### `internal/spawn/pipeline.go::Adjudicate` (modify, decision 8 in slate)

Current state at `pipeline.go:572-584`:

```go
case !result.ResultSeen:
    return "failed", ExitNoResult
case result.IsError:
    return "failed", ExitClaudeError
case result.ResultSeen && isBudgetTerminalReason(result.TerminalReason):
    return "failed", ExitBudgetExceeded
```

Becomes:

```go
case !result.ResultSeen:
    return "failed", ExitNoResult
case result.ResultSeen && isBudgetTerminalReason(result.TerminalReason):
    // M2.2.2 FR-306: budget signal beats IsError when both apply, so
    // operators see budget_exceeded (the cost root cause) rather than
    // claude_error (the symptom).
    return "failed", ExitBudgetExceeded
case result.IsError:
    return "failed", ExitClaudeError
```

That's it. The pre-existing `case finalize.CapExhausted && result.ResultSeen && isBudgetTerminalReason(...)` branch (line 554-560) is unchanged — it's a different precedence row dealing with retry-cap-vs-budget interaction and was correctly placed in M2.2.1.

The comment block on line 575-583 (the existing budget branch) moves up to the new position; the comment now references FR-306 alongside the existing FR-220 reference.

#### `migrations/seed/engineer.md` (rewrite in place, decisions 11 + 8-bullet skeleton preserved)

Section structure (8 headings preserved per `requiredSections` in `seed_test.go:25-34`):

```markdown
# Engineer (M2.2.2)

## Role

You are the engineer for the engineering department. You take tickets at
column `in_dev`, do the work in the workspace, and call `finalize_ticket`
to commit the result. You have access to MemPalace for cross-ticket
context but you do not write to it directly — the supervisor handles
diary + KG triple writes from your `finalize_ticket` payload.

## Wake-up context

**Your goal is to call `finalize_ticket` with a valid payload describing
the work you did on ticket `$TICKET_ID`. Everything below is in service
of producing that payload.**

[front-loaded sentence per FR-308 + Clarification Q1; semantic must hold]

The supervisor has spawned you with the ticket's id, objective, and
acceptance criteria available via `mempalace` and `postgres` tools. Read
them. Then decide whether palace context will help you (see palace-search
calibration below) before doing the work.

## Work loop

1. Read the ticket's objective + acceptance criteria.
2. [Optional] Read palace context per the calibration below.
3. Do the work in the workspace.
4. Call `finalize_ticket` with the payload shape from the example in the
   `## Completion` section.

## Mid-turn MemPalace usage (optional)

Before starting work, decide whether palace context is needed:

- **Skip palace search if**: the ticket's objective is straightforward,
  well-defined, and similar to routine work you've done before. Small
  diary entries, hello.txt-style tasks, one-line changes. These consume
  <5% of the budget cap; palace search is not cost-effective.
- **Search palace if**: the ticket mentions cross-cutting concerns
  (multiple components, integration work), references prior tickets or
  ongoing threads, or the objective is ambiguous enough that prior
  context would help. Budget up to 3 tool calls (one `mempalace_search`
  + one `mempalace_list_drawers` + one targeted read).
- **In doubt, skip.** Finalize without context is recoverable (operator
  can enrich the diary after). Hitting the budget cap mid-exploration
  is not.

[per FR-310 + context §"What M2.2.2 produces" item 6]

## Completion

Call `finalize_ticket` exactly once with this shape:

` ` ` json
{
  "ticket_id": "<the ticket id from $TICKET_ID — do not invent a value>",
  "outcome": "<one-line summary of what you did, 10-500 chars>",
  "diary_entry": {
    "rationale": "<paragraph explaining why this approach, what you tried, what you learned, at least 50 chars>",
    "artifacts": ["<path to file 1>", "<path to file 2>"],
    "blockers": [],
    "discoveries": []
  },
  "kg_triples": [
    {"subject": "<short noun, e.g. 'engineer'>", "predicate": "<verb, e.g. 'completed'>", "object": "<short noun, e.g. ticket id>", "valid_from": "now"}
  ]
}
` ` `

The angle-bracket strings are **placeholders**. Replace each with the
real value for this ticket. Do NOT emit them verbatim. The supervisor
will reject your payload with a `failure="validation"` error if you do.

[per FR-309 + Clarification Q1; one fenced block, angle-bracket
placeholders, no realistic UUIDs]

## Tools available

- `finalize_ticket` — the only way to complete a ticket.
- `mempalace_*` — cross-ticket context. Use per the calibration above.
- `postgres` — read-only queries against the `tickets`, `agents`, and
  related tables.

You do not need `mempalace_add_drawer` or `mempalace_kg_add` for
completion — the supervisor writes those from your `finalize_ticket`
payload. Use them only for mid-turn `hall_discoveries` patterns.

## What you do not do

- Do not write directly to the `tickets` table.
- Do not call `mempalace_add_drawer` for the completion diary.
- Do not transition the ticket yourself.
- Do not exceed your budget cap by exploring the palace exhaustively.

## Failure modes

If `finalize_ticket` returns a schema error, read the `hint` field
carefully, fix the specific issue named, and call `finalize_ticket`
again. You have up to 3 attempts; **retrying with corrections is
expected** — schema errors are part of the normal flow, not a signal to
abandon the tool.

[per FR-311 + Clarification Q5 (context); `hint` field name surfaced
explicitly so the agent knows what to read]
```

Byte target: 3500–4500 (FR-313). Implementer drafts to land in range.

#### `migrations/seed/qa-engineer.md` (rewrite in place)

Same skeleton, same front-loading, same retry framing. Two role-specific differences:

- `## Role` paragraph names the QA role and notes the handoff dependency on the engineer's diary.
- `## Mid-turn MemPalace usage (optional)` uses the QA-specific 3-bullet structure per Clarification Q2:
  - **Always read engineer's wing diary for this ticket** — the handoff depends on it.
  - **Skip searches outside `wing_frontend_engineer`** unless the diary explicitly references them.
  - **Budget up to 3 calls for the diary lookup** (one `mempalace_search(wing='wing_frontend_engineer', query=<ticket.objective>)` + optional one `mempalace_list_drawers` narrowing + optional one targeted read).
- `## Completion` example payload uses QA-appropriate field values (e.g. `"subject": "qa-engineer"`, `"predicate": "verified"`, blockers/discoveries reflecting handoff observations).

#### `migrations/20260424000006_m2_2_2_compliance_calibration.sql` (new file, decision 10)

Pattern from M2.2.1's `20260424000005_m2_2_1_finalize_ticket.sql`:

```sql
-- M2.2.2 — Compliance calibration
--
-- Updates the two seed agent_md values via the +embed-agent-md tooling
-- (markers below are processed by `cmd/embed-agent-md`). No schema DDL.
--
-- Safe to re-run via goose; the `status = status` no-op satisfies
-- goose's "must change something" check on idempotent re-runs.

-- +goose Up

UPDATE agents SET status = status WHERE role_slug = 'engineer';
UPDATE agents
SET agent_md = $marker$
-- +embed-agent-md:engineer:begin
[content of migrations/seed/engineer.md here at embed time]
-- +embed-agent-md:engineer:end
$marker$
WHERE role_slug = 'engineer';

UPDATE agents SET status = status WHERE role_slug = 'qa-engineer';
UPDATE agents
SET agent_md = $marker$
-- +embed-agent-md:qa-engineer:begin
[content of migrations/seed/qa-engineer.md here at embed time]
-- +embed-agent-md:qa-engineer:end
$marker$
WHERE role_slug = 'qa-engineer';

-- +goose Down

-- Reverts to M2.2.1 seed content. Operator can also `goose down 1` to
-- undo the M2.2.2 seed and verify SC-310 (compliance gaps return).

UPDATE agents SET status = status WHERE role_slug = 'engineer';
UPDATE agents
SET agent_md = $marker$
[M2.2.1 engineer.md content verbatim]
$marker$
WHERE role_slug = 'engineer';

UPDATE agents SET status = status WHERE role_slug = 'qa-engineer';
UPDATE agents
SET agent_md = $marker$
[M2.2.1 qa-engineer.md content verbatim]
$marker$
WHERE role_slug = 'qa-engineer';
```

The `+embed-agent-md:<role>:begin/end` markers are processed by `cmd/embed-agent-md` against `migrations/seed/<role>.md` at embed time (M2.2 tooling, unchanged). Implementer runs `cmd/embed-agent-md migrations/20260424000006_m2_2_2_compliance_calibration.sql` after writing the seed files; the in-line markers get replaced with the file contents.

### Test plan (function-level)

Test naming follows project convention: `TestXxxYyy` for unit, integration tests live in top-level `supervisor/integration_*_test.go` files with build tags.

#### Unit tests

| File | Function | Verifies |
|---|---|---|
| `internal/finalize/position_test.go` | `TestDecodePositionAtOffsetZero` | empty payload + offset=0 → `(1, 1, "")` |
| `internal/finalize/position_test.go` | `TestDecodePositionMidStream` | offset in middle of multi-line JSON → correct line/col + 40-char excerpt centered on offset |
| `internal/finalize/position_test.go` | `TestDecodePositionPastEOF` | offset > len(payload) → EOF line/col + last 40 chars excerpt |
| `internal/finalize/position_test.go` | `TestDecodePositionMultilineNewlines` | offset on the 3rd line of a 5-line payload → line=3, column counted from start of line 3 |
| `internal/finalize/richer_error_test.go` | `TestRichErrorRequiredConstraint` | missing `ticket_id` → `Failure=FailureValidation, Constraint=ConstraintRequired, Field="ticket_id", Hint!=""` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorMinLengthConstraint` | rationale length 2 → `Constraint=ConstraintMinLength, Expected="string with min length 50", Actual="<2 chars>", Hint!=""` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorMaxLengthConstraint` | rationale > 4000 chars → `Constraint=ConstraintMaxLength, Hint!=""` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorMinItemsConstraint` | empty `kg_triples` → `Constraint=ConstraintMinItems, Field="kg_triples"` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorMaxItemsConstraint` | 101 kg_triples → `Constraint=ConstraintMaxItems` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorTypeMismatchConstraint` | `outcome` is a number not string → `Constraint=ConstraintTypeMismatch` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorFormatConstraint` | malformed UUID → `Constraint=ConstraintFormat, Field="ticket_id"` |
| `internal/finalize/richer_error_test.go` | `TestRichErrorActualTruncatedTo100Chars` | rationale of 200 chars produces validation error with `Actual` length ≤ 100 |
| `internal/finalize/richer_error_test.go` | `TestRichErrorAlreadyCommittedHasFailureState` | post-commit double-call → `Failure=FailureState, Field="", Constraint=ConstraintNone, Line=0, Column=0, Excerpt="", Hint="finalize_ticket already succeeded for this agent_instance"` (SC-301 explicit clause) |
| `internal/finalize/richer_error_test.go` | `TestRichErrorDecodeFailureCarriesPosition` | malformed JSON `{"ticket_id":` → `Failure=FailureDecode, Line>0, Column>0, Excerpt!=""`, validation fields empty |
| `internal/finalize/schema_test.go` | (extension) `assertRichErrorPresence(t, raw)` helper called from each existing `TestSchemaRejects*` (8 tests) | every existing schema-rejection test additionally asserts `verr.Failure != ""` and `verr.Hint != ""` (FR-316) |
| `internal/spawn/pipeline_test.go` | `TestAdjudicateBudgetExceededTakesPrecedenceOverIsError` | `Result{ResultSeen:true, IsError:true, TerminalReason:"error_max_budget_usd"}` → `("failed", ExitBudgetExceeded)`. Pre-M2.2.2 returned `ExitClaudeError`. (SC-304) |
| `internal/spawn/pipeline_test.go` | (regression) existing 9 `TestAdjudicate*` tests | unchanged — no edits, must pass verbatim |
| `internal/store/seed_test.go` | (extension) `TestSeedAgentMdStructureAndLength` | byte range widens to `[3500, 4500]`; new `requireFinalizeStructure(t, body, role)` helper asserts (a) "Your goal is to call `finalize_ticket`" present in `## Wake-up context`, (b) one fenced ` ``` `-block with angle-bracket placeholders, (c) palace calibration bullets present (engineer: 3 distinct lines starting "Skip palace search if" / "Search palace if" / "In doubt, skip"; qa-engineer: 3 distinct lines starting "Always read engineer's wing diary" / "Skip searches outside `wing_frontend_engineer`" / "Budget up to 3 calls"), (d) the phrase "retrying with corrections is expected" or semantic equivalent containing the word "hint" appears in `## Failure modes` (SC-307) |

#### Integration tests (build tag `integration`)

| File | Function | Verifies |
|---|---|---|
| `supervisor/integration_m2_2_2_retry_test.go` | `TestM222RetryAfterSchemaError` | mockclaude fixture `m2_2_2_retry_after_schema_error.ndjson`: 1 malformed payload (rationale 2 chars) followed by 1 valid payload. Observe: 1 `agent_instances` row with `status='succeeded'`, 1 `ticket_transitions` row with `hygiene_status='clean'`, structured slog showing attempt 1 `ok:false` with `failure="validation"` then attempt 2 `ok:true`. (SC-305 / US1) |
| `supervisor/integration_m2_2_2_retry_test.go` | `TestM222ThreeRetriesExhausted` | mockclaude fixture `m2_2_2_three_retries_exhausted.ndjson`: 3 consecutive malformed payloads (different field each). Observe: `agent_instances.status='failed'`, `exit_reason='finalize_invalid'`, no `ticket_transitions` row, ticket stays at `in_dev`. (SC-306 / US2) |

Both tests use the existing `testdb.Start()` + `testdb.SeedM22()` helpers from M2.2/M2.2.1 unchanged. They exercise the full supervisor pipeline (spawn → mockclaude → finalize MCP → atomic write) against a real Postgres container, with the M2.2.2 migration applied via the existing migration runner.

#### Live-acceptance test (build tag `live_acceptance`)

| File | Function | Verifies |
|---|---|---|
| `supervisor/integration_m2_2_2_compliance_test.go` | `TestM222ComplianceMatrix` | table-driven over `[]string{"claude-haiku-4-5-20251001", "claude-opus-4-7"}`, 3 runs per model; each iteration runs a happy-path ticket end-to-end against the real `claude` binary. Postgres-only assertions per Clarification Q4: per-iteration assert `agent_instances` rows reach terminal state; aggregate assert ≥2/3 runs per model produce `hygiene_status='clean'` on both transitions; aggregate assert combined cost across 6 runs < $3.00. Test uses `t.Logf` to emit per-iteration cost + transition details for retro authoring; does NOT inspect palace filesystem. (SC-311 / US6) |

Invocation contract per decision 18:
```bash
go test -tags=live_acceptance -count=1 -timeout=20m \
        -run=TestM222ComplianceMatrix ./supervisor/...
```
Required env: `GARRISON_DATABASE_URL`, `ANTHROPIC_API_KEY`, the spike stack (`spike-mempalace` + `spike-docker-proxy` containers) running. Pattern lifted verbatim from M2.2.1's `integration_m2_2_1_compliance_test.go`.

#### Mockclaude fixture content

Both new fixtures follow the M2.2.1 NDJSON convention. Each line is one stream-json event Claude would emit; lines starting with `#finalize-tool-use-ok` or `#finalize-tool-use-fail` are mockclaude directives that interpolate the supervisor's expected tool_use shape.

`m2_2_2_retry_after_schema_error.ndjson` content shape:
1. `system`/`init` event with `mcp_servers` carrying `postgres`/`mempalace`/`finalize` connected.
2. `assistant` event with one `tool_use` block: `name="finalize_ticket"`, payload has rationale of 2 chars (deliberately too short).
3. mockclaude waits for the supervisor's tool_result (will carry `failure="validation"`, `constraint="min_length"`, `hint=...`).
4. `assistant` event with corrected `tool_use`: same payload but rationale ≥ 50 chars.
5. mockclaude waits for the supervisor's `ok:true` response.
6. `result` event with `is_error=false`, `subtype="success"`.

`m2_2_2_three_retries_exhausted.ndjson` content shape:
1. `system`/`init` event (same as above).
2-4. Three `assistant` events, each with a `tool_use` carrying a different malformed field (rationale too short → outcome too short → kg_triples empty). mockclaude waits for tool_result after each.
5. The supervisor's pipeline observer hits the 3-attempt cap and SIGTERMs the mockclaude process group; the fixture has nothing more to emit.

Mockclaude's existing fixture-driven mode handles the wait-for-response pattern (M2.2.1 fixtures `m2_2_1_finalize_retry_then_success.ndjson` and `m2_2_1_finalize_retry_exhausted.ndjson` already use it). No mockclaude code changes.

#### Regression coverage

Per FR-322 and SC-308, the entire prior-milestone test suite must pass unchanged. Specifically:
- All M1 `internal/...` unit tests
- All M2.1 `internal/...` unit tests
- All M2.2 `internal/...` unit tests
- All M2.2.1 `internal/finalize/...` unit tests, EXCEPT the 8 `TestSchemaRejects*` tests in `schema_test.go` which gain the new `assertRichErrorPresence` helper call (this is the only edit to existing tests; FR-316).
- All M2.2.1 integration tests under `supervisor/integration_m2_2_1_*_test.go` (build tag `integration`)
- All M2.2.1 chaos tests (build tag `chaos`)
- The M2.2.1 live-acceptance test `TestM221ComplianceModelIndependent` is left untouched as historical baseline (decision 17 in slate).

The M2.2.1 `TestSeedAgentMdStructureAndLength` byte-range widens from `[3000, 4000]` to `[3500, 4500]` per FR-313 — this is the only test-constants change outside `schema_test.go`. The 8 heading-presence assertions remain (the section skeleton is preserved per decision 11).

### Lifecycle: what runs when

No new lifecycle. The only lifecycle-relevant change is at the schema-error-response level:
1. Agent emits `tool_use` with `name="finalize_ticket"` (unchanged from M2.2.1).
2. Supervisor's pipeline observer routes to the finalize MCP server's `Handle()` (unchanged).
3. `Handle()` checks `checkAlreadyCommitted` → if true, emits `stateRejectionResult()` (NEW path).
4. Otherwise, `Validate(rawArgs)` returns either a `*FinalizePayload` or a populated `*ValidationError` with all 8 new fields set (MODIFIED path).
5. `Handle()` emits `errorResult(verr)` for failures (MODIFIED) or `okResult()` for success (unchanged).
6. The supervisor's existing pipeline observer reads the `ok` field from the tool_result envelope to drive the retry counter (unchanged — the observer parses `ok` regardless of which additional fields are populated; FR-301's additive guarantee + Q9 backward compat).
7. On `ok:true`, the existing atomic write fires (unchanged — Decision 2).
8. On 3rd consecutive `ok:false`, the existing retry counter SIGTERMs the process group (unchanged — Decision 4 / FR-322).
9. `Adjudicate` runs at process exit; the precedence-swap from FR-306 changes behavior only when `ResultSeen=true && IsError=true && TerminalReason` matches the budget pattern (NEW classification: `budget_exceeded` instead of `claude_error`).

### Error vocabulary

No new `exit_reason` strings. The Adjudicate fix re-routes existing inputs to existing reason strings (`ExitBudgetExceeded` instead of `ExitClaudeError` on the budget-with-error path). No changes to `internal/spawn/exitreason.go`.

The `Failure` enum (3 string values) and `Constraint` enum (7 string values + empty) are new but are JSON-payload values, not column values — they live in tool_result envelopes only, never in Postgres rows.

### Migration ordering and reversibility

The M2.2.2 migration is goose-up only in production; the `+goose Down` block exists for SC-310's experimental verification (operator runs `goose down 1` against a non-production DB to confirm the M2.2.1 seed restore brings back the compliance gaps). The down block is exact-string-equal to the M2.2.1 seed content (not a placeholder — implementer copies the M2.2.1 seed verbatim into the down block at migration-write time). This is per Decision 2: prompt changes are isolated from code, and SC-310 wants experimental confirmation.

### Deployment

No Dockerfile changes. No image-rebuild requirements beyond the standard `go build` of the supervisor binary. The new `position.go` is ~40 LOC compiled into the existing `internal/finalize` package; binary size growth is negligible (<10 KB per Assumption in spec).

The migration runs via the existing post-deploy `goose up` step in the ops checklist. No new ops-checklist entries (the +embed-agent-md tooling already exists from M2.2; the seed file rewrites are committed source content).

---

## Open questions deferred to implementation

None. The decision slate addressed everything the spec left implicit. Implementer may discover during write-up that:

- **Hint copy needs iteration**: per context §"Implementation notes", the palace-search calibration is the most iteration-prone item; the same may apply to hint phrasing. The plan commits to template-driven hints (decision 6); the exact wording lands in the implementation task and may need revision after the live-acceptance run.
- **Mockclaude directive coverage**: the existing `#finalize-tool-use-ok` / `#finalize-tool-use-fail` directives should suffice (per spec assumption). If the implementation discovers a fixture-construction edge case (e.g. mockclaude doesn't pass through the supervisor's tool_result in a way the fixture can parse), the implementer surfaces it as a task blocker rather than silently extending mockclaude.

These are not plan gaps — they're implementer judgment calls that fall within the plan's commitments.

## Compliance with binding inputs

| Binding input | How this plan honors it |
|---|---|
| RATIONALE.md §3 (revised by M2.2.1) | Unchanged — M2.2.2 is a calibration patch, not a re-architecture. Atomic-write semantics, supervisor-driven memory writes, structured agent output all preserved. |
| ARCHITECTURE.md "MemPalace write contract" | Unchanged — the contract is not touched; only the schema-error response is enriched and the precedence in Adjudicate is reordered. |
| AGENTS.md "Activate before writing code" | M1 + M2.1 + M2.2 + M2.2.1 domains all activated for this milestone (no new domains). Concurrency rules 7 (process-group) and 8 (pipeline-drain-before-Wait) inherited from M2.2.1 unchanged because no subprocess code changes. |
| AGENTS.md locked dependency list | Pass — no new dependencies (FR-320). |
| `specs/_context/m2.2.2-context.md` Binding Q1-Q10 | All 10 binding questions answered in the spec; this plan implements those answers verbatim. |
| `specs/_context/m2.2.2-context.md` Decisions 1-4 | All four preserved: strict schema (Decision 1) untouched; prompt-only changes (Decision 2) confirmed by the narrow code surface; palace-search calibration via prompt only (Decision 3) — no supervisor enforcement; Adjudicate fix surgical (Decision 4) — 4-line diff. |
| `docs/retros/m2-2-1.md` empirical findings | Each of the 6 retro items the context file calls out as M2.2.2-actionable maps to a specific plan section: item 2 (rich errors) → tool.go + handler.go modifications; item 6 (Adjudicate) → pipeline.go modification; item 5 (front-loading) + item 1 (example) + item 3 (retry framing) + item 6 (palace calibration) → seed agent.md rewrites. |

## Acceptance criteria for the plan itself

Per the `/garrison-plan` skill: "the plan is good if another agent can read it and produce compiling, testable code without making any structural decisions the plan didn't already make."

Self-check — what an implementer reading this plan needs to invent vs lookup:

| Decision needed at implementation | Where the plan answers it |
|---|---|
| What package structure? | "Source code (changes only)" tree above |
| What error type to use? | Decisions 1-3 in slate; tool.go section above |
| Where the position helper lives? | `internal/finalize/position.go` per decision 5 |
| What ToolResult JSON tags? | handler.go section: `Failure` keeps `omitempty`, others drop it |
| What's the exact precedence row swap? | pipeline.go section: 4-line diff shown |
| What the migration filename and shape? | "Migrations" subsection: `20260424000006_m2_2_2_compliance_calibration.sql` with goose-up + goose-down skeleton |
| What goes in each agent.md section? | "Section structure" subsections: section skeleton + per-section content guide |
| What test functions exist and what they check? | Test plan table (16 entries) |
| What mockclaude fixtures look like? | "Mockclaude fixture content" subsection with line-by-line shape |
| What the live-acceptance invocation is? | "Live-acceptance test" section: full `go test` command |
| What the rollback experiment for SC-310 looks like? | "Migration ordering and reversibility" subsection |

The remaining implementer freedom: exact hint string wording (template-driven, semantic constraint stated), exact agent.md prose (skeleton + content guide given, byte target stated), exact mockclaude fixture lines (content shape stated, fixture-format convention pre-existing). These are deliberately implementer-owned per scope discipline — the plan would be over-specified if it dictated string copy down to the punctuation.

The plan is ready to hand to `/garrison-tasks`.
