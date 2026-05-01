# M5.3 mockclaude fixtures

NDJSON scripts driving the chat-driven mutation chaos tests (T020) and
the Playwright golden-path (T021). Mirror the existing
`m2_2_*.ndjson` / `m5_1_*.ndjson` shape: `#`-prefixed directives
interleaved with literal claude wire-format JSON lines.

## Fixtures

| File | Scenario | Closes |
|---|---|---|
| `m5_3_create_ticket_happy.ndjson` | Operator → assistant → tool_use create_ticket → tool_result success → terminal | T021 golden path |
| `m5_3_transition_ticket_happy.ndjson` | Same shape with transition_ticket | T021 |
| `m5_3_edit_agent_config_leak_fail.ndjson` | edit_agent_config with planted `sk-` value → tool_result with `error_kind=leak_scan_failed` | T020 leak-scan parity |
| `m5_3_propose_hire_happy.ndjson` | propose_hire happy path | T021 |
| `m5_3_compound_two_verbs.ndjson` | One assistant turn emits two tool_use events (transition + pause_agent) | T020 atomicity per-verb |
| `m5_3_chaos_ac1_palace_inject.ndjson` | mempalace.search returns malicious string → assistant interprets as command → calls create_ticket | T020 AC-1 |
| `m5_3_chaos_ac2_composer_inject.ndjson` | Operator pastes injection-shaped content; assistant interprets as instruction | T020 AC-2 |
| `m5_3_chaos_ac3_feedback_loop.ndjson` | tool_result text contains tool-call-shaped string; assistant chains; per-turn ceiling fires at 50 | T020 AC-3 |
| `m5_3_chaos_ceiling_breach.ndjson` | Minimal: 51 sequential `tool_use` events trigger ceiling fire | T020 ceiling chaos |

## Selection mechanism

`GARRISON_MOCKCLAUDE_FIXTURE=m5_3/m5_3_create_ticket_happy` env var
selects which script the mockclaude binary replays per plan §11. The
mockclaude binary's existing fixture-selection logic resolves the env
var to a path under `internal/spawn/mockclaude/scripts/` and replays
the NDJSON line-by-line.

## Status (M5.3 implementation phase)

The fixture **bodies** below are M5.3-shape skeletons calibrated
against the chat policy's wire-format expectations (init →
content_block_start → content_block_delta → tool_use → user
tool_result → result). Per-fixture refinement to drive specific
chaos test assertions lives in T020's chaos test commits — each
`chaos_test.go` test reads its bound fixture and asserts the
expected outcome (audit row outcome, notify channel, terminal
error_kind).

If a chaos test asserts a specific timing or sequence the skeleton
doesn't yet exhibit, the test commit refines that fixture inline.
The skeletons below are sufficient for the parser layer to consume
without panic; behavioral assertions land alongside their tests.
