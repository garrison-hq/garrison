<!--
Thanks for the PR. Before opening, please confirm the checklist
below so review can focus on the change itself rather than the
mechanics. See CONTRIBUTING.md for context on each item.
-->

## What this changes

<!-- One paragraph. WHY this change is needed, not what the code does. -->

## Linked issue

<!--
Every non-trivial PR should link an issue. See CONTRIBUTING.md
§"Open an issue first". If this is a typo fix or obvious bug, say
so explicitly instead of linking an issue.
-->

Fixes #

## Target milestone

<!-- Which milestone does this belong to? -->

- [ ] Current active milestone
- [ ] Documentation / tooling (no milestone)
- [ ] Other (explain):

## Checklist

- [ ] Read `AGENTS.md` and `CONTRIBUTING.md`.
- [ ] Scope is minimal — no unrelated cleanup, no speculative
  abstractions, no "while I'm here" refactors.
- [ ] If a new direct dependency was added, it's justified in a
  commit message paragraph per `CONTRIBUTING.md` §"The locked
  dependency list" and the milestone retro will be updated.
- [ ] `make lint` passes (`go vet` + `gofmt`).
- [ ] `make test` passes.
- [ ] `make test-integration` passes (or: explain why this PR
  doesn't touch anything integration-tested).
- [ ] If this PR touches reconnect, subprocess lifecycle, or
  shutdown paths: `make test-chaos` passes.
- [ ] Documentation invalidations addressed. If this PR contradicts
  something in `ARCHITECTURE.md`, `RATIONALE.md`, or a spec, the
  relevant document is updated in the same PR.

## Notes for reviewers

<!-- Anything that will save review time: tricky bits, known limitations, trade-offs. -->
