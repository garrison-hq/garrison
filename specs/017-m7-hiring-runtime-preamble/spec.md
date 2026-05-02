# Feature specification: M7 — First custom agent end-to-end (hiring + per-agent runtime + immutable preamble)

**Feature Branch**: `017-m7-hiring-runtime-preamble`
**Created**: 2026-05-02
**Status**: Draft
**Input**: User description: "Hiring flow + per-agent Docker runtime + immutable prompt-hardening preamble (M7)"

**Binding context**: [`specs/_context/m7-context.md`](../_context/m7-context.md) — every section is a binding input to this spec; defaults from "Open questions the spec must resolve" are encoded below per the operator-approved answer set (see Clarifications).

**Substrate spike**: [`docs/research/m7-spike.md`](../../docs/research/m7-spike.md) — §1–§7 question-framing + §8 empirical Docker + claude-prompt probes. §8 is binding for sandbox + preamble decisions.

**Threat models**: [`docs/security/agent-sandbox-threat-model.md`](../../docs/security/agent-sandbox-threat-model.md) (10 rules), [`docs/security/hiring-threat-model.md`](../../docs/security/hiring-threat-model.md) (12 rules). Each rule is binding; FRs cite the rule by number.

**Prior milestone**: M6 shipped 2026-05-02 (PR #18). M7 begins from the post-PR-#19-merge HEAD. The M7-prep bundle (PR #19) carries this context, the spike addendum, and both threat models.

---

## Clarifications

### Session 2026-05-02

These are the operator-approved answers from the `/garrison-specify` pre-flight, encoding the 18 binding questions from the context doc. Each one closes (or explicitly defers with a fallback) a question the plan would otherwise have to re-litigate.

- Q1: Per-agent network scaling (bridge / overlay / shared-bridge). → A: **Defer to plan with bridge-driver fallback.** Plan empirically probes bridge scaling at N≥50 networks; switches to overlay if bridge fails.
- Q2: `hiring_proposals` schema for skill-change proposals (extend vs split). → A: **Extend the existing table** with optional `target_agent_id`, `proposal_type` enum, `skill_diff_jsonb` columns. One table.
- Q3: Image-build vs bind-mount for skills. → A: **Bind-mount, single base image** (per spike §4 lean).
- Q4: `bump_skill_version` UX — full propose→approve cycle vs lighter approve-diff. → A: **Full propose→approve cycle.** Skill bump is a security event; operator fatigue is acceptable.
- Q5: `POST /containers/create` socket-proxy filter precision. → A: **Defer to plan.** Rule shape is `Image` allow-list + `HostConfig.{Mounts,NetworkMode,CapAdd}` filters; plan ships the rules.
- Q6: Existing-agent migration shape. → A: **One-shot grandfathering migration with synthetic audit row.** M2.x-seeded `engineer` and `qa-engineer` rows do NOT pass through the propose→approve gate; the migration writes a `chat_mutation_audit` row marking grandfathered status.
- Q7: Workspace persistence cycle trigger. → A: **Operator-only deactivation.** Auto-on-idle deferred to M9.
- Q8: Skill RO-mount tampering window. → A: **Defer to plan.** Atomic write-then-rename for skill updates; running-agent gets new version on next-spawn (not container restart).
- Q9: Egress grant flow for hired agents. → A: **Same propose→approve cycle as skills.** No separate egress verb.
- Q10: OOM exit handling. → A: **New `runaway` hygiene_status category.** Distinct from `claude_error`; distinct from `crashed`.
- Q11: Sandbox parity for chat runtime. → A: **Full parity at M7 ship.** Chat-CEO containers adopt all 10 sandbox rules.
- Q12: Image digest pinning for `garrison-claude:m5`. → A: **Defer to plan.** Pin to digest in agent-create path; plan owns the digest record-keeping shape.
- Q13: Default registry per environment. → A: **SkillHub primary, skills.sh fallback.** Both enabled by default; operator can disable either via env var.
- Q14: Skill content scanning at approval time. → A: **Coarse regex scan (`curl http`, `wget http`, `nc -e`, `bash -i`, etc.).** Surfaces findings in the approval UX; does NOT block approval.
- Q15: Preamble-vs-skill conflict resolution. → A: **Empirical pre-plan probe required.** Spec commits to "preamble wins via prompt position-power"; plan validates with a real spawn against a contrived skill before /garrison-implement.
- Q16: MCP-server allow-list for hired agents. → A: **Per-agent MCP set granted at hire** (operator approves explicitly). No default-per-skill MCP allow.
- Q17: Audit retention policy. → A: **Indefinite for `chat_mutation_audit` and `agent_container_events`.** Schema-only retention column added now (`retention_class TEXT`); enforcement is M9-or-later.
- Q18: SkillHub live behaviour. → A: **Empirical pre-plan probe required** (operator-side; needs SkillHub creds). Spec defers HTTP client shape to plan; lists open variables.

The three clarify-flag items committed-as-leans (operator confirmed pre-flight option 2 — flag only if wrong):

- F1: Single-tenant filter shape on `hiring_proposals` — committed lean: **structural-only `companies.id` column, no filter.**
- F2: `agent_container_events` vs reusing `event_outbox` — committed lean: **new table** (audit ≠ pg_notify semantics).
- F3: `update_agent_md` chat-verb vs Server-Action-only — committed lean: **Server-Action-only** (operator-edits agent.md via dashboard form, not chat).

---

## User scenarios & testing *(mandatory)*

### User story 1 — Hire a new custom agent end-to-end (priority: P1)

The operator opens CEO chat, says *"I need a security-research agent who can run static analysis on our Go code"*, and the chat-CEO calls `propose_hire` with a draft role proposal: name, role-slug, department, agent.md, suggested skills (e.g. `skills.sh/golang-staticanalysis`, `skillhub.iflytek.com/sast-mcp-bridge`). The proposal lands in `/admin/hires` on the dashboard. The operator reviews — sees the digest of each proposed skill, the coarse-scan findings (none flagged), the proposed agent.md, the immutable preamble that will sit above it — clicks **approve**. The supervisor downloads each skill, verifies digest, extracts safely, builds the per-agent container with the right mounts/caps/network, starts it, and the operator sees a new "security-research" agent in the engineering kanban with status `active`. The agent's first ticket spawns in its container, runs to clean finalize.

**Why this priority**: This is M7's headline. Every M7 thread (hiring, sandbox, preamble) is required for this single story to land safely. P1 because without this, M7 ships nothing operator-visible.

**Independent test**: Send a hire intent in CEO chat; confirm a proposal row in `hiring_proposals`; click approve; confirm an `agents` row + a per-agent container running; spawn a test ticket; confirm finalize succeeds with diary-vs-reality verifier passing.

**Acceptance scenarios**:

1. **Given** the operator's chat thread carries an intent for a new agent role, **when** the chat-CEO calls `propose_hire(role, agent_md, skills, mcp_servers, egress)`, **then** a `hiring_proposals` row lands with `status='pending'` and the dashboard `/admin/hires` shows the proposal with all five attributes.
2. **Given** a pending hire proposal, **when** the operator clicks approve in the dashboard, **then** the supervisor (a) writes the `agents` row, (b) downloads each skill from its registry over HTTPS, (c) verifies digest matches the proposal-recorded digest, (d) extracts each archive with path validation, (e) writes `agent_container_events` create + start rows, (f) starts the per-agent container with the Rule 2 mount layout, Rule 3 network policy, Rule 4 user+caps, Rule 5 resource caps, all in a single transaction (rollback on any sub-failure).
3. **Given** an active agent in its container, **when** the supervisor receives a ticket spawn for that agent, **then** the spawn invokes `docker exec -i` against the persistent container (NOT `docker run`), the immutable preamble is passed via `--append-system-prompt`, the agent runs to finalize, and the diary-vs-reality verifier confirms every claimed artefact path resolves to a real file inside `/var/lib/garrison/workspaces/<agent-id>/`.

---

### User story 2 — Existing M2.x agent migrates to the container runtime (priority: P1)

The M2.x-seeded `engineer` and `qa-engineer` rows have shipped on direct-exec since M2.1. M7's go-live runs a one-shot migration that brings them under the sandbox: builds their containers, writes a synthetic `chat_mutation_audit` row marking grandfathered status, swaps their spawn path from direct-exec to `docker exec`, and confirms a baseline ticket still finalises clean. Existing M2.x integration tests pass unchanged.

**Why this priority**: P1 because M7 cannot ship while two agents bypass the sandbox — the threat-model claim "every agent runs in a container" wouldn't hold. Independent of US1: ships even if no operator hires anything new.

**Independent test**: Run the M2.x integration suite (golden-path, hygiene-evaluator) post-migration; all tests pass; no test depends on direct-exec semantics.

**Acceptance scenarios**:

1. **Given** an M7 deploy, **when** the migration runs, **then** every `agents` row pre-existing M7 has a per-agent container created and started, an `agent_container_events.kind='migrated'` row, and a `chat_mutation_audit` row tagged `grandfathered_at_m7`.
2. **Given** a migrated `engineer` agent, **when** a ticket flows through the standard kanban path (todo → in_dev → done), **then** the spawn fires via `docker exec` against the engineer's container, finalize lands with the same hygiene_status the M2.x suite expects, and no test depends on the supervisor's working directory layout (tickets land via the bind-mounted workspace, not via inherited cwd).
3. **Given** the M2.x-suite integration tests, **when** they run against the migrated runtime, **then** every test passes without modification.

---

### User story 3 — Operator approves a skill-change to an existing agent (priority: P2)

The operator's chat-CEO turn says *"the engineer's lacking a way to read SQL migrations — let's add `skills.sh/sqlmigrate-reader`"*. The chat calls `propose_skill_change(agent='engineer', add=['skills.sh/sqlmigrate-reader@v1.2.0'])`. A new proposal row appears in `/admin/hires` (skill-change variant). The operator reviews the diff — added skill name, digest, coarse-scan findings (none) — clicks approve. The supervisor downloads, validates, extracts, swaps the bind-mount under the engineer container atomically, the engineer's next spawn includes the new skill.

**Why this priority**: P2 because skill-change is the operating mode after the initial M7 ship — most hire actions over time will be skill-changes to existing agents, not new-role hires. P2 not P1 because US1 + US2 ship the foundation; US3 builds on them.

**Independent test**: Issue a `propose_skill_change` against an existing agent; approve; confirm the new skill is in `agents.skills`; confirm the engineer's next spawn sees the skill in `/workspace/.claude/skills/`.

**Acceptance scenarios**:

1. **Given** an active `engineer` agent with a known skills set, **when** the chat calls `propose_skill_change` adding a new skill, **then** a `hiring_proposals` row with `proposal_type='skill_change'` and `target_agent_id=<engineer.id>` lands in `pending`.
2. **Given** an approved skill-change, **when** the supervisor's install actuator runs, **then** it (a) downloads the new skill, (b) extracts to `/var/lib/garrison/skills/<engineer.id>/<skill-package>/`, (c) atomically renames the package dir (write-then-rename), (d) updates `agents.skills[].digest_at_install`, (e) writes a `chat_mutation_audit` row with the diff snapshot.
3. **Given** the next ticket spawn for the engineer post-skill-add, **when** `docker exec` runs claude inside the engineer container, **then** the new skill appears in `/workspace/.claude/skills/<skill-package>/` (bind-mounted RO) and claude can invoke its tools.

---

### User story 4 — Operator rejects a malicious-looking proposal (priority: P2)

The operator's chat-CEO turn produces a proposal whose coarse content scan flags a suspicious pattern (e.g. `curl http://exfil.evil.com` in a skill's bash hook). The dashboard surfaces the flag. The operator clicks reject with a one-line reason. The proposal is not deleted — it stays in `hiring_proposals` with `status='rejected'`, `rejected_at`, `rejected_reason`, and a `chat_mutation_audit` row records the rejection (snapshot included).

**Why this priority**: P2 because rejection is symmetric with approval (hiring Rule 3) and ships in the same release; the audit trail's value depends on rejected proposals being preserved + queryable.

**Independent test**: Issue a `propose_hire` with a contrived skill content that trips the coarse scan; verify the dashboard surfaces the flag; click reject; confirm the proposal row's status, the audit row, and that no `agents` row was written.

**Acceptance scenarios**:

1. **Given** a pending proposal with a coarse-scan flag visible in the approval UX, **when** the operator clicks reject and provides a reason, **then** the row's `status='rejected'`, `rejected_at` is the current time, `rejected_reason` carries the operator's text, and a `chat_mutation_audit` row is written with the proposal snapshot AND the rejection reason.
2. **Given** a rejected proposal, **when** the operator queries `/admin/hires?status=rejected`, **then** the rejection is visible with the original proposal content, the operator-provided reason, and the rejection timestamp.
3. **Given** a rejected proposal, **when** the supervisor processes hire-flow events, **then** no `agents` row exists for that proposal, no skill download is initiated, and no per-agent container is created.

---

### User story 5 — Operator bumps a skill version safely (priority: P3)

A skill the operator approved at v1.0.0 has a newer v1.1.0 published. The operator's chat-CEO says *"bump `skills.sh/golang-staticanalysis` to v1.1.0 on the engineer"*. The chat calls `bump_skill_version`. A new proposal row appears with `proposal_type='version_bump'` showing the diff from v1.0.0 to v1.1.0 — version, new digest, coarse-scan findings on the new bytes. The operator reviews, approves. The supervisor downloads v1.1.0, validates, atomically swaps the bind-mount; the engineer's next spawn picks up the new version.

**Why this priority**: P3 because version bumps are operationally less common than initial hires + skill adds; but ships in the same release because the chat-threat-model verb-set must be complete (M5.3 sealed-set extension precedent).

**Independent test**: Issue `bump_skill_version` against an installed skill; approve; confirm new version's digest in `agents.skills[].digest`; confirm next spawn sees the new bytes.

**Acceptance scenarios**:

1. **Given** an installed skill at version v1.0.0, **when** the chat calls `bump_skill_version(skill, 'v1.1.0')`, **then** a `hiring_proposals` row with `proposal_type='version_bump'` lands carrying both old and new digests + a diff manifest.
2. **Given** an approved bump, **when** the install actuator runs, **then** the v1.1.0 package downloads, extracts to a temp dir, and renames atomically over the existing package dir; `agents.skills[].version` and `digest_at_install` update.

---

### User story 6 — Forensic audit reconstruction (priority: P3)

A month after M7 ships, an issue surfaces: an agent did something unexpected on a specific date. The operator queries `agent_instances` for that timeframe; each row carries `image_digest`, `preamble_hash`, `claude_md_hash`, and a foreign key to the originating `chat_mutation_audit` row. From those four fields the operator can reconstruct: which container image was running, which preamble version was active, what cwd CLAUDE.md content was loaded, and which approval (with snapshot) authorised the agent's existence.

**Why this priority**: P3 because forensic queries are post-incident, not in the operator's daily loop; but the audit shape MUST be in place from day one, so the spec ships it as a P3 deliverable rather than deferring.

**Independent test**: Pick a random `agent_instances` row; verify all four audit fields are populated and resolve to the expected sources.

**Acceptance scenarios**:

1. **Given** a completed agent_instance run, **when** the operator queries it by id, **then** the row carries non-null `image_digest`, `preamble_hash`, `claude_md_hash`, and (if the agent is M7-hired) a `originating_audit_id` referencing the approval snapshot.
2. **Given** a `chat_mutation_audit.id`, **when** the operator queries it, **then** the row's `proposal_snapshot_jsonb` contains the full proposal content as it stood at decision time (not a stale reference).

---

### Edge cases

- **Skill download digest mismatch.** Registry serves bytes whose SHA-256 differs from the proposal-recorded digest. Install fails with `install_failed`; the dashboard surfaces a clear "registry returned different bytes than approved" error. The proposal stays approved (operator already decided); a re-attempt is operator-button.
- **Path-traversal in skill archive.** Archive entry contains `../../etc/passwd` or an absolute path. Pre-validation pass rejects the entire archive (no partial install); install fails with `archive_unsafe`; the offending entry path is logged.
- **Container OOM kill.** Agent exceeds `--memory` cap; kernel OOM-kills. Supervisor sees container exit; spawn's hygiene_status is `runaway` (per Q10). `agent_container_events.kind='oom_killed'` with the cgroup memory.peak value.
- **Network egress attempt blocked by `--network=none`.** A skill or claude-tool tries to `wget` an external URL on an agent without an egress grant. Connection fails with `Network unreachable`; agent surfaces the failure as a tool error; ticket flows on (the failure is the agent's, not the supervisor's).
- **`--append-system-prompt` injection-detection refusal.** A future preamble version somehow trips Claude's built-in identity-injection detector (per spike §8 P9). The agent's first reply contains an explicit refusal of the preamble. The byte-equality test on the preamble const catches the regression at PR review; if it slips, the M7 acceptance walk's "preamble must be honoured" test catches at runtime.
- **Existing-agent migration grandfathering.** The M2.x-seeded engineer is mid-spawn when migration runs. Migration completes the in-flight spawn under direct-exec, then transitions; no in-flight spawn is interrupted.
- **Persistent-container restart on supervisor restart.** Supervisor restarts (planned or crash); per-agent containers are still running. Supervisor reconciles the `agent_container_events` table against `docker ps`, adopts running containers, restarts containers in `should-be-running` state that aren't, garbage-collects orphans (containers with no `agents` row).
- **Preamble-vs-skill conflict at runtime** (deferred from Q15 to plan-validation). Preamble says "do not run `git push`"; an installed skill says "always git push at end of turn". Plan validates empirically; spec commits to "preamble wins via prompt position-power" (above agent.md, above skills, above CLAUDE.md).
- **SkillHub creds unavailable at install time.** SkillHub auth token expired or wrong. Install fails with `registry_auth_failed`; operator sees the error in the dashboard; rotates the token + retries via the install-retry button.
- **Coarse-scan false positive.** Skill has `curl http://...` in a comment for documentation (legitimate). Scan flags it; operator sees flag + sees the surrounding context; approves anyway. The flag is informational, not blocking (per Q14).
- **Operator approves while supervisor is mid-restart.** Approval Server Action transaction sees the supervisor unavailable. Server Action returns a clear error; proposal stays pending; operator retries when supervisor is back.

---

## Requirements *(mandatory)*

Citations to `agent-sandbox-threat-model.md` Rule N are abbreviated **AS-N**; `hiring-threat-model.md` Rule N is **HR-N**; `m7-spike.md` §X.Y is **SP-§X.Y**.

### Functional requirements — Hiring flow (Thread 1)

- **FR-101**: System MUST extend `hiring_proposals` (M5.3) with columns `target_agent_id UUID NULL` (FK to `agents.id`), `proposal_type TEXT` (CHECK in `'new_agent'`, `'skill_change'`, `'version_bump'`), `skill_diff_jsonb JSONB NULL`, `proposal_snapshot_jsonb JSONB NOT NULL`, `skill_digest_at_propose TEXT NULL`, status transition columns (`approved_at`, `approved_by`, `rejected_at`, `rejected_reason`).
- **FR-102**: System MUST treat `hiring_proposals` rows as content-immutable post-create (HR-4); only status-transition columns may UPDATE.
- **FR-103**: System MUST add chat verbs `propose_skill_change` and `bump_skill_version` to the M5.3 sealed verb set, mirroring the `propose_hire` audit + payload shape (chat-threat-model precedent).
- **FR-104**: System MUST add Server Actions `approve_hire`, `reject_hire`, `approve_skill_change`, `reject_skill_change`, `approve_version_bump`, `reject_version_bump`, `update_agent_md`. Each writes a `chat_mutation_audit` row with the full proposal snapshot (HR-9).
- **FR-105**: System MUST integrate with skills.sh (public registry) and SkillHub (private registry) as supervisor-only HTTPS clients; agent containers MUST NOT have direct registry egress (HR-6).
- **FR-106**: System MUST capture each skill's SHA-256 digest at `propose_hire` time AND re-verify at install time; install fails if the digests differ (HR-7).
- **FR-107**: System MUST validate every archive entry's path during extract (no `..`, no absolute, no symlinks pointing outside) and reject the entire archive on any violation (HR-8).
- **FR-108**: System MUST run a coarse static scan on each skill's content at approval time (regex against `curl http`, `wget http`, `nc -e`, `bash -i`, etc.) and surface findings in the approval UX. Scan is informational; it MUST NOT block approval (Q14).
- **FR-109**: System MUST display sibling pending proposals (same chat session, same target agent, similar role) on the approval surface so the operator can pick which they meant (HR-4 enabling Rule).
- **FR-110**: System MUST persist rejected proposals — rejection MUST NOT delete the row (HR-3 carryover).
- **FR-111**: System MUST surface a forensic-query view at `/admin/audit?type=hire` showing every approve / reject / skill-change / version-bump audit row with the snapshot expanded (HR-9).
- **FR-112**: System MUST require a one-shot grandfathering migration for M2.x-seeded `engineer` and `qa-engineer` rows; the migration writes a synthetic `chat_mutation_audit` row with `kind='grandfathered_at_m7'` for each (Q6).

### Functional requirements — Per-agent Docker runtime (Thread 2)

- **FR-201**: System MUST create exactly one persistent Docker container per `agents` row; container lifecycle is bound to agent activation (created+started on activate, stopped+removed on deactivate). NOT one-container-per-spawn (AS-1, SP-§8 P1).
- **FR-202**: System MUST invoke each spawn via `docker exec -i` against the persistent container (NOT `docker run`); stdin/stdout streaming uses NDJSON line buffering verified compatible with `internal/claudeproto` (SP-§8 P2).
- **FR-203**: System MUST create every agent container with `--read-only --tmpfs /tmp:rw,size=64m --tmpfs /var/run -v /var/lib/garrison/workspaces/<agent-id>:/workspace:rw -v /var/lib/garrison/skills/<agent-id>:/workspace/.claude/skills:ro` (AS-2).
- **FR-204**: System MUST create every agent container with `--network=none` then `docker network connect` to a per-agent custom network containing only the supervisor-mediated sidecars the agent needs (mempalace + pgmcp + Infisical by default) (AS-3).
- **FR-205**: System MUST NOT share networks between agents; each agent gets a distinct `garrison-agent-<id>-net` (AS-3).
- **FR-206**: System MUST run agent containers with `--user <agent-uid> --cap-drop=ALL`; UIDs allocated from per-customer ranges defined in supervisor config (AS-4).
- **FR-207**: System MUST set `--memory`, `--cpus`, `--pids-limit` on every agent container; defaults 512m / 1.0 / 200; per-role overrides via `agents.runtime_caps JSONB` column (AS-5).
- **FR-208**: System MUST extend the M2.2 docker-socket-proxy allow-list with `POST /containers/create` filtered on body fields (`Image`, `HostConfig.Mounts`, `HostConfig.NetworkMode`, `HostConfig.CapAdd`); the agent container MUST NOT mount the docker socket (AS-6, HR-12).
- **FR-209**: System MUST wipe `/var/lib/garrison/workspaces/<agent-id>/` on agent deactivation; persisted-past-deactivation state goes through `finalize_ticket` artefact-claim only (AS-7).
- **FR-210**: System MUST stat-verify every artefact path claimed in `finalize_ticket` payloads against `/var/lib/garrison/workspaces/<agent-id>/`; mismatch results in `hygiene_status='missing_artefact'` and finalize rejection (AS-8).
- **FR-211**: System MUST create an `agent_container_events` table recording every container lifecycle event (`kind` ∈ `created`, `started`, `stopped`, `removed`, `migrated`, `oom_killed`, `crashed`); columns include `image_digest`, `started_at`, `stopped_at`, `stop_reason`, `cgroup_caps_jsonb` (AS-10).
- **FR-212**: System MUST add `runaway` to the hygiene_status vocabulary, distinct from `claude_error` and `crashed`. Used when the supervisor observes an OOM kill or pids-limit exhaustion (Q10).
- **FR-213**: System MUST extend `agent_instances` rows with `image_digest`, `preamble_hash`, `claude_md_hash` columns; populated at spawn time (AS-10, FR-303).
- **FR-214**: System MUST adopt running containers on supervisor restart — reconcile `agent_container_events` against `docker ps`, restart containers in `should-be-running` state that aren't, garbage-collect orphans.
- **FR-215**: System MUST apply Rules AS-1 through AS-10 to the M5.1 chat-CEO container at M7 ship (parity, Q11).

### Functional requirements — Immutable prompt-hardening preamble (Thread 3)

- **FR-301**: System MUST define an operator-controlled preamble const, edited via PR + code review only. Const lives in a Garrison-side Go package (location plan-level).
- **FR-302**: System MUST phrase the preamble as policy directives ("Garrison agents: X is prohibited; Y is required"), NOT identity assertions ("You are Z"); identity-style content trips Claude's built-in injection refusal (SP-§8 P9, AS-9).
- **FR-303**: System MUST prepend the preamble const above `agents.agent_md` content in every system-prompt composition; passed via `--append-system-prompt` to claude-code at spawn time.
- **FR-304**: System MUST hash the preamble const at supervisor startup; the hash MUST be recorded on every `agent_instances` row created during that supervisor run (FR-213, AS-10).
- **FR-305**: System MUST hash the cwd CLAUDE.md content at spawn time and record on every `agent_instances` row; CLAUDE.md auto-discovery still loads regardless of `--system-prompt` (SP-§8 P11).
- **FR-306**: System MUST include a byte-equality test that pins the preamble const against a checked-in fixture; future edits show up in code review.
- **FR-307**: System MUST display a banner on the dashboard `/agents/<role>/edit` form explaining: "agent.md edits apply BELOW the immutable security preamble — modifications cannot override hardcoded policy."
- **FR-308**: System MUST validate empirically (during M7 implementation, not retro) that an installed skill containing policy text contradicting the preamble does NOT override preamble enforcement (Q15 deferred-to-plan-with-empirical-probe).

### Functional requirements — cross-thread integration

- **FR-401**: System MUST scope `hiring_proposals` rows by `companies.id` structurally (column present) but apply NO query-time filter at M7 ship (single-tenant; F1 lean).
- **FR-402**: System MUST allow per-agent egress grants through the same propose→approve cycle as skill grants (Q9). Egress grants attach to the per-agent network (FR-205) by joining a shared egress network at activation time.
- **FR-403**: System MUST grant per-agent MCP server access at hire time (operator approves the MCP set explicitly); no default-per-skill MCP allow (Q16).
- **FR-404**: System MUST add a `retention_class TEXT` column to `chat_mutation_audit` and `agent_container_events`; the column is persisted but no enforcement is wired at M7 (Q17 deferred to M9+).
- **FR-405**: System MUST surface every install / migration / start / stop event to the dashboard activity feed, mirroring the M3 + M5.x activity-feed pattern.

### Key entities

- **agents** (existing M2.x table). M7 adds: `image_digest TEXT NULL`, `runtime_caps JSONB NULL`, `egress_grant_jsonb JSONB NULL`, `mcp_servers_jsonb JSONB NULL`, `last_grandfathered_at TIMESTAMPTZ NULL`. Skills column already exists.
- **hiring_proposals** (existing M5.3 table). M7 extends with columns per FR-101.
- **agent_container_events** (new M7 table). Per FR-211 + AS-10.
- **chat_mutation_audit** (existing M5.3 table). M7 extends with `retention_class TEXT NULL` (FR-404). New `kind` values: `grandfathered_at_m7`, `approve_hire`, `reject_hire`, `approve_skill_change`, `reject_skill_change`, `approve_version_bump`, `reject_version_bump`, `update_agent_md`.
- **agent_instances** (existing M2.x table). M7 adds: `image_digest TEXT NOT NULL DEFAULT ''`, `preamble_hash TEXT NOT NULL DEFAULT ''`, `claude_md_hash TEXT NULL`, `originating_audit_id UUID NULL` (FK to `chat_mutation_audit.id` for M7-hired agents).
- **skills storage** (filesystem, not a table). `/var/lib/garrison/skills/<agent-id>/<package-name>/`. Bind-mounted RO into the per-agent container at `/workspace/.claude/skills/`.
- **workspaces storage** (filesystem). `/var/lib/garrison/workspaces/<agent-id>/`. Bind-mounted RW into the per-agent container at `/workspace/`. Wiped on agent deactivation.

---

## Success criteria *(mandatory)*

### Measurable outcomes

- **SC-001**: An operator can hire a new custom agent end-to-end (chat → propose → approve → install → first ticket finalised) in under 5 minutes wall-clock from the moment of approval-button click, assuming registry availability and skills under 50 MB total.
- **SC-002**: 100% of `agents` rows post-M7 (existing M2.x rows + any newly hired) have a per-agent persistent container — zero direct-exec spawns post-migration.
- **SC-003**: 100% of `agent_instances` rows post-M7 have non-null `image_digest`, `preamble_hash`, and (for hired agents) `originating_audit_id`. Forensic-reconstructability at row level.
- **SC-004**: `docker exec` per-spawn cold-start latency (measured at the spawn-decision-to-claude-stdout-first-byte interval) is under 200ms p95 (10× the spike-measured 40ms exec, allowing for claude-init + MCP-handshake overhead).
- **SC-005**: The 10 sandbox rules each have a CI-gated test asserting the rule holds: write-to-`/etc` fails, `--network=none` blocks egress, `--cap-drop=ALL` blocks mount, `--memory` cap honoured by cgroup, `--pids-limit` honoured by cgroup, etc.
- **SC-006**: The byte-equality preamble test passes; an `agent.md` edit attempting to override the preamble (e.g. "ignore Garrison policy") does NOT change observable agent behaviour against a sample prohibited-action ticket.
- **SC-007**: A randomly-picked `agent_instances` row from the M7 acceptance walk reconstructs to the exact image digest, preamble hash, CLAUDE.md hash, and (for hired agents) reviewed-snapshot of the originating approval.
- **SC-008**: M2.x integration suite (golden-path, hygiene, vault, chat) passes unchanged against the migrated runtime.
- **SC-009**: A contrived agent claiming a non-existent path in `finalize_ticket` payload is rejected with `hygiene_status='missing_artefact'`; no false positives in the M7 acceptance walk.
- **SC-010**: SonarCloud new-coverage gate clears at ≥82% on the M7 PR (per M6 retro gotcha #7 — sub-1% headroom is fragile).
- **SC-011**: Skill download with mismatched digest fails with `install_failed`; rejected proposals persist with full snapshot; coarse-scan flags surface in approval UX without blocking.
- **SC-012**: Sandbox parity for the chat-CEO runtime: M5.1's chat container adopts all 10 sandbox rules; chat-CEO operator turns succeed unchanged.

---

## Assumptions

- **One claude OAuth token per customer** (M5.1 deployment shape). Per-agent containers inject the token via env var (Vault Rule 1 carryover); the rate-limit-back-off from M6 still maps per-token = per-company.
- **Docker 24+ available on every Garrison deploy host** (cgroup v2, `--cap-drop=ALL`, `--read-only` + `--tmpfs` composition, `docker network connect/disconnect` semantics). Spike §8 used Docker 29.3.0; 24 is the minimum because of cgroup v2.
- **Skill packages under 50 MB each** (no spike measurement; informed by skills.sh / SkillHub typical sizes). M7 ships without per-skill size cap; M7.1 polish if observed sizes exceed.
- **SkillHub auth token rotation is operator-driven** (manual at M7). Auto-rotation is M9+ if SkillHub adds a refresh-token API.
- **The operator is sole approver** at M7 ship. Multi-approver workflows are M9+ (governance milestone).
- **Per-agent-network bridge driver scales to N=50 agents on a single host**, fallback to overlay if not (Q1 deferred-to-plan).
- **Existing-agent migration is one-shot at M7 deploy time** — not idempotent across multiple M7 deploys; operator runs it once and the synthetic audit row prevents re-run.
- **AGENTS.md active-milestone pointer flips to M7 by the operator at M7 implementation kickoff** (not by this spec).
- **No regression on M5.4's MinIO sidecar or M2.3's Infisical sidecar** — both stay reachable from per-agent containers via the per-agent custom network's selective sidecar attach.

---

## Items to clarify *(post-spec, before plan)*

These survived the binding-input pass and need operator input or empirical input before `/garrison-plan`:

1. **Q1 — Per-agent network driver** — committed-to-bridge-with-overlay-fallback in the clarifications session, but plan must run an empirical N≥50 probe before final commitment.
2. **Q15 — Preamble-vs-skill conflict resolution** — committed lean is "preamble wins via prompt position-power", BUT plan MUST validate empirically with a real opus / haiku spawn against a contrived skill before /garrison-implement.
3. **Q18 — SkillHub live behaviour** — committed-to-deferred; needs an operator-side spike with SkillHub creds before /garrison-plan commits the HTTP client shape.
4. **F1 — Single-tenant filter shape** — lean is structural-only `companies.id`, no query filter. If a customer-A operator is meant to see ONLY customer-A proposals at M7 ship, this needs to flip to "filter at query time."
5. **F2 — `agent_container_events` vs reusing `event_outbox`** — lean is new table. If operator wants the audit shape unified with pg_notify-driven events, this changes.
6. **F3 — `update_agent_md` chat verb vs Server-Action-only** — lean is Server-Action-only. If the chat surface should be able to edit agent.md in-thread, this flips to chat-verb.

---

## Out of scope

Mirrors the context doc's "Out of scope" section verbatim — repeated here so the spec is self-contained:

- BYO-agent (operator-uploaded `agent.md` outside the registry flow) — beta-band per the alpha/beta release-phasing memory.
- Multi-tenant per-customer skill scoping — M9 or beyond.
- MCP-server registry (MCPJungle or alternative) — M8 owns this.
- MCP-server-bearing skills (e.g. `mcp-builder`) — defer to M8.
- Onboarding wizard / template paths — beta-band.
- Mobile UI — beta-band.
- Governance rollback (revert this approval, re-approve at older state) — beta-band.
- Per-customer egress proxy aggregation — M9+.
- Skill SBOM export — M7.1 polish unless cheap.
- Compromised-publisher post-mortem playbook — defer; the audit trail (HR-9 snapshots) is the foundation, the runbook is post-incident response material.
- Mutating sealed M2/M2.3 surfaces beyond the spawn semantics shift (direct-exec → docker exec). Vault rules, finalize tool schema, MemPalace MCP wiring stay sealed.
- The M6 retro deferred items (T009 chaos test, T018 golden-path integration, ThinDiaryThreshold listener wiring) — those are M6.1 polish, not M7.
- Auto-rotating skill upstream changes — operator-explicit `bump_skill_version` only.
- Per-agent customisation of the immutable preamble — preamble is a global const at M7. Per-customer addenda is M9+.

---

## Non-goals (explicit)

Mirrors the context doc's "What this milestone is NOT" section:

- NOT a complete agent-lifecycle redesign. The `agents` table existed since M1; the `agent_instances` lifecycle is M2.x-shipped. M7 adds the runtime-add path and the per-agent container, without redrawing the lifecycle.
- NOT a vault overhaul. Vault env-injection moves from "into supervisor's `exec.Cmd.Env`" to "into the agent container's env" — same Rule 1, same Infisical backend, no API changes.
- NOT a chat substrate rewrite. The M5.3 verb pipeline + M5.x audit pipeline are reused as-is; M7 adds verbs to the sealed set, doesn't redesign the set's machinery.
- NOT a "ship Docker-per-agent first, hiring second" splittable thing. The three threads compose; shipping any one alone leaves the other two as latent risk.
- NOT a multi-customer milestone. Single-tenant posture preserved; customer scoping lands later.
- NOT an immediate cutover for the M2.x-seeded agents. The migration is deliberate, audited, reversible at acceptance time.
