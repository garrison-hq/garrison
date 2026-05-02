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

### M2.3 — Infisical deployment

M2.3 adds Infisical as the vault backend. Three new services (`infisical-postgres`, `infisical-redis`, `infisical`) join the compose topology. The supervisor gains three new env vars: `GARRISON_INFISICAL_ADDR`, `GARRISON_INFISICAL_CLIENT_ID`, `GARRISON_INFISICAL_CLIENT_SECRET`. All seven steps below are required before the supervisor can serve any vault-gated spawn.

For architectural rationale see [docs/security/vault-threat-model.md](./security/vault-threat-model.md) Rules 1–7.

**1. Bootstrap secret generation**

Generate three secrets. Store them in your operator password manager. **Never commit them to the repository or any `.env` file that is checked in.**

```bash
# ENCRYPTION_KEY — 32-byte base64. Used by Infisical to encrypt secrets at rest.
openssl rand -base64 32

# AUTH_SECRET — 32-byte base64. Used by Infisical for session signing.
openssl rand -base64 32

# Infisical Postgres password — separate from garrison-postgres.
openssl rand -base64 24
```

Set the following in your deployment environment (Coolify → Environment Variables, or `.env.local` for local dev only):

```
GARRISON_INFISICAL_ENCRYPTION_KEY=<result of first openssl rand>
GARRISON_INFISICAL_AUTH_SECRET=<result of second openssl rand>
GARRISON_INFISICAL_PG_PASSWORD=<result of third openssl rand>
```

**2. Image digest pinning**

`docker-compose.yml` defaults to `infisical/infisical:latest`. For any environment beyond a developer laptop, pin by digest:

```bash
docker pull infisical/infisical:<version>
docker inspect --format='{{index .RepoDigests 0}}' infisical/infisical:<version>
# → infisical/infisical@sha256:<digest>
```

Set `GARRISON_INFISICAL_IMAGE=infisical/infisical@sha256:<digest>` in your deployment environment before deploying. Record the version and digest in the M2.3 acceptance evidence for audit purposes.

**3. Post-deploy Machine Identity creation**

After `docker compose up` (or Coolify deploy) brings all seven services healthy:

1. Open the Infisical UI at your deployment's internal URL.
2. Create the admin account on first login (Infisical's setup wizard).
3. Create a Project for Garrison.
4. Create two Machine Identities:
   - `garrison-supervisor` — Universal Auth, read-only scope on the paths the supervisor needs (e.g. `/<customer_id>/operator/*`).
   - `garrison-dashboard` — Universal Auth, read + write scope. Park the credentials in your password manager until M4 ships.
5. For each ML, generate a client_id and client_secret. Copy both into your password manager.
6. Set in your deployment environment:

```
GARRISON_INFISICAL_CLIENT_ID=<garrison-supervisor client_id>
GARRISON_INFISICAL_CLIENT_SECRET=<garrison-supervisor client_secret>
GARRISON_INFISICAL_PROJECT_ID=<project id from Infisical UI>
GARRISON_INFISICAL_ENVIRONMENT=<environment slug, e.g. "production">
```

7. Restart the supervisor service. Check logs for `"vault client initialized"` — the supervisor logs this at `INFO` on successful startup.

**4. Seeding an initial secret**

The supervisor reads secrets by path. After seeding in Infisical, register it in Garrison's `secret_metadata` table:

1. In the Infisical UI (or `infisical` CLI with the dashboard ML), add the secret at path `/<customer_id>/<provenance>/<name>`. Example: `/a1b2c3d4.../operator/GITHUB_TOKEN`.

2. Register it in Garrison's Postgres:

```sql
INSERT INTO secret_metadata
  (secret_path, customer_id, provenance, rotation_cadence, last_rotated_at)
VALUES
  ('/<customer_id>/operator/GITHUB_TOKEN',
   '<customer-uuid>',
   'operator_entered',
   '90 days',
   now());
```

This hand-sync path exists until M4 automates the registration flow.

**5. Adding a grant**

A grant ties a secret to an agent role. Grants are database-managed, not Infisical-managed.

1. Create a migration file: `migrations/<timestamp>_m2_3_grant_<role>_<name>.sql`.

2. Insert the grant:

```sql
INSERT INTO agent_role_secrets (role_slug, env_var_name, secret_path, customer_id, granted_by)
VALUES ('engineer', 'GITHUB_TOKEN', '/<customer_id>/operator/GITHUB_TOKEN', '<customer-uuid>', 'operator');
```

3. The `rebuild_secret_metadata_role_slugs` trigger fires automatically and updates `secret_metadata.allowed_role_slugs`.

4. Ship via PR and apply via `goose up` per normal migration discipline.

**6. ML credential rotation**

To rotate `garrison-supervisor`'s client_secret without downtime:

1. In the Infisical UI, navigate to `garrison-supervisor` → Client Secrets → Add New.
2. Copy the new client_secret.
3. Set `GARRISON_INFISICAL_CLIENT_SECRET=<new_secret>` in your deployment environment (Coolify).
4. Trigger a supervisor restart (Coolify → Redeploy or the equivalent for your setup).
5. Verify logs show `"vault client initialized"` on restart.
6. Delete the old client_secret in the Infisical UI.

In-flight agent_instances during the restart follow the same `supervisor_shutdown` contract as M2.1.

**7. Vault-table access policy**

The `garrison_agent_ro` role has **no** grant on `vault_access_log`, `agent_role_secrets`, or `secret_metadata` (FR-412 consequence of Rule 3). This is intentional: Claude subprocesses must never be able to read audit records or grants.

- Ad-hoc queries: use the supervisor's primary connection (DSN in `GARRISON_DATABASE_URL`) or connect as the DB owner via `psql`.
- M3 dashboard reads: the M3 milestone will introduce a dedicated read-only role scoped to the vault tables.
- Operators debugging a spawn: `SELECT * FROM vault_access_log WHERE ticket_id='<uuid>' ORDER BY accessed_at DESC LIMIT 20;` as the DB owner.

---

## M3 — operator dashboard

The M3 milestone ships a Next.js 16 dashboard as a fourth container
alongside supervisor + mempalace + socket-proxy. Reads M2-arc data
through two new dashboard-scoped Postgres roles. No agent-facing
role is touched.

**1. Goose migration ordering**

Run `goose up` BEFORE `bun run drizzle:migrate`. The goose migrations
include `20260426000010_m3_dashboard_roles.sql` and
`20260426000011_m3_dashboard_dept_grants.sql`, which create the two
dashboard-scoped roles + their SELECT grants on supervisor-owned
tables. Drizzle migrations land the dashboard-owned schema (better-
auth tables + `operator_invites`) on top.

```sh
# from the supervisor directory
goose -dir ../migrations postgres "${SUPERVISOR_DSN}" up
```

**2. Role passwords + LOGIN**

Both M3 roles ship as `NOINHERIT NOLOGIN`. Set passwords + flip
LOGIN at deployment time, mirroring the M2.2 `garrison_agent_mempalace`
procedure.

```sql
-- as the DB owner
ALTER ROLE garrison_dashboard_app WITH LOGIN PASSWORD '<random>';
ALTER ROLE garrison_dashboard_ro  WITH LOGIN PASSWORD '<random>';
```

Persist both passwords to your operator secret store (NOT
Infisical — Infisical is the agent-facing vault per the M2.3 threat
model). The dashboard reads them at startup via `DASHBOARD_APP_DSN`
and `DASHBOARD_RO_DSN` (see FR-002a–f and FR-021).

**3. `BETTER_AUTH_SECRET` generation**

```sh
openssl rand -hex 32
```

Persist the value to the operator secret store and surface it to
the dashboard container as `BETTER_AUTH_SECRET`. Rotation
invalidates every existing operator session — generate + rotate
during a low-traffic window.

**4. Drizzle migration application**

```sh
cd dashboard
bun install
DASHBOARD_APP_DSN=<owner-dsn-or-app-role-dsn> bun run drizzle:migrate
```

The migration emits the five dashboard-owned tables (users,
sessions, accounts, verifications, operator_invites) and a
trailing GRANT block that gives `garrison_dashboard_app` CRUD on
each. The grant block is appended automatically by
`drizzle/scripts/append-grants.ts`; if you regenerate the
migration locally, re-run `bun run drizzle:generate` to re-append.

**5. Dashboard image digest pinning**

```sh
docker build -t garrison-dashboard:dev dashboard/
docker images --digests garrison-dashboard:dev
```

Record the digest in your deployment notes. The runtime image is
≤250 MB (verified at T019: 217 MB) and runs `node server.js`
against the Next.js standalone output as the non-root `dashboard`
user. Mirrors the M2.3 ops-checklist Infisical-image-pinning
pattern.

**6. First-run walkthrough**

1. Bring up the compose stack: `docker compose up -d` from
   `supervisor/`.
2. Visit `http://localhost:3000` (or whatever
   `BETTER_AUTH_URL` points at).
3. The middleware redirects to `/setup` because the `users` table
   is empty — fill in name + email + password + submit.
4. The wizard auto-redirects to `/login`; sign in with the same
   credentials.
5. The org overview at `/` renders against the M2-arc data the
   supervisor is producing.
6. To invite a second operator, navigate to `/admin/invites`,
   click **Generate invite**, share the link out-of-band. The
   invitee opens the link and creates their account; both
   operators see identical data thereafter (FR-002f).

---

## M4 — operator dashboard mutations

The M4 milestone ships operator-driven mutation surfaces (vault
writes, ticket mutations, agent settings editor) on top of the M3
dashboard. New schema migrations, a parked-since-M2.3 Machine
Identity activation, dashboard-side Infisical SDK wiring, and a
small supervisor-side LISTEN goroutine for cache invalidation.

**1. Goose migration ordering**

Run `goose up` BEFORE `bun run drizzle:pull` regenerates the
supervisor schema TypeScript bindings. The M4 goose migrations
are:

- `20260427000010_m4_supervisor_schema_extensions.sql` —
  `ticket_transitions.suspected_secret_pattern_category` +
  `secret_metadata.rotation_provider` + `vault_access_log.metadata`
- `20260427000011_m4_vault_access_log_optional_agent.sql` —
  relax `vault_access_log.agent_instance_id` to nullable so
  operator-driven vault mutations (no agent_instance_id) can
  audit
- `20260427000012_m4_dashboard_app_vault_write_grants.sql` —
  grant INSERT/UPDATE/DELETE on the three vault tables to
  `garrison_dashboard_app` so the dashboard's server actions
  can write
- `20260427000013_m4_agents_updated_at.sql` —
  `agents.updated_at` column + auto-bump trigger; the agent
  settings editor uses it as the optimistic-lock version token
  per FR-101

```sh
# from the supervisor directory
goose -dir ../migrations postgres "${SUPERVISOR_DSN}" up
```

After this, regenerate the dashboard schema bindings:

```sh
cd dashboard
bun run drizzle:pull
```

**2. `garrison-dashboard` Machine Identity provisioning**

The dashboard authenticates to Infisical as a Machine Identity
distinct from the supervisor's `garrison-supervisor` ML. M2.3
created the dashboard ML and "parked" it (no credentials issued).
M4 activates it.

In Infisical UI (`https://app.infisical.com` or self-hosted):

1. **Identities → Machine Identities**: confirm `garrison-dashboard`
   exists. If missing, create it.
2. **Add to project**: assign read+write permissions on the
   secrets path tree the dashboard will manage. Typical scope:
   the entire project on a single environment (`prod`); narrower
   scopes are fine for stricter deployments.
3. **Issue Universal Auth credentials**: client ID + client secret.
   Persist both in your secret manager (1Password, etc.) under
   the operator's vault. Never commit either.

**3. Dashboard runtime env vars**

Set the four required + one optional env var on the dashboard
container (Coolify env, restricted file, or `.env` per your
deployment):

```sh
# Required
INFISICAL_DASHBOARD_ML_CLIENT_ID=<from step 2>
INFISICAL_DASHBOARD_ML_CLIENT_SECRET=<from step 2>
INFISICAL_DASHBOARD_PROJECT_ID=<the Infisical project id>

# Optional (defaults shown)
INFISICAL_DASHBOARD_ENVIRONMENT=prod
INFISICAL_SITE_URL=http://garrison-infisical:8080
```

If any of the three required vars are missing, the dashboard
boots normally but vault server actions throw
`VaultError(Unavailable)` at call time. The UI's
`isVaultConfigured()` predicate detects this and surfaces a
"configure vault" prompt instead of action buttons. Non-vault
surfaces (org overview, hygiene, agents registry) work without
Infisical credentials.

**4. Dashboard image digest pinning**

Continues the M3 pattern. After build:

```sh
docker images --digests garrison-dashboard:dev
# record the @sha256:... digest in deployment notes
```

Pin in `compose` or your orchestration via the digest, not the
floating tag.

**5. `secret_metadata.rotation_provider` reclassification**

Migration `20260427000013_m4_agents_updated_at.sql` and the
prior `20260427000010_m4_supervisor_schema_extensions.sql`
default `rotation_provider` to `manual_paste` for every existing
secret_metadata row. After M4 ships, the operator should
reclassify any secrets that Infisical can natively rotate
(Postgres credentials, AWS IAM, etc.) to `infisical_native` via
the M4 vault edit UI. Until reclassified, those secrets show
the paste-new-value rotation flow even where Infisical native
rotation would apply. No data is at risk; it's a UX nuance.

**6. Supervisor restart for the agents.changed listener**

T014 adds a LISTEN goroutine on the supervisor for the
`agents.changed` channel; it starts at process boot. After
deploying the M4 supervisor binary, restart the supervisor
once so the goroutine starts. Subsequent agents-row writes from
the dashboard propagate via `pg_notify` without restart.

```sh
# rolling restart on the same compose stack
docker compose restart supervisor
```

If the supervisor is started without the M4 binary, agent edits
won't propagate to the supervisor's startup-once cache —
operators see "next spawn" still using the prior config.
Surface the gap by checking the supervisor's logs for the
"agents.changed listener failed to start" warning at boot.

---

## M5.1 — chat backend (CEO chat, read-only)

The supervisor gains a per-message Claude Code subprocess runtime
authenticated via the operator's `CLAUDE_CODE_OAUTH_TOKEN` (billed
against the operator's claude.ai account, not Anthropic API credits).
Read-only: no mutation MCP tools mount; chat-driven mutations are
M5.3+ behind a separate threat-model amendment.

### One-time setup at deploy

1. **Build the chat container images** before `compose up -d`:
   ```sh
   cd supervisor
   make chat-image            # → garrison-claude:m5
   make mockclaude-chat-image # → garrison-mockclaude:m5 (for CI / dev only)
   ```
   `garrison-claude:m5` ships the pinned `@anthropic-ai/claude-code@2.1.86`
   on `node:22-slim`. The supervisor's docker-compose.yml does NOT
   build these images — production deploy invokes the make targets.

2. **Verify the docker-proxy `CREATE` flag is in place**:
   ```sh
   docker exec garrison-docker-proxy env | grep CREATE
   # Expected: CREATE=1
   ```
   Without the `CREATE=1` flag the supervisor cannot spawn chat
   containers (the proxy refuses `POST /containers/create`).

3. **Seed `CLAUDE_CODE_OAUTH_TOKEN` into Infisical** via the M4 vault
   create flow at the M5.1 path convention:
   ```
   /<customer_id>/operator/CLAUDE_CODE_OAUTH_TOKEN
   ```
   Use the dashboard's `/vault/new` surface — same UX as any other
   secret. The `<customer_id>` segment is your supervisor's
   `cfg.CustomerID` value (single-tenant, one per Garrison install).
   The token format is `sk-ant-oat01...` (108 chars) — extract from
   `~/.claude/` after `claude` runs once on a workstation, or
   acquire via Anthropic's OAuth-token issuance flow.

### Environment variables

All seven are optional with sensible defaults; override in Coolify
(or the equivalent prod env-var surface) only when needed:

| Variable | Default | Purpose |
|---|---|---|
| `GARRISON_CHAT_CONTAINER_IMAGE` | `garrison-claude:m5` | Image the supervisor spawns per chat message; CI overrides to `garrison-mockclaude:m5` |
| `GARRISON_CHAT_TURN_TIMEOUT` | `5m` | Per-turn wall-clock cap; SIGTERM the docker subprocess on overrun |
| `GARRISON_CHAT_SESSION_IDLE_TIMEOUT` | `30m` | Sessions idle for this long get marked `ended`; subsequent operator messages rejected with `error_kind='session_ended'` |
| `GARRISON_CHAT_SESSION_COST_CAP_USD` | `1.00` | Soft per-session cost cap; reactive check (refuses next turn when running total ≥ cap) |
| `GARRISON_CHAT_OAUTH_VAULT_PATH` | `/operator/CLAUDE_CODE_OAUTH_TOKEN` | Vault path SUFFIX (the `/<customer_id>/` prefix is composed by the supervisor) |
| `GARRISON_CHAT_DOCKER_NETWORK` | `garrison-net` | Compose network the chat container joins; needs DNS reachability to postgres + mempalace |
| `GARRISON_CHAT_DEFAULT_MODEL` | `claude-sonnet-4-6` | `--model` flag passed to claude |

### Token rotation

OAuth tokens expire. To rotate:

1. Acquire a fresh token (e.g. `claude` re-auth on a workstation +
   extract from `~/.claude/`).
2. Use the dashboard's `/vault/edit/<path>/CLAUDE_CODE_OAUTH_TOKEN`
   to replace the value. The M4 server action writes the audit
   row + bumps `secret_metadata.last_rotated_at`.
3. **No supervisor restart needed.** The chat path fetches the
   token per-message, so the next chat-message spawn picks up the
   new value automatically. (FR-005 contract.)

### Monitoring

- **Per-turn duration**: `chat_messages.raw_event_envelope ->> 'result' ->> 'duration_ms'` (read from the JSONB envelope of the stored result event).
- **Per-session running cost**: `chat_sessions.total_cost_usd`. Compare against `GARRISON_CHAT_SESSION_COST_CAP_USD`.
- **Audit trail**: `vault_access_log WHERE metadata ->> 'actor' = 'supervisor_chat'` shows every token reveal; `event_outbox WHERE channel LIKE 'work.chat.%'` shows session lifecycle events.
- **In-flight count**: `SELECT count(*) FROM chat_messages WHERE status IN ('pending', 'streaming')` — typically 0 or 1 single-operator.

### Restart sweep + idle sweep

Both run automatically on supervisor boot / on a 60s ticker:

- **Restart sweep** (FR-083, runs once at boot before LISTEN starts): marks any `pending`/`streaming` chat_messages older than 60s as `aborted` with `error_kind='supervisor_restart'`; rolls their parent sessions to `aborted`. Catches mid-turn supervisor crashes.
- **Idle sweep** (FR-081, 60s ticker): marks active sessions whose newest message is older than `GARRISON_CHAT_SESSION_IDLE_TIMEOUT` as `ended`; emits `pg_notify('work.chat.session_ended', ...)` for the dashboard's activity feed.

No operator action required for either — they self-manage on the same compose stack.

### Failure modes the operator will see

- `error_kind='token_not_found'`: Infisical doesn't have a value at the M5.1 path. Operator action: re-seed via `/vault/new`.
- `error_kind='token_expired'`: token rejected by Anthropic. Operator action: rotate via `/vault/edit`.
- `error_kind='vault_unavailable'`: Infisical is down. Operator action: restart the Infisical service / check health.
- `error_kind='session_cost_cap_reached'`: running cost >= cap. Operator action: start a new session or raise the cap.
- `error_kind='session_ended'`: session timed out. Operator action: start a new session.
- `error_kind='turn_timeout'`: turn exceeded `GARRISON_CHAT_TURN_TIMEOUT`. Operator action: investigate (rare — usually means claude is hung; the next turn will spawn fresh).

## M5.2 — chat dashboard surface

Pre-deploy:

1. Apply `migrations/20260429000016_m5_2_chat_archive_and_cascade.sql` via goose. The migration adds `chat_sessions.is_archived BOOLEAN NOT NULL DEFAULT false` plus the partial index `idx_chat_sessions_user_active_unarchived`, recreates the `chat_messages.session_id` FK with `ON DELETE CASCADE`, and grants `DELETE ON chat_sessions` to `garrison_dashboard_app`.
2. Verify `garrison_dashboard_app` now has `INSERT, SELECT, UPDATE, DELETE` on `chat_sessions` and only `INSERT, SELECT` on `chat_messages` (the cascade does the work; the dashboard role does NOT need DELETE on `chat_messages`). `\dp chat_sessions` and `\dp chat_messages` in psql.
3. Verify the FK is now `ON DELETE CASCADE`. `\d+ chat_messages` should show `"chat_messages_session_id_fkey" FOREIGN KEY (session_id) REFERENCES chat_sessions(id) ON DELETE CASCADE`.
4. `vault_access_log` is intentionally NOT touched (FR-236) — rows referencing a deleted `chat_session_id` via JSONB metadata survive as forensic trail. The dashboard role keeps its M4 INSERT-only grant on `vault_access_log`; no DELETE grant added.

Post-deploy:

1. On supervisor restart, `RunRestartSweep` runs the new orphan-row pass alongside the existing pending-message pass. Watch logs for `chat: orphan sweep synthesised aborted` — expected count zero on healthy boot.
2. Confirm the dashboard's left-rail "CEO chat" entry renders between Activity and Hygiene with the thread-history subnav listing the latest 10 active threads as `thread #N`.
3. Confirm `/chat/all` (full thread list view) and `/chat/<uuid>` (active session view) both load.
4. The new literal channel `work.chat.session_deleted` joins `KNOWN_CHANNELS`; `deleteChatSession` emits IDs-only payload per FR-321.

## M5.4 — knowledge-base pane (Company.md + recent palace writes + KG facts)

M5.4 adds **MinIO** as the 4th container alongside supervisor +
mempalace + docker-proxy + the M2.3 Infisical trio, hosting the CEO-
editable Company.md object at
`s3://garrison-company/<companyId>/company.md`. Persistence uses a
Docker named volume `garrison-minio-data` — no host bind mounts. Two
new Go and TS dependencies: `github.com/minio/minio-go/v7` (supervisor)
and `@uiw/react-codemirror` + `@codemirror/lang-markdown` (dashboard).

The supervisor exposes a new HTTP server (`internal/dashboardapi`) on
port 8081, behind cookie-forward auth against the dashboard's better-
auth `sessions` table:

  GET / PUT /api/objstore/company-md
  GET       /api/mempalace/recent-writes
  GET       /api/mempalace/recent-kg

Pre-deploy:

1. Apply `migrations/20260501000000_m5_4_deploy_marker.sql` via goose.
   The marker has no DDL — its presence in `goose_db_version` is the
   deploy waypoint. No schema changes; no `GRANT SELECT` on `sessions`
   (supervisor connects as schema owner with full privileges per the
   T001 reality-adjustment).
2. Pull the pinned MinIO image: `docker pull
   minio/minio@sha256:69b2ec208575b69597784255eec6fa6a2985ee9e1a47f4411a51f7f5fdd193a9`.
3. Set the operator `.env` values:
   - `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` — MinIO container root
     credentials (env-on-container only; never logged).
   - `GARRISON_MINIO_ACCESS_KEY_PATH` / `GARRISON_MINIO_SECRET_KEY_PATH`
     — Infisical secret paths the supervisor reads at startup.
4. `docker compose up -d` brings up `garrison-minio` alongside the
   existing seven services. Watch `docker logs garrison-minio` for
   `API: http://...:9000` and `Healthcheck: ENABLED`.

Post-deploy:

1. Mint a scoped MinIO service account (do NOT use the root credentials
   for the supervisor). Run inside the `garrison-minio` container:
   ```
   docker exec -it garrison-minio mc alias set local \
     http://localhost:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
   docker exec -it garrison-minio mc admin user svcacct add local \
     "$MINIO_ROOT_USER" --access-key garrison-supervisor \
     --secret-key "$(openssl rand -hex 24)"
   ```
   Capture the printed access-key + secret-key pair.
2. Populate Infisical at the configured paths. The path **must include
   the customer-id UUID prefix** matching `cfg.CustomerID()` —
   Infisical's vault layout is `/<customerId>/<role>/<env_var>` per
   the M2.3 convention. So if `companies.id` is
   `00000000-0000-0000-0000-000000000001`, the configured paths are
   `/00000000-0000-0000-0000-000000000001/operator/MINIO_ACCESS_KEY`
   and `/00000000-0000-0000-0000-000000000001/operator/MINIO_SECRET_KEY`.
   Populate the values with
   the scoped service-account credentials from step 1.
3. Restart the supervisor (`docker compose restart supervisor`). Watch
   logs for:
   - `objstore: bootstrap created bucket garrison-company` (first run)
     OR `objstore: bootstrap bucket exists` (subsequent runs)
   - `dashboardapi server listening addr=0.0.0.0:8081`
   On `objstore: bootstrap failed`, the supervisor exits with
   `ExitFailure`. Check that MinIO is reachable from the supervisor
   container at `garrison-minio:9000` and that the Infisical paths
   carry the scoped credentials.
4. (Optional dev-only) Seed `examples/company.md` into the bucket as a
   starter template:
   ```
   docker cp examples/company.md garrison-minio:/tmp/company.md
   docker exec garrison-minio mc cp /tmp/company.md \
     local/garrison-company/<companyId>/company.md
   ```
   Replace `<companyId>` with the operator's `companies.id` UUID.
5. Open `/chat`, click the Company.md tab, click Edit; verify the
   editor accepts changes and Save round-trips through the supervisor
   proxy. Verify the Recent palace writes + KG recent facts tabs
   render data without errors.

Restart-recovery: the `garrison-minio-data` named volume preserves
bucket state across `docker compose down` + `docker compose up -d`.
**`docker compose down -v` is destructive** — it removes the named
volume and the Company.md object with it. Operator should snapshot the
volume before any destructive command:
   ```
   docker run --rm -v garrison-minio-data:/data:ro -v $(pwd):/backup \
     busybox tar czf /backup/minio-$(date +%Y%m%d).tar.gz /data
   ```

Backup / restore design beyond ad-hoc snapshots is out of scope for
M5.4; tracked as post-M5 follow-up.

TLS: M5.4 ships internal-network plaintext only
(`GARRISON_MINIO_USE_TLS=false`). Operator-controlled TLS termination
is post-M5; the path is documented at FR-667.

## Changelog

- **2026-05-01**: M5.4 knowledge-base pane deployment section added
  (MinIO 4th container, scoped service-account credential model via
  Infisical, dashboardapi HTTP server on port 8081, named volume
  garrison-minio-data, no schema changes, examples/company.md fixture).
- **2026-04-29**: M5.2 chat dashboard surface deployment section added (one new migration, one new pg_notify channel, sidebar entry + thread-history subnav, /chat/all + /chat/[[...sessionId]] routes, orphan-row sweep extension).
- **2026-04-28**: M5.1 chat backend deployment section added (per-message ephemeral chat containers, OAuth token vault seeding, 7 GARRISON_CHAT_* env vars, restart + idle sweeps).
- **2026-04-27**: M4 dashboard mutations deployment section added (4 migrations, dashboard ML activation, supervisor restart for the cache invalidator).
- **2026-04-26**: M3 dashboard deployment section added.
- **2026-04-24**: M2.3 Infisical deployment section added.
- **2026-04-22**: Initial version. M2.1's `garrison_agent_ro` password discipline codified. M2.2 and M2.3 sections sketched based on planned milestone designs.
---
