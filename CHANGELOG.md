# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
once it reaches 1.0. Until then, version numbers correspond to
milestones (M1, M2, ...).

## [Unreleased]

Nothing yet. M2 scope: swap the fake agent for a real Claude Code
invocation, wire in the first MemPalace MCP integration, land the
first engineering-department agent.md. See
[`ARCHITECTURE.md`](./ARCHITECTURE.md) §"Build plan — milestones".

---

## [M1] — 2026-04-22

The first shipped milestone. Event bus and supervisor core. Fake
agent via `sh -c`; no real Claude invocation yet.

### Added

- `supervisor` Go binary. Go 1.25, `CGO_ENABLED=0`, ~18 MB static.
  Subcommands: `--version`, `--migrate`, default run mode.
- Postgres 17 schema under `migrations/` (2 goose migrations):
  `departments`, `tickets`, `event_outbox`, `agent_instances`,
  partial indexes for the concurrency-cap query and the
  fallback-poll query, the `ticket_created_emit` trigger, and the
  `emit_ticket_created` function.
- sqlc-generated typed query layer in `supervisor/internal/store/`.
- `internal/pgdb` — `pgxpool` + dedicated LISTEN `*pgx.Conn`, the
  FR-017 100 ms → 30 s backoff dialer, and `AcquireAdvisoryLock`
  wrapping the fixed FR-018 key.
- `internal/events` — handler registry (`Dispatcher`), LISTEN loop,
  fallback poll, reconnect state machine.
- `internal/spawn` — per-event dedupe transaction, concurrency-cap
  check, `InsertRunningInstance`, `exec.CommandContext` with
  per-stream line-scanning goroutines, NFR-005 SIGTERM → 5 s → SIGKILL
  shutdown, single terminal transaction.
- `internal/recovery` — one-shot reconcile of stale `running` rows
  on startup (NFR-006 5-minute window).
- `internal/concurrency` — two-query cap check; cap = 0 is the
  FR-003 pause signal.
- `internal/health` — `/health` HTTP server returning 200 iff a
  500 ms `SELECT 1` succeeds and `time.Since(LastPollAt) ≤ 2·PollInterval`.
- `cmd/supervisor` — `errgroup.WithContext`, SIGTERM/SIGINT/SIGHUP
  handling, exit codes per contract.
- Tests — ~35 unit tests, an `integration`-tagged suite covering
  US1–US3 and the 100-ticket volume path, a `chaos`-tagged suite
  for reconnect, external SIGKILL, and graceful-shutdown-with-inflight.
- Runtime Docker image at `garrison/supervisor:dev`, built from
  `alpine:3.20` + `ca-certificates` (24 MB).
- Acceptance evidence doc documenting all 10 acceptance steps
  against `make docker` + a fresh `postgres:17`.

### Changed

- Dockerfile runtime base switched from
  `gcr.io/distroless/static-debian12` to `alpine:3.20` during T017
  acceptance. Distroless has no `/bin/sh`; the M1 fake-agent command
  is `sh -c "..."` and cannot exec without a shell. Trade-off: +7 MB.
  See [`docs/retros/m1.md`](./docs/retros/m1.md) §5.

### Resolved

- **LISTEN/poll in-flight race.** `sync.Map` on `Dispatcher`, keyed
  by event ID, gates a second poll-driven spawn while the first
  handler is still running. See retro §1.
- **Terminal-tx write cancelled on graceful shutdown.** Introduced
  `TerminalWriteGrace = 5s` with `context.WithTimeout(context.WithoutCancel(ctx), ...)`.
  See retro §2.
- **Reconnect-path nil-conn deref.** Inner `for conn == nil { ... }`
  dial-retry loop in `internal/events/reconnect.go`. See retro §3.

### Known issues (non-blocking)

- `agent_instances.pid` is never written. `InsertRunningInstance`
  runs before `cmd.Start()` so the pid isn't known at insert time;
  `UpdateInstanceTerminal` doesn't `SET pid`. Observability is fine
  (pid is emitted per subprocess log line); DB column is dead.
  Deferred to M2. See retro §4.
- Acceptance step 9 is under-specified after a clean shutdown.
  T017 interpreted the step as "demonstrate recovery works" by
  injecting a synthetic 10-min-stale row via direct SQL. The
  acceptance doc should be amended in M2 prep. See retro §6.

### Dependencies

Exactly one direct dependency added outside the locked list in
`AGENTS.md` §"Supervisor (Go) — Locked dependency list":

- `github.com/google/shlex` — POSIX-like argv splitter for
  `GARRISON_FAKE_AGENT_CMD`. Justified in the T008 commit; flagged
  in the retro. Single-file, stdlib-only, no transitive deps.

### Deferred to M2+

- Per-agent-type concurrency sub-caps.
- `/metrics` endpoint.
- `agent_instances.pid` backfill.
- Blue/green and rolling deploys (disallowed in M1 by FR-018's
  advisory-lock serialization).
- Operator-tunable fallback-poll batch size.
- Real `claude` CLI integration in the lifecycle manager.

### Open questions resolved

- **`github.com/google/shlex` — accept or implement in-tree?** →
  Accepted. See retro §"Plan-level open questions — resolved".
- **SIGHUP semantics — separate handling or treat as SIGTERM?** →
  Treated as SIGTERM. Single `signal.Notify` registers all three.

---

[Unreleased]: https://github.com/garrison-hq/garrison/compare/m1...HEAD
[M1]: https://github.com/garrison-hq/garrison/releases/tag/m1
