# M7 hiring-flow spike

**Status**: research, not normative. Findings here inform the M7 context doc; the context's open questions cite specific sections of this spike.
**Date**: 2026-05-02
**Branch**: `014-m6-m7-spikes`
**Tooling**: post-M5.4 supervisor + dashboard.

## Why a spike before the context

`ARCHITECTURE.md` defines M7 as: "**Hiring flow.** skills.sh integration plus **SkillHub (iflytek)** as the target-state private-skills registry alongside the public skills.sh feed (decision committed 2026-04-24). Proposal UI, approval writes agents + installs skills."

`docs/skill-registry-candidates.md` already commits the registry choice (SkillHub). What it does *not* commit:
- Whether SkillHub is mature enough at M7 kickoff (the architecture-reconciliation note flags "maturity re-check at M7 kickoff").
- The wire shape of the proposal → approval → install flow.
- Where the approval writes land (`agents` row schema is fixed; the *install* step has no precedent in the codebase yet).
- The MCP-builder authoring loop (referenced in `docs/agents/milestone-context.md` M7 entry but never spiked).

This spike maps the codebase substrate that exists today, surfaces the pieces M7 has to add, and pins the SkillHub re-check + skills.sh + mcp-builder threads as items the M7 plan must close before code lands.

---

## §1 — What's already wired (M5.3 stopgap baseline)

The M5.3 retro left M7 a remarkably clean substrate. M5.3 shipped:

- **`hiring_proposals` table** (`migrations/20260430000000_m5_3_chat_driven_mutations.sql:64`) with the minimum-viable column set:
  ```sql
  id UUID PK
  role_title TEXT
  department_slug TEXT
  justification_md TEXT
  proposed_by_chat_session_id UUID
  proposed_at TIMESTAMPTZ
  status TEXT  -- 'drafted' default; M7 extends with 'approved' / 'rejected' / 'superseded'
  created_at, updated_at
  ```
  Indexed on `(status, department_slug, created_at DESC)` and on `proposed_by_chat_session_id`.

- **`propose_hire` verb** (`supervisor/internal/garrisonmutate/verbs_hiring.go`) — Tier-3 reversibility (M7's review flow marks rejected/superseded; row persists). Always sets `proposed_via='ceo_chat'` and the calling session id. Already in the sealed 8-verb set.

- **`/hiring/proposals` page** (read-only stopgap, M5.3 FR-490..FR-494) — `dashboard/lib/queries/hiring.ts` + dashboard route. Renders the `drafted` rows as a list; no approve/reject affordance.

- **`agents` table** (`migrations/20260422000003_m2_1_claude_invocation.sql:33`) — already has the columns M7 needs for an approval write:
  ```sql
  department_id, role_slug, agent_md, model
  skills JSONB DEFAULT '[]'    -- ← M7 install target
  mcp_tools JSONB DEFAULT '[]' -- ← M7 install target
  listens_for JSONB
  palace_wing TEXT
  status TEXT DEFAULT 'active'
  ```

So M7's "approval writes agents + installs skills" decomposes structurally into:
1. UPDATE `hiring_proposals.status='approved'` (existing column)
2. INSERT into `agents` (existing schema, mostly — see §3)
3. Populate `agents.skills` + `agents.mcp_tools` from the registry (NEW infrastructure — §2 + §4)

What does NOT exist today:
- Any registry client code (no skills.sh fetcher, no SkillHub adapter).
- Any "install" actuator (no code that reads `agents.skills` and pulls skill content into the spawn-time MCP config).
- Approve/reject UI. Today the page is read-only.
- A staging area for proposed roles before they land in `agents` (today the proposal is detached from the agents table — approval is the join point).

---

## §2 — SkillHub maturity re-check — VERDICT: SHIP

**Re-check verdict (2026-05-02)**: **ship M7 with SkillHub** as the private-skills registry. Operator confirmation closes the architecture-reconciliation note's deferred re-check. M7 plan can assume SkillHub-as-registry from the first task; no fallback gate.

**State of the decision** (`docs/skill-registry-candidates.md`): committed 2026-04-24 as target-state. Re-check confirmed 2026-05-02. Rationale unchanged: maturity over operational simplicity at target-state. Operationally a Spring Boot stack + Kubernetes is heavier than the rest of Garrison (which runs on Hetzner + Coolify with ~4 containers); the M7 plan still has to scope the deployment story (single-container vs. K8s vs. managed).

**What the M7 spike should still pin** (the GO/NO-GO is closed; these are the integration questions that remain):

1. **Repo activity** — fetch github.com/iflytek/skillhub, count commits in last 90 days, last release, last issue triaged. The 2026-04-24 evaluation captured "60x the activity of skify" but didn't quote absolute numbers. Re-quote them now.
2. **Deployment shape** — confirm the published artifacts. Is there a Docker Compose path? A single-container option? Or only the full Spring Boot + Kubernetes setup? Garrison's deployment posture at M7 cannot absorb a Kubernetes runtime.
3. **API surface** — pull the OpenAPI spec. Map the endpoints M7 actually needs:
   - skill-package search (CEO chat queries: "find me a Belgian-VAT skill")
   - skill-package install / fetch
   - skill-package versioning (read latest stable)
   - audit log read (does Garrison record SkillHub-side audit events, or duplicate them in `chat_mutation_audit`?)
4. **Auth model** — RBAC with bootstrap admin. Does the operator-CEO have a separate token from the supervisor? How do tokens rotate? Where do they live (Infisical?)?
5. **Public skills.sh fallback** — SkillHub doesn't replace skills.sh. M7 needs both. The fallback semantics (what if the SkillHub instance is down?) need to be in the M7 context's binding decisions.

**Resolved**: the architecture-reconciliation note's "abort-criteria for SkillHub" is moot —
the operator's 2026-05-02 confirmation lifts the deferred-re-check gate.
skify and the git-repo-as-skill-store remain in `docs/skill-registry-candidates.md`
as historical context, not as live fallbacks.

---

## §3 — skills.sh + agent.skills column semantics

**`agents.skills` column** is `JSONB DEFAULT '[]'`. Today nothing writes to it — every agent row in the codebase has `skills='[]'`. The dashboard's edit-agent surface (`/agents/<role>/edit`) renders the field as JSON but treats it as opaque. So the format is undefined.

**What M7 has to define** (this is the binding decision the context doc must close):

The natural shape is an array of `{registry, package, version}` objects:
```json
[
  {"registry": "skills.sh", "package": "anthropic-postgres-best-practices", "version": "latest"},
  {"registry": "skillhub", "package": "hey-anton/belgian-vat", "version": "1.2.3"}
]
```

But the *install* step (read this list → fetch the skill content → place it where the spawned claude can find it) has no implementation today. Garrison's spawn path uses `--mcp-config /tmp/<spawn-id>.json` (M2.x); skills are a different surface — they're prompt-side context, not MCP tools.

**`anthropics/skills/mcp-builder`** — referenced in `docs/agents/milestone-context.md` M7 as "MCP server authoring patterns." Not yet read. The M7 spike needs to:
1. Pull the mcp-builder skill from skills.sh.
2. Read its README to confirm the authoring loop.
3. Decide whether mcp-builder is part of the M7 install surface (i.e. an installed skill that lets the CEO author NEW MCP servers) or a meta-skill for Garrison's own development.

---

## §4 — The "install" actuator + per-agent runtime: scope-merged

**Scope decision (2026-05-02)**: the agent-workspace-sandboxing issue
(`docs/issues/agent-workspace-sandboxing.md`, tracked since 2026-04-24 as a
"post-M2.3 Docker-per-agent" item) merges into M7. Rationale: M7 introduces
the first runtime-created agents AND the first per-agent custom skills. The
container is the natural skill-install boundary — storage isolation,
refresh boundary, leak-scan target — so resolving the runtime + the install
actuator in the same milestone gives a single coherent story instead of
shipping two halves that have to be re-wired later.

The verb `propose_hire` lands a proposal row. M7 ships an approval flow.
After approval, *something* has to:

1. Read `agents.skills` (post-INSERT).
2. For each entry, fetch the skill package from the named registry.
3. **Build (or reuse) the per-agent container image** — base = the M5.1
   `garrison-claude:m5` image (already proven for the chat runtime); add
   the agent's installed skills as a layer (or bind-mount) at
   `/workspace/.claude/skills/<package>/`.
4. **Spawn the agent in the per-agent container** — instead of the current
   direct-exec on the supervisor host. Claude finds installed skills in
   the container's `~/.claude/skills` (claude's native convention).

So M7 closes both the install actuator AND the three sandboxing concerns
(`docs/issues/agent-workspace-sandboxing.md` §"Three distinct concerns"):
sandbox escape, diary-vs-reality path divergence, and the QA cross-check
gap — all become moot when paths inside the container can't diverge from
disk truth.

**Substrate** (already in the codebase, do NOT redesign):

- `garrison-claude:m5` image — built for chat in M5.1; auths via
  `CLAUDE_CODE_OAUTH_TOKEN` env var (no keychain mount needed). Cold
  start ~30s for the bake, per-turn under 1s once warm.
- Docker socket-proxy at `garrison-docker-proxy` (TCP :2375, allow-list
  `POST/EXEC/CONTAINERS`). Already used by M2.2 mempalace + M5.1 chat.
  M7 needs `POST /containers/create` added to the allow-list (the
  socket-proxy gate's biggest extension to date — needs an audit).
- Vault-injection idiom (M2.3): supervisor injects per-agent secrets via
  env vars at spawn time. Container path inherits this directly.

**Open questions for the M7 plan**:

1. **Storage model**: per-agent-row Docker image (heavy — N agents = N
   images) or single base image + per-agent bind-mount of
   `/var/lib/garrison/skills/<agent-id>/` into `/workspace/.claude/skills`?
   Lean: bind-mount, single image. Image rebuilds become an M7 retry.
2. **Refresh cadence**: re-fetch skills on every agent spawn (slow,
   stale-free) or cache and rotate on a schedule (fast, stale-risk)?
   Lean: cache; supervisor invalidates on `agent.skills` UPDATE.
3. **Skill leak-scan**: skills can carry arbitrary prompt text + tool
   definitions. Per `docs/issues/agent-workspace-sandboxing.md` §"Three
   distinct concerns" + M2.3 vault threat model, an installed skill should
   pass leak-scan before it lands. Reuse the M2.3 finalize leak-scan?
4. **Versioning**: pin to a version tag at hire-time, OR follow the
   registry's `latest`? Pin is safer; latest is more honest about a
   living registry. Probably pin + a `bump_skill_version` verb.
5. **Failure mode**: if the install fails post-approval, is the agent row
   rolled back? Or does the agent enter `status='install_failed'` and
   the operator retries from a button?
6. **Docker-proxy allow-list extension**: adding `POST /containers/create`
   broadens the proxy's surface from "exec into existing containers" to
   "create new containers". This is a one-time threat-model amendment
   that needs the same posture as the M2.3 vault work — explicit
   security-doc update before code lands. See
   `docs/issues/agent-workspace-sandboxing.md` §"Planned resolution".
7. **Rollout**: do all existing agents (engineer, qa-engineer) move to the
   container runtime when M7 ships, or only newly-hired ones? The
   migration matters for backwards-compat: existing departments won't
   have an M7 container image. Lean: rebuild all agents at M7 ship.

---

## §5 — What M7 inherits cleanly from M5.x

- The proposal table + verb. **Don't redesign** — the schema is ready for the status transitions M7 adds.
- The `chat_mutation_audit` table. Every M7 approval write goes through the same audit pipeline.
- The dashboard's read-only proposals page is a styling start; M7 extends with approve/reject buttons.
- The `agents` schema needs no change for the `.skills` extension — the column is already there.
- The vault threat model is unchanged; M7 doesn't introduce new agent-vs-vault interactions.
- **The chat-container runtime** (M5.1's `garrison-claude:m5` image + token-via-env auth + socket-proxy gate) — M7 reuses the image as the agent-container base, extending §4's per-agent skill-mount on top. This is the largest single thing M7 inherits.

What M7 does NOT inherit (must build):

- Registry client(s).
- Install actuator + storage layout (now scope-merged with the agent runtime per §4).
- Approve/reject server actions on the dashboard.
- Per-skill leak-scan policy (or an explicit decision to skip — likely reuse M2.3's leak-scan).
- The "agents row appears post-approval" lifecycle — today every agent is seeded by a migration; M7 introduces the first runtime-created agent rows.
- **`POST /containers/create` allow-list addition** to the docker-socket-proxy (one-time threat-model amendment; see §4 Q6).

---

## §6 — Open questions for the M7 context doc

1. ~~**SkillHub re-check verdict** — at M7 kickoff: ship-with-SkillHub, ship-with-fallback, or defer M7 entirely?~~ **Closed 2026-05-02: ship-with-SkillHub.** Deployment shape (single-container vs. K8s vs. managed) rolls into the M7 plan's first task.
2. **`agents.skills` JSONB shape** — finalize the entry schema (registry / package / version / [config]?).
3. ~~**Install storage layout** — supervisor-host filesystem, per-customer? per-spawn? per-agent-row?~~ **Lean closed via §4 scope-merge: per-agent bind-mount of `/var/lib/garrison/skills/<agent-id>/` into the per-agent container's `/workspace/.claude/skills`.** Plan-level decision.
4. **Install timing** — at-approval (eager, container rebuild) or at-first-spawn (lazy bind-mount)? Affects rollback semantics + cold-start latency.
5. **mcp-builder integration** — installed-skill or excluded-from-skill-list (development-only meta-tool)?
6. **Failure-mode UX** — what does an operator see if `install_skill` fails between approval and agent activation?
7. **Multi-tenant scope** — when Garrison hosts multiple customers, do skill installs get a customer-id prefix on the storage path? Do they inherit Infisical-style secret scoping?
8. **Approval guardrails** — the operator approves; does the supervisor enforce any policy (e.g. skill from an allow-listed registry)? Or is approval purely operator-judgment?
9. **Existing-agent migration** — when M7 ships per-agent containers, do the M2.x-seeded `engineer` + `qa-engineer` rows migrate to the container runtime, or only newly-hired agents? Lean: full migration (otherwise we keep the direct-exec runtime alive forever).
10. **Image-build vs bind-mount tradeoff** — per-agent rebuilds of `garrison-claude:m5 + skills` are slow but immutable; bind-mounts are fast but mutable. The choice affects audit (can the operator point at a single immutable image hash for a given agent invocation?).

---

## §7 — Things this spike does NOT cover (pre-context-doc work)

- Live SkillHub deployment test (pre-context-doc — needs an operator-approved staging plan).
- Empirical install latency measurements (depends on §6 Q4 outcome).
- Full mcp-builder skill audit (read of the skill's source — out-of-scope for a research spike, in-scope for the M7 plan doc).
- The dashboard UX for the approval surface (separate design pass).
- skills.sh API stability check — public registry, but its deprecation cadence affects M7's fallback path.

These are flagged so the M7 plan doc can scope them explicitly.
