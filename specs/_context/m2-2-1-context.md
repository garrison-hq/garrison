# M2.2.1 — Structured completion via `finalize_ticket` tool

**Binding context for `/garrison-specify m2-2-1`, `/garrison-plan m2-2-1`, `/garrison-tasks m2-2-1`, `/garrison-implement m2-2-1`.**

This is a **patch milestone** on top of shipped M2.2, not a new capability milestone. Its purpose is narrow: fix the compliance failure mode surfaced in the M2.2 live-run append (haiku-4-5 skipping `mempalace_add_drawer` and `mempalace_kg_add` despite MANDATORY marking in agent.md). The fix is architectural but deliberately scoped — it introduces ONE new in-tree MCP server, adjusts the seed agent.md files, and redefines `hygiene_status` semantics. Nothing else changes.

**Last updated**: 2026-04-23.

---

## Why this milestone exists

The M2.2 retro documented an agent-compliance finding: real claude haiku-4-5-20251001 executed the ticket work but did NOT call the mandatory MemPalace write tools, leaving the palace empty and the memory thesis untested in practice. RATIONALE §5's soft-gate design correctly flagged the gap (hygiene_status would have been `'missing_diary'`), but soft gates are an observability mechanism, not a compliance mechanism.

The operator's decision, after examining four prompt-engineering hypotheses (model strength, prompt structure, user-turn reinforcement, tool-order nudging), was to solve compliance architecturally rather than probabilistically: **make the agent's final structured output the mechanism of completion.** An agent that cannot exit without calling `finalize_ticket` with a valid structured payload cannot fail to write memory.

This is a deliberate revision to RATIONALE §3. The new formulation:

> Agents emit structured reflection via `finalize_ticket`. The supervisor writes to MemPalace from the received payload. Mid-turn writes (hall_discoveries, ad-hoc kg_add) remain agent-driven via the MemPalace MCP tools. The mandatory completion bundle is supervisor-written; reflection is agent-produced.

This preserves reflection (the agent still composes the diary and triples) while making compliance near-certain (the tool call is the only completion path). The reasoning for this formulation is in the session record preceding this context file; the short version is: "reflection preserved, compliance enforced, mid-turn flexibility retained."

---

## Binding inputs

1. `RATIONALE.md` — especially §3 (memory thesis) and §5 (soft gates). M2.2.1 revises §3; the revision is explicit and documented.
2. `ARCHITECTURE.md` — "MemPalace write contract" section. M2.2.1 modifies this section.
3. `AGENTS.md` — repository rules, especially concurrency discipline and the locked dependency list. M2.2.1 adds no new external dependencies; everything is stdlib + existing locked deps.
4. `docs/retros/m2-2.md` — the shipped M2.2 retro, specifically the live-run append's compliance finding. This is the empirical justification for M2.2.1's existence.
5. `docs/research/m2-spike.md` Part 2 and M2.2's `research.md` (T001 pre-impl-spike findings) — for MemPalace's tool inventory and response shapes. Binding for how the supervisor's palace-writer interacts with MemPalace 3.3.2.
6. The M2.2 codebase, specifically:
   - `internal/pgmcp` — the reference implementation for in-tree MCP servers
   - `internal/mempalace` — the palace client package M2.2.1 extends (supervisor-side writes reuse its Client type)
   - `internal/hygiene` — the evaluator logic that is substantially rewritten for M2.2.1
   - `internal/spawn/spawn.go` — the terminal-write flow that M2.2.1 modifies
   - `migrations/seed/engineer.md` and `migrations/seed/qa-engineer.md` — updated for M2.2.1

---

## What M2.2.1 produces

1. **A new in-tree MCP server at `internal/finalize`** (following the `internal/pgmcp` pattern ~150-250 LOC plus tests). Exposes a single tool `finalize_ticket` with a strict JSON schema. The tool is invoked as `supervisor mcp finalize` subcommand, wired into each agent's per-invocation MCP config alongside `postgres` and `mempalace`.

2. **Schema-enforced completion payload.** The tool's `input_schema` requires `ticket_id`, `outcome`, `diary_entry` (with structured sub-fields), and `kg_triples` (array of structured triples). Loose or thin payloads are rejected at schema level; the agent must retry with a valid payload.

3. **Supervisor-side memory writes.** When `finalize_ticket` is called successfully, the supervisor's finalize handler (not the agent) writes the diary to MemPalace via `internal/mempalace`, writes the KG triples, and writes the ticket transition atomically. The agent never touches the `tickets` table directly in this flow.

4. **3-attempt retry semantics.** The `finalize_ticket` tool returns detailed validation errors on failure. The agent can call again with corrections. After 3 attempts, the agent_instance fails with `exit_reason='finalize_invalid'` (new canonical value).

5. **Redefined `hygiene_status` semantics.** Values become:
   - `'clean'` — finalize succeeded, diary + triples written atomically
   - `'finalize_failed'` — agent exhausted 3 attempts, palace writes never happened
   - `'finalize_partial'` — finalize validation succeeded but a palace write errored mid-transaction (rare)
   - `'stuck'` — finalize never called at all (agent ran to turn limit without reaching completion)
   - `'pending'` — row written but evaluation not yet complete (transient)
   - Legacy values (`'missing_diary'`, `'missing_kg'`, `'thin'`) remain for M2.2-era historical rows; no new rows use them
   
   The hygiene checker goroutine is substantially simplified: it reads the finalize outcome from `agent_instances` and writes the matching `hygiene_status`. It no longer queries MemPalace to verify writes, because writes are supervisor-guaranteed when finalize succeeds.

6. **Updated seed agent.md files.** `migrations/seed/engineer.md` and `migrations/seed/qa-engineer.md` are rewritten to remove the manual `mempalace_add_drawer` / `mempalace_kg_add` protocol from the completion flow. Mid-turn use of those tools for `hall_discoveries` is preserved and explained. The completion protocol becomes: "your last tool call before exit is `finalize_ticket` with the full payload. Nothing else transitions the ticket."

7. **Dockerfile update** — the supervisor Dockerfile gains no new installs (finalize is an in-tree subcommand); the mempalace container is unchanged. No version bumps to MemPalace or Claude Code.

8. **End-to-end acceptance criterion**: a single ticket routed through engineer + qa-engineer produces a clean finalize on each agent's exit, a populated palace (both diaries + triples from both agents), two transitioned tickets, and `hygiene_status='clean'` on both `ticket_transitions` rows. Running the same scenario with haiku-4-5 produces identical results to running it with Opus 4.7 — the compliance problem is no longer model-dependent.

---

## What M2.2.1 does not produce

Scope boundaries. Violations are scope creep:

- **No changes to the MemPalace container, its version, or its API.** M2.2.1 is supervisor-side changes only.
- **No dashboard work.** M3 concern.
- **No secret vault integration.** M2.3 concern.
- **No CEO chat, hiring, agent-spawned tickets.** Later milestones.
- **No changes to the M2.2 event bus contract or pg_notify channels.** Same channels, same payloads.
- **No quality assessment of diary content.** M2.2.1 enforces structural completeness (required fields present, types correct, non-empty strings); subjective quality (is the rationale meaningful? are KG triples useful?) is an operator-review concern, not a finalize-layer concern. Explicit non-goal.
- **No retroactive migration of M2.2-era ticket_transitions rows.** Historical rows keep their legacy hygiene_status values; new rows use new values. Two vocabularies coexist in the column during the transition window.
- **No changes to the `mempalace` MCP tool wiring.** Agents still have it available for mid-turn writes. The compliance fix is completion-only.
- **No changes to `internal/pgmcp` or the Postgres MCP server.** Independent and unchanged.

If you find yourself implementing any of these, stop and surface it as an M2.2.1 scope violation.

---

## Architectural decisions (binding)

### Decision 1: Completion semantics

**The agent calls `finalize_ticket`. The supervisor writes diary + KG triples + transition atomically. The agent never writes to `tickets`.**

Rejected alternatives:
- Agent calls finalize then transitions separately — preserves M2.2's compliance failure mode (agent can forget)
- Tool writes diary and transition from its own receiver — couples MCP server with transaction semantics that belong to the supervisor

Rationale: full separation of concerns. The agent produces structured reflection; the supervisor commits it as a single transactional unit. If the finalize tool receiver writes the transition directly, the tool receiver needs access to Garrison's full SQL layer, which is the opposite of what an in-tree MCP server should look like (the `pgmcp` pattern is narrow and isolated).

### Decision 2: Schema strictness

**Strict. Full M2.2 diary shape is required, not tiered.**

Required fields in the `finalize_ticket` input:
```
{
  "ticket_id": "uuid",
  "outcome": "one-line summary string, min 10 chars",
  "diary_entry": {
    "rationale": "string, min 50 chars",
    "artifacts": ["array of strings, can be empty"],
    "blockers": ["array of strings, can be empty"],
    "discoveries": ["array of strings, can be empty"]
  },
  "kg_triples": [
    {
      "subject": "string, min 3 chars",
      "predicate": "string, min 3 chars",
      "object": "string, min 3 chars",
      "valid_from": "iso timestamp or 'now'"
    }
  ]
}
```

Minimum `kg_triples` array length: 1. An agent that completed a ticket must be able to name at least one fact worth remembering.

Rationale: strict forces the reflection. Tiered would allow agents to satisfy the tool with minimum effort, defeating the purpose. Loose (free-text diary string) would lose the structural benefit of the M2.2 diary shape. The risk of strict is retry-loops on weaker models; that risk is addressed by decision 3.

### Decision 3: Retry semantics

**3-attempt cap. After 3 failed attempts, agent_instance fails with `exit_reason='finalize_invalid'`.**

Each retry returns validation errors in the tool response. The agent can see which fields failed and correct them. The retry counter is per agent_instance, tracked supervisor-side via a counter on the in-flight map in `internal/spawn/pipeline.go`.

Rejected alternatives:
- Unlimited retry — burns Claude budget, could loop forever on a model that can't produce valid JSON
- Single attempt — too strict; one malformed retry due to transient model issues shouldn't fail a whole ticket
- 5-attempt cap — 3 is enough for legitimate fixes; 5 implies the agent is genuinely failing and more retries just waste budget

Rationale: 3 attempts covers typical "agent made a JSON mistake, corrected on second try" patterns. Hard cap ensures bounded cost per agent_instance. `exit_reason='finalize_invalid'` lets the hygiene checker surface the failure correctly.

Per-invocation budget cap stays at $0.10 (M2.2's NFR-201). Retries consume budget; exhausting budget during retries terminates via existing `budget_exceeded` path (M2.2's `Adjudicate` already handles this).

### Decision 4: Hygiene checker disposition

**Kept, substantially simplified, with redefined `hygiene_status` vocabulary.**

New values for rows written from M2.2.1 onwards:
- `'clean'` — finalize succeeded, all writes committed atomically by the supervisor
- `'finalize_failed'` — agent exhausted 3 retries; `exit_reason='finalize_invalid'`
- `'finalize_partial'` — finalize validation succeeded but a palace write errored during the atomic transaction; supervisor rolls back the transition and marks the agent_instance failed; rare failure mode (MemPalace mid-run death)
- `'stuck'` — agent ran to turn limit or was killed by the supervisor without ever calling finalize_ticket
- `'pending'` — transient evaluation in-flight

The checker's implementation simplifies dramatically: it reads `agent_instances.exit_reason` and the presence of `ticket_transitions` rows, maps to the status value. No MemPalace queries required — palace writes are supervisor-guaranteed when finalize succeeds.

Legacy M2.2 rows keep their existing values (`'missing_diary'`, `'missing_kg'`, `'thin'`). The transition is handled at the query level; the dashboard surface (M3) will eventually consolidate.

Rationale: keeping the column preserves the operator-visibility surface that was core to M2.2's design. Redefining rather than deprecating honors the original intent (operator sees finalize failures) without duplicating schema. The simplification is a net win — the M2.2 hygiene checker had to dispatch queries to MemPalace and interpret results; M2.2.1's checker is a pure Go function over existing Postgres data.

### Decision 5: Separation from the MemPalace MCP tool

**The `mempalace` MCP server remains wired into every agent's config. Mid-turn writes to `hall_discoveries` and ad-hoc `kg_add` calls are explicitly allowed and documented in the new seed agent.md.**

The completion path is the only place where MCP calls are replaced by `finalize_ticket`. An engineer who notices a pattern worth recording mid-implementation still calls `mempalace_add_drawer('hall_discoveries', ...)`. An engineer who reads their wing before starting still calls `mempalace_search`.

Rationale: reflection discipline is preserved in both directions — required at completion (via finalize), enabled during work (via MCP). The compliance problem was specifically at completion; fixing it shouldn't eliminate mid-turn flexibility.

---

## Binding questions for `/garrison-specify m2-2-1`

The spec must commit to each, with listed defaults if no strong reason otherwise:

1. **Package name**: `internal/finalize`. Parallel to `internal/pgmcp`. Justification required if alternatives proposed.

2. **Subcommand name**: `supervisor mcp finalize`. Parallel to `supervisor mcp postgres`.

3. **MCP config entry position**: the `finalize` server lands as a third entry in `mcpServers` alongside `postgres` and `mempalace`. Order: `postgres`, `mempalace`, `finalize`. Spec commits to the exact `cmd` and `args` invocation, modeled on M2.2's `pgmcp` entry.

4. **Schema version**: embedded in the tool's input schema description as `garrison.finalize_ticket.v1`. Future revisions bump the version; the server handles only v1 in M2.2.1.

5. **Error response shape**: when validation fails, the tool returns a structured error object with `error_type` (one of `"schema"`, `"palace_write"`, `"transition_write"`, `"budget_exhausted"`), `field` (the JSON path of the failing field or null), and `message` (human-readable). Spec commits to the exact error vocabulary.

6. **Retry state location**: the finalize server is stateless; retry tracking lives in the supervisor's per-agent-instance state in `internal/spawn/pipeline.go`. The supervisor passes the current attempt count to the finalize server via an MCP tool parameter? Or the finalize server reads from Postgres? Spec commits.

7. **Atomic write boundary**: a single Postgres transaction covers: MemPalace diary write (via the Go MemPalace client making a synchronous MCP call to the palace sidecar), MemPalace triples write, `UPDATE tickets`, `INSERT ticket_transitions`. If the MemPalace call fails mid-transaction, the Postgres transaction rolls back. Spec commits to the exact transaction semantics, including what happens if the MemPalace write completes but the Postgres commit fails (orphan palace entry).

8. **Seed agent.md shape for M2.2.1**: exact content of `migrations/seed/engineer.md` and `migrations/seed/qa-engineer.md`. The completion protocol section is rewritten; mid-turn section is preserved. Spec produces both files as seed content, embedded via the `+embed-agent-md` tooling.

9. **M2.2 retrospective data handling**: spec commits to whether the M2.2.1 migration touches existing `ticket_transitions.hygiene_status` values (default: leave alone; legacy values remain for audit).

10. **Testing posture for model-compliance**: the M2.2 retro's live-run finding (haiku didn't call palace tools) becomes an acceptance criterion for M2.2.1: the same test, with the same haiku model, produces a populated palace and clean hygiene_status on finalize_ticket's first successful call. Spec commits to this as Criterion X.

---

## Acceptance criteria for M2.2.1

The milestone ships when:

1. `supervisor mcp finalize` subcommand runs, exposes `finalize_ticket` as its single tool, returns valid MCP protocol responses to init and tool-call messages.
2. An agent invocation with `finalize` in its MCP config shows `mcp_servers[2].status='connected'` in the init event.
3. A single ticket processed by the engineer role results in exactly one `finalize_ticket` call, schema-valid, diary + triples written to the palace, ticket transitioned to `qa_review`, `ticket_transitions.hygiene_status='clean'`.
4. Same ticket flow completed by qa-engineer on the qa_review transition, diary + triples written, ticket to `done`, hygiene_status `'clean'`.
5. Both agent_instances have `exit_reason=''` (or NULL) and `total_cost_usd` populated.
6. **Compliance criterion**: the same scenario run with Claude haiku-4-5-20251001 as both agents' model produces identical palace and hygiene_status outcomes as with Opus 4.7. Compliance is no longer model-dependent.
7. **Retry path**: a mockclaude fixture that emits invalid finalize_ticket payloads on the first 2 calls and a valid payload on the 3rd produces a successful ticket completion with `hygiene_status='clean'`. The 2 failed attempts are logged at `info` level.
8. **Failure path**: a mockclaude fixture that emits 3 invalid finalize_ticket payloads produces agent_instance failure with `exit_reason='finalize_invalid'` and `hygiene_status='finalize_failed'`. No transition row is written.
9. **Stuck path**: a mockclaude fixture that exits without ever calling finalize_ticket (turn-limit or killed) produces `hygiene_status='stuck'`. No transition.
10. **Mid-turn writes preserved**: a test that calls `mempalace_add_drawer` to `hall_discoveries` mid-turn and then successfully calls `finalize_ticket` succeeds; the hall_discoveries entry exists in the palace alongside the finalize-written diary.
11. Atomic-write chaos: mockclaude fixture emits valid finalize_ticket; MemPalace container is killed between the palace write and the Postgres commit. Outcome: Postgres transaction rolls back, hygiene_status='finalize_partial', no orphan state (or documented known-orphan behavior if rollback semantics prevent it).
12. All M1, M2.1, M2.2 tests still pass unchanged.

Criterion 6 is the headline acceptance — it validates that the milestone actually fixed the compliance problem M2.2 surfaced.

---

## Testing requirements

Following M2.1/M2.2 patterns:

**New unit tests**:
- `internal/finalize/server_test.go` — MCP init response, tool listing, schema validation for every required field, error vocabulary
- `internal/finalize/schema_test.go` — JSON schema validation coverage: missing fields, wrong types, empty arrays, too-short strings
- `internal/spawn/pipeline_test.go` additions — retry counter increments, cap enforcement, correct exit_reason on exhaustion
- `internal/hygiene/evaluator_test.go` rewrite — new vocabulary (clean, finalize_failed, finalize_partial, stuck), legacy-value pass-through for M2.2 rows
- `internal/spawn/spawn_test.go` additions — atomic write transaction shape, rollback on palace-write failure

**New integration tests** (under `//go:build integration`):
- `TestM221FinalizeHappyPath` — single engineer ticket via happy-path mockclaude, verify criteria 3 + 4
- `TestM221FinalizeRetryThenSuccess` — criterion 7
- `TestM221FinalizeFailsAfterThreeRetries` — criterion 8
- `TestM221FinalizeStuckWhenNeverCalled` — criterion 9
- `TestM221MidTurnWritesPreserved` — criterion 10
- `TestM221ComplianceModelIndependent` — criterion 6, runs the happy path twice, once with haiku, once with opus (manual/operator-run initially; can be automated with model-env-var parameterization)

**New chaos test** (under `//go:build chaos`):
- `TestM221AtomicWriteChaosPalaceKillMidTransaction` — criterion 11

**Regression check**: all M1, M2.1, M2.2 tests pass unchanged. M2.2's end-to-end integration tests are specifically updated to expect the new hygiene_status vocabulary; the data fixtures shift but the behavioral assertions remain.

---

## Implementation notes (non-binding, save time in plan)

- **Package structure**: `internal/finalize` follows `internal/pgmcp` exactly. `server.go` holds the MCP protocol loop, `tool.go` holds the schema and validation, `handler.go` holds the receiver (invoked by the supervisor when a finalize_ticket call comes through the stream-json tool_use path, not by the finalize server itself — critical distinction).

- **Critical architectural point**: the finalize MCP server receives the call from the agent, validates the schema, and returns success/error to the agent. The **supervisor-side handler** (in `internal/spawn`) watches for successful finalize tool_use events in the stream-json stream and triggers the atomic palace+transition write. Two separate code paths: the MCP server validates and returns; the supervisor's event parser acts on valid finalize calls. This keeps the MCP server stateless and in line with the pgmcp pattern.

- **Migration**: `migrations/20260YYYYYYYYY_m2_2_1_finalize_ticket.sql`. Updates the seed agent.md content via the `+embed-agent-md` tooling. No schema changes — hygiene_status is already a TEXT column. Optionally adds a CHECK constraint enforcing the new value vocabulary, but allows legacy values for backward compat. Decide in plan.

- **Dockerfile**: no changes. `supervisor mcp finalize` is a subcommand of the existing binary.

- **MemPalace client reuse**: the supervisor's palace-writing handler reuses `internal/mempalace.Client` (M2.2). No new palace client code needed. The client was built for the hygiene checker's read queries; M2.2.1 extends it with write methods following the same `docker exec` → `mempalace.mcp_server` → stdio JSON-RPC pattern.

- **Seed agent.md rewrite posture**: the M2.2 files are ~5300 chars each. M2.2.1 versions should be SHORTER — the completion protocol section is simpler (one tool call instead of a multi-step manual protocol). Target 3000-4000 chars. Brevity is a compliance aid per the retro's agent.md-size observation.

- **The new engineer.md completion protocol looks like**:
  > ## Completion
  > When you've done the work, call `finalize_ticket` with your ticket ID, outcome summary, diary entry (rationale, artifacts, blockers, discoveries), and at least one KG triple. This is the only way to complete. Do not update the ticket table directly. Do not transition the ticket yourself.

- **Worth flagging for plan phase**: the atomic-write-transaction question (criterion 11) needs careful thought. If MemPalace is a subprocess invoked synchronously from the supervisor-side handler, and the Postgres transaction brackets the palace write, what does rollback mean for the palace side? MemPalace's `add_drawer` is persistent; there's no transactional rollback. The answer is probably "best-effort atomic: if Postgres rollback happens after palace write succeeded, we log an orphan warning and the hygiene checker marks `'finalize_partial'`." Spec commits to this.