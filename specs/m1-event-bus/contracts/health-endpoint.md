# Contract: `/health` HTTP endpoint

**Source requirements**: spec FR-016, FR-017; Clarifications session 2026-04-21 Q2.

## Shape

| Method | Path | Response body | Meaning |
|--------|------|--------------|---------|
| `GET`  | `/health` | (empty, status code is the signal) | see below |

## Status codes

| Code | Condition |
|------|-----------|
| `200 OK` | Both conditions hold: (a) a fresh `SELECT 1` ping via the shared pool succeeded; (b) the fallback poller completed a cycle within the last `2 × cfg.PollInterval`. |
| `503 Service Unavailable` | Either condition fails. |

No other status codes. No readiness/liveness split. No `/health/live` or `/health/ready` variants.

## Freshness semantics

- The ping is issued **synchronously** inside the handler against a shared `*pgxpool.Pool` with a 500 ms timeout. Result updates `LastPingOK` and `LastPingAt` atomically.
- `LastPollAt` is written by the fallback poller on each successful cycle. The handler reads it atomically.
- Condition (b) evaluates `time.Since(LastPollAt) <= 2 * cfg.PollInterval`. Default poll interval is 5 s → `/health` reports 503 if no poll has completed in 10 s.

## Startup behavior

From the FR-017 clarification and plan's startup ordering: `/health` is served as soon as the HTTP server starts, but returns `503` until the canonical startup sequence completes (connect → advisory lock → recovery → initial poll → LISTEN). The first `200` response is possible only after the initial poll has written `LastPollAt`.

## Binding and authentication

- Binds `0.0.0.0:$GARRISON_HEALTH_PORT` (default `8080`).
- **No authentication** in M1. Network-level exposure is delegated to the container runtime and Coolify routing rules.

## Shutdown

On root-context cancellation, `http.Server.Shutdown(ctx)` is called with a context budget of `cfg.ShutdownGrace`. The server stops accepting new connections and waits for in-flight handlers to return.

## What the endpoint MUST NOT do

- Emit response bodies containing sensitive info (DB URLs, env values, process state beyond pass/fail).
- Track request authorship.
- Trigger side effects (no "kick the poller" semantics; the handler is read-mostly, with the one `SELECT 1` being read-only).
