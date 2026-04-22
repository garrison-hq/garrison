# Documentation

Operator-facing and contributor-facing documentation for Garrison.
For the architectural picture and design decisions, see the top-level
[`ARCHITECTURE.md`](../ARCHITECTURE.md) and
[`RATIONALE.md`](../RATIONALE.md) — those remain the binding
documents. The files under this directory are the working-with-it
counterpart.

## Start here

- **[M1 retro](./retros/m1.md)** — what we learned shipping M1. Six
  things the spec got wrong, what the tests that caught each one
  looked like, the trade-offs accepted when the fix went in. The
  best single document for understanding what actually gets built
  when the spec-first workflow runs end-to-end.

## Getting it running

- **[Getting started](./getting-started.md)** — clean-clone-to-running
  walkthrough. Postgres 17, `make build`, `--migrate`, insert a
  department, run the supervisor, insert a ticket, watch the
  subprocess fire.

## Understanding the design

- **[Architecture pointer](./architecture.md)** — a short orientation
  into `ARCHITECTURE.md` and `RATIONALE.md` so you know which one to
  open for which question.
- **[ARCHITECTURE.md](../ARCHITECTURE.md)** (repo root) — components,
  data model, event flow, dashboard surfaces, build plan M1–M8.
- **[RATIONALE.md](../RATIONALE.md)** (repo root) — 12 design
  decisions with alternatives considered and trade-offs accepted.
  §12 lists what this system explicitly is not.

## Retrospectives

One per milestone. Written after acceptance passes; records what
shipped, what the spec got wrong, what the tests caught, and what's
deferred to the next milestone.

- [M1 — event bus + supervisor core](./retros/m1.md) (2026-04-22)

## Specs

Live at [`specs/`](../specs/) in the repo root, not under `docs/`.
Specs are under CC-BY-4.0 (see [`LICENSE-DOCS`](../LICENSE-DOCS))
and include the binding context files under
[`specs/_context/`](../specs/_context) plus per-milestone
directories (`specs/m{N}-*/`).

The M1 artifacts are the first public evidence of what this
workflow produces:
[spec](../specs/m1-event-bus/spec.md) ·
[plan](../specs/m1-event-bus/plan.md) ·
[tasks](../specs/m1-event-bus/tasks.md) ·
[acceptance evidence](../specs/m1-event-bus/acceptance-evidence.md) ·
[retro](./retros/m1.md).

## Contributing

See [`CONTRIBUTING.md`](../CONTRIBUTING.md) for the how; see
[`AGENTS.md`](../AGENTS.md) for the binding guidance that applies to
every patch, human-written or AI-written.
