# M2.2.x compliance arc — retro

**Arc**: M2.2 → M2.2.1 → M2.2.2 → post-ship investigation
**Closed**: 2026-04-24
**Format**: plain markdown. The palace drawer mirror anticipated by AGENTS.md for M2.2-onwards retros is deferred — the arc's post-ship investigation produced the first clean finalize runs in the system, but none landed inside the milestone windows they would normally dogfood. The operator will place a canonical `wing_company / hall_events` drawer manually post-merge; this document is the authoritative content.

---

## 1. Summary

Three narrow milestones (M2.2, M2.2.1, M2.2.2) shipped a progressively stricter compliance mechanism — MemPalace writes from agent.md prompt → `finalize_ticket` tool as the only exit path → richer structured errors + prompt calibration. Each shipped with a clear empirical finding from live runs that became the next milestone's input. After M2.2.2's retro declared the calibration thesis falsified on live models, a follow-on investigation surfaced a three-bug chain in `internal/pgmcp` that had been silently contaminating the live data across all three retros. A 12-run matrix with the bugs fixed observed 4 clean / 6 partial / 2 fail under the current scoring rubric, or 6/6 mechanism-compliant on haiku and 4/6 on opus if the workspace-sandbox-escape is separated out as a supervisor bug. The compliance mechanism works on clean plumbing; it is not reliably clean across 12 runs even post-fix; the prior three retros reached correct observations from contaminated data. Zero new Go dependencies across the arc.

---

## 2. The thesis as originally posed

M2.2's thesis, paraphrasing the shipped M2.2 retro and RATIONALE §3: **the architecture's core bet is that MemPalace diary entries and knowledge-graph triples, written by agents after every ticket transition, will accumulate into cross-instance institutional memory that makes future agents smarter.** M2.2 was the milestone where the thesis became testable. If agents reliably wrote useful diaries under real work and a later agent could demonstrably benefit from a prior agent's diary, the architecture's memory bet was validated. If writes were absent, thin, or useless, the downstream roadmap would need revision before it was built.

M2.2 ship-gate language specifically named the failure mode: "If the write contract produces thin or useless entries, everything downstream has to be reconsidered before it's built."

---

## 3. The three evidence-gathering rounds

### M2.2 — MemPalace MCP wiring (shipped 2026-04-23)

Wired MemPalace into every spawn: MCP tool in every agent's per-invocation config, wake-up context injection at spawn time, hygiene checker asynchronously verifying expected writes. The engineering department's agent.md was rewritten to carry the MANDATORY completion protocol: diary entry + KG triples + then transition. Two new Postgres roles (`garrison_agent_mempalace` alongside M2.1's `garrison_agent_ro`), a socket-proxy deployment topology (`linuxserver/socket-proxy` on `tcp://garrison-docker-proxy:2375`), and a `hygiene_status` column on `ticket_transitions` with vocabulary `clean / pending / missing_diary / missing_kg / thin`.

**Live-run finding**: real `claude-haiku-4-5-20251001`, run against the 5.3K-character engineer agent.md, executed the ticket READ but did NOT call `mempalace_add_drawer` or `mempalace_kg_add` despite the prompt marking both MANDATORY. The palace remained empty post-run for the ticket. This was the exact soft-gate failure mode RATIONALE §3 + §5 anticipated.

M2.2's conclusion, verbatim from its retro: *"The memory thesis has its first real-world test: agent prompt compliance is not automatic."* The M2.2 retro called this an unsolved problem and deferred remediation to downstream milestones.

### M2.2.1 — Structured completion via `finalize_ticket` tool (shipped 2026-04-23)

Re-architected completion around a mechanism instead of a prompt. A new in-tree MCP server (`internal/finalize`) exposed a single `finalize_ticket` tool; agents could only commit a transition by calling it with a schema-valid payload; the supervisor wrote the diary and KG triples from the tool payload, not from subsequent agent tool calls. Once the tool succeeded, writes were guaranteed — the agent could not exit with a successful transition any other way.

Hypothesis: moving MANDATORY from prompt into the only available exit path removes the compliance gap by construction. "Tool-architectural compliance is stricter than prompt-based compliance by construction."

**Live-run findings**: partial validation.

- **Haiku**: reached `finalize_ticket` on attempt 1 (improvement over M2.2, where haiku didn't reach `mempalace_*` at all), but attempt 1's payload was rejected with `error_type="schema", field=""` — a decode-level rejection. Haiku did not correct and retry. It spent ~25 seconds exploring the palace via other MCP calls, then exited with a natural `result` event. No transition committed. Adjudicated as `finalize_never_called`.
- **Opus**: emitted 2 thinking + 2 non-finalize tool_use events in 6 seconds, then hit `terminal_reason="error_max_budget_usd"` at $0.2564 on a $0.20 cap. Never reached `finalize_ticket`.

M2.2.1's conclusion: the tool-as-attractor pattern worked (haiku reached for the tool without being told to), but the schema was too strict for haiku's probabilistic error-recovery and opus exhausted budget on non-finalize exploration. Strictly-better-than-M2.2 on haiku; worse on opus; neither produced a clean transition. Six follow-up candidates were named in the retro, the first five of which became M2.2.2's scope.

As a secondary finding, M2.2.1's opus run surfaced a pre-existing Adjudicate precedence bug: when Claude emitted `is_error=true + terminal_reason="error_max_budget_usd"` together, the `IsError` case fired before the `isBudgetTerminalReason` case, so budget-cap events got classified as `claude_error` rather than `budget_exceeded`. Flagged for a focused next milestone.

### M2.2.2 — Compliance calibration (shipped 2026-04-24)

Narrow patch on M2.2.1. The hypothesis: richer structured errors (line, column, excerpt, constraint, expected, actual, hint) on the handler side, plus prompt calibration (front-loaded goal, palace-search "Skip if trivial" bullets, example `finalize_ticket` payload with angle-bracket placeholders, retry framing naming the `hint` field) would move both models to reliable autonomous compliance on trivial tickets. Also: the Adjudicate precedence fix (one-line swap, `isBudgetTerminalReason` now checked before `IsError` when `ResultSeen=true`).

**Live compliance matrix**: 6 runs (haiku ×3, opus ×3) against the trivial hello-world ticket.

| Iter | Model | Exit | Cost |
|---|---|---|---|
| 1 | haiku | `finalize_never_called` | $0.0399 |
| 2 | haiku | `finalize_never_called` | $0.0574 |
| 3 | haiku | `finalize_never_called` | $0.0440 |
| 4 | opus | `budget_exceeded` | $0.2376 |
| 5 | opus | `budget_exceeded` | $0.2106 |
| 6 | opus | `budget_exceeded` | $0.2061 |

0/3 clean per model. SC-311's "≥ 2/3 clean per model" bar missed by both.

M2.2.2's retro (2026-04-24 afternoon) concluded: **thesis falsified on live models**. The prompt-only calibration did not move either model class on the trivial ticket. Two things were validated: the Adjudicate precedence fix (opus budget-cap events now classified correctly) and the richer-error infrastructure (correct and fully tested but never exercised live because nothing finalized). Five candidate directions were listed for possible future work; the operator would decide scope.

---

## 4. Where the thesis stood after M2.2.2

As of 2026-04-24 afternoon, immediately after M2.2.2's retro landed, the compliance thesis was read as **falsified on live models**. Both haiku and opus had produced 0/3 clean runs on the trivial ticket. Richer errors didn't help haiku (reached the agent, didn't change behaviour). Calibration bullets didn't help opus (budget-exhausted on palace exploration despite "Skip if trivial" prompting). Prompt-only calibration had an observed ceiling.

This was the right read given visible data at the time. Three milestones had produced consistent signal: agents don't complete trivial tickets reliably through the finalize mechanism. The retros' tone was not defeatist — the Adjudicate fix was a permanent win, the richer-error infrastructure was correct, the mockclaude-covered retry path worked. But the live matrix was the headline, and the headline was zero clean runs.

M2.2.2's retro explicitly named five next-milestone candidates (flexible-first-attempt schema mode, supervisor-enforced palace-search budget, exemplar-driven prompt, tooling-side verification before subprocess exit, hint-feedback instrumentation) and left the scope decision to the operator. The framing was "prompt-only calibration has a ceiling; here's what a next attempt could try."

That framing did not survive the investigation chain described in §5.

---

## 5. The triage-hypothesis experiment and what it surfaced

The operator launched a follow-on experiment to test a specific hypothesis: did ticket richness change agent behaviour? M2.2.2 had used one ticket shape (the operator-drafted trivial hello-world). The triage-hypothesis experiment defined two tickets — **Ticket A** (operator-drafted, included an "Expected finalize shape" hint section that effectively pre-staged the payload schema) and **Ticket B** (CEO-simulated, protocol-agnostic, no finalize hint) — and ran a 2×2×3 matrix (2 tickets × 2 models × 3 iterations = 12 runs).

A new test harness at `supervisor/experiment_triage_hypothesis_test.go`, under build tag `experiment`, shared test helpers with the existing integration suite without perturbing any production code. Data-only experiment.

**12-iteration result**: 0/12 clean across both tickets and both models. Same floor as the M2.2.2 baseline (0/6). Grand cost $1.3020 on a $3.80 cap.

Headline findings:

- **Ticket A's "Expected finalize shape" tell did not generate well-formed first attempts.** The hypothesis that richer tickets would move compliance was not supported on either ticket variant.
- **Ticket B (CEO-realistic framing) did not improve either.**
- **Both models skipped the agent.md work loop entirely.** Zero Postgres ticket reads logged. Zero workspace file writes. Effectively zero mempalace calls. Every finalize attempt was a guess at the payload shape from the agent.md's example.

That last finding was the surprise. The M2.2.2 retro's model ("opus burns budget on palace exploration") was not reproduced in the experiment; all 6 opus runs made zero mempalace MCP calls. The behaviour observed was not "over-exploration"; it was "agents didn't do any of the work."

The operator's next move was to capture the raw stream-json to understand what was actually being sent and received. A ~10-line bash `tee` wrapper (`experiment-results/tools/tee-claude.sh`) was dropped in front of the claude binary to duplicate its stdout to per-invocation JSONL files, and a 4-iteration subset reran (one of each cell). This surfaced the patch-validation observation: for each of the 4 captured iterations, the finalize payloads emitted by the agent were **well-formed**. Every rejection the agent received was:

```json
{"ok": false, "error_type": "schema", "failure": "state",
 "message": "internal error checking finalize state",
 "hint": "internal error checking finalize state; please retry",
 ...}
```

Two observations followed from this:

1. The `error_type: "schema"` label was misleading. The authoritative field was `failure: "state"` — the finalize handler's `checkAlreadyCommitted` precheck was failing *internally*, not rejecting the payload on schema grounds. Agents were dutifully retrying as the hint instructed; the precheck was failing the same way on every retry.
2. Every pgmcp SELECT across all 4 captured iterations returned "(mcp__postgres__query completed with no output)". The agent wasn't skipping the work loop; pgmcp was silently hiding ticket data.

This reframed M2.2.2's 0/6 and the triage-hypothesis 0/12 as **driven by infrastructure bugs, not by model compliance**. The retros' observations were correct about what happened on the wire; the interpretations of *why* it happened were contaminated by bugs nobody had seen.

From there, the investigation moved into the forensic described in §6. Detailed bug-level analysis is in `docs/forensics/pgmcp-three-bug-chain.md`; this retro summarises only.

---

## 6. The three-bug discovery chain

Investigation surfaced three distinct bugs in `internal/pgmcp` and its M2.1 grant surface, all of which had been latent since M2.1 or M2.2.1 and had been silently contaminating compliance signal across three milestones:

1. **Missing `agent_instances` SELECT grant for `garrison_agent_ro`.** M2.2.1's finalize precheck (`SelectAgentInstanceFinalizedState` → `status, exit_reason`) queried `agent_instances` via the finalize MCP's Postgres connection (`garrison_agent_ro`), but the role's grants — established in `20260422000003_m2_1_claude_invocation.sql` §Section 2 — never included `agent_instances`. Every finalize precheck returned `permission denied`. The handler surfaced this as `{"ok":false,"failure":"state","hint":"...please retry"}`. Agents dutifully retried; every retry failed identically; three attempts exhausted; exit `finalize_invalid`; the retros' "agent didn't finalize" observation was accurate for mechanism but mislocated for cause.

2. **`CallToolResult` envelope shape wrong.** `runQuery` and `runExplain` in `supervisor/internal/pgmcp/server.go` returned the bare domain payload (`{rows, truncated}` or `{plan}`) as the JSON-RPC result. MCP 2024-11-05 `tools/call` requires the result to be a `CallToolResult` shape — `{"content":[{"type":"text","text":<payload-json>}], "isError":false}`. Without the envelope, Claude Code's MCP client saw no content array and rendered every successful pgmcp call as "(mcp__postgres__query completed with no output)". Every pgmcp call looked like a completed-but-empty call to the agent.

3. **UUID encoding as integer array.** `normalizeValue` in `supervisor/internal/pgmcp/server.go` handled `[]byte` but not `[16]byte`. pgx's default type map returns UUID columns as `[16]byte`, which JSON-serialises as a 16-integer list (`[192, 186, 182, ...]`). Agents could not round-trip that into a follow-up query.

All three bugs were fixed as one patch in commit `59fc977`, with tests that assert the *contract* shape rather than the *observed* shape — `decodeToolResult` helper added to `pgmcp_integration_test.go` to decode the full envelope on every happy-path test; `TestNormalizeValueUUID` pinning the `[16]byte` case; a new migration `20260424000007_agent_ro_agent_instances_grant.sql` granting `SELECT ON agent_instances TO garrison_agent_ro`.

Forensic depth — how each bug stayed hidden, which prior tests passed against the buggy shape, and what test discipline would have caught each earlier — is in `docs/forensics/pgmcp-three-bug-chain.md`.

---

## 7. Post-fix validation

After the fix landed, three validation rounds ran:

**Single post-fix opus run**: a single real opus run on the trivial hello-world ticket. Result: `finalize_ticket` called on attempt 1, schema-valid payload, supervisor committed the transition, `hygiene_status=clean`. The first clean end-to-end observed run in the system. File was written to the workspace tempdir. No sandbox escape observed on this opus run.

**Single post-fix haiku run** (ticket `28cff177-ab5e-4df9-aadd-585fb230d0d6`): haiku also finalized cleanly on attempt 1 with a schema-valid payload and all the supervisor-side writes committing as expected. **Sandbox-escape side effect**: the engineer wrote `changes/hello-<ticket_id>.md` to `~/changes/` (the operator's home directory) instead of to the workspace tempdir. The claim in the finalize payload's `artifacts` field listed the workspace-relative path; disk truth was the host-home absolute path. This surfaced the orthogonal workspace-sandboxing concern now tracked at `docs/issues/agent-workspace-sandboxing.md`.

**12-run post-UUID-fix compliance matrix** (`experiment-results/matrix-post-uuid-fix.md`): 6 haiku + 6 opus runs on Ticket A only. Grand cost $0.4535 reported (with a cost-telemetry blind spot — see §11).

| Bucket | Haiku | Opus | Total |
|---|---|---|---|
| Clean | 0 | 4 | 4 |
| Partial | 6 | 0 | 6 |
| Fail | 0 | 2 | 2 |

Under Framing A (current scoring rubric):

- Haiku: 6/6 ran the mechanism cleanly, finalized on attempt 1 or 2, produced schema-valid payloads with substantive diaries; **all 6 wrote their artifact to `~/changes/` instead of the workspace**. Scored as partial because the on-disk location was wrong.
- Opus: 4 clean (finalized on attempt 1, wrote to workspace, hygiene_status=clean), 1 budget-exceeded at the finalize boundary (attempt emitted but not committed before `terminal_reason="error_max_budget_usd"` at $0.3250 over a $0.30 cap), 1 `finalize_never_called` (emitted own `result` event with `subtype="success"` at $0.1285 without ever reaching the completion tool).

Four fixed-bug signatures (`envelope_empty_output`, `uuid_int_array`, `agent_instances_permission_denied`, `finalize_state_rejection`) scanned zero occurrences across all 12 runs. The M2.2.2 retry path fired exactly once (haiku run 6, attempt 2 succeeded) and committed cleanly — the mechanism works but is not load-bearing on this sample.

A pre-existing, separate opus-only issue surfaced three times: the qa-engineer emitted `SELECT id, objective, acceptance_criteria, status FROM tickets WHERE id = ...`. The `tickets` table has no `status` column (uses `column_slug`). pgmcp returned `column "status" does not exist (SQLSTATE 42703)`. Opus recovered each time (the other columns were enough for the review). Haiku never emitted this query. Not one of the three fixed bugs — a separate opus agent misconception about the schema, present in 50% of opus's qa phases.

---

## 8. Thesis outcome — both framings

The 12-run matrix result can be read two ways. Both are documented here without advocacy.

### Framing A — current scoring rubric

The scoring rubric in `matrix-post-uuid-fix.md` defines Clean as "acceptance criteria met (file at expected workspace path, ≥50 chars, references ticket UUID, no placeholder) AND substantive finalize." Haiku's 6 sandbox-escape runs counted as partial because the on-disk location was `~/changes/`, not the workspace `changes/`.

**Numbers under Framing A**: 4 clean / 6 partial / 2 fail out of 12.

Implication: the compliance mechanism works on clean plumbing (every haiku run produced a substantive finalize with schema-valid payload; opus finalized on 4/6 runs) but is not reliably clean across 12 runs. The sandbox-escape issue keeps workspace isolation on the critical path alongside whatever comes next.

### Framing B — environmental-bug view

Under Framing B, the workspace sandbox-escape is a supervisor-side bug (the supervisor doesn't `chdir` the claude subprocess into the workspace tempdir; the subprocess inherits the operator's shell cwd and resolves relative paths against `~`). The agent's behaviour is otherwise mechanism-compliant: haiku finalized with a schema-valid payload, substantive diary, non-empty artifacts field, KG triple emitted, transition committed, `hygiene_status=clean`. The only wrongness is file placement — which the agent cannot fix without first running `pwd`, which haiku reliably does not do.

**Numbers under Framing B**: haiku 6/6 mechanism-compliant (all finalized, all transitioned, all hygiene-clean). Opus 4/6 mechanism-compliant (1 budget-exceeded, 1 finalize_never_called). Total 10/12 mechanism-compliant.

Implication: the compliance mechanism is near-ceiling on both models for Ticket A. Haiku's sandbox escape is a supervisor fix (cwd handling in `internal/spawn`), not a compliance failure. The mechanism is reliable enough to anchor M2.3 kickoff on.

### Which decisions depend on which framing

- **M2.3 (vault) kickoff sequencing**: Framing B makes the compliance mechanism look trustworthy enough for M2.3 to build on, because vault threat modelling assumes the agent-spawn path is reliable. Framing A also permits M2.3 kickoff (the sandbox escape is orthogonal to vault per `docs/issues/agent-workspace-sandboxing.md`), but keeps sandbox work visible on the critical path.
- **M3 dashboard priorities**: Framing A pushes the sandbox-escape view up in the hygiene dashboard's priority list — the dashboard should surface "artifact claimed vs artifact on disk" as a first-class failure mode. Framing B pushes it down — the sandbox is a supervisor fix that, once done, removes the signal from the dashboard entirely.
- **Operator observability telemetry**: both framings want cost-telemetry-on-clean-finalize fixed (see §11). Framing B makes it more urgent because "clean-finalize rate" becomes the primary success metric and that metric is currently blind on cost.

The retro does not pick a framing. Both numbers are presented because the system's eventual users (the operator, and eventually the dashboard and the CEO) will care about one or the other depending on what they're asking.

---

## 9. What the M2.2.2 retro got wrong

M2.2.2's retro concluded that the calibration thesis was falsified on live models. That conclusion is load-bearing in the retro's headline, its "What didn't work" section, and its framing of the five next-milestone candidates.

**That conclusion was drawn against contaminated data.**

Specifically:

- The 6/6 runs the M2.2.2 matrix observed all included the envelope bug (every pgmcp SELECT returned "completed with no output" to the agent) and the agent_instances permission-denied bug (every finalize precheck returned "internal error...please retry"). The envelope bug silently erased ticket data the agent would have read. The permission-denied bug silently erased the agent's successful retry attempts.
- The UUID bug was latent but not fired in the M2.2.2 matrix runs specifically — those runs' pgmcp queries didn't exercise UUID columns in a round-trip path. The bug was active; it just didn't happen to bite M2.2.2.
- Under the clean plumbing that landed post-investigation, the compliance mechanism works. Framing B puts haiku at 6/6 mechanism-compliant on the same ticket shape M2.2.2 tested. The M2.2.2 retro's "calibration thesis falsified" read does not survive when the data under it is clean.

The distinction worth preserving: **M2.2.2's retro is not wrong about what it observed. It is wrong about what the observation meant — because the observation was contaminated by invisible bugs.**

The observation ("haiku made 1-2 finalize attempts, got rejected, didn't successfully retry; opus exhausted budget without reaching finalize") was a true description of what happened on the wire. The interpretation ("prompt-only calibration has a ceiling on weaker models") was a reasonable best-guess at cause given what was visible. The *cause* — precheck returning a permission-denied error disguised as a `state` rejection, envelope silently hiding ticket reads — was not visible. The retro could not have concluded this without the stream-json capture the operator ran the next day.

M2.2 and M2.2.1's retros have a weaker but similar contamination. M2.2's "haiku skipped MANDATORY writes" conclusion was drawn with the envelope bug eating query output, which plausibly contributed to haiku giving up on the protocol (the palace-search queries it ran in response to the schema rejection would have returned empty under the envelope bug, reinforcing "there's nothing here for me"). M2.2.1's "malformed JSON on finalize" conclusion was drawn with the permission-denied bug consuming budget before finalize could succeed — the agent was spending retry attempts on a precheck that always failed, not on a schema it couldn't satisfy. Three full milestones' retros concluded things that were partly or wholly artifacts of the bug chain.

The retros were correct about the mechanism-level observations and the permanent architectural improvements they shipped (tool-as-completion-mechanism, richer-error infrastructure, Adjudicate precedence fix, hygiene-status vocabulary). What they were wrong about was what those mechanism-level observations implied about model capability and architecture direction.

---

## 10. What the arc actually validated

Six things come out of this arc as empirically validated:

1. **The `finalize_ticket` tool as a completion mechanism works.** M2.2.1's central design — tool-as-only-exit-path, supervisor-writes-from-payload — does what it was designed to do. Post-fix matrix: 10 successful finalizes across 12 runs produced schema-valid payloads, substantive diaries, KG triples, and atomic supervisor-side commits. Haiku reliably reaches for the tool without prompting. Opus reaches for it when not budget-exhausted or prematurely-exited.

2. **The richer-error infrastructure from M2.2.2 is correct.** It fired once in the 12-run matrix (haiku run 6, attempt 2 succeeded after attempt 1 rejection) and worked as designed — the agent read the hint, corrected, and retried successfully. Once-in-12 is low volume but the mechanism is wired end-to-end and produced the right behaviour on the one occasion it was needed. The M2.2.2 retro's worry ("we don't know if better hint wording would have helped haiku because we never observed haiku reading a hint and acting on it") now has one positive observation.

3. **pgmcp as an in-tree MCP server pattern works, but had latent bugs that hid compliance signal across multiple milestones.** The pattern itself is sound — stdio JSON-RPC, Postgres-role-level read-only enforcement, defence-in-depth SELECT filter at the protocol layer. The execution had three bugs that survived because the happy-path integration test asserted the wrong envelope shape. Forensic-level detail in the companion document.

4. **The Adjudicate precedence fix is a permanent win.** Validated by live evidence in M2.2.2 (opus runs classified as `budget_exceeded` instead of `claude_error`) and again in the post-UUID-fix matrix (opus run 7 similarly classified). This is in production and stays.

5. **The compliance mechanism is not yet reliably clean across 12 runs under Framing A.** Partial and fail outcomes exist. Opus's `finalize_never_called` in run 10 ($0.1285, well under budget, emitted a `result` event with `subtype="success"` without ever calling the tool) is a new failure mode the prior matrices didn't surface. Opus's qa-engineer `column "status"` misconception fires 50% of the time. Haiku's sandbox escape fires 100% of the time.

6. **Under Framing B, mechanism-level compliance is near-ceiling on both models for Ticket A.** Haiku 6/6, opus 4/6. The mechanism-level read is that the compliance architecture is real enough to anchor downstream work on.

Five of these were partially or tentatively visible before the investigation but are now empirically grounded. One (pgmcp's latent-bug cost) is a new finding unique to the investigation.

---

## 11. Open concerns

Two concerns noted during the arc that do not become separate tracked issues and do not block M2.3.

**Cost telemetry blind on clean finalizes.** The supervisor signal-kills the claude subprocess on the terminal transition signal *before* claude emits its own `result` event. `agent_instances.total_cost_usd` is populated from that `result` event's `total_cost_usd` field. Consequently, every clean-finalize run records $0.00 cost. The 10/12 successful-finalize runs in the post-UUID-fix matrix all show zero cost; real spend is non-zero and roughly estimable from prior M2.2.2 retro rates (haiku ~$0.047/run, opus ~$0.215/run for analogous shapes) — in the post-UUID-fix matrix, roughly $1.14 of un-recorded spend against the $0.4535 of recorded spend (so real matrix cost ≈ $1.59 on a $3.80 cap). Impact: any cost-based SLO on clean runs is currently blind. Fix is a supervisor-side signal-handling change that lets the `result` event land before the kill; noted for future observability work.

**Column-not-exist schema misconception on opus's qa-engineer.** Opus emits `SELECT id, objective, acceptance_criteria, status FROM tickets WHERE id = ...` in 3/6 post-UUID-fix qa-engineer runs. The `tickets` table has no `status` column. pgmcp returns `column "status" does not exist`. Opus recovers (the other columns give qa enough to review). Haiku never makes this query (0/6). The hypothesis is that opus's training prior assumes a `status` column that Garrison's schema doesn't have. Fix candidates are either a targeted qa-engineer.md note clarifying the ticket schema, or a broader seed-agent.md schema reference. Not pursued now; not on the critical path.

Neither blocks M2.3.

---

## 12. Dependencies added

**Zero new Go dependencies across M2.2, M2.2.1, M2.2.2, and all three pgmcp fixes.** Sixth consecutive milestone (counting M1, M2.1, M2.2, M2.2.1, M2.2.2, and the post-ship pgmcp fix) holding the locked-deps line.

`supervisor/go.mod` and `supervisor/go.sum` diff against `main` at arc close: empty.

Non-Go deployment-layer additions across the arc, for completeness — all established in M2.2 and unchanged since:
- `docker-cli` in the supervisor Dockerfile (runtime binary, not a Go import)
- `mempalace 3.3.2` + transitive Python deps in `Dockerfile.mempalace`, hash-pinned
- `procps` in `Dockerfile.mempalace` (operator convenience)
- `ghcr.io/linuxserver/socket-proxy` as a compose service (digest-pinned)

None of these count against the locked list.

---

## 13. Readiness for M2.3

**M2.3 (vault / Infisical integration) is unblocked.**

The compliance work across M2.2.x established that the mechanism is real enough for M2.3's threat model to build on. M2.3's threat model (`docs/security/vault-threat-model.md`) assumes the supervisor can reliably spawn an agent with a predictable exit contract — agents either finalize (mechanism-compliant) or exit via a known failure mode (`budget_exceeded`, `finalize_never_called`, `finalize_invalid`). Both conditions hold under clean plumbing.

The sandbox-escape issue (`docs/issues/agent-workspace-sandboxing.md`) is explicitly **orthogonal** to vault. The vault assumes the supervisor has control over what the agent can see; sandbox-escape is about what the agent can *reach* beyond its workspace. Both need eventually, neither blocks the other. The issue document specifies that M2.3's acceptance tests note workspace isolation is NOT tested by M2.3.

Planned sequencing for the workspace-sandboxing work: post-M2.3, as Docker-per-agent containerisation. Container-per-spawn gives the supervisor a hard boundary for the agent's filesystem, network, and injected secrets; the vault's env-var injection and the workspace's cwd isolation become the same mechanism (inject into the container, not into a shared host process). Interim mitigation options (Option A: `chdir` the supervisor subprocess; Option B: run as a different OS user; Option C: accept the gap) are documented in the issue; operator call on which to apply before the Docker work lands.

Other things that cleared during the arc:
- The `garrison_agent_ro` grant surface is now complete (includes `agent_instances` as of commit 59fc977). M2.3 can extend this role or introduce new roles without reworking the grant discipline.
- The M2.2 socket-proxy topology is stable and documented in the vault threat model as an existing assumption, not a new concern.
- M2.2's config.AgentMempalacePassword and M2.1's config.AgentROPassword reading patterns are the shape Infisical will replace at M2.3 — env-var today, Infisical-fetch tomorrow.

---

## 14. What future compliance work would need

If and when compliance becomes load-bearing again (M7 hiring flow, M8 product surfaces), the arc suggests specific follow-ups future-you will want to reach for. These are not commitments and not pre-decided work:

1. **Docker-per-agent for sandbox isolation.** Post-M2.3. Fixes the haiku workspace-escape pattern, collapses the vault's env-var injection path and the workspace's cwd isolation into one mechanism. Enables meaningful "artifact claimed vs artifact on disk" verification in the supervisor.

2. **Real cost telemetry through supervisor signal-handling.** Let the claude subprocess emit its `result` event before the supervisor kills it on terminal transition. Populates `agent_instances.total_cost_usd` on clean finalizes. Unblocks any cost-based SLO or per-ticket cost accounting.

3. **Ticket B-style control runs on non-drafted tickets.** The post-UUID-fix matrix ran only Ticket A. The triage-hypothesis experiment's Ticket B (CEO-simulated, no finalize hint) was not re-run with clean plumbing. If the mechanism's reliability on Ticket B differs from Ticket A, that's a signal the operator-drafted tell is doing load-bearing work.

4. **Multi-ticket-shape coverage beyond the hello-world ticket.** The entire arc used one ticket shape. Real work covers a wider surface — tickets with multi-file changes, tickets requiring palace search, tickets that span qa, tickets with cross-department dependencies. Compliance rates on those shapes are not yet measured.

5. **Opus qa-engineer schema reference.** The column-not-exist misconception fires in 50% of opus's qa phases. A targeted agent.md addition (explicit ticket schema in the qa-engineer prompt) would likely eliminate the query errors even though they don't currently affect outcomes.

6. **Hint-feedback instrumentation.** M2.2.2 shipped the richer-error infrastructure but couldn't iterate on hint copy because the live matrix produced no successful retries. Post-UUID-fix has one observation. A telemetry path that records which hint variants produce successful retries would let future milestones iterate empirically.

None are pre-committed. All are things the arc's data makes specifiable.

---

## Arc close

The M2.2.x arc closes with the compliance mechanism validated on clean plumbing, three prior retros' interpretations revised in light of contaminated data, three pgmcp bugs fixed with test discipline that asserts contract shape rather than observed shape, one new standalone issue tracked (agent workspace sandboxing, post-M2.3), and zero new Go dependencies across the arc. The memory thesis from RATIONALE §3 remains the long-term question the architecture bets on; the finalize-mechanism work is the compliance mechanism that supports it. M2.3 kicks off next.
