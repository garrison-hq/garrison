# Quickstart: M1 supervisor

**Audience**: operators and contributors running the supervisor locally for the first time.
**Binding context**: this file documents the build/test/run flow for the structure defined in [`plan.md`](./plan.md). It does not introduce new decisions.

## Prerequisites

- Go 1.23+
- Docker (for the Postgres container in integration tests and the release image build)
- A reachable PostgreSQL 17+ instance for local daemon runs — either a Docker container you manage or an existing dev database

No global `sqlc`, `goose`, or `golangci-lint` install is required to build or run. sqlc-generated code is committed. Goose is embedded via `--migrate`.

## First build

From `supervisor/`:

```sh
make build
```

Produces `supervisor/bin/supervisor`, a static binary (`CGO_ENABLED=0`). The build embeds the version via `-ldflags="-X main.version=..."` when run under the Docker build; local `make build` leaves `main.version` at its default (`"dev"`).

Verify the build:

```sh
./bin/supervisor --version   # prints "supervisor vX.Y.Z" (or "dev")
./bin/supervisor --help      # prints usage + env-var table
```

## Database setup

### Option A — ephemeral container

```sh
docker run -d --name garrison-pg \
  -e POSTGRES_PASSWORD=garrison \
  -e POSTGRES_USER=garrison \
  -e POSTGRES_DB=garrison \
  -p 5432:5432 \
  postgres:17
```

Then:

```sh
export GARRISON_DATABASE_URL='postgres://garrison:garrison@localhost:5432/garrison?sslmode=disable'
```

### Option B — existing dev database

Set `GARRISON_DATABASE_URL` to any reachable Postgres 17+ URL.

## Migrations

```sh
make migrate
```

This runs `./bin/supervisor --migrate`, which applies the two migrations from [`migrations/`](../../migrations/):

1. `20260421000001_initial_schema.sql` — `departments`, `tickets`, `event_outbox`, `agent_instances`, and the two partial indexes.
2. `20260421000002_event_trigger.sql` — `emit_ticket_created` and the `ticket_created_emit` trigger.

Exit codes: `0` on success, `3` on any migration error. No destructive operations on `--migrate`; running it twice is safe (goose tracks applied versions in a `goose_db_version` table).

## Seed a department

The supervisor does not insert departments — the operator does. Minimal seed for a local run:

```sql
INSERT INTO departments (name, slug, concurrency_cap)
VALUES ('Engineering', 'eng', 2);
```

## Run the daemon

Required env vars:

```sh
export GARRISON_DATABASE_URL='postgres://garrison:garrison@localhost:5432/garrison?sslmode=disable'
export GARRISON_FAKE_AGENT_CMD='sh -c "echo hello from $TICKET_ID; sleep 2"'
```

Optional env vars (defaults match [`contracts/cli.md`](./contracts/cli.md)):

```sh
export GARRISON_POLL_INTERVAL=5s
export GARRISON_SUBPROCESS_TIMEOUT=60s
export GARRISON_SHUTDOWN_GRACE=30s
export GARRISON_HEALTH_PORT=8080
export GARRISON_LOG_LEVEL=info
```

Then:

```sh
make run
```

Expected startup sequence in logs (one JSON line per record):

1. config loaded
2. connected to Postgres
3. advisory lock acquired
4. recovery ran (typically 0 rows reconciled on a clean DB)
5. initial fallback poll ran
6. LISTEN on `work.ticket.created` started
7. health server listening on `:8080`

`curl http://localhost:8080/health` returns `200` once step 5 has written `LastPollAt`. It returns `503` before that, and any time a subsequent ping fails or `time.Since(LastPollAt) > 2 * GARRISON_POLL_INTERVAL`.

## Fire an event

In a second shell against the same database:

```sql
INSERT INTO tickets (department_id, title)
VALUES ((SELECT id FROM departments WHERE slug='eng'), 'first ticket');
```

Observe, in the supervisor's stdout:

- `event_outbox` row logged with `channel="work.ticket.created"`
- subprocess `started_at` record with `agent_instance_id`
- subprocess stdout lines as `stream="stdout"` records
- subprocess exit record with `exit_reason="exit_code_0"` and `status="succeeded"`
- `event_outbox.processed_at` update committed atomically with the terminal `agent_instances` row

Verify in the DB:

```sql
SELECT status, exit_reason, finished_at FROM agent_instances ORDER BY started_at DESC LIMIT 1;
SELECT processed_at FROM event_outbox ORDER BY created_at DESC LIMIT 1;
```

## Tests

### Unit tests

```sh
make test
```

Runs every `*_test.go` without build tags. No Postgres needed.

### Integration tests (require Docker)

```sh
make test-integration
```

Runs tests under the `integration` build tag. Each test boots a testcontainers-go Postgres container, runs `--migrate` against it, exercises the target flow, and tears the container down. Expected duration: a few seconds per test for setup, dominated by the initial image pull.

### Chaos tests (require Docker)

```sh
make test-chaos
```

Runs tests under the `chaos` build tag. These inject faults (paused Postgres container, SIGKILL on spawned subprocesses, SIGTERM mid-flight) and assert the supervisor recovers.

The full suite — unit + integration + chaos — is what CI will run against every change that touches `supervisor/` or `migrations/`.

## Shutdown

`Ctrl-C` in the `make run` shell sends SIGINT, which is handled identically to SIGTERM (graceful shutdown per [`contracts/cli.md`](./contracts/cli.md)).

Expected shutdown behaviour:

- Root context cancelled.
- Each in-flight subprocess receives SIGTERM; if still alive after 5s, SIGKILL.
- `/health` begins refusing connections (server enters graceful shutdown for `GARRISON_SHUTDOWN_GRACE`).
- Process exits `0` if no SIGKILL escalations occurred, `5` if any subprocess required SIGKILL.

## Docker image

```sh
make docker
```

Produces `garrison/supervisor:dev`. The image is distroless-static and contains only the binary; it exposes no default port (the container orchestrator maps `GARRISON_HEALTH_PORT`). Run:

```sh
docker run --rm -e GARRISON_DATABASE_URL=... -e GARRISON_FAKE_AGENT_CMD=... -p 8080:8080 garrison/supervisor:dev
```

For `--migrate` mode:

```sh
docker run --rm -e GARRISON_DATABASE_URL=... garrison/supervisor:dev --migrate
```

## Regenerating sqlc code

Only needed when SQL queries in `migrations/queries/` change. Requires the sqlc CLI installed locally.

```sh
make sqlc
```

Commits the regenerated files under `supervisor/internal/store/` to the branch.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Exit code `4` on startup | Another supervisor holds the FR-018 advisory lock on the same DB | Stop the other instance; advisory locks release when the session closes. |
| Exit code `3` on `--migrate` | Migration SQL error | Read the error; inspect `goose_db_version` table. |
| `/health` stuck at 503 | Initial connect retry in progress, or Postgres unreachable | Check `GARRISON_DATABASE_URL`; look at supervisor log records for connect errors. |
| Events stay with `processed_at IS NULL` | Cap is 0 for that department, or all running slots occupied | Raise `departments.concurrency_cap`; fallback poll will pick up deferred events. |
| Subprocess stuck, eventually timeout | `GARRISON_SUBPROCESS_TIMEOUT` reached | Expected; row becomes `status='timeout'`, `exit_reason='timeout'`. |
