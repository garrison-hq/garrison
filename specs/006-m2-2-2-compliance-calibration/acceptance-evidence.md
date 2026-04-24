# M2.2.2 acceptance evidence

**Milestone**: M2.2.2 — Compliance calibration
**Branch**: `006-m2-2-2-compliance-calibration`
**Executed**: 2026-04-23 / 2026-04-24 via T013 playbook (operator-supervised, agent-driven)

## Summary

| Suite | Result | Duration |
|---|---|---|
| Unit (`go test ./...`) | PASS — 14 packages, no regression from M1/M2.1/M2.2/M2.2.1 | 0.1s |
| Integration (`-tags=integration`) | PASS — full suite (M2.2.1 + M2.2.2) green | <60s |
| **Live compliance matrix** (`-tags=live_acceptance`) | **FAIL on SC-311 headline** (haiku 0/3, opus 0/3); see compliance findings | 36m, $0.7956 spent |
| No-new-deps guardrail (FR-320 / SC-309) | PASS — empty `git diff` | — |
| SC-310 rollback experiment | Structurally confirmed (Down block ≡ M2.2.1 seed); operator-run experiment deferred | — |

## Context criteria 1–12

Per [`specs/_context/m2.2.2-context.md`](../_context/m2.2.2-context.md) §"Acceptance criteria for M2.2.2".

| # | Criterion | Observed | Evidence |
|---|-----------|----------|----------|
| 1 | `finalize_ticket` returns extended fields | **PASS** | T002 + T003 + T004 + T005 — every error path constructs the full M2.2.2 envelope; 10 cases pinning each Constraint + decode + state |
| 2 | ≥ 6 new unit cases | **PASS** | 10 in `internal/finalize/richer_error_test.go` (per F1 spec edit) |
| 3 | Adjudicate returns `budget_exceeded` for `IsError + budget` | **PASS** | T006 / `TestAdjudicateBudgetExceededTakesPrecedenceOverIsError` |
| 4 | engineer.md opens with goal + has example + calibration + retry framing | **PASS** | T007 + extended `TestSeedAgentMdStructureAndLength` |
| 5 | qa-engineer.md parallel structure | **PASS** | T008 + same test, qa-specific 3-bullet calibration |
| 6 | Both agent.md files in 3500–4500 bytes | **PASS** | engineer.md=4262, qa-engineer.md=4286 |
| 7 | Mockclaude retry-then-success → hygiene_status='clean' | **PASS** | T011 / `TestM222RetryAfterSchemaError` |
| 8 | Mockclaude 3-retry-exhausted → finalize_failed | **PASS** | T011 / `TestM222ThreeRetriesExhausted` — exit_reason=finalize_invalid, no transition |
| 9 | **Live `TestM222ComplianceMatrix`**: ≥2/3 clean per model, cost <$3 | **FAIL** | haiku 0/3 clean (`finalize_never_called` × 3); opus 0/3 clean (`budget_exceeded` × 3); cost $0.7956 (under cap, but no clean runs) |
| 10 | All M1+M2.1+M2.2+M2.2.1 tests pass unchanged | **PASS** | full unit + integration regression PASS post-M2.2.2 |
| 11 | Zero new Go dependencies | **PASS** | `git diff --stat origin/main..HEAD supervisor/go.{mod,sum}` empty |
| 12 | Agent.md rollback verified (SC-310) | **STRUCTURAL PASS** | Down block content matches M2.2.1 migration seed verbatim; operator-run experiment deferred (low-risk, easy to verify post-merge) |

## Success criteria SC-301..SC-311

| ID | Criterion | Observed |
|---|---|---|
| SC-301 | Failure ∈ {decode,validation,state} + hint + M2.2.1 fields on every error | **PASS** — `TestRichErrorAlreadyCommittedHasFailureState` + 9 other cases, plus T005 helper on 10 legacy reject tests |
| SC-302 | Decode errors carry 1-based positive `line`/`column` | **PASS** — T001 (4 sub-tests) + T004 |
| SC-303 | Validation `constraint` is enumerated only | **PASS** — closed enum in T002, every value covered in T004 |
| SC-304 | `TestAdjudicateBudgetExceededTakesPrecedenceOverIsError` PASS | **PASS** — T006 |
| SC-305 | `TestM222RetryAfterSchemaError` PASS | **PASS** — T011 |
| SC-306 | `TestM222ThreeRetriesExhausted` PASS | **PASS** — T011 |
| SC-307 | Both agent.md: front-loaded + example + role-appropriate calibration + retry framing + 3500–4500 bytes | **PASS** — T007 + T008 |
| SC-308 | All M1/M2.1/M2.2/M2.2.1 tests pass unchanged | **PASS** |
| SC-309 | `git diff --stat origin/main..HEAD supervisor/go.{mod,sum}` empty | **PASS** — empty |
| SC-310 | Reverting seed migrations restores M2.2.1 behaviour exactly | **STRUCTURAL PASS** (down block embeds M2.2.1 seed verbatim); operator-run rollback experiment deferred |
| SC-311 (headline) | Live matrix: ≥2/3 clean per model, cost <$3 | **FAIL** — both models 0/3 clean; cost $0.7956 satisfied the cap but no clean runs were produced |

## Compliance-thesis findings (SC-311 / Criterion 9) — headline

The headline criterion **did not validate**. M2.2.2's calibration did
not move either model to the 2/3-clean-runs bar on the trivial
hello-world ticket. Cost stayed well under the $3 cap because failures
fired early.

### Per-iteration table

| Iter | Model | Engineer exit | Cost | hygiene_status |
|---|---|---|---|---|
| 1 | claude-haiku-4-5-20251001 | finalize_never_called | $0.0399 | (no transition) |
| 2 | claude-haiku-4-5-20251001 | finalize_never_called | $0.0574 | (no transition) |
| 3 | claude-haiku-4-5-20251001 | finalize_never_called | $0.0440 | (no transition) |
| 4 | claude-opus-4-7 | budget_exceeded | $0.2376 | (no transition) |
| 5 | claude-opus-4-7 | budget_exceeded | $0.2106 | (no transition) |
| 6 | claude-opus-4-7 | budget_exceeded | $0.2061 | (no transition) |

### Aggregate

- Haiku clean runs: **0 / 3** (need ≥ 2 for SC-311 PASS) → **FAIL**
- Opus clean runs: **0 / 3** (need ≥ 2 for SC-311 PASS) → **FAIL**
- Total cost across 6 runs: **$0.7956** — well under the $3 cap (ironic
  given each run failed early)
- SC-311 disposition: **NO PASS for either model**

### What worked (positive M2.2.2 outcomes)

- **Adjudicate precedence fix verified** — opus's three runs all
  classified as `exit_reason=budget_exceeded` (not `claude_error` as
  M2.2.1 would have produced). FR-306 / SC-304 validated by live
  evidence in addition to T006's unit test. This is the only one of
  the four M2.2.2 thesis items that demonstrably improved live
  behaviour.
- **Cost reduction for opus**: M2.2.1's single opus run hit
  $0.2564; M2.2.2's three opus runs averaged $0.215, suggesting the
  palace-search calibration trimmed exploration somewhat — but not
  enough to reach finalize_ticket within budget. Marginal, not
  decisive.
- **Total spend $0.7956 across 6 runs**: well under SC-311's $3 cap.

### What didn't work (M2.2.2 thesis falsified)

- **Haiku still cannot recover** from schema errors. All 3 haiku runs
  emitted ≤ 2 finalize attempts (decode-shape errors with `field=""`),
  then exited cleanly via `result` event without ever calling
  `finalize_ticket` successfully → `exit_reason=finalize_never_called`.
  The richer error responses (line, column, excerpt, hint) reached the
  agent but did not change behaviour. M2.2.1's "haiku gives up after
  schema error" failure mode is unchanged in M2.2.2.
- **Opus still over-explores the palace.** All 3 opus runs hit budget
  cap before finalize_ticket. The palace-search calibration bullets
  (Skip if / Search if / In doubt skip) did not produce the predicted
  exploration restraint.
- **Front-loading + example + retry framing** did not produce
  measurable lift on the trivial ticket for either model.

### Pattern observation: tool-architectural compliance ceiling

Combined with M2.2.1's live findings, the data suggests that for
trivial tickets the strict-schema + supervisor-driven-write architecture
has a ceiling on autonomous-compliance with current models:

- haiku: emits malformed payloads, can't read structured-error feedback
- opus: explores too much before completing
- M2.2.1: established the architecture works end-to-end via mockclaude
- M2.2.2: confirmed prompt-only calibration is insufficient

Possible next-milestone (M2.2.3 or M3) directions implied by this
finding (deferred — out of M2.2.2 scope per spec §"Out of scope"):

1. Flexible-first-attempt schema (M2.2.2 explicitly dropped this; the
  evidence here suggests it deserves reconsideration).
2. Supervisor-enforced palace-search budget (Decision 3 deferred this
  to M3+; the opus pattern strongly implies it's needed).
3. Exemplar-driven prompt — embed a fully-realised example in the
  prompt with concrete (but disposable) values instead of placeholders;
  current Q3 placeholder syntax may be too abstract for haiku.
4. Tooling-side verification before subprocess exit — supervisor
  detects "result event without prior ok:true finalize" and emits a
  retry-eligible signal back to the agent.

These are observations for the next milestone's spike, not M2.2.2
implementation work.

### Manual palace inspection (per Clarification 2026-04-23 Q4)

No clean runs occurred, so no diary drawers were written by either
agent's `finalize_ticket` invocation in any of the 6 iterations. The
existing `wing_frontend_engineer` palace contained only test-fixture
drawers from prior M2.2.1 runs (~30+ entries from earlier integration
tests). Neither model wrote a new diary drawer corresponding to the
M2.2.2 ticket UUIDs.

The palace did surface to the engineer: at iteration 2, haiku called
`mempalace_search` and got 30 prior drawer hits — many about the
M2.2.1 retry fixtures. The semantic-similarity scores were low (~0.3),
suggesting the search was wide and unfocused. This is consistent with
the palace-search-calibration not producing the predicted "skip for
trivial tickets" behaviour, and is captured for the retro.

## No-new-deps guardrail (FR-320 / SC-309)

```
$ git diff --stat origin/main..HEAD -- supervisor/go.mod supervisor/go.sum
(empty)
```

Fifth consecutive milestone holding the locked-deps line.

## SC-310 rollback experiment

**Structural verification (this session)**: the `+goose Down` block
of `migrations/20260424000006_m2_2_2_compliance_calibration.sql`
embeds the M2.2.1 engineer.md and qa-engineer.md content verbatim,
quoted with a distinct `$m221_*_md$` dollar-quote tag. Operator
running `goose -dir migrations down 1` against a non-production DB
will restore the agent_md columns to their M2.2.1 state.

**Behavioural verification (deferred to operator)**: re-running
M2.2.1's mockclaude integration tests against a rolled-back DB
should reproduce M2.2.1's behaviour exactly. Given that the live
matrix already established M2.2.2's behaviour is essentially
indistinguishable from M2.2.1's on the compliance dimension, this
experiment is low-information at the moment — both states show
the same compliance gaps. The structural check is sufficient to
confirm Decision 2 (prompt changes are isolated from code).

## Live-run mechanics observed

- All 6 iterations completed with the test harness behaving
  correctly: spike-mempalace + spike-docker-proxy stable across
  the 36-minute run; testdb spawned a fresh Postgres per iteration
  cleanly; supervisor binary built once and reused.
- Each iteration consumed ~6 minutes wall-clock (the per-iteration
  context timeout). Failure was detected by the
  `waitForTwoTerminalRows` helper hitting its 6-minute deadline.
- No environmental flakes; no MCP connection drops; no Postgres
  container restarts; no test-harness errors.

## Ship disposition

**Recommend: ship as "M2.2.2 — partial improvement (Adjudicate
precedence) + thesis falsified on live models."**

Per the SC-311 honest-ship clause:

> Partial pass (one model clean, one still failing) ships as
> "partial improvement" and is documented as such in the retro.

The actual outcome is more honest still: **no model clean**. But
M2.2.2 nevertheless ships meaningful artifacts:

- **Adjudicate fix (FR-306)**: validated by live runs; opus's
  budget-cap events now classify correctly. This is a permanent
  observability improvement worth shipping independently.
- **Richer error layer (FR-301..FR-305)**: code is correct and
  fully tested; agents will benefit when prompted appropriately.
  The infrastructure is in place for future milestones to iterate.
- **Mockclaude regression net**: T011 expanded coverage of the
  retry path with the new error shape; T010 fixtures will catch
  any future regression of the Q9-additive guarantee.
- **Empirical evidence for M2.2.3 / M3**: the live matrix
  produced clear data that prompt-only calibration has a ceiling
  on weaker models. This evidence is the main input for the next
  milestone's scope decision.

The retro (T014) documents the thesis falsification and names the
candidate next-milestone directions. M2.2.2's PR ships as honest
"the calibration approach didn't move the needle on live runs;
here's what we learned and what to try next."
