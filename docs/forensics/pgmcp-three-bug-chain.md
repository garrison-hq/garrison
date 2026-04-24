# pgmcp three-bug chain — forensic

**Discovered**: 2026-04-24, during the M2.2.x compliance arc close-out.
**Fixed**: commit `59fc977` (pgmcp) + migration `20260424000007_agent_ro_agent_instances_grant.sql` (grant).
**Companion**: `docs/retros/m2-2-x-compliance-retro.md` for arc-level narrative; this document is the focused technical read.

---

## 1. TL;DR

Three bugs in `internal/pgmcp` and its M2.1 grant surface compounded to make every agent interaction with Postgres through pgmcp look like "completed with no output", every `finalize_ticket` call return `failure:"state"` with a "please retry" hint, and every UUID round-trip fail silently. Across three successive retros (M2.2, M2.2.1, M2.2.2), the bugs made model behaviour appear worse than it was and led each retro to interpret the observed failures as model-capability ceilings rather than as infrastructure contamination. The underlying pattern: happy-path tests keyed on the *observed* envelope shape instead of the *contract* envelope shape, so the tests passed green against a non-compliant implementation for months.

---

## 2. The three bugs, in discovery order

### Bug 1 — `agent_instances` SELECT grant missing for `garrison_agent_ro`

**Where**: `migrations/20260422000003_m2_1_claude_invocation.sql` §Section 2 — the grant surface for the `garrison_agent_ro` role. The role got SELECT on `tickets`, `ticket_transitions`, `departments`, and `agents`. It did not get SELECT on `agent_instances`.

**When introduced**: M2.1 (2026-04-22), when `garrison_agent_ro` was first created.

**When made load-bearing**: M2.2.1 (2026-04-23), when `internal/finalize/handler.go` added a precheck that queries `agent_instances` for the already-committed check (`SelectAgentInstanceFinalizedState` → `status, exit_reason`). The finalize MCP server connects as `garrison_agent_ro` via `GARRISON_DATABASE_URL`. The query exists because M2.2.1's finalize contract needs to reject a second `finalize_ticket` call with a clean `state` rejection rather than silently letting it collide with the already-committed row. Reading is strictly necessary; writing is done by the supervisor under a separate role.

**How it manifested to agents**: every `finalize_ticket` call failed the precheck with `permission denied for table agent_instances`. The handler's `checkAlreadyCommitted` branch at `handler.go:101-110` surfaces internal errors as a `state`-shaped rejection with a retry hint:

```json
{"ok": false, "error_type": "schema", "failure": "state",
 "message": "internal error checking finalize state",
 "hint": "internal error checking finalize state; please retry",
 ...}
```

Agents dutifully retried as the hint instructed. Every retry failed identically because the grant was missing on every connection. Three attempts exhausted. Exit `finalize_invalid`. The ticket never transitioned. The retros observed "agent called finalize but couldn't get it right" and interpreted this as a schema-compliance gap.

**How it stayed hidden**: `internal/finalize`'s happy-path tests used mockclaude fixtures and a testdb role that had broader grants than `garrison_agent_ro` in production. The precheck path was exercised in isolation but not through `garrison_agent_ro`'s actual grant surface. M2.2.1's acceptance evidence didn't include a "permissions round-trip" test that would have surfaced the missing grant by trying to read `agent_instances` as the actual production role.

Additionally, the rejection shape disguised the cause: the handler labels internal errors with `error_type: "schema"` even though the authoritative field is `failure: "state"`. Slog logs during live runs showed the `error_type` field prominently; the `failure` field was less salient. Three retros read the logs and wrote "schema failure" in their analysis. It wasn't a schema failure.

**The fix**: migration `20260424000007_agent_ro_agent_instances_grant.sql` — two lines:

```sql
-- +goose Up
GRANT SELECT ON agent_instances TO garrison_agent_ro;

-- +goose Down
REVOKE SELECT ON agent_instances FROM garrison_agent_ro;
```

Matches the grant `garrison_agent_mempalace` already has from the M2.2 migration. Read-only, no other privileges.

---

### Bug 2 — `CallToolResult` envelope shape wrong

**Where**: `supervisor/internal/pgmcp/server.go` — `runQuery` and `runExplain` functions. Pre-fix, both returned a bare domain payload as the JSON-RPC result:

```go
// runQuery, pre-fix
return jsonRPCResponse{Result: map[string]any{
    "rows":      out,
    "truncated": truncated,
}}
```

MCP 2024-11-05 `tools/call` requires the result to be a `CallToolResult` shape:

```json
{
  "content": [{"type": "text", "text": "<payload-json>"}],
  "isError": false
}
```

The domain payload is supposed to be JSON-encoded and placed in `content[0].text`; the caller parses `content[0].text` back into the structured fields. Pre-fix, pgmcp was skipping the envelope entirely and putting `{rows, truncated}` at the top level of the JSON-RPC `result`.

**When introduced**: M2.1 (2026-04-22), when `pgmcp` was first shipped. The bug existed from the first commit.

**How it manifested to agents**: Claude Code's MCP client expects `content[]` and `isError`. When it saw neither, it rendered every pgmcp call as "(mcp__postgres__query completed with no output)" regardless of whether the query returned rows. Every successful pgmcp SELECT — ticket reads, palace-related queries, agent-config lookups — looked identical to a completed-but-empty call from the agent's perspective. This explained the M2.2 retro's "haiku skipped MANDATORY writes" (step 1 of the work loop, reading the ticket, returned empty — haiku reasonably concluded there was no ticket to work on) and the triage-hypothesis matrix's "both models skipped the agent.md work loop entirely" (same mechanism: every pgmcp SELECT came back empty, short-circuiting the work loop at step 1).

**How it stayed hidden**: the happy-path test `TestPgmcpQueryHappyPath` in `supervisor/internal/pgmcp/pgmcp_integration_test.go` passed green against the buggy implementation. Its assertion keyed on `rm["rows"]` at the top-level `Result`:

```go
// pre-fix TestPgmcpQueryHappyPath
rm, _ := r.Result.(map[string]any)
rows, _ := rm["rows"].([]any)
if len(rows) != 1 {
    t.Fatalf("expected 1 row, got %d", len(rows))
}
```

This test asserted the *observed* envelope shape as correct. A test asserting the *contract* envelope — "result is a `CallToolResult`, has a `content` array with one text item, parse that text as JSON, then check the parsed payload" — would have failed against the buggy implementation. Nobody wrote that test because nobody was looking for that bug.

`TestPgmcpExplainRunsAgainstRealTable` had the same shape flaw.

**The fix**: a `toolResultOK` helper in `server.go` that JSON-encodes the domain payload into `content[0].text` and sets `isError: false`. `runQuery` and `runExplain` both call it instead of returning the bare map. Tests were updated to decode the envelope via `decodeToolResult` so envelope regressions surface as explicit test failures.

---

### Bug 3 — UUID encoding as integer array

**Where**: `supervisor/internal/pgmcp/server.go` — `normalizeValue` function. Pre-fix:

```go
func normalizeValue(v any) any {
    switch x := v.(type) {
    case []byte:
        return string(x)
    default:
        return v
    }
}
```

Handled `[]byte` (BYTEA columns, etc.) but not `[16]byte`. pgx's default type map returns UUID columns as `[16]byte`, not as `[]byte`. Without a dedicated case, the value fell through to `default` and was JSON-serialised as a 16-integer array (`[192, 186, 182, 85, 6, 117, 78, 138, 181, 221, 166, 227, 242, 162, 175, 106]` instead of `"c0bab655-0675-4e8a-b5dd-a6e3f2a2af6a"`).

**When introduced**: M2.1 (2026-04-22), same as bug 2. Latent — pgmcp's M2.1 read surface rarely returned UUIDs in a round-trip path that would bite the agent. M2.2 and M2.2.1 extended the read surface to include `agent_instances` and additional UUID-valued joins; by M2.2.2 the bug was actively firing on every pgmcp call that returned a UUID column, but was compounded by bug 2 so the symptom wasn't distinguishable from "completed with no output".

**How it manifested to agents**: an agent running a query like `SELECT id, objective FROM tickets WHERE ...` would see (under bug 2's envelope issue, nothing; after bug 2 was fixed but before bug 3 was fixed) the `id` column as an integer array. If the agent then tried to construct a follow-up query like `SELECT * FROM ticket_transitions WHERE ticket_id = '<id>'`, the string it had was not a valid UUID literal. Every UUID-dependent round-trip either errored or silently referenced a different value than expected.

**How it stayed hidden**: no pgmcp test exercised a UUID column at all. Not because UUID support was deprioritised — because "return correctly-encoded data for all common Postgres types" wasn't stated as a requirement and wasn't checked. `TestPgmcpQueryHappyPath` used `SELECT 1 AS one` — an integer column. No type-coverage test existed.

This is a subtler form of the pattern from bug 2: tests didn't assert the contract (agents can round-trip common Postgres types) so the implementation didn't need to satisfy it. The fact that `[16]byte` is pgx's default UUID representation is documented in pgx; the fact that `normalizeValue` needed to handle it was not documented anywhere because no test made it a visible requirement.

**The fix**: a new case in `normalizeValue`:

```go
case [16]byte:
    return fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])
```

Two new unit tests:
- `TestNormalizeValueUUID` pins the `[16]byte` → canonical UUID-string conversion
- `TestNormalizeValueBytesSlice` pins the pre-existing `[]byte` case so a future refactor doesn't regress it while working on `[16]byte`

---

## 3. The invisibilizing pattern

The three bugs compounded in discovery-obscuring ways. Bug 2 (envelope) made every pgmcp call look empty to the agent; an agent seeing "no output" couldn't construct a meaningful follow-up query, so bug 3 (UUID) had no chance to fire in observable ways. Bug 1 (grant) shared a discovery-obscuring pattern with bug 2: the rejection shape labelled the failure as `"schema"` when the authoritative field was `"state"`. The surface label matched what the retros expected given the M2.2.1 narrative; the logs were consistent with both the schema-compliance story and the actual precheck-permission-denied story, and nobody was looking for the second.

Specifically called out: **`TestPgmcpQueryHappyPath` existed and passed green against buggy code.** Its assertion keyed on `rm["rows"]` at the top-level `Result`, asserting the non-compliant envelope shape as correct. A test asserting the correct MCP `CallToolResult` shape — content array, `type: "text"`, JSON-decodable text — would have failed against the buggy implementation from day one. The test didn't exist because the test-writer wrote what the implementation produced rather than what the contract required.

Similarly: no pgmcp test exercised a UUID column. Not because UUID support was deprioritised, but because "return correctly-encoded data for all common Postgres types" wasn't stated as a requirement and wasn't checked. Type coverage wasn't on the test matrix because nobody asked what types pgmcp needed to support.

---

## 4. Consequences across M2.2.x

Each prior retro's load-bearing conclusion is partly or wholly an artifact of this bug chain. Specifically:

### M2.2 retro — "haiku skipped MANDATORY writes"

Observed: real haiku-4-5-20251001 executed the ticket READ but did not call `mempalace_add_drawer` or `mempalace_kg_add`. Retro conclusion: agent prompt compliance is not automatic.

The observation was correct on the wire; the interpretation was contaminated by bug 2. Under the envelope bug, haiku's work-loop step 1 ("read the ticket from Postgres") returned "completed with no output". A model that queries Postgres, sees no ticket, and then doesn't proceed to MANDATORY writes is not "skipping MANDATORY writes" — it's concluding there's no work to do because there appears to be no ticket. Post-fix, the 12-run matrix observes haiku at 6/6 mechanism-compliant (finalize with substantive diary on every run) on the same ticket shape without any prompt changes.

### M2.2.1 retro — "malformed JSON on finalize"

Observed: haiku's first `finalize_ticket` attempt failed with `error_type="schema", field=""`. Retro conclusion: decode-level rejection, agent didn't retry.

The label was misleading. The stream-json capture (commit `e9c0b8e`) revealed the finalize payloads the agent emitted were well-formed; every rejection had `failure: "state"` — the authoritative field masked by the `error_type: "schema"` surface label. Bug 1 was failing the precheck with permission-denied; agents were retrying the same well-formed payload against a precheck that would fail identically on every retry. Haiku wasn't giving up on a schema it couldn't satisfy — it was giving up on a precheck that couldn't succeed regardless of payload shape.

### M2.2.2 retro — "calibration thesis falsified on live models"

Observed: 0/6 clean runs across both models. Every haiku: `finalize_never_called`. Every opus: `budget_exceeded`. Retro conclusion: prompt-only calibration has a ceiling.

Every run was contaminated. Bug 1 was active on every haiku attempt (the richer errors reached the agent, but the rejection was driven by the precheck's permission-denied, not by a schema error the richer fields would have helped diagnose). Bug 2 was active on every opus attempt: opus's palace exploration returned "completed with no output" for every query, so opus spent budget on queries producing no usable data. What looked like "over-exploration" was actually "trying to make headway against a broken tool". Bug 3 was latent but not fired in M2.2.2's run shape. The post-UUID-fix matrix shows haiku 6/6 mechanism-compliant on the same ticket shape without any prompt changes beyond what M2.2.2 shipped — the calibration may be doing its job; the bugs were eating the signal.

The retros were not wrong about what they observed. Every wire-level observation is accurate; what got contaminated was the interpretation. The arc-level retro (`docs/retros/m2-2-x-compliance-retro.md`) documents both the preserved mechanism-level wins and the revised interpretive conclusions.

---

## 5. Test discipline that would have caught this earlier

These are specific discipline items pgmcp's tests would need to have caught the bug chain pre-ship. Not a general lecture.

### 5.1 Happy-path tests must assert the contract shape, not the observed shape

Bug 2 survived because `TestPgmcpQueryHappyPath` asserted `rm["rows"]` at the top-level `Result`, which matched what the buggy code produced. The correct shape is `result.content[0].text` as a JSON-encoded payload per MCP 2024-11-05. The post-fix `decodeToolResult` helper in `pgmcp_integration_test.go` asserts the full envelope (`isError == false`, `content[]` has one text item, `text` is JSON-decodable) and returns the parsed payload for the existing assertions. Generalised: for any in-tree implementation of a third-party protocol, the tests should assert what the *protocol* requires, not what the *current implementation* produces.

### 5.2 Type-coverage tests for data-plane tools

Bug 3 survived because pgmcp had no test that exercised a UUID column. The post-fix `TestNormalizeValueUUID` pins the conversion; it was written to fix the specific bug, not as part of a broader type-coverage discipline.

Broader discipline: a data-plane tool (pgmcp, or any future MCP server that returns Postgres data) should have explicit round-trip tests for every common Postgres type the agent might encounter — UUID, timestamp, jsonb, numeric, array-of-text, array-of-UUID. Each type gets a test that:
1. Inserts a known value
2. Selects it through the tool
3. Decodes the response
4. Asserts the value round-trips to the expected canonical representation

This would have caught bug 3 at M2.1. It would also catch analogous bugs in future columns types pgmcp hasn't yet encountered.

### 5.3 Integration tests must include a full round-trip from agent's perspective

Bugs 1 and 2 both survived because the tests exercised their code paths in isolation rather than as the agent would see them. Bug 1 (permission-denied on `agent_instances`) was a grant issue; the finalize handler's tests used a testdb role with broader grants than production. Bug 2 (envelope shape) was a wire-format issue; the pgmcp tests decoded the JSON-RPC response structurally but didn't decode it through the MCP client protocol.

Generalised: an integration test for an MCP-exposed tool should simulate the agent's round-trip — agent's tool_use input goes in, server produces a response, the response is decoded as the agent's MCP client would decode it, and only the decoded result is asserted. This catches envelope regressions, grant-boundary issues that don't show up under broader test roles, and any middleware bug between the handler and the wire.

The M2.1 pgmcp test suite had structural assertions on the JSON-RPC response but no "decode as an MCP client would" step. Adding that step pre-ship would have surfaced bug 2 immediately.

---

## 6. Whether this pattern is pgmcp-specific or systemic

The operator's position on 2026-04-24: this pattern is pgmcp-specific, not a broader testing discipline problem across Garrison's in-tree components. This document records that position without relitigating it but flags the trigger condition for future revisiting.

**Trigger condition**: if a future milestone surfaces a similar "test asserted wrong shape and passed" finding in another in-tree component — `internal/claudeproto`, `internal/finalize`, `internal/mempalace`, `internal/hygiene`, or any future in-tree MCP server — the pattern generalises and the operator's position may need revisiting. At that point the question changes from "pgmcp had three bugs" to "our test discipline systematically misses contract-shape issues in protocol-implementing code."

---

## 7. Time cost of the chain

Approximate operator + assistant time, 2026-04-24 afternoon/evening:

- **Triage-hypothesis experiment design + 12-run matrix**: ~1h. Produced the 0/12 result that matched M2.2.2's 0/6, which raised the "something else is going on" flag.
- **Stream-json capture addendum (tee wrapper, 4-iteration subset, analysis)**: ~45min. Surfaced the `failure: "state"` distinction and the "every pgmcp SELECT returns empty" observation. This was the pivot from "prompt-calibration ceiling" to "infrastructure bugs".
- **Three-bug investigation + fix**: ~1.5h. Triangulating bug 1 via the precheck code path; verifying bug 2 against MCP 2024-11-05 spec; identifying bug 3 from `normalizeValue`'s fall-through case. All three fixed as one patch in commit `59fc977`.
- **Single-run opus validation + single-run haiku validation + 12-run matrix**: ~1.5h wall-clock. The 12-run matrix itself was 12m4s of test execution; around it, the operator ran the two single runs to confirm the fix before committing to a full matrix, and reviewed the matrix output afterwards.

**Total**: ~4-5 hours of operator + assistant time, plus approximately $2 of real model spend (rough estimate; the precise number is blurred by the cost-telemetry blind spot documented in the arc retro §11).

**Net assessment**: this cost was borne to reach a correct understanding of what the three prior retros had been describing. Three milestones had shipped with partly-contaminated interpretations of their live data. Four hours to reach clean plumbing and a revised interpretation is a good investment — the alternative was building M2.3 (or a speculative M2.2.3) on top of the uncontested "compliance thesis falsified" read from M2.2.2, which would have been working against a misunderstood ceiling.

The discovery path was not efficient in isolation: the stream-json capture could, in principle, have been part of M2.2.2's live-matrix workflow. It wasn't, because M2.2.2's retro was written before the question "what do the raw payloads actually look like" was asked. The lesson for future milestones with live-run evidence: if the interpretation depends on what the agent saw, the raw wire capture is part of the minimum evidence, not an optional addendum.

---

## Artifacts referenced

- **Commit `59fc977`**: `pgmcp: envelope wrapper + UUID normalization + agent_instances grant` — the three-bug fix.
- **Migration `20260424000007_agent_ro_agent_instances_grant.sql`**: the grant fix.
- **`supervisor/internal/pgmcp/server.go`**: post-fix implementation of `toolResultOK` and the `[16]byte` case in `normalizeValue`.
- **`supervisor/internal/pgmcp/pgmcp_integration_test.go`**: post-fix `decodeToolResult` helper and the rewritten happy-path tests.
- **`supervisor/internal/pgmcp/pgmcp_test.go`**: `TestNormalizeValueUUID` and `TestNormalizeValueBytesSlice`.
- **`experiment-results/matrix-post-uuid-fix.md`**: post-fix 12-run matrix. Clean-bug-signature scan results in §3.6.
- **`docs/retros/m2-2-x-compliance-retro.md`**: arc-level retro. This forensic is the detail companion.
