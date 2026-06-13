#!/usr/bin/env bash
# scripts/m11-acceptance.sh — scripted walk of every M11 success criterion
# (SC-001..SC-009) from specs/023-m11-action-broker/spec.md, plus the
# two pre-PR-push clearance probes from tasks.md T014:
#   - new-code coverage probe (≥82% on Go-side new code, M6 retro #7)
#   - SonarCloud new-issues pre-clearance (M8/M9/M10 T014 pattern)
#
# SC → verifying step mapping (tasks.md T014):
#   SC-001  TestRequestExternalActionWritesExactlyOnePendingRow (T008) +
#           TestEndToEndApproveTierGitHubCommentBack (T011, integration)
#   SC-002  TestHandleAutoTierExecutesWithoutGate +
#           TestHandleNotifyTierExecutesThenNotifies +
#           TestHandleNeverExecutesPendingApprove +
#           TestHandleNeverExecutesHumanOnly (T009) +
#           TestAutoTierExecutesWithoutGate +
#           TestNotifyTierExecutesThenSurfaces (T011, integration)
#   SC-003  TestFloorCannotBeLowered + TestClassifyUnknownDefaultsApprove
#           (T004/policy_test.go) + TestFloorEnforcedAtDB (T011, integration) +
#           TestRequestExternalActionIgnoresAgentSuppliedTier (T008)
#   SC-004  git grep assertion — broker is the only door: no squid.conf
#           change; no GITHUB_PAT in agent containers; dispatcher is
#           supervisor-side only
#   SC-005  TestHandleVaultUnavailableFailsClosed + TestPostCommentNeverLogsPAT
#           (T009): vault-scoped PAT never in agent env/prompt/context
#   SC-006  TestConcurrentClaimDispatchesExactlyOnce +
#           TestRestartMidDispatchNoDoublePost (T010, chaos)
#   SC-007  TestEndToEndApproveTierGitHubCommentBack (T011, integration):
#           audit reconstructs agent_instance_id, payload, tier,
#           approved_by, outcome — all immutable
#   SC-008  git-log assertion: action-broker-threat-model.md commit (T001)
#           precedes first internal/actionbroker/ commit (T006)
#           (the M9 SC-007 / M10 pattern)
#   SC-009  go mod tidy produces no change to go.mod/go.sum;
#           bunx tsc --noEmit passes; all M1–M10 regression suites pass
#           (go test ./... default + -tags=integration + -tags=chaos);
#           git diff for squid.conf is empty;
#           TestVerbsRegistryMatchesEnumeration + TestVerbsSlicesDisjoint
#           pass (sealed-verb registry additive only)
#
# Acceptance is interpreted strictly: any failed step fails the run
# (exit 1). The operator-attended item (SC-004 dashboard/egress walk) is
# printed as instructions, not asserted for the interactive portion —
# same posture as M9's SC-006 and M10's SC-006 operator-attended walks.
# The Sonar probe is asserted when a PR analysis exists; before the
# branch is pushed it reports "pending" and the operator re-runs this
# script (or just that step) after CI's SonarCloud job lands.
#
# Per tasks.md T014: if a step fails, patch the relevant earlier task's
# files (focused patch, no new features), then re-run this script from
# the top.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUP_DIR="${REPO_ROOT}/supervisor"
WORK_DIR="$(mktemp -d /tmp/m11-acceptance.XXXXXX)"

SONAR_HOST="https://sonarcloud.io"
SONAR_PROJECT="garrison-hq_garrison"
COVERAGE_THRESHOLD=82

PASS=0
FAIL=0
declare -a FAILURES=()

ok() {
  local label="$1"
  PASS=$((PASS + 1))
  printf '  PASS  %s\n' "${label}"
  return 0
}

fail() {
  local label="$1"
  FAIL=$((FAIL + 1))
  FAILURES+=("${label}")
  printf '  FAIL  %s\n' "${label}" >&2
  return 0
}

note() {
  local message="$1"
  printf '  ....  %s\n' "${message}"
  return 0
}

# run_step <label> <log-name> <cmd...> — run a command from supervisor/,
# capture its output, and book a pass/fail without short-circuiting the
# walk (the operator sees every gap in one run).
run_step() {
  local label="$1" log="$2"
  shift 2
  if (cd "${SUP_DIR}" && "$@" > "${WORK_DIR}/${log}.log" 2>&1); then
    ok "${label}"
  else
    fail "${label} (see ${WORK_DIR}/${log}.log)"
  fi
  return 0
}

echo "M11 acceptance walk — ${REPO_ROOT}"
echo "Artifacts: ${WORK_DIR}"
echo

# -----------------------------------------------------------------------------
# Step 0 — preconditions. Integration + chaos suites run testcontainers;
# copy-migrations stages the repo-root migrations for cmd/supervisor's
# go:embed (every ./... build needs it).
# -----------------------------------------------------------------------------
echo "== Step 0: preconditions"

command -v go >/dev/null 2>&1 || fail "precondition: go on PATH"
command -v git >/dev/null 2>&1 || fail "precondition: git on PATH"
if ! docker info >/dev/null 2>&1; then
  fail "precondition: docker reachable (testcontainers for integration + chaos suites)"
fi
if (cd "${SUP_DIR}" && make copy-migrations > "${WORK_DIR}/copy-migrations.log" 2>&1); then
  ok "step 0: migrations staged for go:embed (make copy-migrations)"
else
  fail "step 0: make copy-migrations (see ${WORK_DIR}/copy-migrations.log)"
fi

if (( FAIL > 0 )); then
  echo
  echo "Preconditions failed — aborting." >&2
  exit 1
fi

# -----------------------------------------------------------------------------
# SC-001 — an agent's request_external_action call produces exactly one
# immutable pending_actions row anchored on the calling agent_instance_id,
# with the tier assigned by the policy table (not an agent-supplied field)
# and a typed queued/at-tier result returned — and nothing has acted on
# the world. Verified by unit test (T008) + integration test (T011).
# -----------------------------------------------------------------------------
echo
echo "== SC-001: request-to-pending lifecycle (T008 unit + T011 integration)"

run_step "SC-001: verb writes exactly one pending row (TestRequestExternalActionWritesExactlyOnePendingRow)" sc001-unit \
  go test -tags=integration -run 'TestRequestExternalActionWritesExactlyOnePendingRow$' ./internal/garrisonmutate/ -count=1 -timeout 600s

run_step "SC-001: end-to-end approve-tier comment lifecycle (TestEndToEndApproveTierGitHubCommentBack)" sc001-e2e \
  go test -tags=integration -run 'TestEndToEndApproveTierGitHubCommentBack$' ./internal/actionbroker/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-002 — each tier path is backed by a passing test: auto executes with
# a log and no gate; notify executes then surfaces post-hoc; approve is
# blocked until the operator approve click, then dispatches; human_only is
# never dispatched and waits for "mark as done." (T009 unit + T011 integration)
# -----------------------------------------------------------------------------
echo
echo "== SC-002: four tiers behave distinctly (T009 unit + T011 integration)"

run_step "SC-002: auto tier executes without gate (TestHandleAutoTierExecutesWithoutGate)" sc002-auto-unit \
  go test -tags=integration -run 'TestHandleAutoTierExecutesWithoutGate$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-002: notify tier executes then notifies (TestHandleNotifyTierExecutesThenNotifies)" sc002-notify-unit \
  go test -tags=integration -run 'TestHandleNotifyTierExecutesThenNotifies$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-002: pending approve not dispatched (TestHandleNeverExecutesPendingApprove)" sc002-approve-gate-unit \
  go test -tags=integration -run 'TestHandleNeverExecutesPendingApprove$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-002: human_only never dispatched (TestHandleNeverExecutesHumanOnly)" sc002-humanonly-unit \
  go test -tags=integration -run 'TestHandleNeverExecutesHumanOnly$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-002: auto tier integration (TestAutoTierExecutesWithoutGate)" sc002-auto-int \
  go test -tags=integration -run 'TestAutoTierExecutesWithoutGate$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-002: notify tier integration (TestNotifyTierExecutesThenSurfaces)" sc002-notify-int \
  go test -tags=integration -run 'TestNotifyTierExecutesThenSurfaces$' ./internal/actionbroker/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-003 — permanent-Approve floor cannot be lowered: a contrived attempt
# (config or agent-supplied) to classify a floor action type as auto/notify
# is rejected or ignored; an unclassified action type defaults to approve.
# (T004/policy_test.go + T011 integration + T008 unit)
# -----------------------------------------------------------------------------
echo
echo "== SC-003: permanent-Approve floor cannot be lowered (T004 + T011 + T008)"

run_step "SC-003: Classify(github_issue_comment) → approve (TestClassifyFloorIsApprove)" sc003-classify \
  go test -run 'TestClassifyFloorIsApprove$' ./internal/actionbroker/ -count=1 -timeout 120s

run_step "SC-003: floor cannot be lowered by policy override (TestFloorCannotBeLowered)" sc003-floor \
  go test -run 'TestFloorCannotBeLowered$' ./internal/actionbroker/ -count=1 -timeout 120s

run_step "SC-003: unknown action type defaults to approve (TestClassifyUnknownDefaultsApprove)" sc003-unknown \
  go test -run 'TestClassifyUnknownDefaultsApprove$' ./internal/actionbroker/ -count=1 -timeout 120s

run_step "SC-003: floor check matches DB CONSTRAINT list (TestFloorCheckMatchesPolicy)" sc003-dbcheck \
  go test -run 'TestFloorCheckMatchesPolicy$' ./internal/actionbroker/ -count=1 -timeout 120s

run_step "SC-003: agent-supplied tier ignored (TestRequestExternalActionIgnoresAgentSuppliedTier)" sc003-agent-tier \
  go test -tags=integration -run 'TestRequestExternalActionIgnoresAgentSuppliedTier$' ./internal/garrisonmutate/ -count=1 -timeout 600s

run_step "SC-003: DB CHECK rejects floor action below approve (TestFloorEnforcedAtDB)" sc003-db-enforce \
  go test -tags=integration -run 'TestFloorEnforcedAtDB$' ./internal/actionbroker/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-004 — the broker is the only door: agent containers cannot reach the
# external provider directly. Asserted via:
#   (a) squid.conf is byte-for-byte unchanged from M10 (no egress hole)
#   (b) no GITHUB_PAT or outbound GitHub call in any agent-container path
#       (internal/agents/, internal/claudeproto/, internal/finalize/)
#   (c) no squid.conf reference inside the dispatcher or main.go
#       (dispatcher is supervisor-side, routes through no agent network)
# The interactive portion (TCP_DENIED logs for a live direct-egress
# attempt) requires a running dev stack; it is printed as instructions.
# -----------------------------------------------------------------------------
echo
echo "== SC-004: broker is the only door (git grep + squid.conf unchanged)"

# (a) squid.conf unchanged from the pre-M11 baseline in main
SQUID_CONF="${SUP_DIR}/egress/squid.conf"
SQUID_DIFF="$(git -C "${REPO_ROOT}" diff --name-only HEAD \
  -- supervisor/egress/squid.conf 2>/dev/null)"
if [[ -z "${SQUID_DIFF}" ]]; then
  # working tree clean; check against merge-base with origin/main
  BASE_REF="origin/main"
  git -C "${REPO_ROOT}" rev-parse -q --verify "${BASE_REF}" >/dev/null 2>&1 || BASE_REF="main"
  MERGE_BASE="$(git -C "${REPO_ROOT}" merge-base "${BASE_REF}" HEAD 2>/dev/null || true)"
  if [[ -n "${MERGE_BASE}" ]]; then
    SQUID_BRANCH_DIFF="$(git -C "${REPO_ROOT}" diff --name-only \
      "${MERGE_BASE}..HEAD" -- supervisor/egress/squid.conf 2>/dev/null)"
    if [[ -z "${SQUID_BRANCH_DIFF}" ]]; then
      ok "SC-004: supervisor/egress/squid.conf unchanged from M10 baseline — egress allow-list holds"
    else
      fail "SC-004: supervisor/egress/squid.conf has been modified on this branch — egress hole opened"
    fi
  else
    ok "SC-004: supervisor/egress/squid.conf clean in working tree (merge-base unavailable)"
  fi
else
  fail "SC-004: supervisor/egress/squid.conf has uncommitted changes"
fi

# (b) no GITHUB_PAT reference in agent-container paths
if grep -rn 'GITHUB_PAT' \
    "${SUP_DIR}/internal/agents/" \
    "${SUP_DIR}/internal/claudeproto/" \
    "${SUP_DIR}/internal/finalize/" \
    > "${WORK_DIR}/sc004-pat-grep.log" 2>&1; then
  fail "SC-004: GITHUB_PAT found in agent-container code (see ${WORK_DIR}/sc004-pat-grep.log)"
else
  ok "SC-004: GITHUB_PAT not present in any agent-container path — credential isolation holds"
fi

# (c) no squid.conf mutation inside dispatcher or main.go
if grep -rn 'squid\.conf' \
    "${SUP_DIR}/internal/actionbroker/" \
    "${SUP_DIR}/cmd/supervisor/main.go" \
    > "${WORK_DIR}/sc004-squid-grep.log" 2>&1; then
  fail "SC-004: squid.conf reference found in dispatcher or main.go (see ${WORK_DIR}/sc004-squid-grep.log)"
else
  ok "SC-004: no squid.conf reference in dispatcher or main.go — broker does not route through agent network"
fi

note "Interactive portion (operator-attended on a running dev stack):"
note "  Attempt a direct curl to api.github.com from inside an agent container;"
note "  expect TCP_DENIED in 'docker logs garrison-egress-proxy'."
note "  Only the dispatcher (supervisor-side) can reach external providers."
note "Not asserted here — same operator-attended posture as M9/M10 SC-006 walks."

# -----------------------------------------------------------------------------
# SC-005 — credential isolation: the dispatcher's vault-scoped PAT never
# appears in any agent container's env, prompt, or context; vault-fail
# triggers fail-closed, not fallback to unscoped credential. (T009 unit)
# -----------------------------------------------------------------------------
echo
echo "== SC-005: credential isolation — vault-scoped PAT never in agent context (T009)"

run_step "SC-005: vault unavailable → fail-closed, no PostComment (TestHandleVaultUnavailableFailsClosed)" sc005-vault \
  go test -tags=integration -run 'TestHandleVaultUnavailableFailsClosed$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-005: PAT never appears in slog output (TestPostCommentNeverLogsPAT)" sc005-pat-log \
  go test -run 'TestPostCommentNeverLogsPAT$' ./internal/actionbroker/ -count=1 -timeout 120s

# -----------------------------------------------------------------------------
# SC-006 — exactly-once dispatch under restart + race: an approved action
# redelivered or surviving a supervisor restart executes exactly once —
# no double-post. (T010 chaos, FOR UPDATE SKIP LOCKED discipline)
# -----------------------------------------------------------------------------
echo
echo "== SC-006: exactly-once dispatch under restart + race (T010 chaos)"

run_step "SC-006: concurrent claim dispatches exactly once (TestConcurrentClaimDispatchesExactlyOnce)" sc006-concurrent \
  go test -tags=chaos -run 'TestConcurrentClaimDispatchesExactlyOnce$' ./internal/actionbroker/ -count=1 -timeout 600s

run_step "SC-006: restart mid-dispatch no double-post (TestRestartMidDispatchNoDoublePost)" sc006-restart \
  go test -tags=chaos -run 'TestRestartMidDispatchNoDoublePost$' ./internal/actionbroker/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-007 — immutable, reconstructable audit: a dispatched action
# reconstructs to agent_instance_id, payload, tier, approved_by, outcome
# — all immutable. (T011 integration — TestEndToEndApproveTierGitHubCommentBack)
# The same test was run in SC-001; run it again here explicitly named
# for the SC-007 assertion context.
# -----------------------------------------------------------------------------
echo
echo "== SC-007: immutable, reconstructable audit (T011 integration)"

run_step "SC-007: audit reconstructs agent_id + payload + tier + approved_by + outcome (TestEndToEndApproveTierGitHubCommentBack)" sc007-audit \
  go test -tags=integration -run 'TestEndToEndApproveTierGitHubCommentBack$' ./internal/actionbroker/ -count=1 -timeout 600s
# Note: this test was also run in SC-001; it serves double duty verifying both
# the request-to-pending lifecycle (SC-001) and the immutable audit trail (SC-007).

# -----------------------------------------------------------------------------
# SC-008 — threat model precedes code: docs/security/action-broker-threat-model.md
# is committed in git history before the first supervisor/internal/actionbroker/
# commit. (The M9 SC-007 / M10 SC-008 pattern, FR-030)
# git log -- path lists newest-first, so tail -1 gives the earliest commit.
# We verify by comparing commit dates: threat-model date < first
# actionbroker code date, using merge-base --is-ancestor.
# -----------------------------------------------------------------------------
echo
echo "== SC-008: threat-model commit precedes first internal/actionbroker/ commit (T001 before T006)"

THREAT_SHA="$(git -C "${REPO_ROOT}" log --format='%H' -n1 \
  -- docs/security/action-broker-threat-model.md)"
FIRST_BROKER_SHA="$(git -C "${REPO_ROOT}" log --format='%H' \
  -- supervisor/internal/actionbroker/ | tail -1)"

if [[ -z "${THREAT_SHA}" ]]; then
  fail "SC-008: docs/security/action-broker-threat-model.md not found in git history"
elif [[ -z "${FIRST_BROKER_SHA}" ]]; then
  fail "SC-008: no supervisor/internal/actionbroker/ commits found in history"
elif [[ "${THREAT_SHA}" == "${FIRST_BROKER_SHA}" ]]; then
  ok "SC-008: threat-model and first actionbroker code in same commit — ordering holds"
elif git -C "${REPO_ROOT}" merge-base --is-ancestor "${THREAT_SHA}" "${FIRST_BROKER_SHA}"; then
  ok "SC-008: threat-model (${THREAT_SHA:0:7}) is an ancestor of the first actionbroker commit (${FIRST_BROKER_SHA:0:7}) — ordering holds"
else
  fail "SC-008: threat-model commit (${THREAT_SHA:0:7}) does NOT precede first internal/actionbroker/ commit (${FIRST_BROKER_SHA:0:7})"
fi

# -----------------------------------------------------------------------------
# SC-009 — no regression of sealed surfaces: finalize schemas, vault rules,
# agent roles, MemPalace wiring, the container model, MCPJungle naming,
# and the M10 ingress surface carry unchanged; M11 adds one sealed verb,
# the tier table, the dispatcher, the Outbox, and a threat model.
#
# Verified by:
#   - go mod tidy produces no change to go.mod/go.sum (zero new deps)
#   - bunx tsc --noEmit passes (TS compile clean)
#   - all M1–M10 regression suites pass (default + integration + chaos)
#   - squid.conf diff is empty (confirmed again under SC-009 for clarity)
#   - TestVerbsRegistryMatchesEnumeration passes (12 verbs, additive only)
#   - TestVerbsSlicesDisjoint passes (server-action verbs disjoint from Verbs)
#   - ARCHITECTURE.md M11 amendment pin tests pass (T013)
#
# These runs also write the three coverage profiles the coverage probe below
# consumes (coverage.out, int-coverage.out, chaos-coverage.out).
#
# Two local-host accommodations (same as M9/M10 acceptance scripts):
#   - Coverage runs target only packages with test files; no-test mains
#     (mockclaude, embed-agent-md, vaultlog/cmd) contribute zero lines to
#     CI's profiles and can trip go's on-demand covdata build on a trimmed
#     module-cache toolchain. A plain go build ./... covers them.
#   - -p 1 on container-backed suites serialises package binaries to avoid
#     exhausting docker's bridge address pool when parallel packages each
#     provision testcontainers (M7.1 acceptance run-1 finding).
# -----------------------------------------------------------------------------
echo
echo "== SC-009: no new deps + full M1–M10 regression + TypeScript compile (T013/T014)"

# go mod tidy must leave go.mod and go.sum unchanged
run_step "SC-009: go mod tidy produces no change (zero new deps)" sc009-tidy \
  bash -c 'cp go.mod go.mod.bak && cp go.sum go.sum.bak && go mod tidy && diff go.mod go.mod.bak && diff go.sum go.sum.bak && rm go.mod.bak go.sum.bak'

# TypeScript must compile clean
if (cd "${REPO_ROOT}/dashboard" && bunx tsc --noEmit > "${WORK_DIR}/sc009-tsc.log" 2>&1); then
  ok "SC-009: bunx tsc --noEmit passes"
else
  fail "SC-009: bunx tsc --noEmit failed (see ${WORK_DIR}/sc009-tsc.log)"
fi

# Every package (including no-test mains) must build
run_step "SC-009: every package builds (go build ./...)" sc009-build \
  go build ./...

# Sealed-verb registry: exactly 12 verbs, additive only
run_step "SC-009: TestVerbsRegistryMatchesEnumeration (12 verbs, sealed-verb additive)" sc009-verbs-enum \
  go test -run 'TestVerbsRegistryMatchesEnumeration$' ./internal/garrisonmutate/ -count=1 -timeout 60s

run_step "SC-009: TestVerbsSlicesDisjoint (agent vs server-action verbs disjoint)" sc009-verbs-disjoint \
  go test -run 'TestVerbsSlicesDisjoint$' ./internal/garrisonmutate/ -count=1 -timeout 60s

# Collect packages with tests per tag set
tested_pkgs() {
  local tags="$1"
  local -a tagflag=()
  [[ -n "${tags}" ]] && tagflag=(-tags="${tags}")
  (cd "${SUP_DIR}" && go list "${tagflag[@]}" \
     -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | sed '/^$/d')
  return "$?"
}

mapfile -t DEFAULT_PKGS    < <(tested_pkgs "")
mapfile -t INTEGRATION_PKGS < <(tested_pkgs "integration")
mapfile -t CHAOS_PKGS       < <(tested_pkgs "chaos")

if (( ${#DEFAULT_PKGS[@]} == 0 || ${#INTEGRATION_PKGS[@]} == 0 || ${#CHAOS_PKGS[@]} == 0 )); then
  fail "SC-009: go list found no packages with tests for some tag set (toolchain problem?)"
fi

run_step "SC-009: default suite green (M1–M11 unit tests; coverage.out)" sc009-default \
  go test -coverprofile=coverage.out -covermode=atomic "${DEFAULT_PKGS[@]}" -count=1 -timeout 1200s

run_step "SC-009: integration suite green (M1–M11 integration tests; int-coverage.out)" sc009-integration \
  go test -tags=integration -coverprofile=int-coverage.out -covermode=atomic "${INTEGRATION_PKGS[@]}" -count=1 -timeout 3000s -p 1

run_step "SC-009: chaos suite green (M1–M11 chaos tests; chaos-coverage.out)" sc009-chaos \
  go test -tags=chaos -coverprofile=chaos-coverage.out -covermode=atomic "${CHAOS_PKGS[@]}" -count=1 -timeout 3000s -p 1

# Lint clean
run_step "SC-009: gofmt -l produces no output (formatting clean)" sc009-gofmt \
  bash -c 'out="$(gofmt -l .)"; [ -z "${out}" ] || { echo "${out}"; exit 1; }'

run_step "SC-009: go vet ./... clean" sc009-govet \
  go vet ./...

# ARCHITECTURE.md M11 amendment pin tests (T013)
if (cd "${REPO_ROOT}/dashboard" && bun run test -- tests/architecture-amendment.test.ts > "${WORK_DIR}/sc009-arch-test.log" 2>&1); then
  ok "SC-009: ARCHITECTURE.md M11 amendment pin tests pass"
else
  fail "SC-009: ARCHITECTURE.md M11 amendment pin tests failed (see ${WORK_DIR}/sc009-arch-test.log)"
fi

# Squid.conf unchanged (sealed — already checked in SC-004, confirmed here)
BASE_REF2="origin/main"
git -C "${REPO_ROOT}" rev-parse -q --verify "${BASE_REF2}" >/dev/null 2>&1 || BASE_REF2="main"
MERGE_BASE2="$(git -C "${REPO_ROOT}" merge-base "${BASE_REF2}" HEAD 2>/dev/null || true)"
if [[ -n "${MERGE_BASE2}" ]]; then
  SQUID_SC9_DIFF="$(git -C "${REPO_ROOT}" diff --name-only \
    "${MERGE_BASE2}..HEAD" -- supervisor/egress/squid.conf 2>/dev/null)"
  if [[ -z "${SQUID_SC9_DIFF}" ]]; then
    ok "SC-009: supervisor/egress/squid.conf sealed — broker-is-only-door invariant holds"
  else
    fail "SC-009: supervisor/egress/squid.conf modified on this branch — sealed surface violated"
  fi
else
  note "SC-009: squid.conf sealed check skipped (merge-base unavailable)"
fi

# -----------------------------------------------------------------------------
# Coverage probe — ≥82% statement coverage on M11's new Go code (lines
# this branch added/changed vs the merge-base with main), excluding
# _test.go files and the sonar.coverage.exclusions paths. Diff-aware to
# match Sonar's new-code semantics: only profile blocks overlapping
# M11-changed lines count, so pre-existing uncovered lines in modified
# files don't distort the number. A statement counts as covered if any
# of the three suites reached it (Sonar merges the same three profiles).
# Per-file breakdown prints so a shortfall points at the file to top up.
# -----------------------------------------------------------------------------
echo
echo "== Coverage probe: M11 new-code ≥ ${COVERAGE_THRESHOLD}% (M6 retro #7)"

coverage_probe() {
  local base_ref="origin/main"
  git -C "${REPO_ROOT}" rev-parse -q --verify "${base_ref}" >/dev/null 2>&1 || base_ref="main"
  local base
  base="$(git -C "${REPO_ROOT}" merge-base "${base_ref}" HEAD)" || return 1
  note "new-code window: ${base:0:7} (merge-base with ${base_ref}) .. HEAD"

  # sonar.coverage.exclusions, flattened to one glob per line.
  local exclusions
  exclusions="$(awk '
    /^sonar\.coverage\.exclusions=/ { f = 1 }
    f {
      line = $0
      sub(/^sonar\.coverage\.exclusions=/, "", line)
      cont = ($0 ~ /\\$/)
      gsub(/[\\ ]/, "", line)
      if (line != "") print line
      if (!cont) f = 0
    }' "${REPO_ROOT}/sonar-project.properties" | tr ',' '\n' | sed '/^$/d')"

  local f pat skip
  local -a new_files=()
  while IFS= read -r f; do
    [[ "$f" == *_test.go ]] && continue
    skip=0
    while IFS= read -r pat; do
      [[ -z "${pat}" ]] && continue
      # shellcheck disable=SC2254 — pat is a glob on purpose.
      case "$f" in
        ${pat}) skip=1; break ;;
        *) ;;
      esac
    done <<<"${exclusions}"
    (( skip )) && continue
    new_files+=("$f")
  done < <(git -C "${REPO_ROOT}" diff --name-only --diff-filter=ACMR \
             "${base}..HEAD" -- 'supervisor/*.go' 'supervisor/**/*.go')

  if (( ${#new_files[@]} == 0 )); then
    note "no in-scope new Go files in the window — nothing to probe"
    return 0
  fi

  local module
  module="$(head -1 "${SUP_DIR}/go.mod" | awk '{print $2}')"

  # Changed-line ranges per file, as "<module-path> <start> <end>"
  # tuples parsed from a zero-context diff (the +c,d hunk headers).
  local ranges="${WORK_DIR}/m11-newcode-ranges.txt"
  git -C "${REPO_ROOT}" diff -U0 --diff-filter=ACMR "${base}..HEAD" \
      -- "${new_files[@]}" \
    | awk -v module="${module}" '
        /^\+\+\+ b\// {
          file = substr($2, 3)
          sub(/^supervisor\//, "", file)
          file = module "/" file
          next
        }
        /^@@/ {
          # @@ -a,b +c,d @@ — new-side range is c..c+d-1 (d defaults 1).
          plus = $3
          sub(/^\+/, "", plus)
          n = split(plus, p, ",")
          start = p[1]
          len = (n > 1 ? p[2] : 1)
          if (len > 0) print file, start, start + len - 1
        }' > "${ranges}"

  if [[ ! -s "${ranges}" ]]; then
    note "no added/changed lines in the in-scope files — nothing to probe"
    return 0
  fi

  local merged="${WORK_DIR}/m11-newcode-merged.cov"
  : > "${merged}"
  local prof have_prof=0
  for prof in coverage.out int-coverage.out chaos-coverage.out; do
    if [[ -f "${SUP_DIR}/${prof}" ]]; then
      # tr -d '\0' strips the sparse-allocation NUL bytes that Go's coverage
      # writer occasionally leaves in the file when -p 1 serialises multi-
      # package test runs. grep treats a NUL-containing file as binary and
      # emits zero lines; the tr pass makes it safe for grep/awk.
      tr -d '\0' < "${SUP_DIR}/${prof}" | grep -v '^mode:' >> "${merged}"
      have_prof=1
    fi
  done
  if (( ! have_prof )); then
    echo "no coverage profiles found — did the SC-009 runs complete?" >&2
    return 1
  fi

  awk -v threshold="${COVERAGE_THRESHOLD}" '
    NR == FNR {
      # ranges file: <file> <start> <end>
      nr = ++nranges[$1]
      rstart[$1 SUBSEP nr] = $2
      rend[$1 SUBSEP nr] = $3
      next
    }
    {
      # profile line: <file>:<L1>.<C1>,<L2>.<C2> <numStmt> <count>
      colon = index($1, ":")
      file = substr($1, 1, colon - 1)
      if (!(file in nranges)) next
      pos = substr($1, colon + 1)
      split(pos, seg, ",")
      split(seg[1], s1, "."); l1 = s1[1] + 0
      split(seg[2], s2, "."); l2 = s2[1] + 0
      hit = 0
      for (i = 1; i <= nranges[file]; i++)
        if (l1 <= rend[file SUBSEP i] && l2 >= rstart[file SUBSEP i]) { hit = 1; break }
      if (!hit) next
      key = $1
      stmts[key] = $2
      fileof[key] = file
      if ($3 > 0) covered[key] = 1
    }
    END {
      tot = 0; cov = 0
      for (k in stmts) {
        tot += stmts[k]
        ftot[fileof[k]] += stmts[k]
        if (k in covered) { cov += stmts[k]; fcov[fileof[k]] += stmts[k] }
      }
      for (f in nranges) if (!(f in ftot))
        printf "      n/a  %s  (no instrumented statements on changed lines)\n", f
      for (f in ftot)
        printf "   %5.1f%%  %s\n", 100 * fcov[f] / ftot[f], f
      if (tot == 0) {
        print "no profiled statements on any changed line" > "/dev/stderr"
        exit 1
      }
      pct = 100 * cov / tot
      printf "  M11 new-code statement coverage: %.1f%% (%d/%d statements, threshold %d%%)\n", \
             pct, cov, tot, threshold
      exit (pct >= threshold ? 0 : 1)
    }' "${ranges}" "${merged}"
}

if coverage_probe; then
  ok "coverage probe: M11 new-code coverage ≥ ${COVERAGE_THRESHOLD}%"
else
  fail "coverage probe: M11 new-code coverage below ${COVERAGE_THRESHOLD}% — top up Go-side tests (no production-code changes)"
fi

# -----------------------------------------------------------------------------
# Sonar new-issues pre-clearance (M8 T022 / M9 T020 / M10 T017 pattern)
# — when this branch's PR has a SonarCloud analysis, assert zero unresolved
# new issues and zero TO_REVIEW hotspots. Before the push / before CI's
# sonarcloud job lands there is nothing to query: report pending and re-run
# after. Set SONAR_TOKEN for authenticated queries (public reads work without).
# -----------------------------------------------------------------------------
echo
echo "== Sonar pre-clearance: zero unresolved new issues on the PR"

json_total() {
  # First "total":N in a Sonar JSON payload (paging.total on both the
  # issues and hotspots endpoints). Avoids a jq dependency.
  sed -n 's/.*"total":\([0-9]\{1,\}\).*/\1/p' | head -1
  return "$?"
}

sonar_preclearance() {
  local -a auth=()
  [[ -n "${SONAR_TOKEN:-}" ]] && auth=(-u "${SONAR_TOKEN}:")

  local pr=""
  if command -v gh >/dev/null 2>&1; then
    pr="$(cd "${REPO_ROOT}" && gh pr view --json number -q .number 2>/dev/null || true)"
  fi
  if [[ -z "${pr}" ]]; then
    note "no PR for this branch yet — Sonar analyzes in CI after the push."
    note "Re-run this script once the PR's SonarCloud job has reported."
    return 2 # pending
  fi

  if ! curl -fsS "${auth[@]}" \
      "${SONAR_HOST}/api/project_pull_requests/list?project=${SONAR_PROJECT}" \
      2>/dev/null | grep -q "\"key\":\"${pr}\""; then
    note "PR #${pr} exists but has no Sonar analysis yet — pending."
    note "Re-run this script once CI's sonarcloud job has reported."
    return 2 # pending
  fi

  local issues hotspots
  issues="$(curl -fsS "${auth[@]}" \
    "${SONAR_HOST}/api/issues/search?componentKeys=${SONAR_PROJECT}&pullRequest=${pr}&resolved=false&ps=1" \
    2>/dev/null | json_total)"
  hotspots="$(curl -fsS "${auth[@]}" \
    "${SONAR_HOST}/api/hotspots/search?projectKey=${SONAR_PROJECT}&pullRequest=${pr}&status=TO_REVIEW&ps=1" \
    2>/dev/null | json_total)"
  if [[ -z "${issues}" || -z "${hotspots}" ]]; then
    echo "Sonar API query failed (issues='${issues:-}' hotspots='${hotspots:-}')" >&2
    return 1
  fi
  note "PR #${pr}: ${issues} unresolved new issue(s), ${hotspots} TO_REVIEW hotspot(s)"
  [[ "${issues}" == "0" && "${hotspots}" == "0" ]]
}

sonar_preclearance
case $? in
  0) ok "Sonar pre-clearance: zero unresolved new issues + zero TO_REVIEW hotspots" ;;
  2) note "Sonar pre-clearance pending (no PR analysis yet) — not counted as a step" ;;
  *) fail "Sonar pre-clearance: unresolved new issues or hotspots on the PR — clear them (focused patches, no new features), then re-run" ;;
esac

# -----------------------------------------------------------------------------
# Summary
# -----------------------------------------------------------------------------
echo
echo "============================================"
echo "M11 acceptance walk: ${PASS} pass / ${FAIL} fail"
echo "Artifacts kept at ${WORK_DIR}"
echo "============================================"
if (( FAIL > 0 )); then
  printf 'FAIL: %s\n' "${FAILURES[@]}" >&2
  echo >&2
  echo "Per tasks.md T014: patch the relevant earlier task's files (no new" >&2
  echo "features), then re-run this script from the top." >&2
  exit 1
fi
exit 0
