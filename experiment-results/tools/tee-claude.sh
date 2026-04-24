#!/usr/bin/env bash
# Tee-wrapper around the real claude binary for the triage experiment's
# stream-capture subset run. Duplicates claude's stdout (the stream-json
# NDJSON stream) into a per-invocation file so post-hoc analysis can
# inspect the full tool_use.input payloads and tool_result envelopes
# (including the `hint` text) that the supervisor's slog does NOT emit.
#
# Env (all set by the subset test):
#   CLAUDE_REAL                 absolute path to the real claude binary
#   GARRISON_EXPERIMENT_TEE_DIR directory to write capture files into
#
# Capture filename embeds PID + epoch-ns + random tag so concurrent
# engineer + qa-engineer spawns don't collide. The supervisor does not
# pass any agent_instance_id via claude argv, so the agent_instance
# correlation is recovered post-hoc by matching the first `system`/
# `init` event's `session_id` against the supervisor log's
# `claude init` line (which carries both `session_id` and
# `instance_id`).

set -u

CLAUDE_REAL="${CLAUDE_REAL:?CLAUDE_REAL unset}"
TEE_DIR="${GARRISON_EXPERIMENT_TEE_DIR:?GARRISON_EXPERIMENT_TEE_DIR unset}"
mkdir -p "$TEE_DIR"

out="$TEE_DIR/claude-$$-$(date +%s%N)-$RANDOM.jsonl"

# Pipe claude's stdout through tee so the wrapper's stdout (which the
# supervisor reads) is identical to claude's, and a full copy lands at
# $out. stderr is passed through untouched. PIPESTATUS[0] preserves
# claude's exit code — the supervisor's adjudicator depends on it.
"$CLAUDE_REAL" "$@" | tee "$out"
exit "${PIPESTATUS[0]}"
