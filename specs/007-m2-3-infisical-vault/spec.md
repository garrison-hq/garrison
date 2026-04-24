# Feature specification: M2.3 — Infisical secret vault (supervisor + data model)

**Feature branch**: `007-m2-3-infisical-vault`
**Created**: 2026-04-24
**Status**: Draft
**Input**: `/garrison-specify m2.3` — "Wire Infisical into supervisor-side agent spawn so agents can receive credentials they need for real work, without secrets ever entering the LLM context window. Data model prepares for M3 read surfaces and M4 write surfaces; operator-facing UI is not in scope."

This spec is bound by [`specs/_context/m2.3-context.md`](../_context/m2.3-context.md) and by the threat model at [`docs/security/vault-threat-model.md`](../../docs/security/vault-threat-model.md). Every decision settled in the context file — the M2.3/M3/M4 scope banding, the supervisor-side responsibilities enumerated in "In scope for M2.3 > Supervisor-side vault client", the three-table data model in "In scope for M2.3 > Data model", the three spawn-time enforcement checks in "In scope for M2.3 > Spawn-time enforcement", the deployment shape in "In scope for M2.3 > Infisical deployment", and the secret-pattern scan list in "In scope for M2.3 > Secret-pattern scanning of agent outputs" — is input, not question. Threat-model Rules 1-7 are binding as stated in §4 of the threat model; this spec may not contradict them. Where this spec cites a constraint, the authoritative text lives in the context or threat model; consult the source rather than the paraphrase here.

M2.3 is plumbing, not a product surface. Per "What this milestone is NOT" in the context, no operator sees M2.3 directly: browsing, CRUD, audit-log viewing, rotation UI, and grant editing are all deferred to M3 (read) and M4 (write). M2.3 produces the supervisor-side vault client, the three Postgres tables, the spawn-time enforcement checks, the Infisical service in Coolify, and the secret-pattern scan on agent output. When this milestone ships, no dashboard feature lights up; what changes is that agents can receive credentials at spawn time, and the data is ready for M3's read surfaces to consume.

## Clarifications

### Session 2026-04-24

The following binding questions from `specs/_context/m2.3-context.md` §"Open questions the spec must resolve" are answered in this session. Each answer uses the context's stated default unless noted otherwise; deviations are justified in one or two sentences.

- **Q1 — Environment model**. → **Single Infisical environment**. Path conventions (`/<customer_id>/<provenance>/<name>`) carry Garrison's semantics per Rule 4. Context default.

- **Q2 — Machine identity scope**. → **One identity per supervisor instance** (two total counting dashboard at M3). Rationale: post-M2.2.x spawn rate is low (single-digit spawns per ticket under realistic load, per M2.2.x retro §7). Per-spawn identity would multiply Infisical API calls and operational complexity without meaningfully narrowing the blast radius of a supervisor compromise — which is already covered by the threat model's accepted threats (§3 item 2, §4 Rule 5 consequence). Context default.

- **Q3 — Secret value injection mechanism**. → **Environment variables only at M2.3**. The `/secrets/` mounted-directory option for multi-line secrets (PEM keys, certificates) is reserved for a follow-up milestone if a PEM-sized secret surfaces in scope. Context default.

- **Q4 — Audit log retention**. → **`vault_access_log` retained indefinitely in Postgres** (no cap applied at M2.3; storage cost accepted). Infisical's native audit log retained at community-edition default. **No active export from Infisical to Postgres at M2.3**; if longer retention of the Infisical-side log becomes operationally required, the export job is a follow-up.

- **Q5 — Secret discovery at spawn time**. → **SQL join on `agent_role_secrets`**. Keeps grants in Postgres alongside agent config, consistent with Rule 2's "authoritative policy is Garrison's row-level grants." Context default.

- **Q6 — Bootstrap secret delivery**. → **Coolify environment variables at deploy time**. `INFISICAL_ENCRYPTION_KEY`, `INFISICAL_AUTH_SECRET`, and the supervisor's `INFISICAL_MACHINE_CLIENT_ID` / `INFISICAL_MACHINE_CLIENT_SECRET` are set in the Coolify project's environment configuration and surfaced to containers at startup. Operator-side discipline (store in a password manager, never in a `.env` committed to git) documented in the ops checklist. Context default.

- **Q7 — Secret-pattern scanning patterns and action**. → **Fixed regex list, redact-and-warn, do NOT block**. Patterns per the context list (`sk-` prefix, `xoxb-`, `AKIA`, `-----BEGIN ` PEM header, `ghp_` / `gho_` / `ghu_` / `ghs_` / `ghr_` GitHub token prefixes, obvious `Authorization: Bearer ` contexts). On match, the matched substring is replaced with `[REDACTED:<pattern>]` in the MemPalace write and a hygiene warning is recorded on the ticket (new `hygiene_status` value `suspected_secret_emitted`). The MemPalace write proceeds with the redacted value — blocking the write entirely is overcorrection for a best-effort heuristic that has known false negatives. Context default.

- **Q8 — Infisical version pinning policy**. → **Pin to specific digest in production**. Matches the M2.2 socket-proxy digest-pinning pattern (see threat model §"M2.2 deployment assumptions (socket-proxy)"). Minor-version upgrades reviewed before the digest is bumped, per threat model §3 accepted-risk 4 (Infisical supply-chain compromise).

- **Q9 — Dual-logging failure mode**. → **Fail closed**. If the Infisical fetch succeeds but the `vault_access_log` write to Garrison's Postgres fails, the supervisor discards the fetched value in memory, does not inject it into the subprocess environment, and fails the spawn with `exit_reason = "vault_audit_failed"`. Rationale: Rule 6 pairs audit with access ("audit everything, log no values"). A secret fetched but not recorded in Garrison's own log violates the rule's semantic — the supervisor cannot later answer "did this ticket cause this access?" for that event. Fail-open-with-alert was considered and rejected because the alert surface (M3 hygiene dashboard) does not exist yet at M2.3; the acute failure mode is preferred to a silent diffuse one.

- **Q10 — Per-customer secret namespacing at the Infisical path level**. → **Yes, `/<customer_id>/` path prefix**. Even though M2.3 is single-tenant with `customer_id` always equal to the operating entity's identifier, the path convention is established now so future multi-tenancy migration is additive rather than a rewrite. Context default.

- **Q11 — Test fixtures for Infisical**. → **Testcontainer Infisical for integration tests**. Consistent with the `testcontainers-go` pattern established in M1 (Postgres) and reused in M2.1 (pgmcp) and M2.2 (MemPalace). A separate CI-only Infisical instance was considered and rejected (adds external dependency, drift between CI and integration test environments); mocking the Infisical SDK was rejected per the M2.2.x retro's documented mock-drift pitfalls.

- **Q12 — Migration path for `agent_role_secrets`**. → **Seed migration creates the three tables with zero grant rows** for the engineer and qa-engineer roles (neither requires secrets today). Convention for future grants: one migration file per logical grant set, with the migration header naming the role, the secret paths, and the ticket that approved the grant. This mirrors the "one migration per new role" pattern from M2.1's `garrison_agent_ro` grants and M2.2's `garrison_agent_mempalace` grants.

- Q: `agent.md` secret-value scan cadence — per-spawn or cached? → A: **Per-spawn.** The supervisor resolves `agent.md` content and the fetched secret values on every spawn and runs the substring scan fresh. Rationale: Rule 1 is binding; cache invalidation on out-of-band `agent_role_secrets` changes would introduce a new correctness burden for a check that must never miss. Scan cost is trivial at M2.3 scale (substring-search over KB-sized agent.md × single-digit secret count).
- Q: Attachment point of the agent-output secret-pattern scan? → A: **`finalize_ticket` payload only.** The scan applies to the string-typed fields the supervisor writes to MemPalace from the tool payload (`diary_entry.rationale`, `kg_triples[].subject`, `kg_triples[].predicate`, `kg_triples[].object`) at the finalize handler layer. Rationale: per M2.2.x retro observations, the finalize path is ~100% of actual MemPalace writes at M2.3 (agents rarely call `mempalace_*` MCP tools directly). Threat model §3 addressed-threat 2 says "before being written to MemPalace" — that boundary is the supervisor's handler, not the stream-json pipeline. Broader coverage (stream-json tool_use interception, supervisor-wide slog scanning) is deferred until the hygiene dashboard surfaces a regression.
- Q: Rotation of a secret held by a running agent_instance? → A: **No runtime action.** Running agents finish with their stale env-var value; the next spawn of that role picks up the rotated value naturally. Rationale: aligns with RATIONALE §2 (ephemeral agents) and M2.3's plumbing-only scope. Kill-and-respawn-on-rotation would require Infisical rotation-event watching and subprocess-lifecycle interference that M2.3 scopes out; mid-turn termination could corrupt in-flight ticket state. If rotation-during-run becomes a real operational concern after M4's rotation UI ships, revisit then.
- Q: `secret_metadata.allowed_role_slugs` sync discipline? → A: **Postgres trigger on `agent_role_secrets`.** An INSERT/UPDATE/DELETE trigger rebuilds `secret_metadata.allowed_role_slugs` for the affected `(secret_path, customer_id)` inside the same transaction. Rationale: one authoritative write path regardless of which client touches the grants table (M2.3 seed migration, M4 dashboard, future CLIs, ad-hoc SQL). Application-level maintenance was considered but is fragile as write paths multiply. Introducing Garrison's first Postgres trigger is proportionate here — denormalization sync is the textbook case for triggers.
- Q: Mid-run token expiry semantics? → A: **Running agents are untouched** — they rely on env vars injected at spawn, not on the supervisor's Infisical token afterwards. The supervisor performs **at most one automatic re-authentication per spawn** if its ML token has expired between the last auth and a new spawn's fetch; if the re-auth itself fails, the spawn surfaces `vault_auth_expired`. `vault_auth_expired` never applies to a currently-running `agent_instance`.

## User scenarios and testing (mandatory)

### User story 1 — supervisor spawns an agent with a single Infisical-sourced env var and the agent uses it in a tool call (priority: P1)

The operator seeds a grant row: the `engineer` role receives the secret at path `/<customer_id>/operator/example_api_key` as the env var `EXAMPLE_API_KEY`. The operator inserts an engineering ticket that requires the agent to invoke a tool reading `$EXAMPLE_API_KEY`. The supervisor spawns the engineer. Before spawn, the supervisor executes the three spawn-time checks (Rule 1 leak scan, Rule 2 grant query, Rule 3 no-vault-MCP check), then fetches the secret from Infisical via the Machine Identity, writes a `granted` row to `vault_access_log` inside the same transaction as the `agent_instances` insert, and invokes `claude` with `EXAMPLE_API_KEY=<value>` added to the subprocess environment. The agent uses `$EXAMPLE_API_KEY` in its work; the value never appears in the `claude` argv, system prompt, agent.md content, stream-json events, or any `slog` line. The ticket completes cleanly.

**Why this priority**: this is the smallest slice that proves vault plumbing works end-to-end. If this story ships, every other M2.3 item (rule-enforcement blocks, failure-mode routing, dual-audit correctness, pattern scanning) is a refinement on top of a working spawn-with-secret path. The context's "Acceptance criteria framing" item 1 is the direct codification.

**Independent test**: a testcontainer-based integration test (`TestVaultSpawnWithSingleSecret`) that boots Postgres + MemPalace sidecar + Infisical testcontainer, seeds one secret in Infisical at a known path, seeds one `agent_role_secrets` row, inserts a ticket, and observes: (a) `agent_instances.status='succeeded'` with `hygiene_status='clean'` on the transition; (b) one `vault_access_log` row with `outcome='granted'` joined to the `agent_instance_id` and `ticket_id`; (c) no secret value in any `slog` output (grep-assert against captured supervisor logs); (d) no secret value in `claude`'s argv (inspected via the mockclaude fixture's recorded invocation); (e) the agent's tool call succeeded using the injected env var.

**Acceptance scenarios**:

1. **Given** a single grant row (`agent_role_secrets`) for role `engineer` with `secret_path='/<customer_id>/operator/example_api_key'` and `env_var_name='EXAMPLE_API_KEY'`, and Infisical holding a value at that path, **When** the supervisor spawns an engineer agent for a ticket at `in_dev`, **Then** the subprocess's environment contains `EXAMPLE_API_KEY=<fetched value>` and does not contain any other vault-sourced env var.
2. **Given** the secret is fetched and injected, **When** the supervisor captures the `claude` invocation argv + `--system-prompt` + `--mcp-config`, **Then** the fetched value appears in none of them (argv grep-clean).
3. **Given** the agent runs to completion, **When** the operator queries `vault_access_log`, **Then** exactly one row exists for this spawn with `outcome='granted'`, `agent_instance_id` joined to the spawn's row, `ticket_id` joined to the ticket, `secret_path` matching the grant, and the row does not contain the secret value in any column (schema-level compile-time assertion plus grep-clean test).
4. **Given** the spawn succeeded, **When** the operator queries Infisical's native audit log via its API, **Then** an access record exists for the supervisor's Machine Identity reading the same path (Rule 6 dual-logging verified end-to-end).

---

### User story 2 — Rule 1 enforcement: an agent.md containing a literal secret value blocks spawn (priority: P1)

The operator saves an agent.md that accidentally contains the literal string value of a secret (e.g., a Stripe key pasted into the prompt during draft). The spawn must not proceed — the context window would receive the value. Before any Infisical fetch or subprocess invocation, the supervisor performs the Rule 1 leak scan: resolve the agent's `agent.md` content, fetch the set of secret *values* about to be injected (per the role's `agent_role_secrets` grants), and substring-scan each value against the agent.md content. If any secret value appears verbatim in agent.md, the spawn blocks with `exit_reason = "secret_leaked_in_agent_md"`; no subprocess is spawned, no partial `agent_instances` row records a successful start, and the ticket remains at its prior column awaiting operator intervention.

**Why this priority**: Rule 1 is the first rule in the threat model — secrets never enter agent prompts or context windows. Without this enforcement, the entire threat model collapses. The context calls this "compiler-enforced, not convention" (the no-log property) and "validated before spawn" (the leak scan). P1 because the rule is load-bearing for every other M2.3 feature.

**Independent test**: `TestVaultRule1BlocksSpawnOnLeakedValue` — seed a secret value `"sk-test-abc123"` in Infisical; seed a grant for the engineer role; modify the seed `agent.md` to contain the literal `sk-test-abc123` somewhere in its body; insert a ticket; observe `agent_instances.status='failed'`, `exit_reason='secret_leaked_in_agent_md'`, no `claude` subprocess spawned, no `vault_access_log` row for this spawn.

**Acceptance scenarios**:

1. **Given** an agent.md whose resolved content contains the literal string value of any secret in the role's grant set, **When** the supervisor reaches the spawn path, **Then** the Rule 1 leak scan fails, the spawn is aborted, an `agent_instances` row is written with `status='failed'` and `exit_reason='secret_leaked_in_agent_md'`, no subprocess is spawned, and no `vault_access_log` row records a fetch for this abort.
2. **Given** an agent.md that references a secret by env var name (`$EXAMPLE_API_KEY`) but never by literal value, **When** the supervisor reaches the spawn path, **Then** the Rule 1 leak scan passes and the spawn proceeds.
3. **Given** a role with zero grant rows (and therefore zero secrets about to be injected), **When** the supervisor reaches the spawn path, **Then** the Rule 1 leak scan is a no-op (nothing to substring-search) and the spawn proceeds without an Infisical fetch at all.

---

### User story 3 — Rule 2 enforcement: an agent without a grant receives no secrets (priority: P1)

The operator spawns an agent with a role that has no rows in `agent_role_secrets`. The supervisor issues the per-role SQL query (`SELECT env_var_name, secret_path FROM agent_role_secrets WHERE role_slug = ? AND customer_id = ?`), gets zero rows, skips the Infisical fetch entirely, and spawns the subprocess with a base environment that contains no vault-sourced variables. An agent without a grant does not and cannot receive secrets — not by default, not by inheritance, not by wildcard (threat model Rule 2 consequence).

**Why this priority**: Rule 2 is the per-role scoping guarantee. Without it, agents could receive secrets intended for other roles. The context calls the zero-grants case out explicitly: "Agents with no grants get no secrets." P1 because the rule gates the blast radius of every future grant.

**Independent test**: `TestVaultRule2ZeroGrantsZeroSecrets` — for a role with no `agent_role_secrets` rows, spawn an agent and observe that (a) no Infisical API call is made during spawn (testcontainer request-log empty for the spawn window), (b) the subprocess's environment contains no variable matching any known secret env var name, (c) no `vault_access_log` row is written for the spawn (nothing happened to log), (d) the spawn otherwise proceeds identically to an M2.2-era spawn.

**Acceptance scenarios**:

1. **Given** a role with zero rows in `agent_role_secrets` for the current `customer_id`, **When** the supervisor reaches the spawn path, **Then** the Rule 2 grant query returns zero rows and no Infisical API call is made for this spawn.
2. **Given** a role with one row in `agent_role_secrets`, **When** the supervisor reaches the spawn path, **Then** exactly that one secret is fetched from Infisical; no other secrets are fetched even if other paths exist in the vault.
3. **Given** a role with multiple rows, **When** the supervisor reaches the spawn path, **Then** exactly the granted set is fetched; the injection is deterministic across spawns of the same role.

---

### User story 4 — Rule 3 enforcement: the agent's MCP config never exposes a vault tool (priority: P1)

The supervisor generates the per-invocation MCP config for each spawn. The config contains the M2.1 `postgres` server, the M2.2 `mempalace` server, the M2.2.1 `finalize` server, plus any agent-specific MCP servers declared in `agents.mcp_config`. The supervisor asserts, as a spawn-time check, that no entry in the final merged config registers a vault-related server (no `vault_get`, no `secrets_list`, no server whose name contains "vault" or "secret" or "infisical"). If such an entry is present, the spawn blocks with `exit_reason = "vault_mcp_in_config"`. This prevents a future regression where someone adds vault-as-MCP and silently breaks the threat model.

**Why this priority**: Rule 3 makes the vault opaque to agents — the foundation of prompt-injection resistance (threat model §2 adversary 2). If an MCP vault tool ever appears in a spawn's config, the rule is already broken even if the tool isn't called. P1 because the guard must exist before any future code reviewer can accidentally add such a server.

**Independent test**: `TestVaultRule3BlocksSpawnOnVaultMcp` — inject a mock MCP server entry into `agents.mcp_config` whose name matches one of the banned patterns; attempt a spawn; observe `agent_instances.status='failed'`, `exit_reason='vault_mcp_in_config'`, no subprocess, no Infisical fetch.

**Acceptance scenarios**:

1. **Given** `agents.mcp_config` contains a JSON object whose key or `name` field matches the banned vault patterns (`vault`, `secret`, `infisical`, case-insensitive), **When** the supervisor reaches the spawn path, **Then** the Rule 3 check fails and the spawn is aborted with `exit_reason='vault_mcp_in_config'`.
2. **Given** `agents.mcp_config` contains only non-vault MCP servers, **When** the supervisor reaches the spawn path, **Then** the Rule 3 check passes and the merged MCP config is written per the M2.1 pattern.
3. **Given** the supervisor's own baseline MCP servers (`postgres`, `mempalace`, `finalize`), **When** the supervisor composes the final config, **Then** no baseline server matches the banned patterns (the supervisor's own baseline is not the source of the regression this rule guards against).

---

### User story 5 — each Infisical failure mode routes to a named adjudicator outcome (priority: P2)

When Infisical is unavailable, when the Machine Identity token has expired, when Infisical denies access, when the API is rate-limited, or when the requested secret path doesn't exist, the supervisor observes the specific failure and maps it to a supervisor adjudicator outcome. The five failure modes from the context's "Supervisor-side vault client" §6 — `vault_unavailable`, `vault_auth_expired`, `vault_permission_denied`, `vault_rate_limited`, `vault_secret_not_found` — each become a concrete `exit_reason` value the hygiene checker can surface. No Infisical failure produces an unhandled supervisor error, silent success, or generic `claude_error` classification.

**Why this priority**: failure graceful-ness is a ship criterion (context "Acceptance criteria framing" item 3). P2 because it's polish on top of the happy path — the plumbing works first (US1), then the failure modes get named.

**Independent test**: five `TestVaultFailureMode_*` integration tests, one per failure. Each uses a testcontainer Infisical configured to produce the target failure (container stopped = unavailable; expired token seeded = auth_expired; identity without permission = permission_denied; burst of 100 rapid requests = rate_limited; path not seeded = secret_not_found). Each observes the named `exit_reason` on the `agent_instances` row.

**Acceptance scenarios**:

1. **Given** Infisical is unreachable at the supervisor's configured hostname (e.g., container stopped), **When** the supervisor attempts a vault fetch for a role with a grant, **Then** the `agent_instances` row has `status='failed'`, `exit_reason='vault_unavailable'`, the `vault_access_log` row (if any) has `outcome='error_fetching'`, and no subprocess is spawned.
2. **Given** Infisical returns an HTTP 401 / auth-expired response to the Machine Identity, **When** the supervisor attempts a vault fetch, **Then** `exit_reason='vault_auth_expired'` and the supervisor does not retry the fetch within the same spawn attempt (the spawn is failed; operator rotates the MI credentials out-of-band).
3. **Given** Infisical returns an HTTP 403 for the specific path, **When** the supervisor attempts the fetch, **Then** `exit_reason='vault_permission_denied'` and the `vault_access_log.outcome` is `denied_infisical` (distinguished from `denied_no_grant` which applies only at the Garrison-side Rule 2 check).
4. **Given** Infisical returns an HTTP 429, **When** the supervisor attempts the fetch, **Then** `exit_reason='vault_rate_limited'`; the supervisor does not attempt in-flight retry at M2.3 (back-off loops are deferred per context "Out of scope" analogy to M2.1's rate-limit observability).
5. **Given** Infisical returns an HTTP 404 or equivalent "not found" for the requested path, **When** the supervisor attempts the fetch, **Then** `exit_reason='vault_secret_not_found'`; the operator sees the failure and fixes either the grant row's `secret_path` or the missing Infisical entry.

---

### User story 6 — dual audit: Garrison-side and Infisical-side logs both record the access, neither contains the secret value (priority: P2)

Every successful vault fetch produces two audit records: Infisical's native audit log (automatic via the API call) and a `vault_access_log` row in Garrison's Postgres. The dual record exists because Infisical's log is authoritative for "did access happen" but Garrison needs context-joined queries ("which tickets caused which accesses") that require the secrets log to be joined to tickets and agent_instances (threat model Rule 6). The Garrison-side row contains `agent_instance_id`, `ticket_id`, `secret_path`, `outcome`, `timestamp`, and does NOT contain the secret value. The value-exclusion is enforced at the Go type level in the vault client: the logger type has no method that accepts a `SecretValue` type (context "Supervisor-side vault client" item 5). A compile-time prevention, not a convention.

**Why this priority**: Rule 6 is testable but the test shape is "observe both logs after a run" — a richer integration check. P2 because it depends on US1 shipping first and the test is confirmatory rather than enabling.

**Independent test**: `TestVaultDualAuditRecord` — after a clean spawn (US1), grep Garrison's `slog` output, `vault_access_log` rows, and Infisical's audit log endpoint for the literal secret value. All three greps return zero matches. The `vault_access_log` row and the Infisical log entry both refer to the same `secret_path` and timestamp (within clock-skew tolerance).

**Acceptance scenarios**:

1. **Given** a successful vault fetch during US1's spawn, **When** the operator queries `vault_access_log`, **Then** exactly one row exists for the access with `outcome='granted'` and no column contains the secret value.
2. **Given** the same access, **When** the operator queries Infisical's native audit log via its API for the supervisor's Machine Identity over the spawn's time window, **Then** exactly one access record exists, naming the same path; its `timestamp` is within ±5 seconds of the `vault_access_log` row's `timestamp`.
3. **Given** the supervisor's code is compiled, **When** a developer attempts to add a `slog` call or audit-log write that passes a `vault.SecretValue`-typed variable as an argument, **Then** the Go compiler rejects the code (the vault package's logger type has no method accepting `SecretValue`; passing `string(secretValue)` requires an explicit, grep-visible conversion that code review catches).

---

### User story 7 — secret-pattern scanning redacts suspected secrets in agent output before MemPalace write (priority: P2)

Per context "Secret-pattern scanning of agent outputs" and threat model §3 addressed-threat 2, agent output is scanned for the fixed list of secret-shape patterns (Q7 clarification) before being written to MemPalace via `finalize_ticket`. On match, the matched substring is replaced with `[REDACTED:<pattern>]` in the MemPalace write, and a new hygiene status `suspected_secret_emitted` is recorded on the `ticket_transitions` row. The MemPalace write proceeds with the redacted value — the scan is best-effort by design (Q7, context "Secret-pattern scanning" last paragraph), and blocking the write on a heuristic match is overcorrection.

**Why this priority**: pattern scanning protects MemPalace from the threat model's "secrets appearing in downstream stores" addressed threat. P2 because it's a heuristic-grade safeguard, not a correctness primitive — Rules 1-3 are the primary defense; this is defense-in-depth.

**Independent test**: `TestSecretPatternScanRedactsBeforeMemPalaceWrite` — drive a mockclaude fixture that emits a `finalize_ticket` payload whose `diary_entry.rationale` contains a literal `sk-test-abc123`. Observe: (a) the MemPalace-written diary entry contains `[REDACTED:sk_prefix]` instead of the literal; (b) the `ticket_transitions.hygiene_status` is `suspected_secret_emitted`; (c) the finalize transition otherwise commits normally (the redaction does not fail the spawn).

**Acceptance scenarios**:

1. **Given** a finalize payload with `diary_entry.rationale` containing `"sk-test-abc123 is my key"`, **When** the supervisor applies the secret-pattern scan before the MemPalace write, **Then** the written diary entry reads `"[REDACTED:sk_prefix] is my key"` and `hygiene_status='suspected_secret_emitted'`.
2. **Given** a finalize payload with no matching patterns, **When** the scan runs, **Then** the MemPalace write content is byte-identical to the payload and `hygiene_status='clean'`.
3. **Given** a payload where a pattern matches inside a `kg_triples[].object` field, **When** the scan runs, **Then** the redaction applies there too (scan covers all string-typed payload fields the supervisor writes to MemPalace).
4. **Given** the scan finds a match, **When** the hygiene status is set, **Then** the transition still commits atomically per M2.2.1 semantics — the scan result is a property of the write content, not a gate on the transition.

---

### User story 8 — dual-logging failure is fail-closed: the spawn aborts with `vault_audit_failed` (priority: P2)

Per Q9, if the Infisical fetch succeeds but writing the corresponding `vault_access_log` row to Garrison's Postgres fails (Postgres unreachable, tx rollback, constraint violation), the supervisor discards the fetched value in memory, does not inject it into the subprocess, and fails the spawn with `exit_reason = "vault_audit_failed"`. Rule 6's "audit everything, log no values" is the motivation: a fetched value without its Garrison-side audit record is a provenance gap Rule 6 exists to prevent.

**Why this priority**: fail-closed is a deliberate safety tradeoff against availability. P2 because the failure window is narrow (Postgres is already required for the transaction enclosing the spawn) but the property matters for Rule 6 integrity.

**Independent test**: `TestVaultAuditFailureFailClosed` — use a testcontainer Postgres that starts healthy, spawn the agent up to the moment of `vault_access_log` insertion, simulate a Postgres error at that point (e.g., drop the table mid-run or intercept the insert), observe: (a) `agent_instances.exit_reason='vault_audit_failed'`; (b) no subprocess was spawned with the fetched value; (c) Infisical's native audit log shows the access (the fetch happened) but Garrison has no corresponding row (the reason the rule-enforcement fails closed).

**Acceptance scenarios**:

1. **Given** the Infisical fetch has succeeded and the fetched value is held only in the supervisor's memory, **When** the `vault_access_log` INSERT fails for any Postgres-side reason, **Then** the supervisor does not pass the value to the subprocess environment, the in-memory value is zeroed / released, and the `agent_instances` row records `exit_reason='vault_audit_failed'`.
2. **Given** the `vault_access_log` INSERT succeeds, **When** the subprocess spawns, **Then** the injection proceeds and US1's happy path applies.
3. **Given** fail-closed fired, **When** the operator reviews the failure, **Then** Infisical's audit log still shows the fetch (the access did happen at Infisical); the Garrison-side provenance gap is the signal for operator follow-up (unusual failure → investigate Postgres health).

---

### User story 9 — `secret_metadata` is populated at Infisical bootstrap for M3 read-surface queries (priority: P3)

When the operator seeds secrets into Infisical during M2.3 bootstrap (via the Infisical UI or API, per the operator's out-of-band discipline), a corresponding `secret_metadata` row is written to Garrison's Postgres. The row records `secret_path`, `customer_id`, `provenance` (enum: `operator_entered`, `oauth_flow`, `environment_bootstrap`, `customer_delegated`), `rotation_cadence` (`30d` / `90d` / `never`), `last_rotated_at`, `last_accessed_at` (updated by the vault client on each successful fetch), and `allowed_role_slugs` (denormalized from `agent_role_secrets`; sync discipline flagged for clarify). At M2.3 this table is populated by the initial seeding migration and by the OAuth flow handler for delegated secrets; M4's write dashboard later will maintain it on CRUD.

**Why this priority**: P3 because M2.3 does not consume `secret_metadata` at runtime — the vault client reads grants from `agent_role_secrets` and fetches values from Infisical directly. `secret_metadata` exists so M3's read surfaces have something to query without hitting Infisical's API per dashboard page load. The table must exist and be populated enough that M3 has real data, but the runtime vault path doesn't depend on it.

**Independent test**: `TestSecretMetadataPopulatedAtBootstrap` — after the bootstrap migration runs and the operator seeds one secret, observe: (a) one `secret_metadata` row for the path; (b) `provenance`, `rotation_cadence`, `customer_id` columns are populated per the seed; (c) `last_accessed_at` is NULL prior to any access, and is updated by the vault client's fetch path on the first US1-style spawn.

**Acceptance scenarios**:

1. **Given** an operator-seeded secret in Infisical with path `/<customer_id>/operator/example_api_key`, **When** the M2.3 bootstrap migration runs and the seed step completes, **Then** one `secret_metadata` row exists for that path with `provenance='operator_entered'`, `rotation_cadence='90d'` (default), `last_rotated_at=<seed time>`, `last_accessed_at=NULL`.
2. **Given** a subsequent successful vault fetch for that secret during an agent spawn, **When** the supervisor's vault client completes the fetch and audit write, **Then** `secret_metadata.last_accessed_at` is updated to the fetch timestamp within the same transaction as the `vault_access_log` write.
3. **Given** M3's eventual read-surface query "show me secrets past rotation deadline", **When** M3 joins `secret_metadata` on `now() - last_rotated_at > rotation_cadence`, **Then** the data is present and queryable without reaching Infisical's API.

---

### Edge cases

- **A grant row references a secret path that Infisical does not have**. Failure routes to `vault_secret_not_found` (US5 scenario 5). The spawn fails; the operator fixes either the grant or the Infisical entry. The `vault_access_log` row is written with `outcome='error_fetching'`.
- **The agent.md contains a string that looks like a secret but is not one** (e.g., a hex sequence that happens to match the PEM-header regex in agent.md explanation prose). Rule 1's scan compares against the *values* about to be injected, not against generic patterns — a false positive here is impossible because the scan is literal-substring against the specific fetched values. No regression risk.
- **Two grant rows for the same role grant the same `env_var_name` via different `secret_path`s** — blocked at migration time by the `(role_slug, env_var_name)` uniqueness constraint on `agent_role_secrets` (context "Data model" §`agent_role_secrets`). The second migration fails; the operator reconciles before merging.
- **A secret whose literal value is short enough to false-positive-match agent.md content by coincidence** (e.g., a 3-char API key colliding with generic English). Rule 1 treats the match as a real hit and blocks the spawn; operator rotates the secret to a longer value before retrying. This is the right behavior — a 3-char secret is broken anyway.
- **Claude emits a `finalize_ticket` payload whose `diary_entry.rationale` contains what looks like a secret but is not** (e.g., the string `"sk-note: see README"`). The pattern scanner redacts it to `[REDACTED:sk_prefix]` and sets `hygiene_status='suspected_secret_emitted'` (US7). The operator manually reviews the diary, sees the false positive, and either accepts the redaction or backfills the content. The false positive is noise, not breakage.
- **Operator rotates a secret in Infisical while an agent_instance is running with the old env-var value**. The supervisor takes no action on the running agent (FR-429). The running agent finishes its work with the stale value; the next spawn of that role performs its own Rule 2 fetch and picks up the new value. If the rotation was urgent (e.g., the old value was compromised), the operator terminates running agents out-of-band (SIGTERM via operator-side tooling not in M2.3 scope) and new spawns use the rotated value.
- **Two agents spawn concurrently for roles with disjoint grants**. Each spawn performs its own Rule 2 query (no shared state), fetches its own secrets, writes its own `vault_access_log` row. No cross-agent contamination. Per-department concurrency caps from M1 still bound the total.
- **The supervisor's `INFISICAL_MACHINE_CLIENT_SECRET` env var is unset at startup**. The supervisor fails startup with a clear error naming the missing bootstrap env var (Q6). No agent spawns occur; the operator fixes the Coolify env configuration.
- **Infisical is up but its Postgres backing service is down**. Infisical returns an internal error; the supervisor classifies as `vault_unavailable` (US5). Infisical's own health is a black box from Garrison's perspective — the supervisor sees "the API didn't respond correctly" and routes accordingly.
- **The Rule 1 scan is expensive for a role with many grants** (say, 20 secrets × 5KB agent.md = 100KB of substring work per spawn). Acceptable at M2.3 — the spawn path is not hot. If this becomes a bottleneck, scan cadence becomes a Q for a future milestone (already flagged for clarify).

## Requirements (mandatory)

### Functional requirements

**Supervisor-side vault client (`internal/vault`)**

- **FR-401**: the supervisor MUST include a new `internal/vault` Go package that authenticates to the configured Infisical instance as a Machine Identity via Universal Auth. Credentials (`client_id` + `client_secret`) MUST be read from environment variables (`INFISICAL_MACHINE_CLIENT_ID`, `INFISICAL_MACHINE_CLIENT_SECRET`) per Q6. If either variable is unset at supervisor startup, the supervisor MUST fail startup with a named error; the supervisor MUST NOT continue in a partially-configured state.
- **FR-402**: the vault client MUST fetch secrets by path. The fetch call takes a set of `{secret_path, env_var_name}` pairs (derived from the Rule 2 query in FR-409) and returns a map `env_var_name → secret_value`. The client MUST NOT fetch paths outside the passed set.
- **FR-403**: the vault client MUST represent secret values using a distinct Go type (`vault.SecretValue`) whose encoding-format methods (`MarshalText`, `MarshalJSON`, `String`, `GoString`, any `slog.LogValuer` implementation) MUST return a redacted placeholder (`[REDACTED]`) rather than the raw value. The raw value is accessible only via an explicit accessor that the no-log logger type does not call. Per threat model Rule 6 consequence ("logger type has no method that accepts a value") and context "Supervisor-side vault client" item 5.
- **FR-404**: the vault client MUST write a `vault_access_log` row to Garrison's Postgres in the same transaction as (or atomically adjacent to) the `agent_instances` INSERT for the spawn. If the `vault_access_log` INSERT fails, the spawn MUST abort with `exit_reason='vault_audit_failed'` per US8 / Q9 fail-closed. The fetched value MUST be zeroed / released from the supervisor's memory on this abort path and MUST NOT be passed to any subprocess.
- **FR-405**: the vault client MUST produce named supervisor adjudicator outcomes for each Infisical failure mode: HTTP unreachable / TLS error → `vault_unavailable`; HTTP 401 / token-expired → `vault_auth_expired`; HTTP 403 → `vault_permission_denied`; HTTP 429 → `vault_rate_limited`; HTTP 404 / path-missing → `vault_secret_not_found`. No Infisical error MAY route to generic `claude_error` classification. Per context "Supervisor-side vault client" item 6. Per Session 2026-04-24 clarification: on HTTP 401, the supervisor MUST attempt exactly one automatic re-authentication using the held ML `client_id` / `client_secret` and retry the fetch once; only if the re-auth itself fails (or the subsequent fetch fails again with 401) does the spawn surface `vault_auth_expired`. `vault_auth_expired` MUST NOT be applied to a currently-running `agent_instance` — running subprocesses hold injected env vars independently of the supervisor's token state.
- **FR-406**: the vault client MUST NOT log secret values. Compile-time: `vault.SecretValue`'s formatter methods produce `[REDACTED]` per FR-403. Runtime: the package's test suite includes a grep-based assertion that scans captured `slog` output for any secret value across every test that fetches a secret.

**Spawn-time enforcement**

- **FR-407**: before spawning a Claude subprocess, the supervisor MUST run the **Rule 1 leak scan** per-spawn (no caching): for each `{env_var_name, secret_value}` pair about to be injected, substring-search the freshly-resolved `agent.md` content for the literal `secret_value`. If any match is found, the spawn MUST abort with `exit_reason='secret_leaked_in_agent_md'`; no subprocess is spawned; the `agent_instances` row records the failure. If the grant set is empty (zero `agent_role_secrets` rows), the scan is skipped (nothing to search for). Per Session 2026-04-24 clarification, the scan runs on every spawn regardless of whether the agent's grant set or agent.md content have changed since the last spawn.
- **FR-408**: the Rule 1 scan MUST be performed after the vault fetch but before the subprocess invocation. This ordering requires holding the fetched values in memory briefly before deciding whether to inject; the values MUST be zeroed on the abort path per FR-404.
- **FR-409**: before any vault fetch, the supervisor MUST run the **Rule 2 grant query**: `SELECT env_var_name, secret_path FROM agent_role_secrets WHERE role_slug = ? AND customer_id = ?`. If the result set is empty, the supervisor MUST skip the Infisical fetch entirely (FR-402's input is empty, so no API call occurs). Per Q5 and context "Spawn-time enforcement" item 2.
- **FR-410**: before spawning, the supervisor MUST run the **Rule 3 MCP-config check**: inspect the final merged per-invocation MCP config (baseline servers + `agents.mcp_config`) for any entry whose server name or key matches a banned vault-pattern (case-insensitive substring match against `vault`, `secret`, `infisical`). On match, the spawn MUST abort with `exit_reason='vault_mcp_in_config'`. The supervisor's baseline MCP servers (`postgres`, `mempalace`, `finalize`) MUST NOT match these patterns.

**Data model**

- **FR-411**: the M2.3 migration MUST create the `agent_role_secrets` table with columns `role_slug` (text, FK to `agents.role_slug`), `secret_path` (text), `env_var_name` (text), `customer_id` (UUID), `granted_at` (timestamptz), `granted_by` (text — operator user ID or the literal `'system'` for bootstrap), plus standard audit columns (`created_at`, `updated_at`). A unique constraint on `(role_slug, env_var_name, customer_id)` MUST exist. No default grants; no rows inserted for existing roles.
- **FR-412**: the M2.3 migration MUST create the `vault_access_log` table with columns `id` (UUID PK), `agent_instance_id` (UUID, FK to `agent_instances`), `ticket_id` (UUID, nullable, FK to `tickets`), `secret_path` (text), `outcome` (text, enum: `granted` / `denied_no_grant` / `denied_infisical` / `error_fetching` / `error_injecting` / `error_auditing`), `timestamp` (timestamptz), plus standard audit columns. The table MUST NOT have any column typed or named suggestively of storing the secret value. Indexes MUST exist on `agent_instance_id` and `ticket_id` for M3's context-joined queries.
- **FR-413**: the M2.3 migration MUST create the `secret_metadata` table with columns `secret_path` (text PK within customer), `customer_id` (UUID, part of composite PK with `secret_path`), `provenance` (text, enum: `operator_entered` / `oauth_flow` / `environment_bootstrap` / `customer_delegated`), `rotation_cadence` (interval: default `90d`, allowed `30d`, `90d`, `never`), `last_rotated_at` (timestamptz), `last_accessed_at` (timestamptz, nullable), `allowed_role_slugs` (text[], denormalized), plus standard audit columns.
- **FR-413a**: the M2.3 migration MUST create a Postgres trigger on `agent_role_secrets` (AFTER INSERT OR UPDATE OR DELETE, FOR EACH ROW) that rebuilds `secret_metadata.allowed_role_slugs` for the affected `(secret_path, customer_id)` tuple inside the same transaction. Per Session 2026-04-24 clarification. The trigger body reads the current set of `role_slug` values from `agent_role_secrets` for the affected tuple and writes the array into `secret_metadata`. The trigger is idempotent (repeated firings for the same tuple produce the same array) and safe under the goose up-down-up sequence. This is Garrison's first Postgres trigger; the M2.3 retro flags it as a precedent-setting introduction per AGENTS.md "What agents should not do" (not a decision change, but a stack-surface additon).
- **FR-414**: the M2.3 migration MUST seed `agent_role_secrets` with zero rows for the `engineer` and `qa-engineer` roles; neither role requires secrets at ship time. Per Q12, future grants land via one migration per grant set.
- **FR-415**: the `customer_id` column on all three vault tables MUST default to the operating entity's identifier (the single row in `companies` at M2.3). Per context "Data model" paragraph 1 and Q10, the path prefix convention is `/<customer_id>/...`.

**Vault client integration with spawn path**

- **FR-416**: the supervisor's existing spawn path (from M2.1's `internal/spawn`, extended by M2.2, M2.2.1, M2.2.2) MUST call the vault client between Rule-1-leak-scan preparation and subprocess invocation. The sequence is: (1) Rule 2 grant query; (2) Rule 3 MCP-config check (no secret values needed — fail-fast before Infisical RTT); (3) vault fetch for the grant set (may be empty); (4) Rule 1 leak scan against resolved agent.md using the fetched values; (5) `vault_access_log` write (may abort to `vault_audit_failed`); (6) subprocess invocation with injected env vars; (7) `secret_metadata.last_accessed_at` update (may be in the same tx as the audit write). Rule 3 runs at step 2 so that a misconfigured `agents.mcp_config` aborts the spawn before the supervisor commits an Infisical round-trip; the check operates on the generated MCP config alone and does not require any fetched value.
- **FR-417**: injected secrets MUST appear only in the subprocess's environment — not in `claude`'s argv, not in `--system-prompt`, not in `--mcp-config`, not in any structured-log line, not in any stream-json event the supervisor emits. The supervisor's existing M2.1 argv-recording fixture MUST be extended with a grep-based assertion that no captured argv contains any currently-injected secret value.

**Secret-pattern scanning of agent output**

- **FR-418**: before the supervisor writes any agent-produced content to MemPalace via `finalize_ticket`, it MUST scan each string-typed field in the finalize payload (`diary_entry.rationale`, `kg_triples[].subject`, `kg_triples[].predicate`, `kg_triples[].object`, and any future string field M2.3 adds or inherits) against the fixed pattern list from Q7. Per Session 2026-04-24 clarification, the scan's attachment point is the finalize handler only — stream-json `tool_use` events (including `mempalace_*` tool calls the agent issues directly) and supervisor `slog` lines are NOT scanned at M2.3. Extending coverage is a follow-up gated on hygiene-dashboard findings.
- **FR-419**: on pattern match, the supervisor MUST replace the matched substring with `[REDACTED:<pattern>]` in the written content AND set `ticket_transitions.hygiene_status` to `suspected_secret_emitted` for the transition. The transition MUST still commit atomically per M2.2.1 semantics; the scan result is a property of the write content, not a gate on the commit. Per Q7.
- **FR-420**: the `hygiene_status` vocabulary on `ticket_transitions` MUST be extended additively with `suspected_secret_emitted`. No existing hygiene values are removed or renamed. M2.2.2's `clean` / `pending` / `missing_diary` / `missing_kg` / `thin` / `finalize_failed` / etc. all continue to apply unchanged.

**Infisical deployment**

- **FR-421**: the Coolify deployment MUST include an Infisical service running `infisical/infisical:<pinned-digest>` per Q8, with its own PostgreSQL and Redis backing services deployed alongside (not sharing Garrison's Postgres). Per context "Infisical deployment" and threat model §5.
- **FR-422**: the Infisical service MUST be bound to the Coolify internal network, not exposed publicly. The supervisor and dashboard reach Infisical via the internal hostname. Per context "Infisical deployment".
- **FR-423**: two Machine Identities MUST be created during Infisical bootstrap: `garrison-supervisor` (read-only on paths scoped to supervisor-needed secrets) and `garrison-dashboard` (read+write, scoped for M4's write flows). M2.3 uses only the supervisor identity at runtime; the dashboard identity is created-and-parked for M4. Per context "Infisical deployment".
- **FR-424**: Infisical's own bootstrap secrets (`ENCRYPTION_KEY`, `AUTH_SECRET`) MUST be generated via `openssl rand -base64 32` per Infisical's documented procedure and set via Coolify environment variables at deploy time (Q6). The ops checklist (`docs/ops-checklist.md`) MUST document the generation procedure, the storage discipline (password manager, never `.env`-committed), and the rotation procedure.

**Scope discipline**

- **FR-425**: the M2.3 implementation MUST add exactly two new Go dependencies to `supervisor/go.mod` / `supervisor/go.sum`: (1) `github.com/infisical/go-sdk` (vault client) and (2) `golang.org/x/tools/go/analysis` (custom `vaultlog` vet analyzer enforcing SC-410 at build time). Both additions MUST be justified in their respective commit messages per AGENTS.md stack soft-rule and flagged in the M2.3 retro. Prior milestones (M1, M2.1, M2.2, M2.2.1, M2.2.2, post-ship pgmcp fix) held the locked-deps line at zero additions; M2.3 breaks that streak with two principled additions, both tied directly to threat-model Rule 6 enforcement.
- **FR-426**: the M2.3 implementation MUST NOT introduce a dashboard surface, a web UI, a CEO-chat surface, or any operator-facing HTTP endpoint for secrets. All operator-facing UI is deferred to M3 (read) and M4 (write) per the context's scope deviation from the threat model. Per context "Out of scope for M2.3" and "What this milestone is NOT".
- **FR-427**: the M2.3 implementation MUST NOT introduce workspace-sandboxing work. The `docs/issues/agent-workspace-sandboxing.md` issue is acknowledged and orthogonal per M2.2.x retro §13. A sandboxed-filesystem boundary is planned post-M2.3 as Docker-per-agent work; M2.3 ships with that gap acknowledged and deferred.
- **FR-428**: the M2.3 implementation MUST NOT alter the `finalize_ticket` schema, the Adjudicate precedence rules from M2.2.2, the atomic-write transaction from M2.2.1, the MemPalace wiring from M2.2, or the pg_notify channel names from M1. All prior-milestone architectural decisions are preserved. M2.3 is additive.
- **FR-429**: the supervisor MUST NOT respond to Infisical-side secret rotation by terminating running `agent_instances` or re-injecting env vars into live subprocesses. Running agents finish with whatever value they were spawned with; the next spawn of the same role picks up the rotated value on its own Rule 2 fetch. Per Session 2026-04-24 clarification. Rotation-event subscription, respawn-on-rotation, and `secret_stale` tagging are all explicitly out of scope at M2.3.

### Key entities

- **Vault secret value (`vault.SecretValue`)**: the Go type representing a secret value fetched from Infisical. Its formatter methods (`String`, `MarshalText`, `MarshalJSON`, `LogValue`) return `[REDACTED]`. The raw value is accessible only via a named explicit accessor (e.g., `UnsafeValue()`) whose use is grep-auditable. Not a persistent entity; held in supervisor memory between fetch and subprocess-env-injection and zeroed after injection.
- **Vault grant row (`agent_role_secrets`)**: one grant per `(role_slug, env_var_name, customer_id)` tuple. Maps a Garrison-side role to an Infisical-side path and an env var name. The primary authoritative policy surface for Rule 2 per-role scoping.
- **Vault access record (`vault_access_log`)**: one row per supervisor-initiated vault access attempt (successful or failed). Context-joined to `agent_instances` and `tickets`. Contains no secret value. The primary Garrison-side audit surface for M3's read dashboard.
- **Secret metadata record (`secret_metadata`)**: one row per secret-path × customer. Carries provenance, rotation cadence, last-rotated and last-accessed timestamps, denormalized role allowlist. The primary query surface for M3's "stale secrets" and "which roles can access this secret?" views.
- **Supervisor Machine Identity**: the `garrison-supervisor` identity in Infisical, authenticated via Universal Auth with client_id + client_secret read from supervisor env vars. Scoped read-only on the supervisor-accessible subset of secrets. One identity per supervisor instance (Q2).
- **Rule enforcement outcome**: an `exit_reason` value written to `agent_instances` when a spawn-time rule check fails. New values added by M2.3: `secret_leaked_in_agent_md` (Rule 1), `vault_mcp_in_config` (Rule 3), `vault_unavailable` / `vault_auth_expired` / `vault_permission_denied` / `vault_rate_limited` / `vault_secret_not_found` (Infisical failure modes), `vault_audit_failed` (Q9 dual-log fail-closed). Each is grep-distinguishable in hygiene queries.

## Success criteria (mandatory)

### Measurable outcomes

- **SC-401**: `TestVaultSpawnWithSingleSecret` (US1 independent test) passes: a real Infisical testcontainer holds a secret; the supervisor spawns an engineer with a grant; the subprocess's environment contains `EXAMPLE_API_KEY=<fetched value>`; `agent_instances.status='succeeded'`; one `vault_access_log` row with `outcome='granted'`; the value appears in no `slog` output, no `claude` argv, no MCP config, no stream-json event.
- **SC-402**: `TestVaultRule1BlocksSpawnOnLeakedValue` passes: an agent.md containing a literal secret value aborts the spawn with `exit_reason='secret_leaked_in_agent_md'`; no subprocess; no `claude` invocation. Reverse case (env-var-name-only reference) spawns successfully.
- **SC-403**: `TestVaultRule2ZeroGrantsZeroSecrets` passes: a role with no `agent_role_secrets` rows triggers no Infisical API call and no `vault_access_log` row; the spawn proceeds with a base environment carrying no vault-sourced variables.
- **SC-404**: `TestVaultRule3BlocksSpawnOnVaultMcp` passes: an `agents.mcp_config` containing a banned vault-pattern aborts the spawn with `exit_reason='vault_mcp_in_config'`. Reverse case (no vault MCP) spawns successfully.
- **SC-405**: the five `TestVaultFailureMode_*` tests (US5) each pass: each Infisical-side failure produces its named `exit_reason` with the correct `vault_access_log.outcome`, and no failure routes to `claude_error`.
- **SC-406**: `TestVaultDualAuditRecord` passes: after a clean spawn, both `vault_access_log` and Infisical's native audit log contain one record for the access, referencing the same path within ±5 seconds; neither log contains the secret value on a grep.
- **SC-407**: `TestSecretPatternScanRedactsBeforeMemPalaceWrite` passes: a finalize payload containing a pattern-matching substring produces a MemPalace write with the redaction placeholder, and the transition's `hygiene_status='suspected_secret_emitted'`; the transition still commits atomically.
- **SC-408**: `TestVaultAuditFailureFailClosed` passes: a simulated `vault_access_log` INSERT failure after a successful fetch produces `exit_reason='vault_audit_failed'`; no subprocess is spawned; the in-memory value is not leaked to environment or log.
- **SC-409**: `TestSecretMetadataPopulatedAtBootstrap` passes: after the bootstrap migration and one seed, a `secret_metadata` row exists with correct `provenance`, `rotation_cadence`, `customer_id`; a subsequent successful spawn updates `last_accessed_at`.
- **SC-410**: compile-time assertion — an attempt to add a Go code change passing a `vault.SecretValue` (or its raw string form without an explicit `UnsafeValue()` call) to `slog.*` produces a compile-time or lint-time failure. The guard is either the absence of a matching logger method (preferred) or a `go vet` / custom linter rule (fallback).
- **SC-411**: `git diff --stat origin/main..HEAD -- supervisor/go.mod supervisor/go.sum` shows **exactly two** new direct dependencies: `github.com/infisical/go-sdk` and `golang.org/x/tools/go/analysis`. No other direct additions; no removals. Transitive-dep additions pulled in by these two are permitted without additional justification. Per FR-425.
- **SC-412**: the `docs/ops-checklist.md` file contains a new M2.3 section covering: Infisical container digest-pinning at deploy time, `ENCRYPTION_KEY` / `AUTH_SECRET` generation and storage, Machine Identity credential rotation procedure, and the discipline around `.env`-committing (explicit don't).
- **SC-413**: all M1, M2.1, M2.2, M2.2.1, M2.2.2 tests pass unchanged. M2.3 is additive; no prior-milestone test is modified to accommodate M2.3 behavior except where a fixture adds the `customer_id` column (additive; existing assertions preserved).
- **SC-414**: the three migrations (`agent_role_secrets`, `vault_access_log`, `secret_metadata`) are idempotent under the goose up-down-up sequence: the sequence produces no schema drift, no orphaned rows, and no failed transitions on a fresh database.
- **SC-415 (headline)**: the combined M2.3 acceptance evidence document (`specs/007-m2-3-infisical-vault/acceptance-evidence.md` per prior-milestone pattern) records all SC-401 through SC-414 as passing, captures the testcontainer Infisical version pinning used for SC-401–SC-409, and links to the ops-checklist section for SC-412. The M2.3 retro references this evidence doc.

## Assumptions

- The Infisical Go SDK's Universal Auth flow and secret-fetch API behave per Infisical's public documentation at `infisical.com/docs`. M2.3 does not carry a research spike (context "Binding inputs" item 4 names the docs as the reference). If the SDK deviates from the docs during implementation, that deviation becomes a clarify item for the plan phase, not a blocker for the spec.
- The Coolify deployment supports adding a new service to the existing project without requiring network-topology surgery. The M2.2 socket-proxy topology already established the multi-service pattern (threat model §"M2.2 deployment assumptions"); Infisical is additive.
- `testcontainers-go` supports running Infisical as a container. If it doesn't, fallback to a Docker Compose-based integration harness is acceptable; the mock-Infisical option is rejected per Q11.
- The supervisor's `SpawnContext` (or equivalent plumbing carrying role_slug, ticket_id, agent_instance_id) is already available at the point the vault client integrates (FR-416). M2.1's `internal/spawn` and M2.2's extensions have threaded this context through.
- The `companies` table has exactly one row at M2.3 ship; its UUID is the single value used for `customer_id` across all vault tables. If multi-company ever activates, the `customer_id` column becomes a real discriminator; M2.3 ships single-tenant per context "Out of scope" multi-tenancy deferral.
- The ops checklist pattern from M2.1 (`garrison_agent_ro` password generation) and M2.2 (`garrison_agent_mempalace` password generation) is extended in M2.3 for Infisical's bootstrap secrets. No new documentation patterns are introduced.
- The operator seeds the initial set of Infisical secrets out-of-band via Infisical's own CLI or API as part of the M2.3 deployment rollout, following the ops checklist. M2.3 does not ship a seed-automation tool; M4's write dashboard will.
- `claude` (the binary) does not leak subprocess environment variables into its stream-json output. If an M2.3 spike or the implementation phase discovers otherwise, the supervisor's injection path would need a remediation (scrub env vars from any reflected output) — flagged as a spike-worthy concern for the plan phase but not pre-committed.
- The post-ship investigation at the end of M2.2.x has cleared the plumbing (see retro §7 post-fix validation); M2.3 builds on clean plumbing per retro §13 "M2.3 readiness".

## Dependencies on prior milestones

- **M2.2.x arc closed** with clean plumbing per retro §13. The `finalize_ticket` mechanism, `internal/finalize` server, `vault_access_log`-analogous hygiene vocabulary, `hygiene_status` column on `ticket_transitions`, and the testcontainer integration pattern are all intact.
- **M2.2 shipped** the MemPalace MCP wiring and the socket-proxy Coolify topology (threat model §"M2.2 deployment assumptions"). M2.3's Infisical service lands alongside these in the same Coolify project network.
- **M2.1 shipped** the Claude Code invocation contract, `--mcp-config` generation, stream-json parsing, and the `garrison_agent_ro` Postgres role + grants pattern. M2.3 does not alter any of these; the vault client integrates into the existing spawn path between the Rule 2 grant query and the subprocess invocation.
- **M1 shipped** the event bus, the supervisor lifecycle, the pg_notify channel naming, and `agent_instances.exit_reason` as the canonical failure vocabulary. M2.3 extends the vocabulary additively; no existing value is renamed.
- **`internal/spawn/exitreason`** from M2.1 is the extension point for the new `exit_reason` values (`secret_leaked_in_agent_md`, `vault_mcp_in_config`, `vault_unavailable`, `vault_auth_expired`, `vault_permission_denied`, `vault_rate_limited`, `vault_secret_not_found`, `vault_audit_failed`).

## Out of scope

Per [`specs/_context/m2.3-context.md`](../_context/m2.3-context.md) §"Out of scope for M2.3 (explicit deferrals)" and §"What this milestone is NOT", the following are explicitly NOT part of this milestone:

- **Operator-facing UI for secrets**: browsing, CRUD, audit-log viewer, rotation UI, access-control grant editing, secret-usage view, error-handling UI for Infisical failure modes. All deferred to M3 (read) and M4 (write).
- **Multi-tenancy enforcement**: `customer_id` column exists on all vault tables (FR-411 – FR-413), but per-customer isolation is not enforced at M2.3. True multi-tenancy is a future additive migration.
- **Workspace sandboxing**: the `docs/issues/agent-workspace-sandboxing.md` issue is orthogonal and deferred post-M2.3 to Docker-per-agent work. M2.3's acceptance tests do NOT test workspace isolation.
- **Intentional operator-leakage mitigations**: if the operator pastes a secret into a chat message, commits a `.env`, or otherwise bypasses Garrison's vault surface, the vault cannot save them. Per threat model §3 accepted-threat 2.
- **Host-compromise defenses**: if the host running Garrison is rooted, every secret is compromised regardless of vault design. Mitigation is systems-level and belongs elsewhere. Per threat model §2 deprioritized adversaries.
- **Dashboard / web UI work of any kind**: M3 concern.
- **Rate-limit back-off / retry loops inside the vault fetch path**: M2.3 fails the spawn on HTTP 429; retry logic is deferred analogous to M2.1's rate-limit observability-only posture.
- **Secret rotation runtime-invalidation**: rotation metadata is first-class in `secret_metadata`, but an agent_instance holding a stale value after rotation is not killed and respawned at M2.3. Flagged for clarify.
- **Bootstrap-automation tooling**: operator seeds Infisical via its own UI/API per the ops checklist. A Garrison-native seed tool is M4's concern.
- **Scanner-pattern expansion beyond the Q7 list**: new patterns land in a follow-up if the hygiene dashboard surfaces regressions. The fixed list is the M2.3 shipping surface.
- **CEO-agent access to vault**: the CEO is an agent like any other at M2.3; if the CEO role needs secrets, it acquires grants via `agent_role_secrets`. No special-case vault path for the CEO exists.

If the implementation phase tries to expand into any of these, stop and surface the scope violation.
