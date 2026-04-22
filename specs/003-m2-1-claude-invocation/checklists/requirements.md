# Specification Quality Checklist: M2.1 — Claude Code invocation

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-04-22
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

### Content-quality caveats (accepted by project convention)

Three "Content Quality" items are marked passed with caveats, matching precedent set by the M1 spec:

- **"No implementation details (languages, frameworks, APIs)"** — the spec names Go, `jackc/pgx/v5`, specific syscalls (`syscall.Kill(-pgid, SIGTERM)`), `exec.CommandContext`, `context.WithoutCancel`, SQL statements, and the `claude` CLI's argv. These references are deliberate: M2.1 is an infrastructure milestone whose contract *is* the tool integration (spawn flags, stream-json shapes, process-group signalling). AGENTS.md pins Go and the dependency list; `m2.1-context.md`'s "Spawn contract" pins the argv. Abstracting these would drop binding information. M1's spec followed the same convention; M2.1 follows M1.
- **"Focused on user value and business needs" / "Written for non-technical stakeholders"** — the "user" here is the solo operator running Garrison on Hetzner, whose "business need" is a working supervisor. The spec is written for someone who reads RATIONALE.md, ARCHITECTURE.md, and the M2 spike. It is not written for a non-technical stakeholder, and pretending otherwise would be dishonest. The user-story framing (operator-as-user) is preserved.

### Clarifications resolved (session 2026-04-22)

Three `[NEEDS CLARIFICATION]` markers present in the initial draft were resolved in the `/speckit.clarify` session:

- **Q1 (timeout start-of-clock)** → clock starts at `cmd.Start()`. A single 60-second window covers MCP boot, init handshake, and LLM work. Integrated into NFR-101.
- **Q2 (MCP config write failure)** → record and continue. Failed `agent_instances` row with `exit_reason='spawn_failed'`, event marked processed, no retry, dispatcher keeps running. Integrated into FR-103 and the edge-cases list.
- **Q3 (subprocess exit without `result`)** → fail closed. `status='failed'`, `exit_reason='no_result'`, regardless of exit code. Integrated as FR-110a; exit-reason vocabulary in FR-112 extended to include `no_result` and `spawn_failed`.

All three resolutions are recorded under `## Clarifications / ### Session 2026-04-22` in the spec.

### Binding questions resolved in the spec (not deferred)

Per the input prompt, the seven "Binding questions for `/speckit.specify`" in `m2.1-context.md` must be resolved here, not punted to `/speckit.clarify`. All seven are committed:

1. Postgres MCP server implementation → **in-tree Go server** shipped with the supervisor (FR-116, Assumptions).
2. Postgres read-only enforcement → **Postgres role-based** with SELECT-only grants, plus MCP protocol-layer statement filtering as defense in depth (NFR-104).
3. Subprocess timeout default → **60 seconds** (NFR-101). Start-of-clock semantics flagged as Q1 for clarify.
4. Per-invocation budget cap → **$0.05** via `--max-budget-usd` (NFR-103).
5. Per-invocation MCP config file location → **supervisor-owned state directory**, default `/var/lib/garrison/mcp/` (NFR-105).
6. Engineer agent.md delivery → **inline in `agents.agent_md`** column, with `examples/agents/engineer.md` as a bootstrap example (FR-118).
7. Engineering concurrency cap → **1** (NFR-107).

All checklist items are now complete. The spec is ready for `/speckit.plan`.
