# M7.1 spike — real container execution pipeline

Date: 2026-06-10. Time-boxed hands-on spike per RATIONALE §13, run in
`~/scratch/m7-1-spike/` against the live Acme Crates BV compose stack.
Binding input to `specs/_context/m7-1-context.md`.

Context: M7 shipped the per-agent container substrate (migrate7
grandfathering, socket-proxy controller, cgroup caps, image pinning)
but `runRealClaudeViaContainer` is an outline and `UseDirectExec` is
guard-forced on. This spike answers the unknowns blocking the real
pipeline. The questions are about how docker exec, claude-in-container,
and egress *actually* behave — not how their docs say they behave.

## Environment

- Host: Fedora (kernel 6.19), Docker with `linuxserver/socket-proxy`
  at `garrison-docker-proxy:2375` (`POST=1 EXEC=1 ALLOW_START=1
  IMAGES=1`).
- Agent image: `garrison-claude:m5` (node:22-slim + claude 2.1.170,
  entrypoint `/usr/local/bin/claude`, user `node` uid 1000).
- Sandbox caps under test (M7 sealed set): `--read-only`,
  `--cap-drop ALL`, `--pids-limit 200`, `-m 512m`,
  tmpfs `/tmp` 64m — plus spike additions noted below.
- Claude auth: operator OAuth subscription token
  (`CLAUDE_CODE_OAUTH_TOKEN`); `ENABLE_TOOL_SEARCH=false` pinned per
  the 2.1.170 deferral finding (commit 144e44f).

## Findings

### F1 — idle entrypoint: containers must override the image entrypoint

The image's entrypoint is `claude`, so a bare `create` exits(1)
immediately (the Exited(1) fleet observed in the acceptance run).
`--entrypoint /bin/sleep` + cmd `infinity` gives a standing container;
`/bin/sleep` exists in node:22-slim. `Entrypoint` override is a plain
field in the create body — one-line change to `buildCreateBody`.

### F2 — exec through the socket proxy works, including Env injection

Full production path verified over plain HTTP against
`garrison-docker-proxy:2375`:

- `POST /containers/<name>/exec` → 201 with exec id. The body's
  `"Env": [...]` field is accepted — **per-exec secret injection
  works**; secrets never live in the container's create config or
  argv.
- `POST /exec/<id>/start {"Detach":false,"Tty":false}` → 200
  `application/vnd.docker.raw-stream`, response body is the classic
  8-byte-header multiplex framing (`[stream(1) pad(3) size(4)]`,
  stream 1 = stdout, 2 = stderr). A demultiplexer is ~25 lines; the
  scaffold's "raw stream as stdout" comment in `socketproxy.go` T004
  is the only missing piece.
- `GET /exec/<id>/json` → `ExitCode`. Allowed under `EXEC=1` (needed
  for the pipeline's exit adjudication).

No stdin attach is required: the direct-exec spawn already passes the
prompt via argv with stdin=/dev/null, and the same shape works in the
container. This avoids HTTP connection hijacking entirely — the
stdout-only stream is a normal chunked response.

### F3 — `network=none` makes claude hang, not fail

With no network, `claude -p` does not fail fast: it retries and hangs
past 120 s (would burn the full 5 m subprocess timeout on every
spawn). **The container pipeline requires egress; `network=none` for
the execution container is not viable.** The M7 spec's egress
allow-list rule has to be delivered by an egress proxy, not by network
removal.

### F4 — claude honors HTTPS_PROXY end to end

With `HTTPS_PROXY=http://<squid>:3128`, a full turn completed and the
squid access log shows the exact egress surface:

```
CONNECT api.anthropic.com:443            (×4, the API traffic)
CONNECT http-intake.logs.us5.datadoghq.com:443   (telemetry)
```

So the enforcement point can be a dual-homed proxy container with a
CONNECT allow-list. Telemetry endpoints (datadog; statsig/sentry also
documented upstream) need an explicit allow-or-deny decision — claude
functioned with them allowed; denial is expected to be non-fatal but
was not exercised in this spike.

### F5 — HOME on a read-only rootfs needs a tmpfs

User `node`'s HOME is on the read-only rootfs; claude writes session
state under `~/.claude`. tmpfs `/home/node` (64 m) fixed it. Should
join the sealed tmpfs set alongside `/tmp`.

### F6 — in-container stdio MCP servers work via a read-only binary mount

The CGO_ENABLED=0 supervisor binary bind-mounted read-only at
`/usr/local/bin/garrison-supervisor` runs fine under
read-only/cap-drop ALL; `mcp-postgres` completed the JSON-RPC
initialize handshake against `garrison-postgres` over the network.
The per-spawn MCP config can be written into the container's `/tmp`
tmpfs via a tiny exec (`sh -c 'cat > /tmp/mcp-<id>.json'`) before the
claude exec, and removed after.

### F7 — full integration green

One exec inside the sandboxed container with: oauth token + 
`HTTPS_PROXY` via squid + `ENABLE_TOOL_SEARCH=false` + `--tools ""` +
in-container postgres MCP entry → claude called the MCP tool and
answered correctly (2 turns, $0.014). Every layer of the target
pipeline is individually and jointly proven.

## Surprises

1. `network=none` ⇒ hang-not-fail (F3) was the sharpest surprise; it
   invalidates the literal reading of the M7 "network=none default"
   rule for execution containers.
2. Claude's telemetry CONNECTs (datadog) — an egress allow-list that
   only permits api.anthropic.com will generate persistent denied
   CONNECTs in proxy logs unless telemetry is disabled via env
   (`DISABLE_TELEMETRY`/`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`)
   or explicitly allowed.
3. Exec `Env` passing through linuxserver/socket-proxy needed no extra
   proxy permissions beyond `EXEC=1`.

## Open questions (operator decisions for the context doc)

1. **Egress topology.** Spike used a dual-homed squid on the default
   compose network. Production shape: a dedicated internal
   `garrison-agents` network holding only agent containers + the
   egress proxy (+ optionally postgres/mcpjungle, see Q2), proxy
   dual-homed to reach the internet, CONNECT allow-list =
   `api.anthropic.com` (+ telemetry decision). Which proxy (squid vs
   tinyproxy vs envoy) and per-customer vs shared remains the
   operator's call; per-customer egress was previously deferred to
   M9+.
2. **MCP topology for M7.1.** Two scopes:
   - *M7.1a (recommended, this milestone):* keep today's stdio MCP
     set, run the servers inside the agent container via the
     read-only binary mount (F6). Requires postgres reachable from
     the agent network — same trust model as direct-exec today
     (agent_ro DSN + instance-scoped finalize), now with fs/caps/pids
     isolation added.
   - *M7.1b (defer):* all agent MCP through the MCPJungle gateway
     (streamable_http + per-agent bearer). Depends on registering the
     in-tree servers in MCPJungle — its own milestone-sized effort.
3. **Telemetry egress**: allow, or set the disable env in the spawn
   pipeline and deny at the proxy. Spike default suggestion: disable +
   deny (smaller allow-list, quieter logs).

## Implications for M7.1

- `buildCreateBody`: add `Entrypoint: ["/bin/sleep"], Cmd:["infinity"]`
  override + tmpfs `/home/node` + join the agents egress network
  instead of `none`. Container naming already fixed separately
  (`garrison-agent-<short-id>`); spawn-side lookup must use the same
  convention (the `containerNameForRole` latent bug from the
  acceptance-run diary).
- `socketproxy.Exec`: add `Env` to exec-create; implement the 8-byte
  demux; surface `GET /exec/<id>/json` for exit codes. No stdin/hijack
  work needed.
- `spawn/m7.go runRealClaudeViaContainer`: becomes a thin variant of
  the direct-exec pipeline — same argv builder, same claudeproto
  consumer fed by the demuxed stdout stream, same FR-108 health gate,
  budget, finalize observer, terminal adjudication. Secret/bearer
  injection moves to exec-create Env (vault values still
  UnsafeBytes-scoped in the supervisor; they transit the proxy POST
  body — the proxy is localhost-only and already trusted with the
  docker socket, but note it in the threat model).
- MCP config per spawn: write to container `/tmp` via exec, point
  `--mcp-config` there, reference the mounted supervisor binary; the
  `GARRISON_SUPERVISOR_BIN_OVERRIDE` knob already abstracts the path.
- Compose: new internal network + egress-proxy service + allow-list
  config; agent containers join it at create.
- The M7 context/spec egress rule needs amending from "network=none"
  to "no egress except through the allow-listed proxy" — sealed-surface
  amendment to be recorded in the M7.1 context doc.
- `EnsureMcpjungleTokenForAgent` (M8 T010) slots directly into the
  exec Env once the gateway path lands (M7.1b); not blocking M7.1a.
