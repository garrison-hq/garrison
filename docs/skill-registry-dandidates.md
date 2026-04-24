# Skill registry options — evaluation (for M7 integration, not yet active)

**Status**: target-state concern. **SkillHub (iflytek) committed as target-state choice** (operator decision, 2026-04-24). Integration deferred to M7, when the CEO hiring flow needs to query a registry to pick skills for newly-hired agents.

When M7's hiring flow activates, the skill registry question becomes load-bearing. ARCHITECTURE.md's hiring flow currently describes "CEO queries skills.sh" — this is sufficient for public skills but does not address private Hey Anton-specific skills (Belgian VAT, Peppol, kinesitherapist regulatory context) that cannot live on skills.sh.

This document captures evaluation work done during M2.2.2 to pre-stage M7's decision.

---

## Candidate options

### SkillHub (github.com/iflytek/skillhub) — COMMITTED TARGET-STATE

**Shape**: enterprise-grade Java/Spring stack, Kubernetes-native, RBAC with audit logs, bootstrap admin discipline, OpenAPI-generated SDKs, semantic versioning of skill packages. Built by iFlytek (a large Chinese AI company) for their Astron agent framework. ~60x the activity of skify by the repo signals.

**Fit assessment**:
- **For**: mature, well-engineered, governed. The governance surface (audit logs, RBAC, semantic versioning, OpenAPI SDKs) aligns with what a multi-tenant Garrison deployment will need once Hey Anton has real customers with regulatory constraints.
- **Against**: over-engineered for current Garrison operational posture (solo-founder, Hetzner + Coolify). Running SkillHub requires a full Spring Boot stack plus Kubernetes. At target-state this is tolerable; near-term it's not.

**M7 readiness**: Operator committed to SkillHub as the target-state choice on 2026-04-24, prioritizing maturity over operational simplicity at target-state. Integration is still deferred to M7; this doc records the decision and the rationale, not build-trigger work.

**Near-term operational note**: operator could experiment with SkillHub standalone for Hey Anton before M7 (use operationally without integrating architecturally). Not a recommendation; just flagging the option.

### skify (github.com/lynnzc/skify)

**Shape**: small, Apache-2.0, Node/TypeScript self-hosted skill registry. CLI (`@skify/cli`), REST API, Web UI, token-based RBAC (read/publish/admin). Dual deployment: Cloudflare Workers (D1 + R2) or Docker (SQLite + filesystem).

**Fit assessment**:
- **For**: smallest honest option for the stated need. Supports publish/version/search/install. Can pull from GitHub repos (including private with token). Works alongside skills.sh rather than replacing it — an aggregator, not a substitute.
- **Against**: 24 stars, single contributor, 14 commits (as of 2026-04-23). This is a personal project maturity-wise. Betting Garrison's M7 hiring flow on it means betting on its continued maintenance or forking it.

**M7 readiness**: **Not chosen.** Operator prioritized SkillHub's maturity despite the operational complexity tradeoff. Remains in this doc for completeness and as a fallback if SkillHub proves untenable at M7 kickoff.

### Git-repo-as-skill-store (vercel-labs/skills pattern, npx skills tooling)

**Shape**: no new infrastructure. Skills live in git repos (public or private); a CLI tool (e.g. `npx skills add owner/repo/skill-name`) pulls them into `.claude/skills/` at the target project.

**Fit assessment**:
- **For**: zero new infrastructure, uses git/GitHub you already trust, no servers to run, no maintenance burden.
- **Against**: no UI for browsing, no full-text search across skills, no versioning beyond git tags, tooling support for private repos is limited as of 2026-04 (vercel-labs/skills#381 tracks this gap).

**M7 readiness**: **Not chosen.** Remains as a zero-infrastructure fallback if SkillHub proves untenable at M7 kickoff.

### Generic artifact repo (Gitea, Forgejo, Reposilite, Nexus)

**Shape**: mature artifact registries repurposed for skills.

**Fit assessment**: not designed for this use case. Would require building installation tooling on top. Only worth considering if you already run one of these for another reason.

**M7 readiness**: **Not a candidate.**

---

## Agent.md as a separate question

The user prompt that surfaced skify originally suggested "store skills AND agent files in there." These are two different architectural questions:

**Skills**: ephemeral, composable, reusable across agents. A registry makes sense.

**agent.md**: core configuration, intentionally per-agent, currently stored inline in Postgres (`agents.agent_md TEXT NOT NULL` per M2.2 schema). Moving agent.md out of Postgres would revise RATIONALE §4 ("Postgres as state store") — not a decision to make casually. The M2.2 `+embed-agent-md` tooling works (agent.md lives as seed migrations, versioned in git, deployed via goose, loaded at startup cache).

**Recommendation for M7**: keep agent.md in Postgres. The hiring flow's approval surface writes to `agents.agent_md` directly. The skill registry question is orthogonal — agents reference skill names; the registry resolves those names to skill content at install time, not at agent-spawn time.

If agent.md iteration speed becomes a real pain before M7, the answer is either:
1. Hot-reload of `internal/agents` cache (already deferred as an M2.2 / M3 open question)
2. Admin UI for editing agent.md live in Postgres (part of the hiring flow's approval surface, M7)

Not a separate registry.

---

## Decision record

**2026-04-24 (initial)**: During M2.2.2 close-out, initial evaluation leaned toward skify or git-repo-as-skill-store, with SkillHub assessed as "not a fit at current scale."

**2026-04-24 (revised)**: Operator reconsidered during target-state architectural discussion and committed to SkillHub specifically. Rationale: maturity matters more at target-state than operational simplicity, and SkillHub's governance surface aligns with what a multi-tenant Garrison deployment will need once Hey Anton has real customers.

**Defer integration to M7.** M2.3 and M3 do not need a skill registry. Current state (skills defined inline in agent config or installed via migration) holds through M3 without blockers.

**Re-evaluate at M7 kickoff** whether SkillHub has remained the right call. Specifically re-check:
1. Is SkillHub still actively maintained and getting security updates?
2. Has MCPJungle or another ecosystem project absorbed skill-registry functionality, potentially collapsing the SkillHub + MCP-registry decision into one system?
3. Is Garrison's operational posture (Hetzner + Coolify) still the same, or has it moved toward Kubernetes in a way that reduces SkillHub's deployment friction?

If any of those answers change the calculus, the decision is open for revision. The commitment here is directional, not permanent.

---

## Related

- `docs/mcp-server-registry-candidates.md` — parallel doc for MCP server registry evaluation. SkillHub and MCP registry are separate concerns per explicit operator decision (2026-04-24); see that doc's "Why not paired with SkillHub" section for the rationale.
- `docs/issues/agent-workspace-sandboxing.md` — unrelated but similar pattern (target-state concern tracked as standalone issue).
- ARCHITECTURE.md hiring flow (M7) — consumer of this registry decision.