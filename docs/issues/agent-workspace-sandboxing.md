# Agent workspace sandboxing — standalone issue

**Status**: **scope-merged with M7 (2026-05-02)**. The Docker-per-agent
resolution lands as part of M7's hiring-flow + per-agent custom-skills
work — the container is the natural skill-install boundary, so resolving
the runtime + the install actuator in the same milestone gives one
coherent ship instead of two halves wired together later. See
`docs/research/m7-spike.md` §4 for the merged scope.
**Surfaced**: 2026-04-24, post-UUID-fix haiku validation run.
**Scope**: separate from M2.3 vault threat model; see "Relationship to vault" below.

## The finding

During the post-UUID-fix haiku validation run (ticket `28cff177-ab5e-4df9-aadd-585fb230d0d6`, `experiment-results/post-uuid-fix-haiku-run.md`), `claude-haiku-4-5-20251001` wrote the ticket's changelog file to an absolute path outside the Go-test workspace tempdir:

- **Claimed path** (in `finalize_ticket` diary artifacts): `changes/hello-28cff177-ab5e-4df9-aadd-585fb230d0d6.md`
- **Actual write location**: `~/changes/hello-28cff177-ab5e-4df9-aadd-585fb230d0d6.md`

The parent directory `~/changes/` was also created by the Write tool call. Both have been cleaned up post-run.

## Root cause

The supervisor spawns `claude` as a subprocess inheriting the operator's shell cwd. The Go test sets a workspace tempdir and passes it as an environment variable, but doesn't `chdir` the supervisor subprocess into it. The agent's `pwd` therefore resolves to `~` (the real home of the user running the test).

Two separate agent behaviors interact with this:

1. **Opus** ran `pwd && ls -la changes/` as its first Bash call, discovered the absolute path, then `mkdir -p <workspace>/001/changes` before writing with the workspace-relative path. Opus avoided the trap by checking first.

2. **Haiku** skipped the cwd check and wrote to an absolute path matching what `pwd` would have returned. The filename was correct (referenced the ticket ID); the location was wrong.

This is not a finalize-schema problem, a prompt problem, or a pgmcp problem. It is a workspace-isolation problem in the supervisor's subprocess spawn machinery.

## Three distinct concerns this surfaces

**1. Sandbox escape**: an agent can write to arbitrary paths on the host filesystem. In production with real Hey Anton customer data or real repo credentials, this is a security issue. In development it is a hygiene issue (tests leak files into operator home directories).

**2. Diary-vs-reality inconsistency**: the engineer's finalize payload claimed `changes/hello-<ticket_id>.md` as the artifact path, but disk truth was `~/changes/hello-<ticket_id>.md`. The atomic-write commit went through because the supervisor doesn't verify artifact paths against actual filesystem state. Memory layer will therefore record a wrong path.

**3. QA cross-check gap**: the QA-engineer's Read tool call used the absolute path, not the path claimed in the engineer's diary. QA saw a file matching the filename, approved it, and finalized clean. The "is the artifact where the diary says it is" check is not part of QA's protocol. Could be a QA agent.md improvement or a supervisor-side assertion.

## Relationship to M2.3 vault threat model

**The vault threat model** (`docs/security/vault-threat-model.md`) is about credential injection: env vars only, supervisor-mediated, agent never calls a vault MCP tool directly. It does not address workspace isolation because the vault assumes the supervisor has control of what the agent can see.

**Workspace sandboxing is orthogonal**. An agent with no vault-injected secrets can still write outside its workspace. An agent with vault-injected secrets can write those secrets to arbitrary host paths if not sandboxed. The two threat models compose: vault + sandbox gives you "agent has credentials, agent cannot exfiltrate them"; neither alone is sufficient for production.

The M2.3 milestone should land the vault without taking on sandboxing scope. M2.3's acceptance tests should explicitly note that workspace isolation is NOT tested by M2.3 and is a separate concern.

## Planned resolution: Docker-per-agent (in M7)

**Originally** scoped as a post-M2.3 standalone work item; **2026-05-02
moved into M7** because M7 introduces per-agent custom skills and the
container is the natural skill-install boundary. The supervisor spawns a
container rather than a direct `claude` subprocess; the container has:

- Its own rootfs or bind-mounted workspace tempdir as `/workspace` (cwd)
- A bind-mount of `/var/lib/garrison/skills/<agent-id>/` into
  `/workspace/.claude/skills` for per-agent installed skills (see
  `docs/research/m7-spike.md` §4)
- Host filesystem hidden
- Network policy controlling what external APIs the agent can reach
- Env vars for vault-injected secrets (the vault injects into the container, not the host process)

This solves all three concerns above:

- Sandbox escape: container's filesystem is isolated; writes outside `/workspace` don't touch the host
- Diary-vs-reality consistency: paths in the container are unambiguous (the only `changes/` that exists is the workspace's)
- QA cross-check: less load-bearing when paths can't diverge, but the QA agent.md can still be tightened

The base image is M5.1's `garrison-claude:m5` (already proven for the chat
runtime) extended with the agent's installed skills. Adding `POST
/containers/create` to the docker-socket-proxy allow-list is a one-time
threat-model amendment that lands with M7 — see M7 spike §4 Q6.

## Interim mitigations (until M7 ships)

The original three options below stand for any compliance experiments
between now and M7. M2.3 → M5.4 all shipped on the original direct-exec
runtime; Option A (`chdir` into workspace tempdir) was never wired in but
remains the cheapest stop-gap if you need it before M7.

- **Option A**: `chdir` the supervisor subprocess into the workspace tempdir before spawning claude. One-line change in `internal/spawn` (probably `spawn.go`'s `exec.Cmd.Dir` field). Doesn't prevent absolute-path writes but eliminates the "fallback cwd is operator home" problem.
- **Option B**: Run experiments as a different OS user with no meaningful home directory (e.g. a `garrison-test` user). Quick to set up, doesn't solve the fundamental problem but contains blast radius.
- **Option C**: Accept the gap for now; document that haiku may write files to the operator's home directory during experiments and clean up as a post-run step.

Operator's call. Option A is probably the cheapest with the highest mitigation-to-effort ratio.

## Acceptance criteria for "resolved"

This issue is resolved when:

- Agents spawned by the supervisor cannot write to host filesystem outside a designated workspace
- A test that verifies an agent attempting to write to `/tmp/unexpected-path` is either blocked or isolated to the container's filesystem
- The vault (M2.3) injects secrets into the agent container, not into a shared host env
- Diary-vs-reality path consistency is verified by supervisor (artifact paths in finalize payload must resolve to real files in the agent's workspace)

## Related files

- `experiment-results/post-uuid-fix-haiku-run.md` — the run that surfaced this
- `docs/security/vault-threat-model.md` — orthogonal, separate concern
- `internal/spawn/` — where the cwd fix (interim mitigation Option A) would land
- Future `internal/containerize/` or similar — where Docker-per-agent would live