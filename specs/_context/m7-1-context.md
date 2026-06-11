# M7.1 context — real container execution pipeline

Status: context for `/speckit.specify`. Prior milestone: M7 (hiring +
per-agent container substrate, shipped 2026-05-03; container *exec*
deliberately left as an outline behind the `UseDirectExec` guard).
Written on branch `020-m7-1-container-exec` after the 2026-06-10
M1–M8 acceptance run and the M7.1 spike.

Binding inputs (read before speccing — full annotations in §Binding
inputs below): `docs/research/m7-1-spike.md`,
`specs/_context/m7-context.md`,
`docs/security/agent-sandbox-threat-model.md`, `docs/retros/m7.md`,
`docs/retros/m8.md`, `specs/_context/m8-context.md`.

---

## Scope deviation from committed docs

Two deviations from `agent-sandbox-threat-model.md` Rule 3, both
operator-approved 2026-06-10:

1. **Shared agents network instead of per-agent networks.** Rule 3
   specifies a *per-agent* custom network containing only that agent's
   sidecars. M7.1 ships ONE internal `garrison-agents` network shared
   by all agent containers plus the sidecar set. Rationale: agent
   containers run no listening services (idle `sleep` + transient
   `claude` execs), so cross-agent reach is a low-value surface in the
   one-customer alpha, and per-agent networks add dynamic
   network-create machinery to every hire. Per-agent networks remain
   the multi-customer target and pair with the M9+ per-customer egress
   proxy work. The threat model should gain a dated amendment note
   referencing this section.
2. **Mempalace is absent from the in-container sidecar set.** Rule 3's
   default sidecar set names mempalace; its MCP server is stdio-only
   and runs via `docker exec` into the shared `garrison-mempalace`
   container — unreachable from inside an agent container (which has
   no docker socket, by design). Palace *writes* are unaffected: the
   M2.2.1 finalize path has the supervisor commit diary + KG triples
   from the finalize payload. What container-executed agents lose is
   mid-run `mempalace_search` context reads. Resolution options are an
   open question for the spec (§Open questions, Q1).

The literal `network=none` reading that the acceptance-run diary
flagged as a sealed-surface conflict turned out NOT to be one: Rule 3
itself specifies create-with-none *then* selective attach. The egress
proxy is Rule 3 as designed, with the proxy joining the sidecar set.

---

## Why this milestone now

M7 shipped the container substrate — migrate7 grandfathering, the
socket-proxy controller, cgroup caps, digest pinning, the preamble —
but `runRealClaudeViaContainer` is an outline and a guard forces
direct-exec even when `UseDirectExec=false`. Consequence, observed
live in the acceptance run: ticket agents execute as supervisor
children with the supervisor's filesystem and environment, the
per-agent containers sit `Exited(1)` and unused, and runbook 03's
sandbox checks (§3.4 caps/egress, §3.6 preamble hardening) are
unverifiable. M7.1 closes the gap between the threat model the
operator believes is in force and the runtime that actually executes
agent work.

The M7.1 spike (2026-06-10) de-risked every mechanical unknown — see
`docs/research/m7-1-spike.md` F1–F7. Headline: the full pipeline ran
green end-to-end (sandboxed container + exec-injected secrets +
in-container stdio MCP + claude through an HTTPS_PROXY allow-list),
and `network=none` execution is non-viable because claude hangs
rather than fails offline (F3).

---

## In scope

### Exec transport (`internal/agentcontainer`)

- Finish `socketProxyController.Exec`: the 8-byte stdout/stderr
  demultiplexer (spike F2), `Env` on exec-create (per-exec secret
  injection; secrets never land in container config or argv), and
  exec-inspect for exit codes. No stdin attach / connection hijacking
  — the prompt rides argv exactly as in direct-exec.
- Create-body changes (spike F1/F5): idle entrypoint override
  (`/bin/sleep infinity`), tmpfs for the agent user's HOME, and
  joining the `garrison-agents` network at create (replacing the
  shipped `NetworkMode: none` placeholder per Rule 3's attach step).
- Reconcile path for the already-grandfathered containers: existing
  `Exited(1)` containers must be recreated or migrated to the new
  create shape, idempotently, at boot (migrate7's job or a sibling).

### Egress (compose + new sidecar)

- New internal `garrison-agents` docker network.
- Shared squid sidecar, dual-homed (agents network + default network),
  CONNECT allow-list: `api.anthropic.com` only. Claude telemetry is
  disabled via env in the spawn pipeline AND denied at the proxy
  (operator decision 2026-06-10).
- Postgres joins the agents network (pgmcp/finalize/garrison-mutate
  DSN reach — same trust model as direct-exec today: `agent_ro` +
  instance-scoped finalize + main-DSN garrison-mutate in agent mode).

### Spawn pipeline (`internal/spawn`)

- `runRealClaudeViaContainer` becomes a real variant of the
  direct-exec pipeline sharing the same argv builder, claudeproto
  consumer, FR-108 MCP health gate, budget enforcement, finalize
  observer, and terminal adjudication — fed by the demuxed exec stream
  instead of `cmd.StdoutPipe`.
- Per-spawn MCP config written into the container's `/tmp` tmpfs via a
  small exec, referencing the read-only-mounted static supervisor
  binary (spike F6); removed on every exit path like today's host-side
  config.
- Secret injection (vault values, and later the M8 T010 MCPJungle
  bearer) moves to exec-create `Env`.
- Timeout/termination semantics translated to container land (open
  question Q3 — the spec must decide the mechanism, but delivering
  AGENTS.md concurrency rules 4/7 equivalents is in scope).
- Flip `UseDirectExec` default to false; the flag REMAINS as the
  rollback lever per the M7 retro's soak-window plan. Remove the
  guard that currently overrides the flag.
- Fix the container-name mismatch: migrate7 creates
  `garrison-agent-<short-agent-id>`; the spawn-side lookup must use
  the same convention (acceptance-run diary, latent-bug note).

### Verification

- Runbook 03 §3.4 (caps + egress deny/allow) and §3.6 (preamble
  hardening probe) become runnable; runbook errata for §3.5's exec log
  lines get trued up as part of the milestone's doc pass.

## Out of scope

1. **MCPJungle-gateway MCP for agents (M7.1b)** — all-gateway MCP with
   per-agent bearers is the end-state but depends on registering the
   in-tree servers in MCPJungle; its own milestone. The T010 bearer
   resolver slots into exec `Env` when that lands.
2. **Skill-install actuator / `agent_install_journal` pipeline** —
   separate remediation track; not container-exec work.
3. **Per-customer egress proxies and per-agent networks** — M9+ /
   multi-customer (see §Scope deviation).
4. **Egress grants for third-party skills** (threat-model open Q7 —
   propose/approve flow for agent-specific extra hosts): deferred
   until a hired skill actually needs HTTP egress.
5. **Removing the `UseDirectExec` flag** — post-soak polish PR per the
   M7 retro.
6. **Workspace cycling / diary-vs-reality verifier (Rules 7/8)
   completion** — only touched if the workspace-keying decision (Q2)
   forces it; otherwise stays where M7 left it.
7. **Agent image changes beyond what the spike proved necessary** — no
   python/mempalace-in-image experiments (see Q1).

---

## Binding inputs

| Doc | Why it binds |
|---|---|
| `docs/research/m7-1-spike.md` | Observed behavior: exec transport mechanics (F2), network=none hang (F3), HTTPS_PROXY honoring + observed egress surface (F4), HOME tmpfs (F5), in-container stdio MCP via binary mount (F6), full integration (F7). The spec must not contradict observed behavior. |
| `specs/_context/m7-context.md` | The sandbox rules' milestone framing; spawn-invocation thread this milestone completes; what M7 explicitly deferred. |
| `docs/security/agent-sandbox-threat-model.md` | The 10 sandbox rules. Rule 3 governs the network design (with the §Scope-deviation amendments); Rules 2/4/5 bind the create shape; Rule 6 binds proxy allow-listing. |
| `docs/retros/m7.md` | UseDirectExec flip conditions, OnComplete readiness signal, open questions this milestone inherits. |
| `docs/retros/m8.md` + `specs/_context/m8-context.md` | T010 bearer-injection seam (lands in exec Env later); Controller.Exec interface-ripple warning. |
| Acceptance-run diary (MemPalace, `claude-garrison-shakedown` / `garrison-dummy-company-bootstrap`) | Live errata: Exited(1) fleet, container-naming mismatch, ENABLE_TOOL_SEARCH pin, workspace MkdirAll behavior. |

## Open questions the spec must resolve

1. **Mempalace mid-run reads.** Accept the read-loss for container
   agents (wake-up context is already injected supervisor-side), build
   an stdio↔TCP bridge sidecar, or defer to the M7.1b gateway? If
   accepting the loss: does the hygiene checker's expectations or any
   agent_md text need adjusting so agents aren't instructed to call
   tools they no longer have?
2. **Workspace keying.** The threat model and M7 create-spec mount
   per-AGENT workspaces (`/var/lib/garrison/workspaces/<agent-id>`);
   the live direct-exec path uses per-DEPARTMENT paths
   (`…/workspaces/<dept-slug>` as cmd.Dir, now MkdirAll'd per spawn).
   Pick the canonical keying for container mounts, define the
   transition for existing dept workspaces, and state what `cwd`
   the in-container claude gets.
3. **Timeout + termination semantics.** Direct-exec kills the process
   group (AGENTS.md rules 4/7). Docker's exec API has no kill
   endpoint: candidates are a second exec issuing the in-container
   kill, or container restart as the SIGKILL analog. The spec must
   define SIGTERM-grace → SIGKILL behavior, what happens to the idle
   `sleep` PID 1, and the exit_reason mapping when the timeout fires.
4. **Concurrency per container.** Department caps currently serialize
   spawns (cap=1 in the alpha). If a cap >1 ever lands, multiple execs
   would share one container's pids/memory budget. State the
   constraint (e.g., per-agent serialization) rather than leaving it
   implicit.
5. **Squid config ownership.** Static allow-list file committed in the
   repo vs templated from env; log retention; whether denied CONNECTs
   surface anywhere the operator looks.
6. **Secret transit via proxy POST body.** Exec-create Env passes
   vault values through the socket proxy as JSON. The proxy is
   localhost-only and already docker-socket-trusted; the spec should
   record this acceptance explicitly (vault threat-model note) or
   choose an alternative.
7. **Telemetry disable mechanism.** Which env claude 2.1.170 actually
   honors for full telemetry-off (candidates: `DISABLE_TELEMETRY`,
   `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`) — verify, don't assume;
   the proxy denies regardless.
8. **Existing-container migration.** Recreate-on-boot for the
   grandfathered Exited(1) containers: in migrate7 or a new
   reconciler? What does idempotency look like across repeated boots?

## Acceptance criteria framing

Detailed criteria belong in the spec; frame them along these axes:

- **The flagship**: with `UseDirectExec=false`, a live ticket completes
  the full loop — dispatch → exec in the agent's container → MCP tool
  use → finalize → hygiene → kanban transition — with the claude
  process verifiably inside the container (exec-inspect PID, container
  cwd in the init frame).
- **Sandbox holds**: runbook 03 §3.4 passes — caps non-zero on
  inspect; `example.com` blocked from inside; `api.anthropic.com`
  reachable only via the proxy; telemetry hosts denied. §3.6 preamble
  probe behaves per the preamble.
- **Secret hygiene**: no token appears in `docker inspect` of the
  agent container or in any argv; exec-create Env is the only transit.
- **Rollback lever**: `UseDirectExec=true` still runs the legacy path
  green (the M2.x suites keep passing untouched).
- **Idempotent substrate**: repeated boots with existing containers
  converge (no duplicate containers, no flapping recreates).
- **Failure surfaces**: proxy down, container missing, exec failure
  each map to typed exit_reasons and leave the event/poll retry
  semantics intact.

## What this milestone is NOT

- Not the MCP gateway migration (M7.1b) — stdio MCP topology is
  deliberately preserved.
- Not a hiring-flow milestone — propose/approve/install surfaces are
  untouched except where container naming/reconcile forces contact.
- Not an egress-policy product — one shared proxy, one allow-list
  entry; grants UX comes later.
- Not the multi-customer isolation milestone — shared network is an
  accepted alpha deviation, recorded above.
- Not a chance to redesign spawn: the pipeline contract (claudeproto,
  budget, finalize, adjudication, typed exit reasons) is sealed; only
  the transport under it changes.

## Spec-kit flow

1. `/garrison-specify m7-1` — spec against this context (constitution
   unchanged).
2. `/speckit.clarify` — expected to burn down Q1–Q8 above.
3. `/garrison-plan m7-1`, `/garrison-tasks m7-1`, `/speckit.analyze`,
   `/garrison-implement m7-1`.
4. Retro per the M3+ dual policy (markdown + palace mirror), including
   the threat-model amendment notes from §Scope deviation and whether
   the spike's prevention count held.
