---
description: Execute a milestone's task list with Garrison's implementation discipline. Usage `/garrison-implement <milestone-id>`, e.g. `/garrison-implement m2-1`.
---

Execute the $ARGUMENTS task list. The spec, plan, tasks, and analyze phases are complete. All structural decisions are final. Your job is to produce working code that satisfies the tasks as written, not to revisit the design.

## How to execute

Work tasks in strict order. One task per session unless a task is explicitly small enough to combine with the next. The retro task is never combined with anything; it is separate by design.

For each task:

1. Read the task's "Depends on", "Files", "Completion condition", and "Out of scope for this task" fields before writing any code.
2. Activate the milestone's domain knowledge from AGENTS.md before producing code. Read `AGENTS.md` "Activate before writing code" section for this milestone and the prior milestones whose code you're extending.
3. Produce the files listed under "Files". Do not produce files not listed unless they are obviously required (go.sum regeneration, test fixture data, sqlc-generated output). If you find yourself creating a file not in the task's "Files" list, stop and ask.
4. Write the tests described in the completion condition alongside the implementation, not after. Commit when the completion condition passes.
5. Commit with a message that names the task ID ("T005: <task title>") and, if any dependency was added outside the locked list, a paragraph justifying it per AGENTS.md soft rule.
6. Do not move to the next task until the current task's completion condition is verifiably satisfied. "Compiles" is not "passes the test." Run the test.

## Precedence when documents conflict

From AGENTS.md:

RATIONALE.md > `specs/_context/$ARGUMENTS-context.md` > ARCHITECTURE.md > research spike (for tool behavior only) > `.specify/memory/constitution.md` > installed skills > agent defaults.

If the spec/plan/tasks themselves contain an inconsistency with a higher-authority document, stop and surface it rather than silently resolving. If they contradict each other (spec says X, plan says not-X, tasks says Y), stop and ask.

A research spike, if one exists for this milestone, is binding for "how does the external tool behave" questions (exit codes, output format, event types, kill semantics). It is NOT binding for "how does Garrison design around that behavior" — those decisions live in the context file, spec, and plan. If the spike suggests a design, re-read it as observation, not recommendation.

## Prior milestones are running production

If this milestone extends prior shipped milestones, the prior milestones' code is in production:

- Preserve existing tests unless the task explicitly says they should change
- Run the prior milestones' chaos tests after any task that touches code shared with them — these are the safety net that caught prior bugs and must keep passing through this milestone
- If an existing test fails and the task doesn't mention fixing it, stop and surface it. An unexpected test failure is a signal the change had broader impact than planned.

## Verification before completion

Use `obra/superpowers/verification-before-completion` discipline on every task:

- "Compiles" is not "done."
- "Tests exist" is not "tests pass."
- "The code does what I intended" is not "the code does what the task requires."

When you think a task is done, re-read the task's completion condition verbatim. Does your code satisfy every clause? If the clause says "three unit tests pass," the three tests exist, pass, and are named exactly as specified. If the clause references an external document ("verbatim from $ARGUMENTS-context.md §X"), open that document and diff.

For any task involving external tool integration (Claude Code, MemPalace, MCP servers, Infisical, etc.), verify by running the real tool at least once during task development. A mock-only test suite can drift from reality.

## Scope discipline

Out-of-scope work is not a time-saver. If a task mentions something that belongs to a later task, do not "just quickly add it." Finish the current task. Commit. Start the next.

Exception: if you discover during a task that a later task's approach as written in the plan won't work, stop and surface the conflict. Do not silently change a later task's approach while working on an earlier one.

## Dependencies

If a task requires a dependency outside the locked list from AGENTS.md, you may add it but the commit message must include a paragraph justifying:

- What the dependency does
- Why stdlib or an already-installed dependency isn't sufficient
- What alternatives were considered

If the justification feels thin when you write it, the answer is probably that you shouldn't add the dependency. Soft rule, not hard, but write the justification honestly and you'll catch most of the bad calls.

The plan's decisions may have introduced a specific dependency — that's pre-justified by the plan. Any dependency the plan didn't already approve requires fresh justification.

## When to stop and ask

- A task's completion condition is genuinely unclear (not "I want more detail" — actually ambiguous)
- Spec, plan, or tasks contradict each other in a way that wasn't caught by `/speckit.analyze`
- A completion condition turns out to be impossible as written
- External tool behavior contradicts the spike (version drift, deprecated flags, undocumented event types)
- A dependency or external tool is not reachable from the environment

Otherwise: execute the plan. Don't relitigate it.

## When a task is complete

After committing, state explicitly: "T00X complete. Completion condition satisfied: [brief restatement]. Ready to proceed to T00Y." Then wait for confirmation before starting the next task. This gives a natural review checkpoint between tasks.

An implementation session that blasts through multiple tasks without checkpoints has failed the discipline and risks compounding mistakes across a 2000-line diff.

## Milestone-specific risks

Before starting, read the most recent shipped milestone's retro at `docs/retros/m{N}.md`. The retro documents what the prior milestone's spec got wrong. This milestone has similar shapes of risk:

- If prior retros surfaced race conditions, watch for similar races in any new dedupe/concurrency code
- If prior retros surfaced context handling bugs in shutdown, verify shutdown paths in any new subsystems
- If prior retros surfaced subprocess signal-handling issues, verify this milestone's subprocess code uses the discipline from AGENTS.md rule 7
- If prior retros surfaced external-tool timing surprises, give generous timeouts in this milestone's equivalent paths

These aren't superstitions; they're pattern recognition from real prior bugs in this codebase.

Begin with T001.