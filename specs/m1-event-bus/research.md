# Research: M1 — event bus and supervisor core

**Phase**: 0 (outline + research)
**Status**: complete — all NEEDS CLARIFICATION items from spec's open-questions section were resolved during `/speckit.clarify`; this file records the 7 structural decisions made during `/speckit.plan` plus the 2 micro-gaps flagged and resolved with the operator.

No open NEEDS CLARIFICATION markers remain.

---

## Decision 1 — Package layout under `supervisor/`

**Decision**: `cmd/supervisor` + eight internal packages (`config`, `pgdb`, `store`, `events`, `concurrency`, `spawn`, `recovery`, `health`). One responsibility per package; no circular dependencies; `store` is generated-only.

**Rationale**:
- Mirrors the five subsystems in the supervisor's errgroup wiring (connection, events, concurrency, spawn, health) with two thin helpers (`config`, `recovery`) and a generated data-access layer (`store`). Each has a natural home for its tests.
- Keeps each package small enough that an agent-generated test file can exhaust the package's surface area.
- Separates concerns that the spec treats separately: FR-003 (cap) and FR-004–FR-006 (spawn + terminal writes) live in different packages so their tests don't cross-contaminate.

**Alternatives considered**:
- Flat `internal/supervisor` with all code in one package. Rejected: slog-field scoping and test boundaries suffer; cross-package interface discipline is lost.
- Deeper hierarchy (e.g. `internal/events/listener` + `internal/events/dispatch` + `internal/events/poll`). Rejected: premature for M1's surface area; eight internal packages already cover the seams.

---

## Decision 2 — Config shape

**Decision**: Environment variables only, loaded into a typed `config.Config` struct validated at startup. No config file support. Env names prefixed `ORG_OS_`.

**Rationale**:
- FR-013 explicitly disallows config files in M1.
- Env-var-only maps directly to container-runtime config injection (Coolify, Docker, systemd), which is the deployment target (constitution XI).
- A typed struct centralizes defaults and validation, keeping `main.go` readable.

**Alternatives considered**:
- Viper or `kelseyhightower/envconfig`. Rejected: AGENTS.md explicitly lists Viper as "do not reach for"; the M1 config surface is small enough that stdlib `os.Getenv` + a handwritten `Load()` is shorter than any library wrapper.
- YAML/TOML config file with env override. Rejected by FR-013.

---

## Decision 3 — Event dispatcher routing

**Decision**: Static. Compile-time map of channel → handler constructed in `cmd/supervisor/main.go` and frozen at `events.NewDispatcher` construction.

**Rationale**:
- M1 has exactly one channel (`work.ticket.created`); dynamic registration solves no problem here.
- Static routing makes FR-014's "clear error at startup if a received notification's channel has no registered handler" enforceable at the route-table cross-check step, turning what would be a runtime bug into a boot-time failure.
- Keeps the dispatcher dependency-free (no reflection, no plugin loading).

**Alternatives considered**:
- Dynamic runtime registration with a `Register(channel, handler)` method. Rejected: adds a race surface (register-after-dispatch-started) for zero M1 benefit.
- Reflection-based handler discovery. Rejected: overkill and opaque.

---

## Decision 4 — sqlc layout

**Decision**: SQL under repo-root `migrations/` (schema `.sql` files + `queries/` subdirectory grouped by domain). sqlc config at `supervisor/sqlc.yaml`. Generated Go code lands in `supervisor/internal/store` and is tracked in git.

**Rationale**:
- AGENTS.md "Repository layout (target)" places `migrations/` at repo root because a future Drizzle-based dashboard will derive TS types from the same SQL. Putting queries outside `supervisor/` honors that.
- Domain-grouped query files (`departments.sql`, `tickets.sql`, `event_outbox.sql`, `agent_instances.sql`) match how the supervisor code accesses them — each internal package touches one or two files only.
- Committing generated code means `go build` works from a clean clone without sqlc installed; CI regenerates and diffs to catch drift.

**Alternatives considered**:
- Queries under `supervisor/internal/store/queries/`. Rejected: blocks sharing with the future TS side.
- Generated code in `.gitignore` and regenerated in CI. Rejected: breaks `go build` on clean clones, which violates the "six-line Dockerfile" ethos.

**M1 queries** (listed in the plan; enumerated here for traceability):

| Group | Queries |
|-------|---------|
| `departments` | `GetDepartmentByID`, `InsertDepartment` |
| `tickets` | `GetTicketByID`, `InsertTicket` |
| `event_outbox` | `GetEventByID`, `SelectUnprocessedEvents`, `LockEventForProcessing`, `MarkEventProcessed` |
| `agent_instances` | `InsertRunningInstance`, `UpdateInstanceTerminal`, `CountRunningByDepartment`, `RecoverStaleRunning` |

---

## Decision 5 — Migration tooling

**Decision**: goose, embedded as a library via `github.com/pressly/goose/v3`. `--migrate` is an in-binary subcommand; no separate goose CLI dependency.

**Rationale**:
- FR-013 already names goose.
- Goose exposes a stable Go API (`goose.UpContext(ctx, db, dir)`) that makes in-binary migrations trivial and avoids shipping a separate CLI.
- M1 target deployments (Hetzner + Coolify) are happier with one binary than two.

**Alternatives considered**:
- tern. Rejected: no as-clean Go library API; typically invoked as a separate binary.
- Raw `database/sql` migrations in-house. Rejected: reinventing version tracking and concurrency safety for no gain.

**File naming**: `migrations/<YYYYMMDDHHMMSS>_<slug>.sql` (goose default), with `-- +goose Up` / `-- +goose Down` sentinels.

---

## Decision 6 — Logging fields

**Decision**: `slog.JSONHandler` to stdout only; configurable level (`debug|info|warn|error`, default `info`); baseline fields (`service`, `version`, `pid`) at logger construction; domain fields (`event_id`, `channel`, `ticket_id`, `department_id`, `agent_instance_id`, `stream`, `exit_code`, `exit_signal`) attached via `slog.With` as scope widens.

**Rationale**:
- Containerized deployment → stdout-only is the standard; file destinations complicate Coolify log collection.
- JSON handler is machine-parseable; Coolify's default log viewer renders it cleanly.
- `slog.With`-based scope widening (vs manual field passing) is the idiomatic Go 1.23 pattern and keeps log-site code short.

**Alternatives considered**:
- `slog.TextHandler`. Rejected: human-readable but harder to index in downstream log processors.
- logrus or zap. Rejected by AGENTS.md "do not reach for" list.
- Logging to a file + stdout. Rejected: unnecessary complexity; Coolify handles persistence.

---

## Decision 7 — Subprocess output handling

**Decision**: Line-buffered streaming to slog; one record per stdout/stderr line; `stream` field distinguishes the two; no per-invocation files. Buffer limit 1 MiB; over-long lines are logged with `truncated=true`.

**Rationale**:
- FR-015 pins the line-by-line, one-slog-record-per-line, structured-fields shape verbatim. The plan decides only the mechanical details (buffer size, goroutine topology).
- `bufio.Scanner` is the stdlib primitive that matches this shape; one goroutine per stream is the simplest correct structure.
- 1 MiB cap prevents a pathological subprocess from OOMing the supervisor while still tolerating long JSON blobs that modern tools emit.

**Alternatives considered**:
- Write subprocess output to per-invocation files under `logs/`. Rejected by FR-015.
- Byte-stream the output without line-framing. Rejected: no structure for downstream grep/filtering.

---

## Micro-gap 1 — FR-018 advisory lock key

**Decision**: `0x6761727269736f6e` (decimal `7453010385829626735`) — ASCII "garrison" packed into 8 bytes, used as an `int64` literal to `pg_try_advisory_lock`.

**Rationale**:
- Deterministic; no startup-time hashing step.
- Self-documenting in hex dumps and `pg_locks` output (visible as "garrison" in ASCII).
- No collision risk: advisory locks are namespaced per database, and this is the only lock the supervisor acquires.

**Alternatives considered**:
- Random 64-bit literal. Rejected: less recognizable in diagnostics.
- `hashtext('garrison.supervisor.m1')`. Rejected: extra query at startup for no benefit.

---

## Micro-gap 2 — FR-004 command-template substitution mechanism

**Decision**: Do both — Go-side literal-token replacement of `$TICKET_ID` and `$DEPARTMENT_ID` in the argv produced by shell-splitting `ORG_OS_FAKE_AGENT_CMD`, AND set those same values as environment variables on the subprocess.

**Rationale**:
- Go-side replacement works for any command the operator writes, shell-wrapped or not (e.g. `["/usr/local/bin/fake-agent", "--ticket", "$TICKET_ID"]`).
- Env-var exposure matches the literal m1-context.md example `sh -c 'echo hello from $TICKET_ID; sleep 2'` without regressing it.
- The two mechanisms don't conflict: Go-side replacement happens before the subprocess starts; env vars are additionally available if the template invokes a shell.

**Alternatives considered**:
- Go-side replacement only. Rejected: regresses the example command literal.
- Env vars only. Rejected: requires every operator template to invoke a shell, which FR-004 does not mandate.

---

## Dependencies surfaced by the plan

Two items outside the explicitly locked list deserve retro-note attention:

1. **`github.com/pressly/goose/v3`** — already named by FR-013 and AGENTS.md allows "one of goose or tern"; not a new approval.
2. **`github.com/google/shlex`** — for argv splitting of `ORG_OS_FAKE_AGENT_CMD`. This is **one new dependency outside the locked list**, flagged as an open question in the plan. Resolution: accept per AGENTS.md soft rule (justification: the operator can write shell-style templates with quoted arguments, and a handwritten whitespace-splitter would silently break those), and note in the M1 retro.

No other new dependencies are introduced.
