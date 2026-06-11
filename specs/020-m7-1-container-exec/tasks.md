# M7.1 tasks — real container execution pipeline

**Branch**: `020-m7-1-container-exec` | **Plan**: [plan.md](./plan.md) | **Spec**: [spec.md](./spec.md) | **Context**: [m7-1-context.md](../_context/m7-1-context.md) | **Spike**: [m7-1-spike.md](../../docs/research/m7-1-spike.md) | **M7 retro**: [m7.md](../../docs/retros/m7.md) | **M8 retro**: [m8.md](../../docs/retros/m8.md)

18 tasks total (T001–T018), count operator-approved 2026-06-10. Executed
linearly by a solo operator. Each task is one Claude Code session in scope and
produces a reviewable commit. The repo is in a working state after every task —
working, not merely compiling: the supervisor binary boots and runs tickets
after each commit.

Risk callouts (woven into specific tasks below):

- **The flag flip is deliberately late (T012), not in the config task (T006).**
  Flipping `UseDirectExec` default to false before T011 makes the pipeline real
  would route live spawns into the M7 outline. Until T012, the default stays
  true and the force-on guard stays in place.
- **Compose lands before the migrate7/skillinstall shape switch (T005 before
  T006)** — otherwise boot-time creates reference a `garrison-agents` network
  that doesn't exist.
- **T009 (runner extraction) is the highest-risk task.** Its gate is zero
  behavioral diff for direct-exec: every existing spawn/pipeline/adjudicate
  test passes unchanged before any later task starts.
- **vaultlog discipline**: the sanctioned production `UnsafeBytes()` call-site
  count stays at two. T009 extracts the env-injection site into a shared
  helper; T011 reuses it — no third call site.
- **sqlc `sqlc.arg(name)` exclusively** in the new query file (M7/M8 gotcha).
- **gofmt + `go vet` from `supervisor/` before every push** (standing operator
  rule; CI's lint job has broken repeatedly on new Go files).
- **Sonar pre-clearance** happens inside T017's acceptance walk (M8 pattern),
  not as a separate loop after merge.

Zero new Go dependencies. Zero new TypeScript dependencies. The squid sidecar
is a compose-level runtime addition (spec §Assumptions); the locked list is
untouched.

---

## Ordering principle

T001 lands the only schema-adjacent work: the data-only agent_md wording
migration (FR-015), the one new sqlc query the boot reconcile needs, and the
synthesis-template wording fix — all mode-neutral, safe under the still-default
direct-exec.

T002–T004 build the `agentcontainer` foundations bottom-up: demux (T002), the
exec transport + `Restart` (T003), the new create shape + single spec source
(T004). After T004 migrate7 already creates idle-entrypoint containers (the
`Exited(1)` fix) while still passing its old keying until T006.

T005 lands the deployment substrate (network, proxy, binds) so T006's switch to
`SpecForAgent` (agents network, agent-ID workspace keying) has something to
attach to. T007 closes the substrate with the boot shape reconcile.

T008–T011 build the spawn path: `mcpconfig.Render` (T008), the argv/runner
extractions (T009), the per-agent slot (T010), then the real container pipeline
(T011) — which is fully testable behind `UseDirectExec=false` set explicitly.
T012 flips the default and removes the guard: the milestone's behavior change
lands in one small, revertable commit.

T013–T015 are the integration and chaos suites. T016 trues the docs. T017
walks the acceptance criteria as a script. T018 is the retro.

---

## Phase 1 — Schema-adjacent foundations

- [ ] **T001** Data migration for agent_md container wording + reconcile sqlc query + synthesis wording
  - **Depends on**: M7/M8 shipped; branch `020-m7-1-container-exec` carries spec/plan/context.
  - **Files**: `migrations/20260610000000_m7_1_agent_md_container_wording.sql` (new); `migrations/queries/m7_1_agents.sql` (new — `ListAgentsForContainerReconcile`); `supervisor/sqlc.yaml` (extend if query files are enumerated); `supervisor/internal/store/m7_1_agents.sql.go` (generated); `supervisor/internal/garrisonmutate/approve.go` (synthesis template wording); `supervisor/internal/garrisonmutate/approve_test.go` (extend).
  - **Completion condition**: Migration applies via `goose up` and reverses via `goose down` against a fresh testcontainer Postgres. Up section is **data-only**: targeted `UPDATE agents SET agent_md = replace(agent_md, <old>, <new>)` statements that (a) replace the seeded "Mid-turn MemPalace usage (optional)" section (old-strings lifted verbatim from `migrations/20260424000006_m2_2_2_compliance_calibration.sql`) with a short "Mid-turn context" paragraph instructing agents to use the wake-up context provided, and (b) drop `mempalace MCP (…)` from the "Tools available" list. `replace()` no-ops on operator-edited rows (idempotent, non-clobbering); down restores via inverse replaces. No schema change → no drizzle pull. The new query uses `sqlc.arg(name)` and returns `id, role_slug, host_uid, image_digest FROM agents WHERE host_uid IS NOT NULL` — covering grandfathered AND hired agents, since hired agents never receive `last_grandfathered_at` (analyze C1); `make sqlc` regenerates cleanly; `go build ./...` passes. `approve.go`'s synthesized template no longer says "consult MemPalace for context" (wording shifts to the wake-up-context phrasing); `TestSynthesizedAgentMDOmitsMempalaceToolGuidance` in `approve_test.go` pins it. `gofmt -l` + `go vet ./...` clean from `supervisor/`.
  - **Out of scope for this task**: any container/exec code (T002+); mcpconfig server-set change (T008); seeding new agents; threat-model/runbook docs (T016).

## Phase 2 — Exec transport and create shape (`internal/agentcontainer`)

- [ ] **T002** 8-byte raw-stream demultiplexer
  - **Depends on**: T001.
  - **Files**: `supervisor/internal/agentcontainer/demux.go` (new); `supervisor/internal/agentcontainer/demux_test.go` (new).
  - **Completion condition**: An internal demux consumes a `[stream(1) pad(3) size(4)]`-framed `io.ReadCloser` (spike F2) and exposes two `io.ReadCloser`s via `io.Pipe` (stream 1 → stdout, 2 → stderr, 0 → stdout). EOF closes both pipe writers; a malformed header closes both with an error (`CloseWithError`); closing the stdout reader closes the raw stream (no goroutine outlives it — concurrency rule 1). Tests pass: `TestDemuxSplitsStdoutAndStderrFrames`, `TestDemuxHandlesFrameSplitAcrossReads`, `TestDemuxPropagatesEOFAndClose`, `TestDemuxRejectsMalformedHeader`. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: wiring into `Exec` (T003); any HTTP code.

- [ ] **T003** Replace `Controller.Exec` with `ExecSpec`/`ExecSession`; add `Restart`
  - **Depends on**: T002.
  - **Files**: `supervisor/internal/agentcontainer/controller.go` (Exec signature replaced; `ExecSpec`, `ExecSession` added; `Restart(ctx, containerID)` added to the interface); `supervisor/internal/agentcontainer/socketproxy.go` (exec-create body gains `Env`/`WorkingDir`/`AttachStdin:false`; exec-start response fed through T002's demux; `ExitCode` polls `GET /exec/<id>/json` ≤10× at 200 ms until `Running=false`, returns (−1, error) past the budget; `Restart` = `POST /containers/<id>/restart?t=5`; exec-create 404/409 → `ErrContainerNotFound`); `supervisor/internal/agentcontainer/fake.go` (new method set; fake exec sessions with scriptable exit codes); `supervisor/internal/agentcontainer/controller_test.go` (extend); `supervisor/internal/spawn/m7.go` (signature adaptation ONLY — the outline keeps its current drain-and-log behavior under the new API).
  - **Completion condition**: Tests pass: `TestExecCreateBodyCarriesEnvWorkingDirNoStdin`, `TestExecStartDemuxesRawStream`, `TestExecExitCodePollsUntilNotRunning`, `TestExecExitCodeGivesUpAfterPollBudget`, `TestExecCreateOn404ReturnsErrContainerNotFound`, `TestRestartPostsRestartEndpoint` — all against the existing httptest captureProxy pattern. `go build ./...` and the full existing suite pass (the m7.go outline compiles and behaves as before; `spawn/m7_test.go` adjusted only where the signature forces it). No connection hijacking anywhere (FR-004).
  - **Out of scope for this task**: create-body shape changes (T004); the real container pipeline (T011); `ReconcileShape` (T007).

- [ ] **T004** New create shape, shape-hash label, and the single per-agent spec source
  - **Depends on**: T003.
  - **Files**: `supervisor/internal/agentcontainer/socketproxy.go` (`buildCreateBody`: `Entrypoint ["/bin/sleep"]` + `Cmd ["infinity"]`, tmpfs `/home/node: rw,size=64m` alongside `/tmp`, `NetworkMode` from `spec.NetworkName`, ro bind `<spec.SupervisorBin>:/usr/local/bin/garrison-supervisor`, container-level `Env` forced empty, `garrison.shape_hash` label = hex SHA-256 of the marshaled body); `supervisor/internal/agentcontainer/controller.go` (`ContainerSpec` gains `SupervisorBin string`; the unused `EnvVars` field is deleted — FR-002 structural, analyze C2); `supervisor/internal/agentcontainer/spec.go` (new — `ContainerName(agentID)` exported wrapper over `shortID`; `SpecForAgent(p AgentSpecParams) ContainerSpec` building the agent-ID-keyed workspace path `<WorkspaceFS>/<full-agent-uuid>`); `supervisor/internal/agentcontainer/socketproxy_test.go` / `spec_test.go` (new tests).
  - **Completion condition**: Tests pass: `TestBuildCreateBodyIdleEntrypointTmpfsNetwork` (FR-005), `TestBuildCreateBodyMountsSupervisorBinaryReadOnly` (FR-014), `TestBuildCreateBodyOmitsContainerLevelEnv` (FR-002), `TestShapeHashDeterministicAndFieldSensitive` (FR-007), `TestContainerNameUsesShortAgentID` (FR-008), `TestSpecForAgentKeysWorkspaceByAgentUUID` (FR-006). Sealed caps unchanged (ReadonlyRootfs, CapDrop ALL, memory/CPU/pids assertions still green). migrate7 still passes its existing args (old keying, `NetworkName:"none"`) and its tests still pass — containers it creates now idle instead of exiting(1), which is strictly less broken.
  - **Out of scope for this task**: switching migrate7/skillinstall to `SpecForAgent` (T006); the boot reconcile (T007); compose network (T005).

## Phase 3 — Deployment substrate

- [ ] **T005** Compose: agents network, egress proxy, squid allow-list, restart permission, workspace binds
  - **Depends on**: T004 (nothing consumes the network yet; landing it first keeps T006 bootable).
  - **Files**: `supervisor/docker-compose.yml` (top-level `networks:` block — `garrison-agents` with `name: garrison-agents`, `internal: true`; new `egress-proxy` service: `ubuntu/squid` **pinned by digest captured at this task** (`docker pull` then pin), `container_name: garrison-egress-proxy`, dual-homed `[default, garrison-agents]`, ro config mount, `restart: unless-stopped`; `postgres` gains `networks: [default, garrison-agents]`; `docker-proxy` gains `ALLOW_RESTARTS: 1`; `supervisor` gains identical-path binds for `${GARRISON_AGENT_WORKSPACE_FS:-/var/lib/garrison/workspaces}` and `${GARRISON_AGENT_SKILLS_FS:-/var/lib/garrison/skills}` following the `GARRISON_SHARED_DIR` precedent comment); `supervisor/egress/squid.conf` (new — exactly the plan §10 content: CONNECT allow-list `api.anthropic.com:443` only, `access_log stdio:/dev/stdout`, `cache deny all`).
  - **Completion condition**: `docker compose config -q` passes. With the stack up: `docker exec`-side probe from a container on `garrison-agents` shows `curl -x http://garrison-egress-proxy:3128 https://example.com` **denied** (403 in `docker logs garrison-egress-proxy`) and `curl -x … https://api.anthropic.com` CONNECT **succeeds** (FR-009; runbook 03 §3.4 shape). Postgres resolves by its existing hostname from the agents network. The supervisor boots and runs a direct-exec ticket exactly as before (nothing consumes the new pieces yet).
  - **Out of scope for this task**: config knobs (T006); socket-proxy policy-test script assertions (T015); runbook text (T016).

- [ ] **T006** Config knobs + migrate7/skillinstall switch to `SpecForAgent`
  - **Depends on**: T005.
  - **Files**: `supervisor/internal/config/config.go` (add `AgentsNetwork` — `GARRISON_AGENTS_NETWORK`, default `"garrison-agents"`; `EgressProxyURL` — `GARRISON_EGRESS_PROXY_URL`, default `"http://garrison-egress-proxy:3128"`; `UseDirectExec` default **stays true** in this task); `supervisor/internal/config/config_test.go` (extend); `supervisor/internal/migrate7/run.go` (Deps gain `NetworkName` + `SupervisorBin`; spec construction via `agentcontainer.SpecForAgent` — agent-UUID workspace keying replaces `deps.WorkspaceFS + "/" + agent.RoleSlug` at run.go:113-114; `os.MkdirAll` the per-agent workspace dir before create); `supervisor/internal/skillinstall/actuator.go` (container_create step builds its spec via `SpecForAgent`, same new Deps); `supervisor/cmd/supervisor/main.go` (thread `cfg.AgentsNetwork` + the supervisor-binary host path — the existing `GARRISON_SUPERVISOR_BIN_OVERRIDE`/shared-dir value — into both); existing migrate7/skillinstall tests (update fixtures).
  - **Completion condition**: `TestAgentsNetworkAndEgressProxyDefaults` passes in config. migrate7 unit + integration tests updated and green: created containers carry the agents network, agent-ID-keyed `/workspace` bind, supervisor-binary ro mount, shape-hash label. Per-department workspace paths remain untouched on the direct-exec side (FR-006: legacy concern only; no data migration — workspaces are scratch). Boot against the dev stack grandfathers a fresh agent into a running idle container on `garrison-agents`. Full suite green.
  - **Out of scope for this task**: the default flip + guard removal (T012); shape reconcile of pre-existing containers (T007); spawn-side anything.

- [ ] **T007** Boot shape reconcile
  - **Depends on**: T006.
  - **Files**: `supervisor/internal/agentcontainer/shape.go` (new — `ReconcileShape(ctx, specs []ContainerSpec) (ShapeReport, error)` on the Controller interface; `ShapeReport{Created, Recreated, Restarted, Unchanged []string}`); `supervisor/internal/agentcontainer/controller.go` (interface + report types); `supervisor/internal/agentcontainer/fake.go` (fake impl); `supervisor/internal/agentcontainer/shape_test.go` (new); `supervisor/cmd/supervisor/main.go` (`buildAgentContainerRuntime`: after `migrate7.Run`, list via `ListAgentsForContainerReconcile` (T001), MkdirAll workspace dirs, build specs via `SpecForAgent`, call `ReconcileShape`, write `agent_container_events` rows from the report — `removed`+`created` pair per Recreated, `created` per Created, `started` per Restarted; failures warn-and-continue like migrate7).
  - **Completion condition**: Tests pass: `TestReconcileShapeRecreatesOnMissingOrStaleLabel` (US4 AS-1 — the Exited(1)/unlabeled fleet), `TestReconcileShapeNoopWhenHashMatches` (US4 AS-2), `TestReconcileShapeCreatesMissingContainer` (US4 AS-3), `TestReconcileShapeStartsStoppedMatchingContainer`, `TestReconcileShapeNeverTouchesForeignContainers` (chat/compose services unaddressed). Boot against the live dev stack converges every active agent's container to the new shape and leaves it running; a second boot logs all-Unchanged and performs zero container mutations (SC-005 at unit level; integration pin in T014). Full suite green.
  - **Out of scope for this task**: spawn-side container lookup (T011); integration test for convergence (T014).

## Phase 4 — Spawn pipeline

- [ ] **T008** `mcpconfig.Render` split + container server set
  - **Depends on**: T001 (no hard dep on T002–T007; sequenced here because spawn work consumes it next).
  - **Files**: `supervisor/internal/mcpconfig/mcpconfig.go` (`Render(params WriteParams) ([]byte, string, error)` extracted from `Write`; `Write` becomes Render-then-write-file with byte-identical output; `WriteParams` gains `OmitMempalace bool`); `supervisor/internal/mcpconfig/mcpconfig_test.go` (extend).
  - **Completion condition**: Tests pass: `TestRenderMatchesWriteOutput` (Render bytes == Write file content for a representative params set), `TestRenderOmitMempalaceForContainerPath` (exactly `postgres` + `finalize` + `garrison-mutate` entries — FR-014, Q1), `TestRenderStillRejectsVaultPatternServers` (Rule 3 checks run in Render regardless of mode). All existing mcpconfig tests pass unchanged. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: the in-container write exec (T011); changing any DSN or server command values (FR-010: trust model unchanged).

- [ ] **T009** Spawn extractions: argv builder, secret-env helper, transport-parameterized runner
  - **Depends on**: T008.
  - **Files**: `supervisor/internal/spawn/spawn.go` (inline argv block at lines 725-736 → `buildClaudeArgv(p argvParams) []string`; secret env-append block at lines 800-807 → `appendSecretEnv(env []string, fetched map[string]vault.SecretValue) []string` carrying the single sanctioned `UnsafeBytes` injection site; steps 9–12 at lines 839-1087 → call into the new runner); `supervisor/internal/spawn/runner.go` (new — `runClaudeSession(ctx, deps, t transport, p sessionParams) error` with `transport{Stdout, Stderr io.Reader; Terminate func(escalate bool) error; ExitDetail func(ctx) WaitDetail}`; drain-before-Wait ordering encoded: `ExitDetail` is called only after `pipelineDone` and `stderrDone` close — concurrency rule 8); `supervisor/internal/spawn/spawn_test.go` (add `TestBuildClaudeArgvGoldenLegacy` pinning the exact legacy flag sequence).
  - **Completion condition**: **Zero behavioral diff for direct-exec.** The entire existing spawn/pipeline/adjudicate/finalize test suite passes unchanged (no test-file edits beyond the new golden test). Direct-exec's transport wraps `killProcessGroup` and the existing drain-then-`cmd.Wait`+`extractExit` logic. `TestBuildClaudeArgvGoldenLegacy` passes. A live direct-exec ticket on the dev stack completes identically (manual smoke). `gofmt -l` + `go vet` clean. This task's commit message records "refactor only — no behavior change" so the soak-window diff walk stays honest.
  - **Out of scope for this task**: any container transport (T011); moving the dispatch branch (T011); inflight slots (T010).

- [ ] **T010** Per-agent in-flight slot
  - **Depends on**: T009.
  - **Files**: `supervisor/internal/spawn/inflight.go` (new — `AgentInflight` mutex + `map[string]struct{}`; `TryAcquire(agentID) (release func(), ok bool)`); `supervisor/internal/spawn/spawn.go` (Deps gains `Inflight *AgentInflight`; acquire in `prepareSpawn` immediately after `concurrency.CheckCap` and before `InsertRunningInstance`, agent resolved via `AgentsCache` by (department, role); failure → `spawnPrep{done:true}` without marking the event processed — identical to cap-full; release handle rides `spawnPrep`, deferred past the terminal write; **enforcement active only when `!UseDirectExec && AgentContainer != nil`**); `supervisor/internal/spawn/inflight_test.go` (new).
  - **Completion condition**: Tests pass: `TestPerAgentSlotDefersSecondEvent` (FR-017 — second concurrent event for the same agent defers, no instance row), `TestPerAgentSlotReleasedAfterTerminalWrite`, `TestPerAgentSlotIndependentOfDepartmentCap` (two different agents in one department bounded only by the cap). The three new tests construct Deps with `UseDirectExec=false` and a non-nil fake controller explicitly (the gate is inert under the still-default direct-exec — analyze I1). Existing spawn tests pass unchanged. `gofmt -l` + `go vet` clean.
  - **Out of scope for this task**: department cap changes (none — Constitution X); container pipeline (T011).

- [ ] **T011** The real container pipeline
  - **Depends on**: T003, T008, T009, T010 (and the substrate from T005–T007 for live verification).
  - **Files**: `supervisor/internal/spawn/m7.go` (rewritten — constants `containerClaudeBin`/`containerSupervisorBin`/`containerMCPDir`/`timeoutKillGrace=10s`/`containerCtxSlack=30s`; full plan §5 step list: `ContainerName(agent.ID)` lookup (deletes `containerNameForRole`), `mcpconfig.Render` with `OmitMempalace`, config-write exec via `GARRISON_MCP_CONFIG_JSON` env + `sh -c 'umask 077; printf %s … > /tmp/mcp-<instance-uuid>.json'`, deferred `rm -f` cleanup exec under `context.WithoutCancel`, `buildClaudeArgv` with the in-container mcpPath wrapped in `/usr/bin/timeout --signal=TERM --kill-after=10s <SubprocessTimeout>s`, exec env per plan D17 (HOME, HTTPS_PROXY, telemetry-off pair per FR-011, `ENABLE_TOOL_SEARCH=false`, auth passthrough, `appendSecretEnv`), `execCtx = SubprocessTimeout + containerCtxSlack`, `Exec` with `WorkingDir:"/workspace"`, container transport — `Terminate` → `Controller.Restart`, `ExitDetail` → `ExecSession.ExitCode` mapped per plan D21 (124 → DeadlineExceeded/timeout; 125–127 sans result frame → spawn_failed; 128+n → Signaled; inspect failure → −1), acceptance-gate workspace path = the agent-ID-keyed dir `<WorkspaceFS>/<agent-uuid>` (analyze U1), `UpdatePID` skipped); `supervisor/internal/spawn/spawn.go` (dispatch branch moves to just after step 3 — wake-up + vault fetch — since the container path owns its own config write and argv); `supervisor/internal/spawn/m7_test.go` (rewritten).
  - **Completion condition**: Tests pass (fake controller + scripted exec sessions): `TestContainerArgvWrapsCoreutilsTimeout`, `TestContainerArgvMatchesDirectExecFlagSet` (golden vs `buildClaudeArgv` — FR-013/US3), `TestContainerMCPConfigPathUsesInstanceID`, `TestContainerExecEnvComposition`, `TestSecretsNeverInArgvOrCreateBody` (SC-003), `TestExit124AdjudicatesTimeout`, `TestExit125To127AdjudicateSpawnFailed`, `TestExit137AdjudicatesSignaledSigkill`, `TestExecCreateFailureWritesSpawnFailedEventRetryable`, `TestConfigWriteExecFailureWritesSpawnFailed`, `TestConfigCleanupExecRunsOnEveryExitPath`, `TestContainerLookupKeyedByAgentIDNotRole` (FR-008 regression pin), `TestShutdownMidExecRestartsContainer`, `TestUpdatePIDSkippedOnContainerPath`. No new exit_reason strings exist anywhere (FR-019 — grep gate). Live smoke on the dev stack with `GARRISON_USE_DIRECT_EXEC=false` set **explicitly**: one seeded ticket completes dispatch → in-container exec → finalize → transition, init frame cwd `/workspace` (US1/SC-001). Full suite green with the default still true.
  - **Out of scope for this task**: flipping the default / removing the guard (T012); integration test files (T013); chaos (T015).

- [ ] **T012** Flip the default, remove the guard
  - **Depends on**: T011.
  - **Files**: `supervisor/internal/config/config.go` (`UseDirectExec` default `true` → `false`; env still parses — the rollback lever per FR-018/M7 retro, the flag is NOT removed); `supervisor/internal/config/config_test.go` (`TestUseDirectExecDefaultsFalse`); `supervisor/cmd/supervisor/main.go` (`buildAgentContainerRuntime` returns `ctrl, cfg.UseDirectExec` instead of `ctrl, true`; the `UseFakeAgent || proxy URL empty` force-direct guard **stays**; spawn.Deps wiring carries `Inflight` + `EgressProxyURL` + anything T011 left env-threaded).
  - **Completion condition**: `TestUseDirectExecDefaultsFalse` passes. Full suite green (fake-agent tests still run direct-exec via the surviving guard). Live boot pair on the dev stack: default boot runs a ticket in-container; `GARRISON_USE_DIRECT_EXEC=true` boot runs the same ticket as a supervisor child with zero exec API calls (US3 independent test). `agent_instances` rows from the pair are indistinguishable in shape (US3 AS-2 spot check). This commit is the milestone's single behavior-flip commit — small and revertable.
  - **Out of scope for this task**: removing the flag (post-soak polish, out of milestone); any feature work.

## Phase 5 — Integration and chaos suites

- [ ] **T013** Golden-path integration suite (the milestone's smoke test)
  - **Depends on**: T012.
  - **Files**: `supervisor/internal/spawn/m7_1_integration_test.go` (new, `//go:build integration` — testcontainers postgres + httptest fake docker proxy streaming canned claudeproto NDJSON in raw-frame encoding).
  - **Completion condition**: `TestContainerPipelineEndToEnd` — full `Spawn` with `UseDirectExec=false`: healthy init frame (mcp servers connected, cwd `/workspace`), finalize tool_use ok, result frame → finalize atomic commit, kanban transition, terminal row matching the direct-exec contract (US1 AS-1/2/3). `TestContainerPipelineMCPGateBails` — init with a failed server → `mcp_<server>_<status>` exit_reason + `Restart` invoked (FR-108 carried, FR-013). `TestBothModesProduceIdenticalRowShape` — same canned stream under each flag value → identical populated/NULL column sets (US3 AS-2, SC-004 row-shape half). All three pass under `-tags=integration`; default-tag suite untouched.
  - **Out of scope for this task**: boot convergence (T014); real-docker scenarios (T015); fixing pipeline bugs by editing tests — bugs found here patch T011's files.

- [ ] **T014** Boot-convergence integration test
  - **Depends on**: T013.
  - **Files**: `supervisor/internal/migrate7/m7_1_reconcile_integration_test.go` (new, `//go:build integration`).
  - **Completion condition**: `TestBootConvergenceFromOldShapeFleet` — fake proxy pre-seeded with old-shape (unlabeled, `Exited(1)`, role-keyed) containers, including at least one hired (never-grandfathered, `last_grandfathered_at IS NULL`) agent (analyze C1) → one boot pass recreates each to the new shape and writes the `removed`+`created` event-row pairs; a second pass reports all-Unchanged and issues zero mutating docker calls (SC-005, US4 AS-1/2/3). Passes under `-tags=integration`.
  - **Out of scope for this task**: live-fleet verification (T017 acceptance walks it); migrate7 behavior changes (any bug patches T006/T007 files).

- [ ] **T015** Chaos tests + socket-proxy policy assertions
  - **Depends on**: T013.
  - **Files**: `supervisor/internal/spawn/chaos_m7_1_test.go` (new, `//go:build chaos`, gated by `GARRISON_CHAOS_DOCKER` — skips cleanly without it); `scripts/socket-proxy-policy-test.sh` (extend: restart endpoint now allowed; still-denied endpoints still denied).
  - **Completion condition**: With `GARRISON_CHAOS_DOCKER` set against the live dev stack: `TestBlackholeEgressTerminatesWithinBudget` — real agent container, `HTTPS_PROXY` pointed at a dropping endpoint, short budget → exit 124 → `exit_reason=timeout` within budget (SC-006; spike F3 structurally prevented). `TestDeniedConnectFailsFast` — live deny-all squid → claude-error-class exit within seconds (clarification 2026-06-10). `TestRestartBackstopRestoresIdleSleep` — `Restart` mid-exec → container running with PID 1 `sleep infinity`. Without the env var, all three skip. `socket-proxy-policy-test.sh` passes with the `ALLOW_RESTARTS` assertions added.
  - **Out of scope for this task**: proxy-log telemetry review (T017 acceptance, SC-002/§3.4); new failure-mode features.

## Phase 6 — Docs, acceptance, retro

- [ ] **T016** True the docs to implemented behavior
  - **Depends on**: T012 (text describes shipped behavior; written before acceptance so T017 can verify runbook commands as-written).
  - **Files**: `docs/security/agent-sandbox-threat-model.md` (four dated amendment notes per FR-020: shared-network deviation, mempalace sidecar absence, egress-proxy-as-Rule-3 reading, exec-Env secret transit acceptance with the vault threat-model cross-note); `docs/runbooks/03-*.md` (§3.4–§3.6 trued per FR-021: real container names, caps/egress check commands, `docker logs garrison-egress-proxy` for denials, §3.5 exec log lines, §3.6 probe-ticket procedure); `docs/ops-checklist.md` (M7.1 section: host dir creation, compose deltas, first-boot reconcile expectations, rollback = `GARRISON_USE_DIRECT_EXEC=true`).
  - **Completion condition**: Every §3.4 command in the runbook executes as written against the live dev stack and produces the documented output. The four threat-model notes each reference the context file's §Scope deviation by name and carry the 2026-06-10 date. Ops-checklist M7.1 steps reproduce a working deploy from a clean host dir state. No competitor/source-org naming anywhere (standing rules).
  - **Out of scope for this task**: ARCHITECTURE.md amendment (ships with the retro/post-merge per M7 precedent); RATIONALE edits (none — no decision changed).

- [ ] **T017** Acceptance: scripted SC walk
  - **Depends on**: T013, T014, T015, T016.
  - **Files**: `scripts/m7-1-acceptance.sh` (new — walks SC-001…SC-006 as concrete invocations: seeded-ticket in-container run + init-frame cwd grep (SC-001); runbook §3.4 caps/egress checks + §3.6 probe (SC-002); secret-hygiene greps over `docker inspect`, argv, supervisor logs (SC-003); full suite in both flag positions + live boot-pair tickets (SC-004); convergence boot pair against the live fleet (SC-005); proxy-stopped typed-failure-within-budget run (SC-006); prints SC-007 as a retro deliverable).
  - **Completion condition**: The script runs end-to-end and every step passes. **If a step fails: open a focused patch against the relevant earlier task's files, then re-run the entire script from the top.** No new features land in this task. Sonar new-code issues queried and cleared before declaring done (M8 pattern). `gofmt -l` + `go vet` clean; CI green on the PR (check `gh pr view --json mergeable` first if CI doesn't fire).
  - **Out of scope for this task**: anything not already specced — gaps discovered here that aren't SC regressions go to the retro's open questions, not into code.

- [ ] **T018** Retro (dual deliverable)
  - **Depends on**: T017.
  - **Files**: `docs/retros/m7-1.md` (new — canonical); MemPalace `wing_company / hall_events` drawer mirror via `mempalace_add_drawer` (+ `mempalace_kg_add` for cross-milestone facts worth surfacing).
  - **Completion condition**: Retro covers everything AGENTS.md requires: what shipped (T001–T017 summary); what the spec got wrong; dependencies added outside the locked list (expected: zero Go/TS deps; the digest-pinned squid compose service called out explicitly); open questions deferred (M7.1b gateway MCP, flag-removal polish PR, egress grants, per-customer proxies/per-agent networks); and the spike-payoff accounting — SC-007's prevention-vs-discovery tally against `docs/research/m7-1-spike.md` F1–F7 and the two clarify probes, per RATIONALE §13. Notes whether any task pushed lint-failing code (standing retro habit). Both deliverables landed; markdown canonical on disagreement.
  - **Out of scope for this task**: new code of any kind; RATIONALE/ARCHITECTURE edits beyond what the operator explicitly requests post-merge.

---

## What this task list does not include

Per the context's out-of-scope list and the plan's deferrals: no MCPJungle
gateway MCP for agents (M7.1b), no skill-install actuator remediation work, no
per-customer proxies or per-agent networks (M9+), no egress grants for
third-party skills, no `UseDirectExec` flag removal (post-soak polish PR), no
workspace cycling / diary-vs-reality completion, no agent image changes (the
image already carries `timeout` and `sleep` — verified at plan time), no
dashboard work, and no frontend tests (standing operator rule).
