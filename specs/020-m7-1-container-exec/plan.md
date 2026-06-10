# Implementation plan: M7.1 — real container execution pipeline

**Branch**: `020-m7-1-container-exec` | **Date**: 2026-06-10 | **Spec**: [spec.md](./spec.md)
**Input**: spec.md (FR-001–FR-021, clarifications 2026-06-10), `specs/_context/m7-1-context.md` (binding, incl. §Scope deviation), `docs/research/m7-1-spike.md` (F1–F7, ground truth)
**Decision slate**: D1–D25 approved by the operator 2026-06-10, including both flagged
recommendations (Env-transit MCP-config write; `ubuntu/squid` digest-pinned).

## Summary

M7 shipped the per-agent container substrate; `runRealClaudeViaContainer` is an
outline and a guard forces direct-exec. M7.1 makes the container path real: the
exec transport (8-byte demux, per-exec Env, exit codes — spike F2), the new
container shape (idle entrypoint, HOME tmpfs, `garrison-agents` network,
shape-hash label — F1/F5, FR-005–FR-008), a shared squid egress proxy with a
one-host allow-list (F4, FR-009–FR-012), and a container variant of the spawn
pipeline that reuses every sealed contract — argv builder, claudeproto consumer,
FR-108 gate, finalize observer, adjudication, typed exit reasons — with only the
transport swapped (FR-013–FR-019). `UseDirectExec` defaults to false; setting it
true restores the legacy path unchanged (US3, the M7-retro soak lever).

Plan-time verifications performed (spec §Assumptions): `/usr/bin/timeout`
(coreutils 9.1) and `/bin/sleep` both present in `garrison-claude:m5`;
`agent_container_events.kind` CHECK constraint enumerated (recreates are
recorded with the existing `removed` + `created` kinds — no schema change).

## Technical context

**Language/version**: Go 1.23+ (supervisor only; no dashboard work this milestone)
**Primary dependencies**: unchanged locked list (AGENTS.md). The squid sidecar is
a compose-level runtime addition, not a Go dependency — the locked list is untouched.
**Storage**: Postgres (no new tables/columns; one data-only migration for FR-015
agent_md wording; one new sqlc query for boot reconcile)
**Testing**: `go test` (unit), `-tags=integration` (testcontainers postgres +
httptest fake docker proxy), `-tags=chaos` gated by `GARRISON_CHAOS_DOCKER`
**Target platform**: Linux, compose stack (Hetzner self-host posture)
**Project type**: long-running Go supervisor + docker compose deployment
**Constraints**: sealed spawn-pipeline contracts (FR-013); concurrency rules 1–8
translated to container land (FR-016/FR-017); vaultlog discipline (`UnsafeBytes`
call-site count unchanged); no stdin attach / connection hijacking (FR-004)
**Scale/scope**: single-customer alpha; one shared agents network + one shared
proxy (context §Scope deviation; per-agent networks and per-customer proxies M9+)

## Constitution check

- **I (Postgres is truth)** — pass. No event-bus or schema-shape change; the
  existing `agent_instances` row remains the record (spec Key entities).
- **III (Agents are ephemeral)** — pass. The *container* persists as substrate
  (sealed M7 surface); the *agent* remains one exec per event, spawned and dead.
- **VII (Go, locked deps)** — pass. Zero new Go dependencies; demux is ~30 lines
  in-tree. Squid is a compose runtime service, flagged here per the soft rule.
- **VIII (contexts everywhere)** — pass. Exec streams derive from
  request contexts; the demux goroutine ends when the raw stream closes; the
  in-container `timeout` plus the supervisor ctx backstop deliver rules 4/7
  analogs (FR-016). Rule 8 (drain before Wait) carries into the shared runner.
- **IX (narrow, end-to-end)** — pass. One transport swap, verifiable live
  (SC-001/SC-002); rollback lever preserved.
- **X (department caps)** — pass. Caps unchanged; per-agent serialization
  (FR-017) layers beneath them without altering cap semantics.

No violations; complexity tracking not needed.

## Project structure

### Documentation (this feature)

```text
specs/020-m7-1-container-exec/
├── spec.md       # approved
├── plan.md       # this file
└── tasks.md      # /garrison-tasks output (next)
```

### Source code (changed and new files)

```text
supervisor/
├── internal/agentcontainer/
│   ├── controller.go        # Exec signature replaced; Restart added; ContainerSpec extended
│   ├── socketproxy.go       # exec Env/WorkingDir/exit-code; create body new shape; shape hash
│   ├── demux.go             # NEW — 8-byte raw-stream demultiplexer (F2)
│   ├── shape.go             # NEW — boot shape reconcile (FR-007, Q8)
│   ├── spec.go              # NEW — SpecForAgent, ContainerName (single shape source)
│   ├── fake.go              # FakeController updated to new method set
│   └── *_test.go            # see Test strategy
├── internal/spawn/
│   ├── spawn.go             # argv builder extracted; dispatch branch moves before host mcpconfig.Write
│   ├── runner.go            # NEW — transport-parameterized consumer/adjudicate/terminal block
│   ├── m7.go                # runRealClaudeViaContainer becomes the real pipeline
│   ├── inflight.go          # NEW — per-agent in-flight slot (FR-017, Q4)
│   └── *_test.go
├── internal/mcpconfig/mcpconfig.go   # Render() split out of Write(); container server set
├── internal/config/config.go         # UseDirectExec default false; 2 new knobs
├── internal/migrate7/run.go          # spec construction via SpecForAgent (agent-ID keying)
├── internal/skillinstall/actuator.go # container_create step uses SpecForAgent
├── internal/store/                   # sqlc regen: ListGrandfatheredAgentsForReconcile
├── cmd/supervisor/main.go            # guard removed; shape reconcile wired after migrate7
├── docker-compose.yml                # garrison-agents network; egress-proxy; postgres dual-home;
│                                     # ALLOW_RESTARTS; supervisor identical-path workspace binds
├── egress/squid.conf                 # NEW — committed allow-list (FR-009, Q5)
└── scripts/m7-1-acceptance.sh        # NEW — SC-001…SC-007 walk
migrations/
└── 20260610000000_m7_1_agent_md_container_wording.sql  # NEW — data-only (FR-015, Q1)
docs/
├── security/agent-sandbox-threat-model.md   # dated amendment notes (FR-020, Q6)
├── runbooks/03-*.md                          # §3.4–§3.6 trued (FR-021)
├── ops-checklist.md                          # M7.1 deploy steps
└── retros/m7-1.md                            # post-ship (dual deliverable)
```

**Structure decision**: no new Go packages (slate D1). All Go work extends
`agentcontainer`, `spawn`, `mcpconfig`, `config`, `migrate7`, `skillinstall`,
and `cmd/supervisor`. New non-Go artifacts are compose-level (D2).

## Subsystem walkthroughs

### 1. `internal/agentcontainer` — exec transport (FR-001–FR-004; spike F2)

`Controller.Exec` is **replaced**, not augmented (D3). The M8-retro ripple
warning is honored by construction: the only callers are the m7.go outline, the
fake, and tests (chat uses `docker run` via CLI; mempalace wake-up uses the
docker CLI). New method set:

```go
type ExecSpec struct {
    Cmd        []string // full argv, argv[0] absolute
    Env        []string // per-exec env — the ONLY transit for secrets/runtime env (FR-002)
    WorkingDir string   // "/workspace" for claude execs (FR-006)
}

type ExecSession struct {
    ID     string
    Stdout io.ReadCloser // demuxed stream 1
    Stderr io.ReadCloser // demuxed stream 2
}
// ExitCode polls GET /exec/<id>/json until Running=false (≤10 polls, 200ms apart),
// then returns ExitCode. If still running after the poll budget: (-1, error) —
// the caller's adjudication falls back to result-frame evidence.
func (s *ExecSession) ExitCode(ctx context.Context) (int, error)

type Controller interface {
    Create(ctx context.Context, spec ContainerSpec) (string, error)
    Start(ctx context.Context, containerID string) error
    Stop(ctx context.Context, containerID string) error
    Restart(ctx context.Context, containerID string) error   // NEW — FR-016 backstop
    Remove(ctx context.Context, containerID string) error
    Exec(ctx context.Context, containerID string, spec ExecSpec) (*ExecSession, error)
    ConnectNetwork(ctx context.Context, containerID, networkName string) error
    Reconcile(ctx context.Context, expected []ExpectedContainer) (ReconcileReport, error)
    ReconcileShape(ctx context.Context, specs []ContainerSpec) (ShapeReport, error) // NEW — FR-007
    ImageDigest(ctx context.Context, imageRef string) (string, error)
}
```

Implementation notes (socketproxy.go):

- Exec-create body gains `"Env"`, `"WorkingDir"`, `AttachStdout/AttachStderr:
  true`, `AttachStdin: false` (FR-004 — no hijacking; the response body is a
  normal chunked `application/vnd.docker.raw-stream`).
- `Restart` is `POST /containers/<id>/restart?t=5`. The socket proxy must allow
  it (compose: `ALLOW_RESTARTS: 1`, §10).
- Exec-create against a missing/stopped container returns the docker 404/409;
  both map to `ErrContainerNotFound` so spawn lands in `spawn_failed` (FR-019).
- Exec-inspect's `Pid` is host-namespace (clarification 2026-06-10) — it is
  read for nothing. Verification rests on exec-by-construction + init-frame cwd
  (FR-003, SC-001).

**Demux** (`demux.go`, D4): a ~30-line loop reading `[stream(1) pad(3)
size(4)]` frames from the raw stream, copying payloads into two `io.Pipe`s
(stream 1 → Stdout, 2 → Stderr, 0 treated as stdout). EOF or a malformed
header closes both pipe writers (error propagated via `CloseWithError`), which
unblocks the consumer exactly like subprocess-stdout EOF does today. No
goroutine outlives the raw stream; closing `ExecSession.Stdout` closes the raw
response body, tearing the whole chain down (rule 1 compliance).

Helper execs (MCP-config write/cleanup, §5) use this same `Exec` method — no
separate API (D5).

### 2. `internal/agentcontainer` — create shape, hash, naming (FR-005–FR-008)

`buildCreateBody` changes (D6, spike F1/F5/F6):

| Field | Old | New |
|---|---|---|
| `Entrypoint` / `Cmd` | image default (`claude`, exits 1) | `["/bin/sleep"]` / `["infinity"]` |
| `Tmpfs` | `/tmp: rw,size=64m` | + `/home/node: rw,size=64m` (F5, same option string the spike proved) |
| `NetworkMode` | `"none"` placeholder | `spec.NetworkName` = the agents network (FR-012 supersedes `none`) |
| `Binds` | workspace + skills | + `<spec.SupervisorBin>:/usr/local/bin/garrison-supervisor:ro` (F6, FR-014) |
| `Labels` | `garrison.agent_id`, `garrison.managed` | + `garrison.shape_hash` |
| `Env` | (whatever the spec carried) | **always empty** — container-level Env is banned (FR-002) |

Caps (`ReadonlyRootfs`, `CapDrop ALL`, memory/CPU/pids, unprivileged user) are
unchanged — sealed M7 surface (Rules 2/5).

**Shape hash** (D7): `garrison.shape_hash` = hex SHA-256 of the marshaled
`containerCreateBody` JSON, computed inside `buildCreateBody` after all fields
are set and before the label is added. It covers every per-agent field, so any
future shape edit (or a workspace/image/uid change for one agent) triggers
recreate for exactly the affected containers.

**One spec source** (`spec.go`): `SpecForAgent(p AgentSpecParams) ContainerSpec`
builds the per-agent spec — image digest, host UID, **agent-ID-keyed**
workspace `<WorkspaceFS>/<full-agent-uuid>` (D8; FR-006 — migrate7's current
role-slug keying at `run.go:113-114` is the change site), skills path
(unchanged keying), agents network name, supervisor binary host path. Callers:
migrate7, the boot shape reconcile, and skillinstall's `container_create` step.
`ContainerSpec` gains a `SupervisorBin string` field.

**Naming** (D9): exported `ContainerName(agentID string) string` returning
`"garrison-agent-" + shortID(agentID)` (what migrate7 already creates). Spawn's
role-keyed `containerNameForRole` (`spawn/m7.go:98`) is deleted — the
acceptance-diary latent bug (FR-008).

### 3. `internal/agentcontainer` — boot shape reconcile (FR-007; US4; Q8)

New `shape.go` (D10). `ReconcileShape(ctx, specs)` per spec:

1. Inspect the container named `ContainerName(spec.AgentID)`.
2. **Missing** → ensure-create-start → report `Created`.
3. **Label mismatch** (old shape has no `garrison.shape_hash` at all — the
   live `Exited(1)` fleet — or a stale hash) → stop, remove, create, start →
   report `Recreated`.
4. **Hash match, not running** → start → report `Restarted`.
5. **Hash match, running** → no-op → report `Unchanged`.

`ShapeReport{Created, Recreated, Restarted, Unchanged []string}` (agent IDs).
The reconcile only considers containers it addresses by the agent-ID name — the
chat container and other compose services are never touched. Repeated boots
with no shape change report everything `Unchanged` (SC-005, AS US4-2).

Event recording stays out of `agentcontainer` (no store dependency): the caller
in `cmd/supervisor` writes `agent_container_events` rows from the report — a
`removed` + `created` pair per `Recreated` agent, `created` per `Created`,
`started` per `Restarted` — all already-valid CHECK kinds, zero schema change.

Host workspace dirs (`<WorkspaceFS>/<agent-uuid>`) are `os.MkdirAll`'d by the
caller before building each spec (FR-006 "ensured at create/reconcile time");
the identical-path compose bind (§10) makes supervisor-side MkdirAll
materialize at the docker daemon's bind source.

### 4. `internal/spawn` — shared argv builder and runner extraction (FR-013)

Two extractions, both behavior-preserving for direct-exec (D22; US3):

**Argv builder**: the inline block at `spawn.go:725-736` becomes
`buildClaudeArgv(p argvParams) []string` (params: task description, model,
budget, mcpPath, systemPrompt). Both transports call it; only `mcpPath`
differs (host `/var/lib/garrison/mcp/mcp-config-<id>.json` vs in-container
`/tmp/mcp-<id>.json`). A golden test pins the legacy flag set byte-for-byte.

**Runner** (`runner.go`): the step 9–12 block (`spawn.go:839-1087` — pipeline
goroutine + stderr mirror + shutdown select + wait detail + acceptance gate +
finalize-committed short-circuit + Adjudicate + terminal write) is factored
into `runClaudeSession(ctx, deps, t transport, p sessionParams) error` with:

```go
type transport struct {
    Stdout     io.Reader
    Stderr     io.Reader
    Terminate  func(escalate bool) error      // direct: killProcessGroup(TERM/KILL);
                                              // container: Controller.Restart (the SIGKILL analog)
    ExitDetail func(ctx context.Context) WaitDetail // direct: drain-then-cmd.Wait + extractExit;
                                              // container: ExecSession.ExitCode + §5 mapping
}
```

`sessionParams` carries what the block already closes over: instance/event/
ticket IDs, role + origin column, agent, dept, wake-up stdout/status,
finalize expectations + onCommit wiring, from/to columns, the acceptance-gate
workspace path. The drain-before-Wait ordering (concurrency rule 8) lives
inside the runner: `ExitDetail` is only called after `pipelineDone` and
`stderrDone` close. Direct-exec behavior is byte-identical; the existing spawn
and pipeline test suites must pass unchanged — that is the refactor's
acceptance gate.

The **dispatch branch moves earlier**: today it sits after host-side
`mcpconfig.Write` and argv build (`spawn.go:746`). It moves to just after step
3 (wake-up + vault fetch), because the container path writes its MCP config
inside the container and builds argv with the in-container path. Steps 1–3
(prepare, hashes, wake-up, vault fetch, Rule-3 `CheckExtraServers`) stay
shared and run before the branch in both modes.

### 5. `internal/spawn/m7.go` — the container pipeline (FR-013–FR-016, FR-019)

`runRealClaudeViaContainer` is rewritten from outline to the real path.
Constants: `containerClaudeBin = "/usr/local/bin/claude"`,
`containerSupervisorBin = "/usr/local/bin/garrison-supervisor"`,
`containerMCPDir = "/tmp"`, `timeoutKillGrace = 10 * time.Second`,
`containerCtxSlack = 30 * time.Second`.

Step list (inputs: everything `sessionParams` needs, plus `agent`, `fetched`
vault values, rendered MCP config bytes):

1. `name := agentcontainer.ContainerName(agent.ID)` (FR-008).
2. Render the MCP config (§6) — pgmcp + finalize + garrison-mutate, no
   mempalace (FR-014, Q1); command paths reference `containerSupervisorBin`;
   DSNs unchanged (FR-010).
3. **Config-write exec** (D15, operator-approved): `Exec` with
   `Env: ["GARRISON_MCP_CONFIG_JSON=<rendered bytes>"]`, `Cmd: ["/bin/sh",
   "-c", "umask 077; printf %s \"$GARRISON_MCP_CONFIG_JSON\" >
   /tmp/mcp-<instance-uuid>.json"]`. Non-zero exit or transport error →
   `spawn_failed` (same contract as today's host-side `mcpconfig.Write`
   failure — spec edge case). The config content transits exec-create Env
   exactly like the secrets do (Q6's recorded acceptance); never argv, never
   stdin (FR-002/FR-004 — this is the deliberate deviation from spike F6's
   `cat >` sketch).
4. **Deferred cleanup exec** on every exit path: `Cmd: ["/bin/rm", "-f",
   "/tmp/mcp-<instance-uuid>.json"]` under `context.WithoutCancel` + a short
   timeout (mirrors the host-side `mcpconfig.Remove` defer). Best-effort:
   failure logs a warning; the file is on tmpfs and dies with the container.
5. Build argv: `buildClaudeArgv(... mcpPath="/tmp/mcp-<id>.json")`, then wrap
   (FR-016, Q3): `["/usr/bin/timeout", "--signal=TERM",
   "--kill-after=10s", "<SubprocessTimeout seconds>", containerClaudeBin,
   argv...]`. Exit 124 ⇒ TERM-killed on budget; 137 ⇒ KILL after grace.
6. Compose exec env (D17) — the only env transit (FR-002):
   `HOME=/home/node`, `HTTPS_PROXY=<cfg.EgressProxyURL>`,
   `DISABLE_TELEMETRY=1`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`
   (FR-011, Q7), `ENABLE_TOOL_SEARCH=false` (acceptance-diary pin),
   `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` passed through from the
   supervisor's own env when set, then vault values via the **shared**
   `appendSecretEnv(env, fetched)` helper — extracted from the direct-exec
   block so the sanctioned `UnsafeBytes()` production call-site count stays at
   two (AGENTS.md vaultlog rule).
7. `execCtx := context.WithTimeout(context.WithoutCancel(ctx),
   deps.SubprocessTimeout + containerCtxSlack)` — the in-container `timeout`
   always fires first; ctx expiry is the blackhole backstop only (clarified
   2026-06-10: proxy 403 denial fails fast as a claude-error, it does not ride
   the timeout).
8. `sess, err := Exec(execCtx, name, ExecSpec{Cmd, Env, WorkingDir:
   "/workspace"})` (FR-006 cwd). Error → `spawn_failed`, event stays
   retryable (FR-019; the boot reconciler is the repair path).
9. Hand off to `runClaudeSession` with the container transport:
   - `Terminate` → `Controller.Restart(name)` — the SIGKILL analog; the idle
     `sleep` PID 1 restarts and the container returns to idle (FR-016). Used
     by the ctx backstop, supervisor shutdown, and the bail hook (MCP-gate /
     parse-error bail), each of which today group-kills.
   - `ExitDetail` → `sess.ExitCode(ctx)` mapped to `WaitDetail` (D21):
     `124` → `ContextErr = context.DeadlineExceeded` (Adjudicate's existing
     timeout row → `exit_reason=timeout`); `125`/`126`/`127` with no result
     frame seen → `spawn_failed` class; `128+n` → `Signaled=true` with the
     decoded signal (`signaled_SIGKILLNAME` via `FormatSignalled`, e.g. 137 →
     `signaled_SIGKILL`); inspect failure → exit code −1, result frames
     govern. No new exit_reason strings anywhere (FR-019).
10. The runner's finalize-committed short-circuit, Adjudicate precedence, cost
    handling, and `writeTerminalCostAndWakeup` are the shared code — identical
    row shapes by construction (US1 AS-3, US3 AS-2).

`UpdatePID` is **skipped** on the container path (pid stays NULL): the exec's
host-namespace PID is not a supervisor child and recording it would invite
PID-level signaling the transport cannot honor. The column is nullable; row
*shape* is unchanged. `recordM7HashesForInstance` runs before the branch,
unchanged.

### 6. Per-agent serialization (FR-017; Q4) — `internal/spawn/inflight.go`

`AgentInflight` (mutex + `map[string]struct{}` keyed by agent UUID) held in
`Deps` (constructed in main). Acquire happens in `prepareSpawn` immediately
after `concurrency.CheckCap` and **before** `InsertRunningInstance`
(`spawn.go:296-377`), resolving the agent via the in-memory `AgentsCache` by
(department, role). Acquire failure returns the same outcome as cap-full:
`spawnPrep{done:true}` without marking the event processed — existing
defer/retry semantics, untouched (FR-013). The release handle rides
`spawnPrep` and is deferred until after the terminal write. Enforcement is
active only on the container path (`!UseDirectExec && AgentContainer != nil`)
so the legacy path's observable behavior is untouched (US3); department caps
are independent and unchanged.

### 7. `internal/mcpconfig` — Render split (FR-014; D16)

`Render(params WriteParams) ([]byte, string, error)` is extracted from
`Write` (returns the marshaled JSON + the basename `mcp-config-<id>.json`);
`Write` becomes Render-then-write-file, byte-identical output. `WriteParams`
gains `OmitMempalace bool`: the container path sets it (Q1; scope deviation 2)
so the config carries exactly pgmcp + finalize + garrison-mutate. Rule 3
checks (`CheckExtraServers`, `RejectVaultServers`, banned-pattern rejection)
run identically in Render — defense-in-depth is mode-independent. The
container path passes `SupervisorBin: containerSupervisorBin` and the same
DSNs as direct-exec (FR-010: `agent_ro` reads, instance-scoped finalize,
main-DSN garrison-mutate in agent mode — trust model unchanged).

### 8. `internal/config` — defaults and knobs (FR-018; D14/D18)

- `UseDirectExec` default flips `true` → `false` (`GARRISON_USE_DIRECT_EXEC`
  still parses; setting it `true` is the rollback lever — the flag is NOT
  removed, post-soak polish per the M7 retro).
- New: `AgentsNetwork string` (`GARRISON_AGENTS_NETWORK`, default
  `"garrison-agents"`) and `EgressProxyURL string`
  (`GARRISON_EGRESS_PROXY_URL`, default
  `"http://garrison-egress-proxy:3128"`). Nothing else.

### 9. `cmd/supervisor/main.go` — wiring

`buildAgentContainerRuntime` (`main.go:1050-1091`):

1. The `return ctrl, true` force-on guard is **removed**; the function returns
   `ctrl, cfg.UseDirectExec` (FR-018). The `UseFakeAgent || proxy URL empty`
   guard stays and forces direct-exec — existing unit/integration suites run
   unchanged.
2. After `migrate7.Run` (which now builds specs via `SpecForAgent` — new shape,
   agent-ID workspace keying, agents network): list reconcile targets via the
   new sqlc query `ListGrandfatheredAgentsForReconcile` (`SELECT id,
   role_slug, host_uid, image_digest FROM agents WHERE last_grandfathered_at
   IS NOT NULL`), MkdirAll each workspace dir, build specs, call
   `ReconcileShape`, and write `agent_container_events` rows from the report
   (§3). Boot order means agents migrate7 just created reconcile as
   `Unchanged`.
3. `spawn.Deps` gains `Inflight` and the egress/network config values.

Reconcile failures log and degrade exactly like migrate7 failures do today
(warn + continue): a broken container surfaces per-spawn as `spawn_failed`
retryable, and the next boot repairs (spec edge case).

### 10. Compose, egress proxy, squid.conf (FR-009–FR-012; D19/D20; Q5)

`supervisor/docker-compose.yml`:

- First explicit `networks:` block: `garrison-agents` with `name:
  garrison-agents` (stable name for create-time `NetworkMode`) and
  `internal: true` (no direct egress).
- New service `egress-proxy`: image `ubuntu/squid` **pinned by digest**
  (operator-approved; digest captured at implement time), `container_name:
  garrison-egress-proxy`, dual-homed (`default` + `garrison-agents`), mounts
  `./egress/squid.conf:/etc/squid/squid.conf:ro`, `restart: unless-stopped`.
  Access log goes to stdout — denied CONNECTs are observable via
  `docker logs garrison-egress-proxy` (FR-009; runbook 03 §3.4 truing cites
  this).
- `postgres` adds `networks: [default, garrison-agents]` — the service-name
  alias on the agents network keeps the existing DSN hostnames working
  (FR-010).
- `docker-proxy` adds `ALLOW_RESTARTS: 1` (FR-016 backstop; the socket-proxy
  policy-test script gains the corresponding allow/deny checks).
- `supervisor` adds two identical-path binds following the existing
  `GARRISON_SHARED_DIR` precedent (`docker-compose.yml:84-89`):
  `${GARRISON_AGENT_WORKSPACE_FS:-/var/lib/garrison/workspaces}` and
  `${GARRISON_AGENT_SKILLS_FS:-/var/lib/garrison/skills}`, each mapped to the
  same absolute path. This is load-bearing for the first time: supervisor-side
  `MkdirAll` must land on the host so the agent-container bind sources resolve
  (FR-006). Documented in ops-checklist.
- Agent containers, chat, and mcpjungle are untouched (chat topology out of
  scope per spec §Assumptions).

`supervisor/egress/squid.conf` (committed, the entire allow-list — Q5):

```text
# M7.1 egress allow-list (spec FR-009). The only route out of the
# garrison-agents network. Exactly one CONNECT destination.
http_port 3128
acl anthropic dstdomain api.anthropic.com
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
http_access allow CONNECT anthropic
http_access deny all
access_log stdio:/dev/stdout
cache deny all
```

Log retention = docker's default json-file rotation; no extra machinery (Q5).

### 11. Migration — agent_md wording (FR-015; Q1; D23)

`migrations/20260610000000_m7_1_agent_md_container_wording.sql`, **data-only**:
targeted `UPDATE agents SET agent_md = replace(agent_md, <old>, <new>)`
statements that (a) replace the seeded "Mid-turn MemPalace usage (optional)"
section (the `mempalace_search` / `mempalace_list_drawers` /
`mempalace_add_drawer` guidance seeded by
`20260424000006_m2_2_2_compliance_calibration.sql:56-76`) with a short "Mid-turn
context" paragraph instructing agents to **use the wake-up context provided**,
and (b) drop `mempalace MCP (…)` from the "Tools available" list. `replace()`
no-ops on operator-edited rows where the seeded text is gone — idempotent and
non-clobbering. The down section restores the seeded text via the inverse
replaces. Palace writes continue exclusively via the finalize payload — that
wording stays.

The synthesized-hire template in `internal/garrisonmutate/approve.go` (line
310: "consult MemPalace for context") gets the same wording shift. The hygiene
checker's expectations are untouched (Q1: writes were never the agent's job
since M2.2.1).

### 12. sqlc additions

One new query (`ListGrandfatheredAgentsForReconcile`, §9). Reuse the existing
`agent_container_events` insert and `UpdateInstanceM7Hashes` queries. No
Drizzle/dashboard changes. No schema migration beyond §11's data-only one.

### 13. Documentation deliverables (FR-020/FR-021; D24)

- `docs/security/agent-sandbox-threat-model.md`: four dated amendment notes —
  shared-network deviation, mempalace sidecar absence, egress-proxy-as-Rule-3
  reading, exec-Env secret transit acceptance (cross-note to the vault threat
  model: proxy is localhost-only and already docker-socket-trusted) (Q6;
  context §Scope deviation).
- Runbook 03 §3.4–§3.6 trued to implemented behavior: real container names
  (`garrison-agent-<short-id>`), the §3.4 caps/egress check commands
  (`docker logs garrison-egress-proxy` for denials), §3.5 exec log lines,
  §3.6 probe-ticket procedure.
- `docs/ops-checklist.md`: M7.1 deploy steps (create host workspace/skills
  dirs, compose up the new network + proxy, expected first-boot reconcile
  log lines, rollback = `GARRISON_USE_DIRECT_EXEC=true`).
- `docs/retros/m7-1.md` + palace mirror, including SC-007 spike-prevention
  accounting (retro-time, not implementation).

## Lifecycle and state machines

### One container-executed spawn

```text
event → prepareSpawn (dedupe → cap check → per-agent slot → INSERT running row)
      → hashes → wake-up (supervisor-side, unchanged) → vault fetch + Rule 3 check
      → [branch: container path]
      → render MCP config → config-write exec ──fail──→ spawn_failed (retryable)
      → claude exec (timeout-wrapped argv, Env-injected, cwd /workspace)
              ──create/start rejected──→ spawn_failed (retryable)
      → shared runner: claudeproto pipeline ← demuxed stdout
              ├─ init frame: FR-108 MCP gate (bail → Restart container → mcp_<s>_<status>)
              ├─ finalize tool_use → observer → atomic commit (unchanged)
              └─ result frame → cost/terminal evidence
      → stream EOF → exec-inspect exit code → WaitDetail mapping (§5.9)
      → finalize committed? yes → done | no → Adjudicate → terminal write
      → cleanup exec (rm -f mcp config) → release per-agent slot
```

### Timeout ladder (FR-016; concurrency rules 4/7 translation)

```text
t = budget          in-container `timeout` sends SIGTERM to claude
t = budget + 10s    `timeout -k` sends SIGKILL                     → exit 124/137
t = budget + 30s    supervisor execCtx expires (blackhole backstop)
                    → Controller.Restart(container) — the SIGKILL analog;
                      idle `sleep` PID 1 returns; stream EOFs; exit_reason=timeout
supervisor shutdown → Restart(container) + supervisor_shutdown (existing precedence)
```

Denied CONNECT (proxy 403) never reaches this ladder: claude exits in ~1 s with
a terminal error frame (clarification 2026-06-10) and adjudicates as a
claude-error-class exit.

### Boot convergence (US4)

```text
boot → migrate7.Run (never-grandfathered agents → create with NEW shape)
     → ReconcileShape over all grandfathered agents:
         no container          → create + start            (Created)
         no/stale shape label  → stop+remove+create+start  (Recreated)  ← Exited(1) fleet
         label match, stopped  → start                     (Restarted)
         label match, running  → no-op                     (Unchanged)
     → event rows from report → second boot: all Unchanged (SC-005)
```

## Failure surface mapping (FR-019 — no new vocabulary)

| Surface | Detection | exit_reason |
|---|---|---|
| Container missing/stopped at spawn | exec-create 404/409 | `spawn_failed` (event retryable) |
| Exec-create/start rejected by proxy | non-2xx | `spawn_failed` |
| MCP-config write exec fails | helper exec exit ≠ 0 | `spawn_failed` |
| Egress proxy down (blackhole) | in-container timeout, exit 124 | `timeout` |
| Denied CONNECT (proxy live) | claude terminal error frame ~1 s | `claude_error` class (existing adjudication) |
| Timeout TERM ignored | `timeout -k` KILL, exit 137 | `signaled_SIGKILL` |
| timeout wrapper itself fails | exit 125/126/127, no result frame | `spawn_failed` |
| Supervisor ctx backstop | execCtx deadline → Restart | `timeout` |
| Supervisor shutdown mid-exec | root ctx done → Restart | `supervisor_shutdown` |
| Supervisor crash mid-exec | startup recovery flips stranded rows (existing, unchanged); orphan bounded by in-container timeout; restarted supervisor opens no stream to it — no double attribution | `supervisor_restarted` (existing) |
| MCP server unhealthy at init | FR-108 gate (unchanged) | `mcp_<server>_<status>` |
| Second event for same agent | per-agent slot at prepareSpawn | (defer — no row, no reason) |

## Test strategy

Go only (standing operator rule). Existing suites are a regression gate in both
flag positions (SC-004).

### Unit (default tags)

`internal/agentcontainer/demux_test.go`:
- `TestDemuxSplitsStdoutAndStderrFrames` — interleaved stream-1/stream-2 frames land on the right readers, payloads intact.
- `TestDemuxHandlesFrameSplitAcrossReads` — header and payload split across arbitrary read boundaries.
- `TestDemuxPropagatesEOFAndClose` — raw-stream EOF closes both pipes; closing Stdout tears down the raw body.
- `TestDemuxRejectsMalformedHeader` — unknown stream byte → both pipes `CloseWithError`.

`internal/agentcontainer/controller_test.go` (extend the existing httptest captureProxy pattern):
- `TestExecCreateBodyCarriesEnvWorkingDirNoStdin` — POST body has Env + WorkingDir, AttachStdin false (FR-002/FR-004/FR-006).
- `TestExecStartDemuxesRawStream` — canned framed response → ExecSession readers yield expected stdout/stderr bytes.
- `TestExecExitCodePollsUntilNotRunning` — inspect returns Running=true twice then ExitCode=124; session returns 124.
- `TestExecExitCodeGivesUpAfterPollBudget` — perpetual Running=true → (−1, error).
- `TestExecCreateOn404ReturnsErrContainerNotFound` — FR-019 mapping input.
- `TestRestartPostsRestartEndpoint` — `POST /containers/<id>/restart`.

`internal/agentcontainer/socketproxy_test.go` (create shape):
- `TestBuildCreateBodyIdleEntrypointTmpfsNetwork` — `/bin/sleep infinity`, `/tmp` + `/home/node` tmpfs, NetworkMode=agents network, ReadonlyRootfs, CapDrop ALL (FR-005).
- `TestBuildCreateBodyMountsSupervisorBinaryReadOnly` — `:ro` bind at `/usr/local/bin/garrison-supervisor` (FR-014).
- `TestBuildCreateBodyOmitsContainerLevelEnv` — Env always empty (FR-002).
- `TestShapeHashDeterministicAndFieldSensitive` — same spec → same hash; any field change → different hash (FR-007).
- `TestContainerNameUsesShortAgentID` — `garrison-agent-<8-char>` (FR-008).

`internal/agentcontainer/shape_test.go` (against FakeController + fake inspect data):
- `TestReconcileShapeRecreatesOnMissingOrStaleLabel` — the Exited(1)/old-shape case (US4 AS-1).
- `TestReconcileShapeNoopWhenHashMatches` — US4 AS-2.
- `TestReconcileShapeCreatesMissingContainer` — US4 AS-3.
- `TestReconcileShapeStartsStoppedMatchingContainer`
- `TestReconcileShapeNeverTouchesForeignContainers` — chat/compose containers unaddressed.

`internal/spawn/m7_test.go` (rewritten):
- `TestContainerArgvWrapsCoreutilsTimeout` — exact `timeout --signal=TERM --kill-after=10s <secs> /usr/local/bin/claude …` prefix (FR-016).
- `TestContainerArgvMatchesDirectExecFlagSet` — golden compare with `buildClaudeArgv` legacy output, only mcpPath differs (FR-013, US3).
- `TestContainerExecEnvComposition` — telemetry vars, HOME, HTTPS_PROXY, auth passthrough, vault values present; FR-011/D17 list exact.
- `TestSecretsNeverInArgvOrCreateBody` — SC-003 guard at the unit level.
- `TestExit124AdjudicatesTimeout`, `TestExit125To127AdjudicateSpawnFailed`, `TestExit137AdjudicatesSignaledSigkill` — D21 mapping (FR-019).
- `TestExecCreateFailureWritesSpawnFailedEventRetryable` — fake controller error → terminal row + event unprocessed.
- `TestConfigWriteExecFailureWritesSpawnFailed` — helper-exec exit 1.
- `TestConfigCleanupExecRunsOnEveryExitPath` — success, bail, and error paths each issue the `rm -f` exec.
- `TestContainerLookupKeyedByAgentIDNotRole` — regression pin for the FR-008 latent bug.
- `TestShutdownMidExecRestartsContainer` — root-ctx cancel → Restart called, `supervisor_shutdown` written.
- `TestUpdatePIDSkippedOnContainerPath` — pid stays NULL.

`internal/spawn/inflight_test.go`:
- `TestPerAgentSlotDefersSecondEvent` — second event for same agent returns cap-full-style defer, no instance row (FR-017).
- `TestPerAgentSlotReleasedAfterTerminalWrite`
- `TestPerAgentSlotIndependentOfDepartmentCap` — two different agents in one department still bounded only by the cap.

`internal/spawn` (existing files): `TestBuildClaudeArgvGoldenLegacy` added to `spawn_test.go`; every existing spawn/pipeline/adjudicate test passes unchanged (runner-extraction gate).

`internal/mcpconfig/mcpconfig_test.go` (extend):
- `TestRenderMatchesWriteOutput` — Render bytes == Write file content.
- `TestRenderOmitMempalaceForContainerPath` — exactly pgmcp + finalize + garrison-mutate (FR-014).
- `TestRenderStillRejectsVaultPatternServers` — Rule 3 mode-independent.

`internal/config/config_test.go` (extend):
- `TestUseDirectExecDefaultsFalse` (FR-018), `TestAgentsNetworkAndEgressProxyDefaults` (D18).

### Integration (`-tags=integration`; testcontainers postgres + httptest fake docker proxy)

`internal/spawn/m7_1_integration_test.go`:
- `TestContainerPipelineEndToEnd` — full `Spawn` with `UseDirectExec=false`; fake proxy streams canned claudeproto NDJSON (healthy init with `/workspace` cwd, finalize tool_use ok, result frame) in raw-frame encoding → finalize atomic commit, kanban transition, terminal row identical to the direct-exec contract (US1 AS-1/2/3).
- `TestContainerPipelineMCPGateBails` — init with a failed server → `mcp_<server>_<status>`, Restart invoked (FR-108 carried).
- `TestBothModesProduceIdenticalRowShape` — same canned stream under each flag value → same columns populated/NULL (US3 AS-2).

`internal/migrate7/m7_1_reconcile_integration_test.go`:
- `TestBootConvergenceFromOldShapeFleet` — fake proxy pre-seeded with old-shape `Exited(1)` containers → boot pass recreates + writes `removed`/`created` event rows; second pass mutates nothing (SC-005, US4).

### Chaos (`-tags=chaos`, gated by `GARRISON_CHAOS_DOCKER`; real docker)

`internal/spawn/chaos_m7_1_test.go`:
- `TestBlackholeEgressTerminatesWithinBudget` — real agent container, `HTTPS_PROXY` at a dropping endpoint, short budget → exit 124 → `timeout` (SC-006; F3 structurally prevented).
- `TestDeniedConnectFailsFast` — live deny-all squid → claude-error exit within seconds (clarification 2026-06-10).
- `TestRestartBackstopRestoresIdleSleep` — Restart mid-exec → container running, PID 1 is `sleep infinity`.

### Acceptance

`scripts/m7-1-acceptance.sh` — walks SC-001…SC-006 as test invocations + the
operator-run live checks (runbook 03 §3.4/§3.6, boot-pair flag flip), and
prints what is deferred to soak. SC-007 (spike prevention tally) is a retro
deliverable.

### Regression

`go test ./...` green; full suite additionally green with
`GARRISON_USE_DIRECT_EXEC=true` semantics (legacy default restored in tests
that construct config from env). M2.x suites untouched (US3 AS / spec SC-004).

## Deployment changes

- `supervisor/docker-compose.yml` — §10 in full (network, egress-proxy,
  postgres dual-home, `ALLOW_RESTARTS`, supervisor identical-path binds).
- `supervisor/egress/squid.conf` — new, committed (§10).
- `migrations/20260610000000_m7_1_agent_md_container_wording.sql` — §11.
- No Dockerfile changes: the agent image already carries `timeout` and
  `sleep` (verified); the supervisor image is untouched.
- `scripts/socket-proxy-policy-test.sh` — extend with restart-endpoint
  allow + still-denied checks.
- `docs/ops-checklist.md` — M7.1 section (§13).

## Rollback lever (US3)

`GARRISON_USE_DIRECT_EXEC=true` at boot: spawn takes the legacy
`exec.CommandContext` branch end-to-end (shared argv builder + runner produce
byte-identical behavior — pinned by the golden argv test and the unchanged
legacy suites). Container substrate keeps reconciling but is unused; no exec
API calls occur during ticket runs (US3 independent test). The flag's removal
remains post-soak polish (M7 retro), out of scope.

## Open questions remaining for /garrison-tasks

None structural. Two implement-time captures, both pre-approved in direction:

1. The `ubuntu/squid` image digest is captured (`docker pull` + pin) when the
   compose change lands.
2. The exact `replace()` old-strings for §11 are lifted verbatim from the live
   `agents.agent_md` rows / the M2.2.2 migration text at implement time.

## What this plan does not pre-decide

One-line forward hooks only: mcpjungle bearer injection (M8 T010) slots into
the exec-Env mechanism when M7.1b lands; per-agent networks + per-customer
proxies are M9+; egress grants for third-party skills wait for a skill that
needs them; workspace cycling / diary-vs-reality completion stays where M7
left it; chat-runtime parity rolls in with the post-soak flag-removal PR.

## Spec-kit flow next

`/garrison-tasks m7-1` → `/speckit.analyze` → `/garrison-implement m7-1` →
retro (markdown + palace mirror, incl. threat-model amendment notes and the
SC-007 prevention tally).
