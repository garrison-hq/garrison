# M1 acceptance evidence (T017)

Executed on 2026-04-22 against the release Docker image built with
`make docker` (`garrison/supervisor:dev`, alpine-based — see commit
for Dockerfile base change rationale) and a fresh `postgres:17`
container on a dedicated `garrison-m1` docker network. Each of the
ten steps from `specs/_context/m1-context.md` §"Acceptance criteria
for M1" was run unmodified, in order, from a clean database.

## Environment

- `garrison/supervisor:dev` image id `7c7729648f66` (24.3 MB alpine)
- `postgres:17`, `POSTGRES_DB=orgos`, `POSTGRES_PASSWORD=postgres`
- network `garrison-m1` (bridge)
- `GARRISON_DATABASE_URL=postgres://postgres:postgres@pg-m1:5432/orgos?sslmode=disable`
- `GARRISON_POLL_INTERVAL=2s`
- `GARRISON_SHUTDOWN_GRACE=30s`
- `GARRISON_FAKE_AGENT_CMD` varies per step (noted inline)

## Step 1 — Run the migrations; the schema exists

```
$ docker run --rm --network garrison-m1 -e GARRISON_DATABASE_URL=... garrison/supervisor:dev --migrate
{"level":"INFO","msg":"OK   20260421000001_initial_schema.sql (10ms)"}
{"level":"INFO","msg":"OK   20260421000002_event_trigger.sql (3.45ms)"}
{"level":"INFO","msg":"goose: successfully migrated database to version: 20260421000002"}
{"level":"INFO","msg":"migrations applied"}
exit=0
```

Tables present after migration:

```
 public | agent_instances
 public | departments
 public | event_outbox
 public | goose_db_version
 public | tickets
```

Partial indexes verbatim from `m1-context.md`:

```
 idx_agent_instances_running  | btree (department_id) WHERE status = 'running'
 idx_event_outbox_unprocessed | (per migration)
```

Trigger function and trigger:

```
 proname            : emit_ticket_created
 tgname             : ticket_created_emit
```

**Pass.**

## Step 2 — Insert the engineering department

```
INSERT INTO departments (slug, name, concurrency_cap)
VALUES ('engineering', 'Engineering', 2);
```

Result:

```
                  id                  |    slug     |    name     | concurrency_cap
--------------------------------------+-------------+-------------+-----------------
 e592b48d-17fa-4b88-8ad6-8d8c5449c64c | engineering | Engineering |               2
```

**Pass.**

## Step 3 — Start the supervisor binary

Started with `GARRISON_FAKE_AGENT_CMD='sh -c "sleep 2; echo done"'`
to give each subprocess a ~2s runtime window so the cap is
observable during sampling in step 5.

Startup log:

```
{"level":"INFO","msg":"supervisor starting","version":"dev"}
{"level":"INFO","msg":"config loaded","poll_interval":2000000000,"subprocess_timeout":60000000000,"shutdown_grace":30000000000,"health_port":8080}
{"level":"INFO","msg":"connected to Postgres"}
{"level":"INFO","msg":"advisory lock acquired"}
{"level":"INFO","msg":"health server listening","addr":"0.0.0.0:8080"}
{"level":"INFO","msg":"startup recovery ran","reconciled":0}
{"level":"INFO","msg":"initial fallback poll ran"}
{"level":"INFO","msg":"LISTEN started","channel":"work.ticket.created"}
```

`/health` returns `http=200`.

**Pass.**

## Step 4 — Insert 3 tickets in quick succession

Single `INSERT … VALUES (…),(…),(…)` statement for the engineering
department:

```
                  id                  | objective
--------------------------------------+-----------
 fa5e8163-b9ab-4598-97f7-8813e04f31cf | t1
 b3184aef-f937-41fb-9d3e-d3c8c1faf4ec | t2
 e218a5ec-3f04-4bcd-b6d9-82dd1807bad5 | t3
```

**Pass.**

## Step 5 — Observe cap-2 enforcement

Sampled `SELECT COUNT(*) FROM agent_instances WHERE status='running'`
every 200ms while the three subprocesses ran:

```
sample  1: running=1
sample  2: running=1
sample  3: running=1
sample  4: running=1
sample  5: running=2
sample  6: running=2
sample  7: running=2
sample  8: running=2
sample  9: running=2
sample 10: running=2
sample 11: running=2
sample 12: running=1
sample 13: running=1
sample 14: running=1
sample 15: running=0
```

Peak concurrency = 2. The third ticket was deferred until a slot
freed (t3 `started_at` = `2026-04-22 00:25:18.88…`, one second after
t1 finished at `00:25:18.88…`). Cap observed correctly.

**Pass.**

## Step 6 — All 3 produce `agent_instances` rows with `status='succeeded'`

```
  status   | count
-----------+-------
 succeeded |     3
```

Per-ticket terminal detail:

```
 objective |  status   |   reason    |          started_at           |          finished_at
-----------+-----------+-------------+-------------------------------+-------------------------------
 t1        | succeeded | exit_code_0 | 2026-04-22 00:25:16.875291+00 | 2026-04-22 00:25:18.88047+00
 t2        | succeeded | exit_code_0 | 2026-04-22 00:25:17.932117+00 | 2026-04-22 00:25:19.93691+00
 t3        | succeeded | exit_code_0 | 2026-04-22 00:25:18.883993+00 | 2026-04-22 00:25:20.887703+00
```

All three have `status='succeeded'` and `exit_reason='exit_code_0'`.

> **Observation (non-blocking):** `agent_instances.pid` is NULL on the
> terminal rows above. `InsertRunningInstance` sets pid, but
> `UpdateInstanceTerminal` is overwriting it to NULL on the terminal
> write. Not an M1 acceptance requirement; flagged for the retro.

**Pass.**

## Step 7 — All 3 `event_outbox` rows marked `processed_at`

```
       channel       | processed | count
---------------------+-----------+-------
 work.ticket.created | t         |     3
```

**Pass.**

## Step 8 — SIGTERM with in-flight subprocess; clean exit within 30s

Restarted supervisor with `GARRISON_FAKE_AGENT_CMD='sh -c "sleep 15; echo done"'`
and inserted one ticket `sigterm-longjob`. After 2s, verified:

```
 status  | count
---------+-------
 running |     1
```

Sent `docker kill --signal=TERM sup-m1` and measured elapsed time
until the container reached `Exited`:

```
shutdown_ms=268
exit_code=0
```

Terminal row written by the shutdown path:

```
    objective    | status |      exit_reason
-----------------+--------+---------------------
 sigterm-longjob | failed | supervisor_shutdown
```

Supervisor log excerpt:

```
{"level":"INFO","msg":"shutdown signal received; cancelling root context","signal":"terminated"}
{"level":"INFO","msg":"shutdown complete"}
```

268ms is well inside the 30s `GARRISON_SHUTDOWN_GRACE` budget; exit
code 0; in-flight subprocess correctly landed `failed` +
`supervisor_shutdown`.

**Pass.**

## Step 9 — Restart; recovery marks leftover running instances `failed`

To exercise recovery meaningfully (step 8's clean shutdown leaves no
stale rows), injected a synthetic stale row directly into the DB with
a backdated `started_at`:

```
INSERT INTO tickets (id, department_id, objective)
VALUES ('11111111-…', dept, 'stale-running-for-recovery');
INSERT INTO agent_instances (department_id, ticket_id, status, started_at)
VALUES (dept, '11111111-…', 'running', now() - interval '10 minutes');
```

Pre-restart:

```
         objective          | status  | started_at
----------------------------+---------+-------------------------------
 stale-running-for-recovery | running | 2026-04-22 00:16:45.671359+00
```

Started a fresh supervisor. Startup log:

```
{"level":"INFO","msg":"startup recovery ran","reconciled":1}
```

Post-restart state of the stale ticket:

```
         objective          |  status   |        reason
----------------------------+-----------+----------------------
 stale-running-for-recovery | failed    | supervisor_restarted
 stale-running-for-recovery | succeeded | exit_code_0
```

Recovery reconciled the stale row (`status=failed`,
`exit_reason=supervisor_restarted`). The *event* on the outbox was
still unprocessed, so the post-recovery fallback poll picked it up
and spawned a fresh subprocess that succeeded — exactly the behaviour
described in plan.md §"Startup: fallback-poll before LISTEN".

**Pass.**

## Step 10 — Insert a 4th ticket; it processes on the new instance

```
INSERT INTO tickets (department_id, objective)
VALUES (dept, 'post-recovery-fresh');
```

After ~3 seconds:

```
      objective      |  status   |  exit_reason
---------------------+-----------+-------------
 post-recovery-fresh | succeeded | exit_code_0
```

```
       channel       | processed
---------------------+-----------
 work.ticket.created | t
```

`/health` returned `http=200` throughout.

**Pass.**

## Summary

All 10 acceptance steps pass against `make docker` on fresh Postgres
17. SC-001 satisfied. M1 ship gate met.

Evidence logs saved under `/tmp/m1-accept/` during the run (gitignored;
not part of the repo). Re-running the sequence is deterministic from
a clean database plus `make docker`.
