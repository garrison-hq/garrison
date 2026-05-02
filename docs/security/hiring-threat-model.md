# Hiring flow — threat model and architectural rules

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

**Status**: Threat model and architectural rules. Drafted 2026-05-02 as
binding input to the M7 (hiring + per-agent runtime) context file.
Sits alongside `docs/security/vault-threat-model.md` (credential
injection), `docs/security/chat-threat-model.md` (operator-vs-agent
input boundary), and `docs/security/agent-sandbox-threat-model.md`
(agent outbound blast radius). This document covers the
**proposal → approval → install** lifecycle that creates the agents
those other documents protect.

**Last updated**: 2026-05-02 (initial draft).

**Precedence**: this document lives below `RATIONALE.md` and the
active milestone context in the document hierarchy (see `AGENTS.md`).
The active milestone context (`specs/_context/m7-context.md` once
written) supersedes this document for operational conflicts; this
document supplies the threat model and architectural principles that
context files cannot re-derive cheaply.

---

## Scope of this document

This is a threat model and a set of architectural rules. It is NOT a
spec, a plan, or an implementation. The spec-kit flow for any hiring-
touching milestone begins from the relevant context file that cites
this document as binding input.

The document covers:

1. What the hiring flow protects (assets)
2. Who it protects against (adversaries)
3. What threats it addresses and which it explicitly accepts
4. Architectural rules Garrison enforces in the hiring integration
5. What the registry providers (skills.sh, SkillHub) supply vs. what
   Garrison builds, with the M7 milestone split
6. Open questions later milestone specs must resolve
7. What each milestone retro must answer

**Adjacent input**: M5.3 already shipped a `propose_hire` chat verb
that writes a row to `hiring_proposals` (audit-only, no install
actuator). M7 closes the loop — approval → write `agents` row →
install skills → activate the per-agent container (handing off to
`agent-sandbox-threat-model.md` from that point forward).

**Empirical input**: `docs/research/m7-spike.md` §2–§4 carries the
SkillHub deployment-shape decision and the install-actuator scope-
merge. §8 carries Docker / preamble probe observations that affect
Rules 4, 6, and 9 below.

---

## 1. Assets

**What hiring protects**:

- **The `agents` table.** Every row is a long-lived spawn template.
  Adding a row creates a runtime entity with a role, a system prompt
  (`agent.md`), an installed-skills set, and a per-agent container
  identity. A row written maliciously is a persistent foothold.
- **The `agents.skills` JSONB column.** Each entry pins a registry +
  package + version. The supervisor reads this at container
  bind-mount time; whatever package text the registry serves becomes
  in-prompt instructions for the agent. Tampering here = tampering
  with future agent behaviour.
- **The `agents.agent_md` column.** The role-specific system prompt.
  Edited by the operator via the dashboard (M3+ surface); used by
  the supervisor at every spawn (M2.1 carryover). Tampering here =
  changing what every spawn of this role does.
- **The hiring-proposal queue (`hiring_proposals` table).** Pending
  proposals sit in a state where their content has been chat-
  authored but not operator-approved. A proposal that survives
  approval becomes an `agents` row; a proposal corrupted between
  write and approval can be approved with content the operator
  didn't see.
- **The skill-package storage at `/var/lib/garrison/skills/<agent-
  id>/`.** The supervisor downloads + extracts skill packages here
  before bind-mounting them into the agent container. The download
  + extract path is a privileged operation running as the supervisor
  user; tampering with the package between download and bind-mount
  installs poisoned skill content.
- **The hire-flow audit trail (`chat_mutation_audit` rows for
  `propose_hire` / `approve_hire` / `reject_hire`).** The operator's
  authority for decisions during M7 — every hire/reject must be
  reconstructable from this trail.

**Multi-tenancy posture**: single-tenant at M7 ship. The hiring flow
already carries `companies.id` via the chat session context (M5.x);
per-customer skill scoping is anticipated post-M9.

---

## 2. Adversaries

Ranked by realistic probability of affecting a deployed Garrison
instance operationally.

1. **A malicious skill on a public registry** (skills.sh). The
   public skills.sh feed accepts uploads from any user under that
   registry's policy. A malicious skill could: carry shell-injection
   payloads disguised as workflow text, embed prompt-injection
   instructions designed to override the agent's later tasks, encode
   exfiltration calls in seemingly-benign tool descriptions, or
   bundle a poisoned MCP server. Most realistic post-M7 ship —
   the registry is open by design. Prompt-injection probes are
   especially easy to plant.

2. **A malicious skill on a private registry** (SkillHub). Same
   vector as #1 with a smaller publish set, but skill provenance is
   weaker: SkillHub's account model is uneven, and the pin-by-version
   semantics may not be cryptographically tied to a specific
   publisher (open question 6.5). A maliciously-published skill that
   shares a name with a legitimate one ("typosquatting") can be
   approved by an operator who recognises the name but not the
   publisher.

3. **Prompt injection during chat-driven proposal authoring.** The
   `propose_hire` verb is invoked by the chat-CEO turn — meaning the
   operator's chat input drives the proposal content, but so do
   prior chat messages, MCP tool results recalled into the turn, and
   palace recall. A malicious external input (a prior ticket
   description, a webhook-sourced palace drawer, an MCP tool result)
   could trick the chat-CEO into proposing an agent with skills /
   role / agent.md content the operator wouldn't have specified.
   The operator's approval is the catch — but only if the operator
   reads the proposal carefully.

4. **Operator approving without reading.** Solo-operator scale means
   the operator has dozens of decisions per day; approval fatigue is
   real. An attacker who can land a prompt-injection-driven proposal
   (#3) is betting on a quick-approve click rather than line-by-line
   review. Same threat surface as `vault-threat-model.md` §2.1
   ("operator making mistakes").

5. **Tampering with the hiring proposal between propose_hire and
   approve_hire.** A proposal row in `hiring_proposals` is mutable
   between proposal-create and proposal-approve. A second chat turn
   that edits the proposal, a direct UPDATE via a compromised
   supervisor process, or a race during chat-driven mutation audit
   (M5.3 carryover) — all could change content the operator
   reviewed in the dashboard before they clicked approve.

6. **Skill download-time tampering.** The supervisor fetches the
   skill package from the registry over HTTPS. A registry-side
   compromise, a CDN cache poisoning, or a TLS misconfiguration
   could substitute a malicious package between operator-approval
   and bind-mount. Within Garrison's control: the digest pin (open
   question 6.5).

7. **Skill extraction-time tampering.** The package format may be
   tar / zip / arbitrary-archive. Path-traversal attacks during
   extraction (entries like `../../etc/passwd`) could write outside
   the intended skill directory. Standard untar-with-validation
   territory; flagged so the M7 plan addresses it.

8. **Pre-existing M2.x agent role-escalation via skill add.** The
   M2.x-seeded `engineer` and `qa-engineer` rows have empty
   `agents.skills`. The hiring flow's `update_agent` path (M5.3
   verb, scope-extended in M7?) could be used to ADD skills to an
   existing role, effectively escalating the role's capability set
   without going through the propose/approve cycle. M7 plan should
   pin whether skill addition to existing roles is in-scope and
   whether it requires the same approval cycle.

**Adversaries we explicitly deprioritise**:

- **Operator approving with full read + intent to compromise.** If
  the operator deliberately approves a malicious skill, the hiring
  flow can't save them. Same posture as `vault-threat-model.md` §2.
- **Anthropic-side compromise of skill processing.** If Claude itself
  is backdoored to interpret skill content adversarially, the system
  is compromised regardless of the hiring flow. Claude Code supply-
  chain risk is acknowledged but out of reach.
- **Host-level attackers with shell on the Garrison host.** They can
  edit `/var/lib/garrison/skills/<agent-id>/` directly, bypassing
  the entire flow. Host-level mitigation is host-level (SSH,
  filesystem permissions, audit logging).
- **Registry-publisher account compromise.** A legitimate publisher
  whose registry account is compromised pushes a malicious version
  of an otherwise-trusted skill. Garrison can pin to digest
  (architectural rule 7) but can't detect a compromised account
  publishing a new version that the operator then explicitly bumps
  to. Publisher-side authentication is out of reach.

---

## 3. Threats addressed vs. accepted

### Threats the hiring flow explicitly addresses

1. **Malicious skill landing without operator review.** Mitigated by
   the propose → approve gate: no `agents` row is written without
   an explicit operator-approve action, and no skill is bind-mounted
   until that row exists. Approval is a discrete dashboard-button
   action audited via `chat_mutation_audit`.

2. **Approved-skill content differing from reviewed-skill content.**
   Mitigated by Rule 7 (digest pin): the digest of the package the
   supervisor will bind-mount is recorded at approval time; if the
   registry serves different bytes at install time, the digest check
   fails and install errors out. The operator's review state is
   preserved.

3. **Path-traversal during skill extract.** Mitigated by Rule 8 (safe
   extract): the supervisor validates every archive entry is a
   relative path inside the target directory; entries with `..`,
   absolute paths, or symlinks pointing outside are rejected. Skill
   package falls through with `install_failed` status.

4. **Proposal tampering between propose and approve.** Mitigated by
   Rule 4 (proposal immutability post-create): once `propose_hire`
   writes a `hiring_proposals` row, the row is read-only until the
   operator approves OR rejects. Edits go through the chat verb
   (which writes a new proposal); the operator sees both proposals
   and picks which (if either) to approve.

5. **Hiring-flow-driven escalation of M2.x agents.** Mitigated by
   Rule 5 (skill-add-to-existing-role through the same gate): adding
   a skill to an existing `agents` row uses the same propose →
   approve cycle, audited identically. No `update_agent` verb path
   that bypasses the gate.

6. **Operator forgetting which proposal they approved.** Mitigated
   by Rule 9 (audit row carries the proposal snapshot): every
   `chat_mutation_audit` row for `approve_hire` includes the full
   reviewed proposal content at the time of approval, not just a
   reference to a (potentially-edited-since) `hiring_proposals` row.
   Forensic queries can reconstruct exactly what was approved.

7. **Agent.md tampering after hiring.** Mitigated by Rule 10 (agent.md
   edits go through the same chat-mutation audit as skill changes):
   even though `agent.md` is operator-edited via a dashboard form,
   each edit lands through the M5.3 verb pipeline with audit + chat
   session attribution. Direct UPDATE-via-supervisor-process is not
   exposed to the chat surface.

8. **Skill-server prompt-injection.** Mitigated by the immutable
   preamble (`agent-sandbox-threat-model.md` Rule 9): any prompt-
   injection content embedded in a skill is read by Claude as
   data within an MCP tool result, AFTER the immutable preamble has
   set the policy that injected instructions are non-binding. Belt-
   and-suspenders with the sandbox's structural rules.

### Threats explicitly accepted

1. **Operator approving a malicious skill in good faith.** If a
   skill carrying prompt injection passes operator review (because
   the injection is well-disguised, or the operator trusts the
   publisher), the skill becomes an installed-agent threat handled
   by `agent-sandbox-threat-model.md`. The hiring flow can't decide
   what the operator should trust.

2. **Registry-publisher compromise.** Garrison pins to digest at
   approval time. A subsequent legitimate-publisher version bump
   that's been compromised post-publish requires the operator to
   explicitly run a `bump_skill_version` action (M7 verb). The
   operator's bump approval is the same review surface as initial
   approval — same risk class as #1.

3. **Skill upstream that the operator chooses to trust.** Once a
   skill is approved, its in-prompt content runs in every spawn of
   that agent. Garrison doesn't re-review approved skills on each
   spawn; the trust is per-skill-version, not per-spawn. Operator
   policy decision; documented expectation.

4. **Skill content shadowing operator policy.** The immutable
   preamble (`agent-sandbox-threat-model.md` Rule 9) sits ABOVE
   `agent.md` and the installed skills, in prompt position. Skills
   cannot structurally override the preamble. But a skill CAN add
   contradicting policy text that confuses the model — Claude's
   conflict-resolution between preamble + skill is empirical (open
   question 6.7). Residual risk; bounded by the sandbox.

5. **Approval-flow timing attacks.** A malicious chat-CEO turn could
   propose at one moment and re-propose with edits seconds later,
   hoping the operator's review window catches the second proposal
   they thought they approved the first. Mitigated partly by Rule 4
   (proposals are immutable; an "edit" is a NEW proposal). But the
   operator could still mis-click. Approval UX surfaces both
   proposals side-by-side; operator can mis-read both.

6. **Side-channel via operator dashboard.** A malicious skill could
   alter what its description reads as in the dashboard if it
   serves different content to a stat / fetch GET vs an installed-
   bind-mount read. Garrison only consumes the package at install
   time; the dashboard description is fetched separately at
   approval time. Versioned mismatch is possible but the digest
   pin makes it auditable.

---

## 4. Architectural rules (binding for M7 and beyond)

These rules are binding. The M7 spec may not contradict them. If a
future retro surfaces a reason to revisit a rule, the rule is amended
here before the spec changes.

### Rule 1: Hiring proposals are write-only via the chat verb

The `hiring_proposals` table is written ONLY by the M5.3-shipped
`propose_hire` chat verb (and the M7 `propose_skill_change` extension
for skill-add-to-existing-role per Rule 5). No supervisor-side
non-chat code writes proposals. No dashboard-direct INSERT path. The
chat session's `mutating_user_id` becomes the proposal author.

This guarantees every proposal is reconstructable to a chat session,
which is reconstructable to an operator + a turn.

### Rule 2: Approvals are write-only via the dashboard verb

The `agents` row INSERT (and the matching `chat_mutation_audit` row)
is written ONLY by the M7-shipping `approve_hire` Server Action,
triggered by an explicit operator-button click in the dashboard. No
chat-side approval path. No bypass via direct SQL. The button's
Server Action handler validates the proposal exists, reads its
content, writes the agent row, schedules the install, and writes the
audit — all in one transaction.

This guarantees every agent row is attributable to a specific operator
action with a timestamp.

### Rule 3: Reject is symmetric with approve

The `reject_hire` Server Action follows the same Server-Action +
button + audit shape. Rejected proposals are NOT deleted — they
remain in `hiring_proposals` with a `status='rejected'` and a
`rejected_at` + `rejected_reason`. Forensic queries can reconstruct
the operator's decision trail (what they considered + rejected).

### Rule 4: Proposals are immutable post-create

Once `propose_hire` writes a row, the row's content fields are
read-only. The only mutations allowed are status field transitions
(`pending` → `approved` / `rejected`) plus their accompanying
metadata (`approved_at`, `approved_by`, `rejected_at`,
`rejected_reason`). A subsequent chat-driven "edit my proposal"
intent results in a NEW proposal row, not an UPDATE.

The dashboard surfaces sibling proposals (same chat session, similar
role) so the operator can choose the one they actually meant. The
operator never approves a proposal whose content has changed since
they last viewed it.

### Rule 5: Skill changes to existing agents go through the same gate

Adding, removing, or version-bumping a skill on an existing `agents`
row requires the same propose → approve cycle as initial hire. M7
ships a `propose_skill_change` chat verb (analogous to
`propose_hire`) and an `approve_skill_change` Server Action
(analogous to `approve_hire`). The audit row carries the diff (added
/ removed / bumped) so the operator's review surface is concrete.

No `update_agent` verb path that adds / removes skills without going
through this gate.

### Rule 6: Registry fetches go through a supervisor-mediated path only

Skill package downloads from skills.sh and SkillHub happen ONLY in
the supervisor's install actuator (`internal/skillinstall` or
similar — M7 package). Agent containers do NOT have egress to the
registries. The supervisor downloads, validates, extracts, and
bind-mounts; the agent only sees the post-validation contents
under `/workspace/.claude/skills/<package>/:ro`.

This collapses the registry-fetch attack surface to one place under
Garrison's control. Registry endpoints are pinned in supervisor
config, not user-supplied URLs.

### Rule 7: Every installed skill carries a digest pin

The `agents.skills` JSONB entries record `{registry, package,
version, digest}`. The digest is captured at approval time (the
supervisor fetches once during the approval Server Action's
transaction, computes the SHA-256, and stores both the bytes and
the digest). At install time (which may be later, e.g. on agent
activation post-deactivation cycle), the supervisor re-fetches and
verifies the digest matches; mismatch → install_failed,
operator-visible error.

Digest pins protect against registry tampering between approval and
install.

### Rule 8: Archive extract is path-validated

The supervisor extracts skill archives with a pre-validation pass:
every entry's path must be (a) relative, (b) not contain `..` or
absolute prefixes, (c) not be a symlink whose target points outside
the extract dir. Entries failing validation reject the entire
extract — no partial install — and the failure is logged with the
offending entry path.

Closes path-traversal vector in adversary class #7.

### Rule 9: Audit rows carry the reviewed-snapshot, not a reference

Every `chat_mutation_audit` row for `approve_hire` /
`reject_hire` / `approve_skill_change` / `reject_skill_change`
carries the FULL proposal content as a JSONB snapshot, not just a
foreign key to `hiring_proposals.id`. This way a forensic query
running months later — when the proposal row may have been edited
by Rule 4-permitted status transitions — sees exactly what the
operator saw at decision time.

Mirrors the `vault_access_log` shape from M2.3 (which captured
secret metadata at access time, not as a stale reference).

### Rule 10: agent.md edits use the chat-mutation-audit pipeline

`agent.md` content is operator-editable via a dashboard form (M3 / M4
read+write surfaces). Every edit lands through the same M5.3 verb
audit pipeline as proposals — `update_agent_md` Server Action writes
the new content, the prior content as a snapshot, the chat session
attribution (or `null` if dashboard-direct), and the audit row.

Mirrors Rule 5's posture: the role's prompt is a security-relevant
asset, edits are first-class auditable events.

### Rule 11: Hire-time + approve-time + install-time hashes are all recorded

For every approved skill, the supervisor records:

- `proposal.skill_digest_at_propose` — what the chat verb saw when
  composing the proposal.
- `audit.skill_digest_at_approve` — what was fetched + verified
  during the approval Server Action transaction.
- `agents.skills[i].digest_at_install` — what was actually
  bind-mounted into the container the first time the agent was
  activated.

Three digests in three columns. Forensic queries can detect drift at
any boundary. Trivial cost; large clarity gain.

### Rule 12: The supervisor's docker-socket-proxy allow-list is operator-controlled

Adding a skill that itself ships an MCP server (e.g. a registered
MCP server per M8) requires the operator to extend the docker-
socket-proxy allow-list to permit that server's container shape.
This is an operator-side config change, not a Server Action. No
runtime-installable MCP server can extend the proxy's allow-list
on its own.

This caps how much the M8 MCP-registry work can broaden the
supervisor's authority via the M7 hiring flow.

---

## 5. What registries provide vs. what Garrison builds

### skills.sh (public registry)

**Provides** (consumed by Garrison via HTTPS GET):

- Open uploads under skills.sh's policy.
- Versioned package URLs (one per package + version tuple).
- Package contents (tarball or directory tree, per skills.sh format).
- Optional metadata (description, author, README) for the dashboard
  approval surface.

**Does NOT provide**:

- Cryptographic publisher attestation. A package's bytes are
  whatever the publisher pushed; provenance is whatever the
  registry surface says.
- Mandatory leak-scan or content audit. Every package is opt-in
  publish.
- Stable-publisher account linkage. Account models on public skill
  registries are uneven.
- Strong typo-squatting prevention. Multiple similar names can
  coexist.

### SkillHub (private registry — iflytek-hosted; verdict: SHIP per `m7-spike.md` §2)

**Provides** (consumed by Garrison via API + auth):

- Account-scoped publish set (smaller surface than skills.sh).
- Versioned packages with API-driven install endpoints.
- Optional in-registry metadata.

**Does NOT provide** (verified at spike or open):

- Same provenance limitations as skills.sh. Account compromise =
  malicious-package vector.
- Live behaviour of the install API at scale (still subject to
  operator-side spike on real creds — see `m7-spike.md` §8.5).

### Garrison builds (no registry UI surface exposed)

- **`internal/skillregistry/skillsh.go`** [M7]. Thin HTTPS client for
  the skills.sh feed. Read-only; no Garrison-side publish.
- **`internal/skillregistry/skillhub.go`** [M7]. SkillHub HTTPS+auth
  client. Same shape — read-only consume.
- **`internal/skillinstall/`** [M7]. The install actuator: download,
  digest-validate, archive-extract, bind-mount-prep. Owns Rules 6, 7,
  8.
- **`hiring_proposals` schema extension** [M7]. Adds `skill_digest_
  at_propose`, `proposal_snapshot_jsonb`, status transition columns.
  Mirrors M5.3's `chat_mutation_audit` shape.
- **`approve_hire`, `reject_hire`, `approve_skill_change`,
  `reject_skill_change` Server Actions** [M7]. Owns Rules 2, 3, 5, 9.
- **`update_agent_md` Server Action** [M7]. Owns Rule 10.
- **`bump_skill_version` chat verb + Server Action pair** [M7].
  Re-runs the propose → approve cycle for an existing skill at a
  new version. Owns the operator's escalation path for legitimate
  upstream updates.
- **Dashboard approval surface** [M7]. Sibling-proposal display
  (Rule 4), digest visualisation (Rule 7), reviewed-snapshot record
  (Rule 9). The UX choice between inline-and-dedicated vs purely-
  dedicated approval surfaces lands in the M7 plan.
- **Audit-trail forensic queries** [M7]. Read-side dashboard surface
  for `chat_mutation_audit` rows tagged `approve_hire` /
  `reject_hire` / `approve_skill_change`. Rule 9's snapshot column
  shows what was actually approved at the time.
- **Existing-agent skill migration** [M7]. The M2.x-seeded
  `engineer` and `qa-engineer` rows have empty `skills` arrays.
  M7's ship migrates them through the propose → approve gate (or
  explicitly opts them out of M7's skill management) so no agent row
  pre-dates Rule 5's invariant.

### Deployment shape

- Supervisor reaches skills.sh + SkillHub from `garrison-net` only.
  Agent containers (per `agent-sandbox-threat-model.md` Rule 3) do
  NOT join `garrison-net`; they're on per-agent networks. Registry
  reach is supervisor-only.
- Skill storage at `/var/lib/garrison/skills/<agent-id>/`. Owned by
  the supervisor user; bind-mounted RO into the agent container
  (per `agent-sandbox-threat-model.md` Rule 2).
- Registry endpoints pinned in supervisor config (env vars):
  `GARRISON_SKILLS_SH_URL`, `GARRISON_SKILLHUB_URL`. No runtime
  tampering with which registry the supervisor talks to.

---

## 6. Open questions the M7 context spec must resolve

1. **`hiring_proposals` schema for skill-change proposals.** The M5.3
   table targets `propose_hire` only (new agent). Skill-change
   proposals have a different content shape (existing agent ID +
   diff). M7 spec: extend the existing table with optional fields,
   or split into `agent_proposals` (skill-changes) + retained
   `hiring_proposals` (new agents)?

2. **Default registry per environment.** Should M7 ship with
   skills.sh enabled and SkillHub disabled-by-default (operator
   opts in per env var), or both enabled? The lean per `m7-spike.md`
   §2 is "SkillHub primary, skills.sh fallback"; spec confirms.

3. **bump_skill_version UX.** Re-run the full propose → approve
   cycle for every version bump (verbose, operator fatigue), or a
   lighter "approve this version diff" surface (lighter, but maybe
   too easy to miss a malicious version)? Plan-level UX call.

4. **MCP-server skills.** Some skills carry MCP server definitions
   (the `mcp-builder` skill is the prototypal example). Does M7
   support MCP-server-bearing skills, or defer them to M8 (MCP
   registry)? Lean: M7 supports skill content but defers
   MCP-server-bearing skills to M8.

5. **Per-publisher trust labels.** The dashboard could surface
   "Published by X (verified)" / "Published by Y (community)"
   distinctions if the registry exposes publisher metadata. M7
   plan: include this in the approval UX, or punt to M8 with the
   MCP-registry?

6. **Skill content scanning at approval time.** Should the
   supervisor run a lightweight static scan over the skill content
   before the digest is recorded — looking for obvious patterns
   (`curl http://exfil.evil.com`, `wget`, `nc -e`, etc.)? Or trust
   operator review entirely? Lean: include a coarse scan, surface
   findings in the approval UX, but don't block on findings (they
   inform, the operator decides).

7. **Preamble-vs-skill conflict resolution.** `m7-spike.md` §8.5
   open follow-up: when the immutable preamble says X and an
   installed skill says NOT X, what does Claude do? The M7 plan
   should empirically test this with a real opus / haiku spawn
   before committing the preamble's rule shape.

8. **MCP-server allow-list for hired agents.** The M2.x-seeded
   agents have a fixed MCP set (mempalace, pgmcp, finalize). Hired
   agents may bring their own MCP servers (open question 4). What's
   the allow-list shape — operator approves the MCP set per agent,
   or there's a default per-skill MCP allow that the operator can
   override?

9. **Audit retention policy.** `chat_mutation_audit` rows for
   approve / reject decisions accumulate indefinitely. Does M7
   ship with a retention policy (e.g. "30-day rolling for
   pending/approved, indefinite for rejected") or punt to M9 + a
   compliance milestone?

10. **Agent deactivation propagation.** When an agent is deactivated
    (operator-driven), what happens to its skills? Bind-mount
    detached, skill files deleted, audit row written? Lean:
    deletion is operator-explicit (separate `delete_agent` Server
    Action); deactivation is reversible.

11. **Hiring across customers (multi-tenant).** Today the chat
    session has a `companies.id`; the proposal inherits it. Does an
    operator hiring on behalf of customer A see / approve only
    that customer's hire proposals? Lean: yes; the dashboard
    filter should default to the operator's active customer
    context (M5.x carryover).

12. **Skill SBOM generation.** Each installed skill is a
    dependency. Does M7 ship with an SBOM-style export (every
    installed skill across every agent + their digests + their
    publishers) for security audit? Lean: trivial cost, defer to
    M7.1 polish if not in scope at M7 ship.

---

## 7. What each milestone retro must answer

### What the M7 retro must answer

- **Rule-set hold**: did Rules 1–12 hold across the M7 implementation?
  Any rule needing pre-ship amendment because the spec couldn't
  satisfy it cleanly?
- **Approval cycle UX**: how often did the operator approve in a
  single click vs. expand the proposal for line-by-line review?
  (Per M2.3 retro discipline — surface UX-driven decisions that
  affect security posture.)
- **Digest pin friction**: how often did the install-time digest
  re-fetch fail to match the approve-time digest? Was the
  difference always a registry tampering signal, or sometimes a
  registry-side cache invalidation?
- **Path-traversal rejections**: did Rule 8's archive-extract
  validator catch any actual malicious paths, or only false positives
  from poorly-packed legitimate skills? Calibrate the rule.
- **Existing-agent migration**: did the M2.x-seeded engineer +
  qa-engineer migrate cleanly through the propose → approve gate at
  M7 ship, or was a transitional bypass needed?
- **Preamble-skill conflict resolution**: did open question 6.7
  surface real conflicts? What did Claude actually do?

### What the M8 retro must answer (hiring carry-over)

M8 adds the MCP-server registry. Each registered MCP server is
analogous to a hired skill — same operator-trust class.

- **MCP-server publisher attribution**: does the registry surface
  publisher identity well enough that operator approval can
  distinguish trusted vs. typosquat?
- **MCP-server allow-list propagation**: when an MCP-bearing skill
  is approved at M7 (deferred via question 6.4) or registered at
  M8, does the docker-socket-proxy allow-list (Rule 12) get the
  right amendments?

### What later retros must answer (open-ended)

- Per-customer skill scoping (M9 or beyond) — when one customer's
  skill set must be invisible to another customer's agent.
- Skill SBOM coverage (M7.1 or later) — what fraction of installed
  skills carry full provenance metadata?
- Compromised-publisher post-mortem if any approved skill is later
  determined malicious — the audit trail's snapshot (Rule 9) is the
  load-bearing reconstruction surface.

---

## Cross-references

- `docs/security/vault-threat-model.md` — credential injection;
  hands off to this document at the point of agent activation
  (vault env vars are injected into the container the install
  actuator built).
- `docs/security/chat-threat-model.md` — operator-vs-agent input
  axis; this document layers on top — every `propose_hire` chat
  verb invocation lives inside the chat threat model's scope.
- `docs/security/agent-sandbox-threat-model.md` — agent outbound
  blast radius; this document hands off at agent activation,
  treating any installed-and-running agent as in-scope of the
  sandbox model.
- `docs/issues/agent-workspace-sandboxing.md` — precursor to the
  sandbox threat model.
- `docs/research/m7-spike.md` §2, §3, §4 — registry decisions and
  install-actuator scope.
- `docs/skill-registry-candidates.md` — the SkillHub vs skills.sh
  decision document.
