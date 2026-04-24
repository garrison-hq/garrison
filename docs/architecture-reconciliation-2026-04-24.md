# Architecture reconciliation — 2026-04-24

**Purpose**: record decisions made during the 2026-04-24 session (M2.2.x arc close-out + target-state discussion) and bring the committed docs in line with current operator intent. This is bookkeeping, not argument.

**Companion docs**: `docs/retros/m2-2-x-compliance-retro.md` (arc retro) and `docs/forensics/pgmcp-three-bug-chain.md` (bug forensic).

---

## 1. Decisions made during the 2026-04-24 session

Enumerated for reference. Each decision is load-bearing for one or more docs listed in §2.

- **Target-state architecture: Supervisor ↔ Vault, not Agent ↔ Vault.** M2.3's threat model (`docs/security/vault-threat-model.md` Rule 3) already states "the vault is opaque to agents" and injects secrets as environment variables at spawn time. Any diagram or prose implying a direct Agent ↔ Vault channel is wrong and needs correction.

- **SkillHub (iflytek) committed as target-state skill registry.** Revises the earlier "not a fit at current scale" assessment. Rationale: maturity and governance surface matter more at target-state than operational simplicity. Integration still deferred to M7.

- **MCPJungle designated leading candidate for M8-era MCP server registry.** New target-state concern surfaced during the session. Self-hosted, Go-based, Postgres-backed, combines registry + runtime proxy. Maturity re-check at M7 kickoff.

- **Agent workspace sandboxing tracked as standalone issue.** Deferred to post-M2.3 Docker-per-agent work. Not scope-merged into M2.3 vault work; the two are orthogonal threat models that compose.

- **RATIONALE §3 revision deferred — no change.** §3 is about the memory thesis. Compliance-mechanism work is tracked separately in the M2.2.x arc retro. No cross-contamination between the two.

- **M2.2.x compliance arc closed with both-framings interpretation.** The 12-run post-UUID-fix matrix result (4 clean / 6 partial / 2 fail under Framing A; 10/12 mechanism-compliant under Framing B) is presented without advocating either framing; downstream decisions pick a framing based on what they're asking.

Nothing else was decided in this session. The decisions above are the complete set worth recording.

---

## 2. Docs that need updating

For each currently-committed doc, the current state, what needs to change, and whether the change is a revision (content replaces prior content) or an addendum (content adds to prior content).

### `ARCHITECTURE.md`

**Current state**: v0.4. Covers components (Supervisor, pgmcp, Agent processes, CEO, Web UI, MemPalace), data model, event flow, build plan. No target-state architecture diagram. The Agent-processes section lists MCP tools available to agents as "Postgres (always, from M2.1), MemPalace (always, from M2.2), plus whatever the agent's config declares" — no implied Agent ↔ Vault connection, but also no explicit Supervisor ↔ Vault statement for M2.3.

**What needs to change**:
- **Add** a target-state architecture diagram section. Mermaid or prose. Should show Supervisor as the central process manager, with arrows to Postgres, MemPalace, Claude subprocesses, and (at target state) Infisical. Agents draw from Supervisor-provided env vars; no direct Agent → Infisical arrow.
- **Clarify** the M2.3 forward-reference in the Build Plan section. Current text says "Self-hosted Infisical with Garrison-native UI (operator never sees Infisical's UI directly); secrets injected as environment variables at spawn time; never enter agent prompts or context windows." This is correct and does not need revision; it can be cross-referenced from the target-state diagram section so readers of the diagram see the rule-level enforcement.
- **Add** a cross-reference from the "MCP" concept (§pgmcp, §Agent processes) to `docs/mcp-registry-candidates.md` noting that M8-era third-party MCP integration is a tracked target-state concern.

**Change type**: addendum. No existing content is revised; new sections are added and existing sections are cross-linked.

### `RATIONALE.md`

**Current state**: 13 decisions plus meta. §3 (memory thesis) is the load-bearing decision most affected by the M2.2.x arc.

**What needs to change**:
- **§3: NO revision.** §3 is the memory thesis — whether MemPalace diary entries and KG triples accumulate into useful cross-agent memory. The arc's compliance work is about the mechanism by which those writes happen, not about whether those writes (once they happen) produce useful memory. These are separable concerns.
- **§3: consider a cross-reference note.** An optional one-line note at the end of §3 could read: "Compliance-mechanism work (how agents reliably perform the writes) is tracked separately; see `docs/retros/m2-2-x-compliance-retro.md`." This keeps future readers from conflating the two questions. Optional because §3 already stands on its own; add only if future confusion is anticipated.
- **§4 (Postgres as state store)**: **no change.** The decision is unaffected by the arc. The pgmcp bug chain operated within the Postgres-as-state-store design, not against it. The test-discipline lessons from the forensic belong in pgmcp's own documentation or in `AGENTS.md`'s testing section, not in RATIONALE §4.
- **Other sections**: no changes required by the session's decisions.

**Change type**: addendum only (optional §3 cross-reference). No revision.

### Milestone roadmap

**Current state**: lives in `ARCHITECTURE.md` §Build plan — milestones. Covers M1 through M8 with one-paragraph descriptions. M7 description says "skills.sh integration, proposal UI, approval writes agents + installs skills." M8 description says "Agent-spawned tickets, cross-department dependencies. The last piece of the zero-human loop. Includes runaway control (per-department weekly ticket-creation budget)."

**What needs to change**:
- **M7**: extend description to mention SkillHub as the target-state registry. Existing skills.sh integration language stays; SkillHub is the private-skills component alongside the public skills.sh feed. Cross-reference `docs/skill-registry-dandidates.md`.
- **M8**: extend description to mention MCP server registry as a target-state concern relevant to M8's product-surface integrations (Twilio, Gmail, Microsoft, Vapi). Cross-reference `docs/mcp-registry-candidates.md`. Note MCPJungle as leading candidate.

**Change type**: addendum. Existing milestone descriptions stay; the new lines point at the two target-state docs.

### `docs/security/vault-threat-model.md`

**Current state**: complete design input for M2.3. Rule 3 states "the vault is opaque to agents" and the M2.2 deployment-assumptions addendum at the bottom documents the socket-proxy topology.

**What needs to change**: **no change.** The sandbox-escape concern surfaced during the arc is orthogonal to the vault threat model. `docs/issues/agent-workspace-sandboxing.md` already documents the orthogonality explicitly ("Workspace sandboxing is orthogonal. An agent with no vault-injected secrets can still write outside its workspace. An agent with vault-injected secrets can write those secrets to arbitrary host paths if not sandboxed. The two threat models compose.").

No revisions. No addenda.

### `docs/mcp-registry-candidates.md`

**Current state**: newly added at commit `3de9b9e`. Current content (MCPJungle leading candidate, MCPProxy backup, others not relevant, build-our-own fallback) reflects the session's decision.

**What needs to change**: **no change.** Doc is current as written.

### `docs/skill-registry-dandidates.md`

**Current state**: newly added at commit `3de9b9e`. Current content (SkillHub committed, skify / git-repo / generic-artifact alternatives not chosen) reflects the session's revised decision. Filename note: the current filename contains a typo (`dandidates` instead of `candidates`). The filename is referenced from `docs/mcp-registry-candidates.md` as `docs/skill-registry-candidates.md`. Either the filename needs correcting or the reference needs updating; since the filename ship is committed, the reference in `mcp-registry-candidates.md` may need amending or a renamed file added with a git mv.

**What needs to change**: **no content change**, but the filename typo is a scannability concern. Recommend either:
- Rename `docs/skill-registry-dandidates.md` → `docs/skill-registry-candidates.md` (single `git mv`), or
- Leave the filename as-is and amend the reference in `docs/mcp-registry-candidates.md` to match.

Operator call on which. This reconciliation doc uses `docs/skill-registry-dandidates.md` verbatim elsewhere to match disk truth at the time of writing.

**Change type**: operational fix, not a content change. If renamed, references in `docs/mcp-registry-candidates.md` and any other doc pointing at the file update accordingly.

### `docs/issues/agent-workspace-sandboxing.md`

**Current state**: newly added at commit `c0bcf75`. Documents the sandbox-escape finding, root cause, three distinct concerns, relationship to M2.3 vault (orthogonal), planned post-M2.3 resolution (Docker-per-agent), and interim mitigations.

**What needs to change**: **no content change.** One reference update worth noting — the doc references `experiment-results/post-uuid-fix-haiku-run.md` as the surfacing run. That file is not currently on disk (the experiment-results directory is gitignored; some single-run write-up files mentioned in the M2.2.x arc brief were not retained). The reference isn't load-bearing — the issue's content stands without the referenced run — but a future reader following the link will hit a missing file. Either retain the reference with a note that the file is local to the operator's disk, or drop the reference. Operator call.

**Change type**: optional reference cleanup.

---

## 3. Docs that exist but are stale

Called out during reconciliation.

- **Earlier `docs/skill-registry-candidates.md` assessment ("SkillHub not a fit")** was stale as of the 2026-04-24 session. The current committed file (`docs/skill-registry-dandidates.md`) reflects the revised decision. No stale version remains; the earlier assessment is captured in the doc's "Decision record" section as the 2026-04-24 (initial) entry, superseded by the 2026-04-24 (revised) entry. No action needed.

- **No other docs surfaced as stale during the reconciliation.**

Specifically checked and confirmed current:
- `docs/security/vault-threat-model.md` — current as written; M2.2 socket-proxy addendum correctly reflects M2.2's shipped topology.
- `docs/research/m2-spike.md` — unchanged; still binding for external tool behaviour (Claude Code, MemPalace).
- `docs/ops-checklist.md` — current as of M2.2 ship.
- `docs/retros/m1.md`, `m2-1.md`, `m2-2.md`, `m2-2-1.md`, `m2-2-2.md` — frozen at ship; not revised retroactively. The M2.2.x arc retro does not edit prior retros; it documents revised interpretations separately.
- `AGENTS.md` — current as written post-commit `f2526b9`.
- `ARCHITECTURE.md` — current content is accurate; only additions (target-state diagram, cross-references) needed per §2.

---

## 4. Cross-reference graph

Which docs reference which. Small table, not exhaustive; focuses on the docs involved in the session.

| Source → | Target | Nature of reference |
|---|---|---|
| `docs/retros/m2-2-x-compliance-retro.md` | `docs/retros/m2-2.md` | Prior retro in the arc (contaminated interpretation preserved) |
| `docs/retros/m2-2-x-compliance-retro.md` | `docs/retros/m2-2-1.md` | Prior retro in the arc |
| `docs/retros/m2-2-x-compliance-retro.md` | `docs/retros/m2-2-2.md` | Prior retro in the arc |
| `docs/retros/m2-2-x-compliance-retro.md` | `experiment-results/matrix-post-uuid-fix.md` | 12-run post-fix matrix data |
| `docs/retros/m2-2-x-compliance-retro.md` | `docs/forensics/pgmcp-three-bug-chain.md` | Bug-level detail companion |
| `docs/retros/m2-2-x-compliance-retro.md` | `docs/issues/agent-workspace-sandboxing.md` | Orthogonal sandbox issue |
| `docs/retros/m2-2-x-compliance-retro.md` | `docs/security/vault-threat-model.md` | M2.3 readiness statement |
| `docs/forensics/pgmcp-three-bug-chain.md` | `docs/retros/m2-2-x-compliance-retro.md` | Arc-level narrative companion |
| `docs/forensics/pgmcp-three-bug-chain.md` | `experiment-results/matrix-post-uuid-fix.md` | Post-fix validation data |
| `docs/skill-registry-dandidates.md` | `docs/mcp-registry-candidates.md` | Parallel evaluation, kept separate per operator decision |
| `docs/skill-registry-dandidates.md` | `docs/issues/agent-workspace-sandboxing.md` | Similar target-state-tracking pattern |
| `docs/skill-registry-dandidates.md` | `ARCHITECTURE.md` (hiring flow / M7) | Consumer of the registry decision |
| `docs/mcp-registry-candidates.md` | `docs/skill-registry-dandidates.md` | Parallel evaluation; "Why not paired with SkillHub" rationale |
| `docs/mcp-registry-candidates.md` | `docs/issues/agent-workspace-sandboxing.md` | Similar target-state-tracking pattern |
| `docs/issues/agent-workspace-sandboxing.md` | `docs/security/vault-threat-model.md` | Orthogonal; both threat models compose |
| `docs/issues/agent-workspace-sandboxing.md` | `experiment-results/post-uuid-fix-haiku-run.md` | Surfacing run (note: local-only file per §2) |

Reverse direction (not exhaustive): `AGENTS.md` references the retros and the context files; `ARCHITECTURE.md` will reference the two target-state candidate docs and the arc retro once the §2 updates land. `RATIONALE.md` stays unreferenced from these docs except via the optional §3 cross-reference note.

---

## 5. What this reconciliation explicitly does NOT do

- **Does not revise RATIONALE §3.** Operator decision: the memory thesis and the compliance mechanism are separable concerns. §3 stays.
- **Does not add a write-up of the `finalize_never_called` opus behaviour** (the 1/6 tail event where opus emitted a `result` event with `subtype="success"` at $0.1285 without ever calling `finalize_ticket`). Operator decision: tail event, not load-bearing for M2.3 readiness.
- **Does not propose M2.3 spec work.** M2.3 kickoff is a separate session after these docs land.
- **Does not re-open any closed decisions.** The session's decisions (§1) stand; the doc updates (§2) carry them into committed form.
- **Does not propose next-step sequencing beyond the pending §2 edits.** Operator decides sequencing after reading.

---

## Outcome

The three documents produced during this session — the arc retro, the bug forensic, and this reconciliation — are the bookkeeping for the M2.2.x close-out. The edits listed in §2 (target-state diagram in ARCHITECTURE.md, milestone roadmap touches for M7 and M8, optional filename rename for the skill-registry doc) are the open items. All other committed docs are current as of this session.
