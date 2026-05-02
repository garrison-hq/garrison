# 20-run cost-telemetry validation — tracked-not-fixed in M6

**Status**: deferred from M6 to a post-M6 polish round.
**Surfaced**: 2026-05-02 by `/speckit-analyze` finding C1 against
`specs/015-m6-decomposition-hygiene-throttle/`.
**Scope**: pure verification gap; does NOT affect functional correctness.

## The gap

`spec.md` §SC-001 promises:

> For a corpus of 20 successful clean-finalize runs after FR-020 lands, the recorded `agent_instances.total_cost_usd` sum is within ±5% of the operator's claude account dashboard total for the same window. (Cost-telemetry blind-spot closed.)

`tasks.md` §T020 walks the success criteria as part of the M6 acceptance check, but explicitly notes:

> For SC-001, a 20-run empirical pass is impractical to script in a single task; in lieu, T020 verifies a 5-run sample matches within ±10% (looser bound for the smaller sample) and documents the deferred 20-run pin in the retro.

So the M6 ship verifies the cost-telemetry fix *qualitatively* (the numbers are non-zero on success and within rough tolerance over 5 runs), but the *quantitative* 20-run pin promised by the spec lands later.

## Why the gap exists

The 20-run validation requires:

- 20 real-claude (not mockclaude) ticket executions in the same calendar window.
- Operator-verified totals from claude.ai's billing dashboard for the same window.
- A reproducible test harness that injects the 20 runs without contaminating production telemetry.

A single Claude-Code session cannot carry the operator wall-clock time for the runs to actually execute, plus the dashboard cross-reference is a manual step. Running this as a scripted task would either (a) take hours of supervised real-claude time, or (b) ship a mockclaude harness whose cost numbers are by-construction matched to the recorded values — verifying nothing.

## Resolution path

A follow-up task in a post-M6 polish round (or as the first task of an M6.1 / M7-adjacent micro-milestone):

1. Operator runs 20 successful clean-finalize tickets across a 24-hour window with the M6 binary deployed.
2. Operator queries `SELECT SUM(total_cost_usd) FROM agent_instances WHERE started_at >= NOW() - INTERVAL '24 hours' AND status='succeeded' AND exit_reason='completed'`.
3. Operator pulls the same-window total from claude.ai's billing dashboard.
4. Compute the delta. If within ±5%, the SC-001 pin is satisfied — record in `docs/retros/m6-1.md` (or wherever the polish round lands).
5. If outside ±5%, open a bug — the cost-telemetry fix has a residual hole.

Estimated operator wall-clock for the validation pass: 1-2 hours of monitoring across a real-traffic day.

## Until then

`docs/retros/m6.md` carries the deferred-validation note as part of the retro's "open questions deferred to the next milestone" section. Cost-related operator decisions in the M6→M7 window should be informed by the 5-run sample (within ±10%) rather than treating the 20-run pin as already in place.

## Cross-references

- `docs/issues/cost-telemetry-blind-spot.md` — the original M2.x telemetry-blind-spot issue that M6 (FR-020 / US2) closes structurally.
- `specs/015-m6-decomposition-hygiene-throttle/spec.md` SC-001 — the 20-run pin this issue tracks.
- `specs/015-m6-decomposition-hygiene-throttle/tasks.md` T020 — the acceptance walk that ships the 5-run interim.
