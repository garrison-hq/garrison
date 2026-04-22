# Getting started

A clean-clone-to-running walkthrough of the M1 supervisor. Every
command below was run against the `001-m1-event-bus` branch and
matches the acceptance evidence at
[`specs/m1-event-bus/acceptance-evidence.md`](../specs/m1-event-bus/acceptance-evidence.md).

## Prerequisites

- **Go 1.25+**. Check with `go version`. The Dockerfile also pins
  Go 1.25-alpine.
- **Docker**. Used for Postgres and (optionally) the supervisor
  image.
- **A POSIX shell.** Tested on Linux (Fedora 43); macOS should work.
- Ports **5432** (Postgres) and **8080** (supervisor `/health`)
  available on localhost.

No Node, no Python, no venv. The supervisor is a single Go binary.

## 1. Clone and build

```bash
git clone https://github.com/garrison-hq/garrison
cd garrison/supervisor
make build
```

`make build` runs `copy-migrations` (stages `../migrations/*.sql` into
`cmd/supervisor/migrations/` so the embed directive can find them)
and then `CGO_ENABLED=0 go build`. The binary lands at
`supervisor/bin/supervisor`.

Check the version subcommand works:

```bash
./bin/supervisor --version
```

## 2. Start Postgres

```bash
docker run -d \
  --name pg-garrison \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=orgos \
  postgres:17
```

Give it ~2 seconds to initialise. Verify:

```bash
docker exec pg-garrison pg_isready -U postgres
```

## 3. Apply migrations

The supervisor binary embeds its own migrations and runs them via
the `--migrate` subcommand.

```bash
export ORG_OS_DATABASE_URL='postgres://postgres:postgres@localhost:5432/orgos?sslmode=disable'
./bin/supervisor --migrate
```

Expected output (slog JSON records):

```
{"level":"INFO","msg":"OK   20260421000001_initial_schema.sql ..."}
{"level":"INFO","msg":"OK   20260421000002_event_trigger.sql ..."}
{"level":"INFO","msg":"goose: successfully migrated database to version: 20260421000002"}
{"level":"INFO","msg":"migrations applied"}
```

Confirm the schema exists:

```bash
docker exec pg-garrison psql -U postgres orgos -c '\dt'
```

You should see `agent_instances`, `departments`, `event_outbox`,
`goose_db_version`, `tickets`.

## 4. Insert a department

Cap = 2 means at most two subprocesses run simultaneously for the
engineering department.

```bash
docker exec pg-garrison psql -U postgres orgos -c \
  "INSERT INTO departments (slug, name, concurrency_cap)
   VALUES ('engineering', 'Engineering', 2);"
```

## 5. Run the supervisor

```bash
export ORG_OS_DATABASE_URL='postgres://postgres:postgres@localhost:5432/orgos?sslmode=disable'
export ORG_OS_FAKE_AGENT_CMD='sh -c "echo hello from $TICKET_ID; sleep 2"'
./bin/supervisor
```

Expected startup sequence:

```
{"level":"INFO","msg":"supervisor starting","version":"dev"}
{"level":"INFO","msg":"config loaded", ...}
{"level":"INFO","msg":"connected to Postgres"}
{"level":"INFO","msg":"advisory lock acquired"}
{"level":"INFO","msg":"health server listening","addr":"0.0.0.0:8080"}
{"level":"INFO","msg":"startup recovery ran","reconciled":0}
{"level":"INFO","msg":"initial fallback poll ran"}
{"level":"INFO","msg":"LISTEN started","channel":"work.ticket.created"}
```

In another terminal:

```bash
curl -s localhost:8080/health
# -> 200
```

## 6. Insert a ticket

```bash
docker exec pg-garrison psql -U postgres orgos -c \
  "INSERT INTO tickets (department_id, objective)
   SELECT id, 'hello-world' FROM departments WHERE slug = 'engineering';"
```

Within ~1 second the supervisor should log a subprocess spawn,
receive its stdout line (`hello from <ticket-id>`), wait 2 seconds
for sleep to complete, and then write a terminal row.

Verify:

```bash
docker exec pg-garrison psql -U postgres orgos -c \
  "SELECT t.objective, a.status, a.exit_reason
     FROM tickets t JOIN agent_instances a ON a.ticket_id = t.id;"
```

```
  objective  |  status   | exit_reason
-------------+-----------+-------------
 hello-world | succeeded | exit_code_0
```

And the outbox row is marked processed:

```bash
docker exec pg-garrison psql -U postgres orgos -c \
  "SELECT channel, processed_at IS NOT NULL AS processed
     FROM event_outbox;"
```

## 7. Graceful shutdown

Send SIGTERM (Ctrl+C in the supervisor terminal, or `docker kill`
if running under Docker). The supervisor logs
`shutdown signal received; cancelling root context`, waits for any
in-flight subprocess (up to `ORG_OS_SHUTDOWN_GRACE`, default `30s`),
writes the terminal row, and exits 0.

## Running under Docker

Instead of `make build`, build the image:

```bash
cd supervisor
make docker   # produces garrison/supervisor:dev
```

Then run it on the same Docker network as Postgres. See
[`specs/m1-event-bus/acceptance-evidence.md`](../specs/m1-event-bus/acceptance-evidence.md)
for the full 10-step sequence used for M1 acceptance.

## Running the tests

```bash
cd supervisor
make test                # unit tests (~1s)
make test-integration    # full integration suite (~15s — spins Postgres)
make test-chaos          # reconnect + SIGKILL + shutdown-with-inflight (~8s)
```

Integration and chaos suites need Docker running so
`testcontainers-go` can start Postgres containers. No DB mocking.

## Common problems

- **`advisory lock already held`** (exit code 4): another supervisor
  instance is running against the same database. Stop it first.
- **`/bin/sh: not found` in subprocess log**: your
  `ORG_OS_FAKE_AGENT_CMD` references `sh -c ...` but the runtime
  image has no shell. The shipped Dockerfile uses `alpine:3.20`
  precisely to provide `/bin/sh`. If you rebuilt from distroless,
  switch the base back.
- **Migrations fail with `permission denied`**: check the DSN's
  user has `CREATE` on the database. The compose default
  (`postgres` superuser on `orgos`) is fine.
- **`/health` returns 503**: either the DB ping failed or the most
  recent poll timestamp is older than `2·ORG_OS_POLL_INTERVAL`.
  Check the supervisor logs for `poll cycle failed` or
  `reconnect backoff`.

## What's next

Once the supervisor is running locally, the useful next reads are:

- [M1 retro](./retros/m1.md) — what was surprising while shipping
  this.
- [`ARCHITECTURE.md`](../ARCHITECTURE.md) — the full system picture
  (including the milestones after M1).
- [`RATIONALE.md`](../RATIONALE.md) — why the design choices above
  were made.
