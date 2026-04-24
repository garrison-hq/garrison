---
description: Produce a milestone implementation plan for Garrison, building on spec-kit's plan phase with project discipline applied. Usage `/garrison-plan <milestone-id>`, e.g. `/garrison-plan m2-1`.
---

Produce the $ARGUMENTS implementation plan for Garrison. The spec and clarify phases are done; their outputs are binding inputs, not drafts to revisit.

## Binding inputs (read in full before planning)

1. The approved spec from the spec phase — the what
2. The clarifications from `/speckit.clarify` — resolved ambiguities, treat as equally binding
3. `specs/_context/$ARGUMENTS-context.md` — operational constraints, especially any section explicitly labeled "binding"
4. If the context file references a research spike, read the spike. Empirical observations are ground truth.
5. `RATIONALE.md` — architectural decisions. Do not propose alternatives.
6. `AGENTS.md` — repository rules, especially the "Activate before writing code" section for this milestone, all concurrency discipline rules, the locked dependency list.
7. If this milestone builds on prior shipped milestones, read their retros at `docs/retros/m{N}.md`. The plan builds on existing code; understand what's there before proposing changes.
8. The existing codebase. Re-read the relevant internal packages before proposing structural changes. The plan extends what's there, not a greenfield design.

## What the plan produces

A concrete implementation plan an agent can follow to produce code. It specifies:

- What changes in each existing package, and which new packages (if any) are created
- Public interfaces between packages (function signatures, method sets, error types)
- Subsystem state machines and lifecycle
- Data model changes (migrations, sqlc queries, schema evolution)
- The test strategy at test-function granularity (not "we'll test X" but "TestSpecificFunction in path/to/file_test.go verifies Y")
- Deployment changes (Dockerfile, Makefile/justfile targets, config files)

## What the plan must NOT do

- Do not re-decide anything settled in the context file, RATIONALE, the spec, or clarify.
- Do not introduce dependencies beyond the locked list from AGENTS.md. Soft rule: new dependencies require a justification, which in a plan means flagging them as an open question rather than baking them in.
- Do not produce code. Pseudocode for clarity is fine; actual function bodies are out of scope.
- Do not speculate about future milestones beyond one-line hooks for forward compatibility. "This abstraction supports future X, Y, Z" is scope creep.
- Do not include timelines or effort estimates.
- Do not propose rewriting existing code. The plan extends; it doesn't reimagine.

## Structural decisions to make explicitly

The plan must make — not defer — every decision the context file and spec leave open. Common categories:

1. **Package boundaries**: which new packages, which existing packages change, what each owns
2. **Type system decisions**: error types, event routing structures, interface method sets
3. **Data handling**: where specific data lands, what's denormalized vs normalized, migration shape
4. **Lifecycle management**: when subsystems start, stop, recover, what their state machines are
5. **File and config management**: where generated files live, how they're named, cleaned up
6. **Binary and tooling**: how external tools are discovered at runtime, how they're deployed
7. **Error vocabulary**: exact strings for exit_reason and similar enumerated error values
8. **Seed data**: exact content of seed files (agent.md, config JSONB, etc.) that the milestone commits

If the context file lists specific "structural decisions the plan must make," the plan addresses every one. Gaps are flagged as open questions, not left implicit.

## Structural decisions to defer

Future-milestone concerns get one-line forward-compatibility mentions, not designed-for. If the plan finds itself designing for a later milestone, it's overreaching.

## Testing plan requirements

The plan names specific test files and describes what each verifies at the test-function level. "We will test X" is not a plan. "internal/foo/bar_test.go::TestBazRejectsMalformedInput verifies that passing a malformed Y returns ErrInvalidInput" is a plan.

Test categories to cover (subset may not apply to every milestone):
- Unit tests for the new or changed packages
- Integration tests via testcontainers-go for end-to-end paths
- Chaos tests for failure modes the context file requires
- Regression check: existing tests from prior milestones still pass unchanged

## Acceptance criteria for the plan itself

The plan is good if another agent can read it and produce compiling, testable code without making any structural decisions the plan didn't already make. If an agent reading the plan would have to invent an error type, name a package, decide between two valid APIs, or pick a file path, the plan has a gap. Flag gaps as open questions rather than leaving them implicit.

## Format

Follow `.specify/templates/plan-template.md` structure. Sentence case throughout. Reference the spec, context file, and spike sections by name when citing constraints. Diagrams in mermaid are fine where they genuinely help; skip them where prose is clearer.

## Before writing the plan

List every structural decision you will make, with your proposed answer (one line each) for operator approval before drafting the full plan. If the context file's "structural decisions to make explicitly" section lists specific decisions, your list matches that structure 1:1.

Wait for operator approval on the decision slate before drafting the full plan.

A plan that skips this checkpoint and jumps straight to the full draft has failed the review discipline and will be rejected.