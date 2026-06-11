#!/usr/bin/env bash
# scripts/m9-acceptance.sh — scripted walk of every M9 success criterion
# (SC-001..SC-010) from specs/021-m9-scheduled-wakeups/spec.md, plus the
# two pre-PR-push clearance probes from tasks.md T020:
#   - new-code coverage probe (≥82% on Go-side new code, M6 retro #7)
#   - SonarCloud new-issues pre-clearance (M8 T022 pattern)
#
# SC → verifying step mapping (tasks.md T020):
#   SC-001  TestTicketModeGoldenPath (T016, integration)
#   SC-002  TestOneshotGoldenPath (T016, integration)
#   SC-003  TestRecoveryCollapseFiresOnce (T017, integration)
#   SC-004  TestConcurrentClaimSingleFiring (T018, chaos)
#   SC-005  T006/T007 gate tests: TestTickOnceGateDeferredWritesEvidence
#           (integration) + TestSpawnOneshotGateDeferUpdatesRun /
#           TestSpawnOneshotRetryAfterGateClearsToFired (integration) +
#           TestDeptWeeklyDeferDetailRendersDecision (unit — the
#           ticket-mode dept-weekly evidence discipline)
#   SC-006  seeded-stack dashboard CRUD walk — operator-attended (the
#           row shapes the dashboard reads/writes are pinned by the
#           T016/T017 Go suites; the walk itself needs a human at
#           /admin/recurring-jobs)
#   SC-007  git-log assertion that T010's threat-model amendment commit
#           precedes T011's verb commit, + the per-turn ceiling tests
#           (T012, unit); the live chat-session walk is
#           operator-attended
#   SC-008  TestZeroIdleCost (T017, integration)
#   SC-009  T004/T011/T013 validation tests: TestValidateTask* (unit +
#           integration), TestCreateScheduledTaskRejects* (integration),
#           TestScheduleValidate* (unit)
#   SC-010  full default + integration + chaos regression run (also
#           produces the coverage profiles the probe consumes)
#
# Acceptance is interpreted strictly: any failed step fails the run
# (exit 1). Operator-attended items (SC-006 walk, SC-007 chat walk) are
# printed as instructions, not asserted — same posture as the M7.1
# script's SC-007 and the M8 script's soak items. The Sonar probe is
# asserted when a PR analysis exists; before the branch is pushed it
# reports "pending" and the operator re-runs this script (or just that
# step) after CI's SonarCloud job lands.
#
# Per tasks.md T020: if a step fails, patch the relevant earlier task's
# files (focused patch, no new features), then re-run this script from
# the top.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUP_DIR="${REPO_ROOT}/supervisor"
WORK_DIR="$(mktemp -d /tmp/m9-acceptance.XXXXXX)"

SONAR_HOST="https://sonarcloud.io"
SONAR_PROJECT="garrison-hq_garrison"
COVERAGE_THRESHOLD=82

PASS=0
FAIL=0
declare -a FAILURES=()

ok() {
  PASS=$((PASS + 1))
  printf '  PASS  %s\n' "$1"
  return 0
}

fail() {
  FAIL=$((FAIL + 1))
  FAILURES+=("$1")
  printf '  FAIL  %s\n' "$1" >&2
  return 0
}

note() {
  printf '  ....  %s\n' "$1"
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

echo "M9 acceptance walk — ${REPO_ROOT}"
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
# SC-001 / SC-002 — the T016 golden-path smoke tests: ticket mode fires
# one templated ticket per slot and advances exactly one slot; oneshot
# completes the full spawn → finalize_oneshot → verification loop with
# zero tickets rows.
# -----------------------------------------------------------------------------
echo
echo "== SC-001 + SC-002: golden paths (T016)"

run_step "SC-001: ticket-mode golden path (TestTicketModeGoldenPath)" sc001 \
  go test -tags=integration -run 'TestTicketModeGoldenPath$' ./internal/schedule/ -count=1 -timeout 600s

run_step "SC-002: oneshot golden path (TestOneshotGoldenPath)" sc002 \
  go test -tags=integration -run 'TestOneshotGoldenPath$' ./internal/schedule/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-003 — recovery collapse: ≥3 missed slots → exactly one firing, no
# backfill (T017).
# -----------------------------------------------------------------------------
echo
echo "== SC-003: recovery collapse (T017)"

run_step "SC-003: missed slots collapse to one firing (TestRecoveryCollapseFiresOnce)" sc003 \
  go test -tags=integration -run 'TestRecoveryCollapseFiresOnce$' ./internal/schedule/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-004 — concurrent claim single-firing (T018 chaos, SKIP LOCKED
# discipline).
# -----------------------------------------------------------------------------
echo
echo "== SC-004: concurrent-claim single firing (T018 chaos)"

run_step "SC-004: concurrent tickOnce fires exactly once (TestConcurrentClaimSingleFiring)" sc004 \
  go test -tags=chaos -run 'TestConcurrentClaimSingleFiring$' ./internal/schedule/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-005 — gates hold: over-budget/paused company defers with
# throttle_events evidence and a typed run outcome (T006 tick-side gate
# + T007 oneshot spawn-side gate + the ticket-mode dept-weekly evidence
# rendering).
# -----------------------------------------------------------------------------
echo
echo "== SC-005: budget/pause gates defer with evidence (T006/T007)"

run_step "SC-005: tick loop gate defer writes evidence (TestTickOnceGateDeferredWritesEvidence)" sc005-tick \
  go test -tags=integration -run 'TestTickOnceGateDeferredWritesEvidence$' ./internal/schedule/ -count=1 -timeout 600s

run_step "SC-005: oneshot spawn gate defer + retry-after-clear (TestSpawnOneshotGateDefer*)" sc005-spawn \
  go test -tags=integration -run 'TestSpawnOneshotGateDeferUpdatesRun$|TestSpawnOneshotRetryAfterGateClearsToFired$' ./internal/spawn/ -count=1 -timeout 600s

run_step "SC-005: dept-weekly defer detail renders the throttle decision (unit)" sc005-detail \
  go test -run 'TestDeptWeeklyDeferDetailRendersDecision$' ./internal/schedule/ -count=1 -timeout 120s

# -----------------------------------------------------------------------------
# SC-006 — dashboard CRUD round-trip: operator-attended walk against a
# seeded dev stack. The Go suites above pin every row shape the
# dashboard reads and writes; the click-through needs a human.
# -----------------------------------------------------------------------------
echo
echo "== SC-006: dashboard CRUD walk (operator-attended)"
note "On a seeded dev stack, at /admin/recurring-jobs:"
note "  1. create a task (CreateTaskForm) — validation errors render inline;"
note "  2. let a slot fire — run history shows outcome 'fired';"
note "  3. pause — a slot passes, nothing fires, nothing pending;"
note "  4. resume — next_fire_at recomputes future-only (advance-only);"
note "  5. delete — soft delete; run history + audit rows survive;"
note "  6. verify exactly one chat_mutation_audit row per mutation (verb"
note "     from ServerActionVerbs, chat anchors NULL, delete snapshots"
note "     pre-state into args_jsonb)."
note "Not asserted here — same operator-attended posture as the M7.1/M8 walks."

# -----------------------------------------------------------------------------
# SC-007 — chat authoring: the threat-model amendment (T010) must be
# committed in history before the verb code (T011); the per-turn
# ceiling fires at call N+1 (T012). The live chat-session walk is
# operator-attended.
# -----------------------------------------------------------------------------
echo
echo "== SC-007: chat authoring (T010 ordering + T012 ceiling)"

T010_SHA="$(cd "${REPO_ROOT}" && git log --format='%H' -n1 \
  --grep='^T010: Docs — chat-threat-model amendment' HEAD)"
T011_SHA="$(cd "${REPO_ROOT}" && git log --format='%H' -n1 \
  --grep='^T011: internal/garrisonmutate — create_scheduled_task verb' HEAD)"
if [[ -z "${T010_SHA}" || -z "${T011_SHA}" ]]; then
  fail "SC-007: T010 (${T010_SHA:-missing}) / T011 (${T011_SHA:-missing}) commits not found in history"
elif [[ "${T010_SHA}" != "${T011_SHA}" ]] && \
     (cd "${REPO_ROOT}" && git merge-base --is-ancestor "${T010_SHA}" "${T011_SHA}"); then
  ok "SC-007: threat-model amendment (${T010_SHA:0:7}) precedes the verb commit (${T011_SHA:0:7})"
else
  fail "SC-007: T010 amendment commit does NOT precede T011 verb commit (FR-601 ordering)"
fi

run_step "SC-007: per-turn ceiling fires at call N+1 (TestScheduledTaskCeiling*)" sc007-ceiling \
  go test -run 'TestScheduledTaskCeiling' ./internal/chat/ -count=1 -timeout 120s

note "Live chat-session walk (operator-attended): ask the CEO chat to"
note "schedule a recurring task; verify the audit row anchors the chat"
note "session and the resulting row behaves identically to a"
note "dashboard-created one; a 4th create_scheduled_task call in one"
note "turn must bail with scheduled_task_creation_ceiling_reached."

# -----------------------------------------------------------------------------
# SC-008 — zero idle cost: ticks with no due tasks record zero runs and
# zero instances (T017's SC-008 proxy).
# -----------------------------------------------------------------------------
echo
echo "== SC-008: zero idle cost (T017)"

run_step "SC-008: no due tasks → zero runs, zero instances (TestZeroIdleCost)" sc008 \
  go test -tags=integration -run 'TestZeroIdleCost$' ./internal/schedule/ -count=1 -timeout 600s

# -----------------------------------------------------------------------------
# SC-009 — validation rejects with typed errors and no row change, from
# every authoring surface: schedule.ValidateTask (T004), the chat verb
# (T011), and the dashboardapi validate endpoint the dashboard actions
# call (T013).
# -----------------------------------------------------------------------------
echo
echo "== SC-009: validation rejects (T004 / T011 / T013)"

run_step "SC-009: ValidateTask unit rejects + next-fire computation (T004)" sc009-validate-unit \
  go test -run 'TestValidateTask' ./internal/schedule/ -count=1 -timeout 120s

run_step "SC-009: ValidateTask DB-backed rejects (unknown dept, duplicate name)" sc009-validate-db \
  go test -tags=integration -run 'TestValidateTaskRejectsUnknownDepartment$|TestValidateTaskRejectsDuplicateName$' ./internal/schedule/ -count=1 -timeout 600s

run_step "SC-009: create_scheduled_task verb rejects (grammar, interval, dupe, agent caller)" sc009-verb \
  go test -tags=integration -run 'TestCreateScheduledTaskRejects' ./internal/garrisonmutate/ -count=1 -timeout 600s

run_step "SC-009: POST /schedule/validate 200/422/auth contract (T013)" sc009-endpoint \
  go test -run 'TestScheduleValidate' ./internal/dashboardapi/ -count=1 -timeout 120s

# -----------------------------------------------------------------------------
# SC-010 — full regression: default + integration + chaos suites from
# M1–M8 plus everything M9 added, all green. These runs write the same
# three coverage profiles CI hands to Sonar (coverage.out,
# int-coverage.out, chaos-coverage.out) — the probe below consumes
# them.
#
# Two local-host accommodations vs the bare `make test*` shape:
#   - Coverage runs cover only packages that carry test files; the
#     no-test main packages (mockclaude, embed-agent-md, vaultlog/cmd)
#     contribute zero lines to CI's profiles anyway, and including
#     them trips go1.25.0's broken on-demand `covdata` build when go
#     runs from a trimmed module-cache toolchain ("go: no such tool
#     covdata"; fixed in 1.25.9, harmless in CI's full distribution).
#     A plain `go build ./...` keeps the compile check on those mains.
#   - -p 1 on the container-backed suites serialises package binaries:
#     the agentcontainer bridge-scaling test provisions 25 docker
#     networks, and parallel packages' testcontainers exhaust the
#     daemon's default address pools on a host that also runs the
#     live stack (M7.1 acceptance run-1 finding).
# -----------------------------------------------------------------------------
echo
echo "== SC-010: full regression (default + integration + chaos)"

run_step "SC-010: every package (incl. no-test mains) builds" sc010-build \
  go build ./...

# Tagged test files are invisible to a default-tags `go list`, so the
# tested-package list is computed per tag set.
tested_pkgs() {
  local tags="$1"
  local -a tagflag=()
  [[ -n "${tags}" ]] && tagflag=(-tags="${tags}")
  (cd "${SUP_DIR}" && go list "${tagflag[@]}" \
     -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | sed '/^$/d')
}

mapfile -t DEFAULT_PKGS < <(tested_pkgs "")
mapfile -t INTEGRATION_PKGS < <(tested_pkgs "integration")
mapfile -t CHAOS_PKGS < <(tested_pkgs "chaos")
if (( ${#DEFAULT_PKGS[@]} == 0 || ${#INTEGRATION_PKGS[@]} == 0 || ${#CHAOS_PKGS[@]} == 0 )); then
  fail "SC-010: go list found no packages with tests for some tag set (toolchain problem?)"
fi

run_step "SC-010: default suite green (coverage.out)" sc010-default \
  go test -coverprofile=coverage.out -covermode=atomic "${DEFAULT_PKGS[@]}" -count=1 -timeout 1200s

run_step "SC-010: integration suite green (int-coverage.out)" sc010-integration \
  go test -tags=integration -coverprofile=int-coverage.out -covermode=atomic "${INTEGRATION_PKGS[@]}" -count=1 -timeout 3000s -p 1

run_step "SC-010: chaos suite green (chaos-coverage.out)" sc010-chaos \
  go test -tags=chaos -coverprofile=chaos-coverage.out -covermode=atomic "${CHAOS_PKGS[@]}" -count=1 -timeout 3000s -p 1

# -----------------------------------------------------------------------------
# Coverage probe — ≥82% statement coverage on M9's new Go code: the
# lines this branch added/changed (vs the merge-base with main),
# excluding _test.go files and everything sonar.coverage.exclusions
# already keeps out of the gate. Diff-aware to match Sonar's new-code
# semantics: only profile blocks overlapping M9-changed lines count,
# so pre-existing uncovered lines in modified files don't distort the
# number. A statement counts as covered if any of the three suites
# reached it (Sonar merges the same three profiles). Per-file
# breakdown prints so a shortfall points at the file to top up.
# -----------------------------------------------------------------------------
echo
echo "== Coverage probe: M9 new-code ≥ ${COVERAGE_THRESHOLD}% (M6 retro #7)"

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
      case "$f" in ${pat}) skip=1; break ;; esac
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
  local ranges="${WORK_DIR}/m9-newcode-ranges.txt"
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

  local merged="${WORK_DIR}/m9-newcode-merged.cov"
  : > "${merged}"
  local prof have_prof=0
  for prof in coverage.out int-coverage.out chaos-coverage.out; do
    if [[ -f "${SUP_DIR}/${prof}" ]]; then
      grep -v '^mode:' "${SUP_DIR}/${prof}" >> "${merged}"
      have_prof=1
    fi
  done
  if (( ! have_prof )); then
    echo "no coverage profiles found — did the SC-010 runs complete?" >&2
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
      printf "  M9 new-code statement coverage: %.1f%% (%d/%d statements, threshold %d%%)\n", \
             pct, cov, tot, threshold
      exit (pct >= threshold ? 0 : 1)
    }' "${ranges}" "${merged}"
}

if coverage_probe; then
  ok "coverage probe: M9 new-code coverage ≥ ${COVERAGE_THRESHOLD}%"
else
  fail "coverage probe: M9 new-code coverage below ${COVERAGE_THRESHOLD}% — top up Go-side tests (no production-code changes)"
fi

# -----------------------------------------------------------------------------
# Sonar new-issues pre-clearance (M8 T022 pattern) — when this branch's
# PR has a SonarCloud analysis, assert zero unresolved new issues and
# zero TO_REVIEW hotspots. Before the push / before CI's sonarcloud job
# lands there is nothing to query: report pending and re-run after.
# Set SONAR_TOKEN for authenticated queries (public reads work without).
# -----------------------------------------------------------------------------
echo
echo "== Sonar pre-clearance: zero unresolved new issues on the PR"

json_total() {
  # First "total":N in a Sonar JSON payload (paging.total on both the
  # issues and hotspots endpoints). Avoids a jq dependency.
  sed -n 's/.*"total":\([0-9]\{1,\}\).*/\1/p' | head -1
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
echo "M9 acceptance walk: ${PASS} pass / ${FAIL} fail"
echo "Artifacts kept at ${WORK_DIR}"
echo "============================================"
if (( FAIL > 0 )); then
  printf 'FAIL: %s\n' "${FAILURES[@]}" >&2
  echo >&2
  echo "Per tasks.md T020: patch the relevant earlier task's files (no new" >&2
  echo "features), then re-run this script from the top." >&2
  exit 1
fi
exit 0
