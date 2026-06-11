#!/usr/bin/env bash
# M8: one-shot bootstrap for the dev MCPJungle sidecar.
#
# What it does:
#   1. Runs `mcpjungle init-server` against the garrison-mcpjungle
#      container so MCPJungle generates its admin bearer token.
#   2. Captures the token from stdout.
#   3. Writes the token to the dev Infisical instance at vault path
#      mcpjungle/admin (matching GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH).
#
# Prereqs:
#   - docker compose stack is up (supervisor/docker-compose.yml)
#   - infisical CLI is on PATH and logged in to the dev project
#   - GARRISON_INFISICAL_PROJECT_ID env var is set
#
# Idempotency: re-running this script will fail at step 1 if MCPJungle
# is already initialised. That's intentional — operators rotate the
# admin token by tearing down + re-up'ing the mcpjungle service.

set -euo pipefail

: "${GARRISON_INFISICAL_PROJECT_ID:?required: dev Infisical project id}"

CONTAINER_NAME="garrison-mcpjungle"
VAULT_PATH="${GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH:-mcpjungle/admin}"
INFISICAL_ENV="${GARRISON_INFISICAL_ENVIRONMENT:-dev}"

if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  echo "error: ${CONTAINER_NAME} is not running. Bring up the compose stack first." >&2
  exit 1
fi

if ! command -v infisical >/dev/null 2>&1; then
  echo "error: infisical CLI not found on PATH" >&2
  exit 1
fi

echo "==> [1/3] running mcpjungle init-server in ${CONTAINER_NAME}"
# Full path: the upstream image is distroless (no shell, no PATH); the
# binary lives at /mcpjungle (container entrypoint).
TOKEN_OUTPUT="$(docker exec "${CONTAINER_NAME}" /mcpjungle init-server 2>&1)"
echo "${TOKEN_OUTPUT}"

# Older MCPJungle versions print the admin token to stdout as:
#   admin_token: <token>
# Newer versions write it to /root/.mcpjungle.conf inside the container
# (YAML: access_token: <token>) and only print a confirmation.
TOKEN="$(echo "${TOKEN_OUTPUT}" | awk -F': ' '/^admin_token:/ {print $2}' | tr -d '[:space:]')"
if [[ -z "${TOKEN}" ]]; then
  CONF_TMP="$(mktemp)"
  if docker cp "${CONTAINER_NAME}:/root/.mcpjungle.conf" "${CONF_TMP}" >/dev/null 2>&1; then
    TOKEN="$(awk -F': ' '/^access_token:/ {print $2}' "${CONF_TMP}" | tr -d '[:space:]')"
  fi
  rm -f "${CONF_TMP}"
fi
if [[ -z "${TOKEN}" ]]; then
  echo "error: failed to parse admin token from init-server output or .mcpjungle.conf" >&2
  exit 1
fi

# Split the vault path into folder + key for Infisical's CLI.
FOLDER="$(dirname "/${VAULT_PATH}")"
KEY="$(basename "${VAULT_PATH}")"

echo "==> [2/3] writing token to Infisical at ${VAULT_PATH}"
infisical secrets set "${KEY}=${TOKEN}" \
  --projectId="${GARRISON_INFISICAL_PROJECT_ID}" \
  --env="${INFISICAL_ENV}" \
  --path="${FOLDER}"

echo "==> [3/3] verifying readback"
infisical secrets get "${KEY}" \
  --projectId="${GARRISON_INFISICAL_PROJECT_ID}" \
  --env="${INFISICAL_ENV}" \
  --path="${FOLDER}" >/dev/null

echo "done. The supervisor will pick up the admin token at boot via"
echo "GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH=${VAULT_PATH}."
