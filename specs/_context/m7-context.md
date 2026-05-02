# M7 — First custom agent end-to-end (context)

**Status**: context for `/speckit.specify`. M6 shipped 2026-05-02 in PR #18; the M7-prep bundle (this doc + retro addendum + spike empirical addendum + 2 threat models + governance amendments) lands in PR #19. M7 implementation begins after PR #19 merges.

**Prior milestone**: M6 retro at [`docs/retros/m6.md`](../../docs/retros/m6.md). M6 closed the M5 → M6 transition: CEO chat substrate is now backed by per-company throttle + hygiene observability. M7 starts from that substrate to ship the first runtime-created custom agent.

**Binding inputs** (read before specifying):

- [`ARCHITECTURE.md`](../../ARCHITECTURE.md) M7 paragraph (amended in this PR): three threads — hiring flow, per-agent Docker runtime, immutable prompt-hardening preamble.
- [`docs/research/m7-spike.md`](../../docs/research/m7-spike.md) — substrate map (§1–§7) plus empirical probe addendum (§8). §8 is binding for sandbox + preamble decisions: 12 probes covering Docker primitives and `claude --print` system-prompt composition.
- [`docs/security/agent-sandbox-threat-model.md`](../../docs/security/agent-sandbox-threat-model.md) — 10 architectural rules for the agent's outbound blast radius. Closes [`docs/issues/agent-workspace-sandboxing.md`](../../docs/issues/agent-workspace-sandboxing.md) by structural enforcement.
- [`docs/security/hiring-threat-model.md`](../../docs/security/hiring-threat-model.md) — 12 architectural rules for the propose → approve → install lifecycle. Mirrors the `vault-threat-model.md` shape.
- [`docs/security/vault-threat-model.md`](../../docs/security/vault-threat-model.md) — Rule 1 (secrets never in prompts) carries unchanged. Vault env-injection happens at container-start time in M7, not at direct-exec time.
- [`docs/security/chat-threat-model.md`](../../docs/security/chat-threat-model.md) — M5.3 sealed verb set. M7 adds verbs (`approve_hire` Server Action, `propose_skill_change` chat verb, `bump_skill_version`) — same amendment shape as M6's `create_ticket` parent extension.
- [`docs/retros/m6.md`](../../docs/retros/m6.md) — open questions deferred forward; gotcha #7 (coverage-gate clearance) and gotcha #8 (`pgtype.Numeric` test fixture) inform M7 implementation discipline.
- [`docs/skill-registry-candidates.md`](../../docs/skill-registry-candidates.md) — SkillHub-vs-skills.sh decision (verdict: SHIP both, SkillHub primary) committed 2026-04-24.
- [`docs/issues/agent-workspace-sandboxing.md`](../../docs/issues/agent-workspace-sandboxing.md) — precursor to the sandbox threat model. Documents the 2026-04-24 haiku run that surfaced the workspace-escape failure mode.
- [`AGENTS.md`](../../AGENTS.md) — locked-deps soft rule (M7 likely adds Docker SDK or skill-registry HTTP client; both need justification). Concurrency rules, spec-kit workflow.
- `supervisor/internal/spawn/spawn.go` + `pipeline.go` — current direct-exec spawn surface; M7's per-agent runtime replaces direct-exec with `docker exec`.
- `supervisor/internal/mempalace/wakeup.go:127` (`ComposeSystemPrompt`) — current system-prompt composition. M7 adds the immutable preamble as a prepended block.
- `migrations/20260430000000_m5_3_chat_driven_mutations.sql` — `chat_mutation_audit` shape; M7 reuses for `approve_hire` etc.
- `migrations/20260424000004_m2_2_1_finalize_ticket.sql` — `finalize_ticket` artefact-claim shape; M7 extends with stat-verification (sandbox Rule 8).

---

## Scope deviation from committed docs

**No scope deviation as of PR #19.** The pre-PR #19 state had a real conflict (ARCHITECTURE.md M7 = "hiring flow only", AGENTS.md "Standing out-of-scope" listed Docker-per-agent), but the PR #19 amendments to both governance docs aligned them with the spike + threat models. The context-doc binding chain is now consistent end-to-end.

One thing worth calling out: **M7 is the first milestone where Garrison's documented threat surface materially expands.** Three threat models (vault, chat, agent-sandbox, hiring) + two retro-binding amendment patterns (chat-threat-model M5.3 verb-set extension, vault-threat-model M2.3 → M3 → M4 banding) sit in the precedent stack. M7 should not invent a fourth pattern — extend the existing ones.

---

## Why this milestone now

M6 shipped the back-pressure (throttle gate) + observability (hygiene predicates) + cost-honesty (result-grace window) the M5.x chat substrate needed before agents could be allowed to spawn at scale. With those guardrails in place, M7 closes the operator's "I want to hire a new role for my company" loop:

1. **Hiring is the unblocker for production deploy.** Today every agent is migration-seeded (`engineer`, `qa-engineer`, `tech-writer`). Adding a new role requires a code change. M7 ships the runtime-add path.
2. **Per-agent runtime is the unblocker for hiring.** Without container isolation, every newly-hired agent inherits the supervisor's direct-exec posture — meaning every operator approval is a trust-the-skill bet without structural backstop. The sandbox rules are what make "approve this skill" survivable.
3. **Immutable preamble is the unblocker for the operator-trust posture.** With third-party skills entering production prompts, the operator needs a stable policy floor that can't be overridden by skill content (intentionally or via injection). The preamble is that floor. The spike §8 P9 finding (Claude rejects identity-override-style preambles as injection) reshapes how the floor is phrased, not whether one is needed.

The three threads compose: hiring without sandbox is unsafe; sandbox without preamble is silent (the operator wouldn't know what was contracted); preamble without sandbox is unenforced. M7 ships them together because shipping any one alone leaves the other two as latent risk.

This timing also matches the **alpha vs beta release-phasing memory**: M7 is alpha-track ("prove it operates"). BYO-agent, multi-company, onboarding wizards, mobile, and governance rollback are explicitly beta. M7's scope respects that line.

---

## In scope

### Hiring flow (Thread 1)

- **Schema** — extend `hiring_proposals` (M5.3 stopgap surface) with proposal-snapshot, digest-at-propose, status transition columns. Mirror M5.3's `chat_mutation_audit` shape.
- **Verbs (chat-side)** — extend the M5.3 sealed verb set: `propose_skill_change`, `bump_skill_version`. Per chat-threat-model precedent, every new verb is a per-row amendment to the doc.
- **Server Actions (dashboard-side)** — `approve_hire`, `reject_hire`, `approve_skill_change`, `reject_skill_change`, `update_agent_md`. Each lands an audit row carrying the reviewed-snapshot (hiring-threat-model Rule 9).
- **Registry clients** — `internal/skillregistry/skillsh.go` + `internal/skillregistry/skillhub.go`. Read-only HTTPS consumers, supervisor-only.
- **Install actuator** — `internal/skillinstall/`. Owns download, digest validation (hiring-threat-model Rule 7), archive extract with path validation (Rule 8), bind-mount-prep at `/var/lib/garrison/skills/<agent-id>/`.
- **Dashboard surface** — sibling-proposal display (Rule 4 makes proposals immutable; operator picks which to approve), digest visualisation, audit-trail forensic view.
- **Existing-agent migration** — `engineer` + `qa-engineer` rows pass through propose → approve gate (or are explicitly grandfathered via a one-shot migration with an audit row recording the grandfathering).

### Per-agent Docker runtime (Thread 2)

- **Per-agent persistent container** — one container per `agents` row; container lifecycle ≠ spawn lifecycle (sandbox Rule 1). Tracked in new `agent_container_events` table (sandbox Rule 10).
- **Mount layout** — `--read-only` + `--tmpfs /tmp` + `-v /var/lib/garrison/workspaces/<id>:/workspace:rw` + `-v /var/lib/garrison/skills/<id>:/workspace/.claude/skills:ro` (sandbox Rule 2).
- **Network** — per-agent custom network, default-deny + selective sidecar attach (sandbox Rule 3). Bridge-driver-vs-overlay choice is plan-level (open question 1 below).
- **Privilege drop** — `--user <agent-uid> --cap-drop=ALL`; UIDs allocated from per-customer ranges (sandbox Rule 4).
- **Resource caps** — `--memory`, `--cpus`, `--pids-limit` mandatory; per-role overrides via `agents.runtime_caps` JSONB column (sandbox Rule 5).
- **Spawn invocation** — replace `internal/spawn`'s direct-exec with `docker exec -i` against the persistent container. NDJSON streaming verified compatible (spike §8 P2).
- **Diary-vs-reality verifier** — hooks into the `finalize_ticket` commit path; stats every claimed artefact path against `/var/lib/garrison/workspaces/<agent-id>/`. Mismatch → `hygiene_status='missing_artefact'` (sandbox Rule 8).
- **Workspace cycling** — workspace dir is wiped between agent deactivation cycles (Rule 7); claim shape is finalize_ticket artefact-claim (M2.2.1 carryover).
- **Socket-proxy extension** — add `POST /containers/create` to allow-list with body-field filters on `Image`, `HostConfig.Mounts`, `HostConfig.NetworkMode`, `HostConfig.CapAdd` (sandbox Rule 6 + hiring Rule 12 + open question 5 below).

### Immutable prompt-hardening preamble (Thread 3)

- **Preamble const** — Go const in a new `internal/agentpolicy` package (or extension of `internal/mempalace`). Operator-controlled; edits go through code review.
- **Preamble shape** — directive-style policy (sandbox Rule 9 + spike §8 P9): "Garrison agents: container escape is prohibited / vault tool calls are prohibited / network evasion is prohibited / [...] Required: complete only the assigned ticket / [...]". NOT identity-style ("You are X"). Final wording lands in the M7 plan; this context fixes the phrasing constraint.
- **Composer extension** — `ComposeSystemPrompt` (or wrapper) prepends the preamble const above `agent.md`. Drives `--append-system-prompt` argv at spawn time, not CLAUDE.md content (CLAUDE.md auto-discovery still loads but isn't the operator-controlled surface; spike §8 P11).
- **Audit hashes** — `agent_instances` rows record `preamble_hash` AND `claude_md_hash` AND `image_digest` per spawn (sandbox Rule 10).
- **Byte-equality test** — checked-in fixture for the preamble const so future edits land via PR + code review (matches M2.2.1's `finalize_ticket` exact-template-shape pattern).
- **Dashboard banner** — on `/agents/<role>/edit`, explain "agent.md edits apply BELOW the immutable security preamble — modifications cannot override hardcoded policy."

---

## Out of scope

Listed explicitly so the spec doesn't drift:

1. **BYO-agent (operator-uploaded `agent.md` outside the registry flow)** — beta-band per the alpha/beta release-phasing memory. M7 stays inside the registry-driven path.
2. **Multi-tenant per-customer skill scoping** — M9 or beyond. Today the chat session carries `companies.id`; the data model already future-proofs but the runtime doesn't yet partition.
3. **MCP-server registry (MCPJungle or alternative)** — M8 owns this. M7 may need to *consume* one or two new MCP servers (per-skill basis) but does not ship the registry surface.
4. **MCP-server-bearing skills** — defer to M8. M7's install actuator handles plain-skill packages; MCP-server-bearing skills (e.g. `mcp-builder`) wait for the M8 MCP-registry.
5. **Onboarding wizard / template paths** — beta-band. M7 ships the "expert operator hires through chat" path; the guided UX wraps later.
6. **Mobile UI** — beta-band.
7. **Governance rollback** — beta-band. M7's audit is the forensic foundation; the formal rollback workflow (revert this approval, re-approve at older state) waits.
8. **Per-customer egress proxy** — sandbox Rule 3 ships per-agent-network egress grants; the per-customer egress proxy aggregation pattern is M9+.
9. **Skill SBOM export** — open question 12 in `hiring-threat-model.md`. Defer to M7.1 polish unless cheap.
10. **Compromised-publisher post-mortem playbook** — defer; the audit trail (Rule 9 snapshots) is the foundation, the runbook is post-incident response material.
11. **Mutating sealed M2/M2.3 surfaces** — supervisor spawn semantics IS being mutated (direct-exec → docker exec) but the change is scoped; vault rules, finalize tool schema, MemPalace MCP wiring stay sealed.
12. **The retro's M6 deferred items (T009 chaos test, T018 golden-path integration, ThinDiaryThreshold listener wiring)** — those are M6.1 polish, not M7. Tracked in M6 retro §"Open questions deferred to the next milestone".

---

## Open questions the spec must resolve

These ride forward from the spike, the threat models, and the retro. The spec should resolve via `/speckit.clarify` or pin as deferred-with-explicit-fallback.

1. **Per-agent network scaling** (sandbox open Q3). At N=100 agents that's 100 Docker networks; default bridge driver caps near 31. The plan-level choice between bridge / overlay / per-agent network namespace under a shared bridge affects deployment topology. Spike says "empirically probe"; spec or plan needs to commit.
2. **`hiring_proposals` schema for skill-change** (hiring open Q1). Extend existing table with optional fields, or split into `agent_proposals` (skill-changes) + retained `hiring_proposals` (new agents)?
3. **Image-build vs bind-mount for skills** (sandbox open Q1). Spike §8.4 lean: bind-mount, single base image. Confirms or revisits.
4. **bump_skill_version UX** (hiring open Q3). Full propose → approve cycle for every version bump, or lighter "approve this version diff" surface? Approval-fatigue tradeoff.
5. **`POST /containers/create` proxy filter precision** (sandbox open Q6 + hiring Rule 12). Filter on `Image` / `HostConfig.Mounts` / `HostConfig.NetworkMode` / `HostConfig.CapAdd` is the rule shape; the actual filter rules are config artefacts. Spec or plan ships the rule set.
6. **Existing-agent migration shape** (sandbox open Q9 / hiring open Q5). Pass M2.x-seeded agents through propose → approve, or grandfather with a one-shot audit row? Affects the audit trail's interpretation forever after.
7. **Workspace persistence cycle** (sandbox open Q4). What triggers "agent deactivation"? Operator-only, or auto-on-idle? M9 makes this question more pointed; M7 should pin a default that doesn't paint M9 into a corner.
8. **Skill RO-mount tampering window** (sandbox open Q5). Atomic rename for skill updates? Running agent gets new version on next spawn or only after container restart?
9. **Egress grant flow** (sandbox open Q7). Same propose/approve flow as skills (lean), or a separate verb? Affects when third-party skills that need HTTP egress can be hired.
10. **OOM exit handling** (sandbox open Q8). New `runaway` hygiene_status category vs. fold into `claude_error`?
11. **Sandbox parity for chat runtime** (sandbox open Q9). Apply Rules 1–10 to M5.1's chat-CEO containers at M7 ship? Lean: full parity.
12. **Image digest pinning for `garrison-claude:m5`** (sandbox open Q10). Tag-reference vs digest-pin in the agent-create path. Spec needs to pin.
13. **Default registry per environment** (hiring open Q2). SkillHub primary + skills.sh fallback (lean) confirmed via env-var defaults.
14. **Skill content scanning at approval time** (hiring open Q6). Coarse static scan on the package before digest is recorded? Surface findings to operator without blocking?
15. **Preamble-vs-skill conflict resolution** (hiring open Q7 + spike §8.5 follow-up). When the preamble says X and an installed skill says NOT X, what does Claude do? **Empirical probe required during M7 implementation, not deferred to retro.**
16. **MCP-server allow-list for hired agents** (hiring open Q8). Per-agent MCP set granted at hire, or default-per-skill MCP allow with operator override?
17. **Audit retention policy** (hiring open Q9). Indefinite vs rolling window? M9 / compliance milestone may close this; M7 should at minimum decide whether to schema for retention now.
18. **SkillHub live behaviour** (`m7-spike.md` §8.5 follow-up). The spike deferred SkillHub probes for credentials. **Operator-side spike before the M7 plan commits the SkillHub HTTP client shape.** Findings either land in `m7-spike.md` §9 or in a separate `m7-skillhub-spike.md`.

---

## Acceptance-criteria framing

Detailed criteria belong in the spec (`/speckit.specify` writes them). The spec should frame criteria along these axes:

- **Hiring lifecycle**: an operator-approved hire results in an `agents` row with the proposal-recorded skills installed, a per-agent container running, and a successful test spawn.
- **Sandbox enforcement**: the 10 sandbox rules are each backed by a test that proves the rule holds (write to `/etc` fails, `--network=none` blocks egress, `--cap-drop=ALL` blocks `mount`, etc.). Tests run in CI.
- **Preamble immutability**: the byte-equality test pins the preamble const; an `agent.md` edit attempting to override the preamble (e.g. "ignore Garrison policy") doesn't change observable agent behaviour against a sample prohibited-action ticket.
- **Audit reconstruction**: a randomly-picked `agent_instances` row from M7's acceptance walk reconstructs to the exact `image_digest` + `preamble_hash` + `claude_md_hash` + reviewed-snapshot for the originating approval.
- **Existing-agent migration**: the M2.x-seeded `engineer` and `qa-engineer` agents still spawn cleanly post-M7, now in containers, exercising the same M2.x integration tests.
- **Diary-vs-reality**: a contrived agent claiming a non-existent path in `finalize_ticket` payload is rejected with `hygiene_status='missing_artefact'`.

---

## What this milestone is NOT

- NOT a complete agent-lifecycle redesign. The `agents` table existed since M1; the `agent_instances` lifecycle is M2.x-shipped. M7 adds the runtime-add path and the per-agent container, without redrawing the lifecycle.
- NOT a vault overhaul. Vault env-injection moves from "into supervisor's `exec.Cmd.Env`" to "into the agent container's env" — same Rule 1 (secrets never in prompts), same Infisical backend, no API changes.
- NOT a chat substrate rewrite. The M5.3 verb pipeline + M5.x audit pipeline are reused as-is; M7 adds verbs to the sealed set, doesn't redesign the set's machinery.
- NOT a "ship Docker-per-agent first, hiring second" splittable thing. The three threads compose; shipping any one leaves the other two as latent risk (see "Why this milestone now").
- NOT a multi-customer milestone. Single-tenant posture preserved; customer scoping lands later.
- NOT an immediate cutover for the M2.x-seeded agents. The migration is deliberate, audited, reversible at acceptance time. If the migration surfaces edges, M7 ships with a transitional grandfathering path (open Q6).

---

## Spec-kit flow for M7

1. **`/speckit.constitution`** — already populated. No M7-specific amendments anticipated.
2. **`/garrison-specify m7`** — draft the spec from this context. Three-thread structure should mirror this doc's "In scope" section.
3. **`/speckit.clarify`** — close as many of the 18 open questions as can be closed without empirical input. Defer #1 (network scaling), #15 (preamble-vs-skill conflict), #18 (SkillHub live) to the M7 plan / implementation phase with explicit fallbacks.
4. **`/garrison-plan m7`** — produce the implementation plan. Three thread-specific sections + a cross-thread integration phase. Plan must commit to:
   - Per-agent network driver (open Q1)
   - `POST /containers/create` proxy filter rule set (open Q5)
   - Existing-agent migration shape (open Q6)
   - Image digest pinning surface (open Q12)
5. **Empirical pre-plan probes** — before plan finalises:
   - SkillHub live spike (open Q18) — operator-side, requires SkillHub creds.
   - Network-driver scaling probe (open Q1) — operator-side or implementer-side; needs a host with N≥50 networks.
   - Preamble-vs-skill conflict probe (open Q15) — implementer-side; needs a real opus / haiku spawn against a contrived skill.
6. **`/garrison-tasks m7`** — break the plan into tasks. Coverage-gate clearance per M6 retro gotcha #7 must be its own explicit task, not a side-effect.
7. **`/speckit.analyze`** — cross-artefact consistency check.
8. **`/garrison-implement m7`** — execute. Per M5.4 → M6 lessons: lint locally before pushing, watch CI on PR, target ≥82% coverage with margin.
9. **Retro** — `docs/retros/m7.md` + MemPalace `wing_company / hall_events` drawer mirror per M3+ dual-deliverable policy. Retro answers in both threat models' "What the M7 retro must answer" sections.

---

## Cross-references

- `docs/research/m7-spike.md` §1–§8 — spike (question-framing in §1–§7, empirical addendum in §8)
- `docs/security/agent-sandbox-threat-model.md` — sandbox rules + adversary model
- `docs/security/hiring-threat-model.md` — hiring rules + adversary model
- `docs/security/vault-threat-model.md` — vault Rule 1 (env-injection) carries through M7's container model
- `docs/security/chat-threat-model.md` — verb-set extension precedent
- `docs/retros/m6.md` — M6 ship + post-T021 polish + M7 gotchas
- `docs/issues/agent-workspace-sandboxing.md` — precursor issue
- `docs/skill-registry-candidates.md` — registry-decision document
