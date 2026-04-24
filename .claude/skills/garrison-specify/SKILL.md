---
description: Produce a milestone specification for Garrison using spec-kit, with project discipline applied. Usage `/garrison-specify <milestone-id>`, e.g. `/garrison-specify m2-1`.
---

Produce the $ARGUMENTS specification for Garrison. Run this after `/speckit.specify` has initialized the spec structure, OR as a replacement for it — this command encodes Garrison-specific binding rules on top of spec-kit's defaults.

## Binding inputs (read in full before drafting)

1. `specs/_context/$ARGUMENTS-context.md` — binding constraints for this milestone. Treat these as non-negotiable. If the file does not exist, stop and surface it — the context file must exist before a spec is drafted.
2. `RATIONALE.md` — architectural decisions. Do not propose alternatives to settled decisions.
3. `ARCHITECTURE.md` — relevant sections only, reference them by name rather than paraphrasing.
4. `AGENTS.md` — repository rules, especially the "Activate before writing code" section for this milestone, concurrency discipline, the "what agents should not do" list, and the locked dependency list.
5. `.specify/memory/constitution.md` — project principles.
6. If the context file references a research spike (typically `docs/research/$ARGUMENTS-spike.md` or similar), read it. The spike is ground truth for external tool behavior; cite it by section number rather than re-deriving findings.
7. If this milestone builds on a prior shipped milestone, read that milestone's retro at `docs/retros/m{N}.md`. The retro is how you understand what's already in the codebase and what lessons carried forward.

## What the spec produces

A milestone specification following `.specify/templates/spec-template.md` structure. Content is derived from the binding inputs, not invented. The spec is a thin layer above the context file — structure (user stories, acceptance criteria, non-functional requirements) not re-derivation of decisions the context file already made.

## What the spec must NOT do

- Do not decide things already decided in the context file. Binding constraints (stack, data model, channel names, library choices, spike findings, architectural rules) are inputs, not questions.
- Do not decide things deferred to `/garrison-plan` (package structure, function signatures, file layout). Plan comes next.
- Do not include timelines or effort estimates. Garrison is built by a solo operator; the spec is about what, not when.
- Do not include code. Specs describe behavior; code is produced by `/garrison-implement`.
- Do not re-characterize external tool behavior. If a spike exists, cite its findings; don't re-derive them.
- Do not propose alternatives to decisions settled in RATIONALE.md.
- Do not reproduce large portions of the context file or the spike. Link, don't duplicate.

## Binding questions the spec must resolve (not defer)

If the context file contains a section named "Binding questions for /speckit.specify" (or similar), every question in that section MUST be answered in this spec, not deferred to `/speckit.clarify` or `/garrison-plan`. The context file provides defaults the spec may use if there is no strong reason to choose otherwise; if the spec chooses differently, it justifies in one or two sentences.

The spec is incomplete if any question listed in that section is left open.

## What to flag for `/speckit.clarify`

Only flag items that are genuinely ambiguous after reading all binding inputs:
- Behaviors the spike didn't characterize
- Edge cases the context file didn't cover
- Inconsistencies between inputs that you cannot resolve without operator input

Do NOT flag things the context file already answered. If you find yourself flagging, re-read the context file first.

## Format

Follow `.specify/templates/spec-template.md` structure. Sentence case throughout. Reference binding inputs by section name (e.g. "see 'Spawn contract' in m{N}-context.md"). Reference spike sections by number (e.g. "per spike §2.7"). Do not paraphrase large sections.

## Before writing the spec

1. Confirm `specs/_context/$ARGUMENTS-context.md` exists. If not, stop.
2. List every section of the context file you'll treat as binding. This is a sanity check that you read it fully.
3. List every "Binding question" from the context file (if any) with your proposed answer (one line each) for operator approval before drafting the full spec.
4. List every ambiguity you'll flag for clarify, with a one-line reason.

Wait for operator approval on steps 2-4 before drafting the full spec.

## Acceptance criteria

The spec is good if the operator can hand it to `/speckit.clarify` without first having to fix scope creep, missing binding decisions, or unresolved RATIONALE conflicts.