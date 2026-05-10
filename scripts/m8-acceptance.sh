#!/usr/bin/env bash
# scripts/m8-acceptance.sh — scripted check that exercises every M8
# acceptance criterion from specs/018-m8-zero-human-loop/spec.md.
#
# What it does:
#  1. Run the M8 integration test suite (SC-001 through SC-002, SC-003,
#     SC-004, SC-006, SC-012, SC-014 all covered by go-side tests).
#  2. Run the M8 chaos suite (SC-011 + concurrent-dispatch dedupe).
#  3. Run the legacy M2.x integration suites as a regression gate
#     (SC-011 requires the prior milestones still pass).
#  4. Verify the SonarCloud quality gate locally via go test -cover
#     reports (SC-010 — the actual Sonar gate runs in CI).
#
# Acceptance is interpreted strictly: any non-zero exit step fails the
# run. Re-running this script after a fix must pass cleanly.
#
# SC-005 (per-spawn MCPJungle routing) + SC-008 (subagent token-budget
# inheritance) + SC-013 (≥10-hire customer-prefix soak) are operator-
# driven soak items that require a live stack. They're documented in
# the M8 retro for follow-up.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT/supervisor"

PASS=0
FAIL=0

run_step() {
  local label="$1"
  shift
  echo "==> $label"
  if "$@"; then
    PASS=$((PASS + 1))
    echo "    PASS"
  else
    FAIL=$((FAIL + 1))
    echo "    FAIL"
  fi
}

# -----------------------------------------------------------------------------
# Step 1 — M8 integration suite (Go-side SC coverage).
# -----------------------------------------------------------------------------
run_step "M8 integration: golden path (SC-001)" \
  go test -tags=integration -run TestM8GoldenPath . -count=1 -timeout 300s

run_step "M8 integration: dependency + cycle (SC-002 + FR-103)" \
  go test -tags=integration -run TestM8Dependency . -count=1 -timeout 300s

run_step "M8 integration: runaway (SC-003 + FR-200/201)" \
  go test -tags=integration -run TestM8Runaway . -count=1 -timeout 300s

run_step "M8 integration: MCPJungle (SC-004 + SC-006)" \
  go test -tags=integration -run TestM8MCPJungle . -count=1 -timeout 300s

run_step "M8 integration: audit surface (SC-012 + SC-014)" \
  go test -tags=integration -run TestM8Audit . -count=1 -timeout 300s

# -----------------------------------------------------------------------------
# Step 2 — Chaos suite (SC-011 + dedupe race).
# -----------------------------------------------------------------------------
run_step "M8 chaos: MCPJungle unreachable (SC-011)" \
  go test -tags='chaos integration' -run TestM8MCPJungleUnreachable . -count=1 -timeout 120s

run_step "M8 chaos: dedupe end-state (FR-306 single-row)" \
  go test -tags='chaos integration' -run TestM8RegistrationRequestIdempotent . -count=1 -timeout 120s

# -----------------------------------------------------------------------------
# Step 3 — Regression: M2.x + M5.x + M6 + M7 integration suites still pass.
# -----------------------------------------------------------------------------
run_step "Regression: full integration suite" \
  go test -tags=integration ./... -count=1 -timeout 900s

# -----------------------------------------------------------------------------
# Step 4 — Coverage report (Sonar gate runs in CI; local check is sanity).
# -----------------------------------------------------------------------------
run_step "Coverage report (informational)" \
  go test ./internal/mcpjungle/... ./internal/mcpserverwork/... \
          ./internal/garrisonmutate/... ./internal/throttle/... \
          -cover -count=1 -timeout 180s

# -----------------------------------------------------------------------------
# Summary
# -----------------------------------------------------------------------------
echo ""
echo "===================="
echo "M8 acceptance: $PASS pass / $FAIL fail"
echo "===================="
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi
exit 0
