#!/usr/bin/env bash
# Dev-stack: bring up a local Garrison Postgres + dashboard so the
# operator can click through M3 surfaces with realistic seed data.
#
# What it does:
#   1. Boots a postgres:17 container (named garrison-dev-pg, port 55432)
#   2. Applies all goose migrations from migrations/ (M1 → M3.dept-grants)
#   3. Runs scripts/dev-stack-seed.sql to populate companies, departments,
#      tickets across all Kanban columns, agent instances, hygiene rows,
#      vault metadata + audit log, and recent activity events.
#   4. Sets dashboard role passwords to apppass / ropass and flips LOGIN.
#   5. Runs the dashboard's drizzle:migrate to create better-auth tables
#      + operator_invites in the same database.
#   6. Generates a one-shot BETTER_AUTH_SECRET into .dev-stack.env
#   7. Starts the dashboard (next dev) on http://localhost:3000
#
# Tear down with scripts/dev-stack-down.sh.
#
# Prereqs: docker, goose (from go install), bun.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

CONTAINER_NAME="garrison-dev-pg"
PG_PORT="55432"
PG_PASSWORD="devpassword"
DSN_OWNER="postgres://postgres:${PG_PASSWORD}@localhost:${PG_PORT}/garrison?sslmode=disable"
DSN_APP="postgres://garrison_dashboard_app:apppass@localhost:${PG_PORT}/garrison?sslmode=disable"
DSN_RO="postgres://garrison_dashboard_ro:ropass@localhost:${PG_PORT}/garrison?sslmode=disable"
GOOSE_DSN="host=localhost port=${PG_PORT} user=postgres password=${PG_PASSWORD} dbname=garrison sslmode=disable"
ENV_FILE="${REPO_ROOT}/.dev-stack.env"

# Ensure bun is on PATH for the dashboard build.
if ! command -v bun >/dev/null 2>&1; then
  if [[ -x "$HOME/.bun/bin/bun" ]]; then
    export PATH="$HOME/.bun/bin:$PATH"
  else
    echo "error: bun not found. Install with: curl -fsSL https://bun.sh/install | bash" >&2
    exit 1
  fi
fi

# Next.js 16 requires Node ≥ 20.9.0. Many systems still default to an
# older nvm-managed v18; if so, walk ~/.nvm/versions/node looking for a
# v20+ install and prepend it to PATH. The user's node-22 install is
# the typical case here.
need_node_upgrade=0
if command -v node >/dev/null 2>&1; then
  node_major="$(node -e 'process.stdout.write(String(process.versions.node).split(".")[0])' 2>/dev/null || echo 0)"
  [[ "${node_major:-0}" -lt 20 ]] && need_node_upgrade=1
else
  need_node_upgrade=1
fi
if [[ $need_node_upgrade -eq 1 ]]; then
  if [[ -d "$HOME/.nvm/versions/node" ]]; then
    newest_node="$(ls -1 "$HOME/.nvm/versions/node" | grep '^v[2-9][0-9]\.' | sort -V | tail -1)"
    if [[ -n "$newest_node" && -x "$HOME/.nvm/versions/node/$newest_node/bin/node" ]]; then
      export PATH="$HOME/.nvm/versions/node/$newest_node/bin:$PATH"
      echo "    using Node $newest_node from nvm (system node was <20)"
    else
      echo "error: Node ≥ 20.9.0 required by Next.js 16; none found in ~/.nvm/versions/node" >&2
      exit 1
    fi
  else
    echo "error: Node ≥ 20.9.0 required by Next.js 16; none found on PATH or in ~/.nvm" >&2
    exit 1
  fi
fi

if ! command -v goose >/dev/null 2>&1; then
  if [[ -x "$HOME/go/bin/goose" ]]; then
    export PATH="$HOME/go/bin:$PATH"
  else
    echo "error: goose not found. Install with: go install github.com/pressly/goose/v3/cmd/goose@latest" >&2
    exit 1
  fi
fi

echo "==> [1/7] booting postgres:17 (container=${CONTAINER_NAME}, port=${PG_PORT})"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  echo "    container already exists; reusing. (use scripts/dev-stack-down.sh to wipe)"
  docker start "${CONTAINER_NAME}" >/dev/null
else
  docker run -d --name "${CONTAINER_NAME}" \
    -e POSTGRES_PASSWORD="${PG_PASSWORD}" \
    -e POSTGRES_DB=garrison \
    -p "${PG_PORT}:5432" \
    postgres:17 >/dev/null
fi

echo "==> [2/7] waiting for postgres ready..."
for i in {1..30}; do
  if docker exec "${CONTAINER_NAME}" pg_isready -U postgres -d garrison >/dev/null 2>&1; then
    break
  fi
  sleep 1
  if [[ $i -eq 30 ]]; then
    echo "    postgres failed to come up in 30s" >&2
    exit 1
  fi
done

echo "==> [3/7] applying goose migrations from ./migrations"
goose -dir migrations postgres "${GOOSE_DSN}" up

echo "==> [4/7] seeding M2-arc data + flipping dashboard roles to LOGIN"
docker exec -i "${CONTAINER_NAME}" psql -U postgres -d garrison -v ON_ERROR_STOP=1 \
  < scripts/dev-stack-seed.sql >/dev/null

echo "==> [5/7] running dashboard's drizzle:migrate (better-auth + operator_invites)"
(
  cd dashboard
  DASHBOARD_APP_DSN="${DSN_OWNER}" bun run drizzle:migrate >/dev/null
)

# Drizzle migrations create tables owned by `postgres` superuser. The
# garrison_dashboard_app role needs explicit grants to write to them.
echo "==> [6/7] granting dashboard_app write access to better-auth tables"
docker exec -i "${CONTAINER_NAME}" psql -U postgres -d garrison -v ON_ERROR_STOP=1 <<'SQL' >/dev/null
GRANT SELECT, INSERT, UPDATE, DELETE ON
  users, sessions, accounts, verifications, operator_invites
TO garrison_dashboard_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO garrison_dashboard_app;
SQL

echo "==> [7/7] writing ${ENV_FILE} + starting dashboard on http://localhost:3000"
# Reuse the existing BETTER_AUTH_SECRET if .dev-stack.env is already
# present — that way `dev-stack-down && dev-stack-up` cycles don't
# invalidate browser session cookies (each new secret means every
# existing session 401's on validation, which manifests as a
# perpetual SSE reconnect loop on /activity).
if [[ -f "${ENV_FILE}" ]] && grep -q '^export BETTER_AUTH_SECRET=' "${ENV_FILE}"; then
  SECRET="$(grep '^export BETTER_AUTH_SECRET=' "${ENV_FILE}" | sed -E "s/^export BETTER_AUTH_SECRET='([^']+)'$/\1/")"
  echo "    reusing BETTER_AUTH_SECRET from existing ${ENV_FILE}"
else
  SECRET="$(openssl rand -hex 32)"
fi
cat >"${ENV_FILE}" <<EOF
# Generated by scripts/dev-stack-up.sh — gitignored.
# Source this file before running 'cd dashboard && bun run dev' if you
# want to re-attach to the same stack later: \`source .dev-stack.env\`.
export DASHBOARD_APP_DSN='${DSN_APP}'
export DASHBOARD_RO_DSN='${DSN_RO}'
export BETTER_AUTH_SECRET='${SECRET}'
export BETTER_AUTH_URL='http://localhost:3000'
EOF

cat <<EOF

==================================================================
  Garrison dev-stack is up.

  Postgres:   localhost:${PG_PORT}  (container: ${CONTAINER_NAME})
  Dashboard:  http://localhost:3000  (starting now…)

  First-run flow:
    1. Browser will land on /setup (first-run wizard) — create an
       operator account. Once any user exists, /setup 404s and login
       moves to /login.
    2. After /setup completes you'll see:
       /                 — org overview (3 departments, KPIs, activity)
       /departments/engineering — Kanban with cards across all 4 columns
       /tickets/<id>     — ticket detail with metadata + history
       /hygiene          — 3 hygiene rows across 3 failure-mode buckets
       /vault            — 3 secrets, 4 audit entries
       /agents           — 3 agents (engineer + qa-engineer + tech-writer)
       /admin/invites    — empty until you generate one

  Tear down:  scripts/dev-stack-down.sh
==================================================================

EOF

# Run the dashboard in the foreground so Ctrl+C cleanly kills it.
# shellcheck disable=SC1090
source "${ENV_FILE}"
cd dashboard
exec bun run dev
