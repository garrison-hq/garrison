---
name: garrison-tasks
description: Break the approved milestone plan into executable tasks, following Garrison's task list conventions. Usage `/garrison-tasks <milestone-id>`, e.g. `/garrison-tasks m2-1`.
user-invocable: true
---

Break the approved $ARGUMENTS plan into executable tasks.

## Binding inputs

1. The approved plan from the plan phase — structural decisions are final
2. The spec and clarifications — behavioral requirements are final
3. `specs/_context/$ARGUMENTS-context.md` — operational constraints, especially acceptance criteria
4. `AGENTS.md` — repository rules, especially scope discipline and retro requirements
5. The most recent shipped milestone's task list at `specs/00X-m{N}-<slug>/tasks.md` — use as format reference. Follow the same conventions (ID format, field structure, dependency-ordered sequence, retro as final task).
6. The most recent retro at `docs/retros/m{N}.md` — informs what patterns have worked, what should be called out explicitly as risks in this milestone's tasks.

## What the task list produces

A linear, ordered sequence of tasks an agent executes one at a time. Each task:

- Has a clear, testable completion condition ("compiles" is not testable; "TestXxx passes" is)
- Is self-contained enough for an agent to do in a single session without backtracking
- Produces something reviewable — a commit, a passing test, a runnable binary state
- Names specific files it creates or modifies

## Ordering rules

Tasks must be ordered so that the repository is in a working state after every task. Not "compiles" — working. The binary (or the extended version of the prior-milestone binary) should be runnable after each task.

If this milestone extends a prior shipped milestone, early tasks look different from greenfield:
- T001 is likely migrations, schema changes, or infrastructure — not scaffolding
- Existing packages are modified before new packages are added (unless the dependency order demands otherwise)
- "Scaffold the project" is a task only in the first milestone that exists; later milestones extend

If this milestone is genuinely greenfield for a new subsystem, T001 can be scaffolding for that subsystem specifically.

Do NOT order tasks by "easiest first" or "most interesting first." Dependency order only.

## Task granularity

Each task is roughly 1-3 hours of agent work (or one Claude Code session).

- Too coarse: "Update package X for feature Y" — usually multiple files, multiple concerns
- Too fine: "Create file.go with empty Struct definition" — this is part of a task, not a task
- Right: "Implement package/feature.go with SpecificType and its methods, plus feature_test.go covering the three specific behaviors from the plan"

## What each task contains

- **ID**: T001, T002, ... sequential. Restart from T001 (this is this milestone's tasks, not a continuation).
- **Title**: one line, imperative
- **Depends on**: list of prior task IDs within this milestone, or "M{N} shipped" for the very first tasks that build on a prior milestone
- **Files**: exact paths the task creates or modifies
- **Completion condition**: what passes or works after this task
- **Out of scope for this task**: what the agent might be tempted to do but shouldn't — especially work belonging to a later task

The "out of scope" field is not optional. It's the single most important field for preventing agents from reaching ahead into later tasks' concerns.

## Required special tasks

Every milestone's task list includes:

1. **A "golden path" integration test task near the end** — one test exercising the full happy path from input to observable output. Named explicitly because it's the smoke test for this milestone.

2. **A chaos-test task** if the context file's acceptance criteria include chaos scenarios (most milestones do).

3. **A final task: run the acceptance criteria from the context file as a scripted check**. If a step fails, open a focused patch against the relevant earlier task's files, then re-run all acceptance steps from the top. This task does not introduce new features.

4. **A retro task AFTER acceptance** — write `docs/retros/$ARGUMENTS.md` (or MemPalace entry if memories are available from M2.2 onwards) following the retro structure from AGENTS.md. Retro is the LAST task, not combined with acceptance. The retro must include what AGENTS.md requires: what shipped, what the spec got wrong, dependencies added outside the locked list with justifications, open questions deferred to the next milestone, and for spike-first milestones, whether the spike paid off.

## What the task list must NOT include

- Vague setup tasks ("set up the project") — be concrete or drop
- Tasks for features explicitly out of scope for this milestone
- "Research" or "investigate" tasks — those happened in earlier phases
- Review tasks ("have human review T005") — reviews happen in PR, not as tasks
- Timeline estimates
- Parallelization suggestions — milestones are executed linearly by a solo operator

## Format

Follow `.specify/templates/tasks-template.md` structure. Each task as a numbered entry with the fields above. Sentence case. Reference files by exact path. Reference prior tasks by ID.

## Before writing the task list

Count the tasks you're about to produce. Typical range: 12-22 for most milestones. If it's fewer than 10, you're being too coarse. If it's more than 25, you're over-decomposing or including things that belong in a later milestone.

State the count before drafting the list so the operator can sanity-check it. If the count is outside the typical range, justify in one sentence.

Wait for operator approval on the count and any called-out special considerations before drafting the full list.