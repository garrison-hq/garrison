# Security policy

Garrison is pre-1.0 software built by a solo maintainer. This file
documents what "security reporting" looks like in practice given
that constraint, so expectations are honest up front.

---

## Supported versions

Only the current `main` branch is actively maintained. There are no
backported security fixes for prior tags or pre-M1 branches. If you
are running something other than the most recent release, the first
step of any security discussion will be "please upgrade."

| Version | Supported |
|---|---|
| `main` | Yes |
| Anything older | No |

---

## Reporting a vulnerability

**Do not open a public GitHub issue for a security-sensitive bug.**

Send reports to:

> **TODO-set-security-email@garrison-hq.org**
>
> *(Operator: update this placeholder with a real mailbox before
> announcing the repository publicly. Until then, report privately
> via the GitHub repository's "Security → Report a vulnerability"
> flow, which routes to the repository owner's private advisory
> inbox.)*

Please include:

- A description of the issue and where in the code you believe it
  lives (file path + line number if you have one).
- The conditions under which the issue is exploitable (e.g. "needs
  `GARRISON_FAKE_AGENT_CMD` to be attacker-controlled").
- A minimal reproduction if you have one. A failing
  `integration_test.go`-style test case is ideal but not required.
- Whether you have disclosed this anywhere else and on what
  timeline you expect to disclose publicly.

Encrypted reports are welcome; if you want a PGP key, ask in an
initial plaintext email and one will be shared.

---

## What happens next

- **Acknowledgement**: within **7 calendar days** of the report,
  you will receive a reply confirming the report was received and
  an initial read of whether it looks like a genuine
  vulnerability. Seven days is realistic for a solo maintainer;
  shorter promises would be dishonest.
- **Triage**: if the issue is confirmed, a rough severity and fix
  timeline will follow within another **7 days**. Most fixes will
  land within **30 days** of acknowledgement. Complex fixes may
  take longer; you will be kept in the loop either way.
- **Public disclosure**: coordinated by default. A fix goes in,
  a new release is cut, and the advisory is published with credit
  to the reporter (unless anonymity is requested). If 90 days pass
  without a fix, you are free to disclose publicly — please give
  notice before doing so.

---

## What counts as a vulnerability

Examples that qualify:

- SQL injection, command injection, path traversal, or any input
  handling that lets an unauthenticated party reach state or
  resources they shouldn't.
- Authentication or authorization bypass (once M3+ add an
  authenticated UI).
- Privilege escalation inside a subprocess spawned by the
  supervisor (e.g. an agent subprocess managing to compromise the
  supervisor itself).
- Denial-of-service vectors that a low-effort attacker can
  trigger (not "this would fall over under 100k qps").
- Secrets leakage in logs, error messages, or DB rows.

Examples that do **not** qualify and should go in a normal issue:

- "The supervisor executes `GARRISON_FAKE_AGENT_CMD` as given" — yes;
  that's the point. It's an operator-supplied command. The env
  var is a trust boundary.
- "Postgres is reachable on a known port" — operator
  responsibility.
- "A subprocess uses more memory than I expected" — not a
  vulnerability unless it's triggerable by an unauthenticated
  party.

---

## Current highest-risk surface (M1)

For reference, the M1 supervisor's most sensitive code paths are:

- `GARRISON_FAKE_AGENT_CMD` is a **trust boundary**. The supervisor
  splits it with `shlex` and `exec`s it directly. An attacker who
  can set this env var has arbitrary code execution as the
  supervisor user. This is by design for the fake-agent placeholder
  but will be reconsidered when M2 swaps in the real Claude CLI.
- `pg_terminate_backend` + reconnect path (`internal/events/reconnect.go`) —
  retry logic is non-trivial; a bug that stops the supervisor from
  reconnecting is a DoS against ticket processing.
- The dedupe transaction in `internal/spawn/` — a bug here could
  allow double-spawn of the same event, which is both a
  correctness issue and (at scale) a resource-consumption issue.

Security-minded review of these is particularly welcome.

---

## Scope

This policy covers the Garrison codebase under this repository
(`supervisor/`, `migrations/`, tests, etc.). It does **not** cover:

- Upstream dependencies (report those to their maintainers).
- Deployment infrastructure (Hetzner, Coolify, Docker host,
  Postgres operator-managed).
- Downstream forks.
