# Garrison constitution

Garrison is an event-driven agent orchestration system for a solo operator. This constitution distills binding principles from `RATIONALE.md` and `AGENTS.md`. It introduces no new decisions; it crystallizes decisions already made. Each principle cites its source so agents can trace and verify.

## Core principles

### I. Postgres is the sole source of truth for current state; pg_notify is the event bus

Current state — tickets, agents, departments, hiring requests, concurrency, conversations — lives in Postgres rows. State changes and their notifications fire in the same transaction so delivery cannot drift from state. No parallel state store (filesystem, git, separate queue, managed service) is permitted. See RATIONALE §1, §4.

### II. MemPalace is the sole source of truth for cross-agent, cross-time memory

Institutional knowledge, decisions, and recall across agent instances live in MemPalace via wings, rooms, and halls. Agents do not rely on their own context window to carry information past a single invocation, and no alternative memory layer is introduced. See RATIONALE §3.

### III. Agents are ephemeral

Agents are subprocesses spawned on events, run to completion, and are terminated. No long-running agent daemons, no agent pools, no persistent in-memory conversation state — including for the CEO. Every invocation begins from current Postgres state and MemPalace. See RATIONALE §2, §6.

### IV. Soft gates over hard gates on memory hygiene

Workflow transitions do not fail because an expected MemPalace write was missing or thin. Memory hygiene issues surface on a dashboard for weekly review and backfill, not as transition blockers. Enforcement is by dashboard visibility and operator discipline, not by code-level gates on the workflow. See RATIONALE §5.

### V. Skills come from skills.sh; no curated internal library

Agents acquire capabilities through skills installed from the skills.sh registry, not through a hand-maintained internal skill library or inline skills pasted into `agent.md` files. `agent.md` describes who the agent is; skills describe what it knows. Skills installed from the registry are the unit of capability reuse. See RATIONALE §7.

### VI. Hiring is UI-driven, not git-driven

New agent roles, departments, and skill assignments are proposed, reviewed, and committed through the web UI. The system writes the resulting config to Postgres and installs skills from skills.sh. No git PR workflow gates hiring. YAML export/import is the escape hatch for version control and backup. See RATIONALE §8.

### VII. The supervisor is Go with a locked dependency list

The supervisor is written in Go and deployed as a single static binary. The locked dependency list in `AGENTS.md` governs what may be imported; adding a dependency outside it requires justification in the commit message and a note in the milestone retro. Node and Python tooling do not enter the supervisor; Go tooling does not enter the dashboard. See RATIONALE §9 and AGENTS.md stack rules.

### VIII. Every goroutine accepts a context; no bare `go func()`

Every goroutine accepts a `context.Context` and respects cancellation. Subprocesses spawn via `exec.CommandContext` with a timeout-derived context and a SIGTERM-then-SIGKILL lifecycle. Channels document sender/receiver responsibility and close from the sender side; buffered channels require a documented reason. See AGENTS.md concurrency discipline.

### IX. Specs are narrow per milestone; milestones ship end-to-end functional

Each milestone begins with a narrow specify-cli spec scoped to that milestone only. Future-milestone concerns are recorded as open questions, not implemented early. A milestone is complete when its slice of the system is exercisable end-to-end against real use, not when scaffolding exists. See RATIONALE §10 and AGENTS.md scope discipline.

### X. Per-department concurrency caps bound parallelism

Parallel agent execution is bounded by a per-department cap enforced by the supervisor. Token-budget throttling, per-agent-type caps, and unbounded parallelism are rejected. Caps are editable live in the dashboard; per-agent-type refinements, if ever needed, layer on top of the department cap without replacing it. See RATIONALE §11.

### XI. Self-hosted on Hetzner; no cloud dependencies

Garrison runs on Hetzner with Coolify as the primary deployment target. No component may depend on a managed cloud service (hosted Postgres, hosted queues, hosted vector stores, proprietary cloud APIs). Deployments elsewhere may work but are not optimized for and cannot be required. See RATIONALE §9 trade-offs, §12, and AGENTS.md.

## Precedence when documents conflict

When documents conflict, resolve in this order: `RATIONALE.md` > active milestone context (`specs/_context/m{N}-context.md`) > `ARCHITECTURE.md` > this constitution > installed skills > agent defaults. If a lower authority contradicts a higher one, follow the higher authority and flag the contradiction.

This constitution sits below RATIONALE and the active milestone context deliberately. RATIONALE is the source this constitution distills from and remains authoritative if the two drift. The milestone context is operationally more specific and wins on operational questions.

## Amendment process

Changes to this constitution are not made directly. To amend a principle:

1. Update `RATIONALE.md` first, since RATIONALE is the higher authority. Record the reversed or refined decision, the alternative newly considered, and the trade-off accepted.
2. Update this file to match, keeping the RATIONALE citation accurate.
3. Note the amendment in the next milestone retro, including what changed and why.

If a principle here drifts from `RATIONALE.md` before this process is followed, RATIONALE wins and this file is the one with the bug.

**Version**: 1.0.0 | **Ratified**: 2026-04-21 | **Last Amended**: 2026-04-21
