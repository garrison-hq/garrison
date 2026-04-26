#!/usr/bin/env bash
# Tear down the dev-stack: stop + remove the postgres container and
# delete the generated env file. Safe to run repeatedly.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTAINER_NAME="garrison-dev-pg"

if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  echo "==> stopping + removing ${CONTAINER_NAME}"
  docker rm -f "${CONTAINER_NAME}" >/dev/null
else
  echo "==> ${CONTAINER_NAME} not running"
fi

if [[ -f "${REPO_ROOT}/.dev-stack.env" ]]; then
  rm "${REPO_ROOT}/.dev-stack.env"
  echo "==> removed .dev-stack.env"
fi

echo "==> dev-stack down."
