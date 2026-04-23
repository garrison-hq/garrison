# M2.2 pre-implementation validation spike — research notes

**Status**: T001 validation spike, complete.
**Date**: 2026-04-23.
**Environment**: Fedora 43 host, Docker 29.3.0, MemPalace 3.3.2, Claude Code 2.1.117, Python 3.11 (`python:3.11-slim`), `linuxserver/socket-proxy:latest`.
**Scratch dir**: `~/scratch/m2-2-topology-spike/` (throwaway; not committed; retained until M2.2 ships as evidence).
**Binding**: this file is downstream of [plan.md](./plan.md) §"Pre-implementation validation spike (T001)" and is the authoritative record of the three claims T001 gates on, plus the six implementation-affecting findings the spike surfaced.

---

## Three claims — outcome

### Claim 1 — MCP init reports `mempalace.status="connected"` with 29 tools

**Verdict: PASS.**

MCP config used:

```json
{
  "mcpServers": {
    "mempalace": {
      "command": "docker",
      "args": ["exec", "-i", "spike-mempalace",
               "python", "-m", "mempalace.mcp_server", "--palace", "/palace"]
    }
  }
}
```

Invocation: `claude -p "say hi" --output-format stream-json --verbose --strict-mcp-config --mcp-config ./mcp-config.json --no-session-persistence --max-budget-usd 0.02 --model claude-haiku-4-5-20251001`.

**Init event's `mcp_servers` block (verbatim):**

```json
[{"name":"mempalace","status":"connected"}]
```

**Tool count**: 29 tools with `mcp__mempalace__` prefix. First/last samples:
- First: `mcp__mempalace__mempalace_add_drawer`, `mcp__mempalace__mempalace_check_duplicate`, `mcp__mempalace__mempalace_create_tunnel`
- Last: `mcp__mempalace__mempalace_status`, `mcp__mempalace__mempalace_traverse`, `mcp__mempalace__mempalace_update_drawer`

Claude exit code 0. Full init event saved at `~/scratch/m2-2-topology-spike/runs/claim1.ndjson`.

### Claim 2 — kill-chain tears down cleanly on SIGTERM to the parent pgid

**Verdict: PASS.**

Test: `setsid`-spawned `docker exec -i spike-mempalace python -m mempalace.mcp_server --palace /palace` with a pipe held open by the parent so the in-container Python's `sys.stdin.readline()` blocks. Verified via `/proc`-based process listing inside the sidecar (see finding F6 — `python:3.11-slim` has no `ps`/`pgrep`).

**Before kill**: 1 mempalace.mcp_server Python process visible in the sidecar (pid 264 inside the container).

**After `os.killpg(pgid, SIGTERM)`**:
- Parent exited with code 0 in **3 ms** wall-clock.
- Sidecar process table post-kill: **0 mempalace/python processes** (clean teardown via stdin EOF → readline loop exit).
- Container `spike-mempalace` status: **Up** (container not killed, as expected).

3 ms is astonishingly fast — well inside the 2 000 ms NFR-206 / MCPBailSignalGrace ceiling. Plenty of headroom.

### Claim 3 — wake-up p95 < 1.5 s

**Verdict: PASS.**

Invocation: `docker exec spike-mempalace mempalace --palace /palace wake-up --wing wing_test_newwing`.

10 runs, no warm-up discarded:

| Run | ms |
|-----|-----|
| 1 | 1002 |
| 2 | 994 |
| 3 | 896 |
| 4 | 928 |
| 5 | 771 |
| 6 | 804 |
| 7 | 1031 |
| 8 | 1008 |
| 9 | 815 |
| 10 | 925 |

**p50 = 925 ms, p95 = 1031 ms, p99 = 1031 ms (n=10), min = 771, max = 1031.**

All 10 runs under the 1500 ms threshold. Headroom to the NFR-202 2-second ceiling: ~1 second. The latency is dominated by MemPalace's Python + ChromaDB import cost (~700-800 ms) plus `docker exec` overhead (~200 ms). Not expected to grow materially with palace size — wake-up queries only L0 + L1 summary tokens (~79 tokens observed in this sample).

---

## Claim d — lazy wing creation (Clarify F)

**Verdict: PASS (confirmed).**

`mempalace_add_drawer(wing="wing_test_newwing", room="hall_events", content="...")` against a palace with no pre-existing `wing_test_newwing` succeeded immediately. Response payload:

```json
{
  "success": true,
  "drawer_id": "drawer_wing_test_newwing_hall_events_1080a9f4a42a53f58de401f7",
  "wing": "wing_test_newwing",
  "room": "hall_events"
}
```

Spike §3.7's invariant holds in 3.3.2 for the specific wings M2.2 uses. **Clarify F is closed.** No explicit wing-create bootstrap step needed in the supervisor.

---

## Proxy filter — verified end-to-end

Side-check (not in the original T001 claim set but necessary given finding F5):

Docker client running in a sibling container on the same compose network, with `DOCKER_HOST=tcp://spike-docker-proxy:2375`:

| Request | Outcome |
|---------|---------|
| `docker exec spike-mempalace mempalace --version` | **ALLOWED** → returns "MemPalace 3.3.2" |
| `docker volume ls` | **403 Forbidden** (VOLUMES not in allowlist) |
| `docker pull alpine:3.18` | **403 Forbidden** (IMAGES not in allowlist) |

Proxy env: `POST=1 EXEC=1 CONTAINERS=1`. Filter behaves as expected.

---

## Wheel SHA256 digest (Claim e)

For `mempalace==3.3.2` (linux, py3-none-any, from PyPI):

```
--hash=sha256:cb288e8028d26dfb384125baecfcf584aa2ba5a30a216ff82d9745af070d5e45
```

Verified via `pip download --no-deps mempalace==3.3.2` + `pip hash`. Committed to `~/scratch/m2-2-topology-spike/wheels/mempalace-3.3.2-py3-none-any.whl` for reference.

---

## Six spike findings that affect plan/tasks

These surfaced during T001 and block correctness of tasks that follow T001 as currently written. Each is ordered by how early it affects downstream work.

### F1 — The `chroma.sqlite3` marker assumption is wrong

**Affects**: spec Clarifications Session 2026-04-22 Q4 (palace bootstrap strategy), FR-201 (b)/(c), plan §"Palace bootstrap" state machine, T006's `Bootstrap` implementation, this task's own §"Claim 1 setup".

**Observation**: `mempalace init --yes /palace` produces **only `mempalace.yaml`** — not `chroma.sqlite3`. `chroma.sqlite3` + `knowledge_graph.sqlite3` appear only AFTER the first write (via `mempalace_add_drawer` or `mempalace mine`) triggers ChromaDB initialization.

Post-init `/palace/` contents: `mempalace.yaml` only.
Post-first-`add_drawer` `/palace/` contents: `mempalace.yaml`, `chroma.sqlite3`, `knowledge_graph.sqlite3`, `<uuid-dir>/` (HNSW index).

**Additional finding**: `mempalace init --yes` is **idempotent on its own** — running it a second time against an initialized palace exits 0 and re-writes the identical `mempalace.yaml`. The clarify-session Q4 worry about non-idempotency is unfounded for 3.3.2.

**Remediation options**:
- (a) Change the marker from `chroma.sqlite3` to `mempalace.yaml`. Detect-first logic still works; marker is now accurate.
- (b) Drop detect-first entirely — always run `mempalace init --yes` at startup. Simpler; leans on the idempotency observation above.

Operator decision required before T003/T006. My recommendation: option (b) — unconditional `init --yes` at startup is simpler and idempotent; log the returned `exit=0` at info and move on.

### F2 — `mempalace wake-up` does NOT accept `--palace` or `--max-tokens`

**Affects**: spec FR-207 (invocation form), NFR-209 (`--max-tokens 200` budget), plan §"Wake-up" call shape, T006's `Wakeup` signature, the entire 2-second NFR-202 basis.

**Observation**: `mempalace wake-up --help` in 3.3.2 shows:

```
usage: mempalace wake-up [-h] [--wing WING]
options:
  --wing WING  Wake-up for a specific project/wing
```

`--palace` must be passed at the TOP level: `mempalace --palace /palace wake-up --wing <wing>`. There is NO `--max-tokens` flag. Wake-up output size is not operator-controlled; MemPalace internally emits L0 + L1 summary (~79 tokens observed for a 1-drawer wing; spike §3.1 saw "~170 tokens for typical wing content").

**Remediation**:
- Change `Wakeup`'s call shape: `docker exec spike-mempalace mempalace --palace /palace wake-up --wing <wing>`.
- Remove `MaxTokens` parameter from `WakeupConfig`; drop NFR-209 (or re-scope it to "output is implicitly bounded by MemPalace's L0+L1 renderer; supervisor accepts stdout verbatim").
- Wake-up stdout has a preamble line ("Wake-up text (~N tokens):") + separator + L0/L1 body. For M2.2 the supervisor injects the full stdout verbatim into `--system-prompt` per FR-207a's template; the preamble is informational and harmless in a system prompt. Future optimisation (M6?) could strip it.

### F3 — `Dockerfile.mempalace` cannot use `alpine` base

**Affects**: plan §"`supervisor/Dockerfile.mempalace` (new)" (uses `python:3.11-alpine`), T002's Dockerfile target, tasks.md file listing that references `python3.11-alpine` implicitly.

**Observation**: `pip install mempalace==3.3.2` inside `python:3.11-alpine` fails because chromadb's transitive deps (tokenizers, pydantic-core, others) have no musl wheels on PyPI — pip falls back to source build, which needs Rust toolchain that alpine doesn't have by default.

Switching base to `python:3.11-slim` (debian-glibc): install succeeds directly from prebuilt wheels. Image size: **478 MB** (vs. M2.1 supervisor's 264 MB baseline).

**Remediation**: plan/tasks update `Dockerfile.mempalace` base to `python:3.11-slim`. Final image size isn't a hard constraint per NFR-213; note the ~478 MB in the M2.2 retro for comparison.

### F4 — `pip --require-hashes` requires ALL transitive deps pinned

**Affects**: plan §"`Dockerfile.mempalace` (new)" (uses `pip install --no-cache-dir --require-hashes mempalace==3.3.2 --hash=sha256:...`), T002 discipline intent, NFR-207 (version pin with SHA256 verification).

**Observation**: `pip install --require-hashes` refuses to install mempalace because chromadb's version range is `<2,>=1.5.4` (unpinned). To install with hashes, ALL transitive deps must be pinned with `==` and each needs its own `--hash=sha256:...`. MemPalace 3.3.2's declared dep set: `chromadb<2,>=1.5.4`, `pyyaml>=6.0,<7`. Recursively these have their own unpinned transitives.

**Remediation options**:
- (a) Generate a full lockfile via `pip-compile` (pip-tools) → single-file, all transitives pinned + hashed. Produces a ~40-line requirements file. Maximum rigor.
- (b) Pin only mempalace itself via post-install hash check — `pip install mempalace==3.3.2` without `--require-hashes`, then verify the installed wheel's SHA256 manually in a RUN step. Soft rigor; mempalace is hash-verified, transitives are trusted.
- (c) Skip hash verification entirely — rely on PyPI's own TLS / wheel checksums.

**Recommendation**: option (a) for M2.2 — generate a locked requirements file via `pip-compile` (committed alongside `Dockerfile.mempalace`). Every upgrade re-runs pip-compile. This matches M2.1's GPG+SHA256 rigor for the Claude binary.

### F5 — `linuxserver/socket-proxy` uses TCP :2375, not a unix socket

**Affects**: plan §"Deployment topology" (`DOCKER_HOST=unix:///var/run/docker-proxy.sock`), plan §"`supervisor/docker-compose.yml` (new)" (named volume `docker-proxy-sock`), T002's compose file shape, T009's `GARRISON_DOCKER_HOST` default, T011's mcpconfig `env` block, T015's `DOCKER_HOST` propagation.

**Observation**: `linuxserver/socket-proxy:latest` is a HAProxy-fronted HTTP proxy listening on **TCP port 2375** inside the container. It does NOT expose a unix-domain socket. The `/var/run/docker.sock` file that appears inside the proxy container is a misread of the state — setting up a named-volume `/var/run` mount doesn't produce a proxy unix socket; it just creates an empty shared dir.

Inside the proxy container: `netstat -tln` shows `:::2375 LISTEN haproxy`.

**Remediation**:
- Compose: proxy exposes port 2375 on the docker network; supervisor reaches it via service-name DNS.
- Supervisor env: `DOCKER_HOST=tcp://garrison-docker-proxy:2375`.
- MCP config's mempalace entry's `env`: `{"DOCKER_HOST": "tcp://garrison-docker-proxy:2375"}`.
- No named volume for the proxy socket — remove `docker-proxy-sock` volume from the compose file.
- The supervisor container still mounts no docker socket directly; it talks to the proxy over the compose-network TCP, which is the same security posture (proxy is the filter).

This is the largest plan/tasks edit of the six findings. F5 cascades into T002, T009, T011, T015.

**Alternative considered**: use `tecnativa/docker-socket-proxy` instead, which DOES support unix-socket output via a config setting. Operator decision — I'll stick with linuxserver/socket-proxy per the approved slate and adjust to TCP, unless operator prefers the swap.

### F6 — `python:3.11-slim` has no `ps`/`pgrep`; in-container process inspection uses `/proc` directly

**Affects**: T019's chaos tests (verify "no lingering mempalace.mcp_server processes"), T020 acceptance (any process-introspection step).

**Observation**: Alpine does not include `pgrep`; `python:3.11-slim` (debian-slim) does not include `ps` or `pgrep` or `procps`. In-container process inspection must use `/proc/[0-9]*/cmdline` directly (a `sh -c` one-liner works fine; I used that in Claim 2's verification).

**Remediation**: either (a) `apt-get install -y procps` in `Dockerfile.mempalace` (adds ~1 MB); (b) test-harness uses `/proc`-based inspection without installing tools (minimal image); (c) both — install `procps` for human ops (so an operator running `docker exec spike-mempalace ps aux` gets sensible output) while test code uses `/proc` for robustness.

**Recommendation**: option (c). One line in the Dockerfile; cleaner operator experience; test code stays tool-agnostic.

---

## Summary

All three primary claims PASS. Clarify F is closed. But six spike findings require plan/tasks amendments before implementation proceeds past T001:

- **F1** → palace-bootstrap marker: change from `chroma.sqlite3` to `mempalace.yaml` OR drop detect-first entirely (idempotency observed).
- **F2** → wake-up flag shape: remove `--max-tokens` everywhere; top-level `--palace` goes before the `wake-up` subcommand.
- **F3** → Dockerfile.mempalace base: `python:3.11-slim`, not `python:3.11-alpine`.
- **F4** → pip hashes: generate a lockfile via `pip-compile` for full-tree `--require-hashes` rigor.
- **F5** → socket-proxy transport: TCP `:2375`, not unix socket. Affects compose, env var default, MCP config `env` block.
- **F6** → in-container process inspection: install `procps` + use `/proc` in tests.

All six are implementation-mechanism adjustments, not behavioural reversals. The milestone's thesis is intact; the topology still works.

**T001 completion state**: pass on all gating claims; six findings surfaced and documented; operator input required to absorb F1–F5 into the spec clarifications + plan + tasks before T002 starts. F6 can be absorbed directly into T002's Dockerfile without further operator decision.

The throwaway scratch stack at `~/scratch/m2-2-topology-spike/` remains up until the operator signs off on the findings; it will be torn down then.
