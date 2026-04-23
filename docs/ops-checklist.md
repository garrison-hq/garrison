# Operations checklist

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

Post-migrate and post-deploy steps operators must run after bringing Garrison up on a new environment or applying migrations. This document is updated at every milestone ship; the canonical shape is "here is what you must do that the migration itself cannot or should not do."

**Last updated**: 2026-04-22, after M2.1 shipped.

---

## Why this file exists

Certain setup actions cannot live inside migrations or Dockerfiles:

- **Secrets that the migration itself cannot know** (role passwords, bootstrap keys) — the migration creates the role but cannot set a password without exposing it in the migration history.
- **Operator-specific configuration** (palace paths, deployment-specific overrides) that varies per environment.
- **One-time bootstrap actions** that should run exactly once per environment (key generation, initial seeding).

Rather than scattering these as comments across migrations or as inline notes in retros, they live here. Every milestone retro updates this file if new post-migrate discipline is introduced.

---

## Universal pre-deploy checks

Before deploying any Garrison supervisor for the first time in a new environment:

1. Postgres is reachable from the supervisor's network.
2. The Postgres superuser has privileges to create roles.
3. The supervisor's environment has `GARRISON_DATABASE_URL` set to the superuser DSN (for migration runs) or a user with migration privileges.
4. `goose up` has been run to the latest migration against the target database.

---

## Per-milestone post-migrate steps

After `goose up` completes, run the steps for every milestone that has shipped to the target environment. Steps for unshipped milestones do not apply.

### M1 (shipped 2026-04-22)

No post-migrate steps. M1's schema is self-contained; no scoped roles introduced.

### M2.1 (shipped 2026-04-22)

**1. Set the `garrison_agent_ro` password.**

The M2.1 migration creates the role `garrison_agent_ro` with `LOGIN` privilege but no password. The supervisor reads the password from the `GARRISON_AGENT_RO_PASSWORD` env var and composes it into a DSN at startup. Without the password, `internal/pgmcp` cannot authenticate and every Claude subprocess will fail at MCP init.

**Step:**

```sql
-- Against the Garrison database, as a superuser:
ALTER ROLE garrison_agent_ro PASSWORD '<generate a strong password>';
```

Then set `GARRISON_AGENT_RO_PASSWORD='<same password>'` in the supervisor's environment. Restart the supervisor.

**Verification:**

```bash
# From the supervisor's host, as the same user the supervisor runs as:
PGPASSWORD="$GARRISON_AGENT_RO_PASSWORD" psql \
  -h <host> -U garrison_agent_ro -d <db> \
  -c 'SELECT 1;'
# Should return 1. If it errors with "password authentication failed", the ALTER
# ROLE didn't take or the env var doesn't match.
```

**Why not in the migration**: the password is a secret. Storing it in the migration files commits it to the repo and to every developer's clone. Storing it in an env var scoped to the host that runs the supervisor keeps it out of source control. M2.3's Infisical integration will eventually generalize this pattern; until then, env vars are the simplest correct answer.

---

### M2.2 (applicable once the M2.2 migration runs)

M2.2 introduces a sidecar + socket-proxy topology (see [`supervisor/docker-compose.yml`](../supervisor/docker-compose.yml) and [`docs/security/vault-threat-model.md`](./security/vault-threat-model.md) §"M2.2 deployment assumptions"). The palace lives inside the `garrison-mempalace` container on a Docker-named volume; the supervisor talks to it via filtered `docker exec` through `garrison-docker-proxy`. Post-migrate, the operator owns four steps:

**1. Set the `garrison_agent_mempalace` password.**

Parallel to M2.1's `garrison_agent_ro`. The M2.2 migration creates the role without a password; operators set it post-migrate and configure the supervisor to consume it.

```sql
ALTER ROLE garrison_agent_mempalace PASSWORD '<generate a strong password>';
```

Set `GARRISON_AGENT_MEMPALACE_PASSWORD='<same password>'` in the supervisor's environment (via Coolify's env-var store or the operator-owned `.env`). Restart.

**Verification:**

```bash
PGPASSWORD="$GARRISON_AGENT_MEMPALACE_PASSWORD" psql \
  -h <host> -U garrison_agent_mempalace -d <db> \
  -c 'SELECT COUNT(*) FROM ticket_transitions;'
# Returns a number. Permission denied ⇒ grants didn't take.
```

**2. Confirm the three compose services are up before starting the supervisor.**

Order matters: the supervisor's startup runs `mempalace init --yes /palace` through `docker exec`, which requires `garrison-mempalace` + `garrison-docker-proxy` to be reachable.

```bash
docker compose up -d postgres mempalace docker-proxy
docker exec garrison-mempalace mempalace --version   # → MemPalace 3.3.2
# Through the proxy from a sibling container:
docker run --rm --network <compose-net> -e DOCKER_HOST=tcp://garrison-docker-proxy:2375 \
  docker:cli docker exec garrison-mempalace mempalace --version
# Same output. Failure here ⇒ proxy filter or DNS issue; fix before launching supervisor.
```

Then bring the supervisor up: `docker compose up -d supervisor`. Startup log line `palace_initialized=true` confirms the bootstrap succeeded.

**3. Verify no host-side palace artefacts leaked.**

MemPalace's `init` auto-creates `mempalace.yaml` inside the scanned directory. The sidecar + volume topology keeps everything at `/palace` inside the container, but belt-and-braces on the host:

```bash
# Against the Garrison checkout:
git -C /path/to/garrison status --porcelain
# Output MUST be empty. A modified .gitignore or untracked mempalace.yaml
# indicates palace state leaked into the checkout — stop and investigate.
```

SC-213 asserts this invariant; T020 acceptance re-verifies it post-run.

**4. Pin the socket-proxy image by digest for production.**

The committed `docker-compose.yml` uses `ghcr.io/linuxserver/socket-proxy:latest` for dev convenience. For production, substitute a pinned digest:

```yaml
docker-proxy:
  image: ghcr.io/linuxserver/socket-proxy@sha256:<pinned-digest>
```

The operator chooses a digest at release time. Upgrade cadence: re-pin on every socket-proxy security advisory; otherwise once per quarter at most.

---

### M2.3 (not yet shipped — applicable once Infisical integration ships)

**Infisical bootstrap secrets**: Infisical itself requires `ENCRYPTION_KEY` and `AUTH_SECRET` to operate. These are chicken-and-egg secrets that cannot live in Infisical.

Full step list will be added when M2.3 ships. Anticipated shape:

1. Generate `ENCRYPTION_KEY` (32-byte hex) and `AUTH_SECRET` (32-byte base64).
2. Store in Coolify's (or equivalent orchestration layer's) secret environment variables.
3. Verify Infisical starts and the Garrison dashboard can authenticate.
4. Bootstrap Garrison's Machine Identity for the supervisor and dashboard.
5. Configure the supervisor's `INFISICAL_CLIENT_ID` and `INFISICAL_CLIENT_SECRET`.

The pattern parallels M2.1 and M2.2: the migration/config cannot contain the secret; operators set it post-deploy via env vars scoped to the running host.

---

## Runbooks

### Recovering from a missed post-migrate step

If the supervisor won't start with an authentication error for a scoped role:

1. Identify which role failed from the supervisor's log.
2. Run the corresponding `ALTER ROLE ... PASSWORD` from this document.
3. Confirm the env var matches.
4. Restart the supervisor.

If the supervisor crashes on startup with "palace path not found":

1. Check `GARRISON_PALACE_PATH` is set and pointing to a writable directory.
2. Check the path is not inside a git-tracked tree.
3. Either manually `mempalace init --yes "$PATH"` or let the supervisor's startup sequence handle it.

### Rotating a scoped role password

Periodically (or on suspected compromise):

1. Generate a new password.
2. `ALTER ROLE <role_name> PASSWORD '<new password>';` on the database.
3. Update the supervisor's env var to the new password.
4. Restart the supervisor. Downtime is the single restart interval.

In-flight agent_instances at the moment of restart will be marked `failed` with `exit_reason = "supervisor_shutdown"` per M2.1's graceful shutdown contract. Their tickets return to `todo` on the next restart cycle (supervisor re-picks up unprocessed events via the `processed_at` fallback poll).

---

## Changelog

- **2026-04-22**: Initial version. M2.1's `garrison_agent_ro` password discipline codified. M2.2 and M2.3 sections sketched based on planned milestone designs.