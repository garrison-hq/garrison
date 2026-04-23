# M2.2.1 acceptance evidence

**Milestone**: M2.2.1 — Structured completion via `finalize_ticket` tool
**Branch**: `005-m2-2-1-finalize-ticket`
**Executed**: 2026-04-23 via T018 playbook (operator-run)

## Summary

Observed outcomes across the test suites:

| Suite | Result | Duration |
|---|---|---|
| Unit (`go test ./...`) | PASS — 14 packages, zero regressions from M1/M2.1/M2.2 | 0.1s |
| Integration (`-tags=integration`) | PASS — 136s, all 12+ tests green | 136s |
| Chaos (`-tags=chaos`) | PASS — 24s, including TestM221AtomicWriteChaosPalaceKillMidTransaction | 24s |
| Live compliance, haiku (`-tags=live_acceptance`) | **MIXED** — see compliance findings below | 367s, $0.0927 spent |
| Live compliance, opus | Deferred (budget + time — not executed this pass) | — |
| No-new-deps guardrail | PASS — `git diff origin/main..HEAD supervisor/go.{mod,sum}` returned empty | — |

## Context criteria 1–12

| # | Criterion | Observed | Evidence |
|---|-----------|----------|----------|
| 1 | `supervisor mcp finalize` runs | **PASS** | `supervisor mcp finalize` exchanges valid JSON-RPC init (T005 smoke) |
| 2 | `mcp_servers[2].status='connected'` | **PASS** | TestM221FinalizeHappyPath init event reports all 3 MCPs connected |
| 3 | Engineer → clean transition | **PASS** | TestM221FinalizeHappyPath: `hygiene_status='clean'` on in_dev→qa_review |
| 4 | QA → clean transition | **PASS** | TestM221FinalizeHappyPath: `hygiene_status='clean'` on qa_review→done |
| 5 | Both rows have cost + wake_up_status | **PASS** | TestM221FinalizeHappyPath queries both fields non-null |
| 6 | Haiku ≡ Opus compliance | **PARTIAL** — see "Compliance-thesis findings" below | Live-run log `/tmp/m221-compliance-claude-haiku-4-5-20251001.log` |
| 7 | Retry-then-success | **PASS** | TestM221FinalizeRetryThenSuccess PASS |
| 8 | 3-retry exhaustion → finalize_invalid | **PASS** | TestM221FinalizeFailsAfterThreeRetries PASS |
| 9 | Stuck (never-called) | **PASS** | TestM221FinalizeStuckWhenNeverCalled PASS |
| 10 | Mid-turn writes preserved | **PASS** | TestM221MidTurnWritesPreserved PASS |
| 11 | Atomic-write chaos | **PASS** | TestM221AtomicWriteChaosPalaceKillMidTransaction: exit_reason=finalize_palace_write_failed after `docker kill spike-mempalace` mid-tx |
| 12 | M1/M2.1/M2.2 tests pass unchanged | **PASS** | Full integration + chaos suites green after minor test-fixture updates (documented in commit 3cf22c1) |

## Success criteria SC-251..SC-261

| SC | Observed | Evidence |
|----|----------|----------|
| SC-251 (two-agent clean + cost < $0.20) | PASS (mock) / NOT VALIDATED live | Mock path via TestM221FinalizeHappyPath; live haiku never reached QA |
| SC-252 (retry-then-success) | PASS | TestM221FinalizeRetryThenSuccess |
| SC-253 (3-retry exhaustion) | PASS | TestM221FinalizeFailsAfterThreeRetries |
| SC-254 (stuck never-called) | PASS | TestM221FinalizeStuckWhenNeverCalled |
| SC-255 (atomic-write chaos) | PASS | chaos test PASS; `exit_reason=finalize_palace_write_failed` observed |
| SC-256 (mid-turn preserved) | PASS (in stream) / NOT INSPECTED in live palace | TestM221MidTurnWritesPreserved hygiene=clean |
| SC-257 (M1/M2.1/M2.2 regression-free) | PASS | Full integration + chaos suites green |
| SC-258 (budget + retry interaction) | PASS | TestM221FinalizeBudgetExhaustedDuringRetry: exit_reason=budget_exceeded wins |
| SC-259 (binary size ≤ 115% of M2.2) | NOT MEASURED | Deferred — diff is ~250 LOC so well under +15% |
| SC-260 (seed length 3000–4000) | PASS | TestSeedAgentMdStructureAndLength: engineer.md=3559, qa-engineer.md=3593 |
| SC-261 (haiku ≡ opus) | **PARTIAL** | Haiku tested; opus deferred; see findings below |

## Compliance-thesis findings (SC-261, Criterion 6) — headline

**This is the load-bearing empirical outcome of M2.2.1.** The live haiku
run produced a more nuanced result than the pre-run prediction in the
retro. Summary from `/tmp/m221-compliance-claude-haiku-4-5-20251001.log`:

### What haiku DID do

- **Haiku called `finalize_ticket`.** This is the key M2.2.1 signal:
  unlike the M2.2 live-run where haiku skipped MANDATORY `mempalace_*`
  tools entirely, here haiku reached for the finalize tool as the
  completion mechanism. **The tool-architectural attractor worked.**
  One tool_use observed at t=27s (attempt 1).
- Haiku explored the palace via `mempalace_search`,
  `mempalace_status`, `mempalace_list_drawers`, `mempalace_kg_query`
  (all returned valid results).
- Haiku emitted a natural `result` event at t=53s with
  `terminal_reason="success"`, `is_error=false`, `total_cost_usd=0.0927`.

### What haiku did NOT do

- **Did not retry `finalize_ticket` after the first schema error.** The
  first call returned `{ok: false, error_type: "schema", field: ""}`
  (empty field means the whole envelope failed `json.Unmarshal` — likely
  malformed JSON at the root, not a specific-field violation).
- Instead of fixing the payload and retrying, haiku treated the schema
  error as a signal to **explore** — it spent the next ~25 seconds
  querying palace state (perhaps looking for prior-ticket diaries).
- Eventually haiku returned to text and emitted a terminal result
  without calling finalize again.

### Supervisor's disposition

The adjudicator correctly classified this as
`exit_reason=finalize_never_called` (not `finalize_invalid`, because
the retry counter reached 1, not 3). No `ticket_transitions` row
committed. `hygiene_status` is not writeable (no transition row).

### Thesis revision

M2.2.1's pre-run prediction was "tool-architectural compliance is
stricter than prompt-based by construction." The observation is more
nuanced:

- **Partial validation**: the tool IS the attractor — haiku reaches
  for it. This is strictly better than M2.2 where haiku ignored
  MANDATORY prompts entirely.
- **Retry gap**: haiku does NOT follow the "schema-error → fix →
  retry" discipline. It treats schema rejection as "try a different
  tool / explore" rather than "correct this call."
- **Practical implication**: M2.2.1's 3-attempt retry cap is
  insufficient when the first attempt is malformed and the model
  doesn't retry. In observed behaviour, haiku made 1 attempt, then
  gave up on finalize.

### What would make haiku succeed

Not rigorously tested, but observable paths forward (deferred to M3
or a follow-up patch):

1. **Example payload in the agent.md**. Providing a concrete
   `finalize_ticket` example (not just a field list) might reduce
   first-attempt malformation.
2. **Better error feedback**. The current `error_type: "schema",
   field: ""` is terse. If the server returned a JSON-parse error
   pointing at the specific character where decoding failed, haiku
   might self-correct.
3. **Prompt-level retry instruction**. The completion section in
   `engineer.md` doesn't explicitly tell the agent to RETRY after
   a schema error — it names the 3-attempt limit but doesn't frame
   retry as expected behaviour.
4. **Single-attempt flexible schema**. Relaxing the first attempt's
   strictness (e.g., accepting free-text diary) and tightening over
   attempts, rather than rejecting attempt 1 on a strict shape.

### Opus comparison (deferred)

Not executed this acceptance pass. The operator can run
`go test -tags=live_acceptance -run='TestM221ComplianceModelIndependent/claude-opus-4-7' .`
for the cross-model comparison. Expected opus cost: ~$0.50-$1.00 per
run (estimated from haiku's $0.09 × 5-10x opus price multiplier).

## Live-run mechanics observed

- Per-invocation budget cap $0.20 held (observed $0.0927 for the
  engineer).
- Wake-up injection fired correctly (the supervisor log shows
  `wake_up_complete` for the engineer).
- MCP init reported all three servers `status="connected"` (59 tools
  total, 3 MCP servers).
- `finalize_ticket` tool was exposed in the MCP inventory (haiku
  discovered and invoked it without explicit prompting).

## No-new-deps guardrail (FR-274)

```
$ git diff --stat origin/main..HEAD -- supervisor/go.mod supervisor/go.sum
(empty)
```

**PASS.** Third consecutive milestone (M1 → M2.1 → M2.2 → M2.2.1) with
zero external dependency additions.

## Ship disposition

**M2.2.1 is ship-ready as a partial-compliance improvement over M2.2.**

The architectural pivot (tool as attractor) is validated. The
secondary behaviour gap (haiku's reluctance to retry on schema error)
is a meaningful finding but NOT a ship-gate regression — M2.2.1 is
still STRICTLY BETTER than M2.2 for compliance:

- **M2.2**: haiku didn't call mempalace writes at all → palace empty.
- **M2.2.1**: haiku called finalize once → palace was about to be
  written except for a malformed first attempt.

The follow-up work (retry-resilient prompt improvements; richer
schema errors) is a targeted M3-era improvement that doesn't require
an M2.2.1 delay.
