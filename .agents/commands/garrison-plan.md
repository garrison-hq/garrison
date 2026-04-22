/speckit.implement

Execute the M2.1 task list at specs/002-m2-1-claude-code/tasks.md. The spec, plan, tasks, and analyze phases are complete. All structural decisions are final. Your job is to produce working code that satisfies the tasks as written, not to revisit the design.

## How to execute

Work tasks in strict order. One task per session unless a task is explicitly small enough to combine with the next (the retro task is never combined; it is separate by design).

For each task:

1. Read the task's "Depends on", "Files", "Completion condition", and "Out of scope for this task" fields before writing any code.
2. Activate the M1 domain knowledge PLUS the M2.1 domain knowledge from AGENTS.md before producing code. M2.1 specifically adds: Claude Code non-interactive invocation contract (from docs/research/m2-spike.md), stream-json event routing with mandatory init-event MCP health check, process-group subprocess termination (AGENTS.md concurrency rule 7), NDJSON line parsing as JSON-routed events rather than free-form log lines.
3. Produce the files listed under "Files". Do not produce files not listed unless they are obviously required (go.sum regeneration, test fixture data, sqlc-generated output). If you find yourself creating a file not in the task's "Files" list, stop and ask.
4. Write the tests described in the completion condition alongside the implementation, not after. Commit when the completion condition passes.
5. Commit with a message that names the task ID ("T005: stream-json event router with init-event MCP health check") and, if any dependency was added outside the locked list, a paragraph justifying it per AGENTS.md soft rule.
6. Do not move to the next task until the current task's completion condition is verifiably satisfied. "Compiles" is not "passes the test." Run the test.

## Precedence reminder

When any document says something different from another:

RATIONALE.md > specs/_context/m2-1-context.md > ARCHITECTURE.md > docs/research/m2-spike.md (for tool behavior only) > .specify/memory/constitution.md > installed skills > agent defaults.

If the spec/plan/tasks themselves contain an inconsistency with a higher-authority document, stop and surface it rather than silently resolving. If they contradict each other (spec says X, plan says not-X, tasks says Y), stop and ask.

The spike document is binding for "how does Claude Code behave" questions (exit codes, output format, event types, kill semantics). It is NOT binding for "how does Garrison design around that behavior" — those decisions live in the context file, spec, and plan. If you find the spike suggests a design, that's the spike being misread. Re-read it as observation, not recommendation.

## M1 code is running production

M2.1 extends M1. M1 is shipped and running. When modifying M1 code (internal/spawn in particular will change substantially):

- Preserve existing tests unless the task explicitly says they should change
- Run the M1 chaos tests after any task that touches event dispatch, subprocess lifecycle, or graceful shutdown — they are the safety net that caught M1's bugs and must keep passing through M2.1
- If an M1 test fails and the task doesn't mention fixing it, stop and surface it. An unexpected test failure is a signal the change had broader impact than planned.

## Verification before completion

Use obra/superpowers/verification-before-completion discipline on every task. Specifically:

- "Compiles" is not "done."
- "Tests exist" is not "tests pass."
- "The code does what I intended" is not "the code does what the task requires."

When you think a task is done, re-read the task's completion condition verbatim. Does your code satisfy every clause? If the clause says "three unit tests pass," the three tests exist, pass, and are named exactly as specified. If the clause references an external document ("verbatim from m2-1-context.md §X"), open that document and diff.

M2.1 specifically: for any task that parses Claude Code's stream-json output, verify by running a real claude invocation (not a mock) at least once during that task's development. The spike observed the event vocabulary empirically; the implementation should match what real claude emits, not what the spec paraphrased. A mock-only test suite can drift from reality.

## Scope discipline

Out-of-scope work is not a time-saver. If a task mentions something that belongs to a later task, do not "just quickly add it." Finish the current task. Commit. Start the next.

Specifically in M2.1:

- Do not add MemPalace code anywhere. MemPalace is M2.2. The MCP config file manager should be designed to accept multiple MCP servers (forward-compatibility) but M2.1 only wires Postgres.
- Do not add secret injection. Secrets are M2.3. Environment variables for the Claude subprocess are inherited from the supervisor's environment in M2.1; per-agent secret injection is a later extension point.
- Do not add rate-limit backoff logic. M2.1 LOGS the rate_limit_event from stream-json; it does not act on it. Acting on it is M6.
- Do not add cost-based throttling. M2.1 CAPTURES total_cost_usd; it does not dispatch based on it. M6.
- Do not improve M1's code beyond what the task requires. If a task modifies internal/spawn, resist the urge to also clean up nearby M1 code that's working fine.

Exception: if you discover during a task that a later task's approach as written in the plan won't work, stop and surface the conflict. Do not silently change a later task's approach while working on an earlier one.

## Dependencies

If a task requires a dependency outside the locked list (pgx/v5, sqlc, slog, errgroup, testify, testcontainers-go, goose/v3, shlex), you may add it but the commit message must include a paragraph justifying:

- What the dependency does
- Why stdlib or an already-installed dependency isn't sufficient
- What alternatives were considered

The plan's decisions about the Postgres MCP implementation may have introduced a specific dependency — that's pre-justified by the plan. Any dependency the plan didn't already approve requires fresh justification.

## Rule 1 pre-check (vault anticipation)

M2.3 will introduce a rule: "secret values never appear in agent.md files verbatim; only environment variable names." M2.1 does not handle secrets, but the engineering agent.md written in M2.1 will set the pattern other agent.mds follow. Write it with the rule-1 discipline already in place: reference configuration via environment variable names even when no secrets are actually involved, so that M2.3's validation work fits cleanly.

## When to stop and ask

- A task's completion condition is genuinely unclear (not "I want more detail" — actually ambiguous)
- Spec, plan, or tasks contradict each other in a way that wasn't caught by /speckit.analyze
- A completion condition turns out to be impossible as written (e.g. a test it asks for can't be written against the architecture as planned)
- You encounter behavior from real Claude Code that contradicts the spike (version drift, a flag that no longer works, an event type not documented)
- A dependency or external tool (claude binary, Postgres MCP server) is not reachable from the environment

Otherwise: execute the plan. Don't relitigate it.

## When a task is complete

After committing, state explicitly: "T00X complete. Completion condition satisfied: [brief restatement]. Ready to proceed to T00Y." 

## Milestone-specific risks to watch for

The M1 retro documented six items the spec got wrong. Three of them (LISTEN/poll race, shutdown context, reconnect nil-conn) were caught by chaos tests. One (dead pid column) survived to ship. M2.1 has comparable risks:

- The pid-backfill fix interacts with the M1 §1 dedupe race. The plan's decision 5 addressed this. Be alert during implementation that the interaction is handled correctly; an integration test that spawns rapidly and verifies no duplicate agent_instances rows is the safety check.
- The Claude subprocess may emit events the routing table doesn't know about. The "unknown type warns, doesn't crash" discipline from the context file is the safety; verify it actually works with real claude output.
- Process-group termination has subtle edge cases on some kernels. Test kill-during-init, kill-during-turn, kill-during-shutdown separately. The chaos tests mostly cover this.
- MCP server startup time is variable; the spike observed ~500ms but on a cold container it could be longer. The spawn-to-init-event timeout should be generous (the context file defaults to 60s for the full subprocess; init event should arrive within a few seconds).

Begin with T001.