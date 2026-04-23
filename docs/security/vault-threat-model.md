# Secret vault — threat model and architectural rules

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

**Status**: Design input for M2.3. Not yet specced. This document is binding input to `/speckit.specify` when M2.3 activates.

**Last updated**: 2026-04-22.

**Precedence**: this document lives below `RATIONALE.md` and the active milestone context in the document hierarchy (see `AGENTS.md`). When M2.3 activates, `specs/_context/m2-3-context.md` supersedes this document for operational conflicts; this document supplies the threat model and architectural principles that the context file cannot re-derive cheaply.

---

## Scope of this document

This is a threat model and a set of architectural rules. It is NOT a spec, a plan, or an implementation. When M2.3 activates, the spec-kit flow begins from the context file that will cite this document as binding input.

The document covers:

1. What the vault protects (assets)
2. Who it protects against (adversaries)
3. What threats it addresses and which it explicitly accepts
4. Architectural rules Garrison enforces in the vault integration
5. What Infisical (the chosen backend) provides vs. what Garrison builds
6. Open questions the spec must resolve

---

## 1. Assets

**Secrets Garrison will hold**:

- **Operator-owned operational secrets**: payment processor keys (e.g. Stripe), infrastructure credentials (host provider API, Coolify API, Postgres, object storage), chat and notification webhooks
- **Third-party SaaS API keys**: voice/telephony APIs, LLM-adjacent APIs, transactional email, any tool an agent needs to invoke
- **Customer-delegated OAuth tokens**: Google Workspace, Microsoft, messaging provider tokens that customers grant the operator for specific integrations
- **Agent-specific credentials** (future): if a hired agent needs its own third-party tool access, each hired agent's credentials are a new asset class

**Multi-tenancy posture**: single-tenant at M2.3 ship. Customer-owned credentials today belong operationally to the operator, but the data model anticipates per-customer isolation as a future state. The `customer_id` column exists from M2.3 with a single value (the operating entity's identifier); additive multi-tenancy later is a data migration, not a rewrite.

---

## 2. Adversaries

Ranked by realistic probability of affecting the deployed Garrison instance operationally. This ordering matters: design effort should correspond to realistic threats, not theoretical ones.

1. **The operator, making mistakes.** Pasting a secret in a chat message, committing a `.env`, granting an agent role access it shouldn't have, deleting a secret that's still in use. Solo-operator scale means the operator has the most access and therefore the most opportunities for mistakes. Treated as the top adversary because it is.
2. **Prompt injection via ticket content or MCP tool responses.** A customer-submitted document, a scraped webpage, or a malformed webhook payload contains instructions that trick an agent into retrieving a secret it was granted and emitting it somewhere visible (ticket metadata, diary entry, git commit, tool_result response).
3. **Claude Code session leakage into downstream stores.** Secrets entering the model's context window, then being written verbatim to `slog`, to an observability platform, to MemPalace diary entries, or to generated code that gets committed. This is not an "attack" — it's the system working as designed with a secret in the wrong place. Subtler than the first two and harder to detect.

**Adversaries we explicitly deprioritize**:

- Host-level attackers with shell access to the machine running Garrison. If they have root, they can read the supervisor's env vars at spawn time, dump Postgres, query Infisical's database, and grep for secrets. Application-layer vault design does not defend against this. Mitigation is systems-level (SSH hardening, network configuration, firewall, audit logging at the host layer) and belongs to a different milestone.
- Nation-state-level adversaries. Wrong threat model for an indie self-hosted deployment.
- Customer-against-customer attacks. Single-tenant at M2.3; the data model future-proofs but the defense doesn't exist yet.

---

## 3. Threats addressed vs. accepted

### Threats the vault explicitly addresses

1. **Secrets appearing in agent prompts or context windows.** Mitigated by environment-variable injection at spawn time; secrets never enter the `--system-prompt` or `-p` arguments.
2. **Secrets appearing in logs, observability platforms, MemPalace, or any other downstream store.** Mitigated by discipline in Garrison's code: the supervisor's stream-json event router never logs secret values, the vault client never logs secret values, agent outputs are scanned for suspected-secret patterns before being written to MemPalace (heuristic-based, best-effort, not perfect — but catches the common cases).
3. **Agents retrieving secrets they weren't granted.** Mitigated by the supervisor enforcing the agent-role-to-secret mapping at spawn time; the agent cannot ask for secrets outside its grant because it doesn't know the vault exists (secrets are env vars, not an MCP tool).
4. **Operator granting broader access than intended.** Mitigated by Garrison's UI surfacing "which agent roles can access this secret" when the secret is created or edited, and "which secrets can this role access" when the role is configured. Side-by-side visibility prevents blind grants.
5. **Secrets persisting after they should be rotated.** Mitigated by Infisical's rotation features + Garrison surfacing stale secrets in the dashboard (weekly-review cadence, consistent with the hygiene dashboard pattern from RATIONALE §5).

### Threats explicitly accepted

1. **Host compromise.** If the host machine running Garrison is rooted, every secret is compromised. No application-layer design defends against this.
2. **Intentional operator leakage.** If the operator pastes a secret into a public chat or commit, the vault cannot save them.
3. **Claude Code supply-chain compromise.** If the `claude` binary is backdoored, the system is compromised regardless of vault design. This is a concern about Anthropic and the Claude Code distribution channel, not about Garrison's vault.
4. **Infisical supply-chain compromise.** If Infisical itself is backdoored, secrets are compromised at the backend layer. Garrison pins Infisical versions and reviews major-version upgrades; acute attacks against Infisical are out of Garrison's reach.
5. **Model leakage into training.** The Claude provider's policy determines whether conversations become training data. Secrets never enter conversations by design (rule 1), so this threat is eliminated if rule 1 holds; residual risk is small but acknowledged.

---

## 4. Architectural rules (binding for M2.3 and beyond)

These rules are binding. The M2.3 spec may not contradict them. If a future retro surfaces a reason to revisit a rule, the rule is amended here before the spec changes.

### Rule 1: Secrets never enter agent prompts or context windows

The supervisor reads secrets from Infisical at spawn time and injects them as environment variables into the Claude Code subprocess. The agent's code (including any bash tool calls, scripts, or MCP servers that Claude spawns) sees `process.env.STRIPE_KEY` or equivalent; the LLM's context window never contains the raw secret value.

**Consequence**: `agent.md` files cannot reference secret values directly. They reference environment variable names. "Use `$STRIPE_KEY` to create the payment intent" is allowed; "the Stripe key is `sk_live_...`" is not, because the latter would put the secret in the system prompt.

**Consequence**: the supervisor validates, before spawn, that every secret about to be injected is referenced only by name in the agent.md. A secret whose value string appears in the agent.md is a bug and blocks the spawn.

### Rule 2: Per-role scoping enforced at the supervisor

The supervisor decides, based on the agent role spawning, which secrets to inject. The vault (Infisical) stores and retrieves by name; it does not know which Garrison role gets what. The policy lives in Postgres as a row in the `agent_role_secrets` table (or similar) — one row per (role, secret-path) grant.

**Consequence**: the vault has no policy engine of its own from Garrison's perspective. Infisical's own access controls serve defense-in-depth, but the authoritative policy is Garrison's row-level grants.

**Consequence**: a new agent role with no explicit grants gets zero secrets. No defaults, no inheritance, no wildcards.

### Rule 3: The vault is opaque to agents

Secrets are injected as environment variables. The agent does not call a vault MCP tool. There is no `vault_get(name)` available to the agent at runtime.

This rule rejects the MCP-tool-vault pattern explicitly. It eliminates the prompt-injection path where an attacker manipulates the agent into calling `vault_get('customer_stripe_key')` and emitting the result. An agent that has never been told a vault exists cannot be manipulated into using one.

**Consequence**: secrets the agent needs must be known at spawn time. Dynamic retrieval of "I need a secret I didn't know about when I started" is not supported. If an agent discovers it needs a credential mid-turn, the supervisor spawns a new agent with the expanded grant, or the ticket is rejected with `needs_review`.

### Rule 4: Secrets tagged by provenance

Every secret stored in Infisical carries metadata:
- `customer_id` (currently always the operating entity's identifier, future multi-tenancy)
- `provenance` (one of `operator_entered`, `oauth_flow`, `environment_bootstrap`, `customer_delegated`)
- `created_at`, `last_rotated_at`, `last_accessed_at`
- `allowed_role_slugs` (denormalized from the `agent_role_secrets` table for quick queries)

Provenance is tagged via Infisical's metadata fields or path-prefix conventions (`/<tenant>/operator/...`, `/<tenant>/oauth/...`). The M2.3 spec chooses one convention and documents it.

**Consequence**: when a customer revokes their delegation (real multi-tenancy arrives), Garrison can query `provenance = 'customer_delegated' AND customer_id = ?` and purge the relevant secrets without trawling manually.

### Rule 5: Infisical handles storage, encryption, audit

Infisical provides: AES-256-GCM encryption at rest, end-to-end encryption in transit, audit log of every access and change, rotation primitives for databases and cloud IAM, machine identities for service authentication.

Garrison does NOT implement its own encryption, its own audit log, or its own rotation scheduling. These are Infisical's responsibilities; duplicating them in Garrison would be both more code and less secure.

**Consequence**: Garrison's uptime depends on Infisical's uptime. Both run on the same orchestration layer; the failure domain is already shared. If Infisical is down, the supervisor cannot spawn any agent that requires secrets — the spawn fails with `exit_reason = "vault_unavailable"`.

### Rule 6: Audit everything, log no values

Every vault access from Garrison writes an audit record to both Infisical's native audit log AND to Garrison's own `vault_access_log` table. The dual logging exists because Infisical's audit log is authoritative for "did access happen" but Garrison needs context-joined queries ("show me which tickets caused which secret accesses") that require secrets-log-joined-to-tickets.

Audit records contain: `agent_instance_id`, `secret_path`, `outcome` (granted/denied/error), `timestamp`. They do NOT contain secret values. Enforced in the vault client's Go code — the logger type has no method that accepts a value. Log discipline is a compiler-enforced property, not a convention.

**Consequence**: every path that could log a secret value is audited in code review. The M2.3 spec specifies a lint rule or test that fails if any `slog.*` call in the vault-handling code passes a value derived from an Infisical response.

### Rule 7: Rotation is a first-class concern

Every secret in the vault has a `rotation_cadence` (30 days, 90 days, never) and a `last_rotated_at`. The Garrison dashboard surfaces secrets approaching or past rotation deadline. Operator rotates via the Garrison UI, which calls Infisical's rotation API (for supported secret types) or prompts the operator to paste a new value (for unsupported types).

Customer OAuth tokens have their own rotation model driven by the OAuth provider's refresh flow. The vault stores both access token and refresh token; the supervisor uses the refresh token to obtain a new access token when the current one is close to expiry. This is rotation for OAuth and is handled distinctly from rotation for static API keys.

**Consequence**: M2.3's UI work includes the rotation view, not just CRUD. Rotation is not deferrable to M4 or later — rule 7 is part of what the vault actually is, not an optional feature.

---

## 5. What Infisical provides vs. what Garrison builds

Garrison commits to option A: Garrison owns every operator-facing UI; Infisical is an internal backend the operator never interacts with directly. This has non-trivial implementation cost but was chosen deliberately; see the architecture discussion log and M2.3 retro for the reasoning.

### Infisical provides (Garrison consumes via API + Go SDK)

- Encrypted storage of secret values
- AES-256-GCM encryption at rest, end-to-end encryption in transit
- Audit log of every access and change (API-readable)
- Secret versioning and point-in-time recovery
- Secret rotation primitives for supported backends (Postgres, MySQL, AWS IAM, etc.)
- Machine identities for authentication (Garrison's supervisor is one)
- Project and environment organization
- Path-based secret organization with wildcards
- API rate limiting and abuse protection at the vault layer

### Garrison builds (no Infisical UI surface exposed)

- Secret CRUD screens (create with name/value/environment/path, edit with diff view, delete with "which agents reference this?" warning)
- Environment and path tree management (create, rename, move)
- Audit log viewer with filters, pagination, time-range queries, per-identity filtering
- Rotation configuration UI (which secrets rotate, schedule, integration-specific fields)
- Access control UI (the agent-role-to-secret mapping screen, which is Garrison-native and not expressible in Infisical's UI)
- Secret usage view ("this secret is used by agent_roles X, Y, Z; last accessed by ticket #123 at 14:32")
- Error handling for every Infisical API failure mode (rate limits, auth token expiry, permission-denied, network errors) with operator-facing messages
- Supervisor-side vault client (Go package) that authenticates to Infisical, fetches secrets per spawn, injects as env vars, audits access in both logs
- Spawn-time validation that secret values don't appear in agent.md files (enforcement of rule 1)
- The `agent_role_secrets` table and the per-role-grant enforcement logic (enforcement of rule 2)

Realistic estimate for Garrison-side UI work: two to four weeks of solo frontend work. Accepted as part of the M2.3 budget.

### Deployment shape

- Infisical runs as a service on the same orchestration layer as the supervisor and dashboard
- Infisical's PostgreSQL + Redis are separate from Garrison's Postgres (Infisical's own DB)
- Garrison's supervisor authenticates as a Machine Identity with Universal Auth
- Garrison's dashboard authenticates as a separate Machine Identity with scoped permissions
- Infisical's web UI is NOT exposed publicly; bound to localhost or a private network within the orchestration layer
- All operator access to secrets goes through Garrison's dashboard, which proxies to Infisical's API

---

## 6. Open questions the M2.3 spec must resolve

Questions that depend on concrete implementation context and should not be pre-decided here:

1. **Environment model**: does Garrison use Infisical's environments (dev/staging/prod) for its own environment split, or use a single environment and rely on Garrison's own department/role model? The default lean is single Infisical environment, with path conventions carrying Garrison's semantics.

2. **Machine identity scope**: one machine identity per supervisor instance, or one per agent_instance spawn? Per-spawn is more secure but has higher Infisical API load. The spec decides after measuring supervisor spawn rate.

3. **Secret value injection mechanism**: env vars only, or env vars + a mounted `/secrets/` directory for multi-line secrets (PEM keys, certificates)? Env vars have size limits; files do not. The default is env vars, with the directory option reserved for specific secret types.

4. **Audit log retention**: Infisical's community edition has a retention limit on audit logs; Garrison may need to export logs to its own Postgres for longer retention. Spec decides based on operational requirements.

5. **Secret discovery at spawn time**: how does the supervisor know which secrets to inject for a given agent role? Options: SQL join on `agent_role_secrets` at spawn time; pre-computed denormalized column on `agents`; Infisical's own access policy. Default lean: SQL join on `agent_role_secrets`, which keeps the policy in Postgres where agent config already lives.

6. **Rotation UX**: is rotation initiated from the secret view or from a dedicated rotation dashboard? Default is both surfaces, which is more UI work but matches how operators actually think about rotation.

7. **Bootstrap secret**: Infisical itself requires secrets to operate (`ENCRYPTION_KEY`, `AUTH_SECRET`). These cannot live in Infisical. The spec specifies where these bootstrap secrets live — environment variables set by the orchestration layer, read from a restricted file, or passed in at startup. This is the chicken-and-egg problem every vault solution has; document how we solved it.

8. **Secret-pattern scanning of agent outputs**: the rule about scanning for suspected-secret patterns in Claude's outputs before writing to MemPalace — what patterns, what action on detection? Default: regex for common formats (sk-, xoxb-, AKIA, PEM headers), redact and log a hygiene warning but don't block. Spec commits to the patterns and the action.

---

## 7. What the M2.3 retro must answer

When M2.3 ships, the retro documents:

1. **Did rule 1 hold?** Any case of a secret appearing in an agent prompt, context window, log, or MemPalace write. Even one case is a material finding; the architecture may need revision.
2. **Did the option-A UI actually feel coherent?** The operator uses it for a week; the retro records whether the integrated UI paid off its cost, or whether migrating to option B (Infisical UI + Garrison assignment screen) is warranted. This is the natural checkpoint to revisit the architectural decision.
3. **How did Infisical perform under real load?** Specifically: API latency for spawn-time secret fetches, audit log write rate, any Infisical-side failures the supervisor had to handle gracefully.
4. **Rotation discipline**: did the operator actually rotate secrets on the surfaced schedule, or did rotation warnings accumulate? If ignored, the rotation UX needs redesign.
5. **Audit log usefulness**: did the Garrison-side audit log (with ticket context) prove more useful than Infisical's native log, or was the dual-log overhead not worth it?
6. **Any unauthorized-access attempts**: if the supervisor's per-role grant enforcement ever denied a secret request (which should theoretically never happen if the system is configured correctly), document why and whether it indicates a bug.
---

## M2.2 deployment assumptions (socket-proxy)

Added during M2.2's T002 per T001 finding F5 and plan §"Deployment topology". This section governs only the M2.2 supervisor ↔ MemPalace path; the M2.3 vault design incorporates these assumptions but does not supersede them.

**Topology.** Four containers on a single Coolify-project Docker network: `garrison-supervisor`, `garrison-mempalace`, `garrison-docker-proxy` (`ghcr.io/linuxserver/socket-proxy`, pinned by digest in production), and `garrison-postgres`. The supervisor's Claude-subprocess MCP calls into MemPalace all flow through `docker exec` against the sidecar, with the docker client pointed at `tcp://garrison-docker-proxy:2375` via `DOCKER_HOST`. The proxy is an HAProxy listening on TCP :2375 inside its container and is **not published to the host** — only reachable over the compose network.

**Filter.** Proxy env: `POST=1 EXEC=1 CONTAINERS=1`; every other linuxserver/socket-proxy flag defaults to 0 (deny). Explicitly denied endpoints: IMAGES (no pull/tag), VOLUMES (no create/remove/mount manipulation), NETWORKS (no network attach/detach), BUILD (no image builds), SWARM (no swarm ops), CONFIGS (no swarm config access), CREATE (no new containers), DELETE (no container removal), RESTART/START/STOP (no lifecycle control on containers the supervisor didn't exec into), COMMIT (no container-to-image snapshots), IMAGES DISTRIBUTION (no registry interaction), DATABASE AUTH (no plugin auth), EVENTS (no event firehose), and everything else. Verified end-to-end in T001 — EXEC allowed against `garrison-mempalace`; VOLUMES and IMAGES both return HTTP 403.

**Residual risk the proxy does NOT eliminate.** A compromised supervisor process (RCE, credential theft, whatever) can `docker exec` into **any container on the same Docker engine that it can name**. For single-project Coolify deployment (Garrison's primary model), that's garrison-mempalace, garrison-docker-proxy, and garrison-postgres. Worst case: exec into garrison-postgres and dump the DB — but a compromised supervisor already has `GARRISON_DATABASE_URL` in env and can achieve the same damage via SQL directly; the proxy does not widen the existing blast radius for the single-project model. **For shared-engine deployment** (multiple Coolify projects sharing one Docker engine), the envelope widens to every other project's containers — operator concern, not solvable at the proxy layer. Mitigation: Garrison deploys one-project-per-engine, or the operator accepts the wider envelope.

**Explicitly out of scope for M2.2:** preventing supervisor compromise (M2.3 vault's job), per-container exec allowlisting (requires a different proxy; tecnativa/docker-socket-proxy's filters are similarly endpoint-level, not container-level; a label-filtering fork would be needed for true per-target isolation), preventing the supervisor from enumerating containers via `CONTAINERS=1` for the inspect preflight (required for `docker exec` to resolve names). These constraints inform the M2.3 threat model but are not M2.2 changes.

**Pointers:**
- Plan: [`specs/004-m2-2-mempalace/plan.md`](../../specs/004-m2-2-mempalace/plan.md) §"Deployment topology"
- Spike-verified claims: [`specs/004-m2-2-mempalace/research.md`](../../specs/004-m2-2-mempalace/research.md) §"Proxy filter — verified end-to-end"
- Task: [`specs/004-m2-2-mempalace/tasks.md`](../../specs/004-m2-2-mempalace/tasks.md) T002 (this section's origin)
