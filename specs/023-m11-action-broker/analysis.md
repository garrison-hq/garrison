# Specification Analysis Report — M11 Action Broker (re-check after fixes)

**Milestone**: M11 — Action Broker (outbound external actions, gated by policy)
**Branch**: `023-m11-action-broker`
**Artifacts analyzed**: `spec.md`, `plan.md`, `tasks.md` (plus `m11-context.md` + `.specify/memory/constitution.md`)
**Run type**: Re-check — verifying prior findings are resolved and checking for regressions
**Date**: 2026-06-12
**Analyst**: speckit-analyze (non-interactive; operator absent)

---

## Resolution status of prior findings

| Prior ID | Was | Resolution |
|----------|-----|------------|
| I1 (HIGH) — `pending_action_outcomes` grant self-contradiction in plan.md | "gets `SELECT` only" opening then explains `INSERT` is also needed | **RESOLVED**: plan.md line 202 now reads "gets `SELECT, INSERT`" with consistent justification. No "SELECT only" language remains. |
| I2 (MEDIUM) — `TestHandleNeverExecutesHumanOnly` dead-code path not documented | claim filter excludes `human_only`; test behaviour was opaque | **RESOLVED**: tasks.md T009 (line 104) now carries an explicit implementation note: "The test MUST inject the row directly into `Handle` by using a stub `ClaimDispatchablePendingAction` that returns the row regardless (bypassing the claim filter) — this is the defence-in-depth path." plan.md (line 312) carries a matching note. |
| I3 (MEDIUM) — migration canonical location stated inconsistently | plan.md body and §Project Structure showed `migrations/` root | **RESOLVED**: plan.md D15 (line 52), §Project Structure (line 109), and §Phase 1 deployment (line 409) all now show `supervisor/cmd/supervisor/migrations/` as canonical with `make copy-migrations` mirroring to root. Consistent with tasks.md T002 and M10 precedent. |
| I4 (LOW) — `'classified'` orphaned in outcome CHECK | plan.md data model block included `'classified'` with no writer | **RESOLVED**: plan.md D17 (line 54) and the `pending_action_outcomes` SQL block (line 189) both now explicitly state `'classified'` is NOT in the enum and document why (tier is set on the `pending_actions` row, not as an outcome). |
| A1 (MEDIUM) — `pg_notify` payload unspecified in D18 | "emit `work.action.dispatch_requested`" with no payload stated | **RESOLVED**: plan.md D18 (line 55) now explicitly states "The `pg_notify` **payload is the `pending_actions.id` UUID string** (matching the `mcpserverwork` precedent: `Handle(ctx, eventID pgtype.UUID)` looks up the specific row by that ID). The poll fallback passes a zero UUID; `Handle` falls back to querying for any unprocessed dispatchable row when the payload is zero." The verb handler (line 254) and approve Server Action (line 329) both state the UUID payload explicitly. |
| A2 (LOW) — FR-009 out-of-sequence in spec.md | FR-009 appeared after FR-015 interrupting Thread 2 block | **NOT FIXED** (as expected — flagged LOW with zero functional impact; remains as-is). |
| C1 (MEDIUM) — `AGENTS.md` update not in any task | bound by plan.md but absent from T001 completion condition | **RESOLVED**: tasks.md T001 (line 38) now explicitly lists `AGENTS.md` in the Files section with three named edits: advance current milestone to M11, advance the binding-documents table, and add the sealed-surfaces entry for `request_external_action` + `work.pending_actions` + `work.pending_action_outcomes` + `actionbroker` dispatcher. Completion condition (line 39) confirms this commit is part of T001. |
| C2 (LOW) — `auto`/`notify` row at `status='approved'` unguarded | theoretically impossible state silently accepted by claim | **ACKNOWLEDGED IN PLAN, NOT GUARDED**: plan.md line 220 now explicitly documents this as "a theoretically impossible `auto`-tier row at `status='approved'`… there is no bug… the claim permits it rather than guarding against it, which is intentional." Acceptable posture; no DB CHECK added (low risk). |
| C3 (LOW) — drizzle vs. sqlc split for `human_only`/notify reads not documented in tasks | T012 implementers had to infer the split | **RESOLVED**: tasks.md T012 (line 146) explicitly lists `dashboard/lib/queries/outbox.ts` as carrying "**dashboard-side drizzle queries** for the Outbox page reads: `listPendingApproveActions`, `listHumanOnlyPreparedActions`, `listNotifyPostHocFeedItems`, `listPendingActionOutcomes`; these are drizzle queries, NOT sqlc — the Go-side sqlc queries in `store/m11_action_broker.sql.go` are the supervisor/dispatcher path only." |
| D1 (LOW) — intentional duplication in Assumptions vs. Clarifications | deliberate navigational summary | **NO ACTION REQUIRED** (as stated in prior report). |
| F1 (LOW) — FR numbering gaps | FR-016 and FR-029 absent without explanation | **RESOLVED**: spec.md line 183 now carries an explicit note: "Note on FR numbering: FR-016 and FR-029 are absent (gaps between FR-015→FR-017 and FR-028→FR-030). These numbers were not allocated; all named FRs are present and the thread structure is complete. The gaps have no functional impact." |

---

## Findings Table (new findings from re-check)

| ID | Category | Severity | Location(s) | Summary | Recommendation |
|----|----------|----------|-------------|---------|----------------|
| R1 | Inconsistency | MEDIUM | spec.md:174, 192 vs. plan.md:54, 188, 312; tasks.md:45, 72, 104 | `skipped-human-only` (hyphenated) appears in spec.md FR-024 and Key Entities descriptions, while plan.md D17, the SQL CHECK block, and tasks.md all consistently use `skipped_human_only` (underscored). The DB CHECK in the migration uses the underscore form (tasks.md T002 completion condition). The spec's prose uses the hyphenated form but the implementation is driven by plan.md and tasks.md. A reader comparing spec FR-024 to the migration CHECK finds conflicting strings. | Fix spec.md FR-024 (line 174) and Key Entities (line 192) to use `skipped_human_only` (underscore) so all three artifacts agree on the exact string that lands in the DB. Two-occurrence fix; no functional impact as the tasks and plan are already consistent. |
| R2 | Ambiguity | LOW | spec.md FR-009 | FR-009 (deploy-time tier editability) remains out-of-sequence after FR-015, interrupting the Thread 2 FR-010..FR-015 block. Carried from prior run (A2 — zero functional impact). | Reorder FR-009 immediately before FR-010, or accept as-is. Cosmetic only. |

No new HIGH or CRITICAL findings. No regressions from the fix pass were introduced. R1 is a terminology drift between the spec's prose and the implementation's DB literal — functionally harmless (tasks.md and plan.md are consistent with each other on the underscore form), but worth fixing so the spec is the authoritative source for the exact enum value.

---

## Coverage Summary Table

All 27 FRs and 9 SCs remain fully covered. No coverage regressions.

| Requirement Key | Has Task? | Task IDs | Notes |
|-----------------|-----------|----------|-------|
| FR-001 (sealed verb registration, 12th) | Yes | T005 | `TestVerbsRegistryMatchesEnumeration` updated to 12 verbs |
| FR-002 (agent-only caller) | Yes | T005, T008 | `TestRequestExternalActionRejectsChatCaller` |
| FR-003 (verb writes one pending row, no action) | Yes | T005, T008 | `TestRequestExternalActionWritesExactlyOnePendingRow` |
| FR-004 (typed queued result) | Yes | T008 | `TestRequestExternalActionReturnsQueuedResult` |
| FR-005 (ignore agent-supplied tier) | Yes | T008 | `TestRequestExternalActionIgnoresAgentSuppliedTier` |
| FR-006 (joins garrison-mutate, not new server) | Yes | T005 | `agentVerbNames()` += `request_external_action`; joins existing surface |
| FR-007 (squid.conf unchanged) | Yes | T007, T014 | `squid.conf` byte-for-byte unchanged; SC-004 git-diff assertion |
| FR-008 (external action supervisor-side only) | Yes | T006, T007 | Dispatcher HTTP client constructed supervisor-side in `main.go` |
| FR-009 (deploy-time tier config) | Yes | T004 | Compile-time constants in `policy.go`; no operator edit surface |
| FR-010 (four tiers defined) | Yes | T004, T009 | `Tier` type + four constants; unit tests cover each path |
| FR-011 (tier in table/code not prompts) | Yes | T004, T008 | Policy map; `RequestExternalActionArgs` struct has no tier field |
| FR-012 (tier reason recorded) | Yes | T005 | `tier_reason` column; `Classify` returns reason string |
| FR-013 (tier fixed at write time) | Yes | T005, T008 | `tier` written from `Classify` at `InsertPendingAction` time |
| FR-014 (permanent-Approve floor) | Yes | T004, T005, T011 | Three layers: `floor` map, `Classify` order, DB CHECK |
| FR-015 (unclassified → approve default) | Yes | T004, T008 | `TestClassifyUnknownDefaultsApprove` |
| FR-017 (dispatcher never executes human_only) | Yes | T006, T009 | `ClaimDispatchablePendingAction` filters `tier <> 'human_only'`; `TestHandleNeverExecutesHumanOnly` via injected stub |
| FR-018 (reactive dispatcher, pg_notify + poll) | Yes | T006, T007, T009 | `RunLoop` + `Channel` constant; `pg_notify` + poll fallback; `slog` logging |
| FR-019 (vault-scoped PAT credential) | Yes | T006, T009 | `VaultFetcher` interface; `TestHandleVaultUnavailableFailsClosed` |
| FR-020 (GitHub REST via stdlib net/http) | Yes | T006, T009 | `github.go` stdlib `net/http`; `TestPostCommentBuildsCorrectRequest` |
| FR-021 (exactly-once dispatch) | Yes | T010, T014 | `TestConcurrentClaimDispatchesExactlyOnce`; SC-006 acceptance |
| FR-022 (no auto-retry, mark failed) | Yes | T009 | `TestHandleRecoverableFailureMarksFailedNoRetry` |
| FR-023 (fail-closed if vault unavailable) | Yes | T009 | `TestHandleVaultUnavailableFailsClosed` |
| FR-024 (immutable outcome history) | Yes | T002, T009, T011 | `pending_action_outcomes` table; outcome inserts in dispatcher |
| FR-025 (Outbox dashboard surface) | Yes | T012 | `page.tsx` Server Component at `/admin/outbox` |
| FR-026 (Approve/Reject as Server Actions) | Yes | T012 | `actions.ts`; `ServerActionVerbs` entries in T005 |
| FR-027 (human_only mark-as-done in Outbox) | Yes | T012 | `markActionDone` Server Action; `human_only` section in `page.tsx` |
| FR-028 (notify-tier post-hoc feed) | Yes | T012 | `notify`-tier post-hoc feed section in `page.tsx` |
| FR-030 (threat model before dispatcher code) | Yes | T001, T014 | T001 is first task; SC-008 git-log check in acceptance script |
| SC-001 | Yes | T008, T011 | |
| SC-002 | Yes | T009, T011 | All four tier paths tested |
| SC-003 | Yes | T004, T008, T011 | Three enforcement layers; `TestFloorEnforcedAtDB` |
| SC-004 | Yes | T007, T014 | `squid.conf` unchanged; git grep no agent-env PAT |
| SC-005 | Yes | T009 | `TestPostCommentNeverLogsPAT` + vault fail-closed |
| SC-006 | Yes | T010 | Chaos tests |
| SC-007 | Yes | T011 | Audit reconstruction in integration test |
| SC-008 | Yes | T001, T014 | T001 carries threat model; git-log check in acceptance script |
| SC-009 | Yes | T005, T014 | Registry enumeration tests; `go mod tidy` check |

---

## Constitution Alignment Issues

None. All nine constitution principles hold; no violations introduced by the fixes.

- **Principle I**: `pending_actions` is source of truth; dispatcher reactive on `work.action.dispatch_requested`; poll fallback present. Compliant.
- **Principle III**: Pending row carries all dispatch/audit context; requesting agent may be gone. Compliant.
- **Principle VI**: Approve/Reject/done are dashboard Server Actions; no git PR path. Compliant.
- **Principle VII**: Zero new Go dependencies; GitHub call is stdlib `net/http`. Compliant.
- **Principle VIII**: `RunLoop(ctx, deps)` + errgroup; context cancellation respected. Compliant.
- **M2.2.1 mechanism-over-prompt**: tier lives in `policy.go` code constants, never in prompts. Compliant.
- **Sealed-surface discipline**: one sealed verb added the M5.3/M8/M9 way; threat model precedes code; no existing sealed surface mutated. Compliant.

---

## Unmapped Tasks

None. All 15 tasks (T001–T015) map to at least one FR or SC.

---

## Metrics

| Metric | Value |
|--------|-------|
| Total Functional Requirements | 27 (FR-001..FR-030, excluding absent FR-016 and FR-029) |
| Total Success Criteria | 9 (SC-001..SC-009) |
| Total Tasks | 15 (T001–T015) |
| FR Coverage % | 100% |
| SC Coverage % | 100% |
| Prior findings resolved | 9 of 10 (A2/F1-cosmetic carried as R2) |
| Regressions introduced by fixes | 0 |
| New findings this run | 2 (1 MEDIUM R1, 1 LOW R2) |
| **Critical Issues** | **0** |
| **HIGH Issues** | **0** |
| **MEDIUM Issues** | **1** (R1 — `skipped-human-only` hyphen vs. underscore in spec.md) |

---

## Next Actions

No CRITICAL or HIGH issues remain. All load-bearing paths are fully consistent across spec, plan, and tasks. The three-artifact set is internally consistent on: grants, migration canonical location, `pg_notify` payload contract, `AGENTS.md` update, dead-code test guidance, FR numbering, and `'classified'`-enum exclusion.

**One item worth fixing before `/garrison-implement m11`**:

1. **R1** — Fix spec.md FR-024 (line 174) and Key Entities (line 192) to use `skipped_human_only` (underscore, matching the DB CHECK, plan.md D17, and tasks.md). Two occurrences; one-minute fix. Prevents a reader from believing the spec's prose string literal (`skipped-human-only`) is what lands in the DB.

**Cosmetic (optional)**:

2. **R2** — Reorder FR-009 before FR-010 in spec.md (Thread 2 readability). Zero functional impact.

**Verdict**: Proceed to `/garrison-implement m11`. All prior HIGH and MEDIUM findings are resolved. The remaining R1 is a display-text vs. DB-literal mismatch that a careful implementer will resolve from tasks.md alone.

---

*Would you like concrete remediation edits for R1 (the two spec.md occurrences of `skipped-human-only`)?*
