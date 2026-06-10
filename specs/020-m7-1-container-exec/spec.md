# Feature Specification: M7.1 — real container execution pipeline

**Feature Branch**: `020-m7-1-container-exec`
**Created**: 2026-06-10
**Status**: Draft
**Input**: `specs/_context/m7-1-context.md` (binding) + `docs/research/m7-1-spike.md` (ground truth, cited as F1–F7)

Ticket agents currently execute as supervisor children even though the
M7 container substrate exists; `UseDirectExec` is guard-forced on and
the per-agent containers sit unused (see "Why this milestone now" in
m7-1-context.md). This spec covers moving real ticket execution into
the per-agent sandbox, with the egress allow-list delivered per
sandbox Rule 3 as amended ("Scope deviation" in m7-1-context.md:
shared `garrison-agents` network; mempalace out of the in-container
sidecar set).

Decisions Q1–Q8 from the context's "Open questions" section were
resolved with the operator on 2026-06-10 and are bound in the
requirements below; they are not open for re-litigation at clarify.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A ticket executes inside the agent's sandbox (Priority: P1)

The operator boots the supervisor with container execution on (the new
default). A ticket dropped into a department's `in_dev` column is
dispatched normally; the agent's claude process runs inside that
agent's persistent container — not as a supervisor child — uses its
MCP tools, finalizes, and the ticket transitions exactly as it does
today under direct-exec.

**Why this priority**: this is the milestone. Everything else
qualifies or protects it.

**Independent Test**: seed one `in_dev` ticket for an active agent;
observe the exec running in the agent's container, the finalize
commit, and the kanban transition. No other story needs to exist.

**Acceptance Scenarios**:

1. **Given** the supervisor booted with container execution enabled
   and an active agent with a running container, **When** a ticket
   lands on a channel the agent listens for, **Then** an exec is
   created in that agent's container and the claude init frame shows
   the container-side cwd (`/workspace`).
2. **Given** the same run, **When** the agent calls its MCP tools
   (pgmcp, finalize, garrison-mutate), **Then** the tools work from
   inside the container and the finalize atomic commit lands
   identically to the direct-exec path (same transitions, hygiene,
   palace writes via the finalize payload).
3. **Given** a completed run, **When** the operator inspects
   `agent_instances`, **Then** status/exit_reason/cost fields are
   populated by the same adjudication contract as direct-exec (no new
   row shapes, no new statuses).
4. **Given** vault-granted secrets for the role, **When** the exec is
   created, **Then** secrets transit only via exec-create Env (spike
   F2) and appear in neither `docker inspect` of the container nor any
   argv.

---

### User Story 2 - The sandbox demonstrably holds (Priority: P2)

The operator runs runbook 03 §3.4/§3.6 against a live agent container
and every check passes: resource caps are real, egress is allow-listed
to the Anthropic API only, telemetry is off, and the preamble survives
an adversarial prompt.

**Why this priority**: the sandbox is the reason M7.1 exists, but it
is only verifiable once US1 executes inside the container.

**Independent Test**: with at least one container-executed run having
happened, run the §3.4 commands and the §3.6 probe ticket.

**Acceptance Scenarios**:

1. **Given** a live agent container, **When** the operator inspects
   it, **Then** memory/CPU/pids caps are non-zero and rootfs is
   read-only (Rules 2/5).
2. **Given** a shell exec inside the container, **When** it attempts
   to reach a non-allow-listed host, **Then** the connection is denied
   at the egress proxy; **When** claude reaches
   `api.anthropic.com`, **Then** it succeeds only via the proxy
   (spike F4).
3. **Given** a container-executed run, **When** the proxy log is
   reviewed for that window, **Then** no telemetry-host CONNECTs were
   attempted (telemetry disable env set, Q7) and none succeeded
   (proxy deny).
4. **Given** a probe ticket asking the agent to dump its environment,
   **When** the run finalizes, **Then** the output does not contain
   secret values (preamble + Rule hardening; runbook 03 §3.6).

---

### User Story 3 - Rollback lever stays live (Priority: P3)

The operator can set `UseDirectExec=true` and the system runs exactly
as it does today (the M7-retro soak plan). The legacy path is
untouched and its test suites keep passing.

**Why this priority**: container execution ships behind a reversible
default flip; an unusable rollback invalidates the soak plan.

**Independent Test**: boot with the flag true; run one ticket; confirm
the process is a supervisor child and no exec API calls occur.

**Acceptance Scenarios**:

1. **Given** `UseDirectExec=true`, **When** a ticket runs, **Then** the
   direct-exec pipeline handles it with no agentcontainer involvement.
2. **Given** the flag flips between boots, **When** tickets run under
   each mode, **Then** rows in `agent_instances` are
   indistinguishable in shape (only the runtime differs).

---

### User Story 4 - The container fleet converges at boot (Priority: P3)

Whatever state the per-agent containers are in (the current
`Exited(1)` fleet, a stale create-shape, missing entirely), one boot
brings every active agent's container to the new shape, running idle,
and repeated boots are no-ops.

**Why this priority**: without convergence the flip strands existing
agents; with it, M7.1 deploys onto the live alpha without manual
surgery.

**Independent Test**: boot against the existing fleet; verify
recreation; boot again; verify no-op.

**Acceptance Scenarios**:

1. **Given** an agent container created with the old shape, **When**
   the supervisor boots, **Then** the container is removed and
   recreated with the new shape (idle entrypoint, HOME tmpfs, agents
   network, config-hash label — Q8) and left running.
2. **Given** a container already at the current shape, **When** the
   supervisor boots, **Then** no remove/recreate occurs (label
   comparison short-circuits).
3. **Given** an active agent with no container, **When** the
   supervisor boots, **Then** one is created (existing migrate7
   grandfather behavior, new shape).

---

### Edge Cases

- Egress proxy container down at spawn time → claude cannot reach the
  API; the run must terminate within the timeout budget with a typed
  failure, not hang indefinitely (in-container `timeout` wrapper, Q3).
- Behavior of claude on a *denied* CONNECT (proxy 403):
  [NEEDS CLARIFICATION: spike characterized allowed-proxy and
  no-network (hang, F3) but not deny-at-proxy; a short probe decides
  whether denial fails fast or rides the timeout].
- Agent container missing or stopped at spawn time → typed
  `spawn_failed`-class exit, event stays retryable per existing
  semantics; the boot reconciler (US4) is the repair path.
- Exec-create or exec-start rejected by the socket proxy → typed
  failure, no `agent_instances` row stranded in running.
- Timeout fires mid-run → in-container `timeout -k` delivers
  SIGTERM→grace→SIGKILL (Q3); exit code 124 maps to
  `exit_reason=timeout`; supervisor-side ctx expiry triggers the
  container-restart backstop (proxy gains `ALLOW_RESTARTS`).
- Supervisor crashes mid-exec → startup recovery already flips
  stranded running rows (NFR-006 amendment); the orphaned in-container
  process is bounded by the in-container timeout; the restarted
  supervisor must not double-attribute its output.
- Two events for the same agent arrive together → per-agent
  serialization (Q4): at most one in-flight exec per container; the
  second defers under existing cap/defer semantics.
- Squid restarted mid-run → in-flight CONNECTs drop; the run fails
  within budget like any API-network error; no special handling.
- MCP-config write exec fails → same contract as today's host-side
  `mcpconfig.Write` failure: `spawn_failed`, dispatcher continues.

## Requirements *(mandatory)*

### Functional Requirements

Transport (spike F2 is ground truth):

- **FR-001**: The exec transport MUST demultiplex the docker raw
  stream (8-byte frame headers) into distinct stdout/stderr streams;
  stdout feeds the existing claudeproto consumer unchanged.
- **FR-002**: The exec transport MUST support per-exec environment
  injection via the exec-create body, and this MUST be the only
  transit for secrets and runtime env (no container-level Env, no
  argv).
- **FR-003**: The exec transport MUST surface the exec's exit code
  (exec-inspect) to the adjudication layer.
  [NEEDS CLARIFICATION: whether exec-inspect's PID field is
  host-namespace or container-namespace — affects only which
  "verifiably inside the container" check SC-001 uses.]
- **FR-004**: No stdin attachment: the prompt rides argv exactly as in
  direct-exec; connection hijacking is out of contract.

Container shape (Rules 2/4/5; spike F1/F5; Q2/Q8):

- **FR-005**: Agent containers MUST be created with an idle
  entrypoint override so the container runs independently of any exec,
  with tmpfs mounts for `/tmp` and the agent user's HOME, the existing
  cap set unchanged (read-only rootfs, cap-drop ALL, memory/CPU/pids,
  unprivileged user), and attached to the `garrison-agents` network at
  create (replacing the shipped `NetworkMode: none` placeholder; Rule
  3 as amended).
- **FR-006**: Workspace mounts are keyed per AGENT
  (`/var/lib/garrison/workspaces/<agent-id>` → `/workspace`, rw) with
  the skills mount unchanged; the host-side directory is ensured at
  create/reconcile time. In-container claude runs with cwd
  `/workspace`. Per-department workspace paths remain a direct-exec
  legacy concern only; no data migration (workspaces are scratch,
  Rule 7). (Q2)
- **FR-007**: Every container carries a create-shape hash label;
  boot-time reconciliation compares the label and removes + recreates
  on mismatch, leaves matching containers untouched, and creates
  missing ones. Repeated boots with no shape change are no-ops. (Q8)
- **FR-008**: One container naming convention everywhere:
  `garrison-agent-<short-agent-id>` (what migrate7 already creates).
  The spawn-side lookup uses the same convention; the role-keyed
  lookup is removed.

Egress (spike F3/F4; Rule 3 as amended; Q5/Q7):

- **FR-009**: A shared egress proxy joins the `garrison-agents`
  internal network and is the only route out; its CONNECT allow-list
  contains exactly `api.anthropic.com`. The allow-list lives in a
  static config file committed in the repo and mounted by compose.
  Denied attempts are observable in the proxy's container logs. (Q5)
- **FR-010**: Postgres joins the `garrison-agents` network so the
  in-container stdio MCP servers reach it; DSN trust model is
  unchanged from direct-exec (agent_ro reads, instance-scoped
  finalize, main-DSN garrison-mutate in agent mode).
- **FR-011**: The spawn env for container execs MUST set both
  `DISABLE_TELEMETRY=1` and
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`; combined with the
  proxy deny, steady-state runs attempt no non-Anthropic egress. (Q7)
- **FR-012**: `network=none` is NOT a valid execution configuration
  (spike F3: claude hangs rather than fails offline); the spec
  supersedes the literal `none` reading wherever M7 docs carry it,
  per the context's scope-deviation section.

Spawn pipeline:

- **FR-013**: The container execution path MUST reuse the direct-exec
  pipeline's sealed contracts unchanged: argv builder, claudeproto
  consumer, FR-108 MCP health gate (pending-tolerant), budget
  enforcement, finalize observer, terminal adjudication, typed
  exit_reason vocabulary, and event/poll retry semantics. Only the
  transport differs.
- **FR-014**: The per-spawn MCP config is written into the container's
  `/tmp` tmpfs via an exec before the claude exec, references the
  read-only-mounted static supervisor binary (spike F6), and is
  removed on every exit path — mirroring today's host-side config
  lifecycle. The config carries pgmcp + finalize + garrison-mutate;
  mempalace is absent (Q1; scope deviation 2).
- **FR-015**: Synthesized and seeded agent_md guidance MUST NOT
  instruct agents to call mempalace tools they do not have under
  container execution; wording shifts to "use the wake-up context
  provided". Palace writes continue exclusively via the finalize
  payload. (Q1)
- **FR-016**: Timeout discipline: the claude exec is wrapped in
  coreutils `timeout` with SIGTERM, a kill-after grace, and the
  per-spawn budget, so enforcement lives in-container; exit code 124
  maps to `exit_reason=timeout`. As backstop, supervisor-side context
  expiry restarts the agent container via the proxy (which gains
  restart permission); restart is the SIGKILL analog and must leave
  the container back in idle state. (Q3; AGENTS.md rules 4/7
  translation)
- **FR-017**: At most one in-flight exec per agent container,
  enforced supervisor-side, independent of department caps. (Q4)
- **FR-018**: `UseDirectExec` defaults to false; the guard that
  currently overrides the flag is removed; setting it true restores
  the legacy path byte-for-byte (US3). The flag itself is NOT removed
  (post-soak polish per the M7 retro).
- **FR-019**: New failure surfaces map to typed exit_reasons without
  inventing new vocabulary where existing reasons fit: container
  missing/stopped and exec-create/start rejection land in the
  `spawn_failed` class; in-container timeout lands in `timeout`;
  adjudication precedence is unchanged.

Documentation deliverables (bound here so the milestone is not done
without them):

- **FR-020**: `agent-sandbox-threat-model.md` gains dated amendment
  notes for: shared-network deviation, mempalace sidecar absence,
  egress-proxy-as-Rule-3 reading, and the exec-Env secret transit
  acceptance (vault threat model cross-note: localhost-only proxy,
  already docker-socket-trusted). (Q6; scope deviations)
- **FR-021**: Runbook 03 §3.4–§3.6 are trued to the implemented
  behavior (expected log lines, container names, egress checks) as
  part of this milestone.

### Key Entities

- **Agent container**: one per active `agents` row (M7 substrate,
  unchanged identity); new attributes: create-shape hash label, idle
  process, agents-network membership.
- **Exec**: a per-spawn process inside the agent container; carries
  the per-exec Env (secrets, telemetry-off, instance id); its exit
  code feeds adjudication. No persistence of its own — the existing
  `agent_instances` row remains the record.
- **Egress proxy**: shared sidecar; static allow-list; the only
  non-internal route from the agents network.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: With container execution on, a seeded ticket completes
  dispatch → in-container exec → finalize → transition with zero
  manual intervention, and the run is verifiably in-container (init
  frame cwd `/workspace`, plus the PID check pending FR-003's
  clarification).
- **SC-002**: Runbook 03 §3.4 passes in full: caps non-zero,
  `example.com` denied, `api.anthropic.com` reachable; §3.6 probe
  leaks no secret values.
- **SC-003**: Secret hygiene: across a container-executed run, no
  secret value appears in container inspect output, argv, or any
  supervisor log (vaultlog discipline unchanged).
- **SC-004**: Both execution modes green: the full existing test suite
  passes with the flag in each position, and a live ticket completes
  under each mode on the same boot pair.
- **SC-005**: Fleet convergence: starting from the current live state
  (grandfathered old-shape containers), one boot converges every
  active agent's container to the new shape; a second boot performs
  zero container mutations.
- **SC-006**: A run with the egress proxy stopped terminates with a
  typed failure within the configured budget (no indefinite hang —
  the F3 failure mode is structurally prevented).
- **SC-007**: Spike prevention accounting for the retro: issues that
  would have surfaced without the spike (per RATIONALE §13's
  validation habit) are tallied against post-ship surprises.

## Assumptions

- Spike findings (F1–F7) hold on the deployed engine/proxy versions;
  the spike ran against the same live stack M7.1 deploys to.
- coreutils `timeout` is present in the agent image's base
  (node:22-slim); verified at plan time alongside the telemetry env
  probe (Q7's mechanism is asserted by outcome, not assumption).
- The squid sidecar is a compose-level runtime addition, not a Go
  dependency: the locked dependency list is untouched.
- Single-customer alpha: shared agents network and one shared proxy
  are accepted deviations recorded in the context; per-agent networks
  and per-customer proxies remain M9+.
- The chat container path (M5.1 topology) is unaffected: chat spawns
  its own container via `docker run` and is out of this milestone's
  scope.
- mcpjungle bearer injection (M8 T010) is not wired in this milestone;
  the exec-Env mechanism this spec ships is the seam it will use.

## Flagged for /speckit.clarify

Only two items, both behavior probes rather than decisions:

1. Denied-CONNECT behavior (edge case above) — fail-fast vs
   ride-the-timeout; affects failure latency only.
2. Exec-inspect PID namespace (FR-003) — selects the SC-001
   in-container verification mechanism.
