# Contributing to Garrison

Garrison is built by one person alongside other work. Response times
are measured in days to weeks, not hours. Not every proposal will
land — some will conflict with decisions already made in
`RATIONALE.md`, some will be out of scope for the current milestone,
some will be fine in principle but need to wait for the milestone
that makes them load-bearing. That's the pace. If that's a
dealbreaker, fork — the AGPL explicitly allows it.

This document covers how to contribute productively given those
constraints.

---

## Before you start

Read, in order:

1. [`README.md`](./README.md) — the one-page picture.
2. [`AGENTS.md`](./AGENTS.md) — binding guidance for any contributor
   (human or AI). The scope-discipline and locked-dependency rules
   apply to all patches.
3. [`RATIONALE.md`](./RATIONALE.md) — 12 architectural decisions
   with alternatives considered. Most "wouldn't it be better
   if..." suggestions are already answered here. If you find
   yourself proposing something that was explicitly rejected,
   either explain why the rejection no longer applies or pick a
   different proposal.
4. The **active milestone context** at
   `specs/_context/m{N}-context.md`. This is where the operational
   constraints for the current milestone live.
5. The **active milestone spec and plan** in `specs/m{N}-*/`. If
   you're contributing code for a given milestone, you're working
   against these documents, not against general intuition.

---

## Open an issue first

For anything beyond an obvious typo or a small bug fix, **open an
issue before a PR**. This isn't bureaucratic; it's because:

- The current milestone's scope is narrow on purpose. Good
  proposals out of scope for this milestone are better captured as
  issues and revisited when the relevant milestone becomes active.
- Some proposals conflict with `RATIONALE.md` in ways that aren't
  obvious until someone surfaces them. Faster to find out on an
  issue than after a PR is written.
- For any new dependency (see below), the discussion happens in an
  issue first — not in a PR's commit message.

Issue templates in
[`.github/ISSUE_TEMPLATE/`](./.github/ISSUE_TEMPLATE) cover the three
common shapes: bug reports, feature proposals, and spec /
RATIONALE questions. Pick the closest fit.

---

## The locked dependency list

The supervisor has a **locked dependency list** in `AGENTS.md`
under "Stack and dependency rules". The list is:

- `github.com/jackc/pgx/v5`
- `sqlc` (build-time)
- `log/slog` (stdlib)
- `golang.org/x/sync/errgroup`
- `github.com/stretchr/testify`
- `github.com/testcontainers/testcontainers-go`
- `goose` or `tern` (goose was chosen for M1)

Adding a direct dependency outside this list is allowed but requires:

1. A justification paragraph in the commit message explaining what
   the dep does, why stdlib or an already-installed dep isn't
   enough, and what the alternatives were.
2. A mention in the milestone retro (`docs/retros/m{N}.md`).

M1 added exactly one: `github.com/google/shlex` for argv splitting.
See the M1 retro §"Dependencies added outside the locked list" for
the format.

Libraries we explicitly will not accept:

- `lib/pq` — pgx supersedes it.
- `gin` / `echo` / `fiber` — stdlib `net/http` + `chi` if routing
  grows.
- `logrus` / `zap` — `log/slog` is stdlib and sufficient.
- `viper` — env vars + a typed config struct.

---

## Scope discipline

`AGENTS.md` has a section called "Scope discipline (the most
important rule)". It applies to human contributors too. In short:

- Don't add features the current milestone doesn't require.
- Don't add abstractions for hypothetical future requirements.
  Three similar functions is fine; one premature interface is
  usually regrettable.
- Don't add error handling or validation for scenarios that can't
  happen.
- Don't clean up surrounding code as part of a bug fix. A fix
  should be the smallest change that fixes the bug.
- Don't write comments that describe *what* the code does. Well-
  named identifiers already do that. Comments are for *why* —
  subtle invariants, workarounds for specific bugs, behavior that
  would surprise a reader.

Bundled "fix the bug plus some cleanup" PRs are the most common
reason a PR takes longer to review. Split them.

---

## Code rules

### Go

- Go 1.25+. The project builds with `CGO_ENABLED=0` and ships as a
  single static binary.
- Every goroutine accepts a `context.Context` and respects
  cancellation. No bare `go func()`.
- `main` owns a root context. SIGTERM / SIGINT / SIGHUP cancel it.
  Subsystems receive derived contexts.
- `errgroup.WithContext` for top-level "run N subsystems, cancel
  all if one fails". Channel-based fan-in is fine for two
  cooperating goroutines (see `internal/events/reconnect.go`).
- Structured logging via `log/slog`. No `fmt.Print*` in production
  paths. Every lifecycle transition gets one structured record.
- `gofmt` and `go vet` must pass. `make lint` checks both.

### SQL

- `migrations/` at the repo root is the single source of truth.
  The supervisor uses `goose` to apply them; the `sqlc`-generated
  query layer derives from the same SQL. A future TypeScript
  dashboard will derive from the same files.
- Do not hand-edit generated code under
  `supervisor/internal/store/`. Change the SQL (query file under
  `migrations/queries/` or the relevant migration) and run
  `make sqlc`.
- Migrations are immutable once merged. New state needs a new
  migration file.

### Tests

- Integration tests go behind `//go:build integration`. Chaos
  tests behind `//go:build chaos`. Unit tests have no build tag.
- Integration and chaos suites use real Postgres via
  `testcontainers-go`. **Do not mock the database.**
- `make test-integration` and `make test-chaos` must pass before
  PR.

---

## Commit messages

Format:

```
T0XX: one-line subject (≤72 chars)

Optional body explaining WHY the change is needed, not what the
code does. Reference the relevant FR/NFR from the milestone spec
if applicable (e.g. "Fixes the FR-018 advisory-lock path").

If a new dependency is added, include the one-paragraph
justification here (see CONTRIBUTING §"The locked dependency list").
```

The `T0XX:` prefix is how tasks in `specs/m{N}-*/tasks.md` are
tracked. For patches that don't map to a task (doc fixes, bug
fixes), use a descriptive prefix: `docs:`, `fix:`, `chore:`.

One logical change per commit. If your PR has four commits, each
should stand on its own.

---

## Pull request checklist

The PR template in
[`.github/pull_request_template.md`](./.github/pull_request_template.md)
asks you to confirm:

- [ ] Linked issue (or explanation why this didn't need one).
- [ ] Target milestone is the current active one.
- [ ] Locked dependency list not expanded without justification.
- [ ] `make lint`, `make test`, `make test-integration` pass.
- [ ] If chaos-relevant: `make test-chaos` pass.
- [ ] Documentation invalidations addressed (if the change
  contradicts something in `ARCHITECTURE.md`, `RATIONALE.md`, or a
  spec, the relevant document is updated in the same PR).

---

## Licensing

Contributions are accepted under the project's existing licenses:

- **Code** (everything that builds into the supervisor, its tests,
  its Dockerfile, its Makefile, its migrations) is AGPL-3.0-only.
- **Specs and documentation** (everything under `specs/`, `docs/`,
  and the top-level `.md` files other than `LICENSE`) is
  CC-BY-4.0.

By opening a PR you confirm you have the right to contribute the
code under these terms. No CLA, no DCO sign-off — just don't
submit code you don't have the right to license this way.

---

## Code of conduct

This project follows the Contributor Covenant 2.1. See
[`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md). Enforcement contact
is in that document.

---

## Security issues

**Do not open a public issue for a security-sensitive bug.** See
[`SECURITY.md`](./SECURITY.md) for the private reporting path.
