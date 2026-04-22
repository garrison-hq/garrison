# Contract: supervisor CLI

**Source**: spec FR-013. Env-var configuration is documented in the plan's "Config (decision 2)" table.

## Binary

One binary: `supervisor` (built from `supervisor/cmd/supervisor`).

## Flags

| Flag | Behavior | Exit code |
|------|----------|-----------|
| `--version` | Prints `supervisor vX.Y.Z` (from `-ldflags="-X main.version=..."`) to stdout and exits. | `0` |
| `--help` / `-h` | Prints usage (including the env-var table) to stdout and exits. | `0` |
| `--migrate` | Runs goose `UpContext` against `GARRISON_DATABASE_URL`, prints the applied migrations, and exits. | `0` on success, non-zero on any migration error. |
| (no flags) | Runs the supervisor daemon until `SIGTERM`/`SIGINT`. | `0` on clean shutdown, non-zero if any subprocess required a SIGKILL escalation (FR-010/NFR-005) or if the FR-018 advisory lock was already held at startup. |

## Positional arguments

None. Rejects any positional argument with a non-zero exit and a usage message.

## Environment variables (required/optional)

| Env var | Required | Default |
|---------|----------|---------|
| `GARRISON_DATABASE_URL` | Yes | — |
| `GARRISON_FAKE_AGENT_CMD` | Yes for daemon mode | — (not read by `--version`, `--help`; required for `--migrate` because the URL comes from the same var) |
| `GARRISON_POLL_INTERVAL` | No | `5s` |
| `GARRISON_SUBPROCESS_TIMEOUT` | No | `60s` |
| `GARRISON_SHUTDOWN_GRACE` | No | `30s` |
| `GARRISON_HEALTH_PORT` | No | `8080` |
| `GARRISON_LOG_LEVEL` | No | `info` |

`--migrate` reads only `GARRISON_DATABASE_URL` and (for log level) `GARRISON_LOG_LEVEL`. Other env vars are not validated by the migrate path; the daemon path validates all of them.

## Signals (daemon mode)

| Signal | Behavior |
|--------|----------|
| `SIGTERM` | Graceful shutdown (FR-009, FR-010). Root context cancelled; in-flight subprocesses receive SIGTERM → 5s → SIGKILL; binary exits within `GARRISON_SHUTDOWN_GRACE`. |
| `SIGINT` | Same as SIGTERM. |
| `SIGHUP` | Open question: plan proposes identical to SIGTERM; confirm before implementation. |
| `SIGKILL` (not catchable) | Binary dies immediately; stale `running` rows reconciled on next startup via FR-011. |

## Stdout / stderr

Stdout carries all `slog` output (JSON lines). Stderr carries nothing the supervisor produces directly — Go runtime panics, if any, write there. This matches container-log expectations.

## Exit codes — full table

| Code | Meaning |
|------|---------|
| `0` | Clean exit (CLI mode: success; daemon mode: graceful shutdown with no SIGKILL escalations). |
| `1` | Catch-all failure (config load, non-recoverable runtime error). |
| `2` | Invalid CLI usage (unknown flag, positional arg). |
| `3` | `--migrate` failed. |
| `4` | FR-018 advisory lock held by another supervisor. |
| `5` | Graceful shutdown but at least one subprocess required a SIGKILL. |

These are defined as exported constants in `cmd/supervisor` so tests can assert on them.
