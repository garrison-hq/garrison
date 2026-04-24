#!/usr/bin/env bash
# Post-hoc aggregator — re-derive correct finalize/palace metrics
# from the per-iteration raw supervisor logs. The in-test parser uses
# the wrong message shape (targets the finalize-handler-internal
# `finalize: schema rejection` / `finalize: payload accepted` lines
# that don't land in stdout) so MATRIX.md's attempt + first-error
# columns need this pass.
#
# Outputs lines like:
#   run=01 variant=A model=haiku iter=1 finalize_attempts=1 success_at=- first_err=field="",error_type="schema"
#
# Run from experiment-results/ after the 12 iterations complete.

set -euo pipefail
cd "$(dirname "$0")"

for log in run-*.log; do
  [ -f "$log" ] || continue

  # Parse metadata from filename: run-NN-<variant>-<model>.log
  # Model contains dashes, so greedy-match the tail.
  stem="${log#run-}"
  stem="${stem%.log}"
  idx="${stem%%-*}"
  rest="${stem#*-}"
  variant="${rest%%-*}"
  model="${rest#*-}"

  # Count finalize tool_result lines; find first failure + success.
  attempts=$(grep -c '"msg":"finalize tool_result"' "$log" 2>/dev/null || echo 0)
  success_at=$(grep '"msg":"finalize tool_result"' "$log" \
                 | grep -o '"attempt":[0-9]*,"ok":true' \
                 | head -1 \
                 | grep -oE '[0-9]+' | head -1 || true)
  success_at="${success_at:-}"

  # First failed attempt details (error_type, field, etc.).
  first_err=$(grep '"msg":"finalize tool_result"' "$log" \
                | grep '"ok":false' \
                | head -1 \
                | grep -oE '"(error_type|field|failure|constraint)":"[^"]*"' \
                | paste -sd, - || true)

  # Palace-vs-non-palace first-tool timing. Find timestamps.
  first_palace=$(grep '"msg":"mempalace tool_use"' "$log" \
                   | head -1 \
                   | grep -oE '"time":"[^"]+"' | head -1 \
                   | sed 's/"time":"//;s/"$//' || true)
  first_finalize=$(grep -E '"msg":"finalize tool_use"|"msg":"claude subprocess terminal"|Read|Write|Edit' "$log" \
                     | head -1 \
                     | grep -oE '"time":"[^"]+"' | head -1 \
                     | sed 's/"time":"//;s/"$//' || true)

  printf 'run=%s variant=%s model=%s finalize_attempts=%s success_at=%s first_err={%s} palace_first=%s nonpalace_first=%s\n' \
    "$idx" "$variant" "$model" "$attempts" "${success_at:-none}" "${first_err:-none}" "${first_palace:-none}" "${first_finalize:-none}"
done
