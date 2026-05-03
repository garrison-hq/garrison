#!/usr/bin/env bash
# scripts/socket-proxy-policy-test.sh — exercises the docker-socket-
# proxy's surface against the M7 sandbox + hiring threat-model rule
# set (plan §12 / decision #21). For each test case, issues a request
# the proxy must reject; a "control" happy-path request must succeed.
#
# Returns 0 only if every reject case returns HTTP 403 (or 5xx body
# rejection from the proxy's filter) AND the control request returns
# 2xx. Returns non-zero on any unexpected pass.
#
# Usage:
#   GARRISON_DOCKER_PROXY=tcp://localhost:2375 ./socket-proxy-policy-test.sh
#
# linuxserver/socket-proxy currently filters at the endpoint level
# (POST=1, EXEC=1, CREATE=1, etc.). Body-field filtering on
# /containers/create — the plan §12 / decision #21 long-term shape —
# requires either a custom HAProxy ACL fronting the proxy or a sidecar
# that inspects request bodies. Until that operator-driven filter
# lands, this script exercises the endpoint-level rules and documents
# the body-field rules as future-state assertions (marked TODO below).

set -euo pipefail

# GARRISON_DOCKER_PROXY_URL must be a fully-qualified URL with scheme.
# linuxserver/socket-proxy is HTTP-only by design; production deploys
# rely on the docker network's TLS-terminating boundary or VPN. The
# script does NOT default to a clear-text URL — the caller passes one
# explicitly so the URL doesn't appear as a literal in source. This
# also lets a future TLS-terminating sidecar swap the scheme without
# touching the script.
if [[ -z "${GARRISON_DOCKER_PROXY_URL:-}" ]]; then
  echo "GARRISON_DOCKER_PROXY_URL must be set (fully-qualified URL with scheme)" >&2
  echo "  see supervisor/docker-compose.yml for the in-cluster proxy address" >&2
  exit 2
fi
URL_BASE="${GARRISON_DOCKER_PROXY_URL%/}"

PASS=0
FAIL=0
declare -a FAILURES=()

check_rejected() {
  local desc="$1"
  local method="$2"
  local path="$3"
  local body="$4"

  local code
  code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
    -X "${method}" \
    -H 'Content-Type: application/json' \
    --data "${body}" \
    "${URL_BASE}${path}" || true)

  # Anything 2xx means the proxy let the request through unfiltered —
  # which is a fail. Anything ≥400 means the proxy rejected, which is
  # what we want.
  if (( code < 400 )); then
    FAIL=$((FAIL + 1))
    FAILURES+=("REJECT EXPECTED but got ${code}: ${desc}")
  else
    PASS=$((PASS + 1))
  fi
}

# Endpoint-level reject cases. linuxserver/socket-proxy returns 403
# for endpoints whose env-flag is 0; these probes assert each of those
# flags is in fact 0 in the deployed proxy config.

check_rejected "IMAGES disabled — pull/inspect rejected" \
  GET /images/json ''
check_rejected "VOLUMES disabled — listing volumes rejected" \
  GET /volumes ''
check_rejected "NETWORKS disabled — listing networks rejected" \
  GET /networks ''
check_rejected "BUILD disabled — image build rejected" \
  POST /build ''
check_rejected "SWARM disabled — swarm init rejected" \
  POST /swarm/init '{}'

# Body-field filter rules per plan §12 (operator-driven HAProxy ACL or
# sidecar). These cases land once a body-inspecting filter is in front
# of the proxy. They're documented here so the test expands cleanly
# when the filter ships:
#
#   check_rejected "Image=ubuntu rejected (only garrison-claude:m5)" \
#     POST /containers/create '{"Image":"ubuntu"}'
#   check_rejected "HostConfig.Privileged=true rejected" \
#     POST /containers/create '{"Image":"garrison-claude:m5","HostConfig":{"Privileged":true}}'
#   check_rejected "HostConfig.CapAdd=[SYS_ADMIN] rejected" \
#     POST /containers/create '{"Image":"garrison-claude:m5","HostConfig":{"CapAdd":["SYS_ADMIN"]}}'
#   check_rejected "HostConfig.NetworkMode=host rejected" \
#     POST /containers/create '{"Image":"garrison-claude:m5","HostConfig":{"NetworkMode":"host"}}'
#   check_rejected "Mount outside /var/lib/garrison rejected" \
#     POST /containers/create '{"Image":"garrison-claude:m5","HostConfig":{"Mounts":[{"Type":"bind","Source":"/etc","Target":"/etc"}]}}'

# Control happy-path: GET /containers/json (CONTAINERS=1 enables this).
control_code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  "${URL_BASE}/containers/json" || true)
if (( control_code >= 200 && control_code < 300 )); then
  PASS=$((PASS + 1))
else
  FAIL=$((FAIL + 1))
  FAILURES+=("CONTROL FAILED (got ${control_code}): GET /containers/json must succeed")
fi

echo
echo "socket-proxy policy test: ${PASS} pass / ${FAIL} fail"
if (( FAIL > 0 )); then
  echo
  printf 'FAIL: %s\n' "${FAILURES[@]}" >&2
  exit 1
fi
exit 0
