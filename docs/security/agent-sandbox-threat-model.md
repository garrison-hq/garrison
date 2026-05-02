# Agent sandbox — threat model and architectural rules

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

**Status**: Threat model and architectural rules. Drafted 2026-05-02 as
binding input to the M7 (hiring + per-agent runtime) context file. Sits
alongside `docs/security/vault-threat-model.md` (credential injection)
and `docs/security/chat-threat-model.md` (operator-vs-agent input
boundary) — those address orthogonal axes; this document covers the
agent's outbound blast radius.

**Last updated**: 2026-05-02 (initial draft).

**Precedence**: this document lives below `RATIONALE.md` and the active
milestone context in the document hierarchy (see `AGENTS.md`). The
active milestone context (`specs/_context/m7-context.md` once written)
supersedes this document for operational conflicts; this document
supplies the threat model and architectural principles that context
files cannot re-derive cheaply.

---

## Scope of this document

This is a threat model and a set of architectural rules. It is NOT a
spec, a plan, or an implementation. The spec-kit flow for any sandbox-
touching milestone begins from the relevant context file that cites
this document as binding input.

The document covers:

1. What the sandbox protects (assets)
2. Who it protects against (adversaries)
3. What threats it addresses and which it explicitly accepts
4. Architectural rules Garrison enforces in the sandbox integration
5. What Docker provides vs. what Garrison builds, with the M7 milestone split
6. Open questions later milestone specs must resolve
7. What each milestone retro must answer

**Precursor**: `docs/issues/agent-workspace-sandboxing.md` documents
the post-UUID-fix haiku run (2026-04-24) that surfaced the agent-
writes-outside-workspace failure mode and the three concerns it
exposed (sandbox escape, diary-vs-reality divergence, QA cross-check
gap). This threat model formalises the response.

**Empirical input**: `docs/research/m7-spike.md` §8 carries probe
observations on Docker primitives — cold-start, exec stream
buffering, network isolation, mount layering, capability drop. The
architectural rules below are what those primitives can structurally
enforce.

---

## 1. Assets

**What an agent can damage if its outbound boundary fails**:

- **Host filesystem**: any path outside the agent's workspace. Includes
  the supervisor's working tree, sibling agents' workspaces, the
  operator's home, system files, mounted volumes (vault state,
  Postgres data, MemPalace data, MinIO data).
- **Host network egress**: any network path the host can reach —
  inbound services, third-party APIs, the public internet, the
  customer's internal network if Garrison is deployed inside it.
- **Sibling agent state**: another agent's `/workspace/`, in-flight
  diary entries, pending finalize payloads.
- **Supervisor state**: the supervisor's pgxpool, listener
  conn, in-flight contexts. Particularly load-bearing because the
  supervisor is the trust anchor for vault, finalize, hygiene.
- **Sidecar state**: MemPalace database, MinIO objects, Infisical
  secret blobs. Sidecars run inside `garrison-net` and are reachable
  by name from any container that joins the network.
- **Docker control plane**: the docker socket / socket-proxy is the
  largest piece of host authority a misrouted agent can touch. Even
  read access leaks container topology; write access lets the agent
  spawn arbitrary containers with arbitrary mounts.

**Multi-tenancy posture**: single-tenant at M7 ship. Per-customer
sandboxing is anticipated post-M9 — the data model already carries
`companies.id` (M2.x) but the runtime doesn't yet partition agents
by customer.

---

## 2. Adversaries

Ranked by realistic probability of affecting a deployed Garrison
instance operationally.

1. **Prompt injection via ticket content, MCP tool responses, or
   palace recall.** A document the agent reads — a customer-submitted
   spec, a scraped webpage, a webhook payload, a previously-recalled
   diary entry from another agent — contains instructions that trick
   the agent into running tool calls outside its assigned scope (write
   files outside `/workspace`, hit attacker-controlled URLs, exfiltrate
   vault-injected env vars). **Most realistic adversary.** The
   M7 spike §8 P9 finding shows Claude has built-in injection
   detection, but it's not exhaustive — the sandbox is the structural
   floor.

2. **A "hired" agent backed by a malicious skill.** M7's hiring flow
   lets the operator install third-party skills from the public
   skills.sh feed and the SkillHub registry. A skill is arbitrary
   prompt text plus tool definitions — a maliciously-crafted skill
   can carry shell commands disguised as workflow text, calls to
   exfiltration endpoints, or instructions that read like operator
   policy. The operator's approve/reject judgment is the first
   barrier; the sandbox is the second. **Realistic post-M7 ship.**

3. **A correctly-following agent with a buggy spec.** Not malicious
   intent — a ticket whose acceptance criteria, taken literally,
   require the agent to do something host-affecting. e.g. "deploy this
   commit to production" parsed as a literal instruction by an agent
   that has shell access to the deploy host. The agent isn't
   adversarial; the operator's spec was. The sandbox limits blast
   radius regardless of whose fault it is.

4. **Claude itself going off-policy** (rarer but real). The 2026-04-24
   haiku run wrote `~/changes/...` outside its workspace tempdir
   because the model didn't `pwd && ls` to verify path. No injection,
   no malice — just default Bash tool behaviour against an
   un-isolated cwd.

**Adversaries we explicitly deprioritise**:

- **Host-level attackers with shell on the Garrison host.** If they
  have root, they can `docker exec` into any container, read every
  vault secret, dump Postgres. Application-layer sandbox does not
  defend; mitigation is host-level (SSH, firewall, audit). Same posture
  as `vault-threat-model.md` §2.
- **Container-escape exploits in the Docker runtime itself.** A Linux-
  kernel CVE or a Docker daemon CVE that lets a container break out
  of its namespaces. Garrison pins Docker versions and tracks CVE
  advisories; acute attacks against the runtime are out of reach.
- **Malicious supervisor code.** The supervisor IS the trust anchor;
  if its code is hostile, sandboxing is moot. Defense is code review
  and the locked-deps rule (`AGENTS.md` §"Stack and dependency rules").

---

## 3. Threats addressed vs. accepted

### Threats the sandbox explicitly addresses

1. **Agent writes outside its workspace.** Mitigated by `--read-only`
   root + `-v /var/lib/garrison/workspaces/<agent-id>:/workspace:rw` +
   `--tmpfs /tmp` mount layering. Verified empirically (`m7-spike.md`
   §8 P4): writes to `/etc` fail with EROFS; writes to `/workspace`
   and `/tmp` succeed; nothing the agent writes is visible to the
   host without an explicit bind-mount.

2. **Agent reaches host filesystem.** Mitigated by the same RO root +
   bind-mount-only-`/workspace` policy. The agent has no view of
   `~/`, `/var/lib/garrison/workspaces/<other-agent-id>/`, or any
   sidecar volume. Bind-mounts are explicit, audited, per-agent.

3. **Agent makes arbitrary network egress.** Mitigated by
   `--network=none` as the default, plus joining a per-agent custom
   Docker network that contains only the sidecars the agent needs.
   `m7-spike.md` §8 P3 confirms the host-side default-bridge route
   isn't reachable when `--network=none` is set, and that custom-
   network reach is name-resolved (no IP escape).

4. **Agent reaches sibling agents' state.** Mitigated by per-agent
   network (each agent gets its own `garrison-agent-<id>-net` rather
   than a shared `garrison-net`). Sibling-reach requires explicit
   network-create + container-attach; the supervisor doesn't grant it.

5. **Agent escalates from non-root inside container.** Mitigated by
   `--user <agent-uid> --cap-drop=ALL`. `m7-spike.md` §8 P6 verified
   `mount` fails with EPERM under that posture; same applies to
   raw-socket binds, kernel-module loads, time-set syscalls.

6. **Agent exhausts host resources.** Mitigated by `--memory`,
   `--cpus`, `--pids-limit` cgroup caps. Verified at `m7-spike.md`
   §8 P5 — caps materialise as `memory.max`, `cpu.max`, `pids.max`
   cgroup v2 entries. A runaway agent is bounded.

7. **Agent reaches the Docker control plane.** Mitigated by the
   socket-proxy gate (M2.2 deployment pattern, extended in M7 with
   the `POST /containers/create` allow-list). The agent's container
   does NOT mount the docker socket; only the supervisor does.

8. **Diary-vs-reality path divergence.** Mitigated by the agent's
   workspace being the only writable filesystem location whose
   contents persist past the spawn — every artifact path the
   agent claims in finalize resolves to a single physical location
   (`/var/lib/garrison/workspaces/<agent-id>/<artifact>`) that the
   supervisor can stat-verify post-finalize. This closes the
   `docs/issues/agent-workspace-sandboxing.md` §"Three distinct
   concerns" §2.

### Threats explicitly accepted

1. **Host compromise.** If the Garrison host is rooted, every
   container is compromised. Application-layer sandbox does not
   defend; mitigation is host-level.

2. **Docker runtime CVE.** Container-escape via a daemon vulnerability
   defeats namespace isolation. Garrison pins Docker version, tracks
   CVE advisories, schedules upgrades. An acute zero-day is out of
   reach.

3. **Sidecar compromise.** If MemPalace, MinIO, or Infisical sidecars
   are compromised, the data they hold is at risk. Mitigation is per-
   sidecar (the vault threat model covers Infisical; MemPalace and
   MinIO have their own deployment hardening).

4. **Operator running `docker exec` ad-hoc into an agent container.**
   The operator can always shell into any agent container directly
   from the host; the sandbox doesn't gate operator authority.
   Documented expectation, not a defended posture.

5. **Skill supply-chain compromise** (skills.sh / SkillHub). A
   malicious skill installed via the M7 hiring flow can issue tool
   calls within the sandbox boundary. The boundary holds; the agent
   still can't exfiltrate vault env vars or write to host. But the
   skill can absolutely poison diary content, KG triples, or finalize
   payloads. The hiring threat model
   (`docs/security/hiring-threat-model.md`) covers the operator-side
   approval process; once a skill is approved + installed, this
   threat model treats it as in-scope agent code.

6. **Side-channel exfiltration.** A patient agent could exfiltrate
   data via timing channels, error-message patterns, MemPalace
   diary entries that an attacker has read access to, etc. Mitigation
   is leak-scan (M2.3 vault rule 1 carryover) plus hygiene predicates
   (M6 thin-diary / missing-kg-facts) that surface the agent
   behaving anomalously. No tight bound; treated as residual risk.

---

## 4. Architectural rules (binding for M7 and beyond)

These rules are binding. The M7 spec may not contradict them. If a
future retro surfaces a reason to revisit a rule, the rule is amended
here before the spec changes.

### Rule 1: Every agent runs in its own persistent container

One container per `agents` row. Container is created on agent
activation (post-approval in M7's hiring flow; for the M2.x-seeded
engineer + qa-engineer rows, on the M7 migration that brings them
under the sandbox). Container is destroyed on agent deactivation. Per-
spawn invocation uses `docker exec` against the persistent container
(see `m7-spike.md` §8 P1: 40ms vs 400ms cold-start gap).

The container's lifecycle thus ≠ the spawn's lifecycle. The supervisor
must track container state separately from agent_instances state.

### Rule 2: Layered filesystem, RW only the workspace

Every agent container MUST be created with:

- `--read-only` — root filesystem is read-only.
- `--tmpfs /tmp:rw,size=64m` — ephemeral scratch, doesn't survive container exit.
- `--tmpfs /var/run` — runtime sockets (claude / mcp / pgmcp).
- `-v /var/lib/garrison/workspaces/<agent-id>:/workspace:rw` — the
  ONLY persistent writable location.
- `-v /var/lib/garrison/skills/<agent-id>:/workspace/.claude/skills:ro` —
  installed skills (per M7 §4 scope-merge).

No other bind-mounts. No `:rw` on any path other than `/workspace` and
the tmpfs scratch dirs.

### Rule 3: Default-deny network, opt-in sidecar reach

Every agent container MUST be created with `--network=none`, then
attached (via `docker network connect`) to a per-agent custom network
that contains only the sidecars that agent needs (default set:
mempalace + Postgres-via-pgmcp + Infisical for vault-injection).

No agent shares a network with another agent. Sibling agents are
unreachable. A second `docker network connect` to a shared egress
network is the explicit shape for "agent needs internet" (e.g. a hired
agent whose skill genuinely needs HTTP egress) — opt-in per agent,
audited at hire time.

### Rule 4: Drop all capabilities, run as non-root

Every agent container MUST be created with `--user <agent-uid>
--cap-drop=ALL`. UIDs are allocated from a per-customer range
(1000–1999 customer A, 2000–2999 customer B, etc.) so a host-side
`ls -la` stat unambiguously identifies the owning agent.

`m7-spike.md` §8 P6 confirms this posture blocks `mount`, raw-socket
binds, and kernel module loads. No `--cap-add` exceptions without a
threat-model amendment.

### Rule 5: Resource caps are mandatory, not optional

Every agent container MUST be created with `--memory=<cap>
--cpus=<cap> --pids-limit=<cap>`. Defaults: 512 MB / 1.0 CPU / 200 PIDs
per agent. Per-role overrides land in `agents.runtime_caps JSONB` (M7
schema addition).

A runaway agent is bounded by cgroup, not by trust.

### Rule 6: Docker socket access is supervisor-only

The agent container does NOT mount the docker socket. The supervisor
mediates all Docker control-plane operations through the M2.2
socket-proxy. M7 extends the proxy's allow-list with `POST
/containers/create` (one-time threat-model amendment landed alongside
this rule); the proxy's deny-by-default posture for everything else
holds.

The agent has no `docker` binary in its container. If a skill needs
to spawn child processes, it does so via Bash within the same
sandbox (which inherits all rules 1–5).

### Rule 7: Workspace is ephemeral except for finalize-claimed artefacts

`/var/lib/garrison/workspaces/<agent-id>/` is wiped between agent
deactivation cycles. Anything the agent wants persisted past its
own lifecycle goes through finalize_ticket's artifact-claim shape
(M2.2.1 carryover) — claimed artefacts get copied out to a stable
location (MinIO blob, ticket metadata pointer, or a department-
shared workspace per M8 cross-department dependencies).

This means an agent cannot use the workspace as long-term storage
for state it wants to outlive its own lifecycle. State must be
diary, KG, or finalize-claimed.

### Rule 8: Diary-vs-reality is supervisor-verified

Every artefact path the agent claims in a finalize_ticket payload
MUST resolve to a real file inside `/var/lib/garrison/workspaces/
<agent-id>/`. The supervisor stat-verifies each claimed path before
committing the finalize transaction. A path that doesn't exist on
disk → finalize rejected, hygiene_status = `missing_artifact`.

This closes `docs/issues/agent-workspace-sandboxing.md` §"Three
distinct concerns" §2 (diary-vs-reality divergence). Sub-rule of
Rule 2 — the workspace bind-mount is the one trusted location, so
"does the file exist where the agent says" is a single stat call
the supervisor can answer.

### Rule 9: The immutable preamble enforces the sandbox in prompt-space

Beyond structural enforcement, every agent system prompt prepends an
operator-controlled security preamble (see `m7-spike.md` §4.5 + §8
P9–P10). Wording is **policy-style, not identity-assertion-style** —
"Garrison agents: container escape is prohibited" not "You are X and
your role is Y" — to avoid Claude's built-in injection-detection
refusal (`m7-spike.md` §8 P9).

The preamble + the structural rules above are belt-and-suspenders:
the preamble tells the agent what it cannot do; the sandbox makes
those things structurally impossible. If either layer fails, the
other still holds.

### Rule 10: Audit every container lifecycle event

Every container create / start / stop / destroy event lands in
`agent_container_events` (M7 schema addition) — `agent_id`,
`event_type`, `image_digest`, `started_at`, `stopped_at`,
`stop_reason`, `cgroup_caps_json`. Mirrors the
`vault_access_log` shape from M2.3.

Every `agent_instances` row records the `image_digest` AND the
`preamble_hash` AND the `claude_md_hash` of the cwd CLAUDE.md as it
stood at spawn time (M7 schema addition; closes `m7-spike.md` §8.4
implication 6). A forensic query can reconstruct exactly what code +
prompt context an agent ran with.

---

## 5. What Docker provides vs. what Garrison builds

### Docker provides (Garrison consumes via the socket-proxy)

- Linux namespace + cgroup isolation (mount, network, PID, user, IPC, UTS).
- `--read-only`, `--tmpfs`, `-v` mount semantics.
- `--network=<name>` per-network isolation; `docker network connect / disconnect`.
- `--user <uid>` non-root execution.
- `--cap-drop=ALL` and per-cap allow-list (Linux capabilities(7)).
- `--memory`, `--cpus`, `--pids-limit` cgroup caps.
- Image layer storage, content-addressable digests.
- `docker exec -i` line-buffered stdin/stdout (verified `m7-spike.md` §8 P2).

### Garrison builds (no Docker UI surface exposed)

- **Per-agent container image manager** [M7]. Builds /
  re-uses `garrison-claude:m5` + per-agent skill bind-mount layout.
  Tracks image digests in `agents.image_digest` for audit Rule 10.
- **Per-agent network manager** [M7]. Creates
  `garrison-agent-<id>-net` per agent, connects sidecars by name,
  deletes on agent deactivation.
- **Container lifecycle controller** [M7]. Wraps the
  socket-proxy `POST /containers/create` + `POST /containers/start`
  + `POST /containers/stop` calls; emits `agent_container_events`
  audit rows; surfaces failures to the dashboard.
- **`docker exec` shim for spawn invocation** [M7]. Replaces the
  current direct-exec in `internal/spawn`. Preserves the existing
  stdin/stdout pipeline (verified compatible with NDJSON streaming
  per `m7-spike.md` §8 P2).
- **Workspace layout enforcer** [M7]. Ensures every container
  is created with the Rule 2 mount layout; rejects spawn requests
  that would create a container with a non-conformant mount set.
- **Diary-vs-reality stat verifier** [M7]. Hooks into the
  finalize_ticket commit path; stats every claimed artefact;
  rejects the finalize on path mismatch.
- **Preamble const + composer** [M7]. The Go const
  carrying the operator preamble; the `ComposeSystemPrompt`
  extension that prepends it above `agent_md`. See `m7-spike.md`
  §4.5 for shape.
- **Container migration for M2.x-seeded agents** [M7]. The
  `engineer` and `qa-engineer` rows seeded by M2.x migrations
  predate the sandbox; M7's ship migrates them to the container
  runtime, dropping the direct-exec path entirely.

### Deployment shape

- Garrison's supervisor + M2.2 socket-proxy + per-agent containers
  + M2.2 mempalace + M2.3 Infisical + M5.4 MinIO compose into one
  Docker Compose stack on `garrison-net`. M7 introduces the
  per-agent networks **on top of** `garrison-net`, not instead of.
- Each agent container's network membership: per-agent network +
  selective sidecar attach. Default attach set: mempalace +
  Infisical sidecars. Postgres reach goes through pgmcp (which
  itself is on `garrison-net`); the agent's pgmcp socket is a
  per-container UDS bind-mount, not a shared network resource.

---

## 6. Open questions the M7 context spec must resolve

1. **Image-build vs bind-mount for skills.** The spike's lean is
   single base image + per-agent bind-mount of
   `/var/lib/garrison/skills/<agent-id>/` into
   `/workspace/.claude/skills`. The alternative (per-agent custom
   image with skills baked in) is more immutable but pays
   image-build latency on every `bump_skill_version`. Plan-level
   decision; both are compatible with this threat model.

2. **UID allocation strategy.** Per-customer UID ranges (1000–1999,
   2000–2999, ...) are the lean. Open: how does M9 multi-tenant
   isolation extend this? Per-agent globally-unique UIDs avoid
   the per-customer range exhaustion concern but lose the host-
   side audit-via-`ls` clarity.

3. **Per-agent network creation cost.** Every agent gets its own
   `garrison-agent-<id>-net`. At N=100 agents that's 100 Docker
   networks. Docker's network limits (default: ~31 user-defined
   bridge networks per host without overlay) are tight — the M7
   plan should empirically probe whether the bridge driver scales
   or whether overlay / macvlan / a per-agent network namespace
   under a single bridge is needed.

4. **Workspace persistence cycle.** Rule 7 says the workspace is
   wiped between agent deactivation cycles. Open: what's the
   trigger for "agent deactivation"? Operator-initiated only, or
   also automatic on long-idle? M9 (scheduled wake-ups) makes
   this question more pointed — a scheduled-only agent might
   be "deactivated" between wakeups, which would wipe state the
   schedule expects to find.

5. **Skill RO-mount tampering window.** The skill bind-mount is RO
   from the agent's perspective, but the supervisor writes to
   `/var/lib/garrison/skills/<agent-id>/` on host. A racing
   `bump_skill_version` while the agent is mid-spawn could swap
   the skill content under the agent's feet. M7 plan should pin
   whether skill updates are atomic (write-to-tmp + rename) and
   whether a running agent gets the new version on next spawn or
   only after container restart.

6. **`POST /containers/create` allow-list precision.** The
   socket-proxy currently allows `POST/EXEC/CONTAINERS`. M7
   adds `POST /containers/create`, which substantially broadens
   the supervisor's authority — the proxy must filter on
   request-body fields (`Image`, `HostConfig.Mounts`,
   `HostConfig.NetworkMode`, `HostConfig.CapAdd`, etc.) so a
   compromised supervisor process cannot spawn an arbitrary
   container with arbitrary mounts. Plan-level rule additions to
   the proxy config required.

7. **Egress network for hired agents that need it.** Rule 3 says
   "opt-in egress per agent." Open: what's the dashboard surface
   for granting it? Per-agent egress feels like the same shape as
   per-agent skills (proposal → approve → install); the M7 plan
   should decide whether egress grants ride on the same
   `propose_hire` / `approve_hire` flow or get a separate verb.

8. **Exit-on-runaway behaviour.** When `--memory=512m` is
   exceeded, the kernel OOM-kills the agent. The supervisor sees
   the container exit; what hygiene_status does it record? Is an
   OOM exit treated like a `claude_error` exit (M2.2.2 carryover)
   or a new `runaway` category? Plan-level decision; affects
   alerting + retry policy.

9. **Sandbox parity for the chat runtime.** M5.1 already runs
   the chat-CEO turns containerised. Should those containers
   adopt all 10 rules above at M7 ship, or stay on the looser
   M5.1 posture? Lean: full parity — the chat threat model
   (`chat-threat-model.md`) targets a different boundary
   (operator-as-input) but the outbound-blast-radius concerns
   are identical.

10. **Image digest pinning for `garrison-claude:m5`.** The base
    image is currently tag-referenced (`m5`). M7 should pin to
    digest in the agent-create path so a base-image rebuild
    doesn't silently flip the runtime under existing agents.
    Affects audit Rule 10 (the digest recorded must be stable).

---

## 7. What each milestone retro must answer

### What the M7 retro must answer

- **Rule-set hold**: did Rules 1–10 hold across the M7 implementation?
  Any rule needing a pre-ship amendment because the spec couldn't
  satisfy it cleanly?
- **Cold-start budget**: did the per-agent persistent-container
  pattern sit at the spike-projected ~40ms exec latency? Or did
  real claude-code-in-container startup add overhead that pushed
  it past the operator-noticeable threshold?
- **Network scaling**: how many agent-networks did M7 ship with?
  Did the bridge driver hold, or did the plan shift to overlay /
  alternative isolation?
- **`POST /containers/create` proxy filter**: which body-field
  filters landed in the socket-proxy config? Were any deferred
  to M7.1 polish?
- **Diary-vs-reality verifier hits**: did Rule 8's stat-verify reject
  any finalize payloads in the M7 acceptance walk? If yes, were they
  agent bugs or supervisor bugs?
- **Preamble injection-detection collisions**: did the operator
  preamble draft (M7 plan §4.5) trip Claude's built-in injection
  filter at any point? Wording adjustments?
- **Existing-agent migration**: did the M2.x-seeded engineer +
  qa-engineer roles migrate to the container runtime cleanly, or
  was a transitional direct-exec path needed?

### What the M8 retro must answer (sandbox carry-over)

M8 introduces agent-spawned tickets + cross-department dependencies
+ MCP-server registry. Each touches the sandbox.

- **MCP-server registry sandbox parity**: a registered MCP server
  becomes a dep of an agent. Does the registry enforce the same
  Rules 1–10 on registered servers, or are servers a separate
  trust class?
- **Cross-department workspace reach**: does an agent in dept A
  ever need to read dept B's workspace? If yes, what's the audit
  shape? Lean: no direct reach; cross-dept handoff goes through
  finalize_ticket-claimed artefacts (Rule 7 + Rule 8 already
  covers this).

### What later retros must answer (open-ended)

- Multi-tenant per-customer sandbox isolation (M9 or beyond).
- Egress-grant audit shape — once enough hired agents have been
  granted egress, is the per-agent grant pattern still the right
  one, or does a per-customer egress proxy take over?
- Skill supply-chain compromise post-mortem if any landing skill
  is later determined to be malicious — how did the sandbox
  bound the blast radius vs. how the operator review missed it?

---

## Cross-references

- `docs/security/vault-threat-model.md` — credential-injection axis;
  this document treats vault-injected env vars as in-scope agent
  context (Rule 1 holds: secrets stay in env, never in prompt).
- `docs/security/chat-threat-model.md` — operator-vs-agent input
  axis; that document covers prompt content, this one covers tool
  output.
- `docs/security/hiring-threat-model.md` — operator → proposed agent
  → installed agent privilege boundary. Hands off to this document
  at the point a hired agent is activated.
- `docs/issues/agent-workspace-sandboxing.md` — the precursor issue
  doc that surfaced the failure mode this threat model addresses.
- `docs/research/m7-spike.md` §4 + §4.5 + §8 — empirical input.
