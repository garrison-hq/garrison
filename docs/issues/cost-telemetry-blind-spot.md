# Cost telemetry blind on clean finalizes — standalone issue

**Status**: tracked for post-M2 work (M3 or later — operator scope call).
**Surfaced**: 2026-04-24, post-UUID-fix 12-run compliance matrix.
**Scope**: observability / cost accounting. Does NOT affect functional
correctness, mechanism compliance, or the vault path. Pure telemetry bug.

## The finding

`agent_instances.total_cost_usd` records `$0.00` on every clean-finalize
run. The 12-run post-UUID-fix matrix
(`experiment-results/matrix-post-uuid-fix.md`) made the gap visible:

- **Recorded matrix cost**: $0.4535 (across 12 runs)
- **Estimated real spend**: ~$1.59 (using haiku ~$0.047/run and
  opus ~$0.215/run from M2.2.2 baselines for analogous ticket shapes)
- **Un-recorded delta**: ~$1.14 — every successful run shows $0.00

Non-finalize exits (`finalize_never_called`, `budget_exceeded`,
`claude_error`) record their cost correctly. Only **clean** finalizes
are blind.

## Root cause

In `internal/spawn`, when the finalize commit succeeds the supervisor
fires the terminal-transition signal that kills the claude subprocess
**before** claude emits its own `result` event. The
`agent_instances.total_cost_usd` column is populated from that
`result` event's `total_cost_usd` field, parsed by the stream-json
pipeline. No `result` event → no parsed cost → `$0.00` written.

Non-finalize exits don't trigger the early signal-kill, so the
`result` event lands naturally and cost is captured.

## Three concerns this surfaces

1. **Cost-based SLOs are blind on success.** Any "alert if average
   per-ticket cost > $X" rule reads $0.00 for every healthy run and
   the only signal it ever sees is failure-mode cost.
2. **Per-ticket cost accounting is wrong on the common case.** A
   future "what did this ticket cost to complete" UI cannot be built
   from the current column.
3. **Aggregate cost reports systematically under-report.** Clean runs
   are the bulk of healthy-system traffic; their absence skews
   roll-ups. Anthropic dashboard cross-check (M2.2 SC-212 deferred
   item) cannot be done while this gap exists.

## Relationship to other concerns

- **Compliance mechanism**: orthogonal. The mechanism works; cost
  telemetry is a separate read-the-result-event bug.
- **M2.3 vault**: orthogonal. Vault doesn't touch cost capture.
- **Workspace sandboxing**
  (`docs/issues/agent-workspace-sandboxing.md`): orthogonal. Both are
  observability/correctness gaps to fix on the supervisor side, but
  in different files and with different mechanisms.

## Planned resolution

Supervisor-side signal-handling change in `internal/spawn`. Two
candidate shapes:

- **Option A — short grace window before kill.** Post-finalize, set
  a small grace (e.g. 1–2s) for claude to emit `result` naturally;
  only signal-kill if the grace expires. Bounded blast radius;
  doesn't slow down the failure path (still kills immediately on
  non-finalize exits).
- **Option B — read result event then kill.** Block the terminal
  transition write on either (a) `result` event seen, or (b) a
  hard timeout. Cleaner semantically but couples the spawn-write
  path to the pipeline-read path.

Option A is the cheaper fix; Option B is the more correct one.
Operator picks when this work lands.

## Interim mitigation

None recommended. The data is honest about its blind spot — every
recorded $0.00 is recognizable as "clean-finalize, real cost
unknown." Don't paper over it with estimates in the column.

For matrix-style experiments that need real cost numbers now: use
the per-iteration `t.Logf` pattern from
`integration_m2_2_2_compliance_test.go` to capture cost from the
stream-json directly before the supervisor's signal-kill races it.

## Acceptance criteria for "resolved"

This issue is resolved when:

- Clean-finalize runs record non-zero `total_cost_usd` matching
  Claude's `result` event within rounding.
- Aggregate cost across a 10+ run sample matches the Anthropic
  billing dashboard within 5% (the SC-212 cross-check from M2.2 can
  finally be performed).
- Spawn cleanup wall-clock time does not regress beyond the chosen
  grace window (~2s ceiling).
- A test asserts `total_cost_usd > 0` on a successful finalize path
  using the real-claude-via-mockclaude harness.

## Related files

- `docs/retros/m2-2-x-compliance-retro.md` §11 — first surfaced
- `experiment-results/matrix-post-uuid-fix.md` — quantified the gap
- `internal/spawn/spawn.go` — where the terminal-signal-kill lives
- `internal/spawn/pipeline.go` — where the `result` event would be
  read if the kill didn't race it
- `internal/claudeproto` — `result` event type with
  `total_cost_usd` field
