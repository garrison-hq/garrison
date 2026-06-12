#!/usr/bin/env bash
# scripts/m10-acceptance.sh — scripted walk of every M10 success criterion
# (SC-001..SC-009) from specs/022-m10-ingress-connectors/spec.md, plus the
# two pre-PR-push clearance probes from tasks.md T017:
#   - new-code coverage probe (≥82% on Go-side new code, M6 retro #7)
#   - SonarCloud new-issues pre-clearance (M8/M9 T020 pattern)
#
# SC → verifying step mapping (tasks.md T017):
#   SC-001  TestIngress_IssueOpened_CreatesOneTicket +
#           TestIngress_PullRequestReviewRequested_CreatesOneTicket (T013,
#           integration)
#   SC-002  TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket (T014,
#           chaos)
#   SC-003  TestIngress_BadSignature_NoTicket_401 (T013, integration) +
#           TestVerifyGitHubSignature_* (T009, unit)
#   SC-004  TestIngress_BotSender_NoTicket_200 (T013, integration) +
#           TestHandler_Ping_200_NoTicket (T011, unit) +
#           TestHandler_NonActionableSubtype_200_NoTicket (T011, unit)
#   SC-005  TestIngress_BurstExceedsCap_BoundedTickets (T014, chaos) +
#           TestIngress_IngressTicketCountsAgainstDeptBudget (T013,
#           integration; primary FR-603 verifier)
#   SC-006  operator-attended dashboard walk: /admin/connectors surface,
#           ingress-origin chip + external link on a seeded ticket; the
#           data-layer correctness is already asserted by
#           TestIngress_IssueOpened_CreatesOneTicket (T013)
#   SC-007  inbound-only boundary assertion via git grep — no outbound
#           GitHub / mail / external_action calls in internal/ingress/
#   SC-008  git-log assertion that the threat-model commit (T001) precedes
#           the first internal/ingress/ commit (T004) (the M9 SC-007 pattern)
#   SC-009  go mod tidy produces no change to go.mod/go.sum; bunx tsc
#           --noEmit passes; full M1–M9 regression suites pass
#
# Acceptance is interpreted strictly: any failed step fails the run
# (exit 1). The operator-attended item (SC-006 dashboard walk) is printed
# as instructions, not asserted — same posture as M9's SC-006 and M7.1's
# SC-007 operator-attended walk. The Sonar probe is asserted when a PR
# analysis exists; before the branch is pushed it reports "pending" and the
# operator re-runs this script (or just that step) after CI's SonarCloud
# job lands.
#
# Per tasks.md T017: if a step fails, patch the relevant earlier task's
# files (focused patch, no new features), then re-run this script from
# the top.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUP_DIR="${REPO_ROOT}/supervisor"
WORK_DIR="$(mktemp -d /tmp/m10-acceptance.XXXXXX)"

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

echo "M10 acceptance walk — ${REPO_ROOT}"
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
# SC-001 — signature-valid GitHub issues:opened + pull_request:review_requested
# produce exactly one ticket each, with connector provenance in metadata,
# ticket-created notify fires, handler returns 202 (T013, integration).
# -----------------------------------------------------------------------------
echo
echo "== SC-001: golden path — issues:opened + pull_request:review_requested (T013)"

run_step "SC-001: issues:opened creates one ticket (TestIngress_IssueOpened_CreatesOneTicket)" sc001-issue \
  go test -tags=integration -run 'TestIngress_IssueOpened_CreatesOneTicket$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-001: pull_request:review_requested creates one ticket (TestIngress_PullRequestReviewRequested_CreatesOneTicket)" sc001-pr \
  go test -tags=integration -run 'TestIngress_PullRequestReviewRequested_CreatesOneTicket$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-001: null issue body falls back gracefully (TestIngress_NullIssueBody_GracefulFallback)" sc001-null \
  go test -tags=integration -run 'TestIngress_NullIssueBody_GracefulFallback$' ./internal/ingress/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-002 — same delivery POSTed twice produces exactly one ticket, including
# the concurrent race via unique-constraint-on-insert, not a pre-check SELECT
# (T014, chaos; US2-AS2).
# -----------------------------------------------------------------------------
echo
echo "== SC-002: concurrent-redelivery race yields one ticket (T014, chaos)"

run_step "SC-002: concurrent POSTs of one GUID → exactly one ticket (TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket)" sc002 \
  go test -tags=chaos -run 'TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-002: serial redelivery creates no second ticket (TestIngress_SerialRedelivery_NoSecondTicket)" sc002-serial \
  go test -tags=integration -run 'TestIngress_SerialRedelivery_NoSecondTicket$' ./internal/ingress/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-003 — bad or missing signature creates no ticket, returns 401, verified
# over raw body with constant-time comparison (T013 integration +
# T009 unit, FR-300/FR-301/SR1).
# -----------------------------------------------------------------------------
echo
echo "== SC-003: bad/missing signature rejected fail-closed (T013 + T009)"

run_step "SC-003: forged signature against live stack → 401, zero tickets (TestIngress_BadSignature_NoTicket_401)" sc003-live \
  go test -tags=integration -run 'TestIngress_BadSignature_NoTicket_401$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-003: HMAC unit — valid digest succeeds (TestVerifyGitHubSignature_Valid)" sc003-hmac-valid \
  go test -run 'TestVerifyGitHubSignature_Valid$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-003: HMAC unit — wrong digest → ErrBadSignature (TestVerifyGitHubSignature_Mismatch)" sc003-hmac-mismatch \
  go test -run 'TestVerifyGitHubSignature_Mismatch$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-003: HMAC unit — missing sha256= prefix → ErrBadSignature (TestVerifyGitHubSignature_MissingPrefix)" sc003-hmac-prefix \
  go test -run 'TestVerifyGitHubSignature_MissingPrefix$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-003: HMAC unit — empty header fails closed (TestVerifyGitHubSignature_EmptyHeader)" sc003-hmac-empty \
  go test -run 'TestVerifyGitHubSignature_EmptyHeader$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-003: HMAC unit — raw-body exact (TestVerifyGitHubSignature_RawBodyExact)" sc003-hmac-raw \
  go test -run 'TestVerifyGitHubSignature_RawBodyExact$' ./internal/ingress/ -count=1 -timeout 60s

# -----------------------------------------------------------------------------
# SC-004 — bot senders, unsubscribed event types, non-actionable action
# subtypes, and ping events produce no tickets, each returning its
# documented status code; no agent-prompt involvement (T013 integration +
# T011 unit, FR-400/FR-401/FR-403/SR6).
# -----------------------------------------------------------------------------
echo
echo "== SC-004: noise filter discards bots, non-actionable subtypes, ping (T013 + T011)"

run_step "SC-004: bot-sourced issues:opened → 200, no ticket (TestIngress_BotSender_NoTicket_200)" sc004-bot-live \
  go test -tags=integration -run 'TestIngress_BotSender_NoTicket_200$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-004: ping event → 200, no ticket (TestHandler_Ping_200_NoTicket)" sc004-ping \
  go test -run 'TestHandler_Ping_200_NoTicket$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-004: issues:labeled discarded (TestHandler_NonActionableSubtype_200_NoTicket)" sc004-nonaction \
  go test -run 'TestHandler_NonActionableSubtype_200_NoTicket$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-004: bot sender type discarded (TestGitHubFilter_BotSenderTypeDiscarded)" sc004-bot-unit \
  go test -run 'TestGitHubFilter_BotSenderTypeDiscarded$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-004: bot login suffix discarded (TestGitHubFilter_BotSenderLoginSuffixDiscarded)" sc004-bot-login \
  go test -run 'TestGitHubFilter_BotSenderLoginSuffixDiscarded$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-004: issues action gate (TestGitHubFilter_IssuesActionGate)" sc004-issues-gate \
  go test -run 'TestGitHubFilter_IssuesActionGate$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-004: pull_request action gate (TestGitHubFilter_PullRequestActionGate)" sc004-pr-gate \
  go test -run 'TestGitHubFilter_PullRequestActionGate$' ./internal/ingress/ -count=1 -timeout 60s

# -----------------------------------------------------------------------------
# SC-005 — burst cap bounds fan-out: over-cap → 429 before ticket insert;
# breach writes M6 throttle evidence; later under-cap redelivery dedups
# correctly; ingress tickets count against the dept M8 weekly budget
# (T014 chaos + T013 integration; FR-600/FR-601/FR-602/FR-603).
# -----------------------------------------------------------------------------
echo
echo "== SC-005: rate cap bounds fan-out + M6 evidence + FR-603 dept budget (T014 + T013)"

run_step "SC-005: burst cap bounds tickets + 429 + throttle evidence (TestIngress_BurstExceedsCap_BoundedTickets)" sc005-burst \
  go test -tags=chaos -run 'TestIngress_BurstExceedsCap_BoundedTickets$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-005: ingress ticket counts against dept M8 weekly budget (TestIngress_IngressTicketCountsAgainstDeptBudget)" sc005-budget \
  go test -tags=integration -run 'TestIngress_IngressTicketCountsAgainstDeptBudget$' ./internal/ingress/ -count=1 -timeout 600s

run_step "SC-005: rate-cap unit — allows within burst (TestRateCap_AllowsWithinBurst)" sc005-rc-allow \
  go test -run 'TestRateCap_AllowsWithinBurst$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-005: rate-cap unit — rejects over burst (TestRateCap_RejectsOverBurst)" sc005-rc-reject \
  go test -run 'TestRateCap_RejectsOverBurst$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-005: rate-cap unit — per-connector isolation (TestRateCap_PerConnectorIsolation)" sc005-rc-iso \
  go test -run 'TestRateCap_PerConnectorIsolation$' ./internal/ingress/ -count=1 -timeout 60s

run_step "SC-005: throttle evidence written (TestFireIngressRateCap_WritesEvidence)" sc005-evidence \
  go test -tags=integration -run 'TestFireIngressRateCap_WritesEvidence$' ./internal/throttle/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-006 — dashboard surfaces: ingress-origin chip + external link +
# connector-status surface. Data-layer correctness is asserted by SC-001's
# TestIngress_IssueOpened_CreatesOneTicket (the Go suite pins
# tickets.origin='ingress' and the metadata provenance keys). The
# dashboard rendering itself requires an operator-attended walk.
# -----------------------------------------------------------------------------
echo
echo "== SC-006: dashboard surfaces (operator-attended)"
note "Data-layer correctness is already pinned by SC-001's integration tests."
note "Operator-attended walk required on a seeded dev stack:"
note "  1. Open /admin/connectors — verify per-connector: last delivery received,"
note "     accepted count, bad-signature rejection count, and rate-cap breach count."
note "  2. Open the kanban — find a ticket with origin='ingress';"
note "     verify the 'gh: <connector>' chip appears distinct from operator/"
note "     agent/schedule chips; click chip → GitHub URL opens."
note "  3. Open the ticket detail — verify the external source link is shown."
note "  4. Confirm the four origins (operator, agent, schedule, ingress) are"
note "     visually distinguishable."
note "  5. GET /ingress/status without auth cookie → 401 (cookie-auth enforced)."
note "Not asserted here — same operator-attended posture as M9's SC-006 walk."

# -----------------------------------------------------------------------------
# SC-007 — inbound-only boundary: nothing in internal/ingress/ posts back
# to GitHub, sends mail, or mutates external state; no agent MCP verb
# exposes ingress (FR-700, FR-100).
# -----------------------------------------------------------------------------
echo
echo "== SC-007: inbound-only boundary — no outbound action in internal/ingress/"

if grep -rn \
    'github\.com/google/go-github\|PostComment\|SendMail\|external_action' \
    "${SUP_DIR}/internal/ingress/" \
    > "${WORK_DIR}/sc007-grep.log" 2>&1; then
  fail "SC-007: outbound action pattern found in internal/ingress/ (see ${WORK_DIR}/sc007-grep.log)"
else
  ok "SC-007: inbound-only boundary confirmed — grep returned empty"
fi

# No agent-reachable connector verbs in garrisonmutate or claudeproto
if grep -rn \
    'ingress\|connector\|webhook' \
    "${SUP_DIR}/internal/garrisonmutate/" \
    > "${WORK_DIR}/sc007-mutate-grep.log" 2>&1; then
  fail "SC-007: connector keyword found in garrisonmutate — check ${WORK_DIR}/sc007-mutate-grep.log for agent-reachable verbs"
else
  ok "SC-007: no connector keyword in garrisonmutate — agent inbound-only boundary holds"
fi

# -----------------------------------------------------------------------------
# SC-008 — threat-model commit (T001) precedes the first connector-code
# commit (T004) in git history (the M9 SC-007 pattern, FR-800).
# git log -- path lists newest-first, so the threat-model commit must
# appear AFTER (below) the last ingress code commit when printed newest-first.
# We verify by comparing commit dates: threat-model date < first ingress code date.
# -----------------------------------------------------------------------------
echo
echo "== SC-008: threat-model commit precedes first internal/ingress/ commit (T001 before T004)"

THREAT_SHA="$(git -C "${REPO_ROOT}" log --format='%H' -n1 \
  -- docs/security/ingress-threat-model.md)"
FIRST_INGRESS_SHA="$(git -C "${REPO_ROOT}" log --format='%H' \
  -- supervisor/internal/ingress/ | tail -1)"

if [[ -z "${THREAT_SHA}" ]]; then
  fail "SC-008: docs/security/ingress-threat-model.md not found in git history"
elif [[ -z "${FIRST_INGRESS_SHA}" ]]; then
  fail "SC-008: no supervisor/internal/ingress/ commits found in history"
elif git -C "${REPO_ROOT}" merge-base --is-ancestor "${THREAT_SHA}" "${FIRST_INGRESS_SHA}"; then
  ok "SC-008: threat-model (${THREAT_SHA:0:7}) is an ancestor of the first ingress commit (${FIRST_INGRESS_SHA:0:7}) — ordering holds"
elif [[ "${THREAT_SHA}" == "${FIRST_INGRESS_SHA}" ]]; then
  ok "SC-008: threat-model and first ingress code in same commit — ordering holds"
else
  fail "SC-008: threat-model commit (${THREAT_SHA:0:7}) does NOT precede first internal/ingress/ commit (${FIRST_INGRESS_SHA:0:7})"
fi

# -----------------------------------------------------------------------------
# SC-009 — no new Go or TS dependencies (locked-deps discipline); all
# M1–M9 regression suites pass untouched. These runs also write the three
# coverage profiles the coverage probe below consumes (coverage.out,
# int-coverage.out, chaos-coverage.out).
#
# Two local-host accommodations (same as M9 acceptance script):
#   - Coverage runs target only packages with test files; no-test mains
#     (mockclaude, embed-agent-md, vaultlog/cmd) contribute zero lines
#     to CI's profiles and can trip go's on-demand covdata build on a
#     trimmed module-cache toolchain. A plain go build ./... covers them.
#   - -p 1 on container-backed suites serialises package binaries to
#     avoid exhausting docker's bridge address pool when parallel packages
#     each provision testcontainers (M7.1 acceptance run-1 finding).
# -----------------------------------------------------------------------------
echo
echo "== SC-009: no new deps + full M1–M9 regression + TypeScript compile (T016/T017)"

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

run_step "SC-009: default suite green (M1–M10 unit tests; coverage.out)" sc009-default \
  go test -coverprofile=coverage.out -covermode=atomic "${DEFAULT_PKGS[@]}" -count=1 -timeout 1200s

run_step "SC-009: integration suite green (M1–M10 integration tests; int-coverage.out)" sc009-integration \
  go test -tags=integration -coverprofile=int-coverage.out -covermode=atomic "${INTEGRATION_PKGS[@]}" -count=1 -timeout 3000s -p 1

run_step "SC-009: chaos suite green (M1–M10 chaos tests; chaos-coverage.out)" sc009-chaos \
  go test -tags=chaos -coverprofile=chaos-coverage.out -covermode=atomic "${CHAOS_PKGS[@]}" -count=1 -timeout 3000s -p 1

# architecture-amendment pin tests (T016)
if (cd "${REPO_ROOT}/dashboard" && bun run test -- tests/architecture-amendment.test.ts > "${WORK_DIR}/sc009-arch-test.log" 2>&1); then
  ok "SC-009: ARCHITECTURE.md M10 amendment pin tests pass"
else
  fail "SC-009: ARCHITECTURE.md M10 amendment pin tests failed (see ${WORK_DIR}/sc009-arch-test.log)"
fi

# -----------------------------------------------------------------------------
# Coverage probe — ≥82% statement coverage on M10's new Go code (lines
# this branch added/changed vs the merge-base with main), excluding
# _test.go files and the sonar.coverage.exclusions paths. Diff-aware to
# match Sonar's new-code semantics: only profile blocks overlapping
# M10-changed lines count, so pre-existing uncovered lines in modified
# files don't distort the number. A statement counts as covered if any
# of the three suites reached it (Sonar merges the same three profiles).
# Per-file breakdown prints so a shortfall points at the file to top up.
# -----------------------------------------------------------------------------
echo
echo "== Coverage probe: M10 new-code ≥ ${COVERAGE_THRESHOLD}% (M6 retro #7)"

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
  local ranges="${WORK_DIR}/m10-newcode-ranges.txt"
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

  local merged="${WORK_DIR}/m10-newcode-merged.cov"
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
      printf "  M10 new-code statement coverage: %.1f%% (%d/%d statements, threshold %d%%)\n", \
             pct, cov, tot, threshold
      exit (pct >= threshold ? 0 : 1)
    }' "${ranges}" "${merged}"
}

if coverage_probe; then
  ok "coverage probe: M10 new-code coverage ≥ ${COVERAGE_THRESHOLD}%"
else
  fail "coverage probe: M10 new-code coverage below ${COVERAGE_THRESHOLD}% — top up Go-side tests (no production-code changes)"
fi

# -----------------------------------------------------------------------------
# Sonar new-issues pre-clearance (M8 T022 / M9 T020 pattern) — when this
# branch's PR has a SonarCloud analysis, assert zero unresolved new issues
# and zero TO_REVIEW hotspots. Before the push / before CI's sonarcloud
# job lands there is nothing to query: report pending and re-run after.
# Set SONAR_TOKEN for authenticated queries (public reads work without).
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
echo "M10 acceptance walk: ${PASS} pass / ${FAIL} fail"
echo "Artifacts kept at ${WORK_DIR}"
echo "============================================"
if (( FAIL > 0 )); then
  printf 'FAIL: %s\n' "${FAILURES[@]}" >&2
  echo >&2
  echo "Per tasks.md T017: patch the relevant earlier task's files (no new" >&2
  echo "features), then re-run this script from the top." >&2
  exit 1
fi
exit 0
