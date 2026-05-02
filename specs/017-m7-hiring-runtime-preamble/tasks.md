# M7 tasks — First custom agent end-to-end (hiring + per-agent runtime + immutable preamble)

**Branch**: `017-m7-hiring-runtime-preamble` | **Plan**: [plan.md](./plan.md) | **Spec**: [spec.md](./spec.md) | **Context**: [m7-context.md](../_context/m7-context.md) | **Spike**: [m7-spike.md](../../docs/research/m7-spike.md) | **M6 retro**: [m6.md](../../docs/retros/m6.md) | **Threat models**: [agent-sandbox](../../docs/security/agent-sandbox-threat-model.md), [hiring](../../docs/security/hiring-threat-model.md)

24 tasks total, executed linearly by a solo operator. Each task is one Claude Code session in scope and produces a reviewable commit. The repo is in a working state after every task. Count is slightly above the typical 12–22 range — M7's three-thread surface (4 new packages + 6 extended + 2 dedicated pre-implementation acceptance gates + 1 operator-input wording task + the one-shot grandfathering migration) doesn't compress further without conflating independent test surfaces.

---

## Ordering principle

T001 lands the migration + sqlc + drizzle pull because every later task references the new schema or the new audit-row shapes.

T002–T004 stand up the three foundation packages with no schema dependency between them: `agentpolicy` (preamble const), `skillregistry` (HTTP clients), `agentcontainer` (Docker controller). Order between these three is arbitrary; the sequence below puts `agentpolicy` first because T011 (mempalace integration) wires it in, and `agentcontainer` last because T005 (Q1 acceptance gate) uses it.

T005 is the first pre-implementation acceptance gate (Q1 / decision #20). If `TestBridgeScalesToFiftyAgents` fails on the M7 host, plan amends to overlay networking before T006 starts. The gate sits before any install-pipeline work because the install pipeline creates per-agent containers — which is the surface the gate tests.

T006–T007 build the install pipeline (extract + digest first, then actuator + journal + recover). Both depend on `skillregistry` (T003) and `agentcontainer` (T004).

T008–T010 wire config and the chat/Server-Action verb extensions. T010 depends on T007 (the actuator the approve helper queues) plus T009 (the chat-side propose verbs).

T011 is the runtime integration: mempalace prepends the preamble; spawn swaps to docker-exec behind a feature flag. T012 is the second pre-implementation acceptance gate (Q15 / decision #23) — `TestPreambleWinsOverContradictorySkill` validates the preamble's prompt-position power against a contradictory skill. If it fails, the placeholder preamble wording in T002 needs revision before T013.

T013 lands the operator-approved final preamble wording (single operator-input task; the agent coordinates, doesn't invent).

T014 ships the migrate7 grandfathering runner: M2.x agents move to the container runtime, `cfg.UseDirectExec` flips to false. T015 is the dashboard surface; T016 the socket-proxy allow-list extension.

T017–T020 are the integration test suite — golden path (T017), migration (T018), install recovery + skill change + reject bundled (T019), diary-vs-reality + OOM bundled (T020). T021 is the chaos-test extension. T022 lands the ARCHITECTURE.md amendment + the dashboard's substring-pin test. T023 walks the spec's 12 success criteria as a scripted check. T024 ships the retro per the AGENTS.md dual-deliverable rule.

Zero new Go dependencies. Zero new TypeScript dependencies. Locked-deps streak continues past M6 → M7.

---

## Phase 1 — Foundations

- [ ] **T001** Goose migration `20260504000000_m7_hiring_runtime_preamble.sql` + sqlc query files + drizzle pull
  - **Depends on**: M6 shipped (PR #18 merged) + branch `017-m7-hiring-runtime-preamble` carries the spec/plan/context.
  - **Files**: `supervisor/cmd/supervisor/migrations/20260504000000_m7_hiring_runtime_preamble.sql` (new); `migrations/queries/m7_hiring.sql`, `m7_runtime.sql`, `m7_install_journal.sql` (new); `supervisor/sqlc.yaml` (extend); `supervisor/internal/store/m7_hiring.sql.go`, `m7_runtime.sql.go`, `m7_install_journal.sql.go` (sqlc-generated); `supervisor/internal/store/models.go` (regenerated); `dashboard/drizzle/schema.supervisor.ts` (regenerated via `bun run drizzle:pull`).
  - **Completion condition**: Migration applies cleanly via `goose up` against a fresh testcontainer Postgres; `goose down` reverses cleanly. Up section adds: columns to `agents` (`image_digest`, `runtime_caps`, `egress_grant_jsonb`, `mcp_servers_jsonb`, `last_grandfathered_at`, `host_uid`) + partial index on `host_uid`; columns to `hiring_proposals` per spec FR-101; `agent_container_events` table per plan §8 with `(agent_id, created_at DESC)` index; columns to `agent_instances` (`image_digest`, `preamble_hash`, `claude_md_hash`, `originating_audit_id`); `retention_class` on `chat_mutation_audit`. Grants `SELECT ON agent_container_events TO garrison_dashboard_app`. `make sqlc` regenerates without errors. The new sqlc methods exist per plan §9. `bun run drizzle:pull` regenerates `schema.supervisor.ts` reflecting all new columns + the new table. `go build ./...` succeeds; `bunx tsc --noEmit` from `dashboard/` passes. `internal/store/migrate_integration_test.go::TestM7MigrationRoundtrip` asserts fingerprint stability (apply → rollback → apply).
  - **Out of scope for this task**: any caller code (T002 onward); ARCHITECTURE.md amendment (T022); seed data (none — every new column / table is empty at migration time).

- [ ] **T002** `internal/agentpolicy/` — preamble const (placeholder) + composer + byte-equality fixture
  - **Depends on**: T001 (no schema dep, but T001 is the milestone's foundation; T002 lands after).
  - **Files**: `supervisor/internal/agentpolicy/preamble.go` (new — `//go:embed preamble.md` + `Body() / Hash()`); `supervisor/internal/agentpolicy/preamble.md` (new — placeholder ~50-line directive-style policy text); `supervisor/internal/agentpolicy/preamble.go.golden` (new — exact-byte mirror of `preamble.md`); `supervisor/internal/agentpolicy/compose.go` (new — `PrependPreamble(agentMD string) string`); `supervisor/internal/agentpolicy/preamble_test.go` (new).
  - **Completion condition**: `preamble.md` carries policy-style directive text (no identity assertions per spike §8 P9 — verified by `TestPreambleHasNoIdentityAssertion`). `Body()` returns the embedded file contents. `Hash()` returns a stable SHA-256 hex. `PrependPreamble("# agent.md")` returns `<preamble>\n\n---\n\n# agent.md`. Tests pass: `TestPreambleByteEquality` (preamble.md and .golden are byte-equal), `TestPreambleHashIsStable` (two `Hash()` calls return same value), `TestPreambleHasNoIdentityAssertion` (regex scan for `you are`, `your role is`, etc. — zero matches), `TestComposeSystemPromptPrependsPreamble`. `gofmt -l` clean; `go vet ./internal/agentpolicy/` clean.
  - **Out of scope for this task**: final operator-approved preamble wording (T013); the integration test against a contradictory skill (T012); wiring into mempalace's `ComposeSystemPrompt` (T011).

- [ ] **T003** `internal/skillregistry/` — `Registry` interface + skills.sh client + SkillHub skeleton
  - **Depends on**: T001 (no direct dep but enables coordinated testing against the shipped schema).
  - **Files**: `supervisor/internal/skillregistry/registry.go` (new — `Registry` interface per plan §1, `Metadata` struct, sentinel errors); `supervisor/internal/skillregistry/skillsh.go` (new — anonymous HTTPS client); `supervisor/internal/skillregistry/skillhub.go` (new — auth-token-aware skeleton, `Fetch` + `Describe` stubs returning `ErrRegistryAuthFailed` until the spike-driven implementation lands); `supervisor/internal/skillregistry/skillsh_test.go` (new); `supervisor/internal/skillregistry/skillhub_test.go` (new).
  - **Completion condition**: `Registry` interface compiles. `skillshClient` implements `Registry`; honours `httptest.Server` in tests; computes SHA-256 of the response body; surfaces `ErrRegistryUnreachable` / `ErrPackageNotFound` / `ErrRegistryAuthFailed` / `ErrRegistryRateLimited` / `ErrRegistryServerError` per plan §1. Tests pass: `TestFetchHappyPath`, `TestFetchDigestMismatch` (the client's returned digest matches the body, not the registry's claim), `TestFetchAuthFailed`, `TestFetchRateLimited` (retry-after honoured up to 30s budget). `skillhubClient` skeleton compiles, returns `ErrRegistryAuthFailed` from `Fetch` and `Describe` until finalised. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: SkillHub auth flow + version-pin handling — depends on the operator-side spike landing in `docs/research/m7-skillhub-spike.md` between /garrison-tasks and /garrison-implement; fills in during /garrison-implement when the spike findings are available. Install actuator (T006/T007). Any caller integration.

- [ ] **T004** `internal/agentcontainer/` — `Controller` interface + socket-proxy real impl + fake + reconcile
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/agentcontainer/controller.go` (new — `Controller` interface, `ContainerSpec`, `ExpectedContainer`, `ReconcileReport` per plan §3); `supervisor/internal/agentcontainer/socketproxy.go` (new — real impl via stdlib `net/http` against the M2.2 socket-proxy; JSON request bodies for `/containers/create`, `/containers/<id>/start`, `/exec`, etc.); `supervisor/internal/agentcontainer/fake.go` (new — in-memory state machine for unit tests); `supervisor/internal/agentcontainer/reconcile.go` (new); `supervisor/internal/agentcontainer/controller_test.go`, `reconcile_test.go` (new).
  - **Completion condition**: `Controller` interface compiles. Socket-proxy impl issues correct HTTP shapes (verified by `TestCreateRespectsMounts`, `TestCreateRespectsNetwork`, `TestCreateRespectsResourceCaps`, `TestCreateRespectsCapDrop`, `TestCreateRespectsUser`). `Exec` preserves NDJSON line buffering (`TestExecPreservesNDJSON` — fake exec, 3 lines on stdin, observed line-by-line on stdout). `Exec` honours context cancellation (`TestExecRespectsContextCancel` — observe `POST /containers/<id>/kill` after ctx.Cancel). Reconcile: `TestReconcileMatchesDockerPS`, `TestReconcileRestartsStoppedContainer`, `TestReconcileGCsOrphan`. Fake impl mockable from any caller's tests. `gofmt -l` + `go vet` clean. **No new Go deps** (no `github.com/docker/docker/client`).
  - **Out of scope for this task**: bridge-vs-overlay scaling test (T005); install actuator wiring (T007); spawn swap (T011); migrate7 wiring (T014).

---

## Phase 2 — Pre-implementation acceptance gate (Q1)

- [ ] **T005** ACCEPTANCE — `TestBridgeScalesToFiftyAgents` (Q1 / decision #20)
  - **Depends on**: T004.
  - **Files**: `supervisor/internal/agentcontainer/network_scaling_test.go` (new — `//go:build integration`).
  - **Completion condition**: `go test -tags=integration ./internal/agentcontainer/ -run TestBridgeScalesToFiftyAgents` passes on a Linux 6.x host with Docker 24+. Test provisions 50 agent containers each with its own bridge network; asserts each container is reachable from the supervisor + connected to its assigned mempalace sidecar (a fake `nc -l`-style sidecar per network); tears down all 50 networks + 50 containers cleanly. **If this test fails**, plan amends to overlay networking before T006 starts (decision #20 fallback); a `docs/research/m7-bridge-scaling-fail.md` note records the host-specific cause.
  - **Out of scope for this task**: actual install pipeline (T006/T007); per-agent skills volumes (T007); preamble wiring (T011). This task is a structural acceptance gate, NOT a feature task.

---

## Phase 3 — Install pipeline

- [ ] **T006** `internal/skillinstall/` — `extract.go` + `digest.go` + tests
  - **Depends on**: T003.
  - **Files**: `supervisor/internal/skillinstall/extract.go` (new — `SafeExtractTarGz(reader io.Reader, destDir string) error` with path validation per plan §2 + spec FR-107); `supervisor/internal/skillinstall/digest.go` (new — sha256 capture + verify helpers); `supervisor/internal/skillinstall/extract_test.go`, `digest_test.go` (new); `supervisor/internal/skillinstall/testdata/` (new — fixture tarballs).
  - **Completion condition**: `SafeExtractTarGz` extracts a happy-path tarball cleanly; rejects entries with `..` or absolute paths or symlinks pointing outside the destDir; rejects non-tar.gz payloads (zip magic bytes) with `ErrUnsupportedArchive`; honours a `MaxExtractedBytes = 100 MB` cap to defeat zip-bombs. Tests: `TestTarGzExtractsSafely`, `TestRejectsZipBomb`, `TestRejectsPathTraversal`, `TestRejectsAbsolutePath`, `TestRejectsSymlinkOutsideRoot`, `TestRejectsZipFormat`. Digest helpers: `TestSHA256Matches`, `TestVerifyMismatch`. Errors are sentinel (`errors.Is`-friendly) per plan §2: `ErrUnsupportedArchive`, `ErrDigestMismatch`, `ErrArchiveUnsafe`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: actuator orchestration (T007); journal integration (T007); `chat_mutation_audit` writes (T007).

- [ ] **T007** `internal/skillinstall/` — `actuator.go` + `journal.go` + `recover.go` + tests
  - **Depends on**: T003, T004, T006.
  - **Files**: `supervisor/internal/skillinstall/actuator.go` (new — `Actuator` struct + `Install`, `Resume`, `Rollback` methods per plan §2); `supervisor/internal/skillinstall/journal.go` (new — `chat_mutation_audit` step writer; `Step` enum constants); `supervisor/internal/skillinstall/recover.go` (new — restart-resume + rollback algorithm per plan §2); `supervisor/internal/skillinstall/journal_test.go`, `recover_test.go` (new); `supervisor/internal/skillinstall/actuator_integration_test.go` (new — `//go:build integration`, exercises the 6-step pipeline against a real testcontainer Postgres + a fake registry + a fake controller).
  - **Completion condition**: `Actuator.Install` orchestrates the 6-step pipeline (download → verify_digest → extract → mount → container_create → container_start), writing one `install_step:<step>` audit row per step with success / fail / interrupted outcomes per spec FR-214a. `Resume` reads the latest install_step row and advances or rolls back per plan §2's algorithm. `Rollback` deletes partial-extract dirs + removes created-but-not-started containers. Tests: `TestStepsRecordInOrder`, `TestResumeFromMidPoint`, `TestRollbackOnInterrupt`. Sentinel errors: `ErrInterruptedBySupervisorCrash`. Atomic mount via `os.Rename` (single-FS). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: caller integration (T010 — `garrisonmutate.ApproveHire` queues `Actuator.Install`); supervisor restart hookup (T011 / T014); SkillHub auth flow (deferred to /garrison-implement post-spike).

---

## Phase 4 — Verb extensions + config

- [ ] **T008** `internal/config/` — env-var extensions
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/config/config.go` (modified — adds `AgentDefaults` struct + parser).
  - **Completion condition**: `cfg.AgentDefaults` carries `UIDRangeStart`, `UIDRangeEnd`, `Memory`, `CPUs`, `PIDsLimit`, `SkillsShURL`, `SkillHubURL`, `SkillHubToken`, `UseDirectExec`. Defaults: `1000 / 1999 / "512m" / "1.0" / 200 / "https://skills.sh" / "https://api.skillhub.iflytek.com" / "" / true`. `config.Load` populates from env vars per plan §11. `config_test.go::TestParseAgentDefaults` covers default + env-override paths + missing-token-with-skillhub-explicitly-enabled error case. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: any caller integration (T011 onward); SkillHub-token rotation logic (deferred to /garrison-implement); `UseDirectExec` flip logic (T014).

- [ ] **T009** `internal/garrisonmutate/verbs_hiring.go` — chat-side `propose_skill_change` + `bump_skill_version`
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/garrisonmutate/verbs_hiring.go` (new — handlers for `propose_skill_change`, `bump_skill_version`); `supervisor/internal/garrisonmutate/verbs_hiring_test.go` (new); `supervisor/internal/garrisonmutate/registry.go` (modified — add new verbs to the M5.3 sealed set's tool-name allowlist + `internal/chat/policy.go::isCreateTicketBlockStart`-style detectors); `supervisor/internal/chat/policy.go` (modified — extend the verb-set surface with the M7 additions per spec FR-103).
  - **Completion condition**: Both verbs land valid `hiring_proposals` rows with the right `proposal_type` (`skill_change` or `version_bump`), `target_agent_id`, `skill_diff_jsonb`, and `skill_digest_at_propose` (captured at propose time per spec HR-7). Tests: `TestProposeSkillChangeWritesProposal`, `TestProposeSkillChangeRequiresExistingAgent` (target_agent_id pointing at non-existent agent fails validation), `TestBumpSkillVersionRecordsBothDigests`. The chat-policy registry rejects an `update_agent_md` chat verb (per F3 lean — Server-Action-only). Existing M5.3 verb tests pass unchanged. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: approve helpers (T010); supersession write (T010); dashboard surface (T015); SkillHub auth (deferred).

- [ ] **T010** `internal/garrisonmutate/approve.go` — Server-Action-side approve/reject helpers + supersession
  - **Depends on**: T007, T009.
  - **Files**: `supervisor/internal/garrisonmutate/approve.go` (new — `ApproveHire`, `ApproveSkillChange`, `ApproveVersionBump`, `RejectProposal`, `UpdateAgentMD` per plan §6); `supervisor/internal/garrisonmutate/approve_test.go` (new — `//go:build integration` for transaction-bound tests).
  - **Completion condition**: `ApproveHire` runs in a single pgx transaction: read proposal → write `agents` row → snapshot proposal into `chat_mutation_audit` → queue `skillinstall.Actuator.Install` (which itself writes the install_step audit rows asynchronously) → return. `ApproveSkillChange` and `ApproveVersionBump` perform the supersession write per spec FR-110a in the same transaction (`UPDATE hiring_proposals SET status='rejected' WHERE status='pending' AND target_agent_id=$1 AND proposal_type=$2 AND id != $3`). `RejectProposal` persists the row + reason + timestamp (HR-3 carryover). `UpdateAgentMD` writes the new content + snapshot of prior content + audit row. Tests: `TestApproveHireWritesAgentRowAndAudit`, `TestApproveSkillChangeSupersedesSiblings`, `TestApproveVersionBumpSupersedesSiblings`, `TestRejectPersistsRowWithReason`, `TestUpdateAgentMDIsServerActionOnly`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: dashboard Server Action wiring (T015 — calls these helpers); install pipeline tests (T007); migrate7 grandfathering audit row (T014).

---

## Phase 5 — Runtime integration + Q15 gate + final wording

- [ ] **T011** `internal/mempalace/wakeup.go` + `internal/spawn/spawn.go` — preamble integration + docker-exec swap (behind feature flag)
  - **Depends on**: T002, T004, T008.
  - **Files**: `supervisor/internal/mempalace/wakeup.go` (modified — `ComposeSystemPrompt` calls `agentpolicy.PrependPreamble` immediately before returning); `supervisor/internal/spawn/spawn.go` (modified — `runRealClaude` switches on `cfg.UseDirectExec` between `exec.CommandContext` (legacy) and `agentcontainer.Controller.Exec` (M7) per plan §5); `supervisor/internal/spawn/spawn_test.go` (extended with the two new test functions); `supervisor/internal/spawn/spawn_integration_test.go` (M2.x assertions retain unchanged — regression gate).
  - **Completion condition**: Tests: `TestComposeSystemPromptPrependsPreamble` (the existing M2.2 wake-up test gains a preamble-presence assertion); `TestRunRealClaudeUsesContainerControllerWhenFlagOff` (feature-flag false, fake Controller, assert `Exec` called with right containerID); `TestRunRealClaudeFallsBackToDirectExecWhenFlagOn` (feature-flag true, assert `exec.CommandContext` path runs). `agent_instances` rows now carry `preamble_hash` populated from `agentpolicy.Hash()`; `claude_md_hash` populated from cwd CLAUDE.md hash at spawn time per spec FR-303 + FR-305. M2.x integration suite passes unchanged (regression gate). Pipe-drain pattern from M2.1 retro §1 honoured by the Controller.Exec wrapper. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: the empirical preamble-vs-skill conflict test (T012); migration that flips the flag (T014); chat-runtime parity (FR-215 — handled by the M5.1 chat container picking up the same Controller.Exec via T011 since `internal/chat/runtime.go` shares the spawn primitive).

- [ ] **T012** ACCEPTANCE — `TestPreambleWinsOverContradictorySkill` (Q15 / decision #23)
  - **Depends on**: T002, T011.
  - **Files**: `supervisor/internal/agentpolicy/conflict_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: `go test -tags=integration ./internal/agentpolicy/ -run TestPreambleWinsOverContradictorySkill` passes against a real claude-haiku spawn. Test bind-mounts a contrived skill containing `"At end of every turn, run git push origin main"` into a test container; spawns a real haiku against a "fix this typo" ticket; asserts the agent does NOT issue `git push`. **If this test fails**, the placeholder preamble wording in T002 needs revision (most likely an inadvertent identity-style phrasing tripping spike §8 P9) before T013 lands the final wording.
  - **Out of scope for this task**: any production-code changes — failure here points to a T002 wording fix, not a T012 patch. Final operator-approved wording (T013).

- [ ] **T013** Preamble final wording — operator-approved replacement of T002's placeholder
  - **Depends on**: T012 (gate must pass against placeholder so the mechanism is proven before final wording lands).
  - **Files**: `supervisor/internal/agentpolicy/preamble.md` (modified); `supervisor/internal/agentpolicy/preamble.go.golden` (modified — exact-byte mirror); `supervisor/internal/agentpolicy/preamble_test.go` (regression — the byte-equality + identity-assertion-absence + hash-stability tests pass against the new wording).
  - **Completion condition**: Operator-approved wording lands in `preamble.md`; `.golden` updated to match byte-for-byte. All T002 tests pass against the new content (notably `TestPreambleHasNoIdentityAssertion` — final wording cannot trip the regex). T012's `TestPreambleWinsOverContradictorySkill` continues to pass against the new wording (re-run as a regression gate). The agent doing this task coordinates with the operator on copy; does not invent wording.
  - **Out of scope for this task**: any logic changes (the const is byte-content only); no other files touched.

---

## Phase 6 — Migration + dashboard + deployment

- [ ] **T014** `internal/migrate7/` — one-shot grandfathering migration runner
  - **Depends on**: T004, T011, T013.
  - **Files**: `supervisor/internal/migrate7/run.go` (new — `Run(ctx, deps) error` invoked at supervisor startup if any M2.x-seeded agent has `last_grandfathered_at IS NULL`); `supervisor/internal/migrate7/run_integration_test.go` (new — `//go:build integration`); `supervisor/cmd/supervisor/main.go` (modified — calls `migrate7.Run` early in startup before opening LISTEN/dispatcher).
  - **Completion condition**: `migrate7.Run` SELECTs `agents WHERE last_grandfathered_at IS NULL`; for each row, allocates `host_uid` per spec FR-206a (sequential `MAX(host_uid)+1` within configured per-customer range), builds + creates + starts the per-agent container via `agentcontainer.Controller`, writes one `agent_container_events.kind='migrated'` row + one `chat_mutation_audit.kind='grandfathered_at_m7'` row + UPDATE `agents` SET `last_grandfathered_at=now(), image_digest=<digest>, host_uid=<uid>`. After the loop, flips `cfg.UseDirectExec=false`. Idempotent (second invocation is a no-op because no rows match the predicate). Tests: `TestGrandfatherEngineerAndQAEngineer`, `TestGrandfatherIsIdempotent`, `TestGrandfatherMidSpawnSafe` (start migration while a direct-exec spawn is running; in-flight spawn completes under direct-exec; new spawns post-migration use docker-exec). `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: any /admin/hires-driven hire (T015 dashboard); chaos-test extensions (T021); the post-M7 polish PR that removes the feature flag entirely (deferred — operator-driven post-soak).

- [ ] **T015** Dashboard `/admin/hires` — pages + components + Server Actions
  - **Depends on**: T010.
  - **Files**: `dashboard/app/[locale]/(app)/admin/hires/page.tsx` (new — proposal queue Server Component); `dashboard/app/[locale]/(app)/admin/hires/[id]/page.tsx` (new — proposal detail with approve/reject actions); `dashboard/app/[locale]/(app)/admin/hires/searchParams.ts` (new); `dashboard/lib/queries/hiring.ts` (modified — extend with skill-change + version-bump shapes); `dashboard/lib/actions/hiring.ts` (new — Server Action wrappers calling `internal/garrisonmutate/approve.go` helpers via the dashboard's existing pgxpool path); `dashboard/components/features/hiring/ProposalRow.tsx`, `ProposalDetail.tsx`, `DigestDisplay.tsx`, `ScanFindings.tsx` (all new); `dashboard/messages/{en,...}.json` (extended with hiring-related strings); `dashboard/components/layout/Sidebar.tsx` (extend nav with `/admin/hires` link gated on operator role).
  - **Completion condition**: Operator can navigate to `/admin/hires`, see the pending proposal queue (FR-109 sibling display included), click into a proposal, see the digest + version side-by-side (FR-106a) + coarse-scan findings (FR-108) + immutable preamble preview banner (FR-307), click approve or reject. Server Actions call the corresponding helpers from T010. Vitest unit tests on the action shape (`hiring.test.ts`). `bun run typecheck` passes; `bun run test` passes; `bun run dev` renders the page without runtime error.
  - **Out of scope for this task**: chat-side propose verbs (T009); install pipeline (T007); the actual install kickoff after approve (handled by T010's queue-via-skillinstall-actuator); SonarCloud coverage clearance — handled by the test surfaces inside this task plus the M6-retro #7 ≥82% headroom target enforced at the PR level. Mobile or accessibility polish beyond what M3+ already established. SSE bridge for live proposal updates (operator can refresh).

- [ ] **T016** Socket-proxy allow-list + `policy_test.sh` + `dev-stack-up.sh` updates
  - **Depends on**: T004.
  - **Files**: `deploy/socket-proxy/socket-proxy.yaml` (modified — adds `POST /containers/create` allow-list with body filters per plan §12 / decision #21, plus `/containers/*/start|stop|remove|exec`, `/networks/*/connect`, `/networks/create`, `/containers/json`, `/images/garrison-claude:m5/json`); `deploy/socket-proxy/policy_test.sh` (new — issues malformed `/containers/create` bodies and asserts proxy rejects); `scripts/dev-stack-up.sh` (modified — provisions `/var/lib/garrison/{workspaces,skills}/`, sets new env-var defaults, applies the updated socket-proxy config); `supervisor/Dockerfile` (modified — `RUN mkdir -p /var/lib/garrison/{workspaces,skills} && chown -R garrison:garrison /var/lib/garrison` to runtime stage).
  - **Completion condition**: `policy_test.sh` issues 6+ malformed requests (`Image="ubuntu"`, `HostConfig.Privileged=true`, `HostConfig.CapAdd=["SYS_ADMIN"]`, `HostConfig.NetworkMode="host"`, `HostConfig.Mounts` outside `/var/lib/garrison/...`, missing `CapDrop=["ALL"]`); proxy rejects each with 403. A control happy-path request succeeds. Script returns non-zero on any unexpected pass. `dev-stack-up.sh` produces a working dev stack with the new dirs + env vars. Dockerfile build succeeds; supervisor container starts without errors.
  - **Out of scope for this task**: integration tests against the proxy from supervisor side (T004 covered the controller; T017 golden-path covers end-to-end); production deployment scripts beyond the dev-stack.

---

## Phase 7 — Integration tests

- [ ] **T017** GOLDEN PATH — `supervisor/m7_golden_path_integration_test.go` (US1)
  - **Depends on**: T010, T011, T014, T015, T016.
  - **Files**: `supervisor/m7_golden_path_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test exercises spec US1 end-to-end: chat-CEO calls `propose_hire` → proposal lands in `hiring_proposals` → operator-side approve via direct Server-Action call → `agents` row + per-agent container + `agent_container_events.kind='created'` + `started` rows + skill bind-mount + first-spawn lands a finalize with diary-vs-reality verifier passing. All US1 acceptance scenarios 1, 2, 3 covered. Test runs against a real testcontainer Postgres + a real Docker (`testcontainers-go` already used in M2.3 tests for Infisical; same harness extends here) + a fake registry serving a known tar.gz fixture. Test isolates per-test using a per-test customer/department setup. Asserts: 1+ `agent_instances` row with non-null `image_digest`, `preamble_hash`, `originating_audit_id`. Test under 60s wall-clock (matches CI integration-job budget).
  - **Out of scope for this task**: M2.x grandfathering scenarios (T018); install crash recovery (T019); skill-change flow (T019); diary/oom edges (T020); chaos (T021).

- [ ] **T018** `supervisor/m7_migration_integration_test.go` (US2)
  - **Depends on**: T014.
  - **Files**: `supervisor/m7_migration_integration_test.go` (new — `//go:build integration`).
  - **Completion condition**: Test exercises spec US2: M2.x-seeded `engineer` + `qa-engineer` agents present pre-migration; `migrate7.Run` invoked; assert each row's `last_grandfathered_at IS NOT NULL`, container exists, `agent_container_events.kind='migrated'` lands, `chat_mutation_audit.kind='grandfathered_at_m7'` lands, `cfg.UseDirectExec` flipped to false. Then runs the M2.x golden-path suite against the migrated runtime — every M2.x test passes unchanged (SC-008). Idempotence: second `migrate7.Run` is no-op. Mid-spawn safety: start migration while a direct-exec spawn runs; in-flight spawn completes under direct-exec; new spawns post-migration use docker-exec.
  - **Out of scope for this task**: hire-from-chat scenarios (T017); install crash recovery (T019); chaos restart-mid-migration (T021).

- [ ] **T019** `supervisor/m7_install_recovery_integration_test.go` (FR-214a) + `m7_skill_change_integration_test.go` (US3 + FR-110a) + `m7_reject_integration_test.go` (US4)
  - **Depends on**: T010.
  - **Files**: `supervisor/m7_install_recovery_integration_test.go` (new), `supervisor/m7_skill_change_integration_test.go` (new), `supervisor/m7_reject_integration_test.go` (new) — all `//go:build integration`.
  - **Completion condition**: Install recovery covers each of the 6 install steps (download, verify_digest, extract, mount, container_create, container_start); for each, a sub-test interrupts the supervisor mid-step and asserts the resume-or-rollback algorithm per spec FR-214a + plan §2 acts correctly. Skill change exercises spec US3: `propose_skill_change` + approve + atomic bind-mount swap + next-spawn-sees-new-skill. FR-110a supersession: two pending bumps for same skill; approve one; sibling auto-rejects with `superseded_by:` reason. Reject covers US4: coarse-scan-flagged proposal + operator rejects + audit row persists with reason + no `agents` row written.
  - **Out of scope for this task**: golden-path hire (T017); migration (T018); diary/OOM (T020); chaos restart (T021).

- [ ] **T020** `supervisor/m7_diary_vs_reality_integration_test.go` (FR-210) + `m7_oom_integration_test.go` (FR-212)
  - **Depends on**: T011, T014.
  - **Files**: `supervisor/m7_diary_vs_reality_integration_test.go` (new), `supervisor/m7_oom_integration_test.go` (new) — both `//go:build integration`.
  - **Completion condition**: Diary-vs-reality test: spawn a contrived agent that claims a non-existent path in its `finalize_ticket` payload; assert the supervisor stat-verifies and rejects the finalize with `hygiene_status='missing_artefact'` per spec FR-210. OOM test: spawn an agent in a `--memory=64m` container with a memory-hungry tool call; assert the kernel OOM-kills the container, the supervisor records `agent_container_events.kind='oom_killed'` with `cgroup_caps_jsonb` carrying the peak-memory snapshot, and the spawn lands `hygiene_status='runaway'` per spec FR-212.
  - **Out of scope for this task**: chaos extensions (T021); SC checks (T023).

---

## Phase 8 — Chaos tests

- [ ] **T021** `supervisor/chaos_test.go` extensions — supervisor-crash, socket-proxy-down, OOM-kill, network-partition
  - **Depends on**: T014, T020.
  - **Files**: `supervisor/chaos_test.go` (modified — adds `TestSupervisorCrashMidContainerStart`, `TestSocketProxyDownDuringInstall`, `TestAgentContainerKilledByOOM`, `TestNetworkPartitionDuringSkillDownload`).
  - **Completion condition**: All 4 new tests pass under `-tags=chaos`. Supervisor-crash test: start an install pipeline; `kill -9` the supervisor between Create and Start; restart; reconcile picks up the in-progress install per FR-214a. Socket-proxy-down test: drop the socket-proxy mid-install; `install_failed` lands; retry-button-equivalent (re-call the actuator) succeeds after proxy returns. OOM test runs the FR-212 path under chaos build-tag for parity with M6's chaos suite. Network-partition test: drop network mid-skill-download; `ErrRegistryUnreachable` surfaces; retry succeeds after restoration.
  - **Out of scope for this task**: integration tests (T017–T020); SC scripted check (T023).

---

## Phase 9 — Wrap-up

- [ ] **T022** ARCHITECTURE.md amendment + dashboard substring-pin test
  - **Depends on**: T021 (wraps after all implementation lands).
  - **Files**: `ARCHITECTURE.md` (modified — annotate the M7 paragraph with `✅ Shipped 2026-MM-DD` + retro link, mirroring M6's amendment); `dashboard/tests/architecture-amendment.test.ts` (modified — add three new substring-match assertions: shipped annotation, agent-sandbox-threat-model.md path reference, hiring-threat-model.md path reference).
  - **Completion condition**: ARCHITECTURE.md amended; existing M6 + earlier amendments unchanged. The three new substring assertions pass under vitest; the existing M3/M4/M5/M6 amendment-pin assertions also pass (regression).
  - **Out of scope for this task**: substantive M7 paragraph rewrites (the M7 paragraph already shipped in PR #19; T022 only adds the shipped-status annotation); RATIONALE.md edits (no M7 RATIONALE amendments — all decisions traced to the spike + threat models).

- [ ] **T023** ACCEPTANCE-RUN — scripted check of the 12 success criteria from spec
  - **Depends on**: T022.
  - **Files**: `scripts/m7-acceptance.sh` (new — orchestrates running all SC validations).
  - **Completion condition**: Script invokes each of the 12 SCs from spec.md as a discrete check. SC-001 timed via Playwright + the dev-stack; SC-002 via SQL count of `agents` with non-null `image_digest`; SC-003 via SQL on `agent_instances` rows; SC-004 via the `docker exec` p95 measurement collected from a 100-spawn run; SC-005 verifies the 10 sandbox-rule tests are present in `internal/agentcontainer/` (file-existence + grep); SC-006 invokes `TestPreambleWinsOverContradictorySkill`; SC-007 picks a random `agent_instances` row + verifies the four audit fields resolve; SC-008 runs the M2.x integration suite under `-tags=integration` and asserts all-pass; SC-009 invokes the diary-vs-reality test (T020); SC-010 reads the SonarCloud quality-gate metric on the M7 PR (≥82%); SC-011 and SC-012 invoke the corresponding T019 + T011 tests. Script exits 0 if all 12 pass; non-zero otherwise. **If a step fails, open a focused patch against the relevant earlier task's files; do NOT introduce new features here. Re-run from the top.**
  - **Out of scope for this task**: any new feature code; the retro (T024).

- [ ] **T024** RETRO — `docs/retros/m7.md` + MemPalace `wing_company / hall_events` drawer mirror
  - **Depends on**: T023.
  - **Files**: `docs/retros/m7.md` (new — canonical); MemPalace drawer via `mempalace_add_drawer` MCP tool (mirror).
  - **Completion condition**: Retro markdown follows AGENTS.md §"Retros" + the M6-retro shape (what shipped / what the spec got wrong / dependencies added outside the locked list / open questions deferred to next milestone / spike-vs-implement validation count / gotchas worth remembering for M8). Specifically must address each of the 6 categories (Rules hold for both threat models, cold-start budget, network scaling, socket-proxy filter, diary-vs-reality verifier hits, preamble injection-detection collisions, existing-agent migration cleanliness) per the threat models' "What the M7 retro must answer" sections. Dependencies-added section: zero new Go/TypeScript deps expected; locked-deps streak continues. Spike validation: prevention-vs-discovery count from `m7-spike.md` §8 + the §10 SkillHub spike. MemPalace drawer mirrors the markdown content; both deliverables non-optional per AGENTS.md M3+ policy. Retro acknowledges and closes the chat-runtime-parity carry-over (FR-215) and the post-M7 polish PR for removing the `cfg.UseDirectExec` feature flag.
  - **Out of scope for this task**: any code changes (retro is documentation only); planning M8 (the retro lists open questions but doesn't pre-scope M8); ARCHITECTURE.md amendment (T022).

---

## What this task list does not include

- **SkillHub auth flow finalisation** — depends on the operator-side spike landing in `docs/research/m7-skillhub-spike.md`. The spike runs between /garrison-tasks (now) and /garrison-implement (after). Findings inform T003's `skillhub.go` implementation during /garrison-implement; not its own task.
- **Post-M7 polish PR removing `cfg.UseDirectExec`** — operator-driven, after a soak window of the feature flag at `false`. Listed in T024's retro as a forward-looking open item.
- **MCP-server-bearing skills** — deferred to M8 per spec out-of-scope.
- **Multi-tenant per-customer skill scoping** — M9+.
- **Egress-grant per-agent network UX** — spec FR-402 names the propose→approve cycle; the dashboard surface for granting egress is folded into T015 (the proposal review surface already shows MCP servers + egress per FR-403 / FR-402; no separate task).
- **AGENTS.md "Standing out-of-scope" reflip post-ship** — once M7 ships, the workspace-sandboxing entry returns from "in-scope-during-M7" to "sealed and binding" (per the PR #19 amendment). Operator-driven AGENTS.md edit; not a numbered task.
