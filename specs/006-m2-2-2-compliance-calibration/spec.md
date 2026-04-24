# Feature specification: M2.2.2 — Compliance calibration

**Feature branch**: `006-m2-2-2-compliance-calibration`
**Created**: 2026-04-23
**Status**: Draft
**Input**: `/garrison-specify m2.2.2` — "Tune the M2.2.1 finalize mechanism so real models (haiku, opus) can actually use it. Fix the schema-error feedback loop, the Adjudicate precedence bug, and the agent.md prompting."

This spec is bound by [`specs/_context/m2.2.2-context.md`](../_context/m2.2.2-context.md). Every decision settled there — retain strict schema (Decision 1), prompt-only changes (Decision 2), palace-search via prompt not code (Decision 3), Adjudicate fix surgical (Decision 4), additive error-response backward compatibility (binding Q9), 3500-4500 byte agent.md target (binding Q8), live_acceptance-gated compliance matrix (binding Q6) — is input, not question. Where this spec cites a constraint, the authoritative text lives in that file; consult it rather than the paraphrase here.

M2.2.2 is a focused patch on shipped M2.2.1 (branch `005-m2-2-1-finalize-ticket`, PR #4, merged or awaiting merge). The justification is empirical: M2.2.1's live-run append recorded that haiku reached `finalize_ticket` once, received the sparse `error_type="schema", field=""` response, and gave up on the tool rather than fixing and retrying; opus burned its budget on pre-completion palace exploration without ever reaching the tool. Neither is an architectural failure — M2.2.1's thesis (supervisor writes from structured agent output) remains correct. M2.2.2 tunes the operational surfaces so real models can act on what the mechanism asks of them.

This spec is not a re-architecture. RATIONALE §3 as revised by M2.2.1 stays in force (see context §"Why this milestone exists"). The `finalize_ticket` schema stays strict (Decision 1). The atomic write transaction stays identical (Decision 2 — all M2.2.2 changes are localized: `internal/finalize/tool.go`, `internal/spawn/spawn.go`, two seed `agent.md` files).

## Clarifications

### Session 2026-04-23

- Q: For `failure="decode"` error responses, are the validation-only fields (`constraint`, `expected`, `actual`) present as empty strings, absent entirely via `omitempty`, or `null`? → A: **Present as empty strings.** Rationale: keeps the wire shape stable across both failure modes — agents parsing the error object find the same field set regardless of which `failure` branch fired, and empty strings are self-describing ("not applicable to this error class"). `omitempty` would require the agent to branch on field presence; `null` would require both a presence check and a nullability check. Empty-string is the least surprising. The `field` field is already handled this way in M2.2.1 (empty string for non-field-scoped errors), so this is a continuation of the established convention.

- Q: What bullet structure does the qa-engineer.md palace-search calibration use — the same Skip/Search/In-doubt shape as engineer.md, or QA-specific framing? → A: **QA-specific 3-bullet structure**: (1) "Always read engineer's wing diary for this ticket" — the handoff depends on it, not optional. (2) "Skip searches outside `wing_frontend_engineer` unless the diary explicitly references them." (3) "Budget up to 3 calls for the diary lookup (one `mempalace_search(wing='wing_frontend_engineer', query=<ticket.objective>)` + optional one `mempalace_list_drawers` narrowing + optional one targeted read)." Rationale: QA's role is verification of a handoff, not greenfield work. The engineer's Skip-if-trivial heuristic doesn't fit — the engineer's diary is always a required input for QA. This framing guards against the failure mode opus would exhibit for QA (wandering the palace looking for tangentially-related context) without blocking the diary-read that QA genuinely needs.

- Q: For already-committed rejections (post-commit double-call, M2.2.1 FR-260), what `failure` value does the response use, and how are the validation-only fields populated? → A: **Introduce `failure="state"` as a third enum value.** The `failure` enum becomes `{"decode", "validation", "state"}`. The rejection isn't a decode error (JSON parsed fine) and isn't a validation error (the payload shape could be valid; the lifecycle state is the objection). `constraint`, `expected`, `actual`, `line`, `column`, `excerpt` are all empty strings / 0 (same wire-shape-stability convention as Clarification 2026-04-23 Q1). `field` is empty string. `hint` carries the human-readable message (`"finalize_ticket already succeeded for this agent_instance"`). Rationale: keeps FR-303's validation-constraint enum tight and semantically coherent (no `already_committed` intruding on what are otherwise schema-shape constraints), gives the agent a clean three-way branch (decode = JSON broken, validation = payload shape wrong, state = server refuses on lifecycle grounds), and preserves M2.2.1 FR-260's behavior — only the wire shape changes, not the server's decision to refuse.

- Q: Does `TestM222ComplianceMatrix` programmatically inspect the MemPalace filesystem to verify per-role diary behavior (engineer's wing diary, qa-engineer's handoff read), or is palace content observation operator-manual in the retro? → A: **Postgres-only automated assertions; palace observation is operator-manual in `docs/retros/m2-2-2.md`.** The test's automated failure conditions are `hygiene_status='clean'` on both transitions per run, cost bounds, and the 2/3-per-model headline (SC-311 already pins these). Palace filesystem shape (drawer paths, wing layout, diary content quality) is not asserted in-test. The retro captures qualitative findings — which model performed diary-reads, which skipped palace search on trivial tickets, whether opus stayed within the 3-call budget — as operator observation after inspecting the palace mounts. Rationale: keeps the test surface tight and decoupled from MemPalace filesystem layout (which has churned and is outside M2.2.2's control), respects the M2.2 retro pattern (palace content was operator-observed, not asserted), and keeps the automated failure signal focused on the thesis SC-311 tests — did the run produce clean terminal state within budget? The per-model calibration evidence lives in the retro because that's where honest-ship narration belongs (pattern from M2.2.1 retro).

## User scenarios and testing (mandatory)

### User story 1 — agent recovers from a first-attempt schema error and succeeds on retry (priority: P1)

The operator inserts an engineering ticket. The supervisor spawns the engineer with the M2.2.2 seed `agent.md`. The agent begins the work loop, reads the ticket, optionally performs palace search (guided by the calibration heuristics), does the work, and composes a `finalize_ticket` payload using the example in its prompt as a template. The first attempt has a schema issue — a rationale field is too short (< 50 chars) because the agent abbreviated prematurely. The `finalize_ticket` tool rejects with the M2.2.2 richer-error shape: `failure="validation"`, `field="diary_entry.rationale"`, `constraint="min_length"`, `expected="string with min length 50"`, `actual="ok"`, `hint="the rationale field must be at least 50 characters; you sent 2"`. The agent reads the `hint`, extends the rationale to meet the constraint, and calls `finalize_ticket` again. The second call is schema-valid; the supervisor atomically commits the diary + KG triples + transition + terminal `agent_instances` row. `hygiene_status='clean'`. Ticket moves `in_dev → qa_review`.

**Why this priority**: this is the smallest slice that demonstrates the full M2.2.2 value — rich error → actionable retry → clean outcome. If this story ships, every other M2.2.2 item (Adjudicate fix, front-loading, palace calibration) is additive polish on top of a working retry-recovery loop. The acceptance criterion 7 in the context file is the direct codification.

**Independent test**: mockclaude fixture `m2_2_2_retry_after_schema_error.ndjson` — emits one malformed `finalize_ticket` tool_use (rationale too short), receives the richer error, then emits a corrected tool_use on attempt 2. Against a freshly-M2.2.2-migrated Postgres + running MemPalace sidecar + supervisor with the M2.2.2 binary, insert one ticket at `in_dev`; observe: one `agent_instances` row with `status='succeeded'`, `exit_reason=NULL` or `'completed'`, `hygiene_status='clean'`; one `ticket_transitions` row (`in_dev → qa_review`, `hygiene_status='clean'`); structured slog lines showing attempt 1 `ok:false` then attempt 2 `ok:true`.

**Acceptance scenarios**:

1. **Given** the M2.2.2 seed agent.md populated into the `agents` table via the migration and a ticket at column `in_dev`, **When** the supervisor spawns the engineer invocation, **Then** the per-invocation MCP config file contains `postgres`, `mempalace`, `finalize` entries identical to M2.2.1 (Decision 2 — tool contract unchanged); the finalize MCP server's `tools/list` response carries the identical v1 schema (no schema changes).
2. **Given** the agent emits a `finalize_ticket` tool_use whose `diary_entry.rationale` is < 50 chars, **When** the finalize MCP server validates, **Then** the returned tool_result's envelope text parses as `{ok: false, error_type: "schema", failure: "validation", field: "diary_entry.rationale", line: 0, column: 0, excerpt: "", constraint: "min_length", expected: "string with min length 50", actual: "<first 100 chars of actual value>", hint: "<human-readable instruction>", attempt: 1}`.
3. **Given** the agent reads the error and issues a corrected `finalize_ticket` tool_use with rationale ≥ 50 chars and all other fields valid, **When** the finalize MCP server validates, **Then** the tool_result's envelope text parses as `{ok: true, attempt: 2}`, the supervisor's pipeline observer fires `spawn.WriteFinalize`, and the atomic write commits per M2.2.1's semantics exactly (Decision 2 — atomic-write path unchanged).
4. **Given** the atomic write has committed, **When** the operator queries `agent_instances` + `ticket_transitions` for the ticket, **Then** the rows have the same shape as M2.2.1's SC-251 happy path (status='succeeded', hygiene_status='clean' on the transition row, cost populated). No M2.2.2-specific row-shape changes.

---

### User story 2 — agent that emits 3 consecutive malformed payloads fails cleanly with finalize_invalid (priority: P1)

A mockclaude fixture emits 3 consecutive schema-failing `finalize_ticket` calls (different fields each time, each receiving an M2.2.2 richer error but still malformed). The supervisor's retry counter reaches 3, SIGTERMs the process group. Adjudicate returns `exit_reason=finalize_invalid`. No `ticket_transitions` row is written. Hygiene checker (via the M2.2.1 evaluator routing) records no observable transition — the `agent_instances.exit_reason` is the failure signal.

**Why this priority**: this pins M2.2.1's failure-path behavior through the new error shape. If the retry cap stops working because the new error shape confuses the supervisor-side counter, we lose a guard rail that M2.2.1 depended on. Equal priority to US1.

**Independent test**: mockclaude fixture `m2_2_2_three_retries_exhausted.ndjson` — three consecutive malformed payloads, each touching a different invalid field. Observe: `agent_instances.status='failed'`, `exit_reason='finalize_invalid'`; no `ticket_transitions` row; ticket stays at `in_dev`. Directly mirrors M2.2.1's SC-253, validated against the new error shape.

**Acceptance scenarios**:

1. **Given** the supervisor-side retry counter from M2.2.1 is unchanged and the richer error responses are additive (Q9), **When** three malformed attempts fire in sequence, **Then** the counter increments 1 → 2 → 3 identically to M2.2.1 (the supervisor's pipeline observer parses the new envelope's `ok` field regardless of which additional fields are populated).
2. **Given** the 3rd tool_result is `ok:false`, **When** the supervisor observes it, **Then** the pipeline fires `onBail(ExitFinalizeInvalid)`, the process group receives SIGTERM, and Adjudicate returns `(status='failed', exit_reason=finalize_invalid)` unchanged from M2.2.1.

---

### User story 3 — operator observes `budget_exceeded` (not `claude_error`) when Claude hits its own budget cap (priority: P2)

A Claude subprocess hits its `--max-budget-usd` ceiling mid-turn. Claude emits `{type: "result", is_error: true, terminal_reason: "error_max_budget_usd", total_cost_usd: <value above cap>}`. Pre-M2.2.2, Adjudicate's `IsError` case ran before the budget-detection case; the supervisor classified as `exit_reason=claude_error`, hiding the cost root cause from operators. Post-M2.2.2, the precedence is swapped: when `ResultSeen=true`, `isBudgetTerminalReason` check runs first; the budget-cap event classifies as `budget_exceeded`, making cost the canonical failure signal the operator sees.

**Why this priority**: observability-grade, not a compliance gate. The headline M2.2.2 value (haiku + opus reliable compliance) doesn't depend on this, but the operator dashboard (M3) needs the correct classification to surface cost spikes. P2 reflects "important but not blocking."

**Independent test**: mockclaude fixture emitting the `is_error=true + terminal_reason=error_max_budget_usd` result event. Run under any build tag. Observe `exit_reason=budget_exceeded`. Regression: the M2.2.1 `TestM221FinalizeBudgetExhaustedDuringRetry` test continues to pass (that test's fixture uses `subtype="budget_exceeded"` with `is_error=false`, which hits the existing budget case, not the newly-promoted one — so no regression risk).

**Acceptance scenarios**:

1. **Given** `result.ResultSeen=true`, `result.IsError=true`, and `isBudgetTerminalReason(result.TerminalReason)==true`, **When** Adjudicate is called, **Then** it returns `(status='failed', exit_reason='budget_exceeded')`. This is the precedence swap from context §"What M2.2.2 produces" item 2.
2. **Given** `result.ResultSeen=true`, `result.IsError=true`, and `isBudgetTerminalReason(result.TerminalReason)==false` (any other error terminal reason), **When** Adjudicate is called, **Then** it returns `(status='failed', exit_reason='claude_error')` — unchanged M2.2.1 behavior.
3. **Given** `result.ResultSeen=false` and `wait.Signaled=true`, **When** Adjudicate is called, **Then** the existing M2.2.1 precedence (signaled / finalize_invalid / no_result) holds — the M2.2.2 change only affects the `ResultSeen=true` branch.

---

### User story 4 — haiku produces a well-formed first-attempt payload because the example is visible in the prompt (priority: P2)

The agent.md's front-loaded completion directive makes `finalize_ticket` the first thing the agent is primed on (context §"What M2.2.2 produces" item 3). The embedded example payload (item 4) gives haiku a concrete template to adapt — not a literal to copy, explicitly annotated. The retry framing (item 5) reframes schema errors as expected-and-recoverable. The net effect: haiku's first `finalize_ticket` call is well-formed on representative tickets, avoiding the M2.2.1 observation where attempt 1 was malformed and haiku gave up.

**Why this priority**: important for compliance-matrix success (criterion 9) but not individually provable without the live-acceptance test. Mockclaude fixtures can't simulate "the model made a better first attempt because of prompt phrasing." P2 because the mechanism is tested via US1 (retry recovery) regardless — this story is about *frequency* of first-attempt success, which only the compliance matrix surfaces.

**Independent test**: this user story's direct test is the compliance matrix (US6). As a proxy, the M2.2.2 mockclaude fixtures simulate the post-prompt-improvement model behavior — if they pass in conjunction with the agent.md changes shipping, US4 is structurally verified.

**Acceptance scenarios**:

1. **Given** the M2.2.2 seed `engineer.md` is applied via the migration, **When** the `agents.agent_md` column is read, **Then** its content opens with a sentence containing "Your goal is to call `finalize_ticket`" (or semantically equivalent front-loaded phrasing per context §"What M2.2.2 produces" item 3).
2. **Given** the same seed content, **When** the agent.md is searched for a fenced code block containing a `finalize_ticket` payload, **Then** exactly one such block is present; the ticket_id field uses angle-bracket placeholder syntax (`<ticket_id>` or similar), NOT a realistic-looking UUID (context §"Implementation notes" and Q3).
3. **Given** the same seed content, **When** the content is scanned for palace-search calibration bullets, **Then** the "Skip if … / Search if … / In doubt, skip" structure is present (context §"What M2.2.2 produces" item 6).
4. **Given** the same seed content, **When** the content is scanned for retry framing, **Then** the phrase "retrying with corrections is expected" (or semantically equivalent) appears adjacent to the mention of the 3-attempt cap.
5. **Given** both seed agent.md files are read and byte-counted, **When** compared to the context target, **Then** each is within 3500–4500 bytes inclusive.

---

### User story 5 — opus reaches `finalize_ticket` within its budget because palace search is calibrated to the ticket (priority: P2)

The agent.md's palace-search guidance is calibrated (context §"What M2.2.2 produces" item 6): skip for trivial tickets, budget up to 3 calls for complex ones, "in doubt, skip." Opus follows the guidance and does NOT exhaustively explore the palace for a simple hello-world ticket. It reaches `finalize_ticket` within its budget cap.

**Why this priority**: same as US4 — individually provable only via the compliance matrix. P2.

**Independent test**: compliance matrix (US6). No unit-test or mockclaude-fixture proxy can validate "opus made a different prompt-engineering choice because of calibrated guidance."

**Acceptance scenarios**:

1. **Given** M2.2.2's `engineer.md` is read, **When** the palace-search section is located, **Then** three distinct bullets are present: a "Skip if" bullet listing trivial-ticket markers, a "Search if" bullet listing cross-cutting/ambiguous markers with a budget cap (3 tool calls max), and an "In doubt, skip" instruction.
2. **Given** the same seed content, **When** the palace-search section is word-counted, **Then** the total is roughly proportional to M2.2.1's MemPalace-usage section (no sprawling essay; concise enough to actually get read).

---

### User story 6 — compliance matrix validates haiku AND opus reach clean completion on live runs (priority: P1)

The `TestM222ComplianceMatrix` live-acceptance test runs a happy-path ticket three times against haiku-4-5-20251001 and three times against claude-opus-4-7. The test measures per-run outcomes: `agent_instances` rows, `hygiene_status` on transitions, combined cost. The thesis under test: "richer errors + example + front-loading + palace calibration collectively produce reliable compliance on real models."

**Why this priority**: this is the headline M2.2.2 criterion. Criteria 1–8 and 10–12 are hygiene checks; criterion 9 is the thing that decides whether M2.2.2 validates its thesis or ships as "partial improvement." If criterion 9 fails for either model, retro documents the shortfall honestly (same honest-ship pattern M2.2.1 used).

**Independent test**: the test itself — operator invocation of `go test -tags=live_acceptance -count=1 -timeout=20m -run='TestM222ComplianceMatrix' .` with real API credentials. Aggregate budget: under $3 USD across all 6 runs (context §"Acceptance criteria" item 9).

**Acceptance scenarios**:

1. **Given** three runs against haiku-4-5-20251001, **When** the outcomes are collected, **Then** at least 2 of 3 runs have `hygiene_status='clean'` on both the `in_dev→qa_review` and `qa_review→done` transitions.
2. **Given** three runs against claude-opus-4-7, **When** the outcomes are collected, **Then** at least 2 of 3 runs have `hygiene_status='clean'` on both transitions.
3. **Given** the 6 runs combined, **When** total cost is summed, **Then** the aggregate is strictly less than $3.00 USD.
4. **Given** any run that produces `hygiene_status='clean'` (automated), **When** the operator subsequently inspects the palace mount post-run (manual, not part of the test body per Clarification 2026-04-23 Q4), **Then** both wings (`wing_frontend_engineer` + `wing_qa_engineer`) carry at least one diary drawer whose body references the ticket, and at least one KG triple per wing mentions the ticket. The observation is recorded in the retro; test pass/fail does not depend on it.
5. **Given** the retro `docs/retros/m2-2-2.md`, **When** it is written, **Then** it documents the per-model outcomes explicitly (haiku first, then opus) with observed cost, observed retry frequency, observed palace-search behavior, and the manual palace-inspection findings from scenario 4 per Clarification 2026-04-23 Q4. Whether the thesis validates or partially validates, the retro records the honest finding.

---

### Edge cases

- **A decode error at offset 0** (payload is completely malformed, e.g. the agent emitted a non-JSON string). Response: `failure="decode"`, `line=1`, `column=1`, `excerpt=<first 40 chars of the payload>`, `hint="the arguments object must be valid JSON; <underlying parser message>"`. Validation-only fields (`field`, `constraint`, `expected`, `actual`) present as empty strings per Session 2026-04-23 clarification.
- **A decode error past EOF** (payload is truncated mid-object). Response: `line`/`column` point at the EOF position; `excerpt` is the last 40 chars before EOF (or the entire payload if shorter); `hint` names the expected closing token.
- **A validation error on a nested array element** (e.g., `kg_triples[2].subject` too short). Response: `field="kg_triples.2.subject"` (using JSON Pointer path syntax — dots for object members, integer indices for array elements); `constraint="min_length"`; `expected="string with min length 3"`; `actual=<the rejected value>`.
- **An agent that emits two `tool_use` blocks in the same assistant event, one finalize + one mempalace**. Supervisor handles the finalize call identically — the pipeline observer only looks at `name="finalize_ticket"`; the other tool_use is routed to the existing mempalace observer. No M2.2.2 regression risk.
- **An agent that calls `finalize_ticket` after the atomic write has already committed** (post-commit double-call). M2.2.1's decision-to-refuse applies unchanged; only the wire shape changes. The server returns `{ok: false, error_type: "schema", field: "", failure: "state", line: 0, column: 0, excerpt: "", constraint: "", expected: "", actual: "", hint: "finalize_ticket already succeeded for this agent_instance"}` per Clarification 2026-04-23 Q3. The `hint` field surfaces the same message M2.2.1's `message` field did; both fields are present for backward compat per Q9.
- **An opus run that exceeds the budget cap before calling `finalize_ticket` even once**. M2.2.2's Adjudicate fix (US3) correctly classifies as `exit_reason='budget_exceeded'` (not `claude_error`). The compliance matrix counts this as a failed run; the retro documents the frequency.

## Requirements (mandatory)

### Functional requirements

**Richer error responses**

- **FR-301**: the `finalize_ticket` tool's error response MUST extend the M2.2.1 shape additively per context §"What M2.2.2 produces" item 1. All M2.2.1 fields (`error_type`, `field`) remain; new fields added: `failure ∈ {"decode", "validation", "state"}`, `line` (int, 1-based), `column` (int, 1-based), `excerpt` (string, at most 40 chars), `constraint` (string, empty for decode and state errors), `expected` (string, empty for decode and state errors), `actual` (string, truncated to 100 chars for validation errors, empty for decode and state errors), `hint` (string, human-readable). Already-committed rejections (M2.2.1 FR-260) use `failure="state"`, `field=""`, all decode-positional and validation fields empty, `hint="finalize_ticket already succeeded for this agent_instance"` per Clarification 2026-04-23 Q3.
- **FR-302**: for `failure="decode"` responses, the server MUST populate `line` + `column` by converting the `*json.SyntaxError.Offset` to a line/column pair (walking the payload once). Validation-only fields (`constraint`, `expected`, `actual`) MUST be present as empty strings per Clarification 2026-04-23 Q1 for wire-shape stability across all three `failure` branches (decode, validation, state).
- **FR-303**: for `failure="validation"` responses, the server MUST populate `constraint` with one of the enumerated names: `"required"`, `"min_length"`, `"max_length"`, `"min_items"`, `"max_items"`, `"type_mismatch"`, `"format"` (future-proof). `expected` MUST be a human-readable description of the constraint (e.g., `"string with min length 50"`, `"array with min length 1"`). `actual` MUST be the rejected value serialized as a string, truncated to 100 chars if longer. `line`, `column`, `excerpt` MAY be populated when the server can derive them (uncommon for validation errors; commonly empty / 0). For `failure="state"` responses (already-committed per FR-301), `constraint`, `expected`, `actual`, `line`, `column`, `excerpt` are all empty strings / 0 — the enum does not apply because the objection is lifecycle, not schema.
- **FR-304**: for array-element validation failures, `field` MUST use JSON Pointer path syntax with integer indices (e.g., `kg_triples.2.subject`). Nested object paths use dot separators (`diary_entry.rationale`). Consistent with M2.2.1's existing `field` format.
- **FR-305**: every schema-error response MUST carry a non-empty `hint` field. The hint is the agent-facing instruction: it tells the model exactly what to do. Examples: `"the rationale field must be at least 50 characters; you sent 2"`, `"expected object closing brace '}' before end of input"`, `"the kg_triples array requires at least 1 item; you sent []"`. The hint is derived from the constraint + field + actual; boilerplate acceptable, verbatim-copy from a template is fine.

**Adjudicate precedence**

- **FR-306**: in `internal/spawn/spawn.go`'s `Adjudicate` function, when `result.ResultSeen=true`, the `isBudgetTerminalReason(result.TerminalReason)` check MUST run before the `result.IsError` check. Per context §"What M2.2.2 produces" item 2: Claude's `is_error=true + terminal_reason="error_max_budget_usd"` combination classifies as `exit_reason=budget_exceeded`, not `claude_error`. No other precedence rows change (Decision 4 — surgical fix).
- **FR-307**: when `result.ResultSeen=false`, the existing M2.2.1 precedence (MCPBailed / ParseError / ShutdownInitiated / Timeout / Signaled / NoResult / etc.) runs unchanged. The M2.2.2 precedence change only affects the post-result-seen ordering.

**Seed agent.md rewrites**

- **FR-308**: `migrations/seed/engineer.md` MUST be rewritten so that its first substantive paragraph (after the role header) states the agent's primary goal as calling `finalize_ticket` with a valid payload. The work-loop steps are subordinated to that goal. Per context §"What M2.2.2 produces" item 3 the shape is: "Your goal is to call `finalize_ticket` with a valid payload describing the work you did on ticket `$TICKET_ID`. Everything below is in service of producing that payload." Exact wording may vary; the front-loading semantic must hold.
- **FR-309**: `migrations/seed/engineer.md` MUST contain exactly one fenced code block (```` ``` ````-delimited) carrying a complete `finalize_ticket` payload as an example. The example MUST use angle-bracket placeholder syntax for all agent-filled fields (`<ticket_id>`, `<your one-line outcome>`, `<paragraph explaining what you did, at least 50 chars>`, etc.). The example MUST NOT use realistic-looking UUIDs, realistic-sounding rationale prose, or values that could be mistaken for a literal intended emission. Per context §"Implementation notes" second bullet — placeholder-copy-verbatim is a real failure mode.
- **FR-310**: `migrations/seed/engineer.md` MUST contain the palace-search calibration bullets per Q4 / context §"What M2.2.2 produces" item 6: a "Skip if" bullet naming trivial-ticket markers and indicating <5% budget consumption, a "Search if" bullet naming complexity markers and capping at 3 tool calls, and an "In doubt, skip" instruction.
- **FR-311**: `migrations/seed/engineer.md` MUST contain retry-framing language per Q5: "If `finalize_ticket` returns a schema error, read the `hint` field carefully, fix the specific issue named, and call `finalize_ticket` again. You have up to 3 attempts; retrying with corrections is expected." Paraphrase is acceptable if the semantic — that retry with correction is normal and expected — is preserved.
- **FR-312**: `migrations/seed/qa-engineer.md` MUST be rewritten in parallel: front-loaded goal statement (calling `finalize_ticket`), one example payload (with QA-role appropriate field values in placeholder syntax), palace-search calibration with the **QA-specific three-bullet structure** per Clarification 2026-04-23 Q2 — (1) always read engineer's wing diary for this ticket, (2) skip searches outside `wing_frontend_engineer` unless the diary explicitly references them, (3) budget up to 3 calls for the diary lookup — and retry framing identical to the engineer.
- **FR-313**: both rewritten files MUST land within 3500–4500 bytes inclusive (Q8). The byte count is verified via `wc -c`.

**Retry observability**

- **FR-314**: the supervisor's structured logs MUST continue to emit FR-276 `finalize tool_use` + `finalize tool_result` lines identically to M2.2.1. M2.2.2 does not change logging. The new error fields arrive in the `detail` field already logged (M2.2.1 recording) — parsing them is the agent's job, not the supervisor's; the supervisor logs the wire detail verbatim.

**Migration + tests**

- **FR-315**: the M2.2.2 migration MUST be seed-only (follows the M2.2.1 pattern — UPDATE agents SET agent_md via `+embed-agent-md`). No schema DDL. No new columns. The migration filename follows the chronological convention (`migrations/20260YYYYYYYYY_m2_2_2_compliance_calibration.sql`). Per context §"What M2.2.2 does not produce" — no schema changes.
- **FR-316**: existing M2.2.1 tests MUST be updated minimally per Q7: `internal/finalize/schema_test.go` (11 negative cases) gains presence assertions for the new additive fields (a test helper that validates "response has `failure`, `constraint`, `expected`, `actual`, `hint` as strings"). No changes to other test files. Integration + chaos tests from prior milestones run unchanged.
- **FR-317**: new unit tests land per context §"Testing requirements" — ≥ 6 new cases in a new `internal/finalize/richer_error_test.go` file (kept separate from the existing `tool_test.go` so descriptor + happy-path tests stay tight) covering decode at various offsets (0, mid-stream, EOF) and validation for each required field + constraint; 1 new `internal/spawn/pipeline_test.go` test (`TestAdjudicateBudgetExceededTakesPrecedenceOverIsError`) pinning FR-306.
- **FR-318**: new integration tests land (Testing requirements): `TestM222RetryAfterSchemaError` and `TestM222ThreeRetriesExhausted`, both mockclaude-driven under `//go:build integration`. Fixture files under `internal/spawn/mockclaude/scripts/` following the M2.2.1 naming convention.
- **FR-319**: new live-acceptance test lands: `TestM222ComplianceMatrix`, table-driven with `[]string{"claude-haiku-4-5-20251001", "claude-opus-4-7"}`, build-tag `live_acceptance`, operator-invoked. Per Q6. Automated assertions are Postgres-only per Clarification 2026-04-23 Q4: the test fails on `hygiene_status != 'clean'` below the 2/3-per-model bar, aggregate cost ≥ $3, or structural failures (no terminal rows). The test MUST NOT assert palace filesystem layout, drawer paths, or diary content — those are operator-manual observations captured in `docs/retros/m2-2-2.md`. The test MAY `t.Logf` cost and transition details per run to aid manual retro authoring.

**Scope discipline**

- **FR-320**: M2.2.2 MUST NOT introduce new Go dependencies. No additions to `supervisor/go.mod` or `supervisor/go.sum`. Fifth consecutive milestone holding the locked-deps line. Per context §"Acceptance criteria for M2.2.2" item 11.
- **FR-321**: M2.2.2 MUST NOT change the `finalize_ticket` schema (input_schema). The tool descriptor's `input_schema` field is byte-identical to M2.2.1's. Per Decision 1.
- **FR-322**: M2.2.2 MUST NOT change the retry-cap, atomic-write semantics, hygiene_status vocabulary, or MemPalace sidecar topology. All M2.2.1 architectural decisions preserved. Per Decisions 1–4 and context §"What M2.2.2 does not produce".

### Key entities

- **Richer schema-error response**: the JSON object the `finalize_ticket` tool returns on decode, validation, or state failure. Carries the canonical M2.2.1 fields (`error_type`, `field`) plus M2.2.2 additions (`failure`, `line`, `column`, `excerpt`, `constraint`, `expected`, `actual`, `hint`). The wire shape is stable across all three failure branches (empty strings for not-applicable fields per Clarifications 2026-04-23 Q1 and Q3). The agent's retry depends on parsing this shape; the operator's debugging depends on reading it.
- **JSON decode position**: line + column + excerpt triplet the server computes from `*json.SyntaxError.Offset`. Implementation is 30–40 lines of helper code per context §"Implementation notes". Not a persistent entity — produced fresh per error.
- **Front-loaded agent.md**: the rewritten seed `agent.md` shape where the first substantive content states the goal as calling `finalize_ticket`. Subsequent work-loop steps are subordinated via "in service of producing that payload" framing. The semantic is "the agent should know its target tool before doing anything else."
- **Example payload**: the fenced code block inside each seed agent.md carrying a complete `finalize_ticket` payload with angle-bracket placeholders. Not a persistent entity — part of the seed content that lands in `agents.agent_md` via the migration.

## Success criteria (mandatory)

### Measurable outcomes

- **SC-301**: the M2.2.2 `finalize_ticket` tool's error response carries `failure ∈ {"decode","validation","state"}`, `hint`, and at minimum the M2.2.1 fields (`error_type`, `field`) on every schema-error path. Verified by the extended schema tests (including one explicit already-committed case asserting `failure="state"`).
- **SC-302**: for decode errors, `line` and `column` are 1-based positive integers pointing at the failing token. An entirely-empty payload or an unopenable object produces `line=1, column=1`. Verified by a dedicated test.
- **SC-303**: for validation errors, `constraint` is one of the enumerated names (`required`, `min_length`, `max_length`, `min_items`, `max_items`, `type_mismatch`, `format`). No free-form strings. Verified by a dedicated test that runs one case per constraint.
- **SC-304**: `TestAdjudicateBudgetExceededTakesPrecedenceOverIsError` passes — a `Result` with `ResultSeen=true, IsError=true, TerminalReason="error_max_budget_usd"` produces `(status='failed', exit_reason='budget_exceeded')`. Pre-M2.2.2 behavior (same input producing `claude_error`) is gone.
- **SC-305**: a mockclaude fixture emitting one malformed then one valid `finalize_ticket` call produces `hygiene_status='clean'` end-to-end (`TestM222RetryAfterSchemaError` passes).
- **SC-306**: a mockclaude fixture emitting three consecutive malformed payloads produces `hygiene_status='finalize_failed'` and no `ticket_transitions` row — M2.2.1 regression check (`TestM222ThreeRetriesExhausted` passes).
- **SC-307**: both rewritten seed `agent.md` files open with front-loaded `finalize_ticket` goal language, contain exactly one example payload fenced code block with angle-bracket placeholders (no realistic UUIDs), contain their role-appropriate palace-search calibration bullets (engineer: Skip/Search/In-doubt per FR-310; qa-engineer: Always-read-engineer-diary / Skip-outside-engineer-wing / 3-call-budget per FR-312), and contain the retry-framing language naming the `hint` field as the thing to read. Byte count of each file is in 3500–4500 bytes. Verified by extending `TestSeedAgentMdStructureAndLength`.
- **SC-308**: all M1, M2.1, M2.2, M2.2.1 tests pass unchanged, aside from the enumerated M2.2.1 schema-error fixture updates (FR-316). Regression verified by running the full test suite under each build tag.
- **SC-309**: `git diff --stat origin/main..HEAD -- supervisor/go.mod supervisor/go.sum` is empty. Zero new Go dependencies; fifth consecutive milestone.
- **SC-310**: reverting `migrations/seed/engineer.md` and `migrations/seed/qa-engineer.md` to their M2.2.1 versions (with no other code changes) produces M2.2.1 behavior exactly — the compliance gaps return. This confirms Decision 2 (prompt changes are isolated from code) by experiment. Verified once during acceptance by the operator (not automated).
- **SC-311 (headline)**: `TestM222ComplianceMatrix` under `live_acceptance` tag: 3 runs per model × 2 models = 6 total runs; at least 2 of 3 runs per model produce `hygiene_status='clean'` on both transitions; aggregate cost across 6 runs is strictly less than $3.00 USD. The thesis under test validates iff both models hit the 2/3 bar. Partial pass (one model clean, one still failing) ships as "partial improvement" and is documented as such in the retro.

## Assumptions

- The M2.2 spike stack (spike-mempalace + spike-docker-proxy containers) is available for integration + chaos + live-acceptance runs. M2.2.2 does not change the topology; it assumes M2.2.1's acceptance environment is intact.
- `claude-opus-4-7` is available via the `claude` 2.1.117 binary's `--model` flag at acceptance time. Per M2.2.1's Session 2026-04-23 empirical confirmation. If Anthropic deprecates the model between M2.2.1 ship and M2.2.2 acceptance, the compliance matrix's opus leg may require a substitute; spec commits to opus-4-7 as the target and defers any substitution decision to operator judgment at acceptance time.
- Anthropic billing credentials are available for the live-acceptance run. The compliance matrix test cost (~$3 aggregate across 6 runs) is the operator's budget responsibility.
- The `+embed-agent-md` tool from M2.2 continues to work unchanged — the M2.2.2 migration reuses the existing marker convention (`-- +embed-agent-md:engineer:begin` etc.).
- The supervisor binary at M2.2.2 ship time still produces a single-binary executable — no image-level changes are contemplated, and the Dockerfile from M2.2 remains valid. Binary size growth from M2.2.2 is negligible (richer-error code is ~30–40 LOC of helpers; no structural additions).
- Mockclaude's directive set from M2.2.1 (`#finalize-tool-use-ok`, `#finalize-tool-use-fail`) continues to serve M2.2.2 fixtures unchanged. The new fixtures are NDJSON files following the M2.2.1 pattern; no mockclaude extensions needed.

## Dependencies on prior milestones

- **M2.2.1** is the direct predecessor. Its `finalize_ticket` schema, tool server, retry counter, atomic writer, hygiene evaluator routing, and seed-migration pattern all remain as shipped. M2.2.2 modifies the tool server's error-response layer, the Adjudicate precedence in one line, and the two seed `agent.md` files. All other M2.2.1 code is untouched.
- **M2.2 shipped** MemPalace wiring + sidecar topology. M2.2.2 uses it unchanged.
- **M2.1 shipped** Claude Code invocation + stream-json parsing. The pipeline observer's tool_use/tool_result handling inherits unchanged from M2.1's event routing.
- **M1 shipped** event bus + supervisor core. Unchanged.

## Out of scope

Per [`specs/_context/m2.2.2-context.md`](../_context/m2.2.2-context.md) §"What M2.2.2 does not produce", the following are explicitly NOT part of this milestone:

- **Flexible-first-attempt schema mode.** Deliberately dropped — if richer errors don't produce reliable retry behavior, it becomes a future milestone. Observe-then-decide.
- **Changes to the `finalize_ticket` schema** (input_schema). Strict shape preserved; richer errors communicate what's wrong, the schema doesn't weaken.
- **Changes to the 3-attempt retry cap.** Unchanged.
- **Changes to the atomic write transaction.** M2.2.1's semantics (30s ceiling, WithoutCancel, palace calls bracketed by Postgres tx) preserved exactly.
- **Dashboard / UI work.** M3 concern.
- **Secret vault integration (Infisical).** M2.3 concern.
- **MemPalace version bumps, container changes, socket-proxy changes.** Deployment topology frozen at M2.2.
- **CI automation of the compliance matrix.** Remains operator-run under the `live_acceptance` build tag.
- **Persistent retry counter.** In-memory supervisor counter from M2.2.1 remains.
- **`hall_discoveries` searchability fix.** M2.2.1 retro's deferred item; still deferred.
- **Changes to `internal/finalize` beyond the error-response layer.** Server, handler, schema validator core, MCP protocol loop — all preserved.

If the implementation phase tries to expand into any of these, stop and surface the scope violation.
