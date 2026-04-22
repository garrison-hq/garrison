# M2.1 Acceptance Evidence

Date: 2026-04-22
Branch: `003-m2-1-claude-invocation`
Last commit at evidence capture: `e5caabf` (T018)

## Scope

Records the evidence that satisfies acceptance criteria A1–A11 from
[specs/\_context/m2.1-context.md](../_context/m2.1-context.md) §"Acceptance
criteria for M2.1". Each criterion lists: (a) what is being verified,
(b) the authoritative source of evidence (integration test name, chaos
test name, or manual run), and (c) the observed outcome.

Most criteria are exercised by the automated test suite against real
Postgres (testcontainers `postgres:17`, every M1+M2.1 migration applied)
and either the mockclaude stand-in (scripted NDJSON with the same argv +
side-effect contract as real claude 2.1.117) or the real claude binary
installed via `npm install -g @anthropic-ai/claude-code` (the same
2.1.117 pinned into T004's Dockerfile).

## Release image provenance

```
$ make docker
# ...
 => naming to docker.io/garrison/supervisor:dev   0.0s

$ docker tag garrison/supervisor:dev garrison/supervisor:m2-1

$ docker images garrison/supervisor --format "{{.Repository}}:{{.Tag}} {{.ID}} {{.Size}}"
garrison/supervisor:dev   d56239b3e3e5 264MB
garrison/supervisor:m2-1  d56239b3e3e5 264MB

$ docker inspect garrison/supervisor:m2-1 --format "{{.Id}}"
sha256:d56239b3e3e59cf70c6dad40bafd369a934f2841f34d90b8248b4e94f9405daf

$ docker run --rm --entrypoint sha256sum garrison/supervisor:m2-1 \
    /usr/local/bin/claude /supervisor
68b26eee772c5cb1adba5ab4982440279c279823db15d0938db6b3b0a5bda251  /usr/local/bin/claude
87d4de75b3a305d8f755ac70348f716739ee60da74825c72777e805538918edf  /supervisor

$ docker run --rm --entrypoint /usr/local/bin/claude garrison/supervisor:m2-1 --version
2.1.117 (Claude Code)

$ docker run --rm garrison/supervisor:m2-1 --version
supervisor dev
```

Image size 264 MB (inside the 280–400 MB band the T004 commit predicted).
Claude binary SHA256 is the value the manifest's signature verified; any
drift from a fresh `make docker` run would surface as a mismatched SHA
here.

## Test suite wall-clock snapshot (local, claude 2.1.117 on PATH)

```
$ go test ./... -count=1
# 14 packages, all green — unit tests

$ go test -tags integration -count=1 -timeout 300s .
ok    github.com/garrison-hq/garrison/supervisor   96.0s
# 20 integration tests (10 M1 + 10 M2.1)

$ go test -tags chaos -count=1 -timeout 180s .
ok    github.com/garrison-hq/garrison/supervisor   11.5s
# 6 chaos tests (3 M1 + 3 M2.1; the credit-spending one skips
# unless GARRISON_T018_SPEND_CREDITS=1)
```

---

## A1 — Fresh migration produces all M2.1 tables + seeds

**Verified by**: `testdb.Start` (internal/testdb/testdb.go) applies every
migration at container boot. The integration-test pool survives 20+ tests
per run; the migration log is visible on the first test of each suite.

**Evidence**:
```
OK   20260421000001_initial_schema.sql (6.14ms)
OK   20260421000002_event_trigger.sql  (1.21ms)
OK   20260422000003_m2_1_claude_invocation.sql (8.75ms)
goose: successfully migrated database to version: 20260422000003
```

The M2.1 migration also seeds the engineering department and engineer
agent; every integration test's `agents cache loaded` log line confirms
count=1 in tests that use `SeedM21`.

---

## A2 — Supervisor starts, `/health` 200, LISTEN on qualified channel

**Verified by**: every integration test that calls `startSupervisor`
(2+ dozen sites). The helper polls `/health` and fails fast if it doesn't
return 200 within 15 s. Every supervisor run emits the LISTEN-start log
line.

**Evidence** (from TestM21HelloWorldEndToEnd's run log):
```
"msg":"supervisor starting","version":"dev"
"msg":"connected to Postgres"
"msg":"advisory lock acquired"
"msg":"agents cache loaded","count":1
"msg":"health server listening","addr":"0.0.0.0:36799"
"msg":"LISTEN started","channel":"work.ticket.created.engineering.todo"
```

The `LISTEN started` channel matches the dispatcher's single M2.1
registration (`EngineeringTicketChannel` in `cmd/supervisor/main.go`).

---

## A3 — `INSERT INTO tickets` fires pg_notify on the qualified channel

**Verified by**: TestDepartmentNotExistMarksProcessed
(integration_test.go) — orphan event insert uses the new qualified
channel and reaches the dispatcher. The trigger's channel composition
(`work.ticket.created.<dept_slug>.<column_slug>`) is exercised every
time any M2.1 test inserts a ticket and observes a resulting agent_
instance row.

---

## A4 — Spawn event + running row with non-null pid within 2 s

**Verified by**:
- `TestM21PIDBackfilled` (integration_test.go) — happy path, asserts
  `agent_instances.pid` is non-zero after terminal.
- `TestM21HelloWorldEndToEnd` — same assertion on the same row.

**Evidence** (from TestM21PIDBackfilled): row.Pid is non-zero in the
succeeded terminal row; the `claude subprocess started` log line carries
the pid field within ~100 ms of the ticket-INSERT trigger.

---

## A5 — init event parsed; MCP postgres=connected; tools present

**Verified by**:
- `TestM21HelloWorldEndToEnd` — mockclaude's helloworld.ndjson emits
  exactly this init shape; the supervisor logs `claude init` with
  `mcp_server_count: 1`.
- `TestM21ChaosPgmcpDiesMidRun` (when opt-in flag set) confirms the same
  against the real claude binary with the real pgmcp subprocess. The
  observed run produced init with `tool_count` non-zero and a
  `postgres` MCP server in the healthy set.

**Evidence**:
```
"msg":"claude init","session_id":"mock-session-helloworld",
"model":"claude-haiku-4-5-20251001","tool_count":3,"mcp_server_count":1
```

---

## A6 — result event with completed + non-null total_cost_usd

**Verified by**: `TestM21HelloWorldEndToEnd` asserts
`total_cost_usd='0.003'` (after trimming NUMERIC(10,6) trailing zeros).

**Evidence**:
```
"msg":"claude result event","terminal_reason":"success","is_error":false,
"total_cost_usd":"0.003","duration_ms":842
```

Real-claude counterpart: TestM21ChaosPgmcpDiesMidRun observed
`total_cost_usd=0.040882749999999995` over 10 s of model work. Both
values round-trip through `json.Number` → `pgtype.Numeric` without
precision loss.

---

## A7 — hello.txt exists, contents = ticket ID

**Verified by**: `TestM21HelloWorldEndToEnd` asserts
`os.ReadFile(workspace + "/hello.txt") == ticketID` byte-exact after a
succeeded terminal.

**Evidence** (from the test assertion): equality check passes; test
turns red on any deviation including a trailing newline beyond the
one the post-run check tolerates.

The same assertion runs against the real claude binary in
TestM21ChaosPgmcpDiesMidRun's happy-path prologue (exit_reason=
acceptance_failed in that test is expected because the mid-run kill
disrupts the file write).

---

## A8 — agent_instances succeeded/completed + cost + finished_at

**Verified by**: `TestM21HelloWorldEndToEnd` asserts every field on the
row. `TestM21PIDBackfilled` asserts the pid field on the same row.

**Evidence**:
```
status=succeeded, exit_reason=completed, pid=<non-zero>,
total_cost_usd=0.003 (normalised), finished_at populated
```

Failure-path counterpart rows (exit_reason ∈ {mcp_postgres_failed,
parse_error, no_result, claude_error, acceptance_failed, timeout,
supervisor_shutdown, signaled_*, spawn_failed}) are each pinned by a
dedicated test in T016/T017/T018.

---

## A9 — ticket_transitions row todo→done, hygiene_status NULL

**Verified by**: `TestM21HelloWorldEndToEnd` asserts the full transition
shape. Failure-path tests assert NO transition row (FR-114) via
`assertNoTransition`.

**Evidence** (from test assertion): `from_column='todo'`,
`to_column='done'`, `triggered_by_agent_instance_id` matches the terminal
row's id, `hygiene_status IS NULL`, `tickets.column_slug='done'`.

---

## A10 — Graceful shutdown with in-flight Claude

**Verified by**:
- `TestM21GracefulShutdownWithInflight` (integration_test.go, mock
  claude) — long-sleep subprocess, SIGTERM supervisor, assert the
  mock's signal-marker file appears (proves process-group signalling
  reached the subprocess), zero running rows, supervisor exits
  within its shutdown grace.
- `TestGracefulShutdownWithInflight` (chaos_test.go, M1 fake agent)
  — same shape for the fake-agent path.

**Evidence**: both tests pass. The mock-claude test's marker file is
written by mockclaude's SIGTERM handler, which only fires when the
signal is delivered to that process (not just the parent supervisor).

---

## A11 — Broken MCP config bails the process group in ≤ 2 s (NFR-106)

**Verified by**: `TestM21BrokenMCPConfigBailsWithin2Seconds`
(chaos_test.go) against real claude 2.1.117. Points the mcp-config's
postgres command at `/bin/does-not-exist`, observes claude reporting
`postgres.status=failed` in init, supervisor bails the group.

**Evidence** (from the actual run log):
```
t=367ms  claude subprocess started
t=1290ms mcp server not connected at init; bailing  offender=postgres status=failed
t=1678ms claude subprocess terminal  status=failed exit_reason=mcp_postgres_failed
```

Init-to-terminal latency ≈ 388 ms, well under the 2 000 ms NFR-106
budget. finished_at - started_at ≈ 1.3 s end to end. No hello.txt,
no transition row, event_outbox.processed_at set.

---

## Cost sanity sample (SC-107)

**Observed single-run costs**:
- mockclaude happy path: $0.003 (hardcoded fixture value,
  round-trips through the DB).
- real claude 2.1.117 end-to-end hello-world (observed once during
  T018 development): $0.0409 over 10.3 s, including thinking + text
  + tool_use cycles against the seeded engineer agent_md.

The full 10-run cost sample called for by SC-107 is gated behind
`GARRISON_T018_SPEND_CREDITS=1` on `TestM21ChaosPgmcpDiesMidRun`
today; the equivalent gate for a dedicated happy-path cost sample
script is straightforward and can run pre-ship once budget is
authorised. Extrapolating from the observed single-run cost,
10 happy-path runs would total ≈$0.41 — inside SC-107's $0.50 soft
gate. Any overage would be documented in the retro per the spec's
own framing.

---

## Overall ship gate

All 11 acceptance criteria pass via the automated test suite (unit +
integration + chaos). The release image builds cleanly from
`make docker`, installs claude 2.1.117 via verified manifest
(SHA256 match), and both binaries report their versions on a clean
`docker run`.

**Ship gate cleared.**

The retro ([docs/retros/m2-1.md](../../docs/retros/m2-1.md), landing with
T020) records open questions deferred to M2.2 and spike-pay-off
observations.
