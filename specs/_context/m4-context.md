# M4 context — operator dashboard mutations

**Status**: Context for `/garrison-specify m4`. M3 (operator dashboard, read-only) shipped 2026-04-26 and closed its post-ship polish round on 2026-04-27 (`docs/retros/m3.md`). This is the second frontend-bearing milestone; the dashboard turns from observational console to operational console.

**Precondition**: `docs/security/vault-threat-model.md` must be amended before `/speckit.specify` runs. The amendment was committed as a follow-up in `m2.3-context.md` and never landed; Rule 7 currently asserts "rotation is not deferrable to M4 or later" while the M2.3 context already deviated to put rotation UI in M4. The doc is self-contradicting until it is amended. See §"Scope deviation from committed doc" below.

**Binding inputs** (read these before writing the spec; full annotations in §"Binding inputs"):

- `ARCHITECTURE.md` §M4 (line 572), §Dashboard surfaces (line 672), §Open questions for later
- `AGENTS.md` §M2.3 / §M3 / §Scope discipline / §What agents should not do
- `RATIONALE.md` §3 (memory thesis), §4 (Postgres state store), §5 (soft gates), §7 (dashboard surfaces hygiene), §8 (web UI is the operator console)
- `specs/_context/m2.3-context.md` §"Deferred to M4 (dashboard write)"
- `specs/_context/m3-context.md` §"Deferred to M4 (dashboard mutations)"
- `docs/security/vault-threat-model.md` (post-amendment) §Rules 1–7, §6 open questions, §7 retro questions
- `docs/retros/m3.md` §"What got fixed in the polish round" + §"Open questions deferred to the next milestone"
- `docs/retros/m2-3.md` §"Open questions deferred to the next milestone" Q4 (parked `garrison-dashboard` ML)
- `docs/issues/cost-telemetry-blind-spot.md`, `docs/issues/agent-workspace-sandboxing.md` (still surfaced, not fixed in M4)

---

## Scope deviation from committed doc — READ FIRST

`docs/security/vault-threat-model.md` Rule 7 (line 139) currently says:

> "M2.3's UI work includes the rotation view, not just CRUD. Rotation is not deferrable to M4 or later — rule 7 is part of what the vault actually is, not an optional feature."

The M2.3 context already deviated from this — it deferred secret CRUD, grant editing, rotation initiation UI, env/path tree management, and operator-facing error UI to M4 (`m2.3-context.md` §"Deferred to M4"). The M2.3 context's own deviation section flagged the threat-model amendment as a follow-up: *"this is a 15-20 minute doc edit, not a rewrite, and should happen before M4 activates so the retro expectations are aligned."* That edit never landed.

M4 inherits the deviation. The threat model still scopes vault rotation UI to M2.3, while M2.3 shipped without it and M4 is now picking it up. The fix is the threat-model amendment, not a re-litigation of where rotation belongs.

**Required amendment scope** (binding on whoever does the doc edit before `/speckit.specify`):

- §5 deployment shape: add milestone annotations distinguishing M2.3 (supervisor + data model), M3 (read surfaces), M4 (write surfaces).
- Rule 7 phrasing: separate rotation *data model* (M2.3) from rotation *UI* (M4). The first-class status of rotation is preserved; the milestone banding is updated.
- §7 retro questions: questions 2 (option-A UI coherence), 4 (rotation discipline), 5 (audit log usefulness from operator workflow) are all UI-dependent and belong on M4's retro, not M2.3's. Move them.
- §6 open question 6 (rotation UX surface): closed by M4's spec, not left open.

This section is the only conditional section of the canonical structure. Once the amendment lands, this section's "READ FIRST" framing softens to a pointer; M4's spec proceeds against the amended threat model.

---

## Why M4 now

After M3, every M2-arc surface is observable: tickets, transitions, agent instances, vault grants, vault audit, hygiene rows, agents registry, organization overview. The operator can see the system behave. They cannot yet act on what they see.

The current operator workflow for any state change is `psql`, `goose up`, or hand-edited YAML — exactly the surfaces RATIONALE §8 names as ad-hoc tooling that the dashboard exists to replace. M3 closed the observation half of "you live here day-to-day"; M4 closes the action half.

Three forces converge on M4 specifically, in this order:

1. **Vault writes were always M4's concern.** The M2.3 context deferred secret CRUD, grant editing, rotation, env/path tree, and operator error UI here. The `garrison-dashboard` Machine Identity is parked since M2.3 (M2.3 retro Q4) and activates this milestone.
2. **M3's read-only-first thesis paid off** (per ARCHITECTURE.md §M3 and the M3 retro polish round). Several event-payload-shape and rendering bugs were caught during M3's two-day polish window because the operator was watching the system without being able to change its state. The thesis was: "watch the system behave for a few days before giving yourself (and agents) the ability to change state." That window has now closed; the bugs the read-only window was meant to surface have been surfaced.
3. **The dashboard is the operator's console** (RATIONALE §8). Without write surfaces, the console is a viewer with a SQL prompt next to it. Every committed doc that describes the dashboard's role names mutations as part of the product, not as an extension.

M4 also discharges three M3-retro carryovers that were tagged for "the milestone that gains schema mutation rights":

- `suspected_secret_emitted` pattern-category surfacing (M3 retro Q3) — the supervisor scanner labels patterns; the dashboard cannot show them without a schema extension. Lands here.
- Drizzle CLI / production migration story (M3 retro Q1) — M3 left operators running migrations from a separate ops shell; M4 is the natural milestone to reaffirm or revisit.
- `next start` → standalone harness migration (M3 retro Q2) — closer production parity in the test harness.

---

## In scope for M4

Three operator-facing mutation surfaces, plus three M3-retro carryovers, plus the cross-cutting authorization / audit / mutation-event-shape work that all three surfaces share. Authorization is **any logged-in operator can perform all mutations** — no per-action permissions, no approval flows, no dual-control. This is a stated scope decision (not a deferral); the spec writes the consequence (single permission tier) rather than re-debating it.

### 1. Ticket mutations

Three sub-mutations against the existing `tickets` and `ticket_transitions` tables:

- **Create-ticket in UI**: operator-authored, populated into the source column for the relevant department. CEO-decomposed tickets remain M6. The form covers title, description, priority, target department, target column (typically the source column for the department's Kanban). Validation: required fields, priority enum, dept/column foreign keys.
- **Drag-to-move on Kanban**: writes a transition to `ticket_transitions`. **Whether operator-direct transitions traverse the same finalize_ticket-mediated path agents use, or a separate operator transition path that bypasses hygiene checks, is an open question for the spec** — both have consequences (operator transitions feeding hygiene_status creates noise; operator transitions bypassing hygiene erodes the audit unity from M2.2.1 onward).
- **Inline ticket edits**: title, description, priority, assigned-agent. Edits do NOT create `ticket_transitions` rows (those are reserved for column moves). The spec decides where the audit trail for inline edits lives — `event_outbox` row, dedicated `ticket_edits` audit table, or column-level temporal annotations on `tickets` itself.

### 2. Vault writes

Activates the parked `garrison-dashboard` Machine Identity from M2.3 retro Q4. All vault writes go through the dashboard's machine identity — the supervisor's read-only ML is not used for any write path.

- **Secret CRUD**: create (name, value, environment, path, provenance, rotation_cadence), edit with diff view, delete with "this secret is used by roles X, Y, Z" warning. Secret values are never returned to the client unrequested; reveal is an explicit, audited action with a confirmation friction.
- **Role-to-secret grant editing**: add and remove rows in `agent_role_secrets`. Includes "this grant change affects role X — N agent instances of that role currently running may not see the new state until next spawn" guidance copy.
- **Rotation initiation**: dispatch through Infisical's rotation API for supported backends (per threat model §5 list); paste-new-value flow for unsupported types. Updates `secret_metadata.last_rotated_at`. Lives both inline on the secret view AND on a dedicated rotation dashboard (the threat model §6 Q6 default).
- **Environment / path tree management**: create, rename, move paths. Path conventions follow threat model Rule 4's provenance prefixing.
- **Operator-facing error UI**: every Infisical failure mode the supervisor enumerates (`vault_unavailable`, `vault_auth_expired`, `vault_permission_denied`, `vault_rate_limited`, `vault_secret_not_found`) gets an operator-facing message with a documented next-step. Vault-write-specific failures (rotation-failed, grant-conflict, secret-in-use-cannot-delete, path-already-exists) are added on top.

Secret values flow through the dashboard process at write time only — they are never persisted to the dashboard's Postgres tables, never logged through `slog` or any equivalent, and never sent to the client outside an explicit reveal action. The M2.3 vault rules continue to bind: Rule 1 (secrets never enter agent prompts), Rule 2 (per-role scoping), Rule 3 (vault opaque to agents), Rule 6 (audit everything, log no values) all apply to the dashboard's write paths the same way they apply to the supervisor's read paths.

### 3. Agent settings editor

Edits to existing agents only. **Hiring (creating new agents) stays at M7** per AGENTS.md §M7, alongside the skill library and SkillHub integration. M4 does not edit hiring queues, propose hires, or create agent rows.

- **Editable fields**: agent.md (markdown body), model (haiku / sonnet / opus, with the M3 polish-round model-tier chip vocabulary as the in-UI display), concurrency cap, listens_for (channel patterns), skills (slug array drawn from `.claude/skills/`).
- **Hot-reload semantics**: the supervisor reads agent config at spawn time (per the M2.3 spawn ordering). Edits to a running agent do not affect already-spawned subprocesses; the spec defines exactly when an edit takes effect (next spawn) and how the dashboard communicates this to the operator (banner copy, "N instances currently running with prior config" indicator).
- **agent.md secret-leak validation**: M2.3's Rule 1 leak-scan runs at supervisor spawn time. The dashboard must run an equivalent pre-save check — saving an agent.md that contains a secret value verbatim would let the bad agent.md persist until the next spawn fails. The spec decides whether to call into a shared validation library, replicate the check in TS, or proxy to the supervisor.

### 4. M3-retro carryovers (3)

Smaller cross-cutting items the M3 retro tagged for M4. Listed separately from the mutation surfaces because they're carryover work, not new operator-facing surfaces.

- **Drizzle CLI / production migration story** (M3 retro Q1): per CL-004, drizzle-kit is not in the runtime image. Reaffirm the "ops shell" pattern or revisit. M4's mutations exercise the dashboard-owned schema for the first time (better-auth + operator_invites already exist; the spec may add operator-action audit tables here).
- **`next start` → standalone harness migration** (M3 retro Q2): test harness uses `bun run start` which warns about `output: 'standalone'`; production runs `node server.js` against the standalone bundle directly. Move the harness to standalone for closer production parity, or document why the warning is acceptable.
- **`suspected_secret_emitted` pattern-category surfacing** (M3 retro Q3): the supervisor scanner emits 10 pattern labels (sk-, xoxb-, AKIA, PEM, GitHub PAT/App/User/Server/Refresh, bearer-shape) per AGENTS.md §M2.3. The hygiene table currently shows only "secret-shape." A schema extension surfaces the matched category to the dashboard. The spec picks the schema shape (new column on the relevant transition row vs. labels table joined in vs. JSONB on a per-transition metadata column).

### Cross-cutting: authorization, audit, mutation event shape, regression coverage

- **Authorization**: any logged-in operator (better-auth session valid) can perform all mutations. No per-action gating. No approval flows. Single operator pool. Auth boundary is the better-auth session; mutations beyond an authenticated session always succeed if the underlying SQL or vault call succeeds.
- **Audit**: mutations land in the **same activity stream** as M3's read events. The mechanism is open — extending the supervisor's `event_outbox` to include dashboard-originating events, a new `dashboard_events` table that the activity feed merges, or a hybrid. The spec picks the mechanism. The constraint is "every mutation appears in the activity feed without a separate viewer."
- **Mutation rollback**: nothing is implemented today (operator confirmed). Rollback semantics are an open question for the spec, not a precondition. The default lean is "explicit confirm friction on destructive actions, no automatic rollback; multi-step mutations (e.g., rotate = generate + write + revoke old) document partial-failure recovery in the operator-facing error UI." The spec commits to specifics.
- **Concurrent edit detection**: two operators editing the same agent.md, secret, or ticket at once. The single-operator-pool framing makes this rare but not impossible (M3 already supports multiple operator accounts via invites). The spec decides between optimistic-locking with conflict resolution UI, last-write-wins, or session-level edit locks.
- **M3 regression coverage**: every M3 read surface must continue to render correctly under M4's data shapes. The acceptance suite extends the M3 golden-path Playwright spec rather than replacing it; new M4 mutations are tested in addition to, not instead of, the existing read flows.

### Cross-cutting: visual language, layout, deps

- **Visual language**: the M3 polish round produced a small set of named patterns (M3 retro §"What got established as reusable design language" — type scale, columnTone(), card pattern, status chips, day-separator strip, Sparkline / ConcurrencyBar primitives, @panel intercept, "Open full →" escape, slide-in animations honoring `prefers-reduced-motion`). M4 reuses these without re-debating; new mutation-specific primitives (mutation confirm dialog, destructive-action friction pattern, diff view for secret edits) extend the language consistently.
- **Layout**: existing sidebar + topbar shell from M3. M4 adds new routes under existing sections (`/tickets/new`, `/agents/[slug]/edit`, vault sub-views gain create/edit/delete entry points) rather than introducing a new shell.
- **Drizzle ownership**: schema extensions for M4 (mutation audit tables if added, `suspected_secret_emitted` pattern column or labels table) follow the M3 partition: supervisor-owned schema introspected via `drizzle:pull`, dashboard-owned schema hand-written in `drizzle/schema.dashboard.ts`. Goose remains the sole migrator.
- **Deps**: dashboard-side dep additions follow the same justification discipline M3 captured in its retro. The spec should aim for zero new direct deps; any addition gets a one-paragraph justification in the M4 retro.

---

## Out of scope for M4 (explicit deferrals)

### Deferred to M5 (CEO chat — read-only)

- CEO chat surface (the `screen-ceo.jsx` mock)
- Conversation panel, tool-call traces, thread history
- CEO read-only Q&A against the palace and Postgres state

### Deferred to M6 (CEO ticket decomposition + hygiene workflow)

- CEO writes tickets from conversation
- Hygiene → palace-wing jump-and-backfill workflow (operator confirmed M4 does NOT pull this forward)
- Aggregate cost view across tickets and agents
- Rate-limit back-off acting on `rate_limit_event`

### Deferred to M7 (hiring + skills)

- Hiring queue / proposal review (`screen-hiring.jsx` mock)
- Creating new agents in the dashboard — the agent settings editor in M4 edits existing agents only
- Skill library — installed + skills.sh browse + SkillHub integration

### Deferred to M8

- Agent-spawned tickets, cross-department dependencies, MCP-server registry (MCPJungle target candidate)

### Deferred entirely (not banded to a milestone in this context)

- **Supervisor pg_notify channel extension** for the four scaffolded-but-disabled activity-feed chips (`ticket.commented`, `agent.spawned`, `agent.completed`, `hygiene.flagged`). Operator confirmed this is out of M4. Lands in a future milestone TBD; the placeholder chips on `/activity` continue to render at 0.5 opacity with the existing tooltip.

### Explicitly out of scope for M4 entirely (not deferrals, non-goals)

- **Fixing the cost-telemetry blind spot** — surfaced in M3, fix is the supervisor signal-handling change tracked at `docs/issues/cost-telemetry-blind-spot.md`.
- **Fixing workspace-sandbox escape** — surfaced in M3, fix lands as Docker-per-agent post-M3 tracked at `docs/issues/agent-workspace-sandboxing.md`.
- **Multi-user / multi-tenant**. Operator-only single pool continues from M3.
- **Per-action permissions, approval flows, dual-control on mutations**. Any logged-in operator does all mutations; this is a stated decision, not an emergent property.
- **Approval flows on destructive actions beyond confirmation friction**. Confirm-with-typed-name on delete-secret is in scope; multi-operator-approval is not.
- **Theme customization beyond the M3 dark + light pair**. M4 adds no themes.
- **Additional locales beyond English**. The i18n machinery from M3 continues to gate every user-facing string; only English is populated.
- **Mobile-first / sub-768px**. Responsive ≥768px continues from M3.
- **Replacing or revising any sealed M2-arc surface** — supervisor spawn semantics, finalize tool schema, M2.3 vault rules 1–6, `garrison_agent_*` roles, MemPalace MCP wiring are read-only inputs to M4. M4's vault writes go through the parked `garrison-dashboard` ML and do not modify the supervisor's read-only path.

---

## Binding inputs

Read each before writing the spec. Annotations name the load-bearing decision each input carries.

| Input | What it binds |
|---|---|
| `ARCHITECTURE.md` §M4 (line 572) | The four-surface framing of M4 (tickets, kanban, agent configs, "everything daily"). M4 in this context lands three of those surfaces; ARCHITECTURE may need a follow-up edit if the fourth (everything daily) is read more broadly than this context scopes. |
| `ARCHITECTURE.md` §Dashboard surfaces (line 672) | Dashboard surface 9 (vault) carries an "M2.3 + M4" tag — M4 ships the write half of that surface. |
| `AGENTS.md` §M2.3 | Vault rules 1–4 continue to bind on the dashboard write path; the `vaultlog` analyzer pattern extends to TS where applicable. The seven-step spawn ordering is supervisor-side and not modified by M4. |
| `AGENTS.md` §M3 | Visual / interaction patterns from M3's polish round; the locked-deps soft rule on the dashboard side; the dashboard-scoped role discipline. |
| `AGENTS.md` §Scope discipline | Out-of-scope list is binding; M4 must not pull forward CEO chat, hiring, skills, MCP registry, or fixes for the two surfaced-but-not-fixed issues. |
| `AGENTS.md` §What agents should not do | Concurrency rule 8 applies to any new server actions or supervisor-side dashboard helpers. |
| `RATIONALE.md` §3 | MemPalace as source of truth for memory — vault writes do not get mirrored into the palace; ticket/agent edits may, the spec decides. |
| `RATIONALE.md` §4 | Postgres as state store — every M4 mutation lands in Postgres with a defined audit row; no in-memory-only state. |
| `RATIONALE.md` §5 | Soft gates: hygiene continues to be reviewed weekly; M4 mutations don't introduce hard gates. |
| `RATIONALE.md` §7 | Hygiene-warning surface remains a primary product surface — M4 must not regress it. |
| `RATIONALE.md` §8 | The dashboard *is* the operator console; M4 is the milestone where it becomes a write console. |
| `specs/_context/m2.3-context.md` §"Deferred to M4 (dashboard write)" | The exact vault write surface list (CRUD, grants, rotation, env/path, error UI) and the rule that the parked `garrison-dashboard` ML activates here. |
| `specs/_context/m3-context.md` §"Deferred to M4 (dashboard mutations)" | The four mutation surfaces ARCHITECTURE names; this context scopes three of them (vault, tickets+kanban, agents) and pushes the fourth (hygiene backfill) to M6 per operator confirmation. |
| `docs/security/vault-threat-model.md` (post-amendment) | Rules 1–7 continue to bind. The amendment moves rotation UI into M4's banding; the pre-amendment state is internally inconsistent (see §"Scope deviation"). |
| `docs/retros/m3.md` §"What got fixed in the polish round" | Reusable design language M4 surfaces inherit; the production-vs-test runtime diverged-state to revisit (next start vs standalone). |
| `docs/retros/m3.md` §"Open questions deferred to the next milestone" | Three M4-tagged carryovers (Drizzle CLI in production image, next start migration, suspected_secret_emitted category surfacing). |
| `docs/retros/m2-3.md` Q4 | The parked `garrison-dashboard` Machine Identity activates in M4 for vault writes. |

If any of these are missing or unreadable when the spec is written, stop and surface the gap. Do not proceed with a partial input set. The threat-model amendment is a precondition for `/speckit.specify`, not a parallel task.

---

## Open questions the spec must resolve

The context bounds these — it does not decide them.

### Authorization details (the headline is settled, the consequences are not)

The headline — any logged-in operator does all mutations — is fixed. The consequences the spec resolves:

- **Confirmation friction**: which actions require typed-name confirmation (delete secret, delete grant, delete agent.md effectively via field clears), which require single-click confirm, which proceed inline. The default lean is destructive vault and agent operations get typed-name confirm; ticket operations get single-click.
- **Session expiry mid-mutation**: a long edit session (large agent.md write) crossing better-auth session expiry. Save-attempt-401-redirect-to-login-then-restore behavior is the spec's call.
- **CSRF posture**: better-auth provides CSRF for cookie-bound sessions; server actions vs API routes have different shapes. The spec confirms the chosen mechanism per route.

### Mutation event mechanism

How mutations get into M3's activity feed is open. Three shapes:

- **(a) Extend `event_outbox`**: dashboard mutations write rows to the existing supervisor-owned `event_outbox`. Single source of truth; cross-process write semantics need care (the supervisor currently owns this table).
- **(b) Dashboard-side `dashboard_events` table**: parallel events table the dashboard owns; activity feed merges both streams. Two writers, two clients, two streams unioned client-side.
- **(c) Hybrid**: ticket/agent edits land in `event_outbox` (since they're already supervisor-domain entities); vault edits land in `vault_access_log` extended with `outcome` values that include write actions; or a separate `vault_mutations` table.

The spec picks one. The constraint is "every mutation appears in the activity feed."

### Drag-to-move transition path

- **Same finalize_ticket-mediated path** agents use: every operator drag emits a transition with `hygiene_status` evaluated. Pro: audit unity, hygiene catches operator mistakes the same way it catches agent mistakes. Con: hygiene_status was sized for agent runs and may misfire on operator transitions (no agent_instance_id, no expected MemPalace writes).
- **Separate operator transition path**: operator drags bypass hygiene checks entirely. Pro: hygiene stays clean. Con: operator transitions don't appear in hygiene metrics, breaking the "every transition is reviewable" property M2.2.x established.
- **Hybrid**: operator transitions emit a `ticket_transitions` row with `hygiene_status='operator_initiated'` (new value), shown distinctly in the hygiene table.

### Inline-edit audit trail

Three shapes for ticket inline edits (title/description/priority/assigned-agent that don't move columns):

- **`event_outbox` row per edit** — consistent with mutation event mechanism if (a) is chosen.
- **Dedicated `ticket_edits` audit table** — column-level temporal records.
- **Append-only ticket history via column-level change tracking on `tickets`** — JSONB or temporal columns.

### Vault write audit shape

`vault_access_log` was sized for read access (Rule 6). Write actions need an audit shape. Options:

- **Extend `vault_access_log.outcome`** with write-specific values (`secret_created`, `secret_edited`, `secret_deleted`, `grant_added`, `grant_removed`, `rotation_initiated`, `rotation_completed`, `rotation_failed`, `value_revealed`).
- **Separate `vault_mutations` table** — distinct shape for distinct semantics.

The threat model Rule 6 — "audit everything, log no values" — binds either choice. The `vaultlog` go vet analyzer applies on the supervisor side; the dashboard equivalent (TS-side discipline plus a CI check) needs to be defined.

### Agent.md hot-reload semantics

The supervisor reads agent config at spawn time. Edits to a running agent's config:

- **Take effect at next spawn only** (default lean). Banner: "N instances currently running with the prior config; the new config takes effect on the next spawn."
- **Force respawn of running instances** — high blast radius, likely out of scope but the spec should explicitly confirm.

The leak-scan-equivalent for the dashboard's agent.md save is also open: shared library, replicated TS check, or supervisor-proxy check.

### Secret reveal flow

Operator clicks "show value" on a secret. Options:

- **Inline reveal-on-click** with auto-hide after N seconds, audited.
- **Modal reveal** with a confirm step, audited.
- **Copy-to-clipboard without ever rendering the value to the DOM** — hardest to leak, but the operator may need to *read* a value sometimes.

Constraint: every reveal action is audited (Rule 6 extension).

### Rotation UX surface

Threat model §6 Q6 default was "both inline on the secret view AND a dedicated rotation dashboard." The spec confirms this default or reduces to one surface.

For Infisical-supported rotation backends the call goes through Infisical's API; for unsupported types the operator pastes a new value. The spec defines:

- The supported-vs-unsupported determination at the secret level (`rotation_provider` column? heuristic from path prefix? Infisical metadata read?).
- The paste-new-value flow's confirm friction.
- Rotation-initiated event emission to the activity feed.

### Path / environment tree management

Operator-facing UI for creating, renaming, moving paths in Infisical. The spec defines:

- Naming validation (path conventions per threat model Rule 4 — `/<customer_id>/operator/...`, `/<customer_id>/oauth/...`, etc.).
- Rename / move semantics: do these go through Infisical's API directly, or does Garrison batch-update `secret_metadata`?
- Confirmation flow for moves that affect multiple secrets.

### Suspected-secret pattern-category schema

Three shapes for surfacing the M2.3 scanner's 10 pattern labels in the hygiene table:

- **New column on the relevant transition row** — simple, denormalized.
- **`suspected_secret_pattern_match` labels table** joined in — normalized, supports multiple matches per row.
- **JSONB on a per-transition metadata column** — flexible, lossy on filtering.

### Drizzle CLI / production migration

Reaffirm the M3 default (drizzle-kit not in runtime image, ops shell runs migrations) or revisit. M4 is the first milestone where the dashboard owns a non-trivial mutation schema; the migration ergonomics matter operationally now.

### `next start` → standalone harness

Migrate the test harness to the standalone runtime (`node server.js`) for closer production parity, or document why the warning is acceptable.

### Pagination and filtering on mutation surfaces

Every list view that gains mutation entry points (vault list with create button, agents registry with edit, hygiene table with category filter) needs a pagination / filter story consistent with M3's pattern. The spec confirms "follow M3" or names the deviation.

### Concurrent edit detection

Two operators editing the same entity. The spec picks: optimistic-locking with conflict UI, last-write-wins, or session-level edit locks. M3 supports multiple operators via invites; M4 is the first milestone where they can collide.

### Rollback semantics for multi-step mutations

Rotate = generate-new + write-to-Infisical + revoke-old. Partial-failure handling:

- **Fail-closed**: failures roll back the whole rotation.
- **Fail-open with hygiene-critical alert**: partial state persists, operator is alerted to reconcile.
- **Per-step retry**: each step has an idempotent retry path the operator can re-trigger.

### Dependency-justification entries

Any dashboard-side dep additions follow the M3 retro discipline. The spec should target zero new direct deps and document any unavoidable additions.

---

## Acceptance criteria framing

The spec writes the full criteria. The framing the spec must hit:

- **Threat-model amendment landed**: `docs/security/vault-threat-model.md` is amended per §"Scope deviation" before `/speckit.specify` runs. Verifiable by file diff.
- **All three mutation surfaces work end-to-end**: ticket-create / kanban-drag / inline-edit / secret-CRUD / grant-edit / rotation-initiate / agent-config-edit each render, accept input, persist to Postgres or Infisical, and emit an event the activity feed renders.
- **Authorization invariant**: every mutation route is gated by a valid better-auth session; no mutation succeeds without one. Verifiable by integration tests against an unauthenticated client and against an expired session.
- **Vault writes go through `garrison-dashboard` ML**: connection-string DSN inspection at runtime confirms the dashboard write path uses the parked ML, not the supervisor's read-only ML. Verifiable by inspecting the dashboard's runtime config and Infisical's machine-identity audit log.
- **Secret values never persist outside Infisical**: dashboard Postgres tables, dashboard logs, and dashboard event streams contain no secret values. Verifiable by post-run grep / log audit and by `vault_access_log` invariants.
- **Reveal actions are audited**: every operator action that surfaces a secret value to the DOM emits an audit row. Verifiable by exercising reveal flows and inspecting the audit table.
- **Mutations appear in the activity feed**: every mutation creates a row that the activity feed renders, with consistent visual treatment (mutation events distinguishable from M3's read events without being visually noisy). Verifiable by Playwright integration test.
- **agent.md leak-scan parity**: saving an agent.md that contains a fetched secret value verbatim is rejected at the dashboard the same way the supervisor rejects it at spawn time. Verifiable by integration test.
- **Hot-reload semantics communicated**: the agent settings editor surfaces "N running instances will not see this change until next spawn" when applicable. Verifiable by Playwright against a seeded running instance.
- **`suspected_secret_emitted` category surfaces**: hygiene table displays the pattern label for each match, not just "secret-shape." Verifiable by seeded fixture.
- **M3 regressions zero**: the M3 golden-path Playwright spec extends rather than replaces; all M3 surfaces continue to render correctly under M4's data shapes.
- **Drizzle CLI / production migration story**: documented, regardless of which option is chosen.
- **`next start` harness migration**: completed or explicitly deferred with rationale in the M4 retro.
- **Dependency justifications**: any dashboard-side dep additions captured in the M4 retro per the M3-established discipline.

---

## What this milestone is NOT

- It is not a CEO-chat milestone (M5).
- It is not a CEO-ticket-decomposition milestone (M6). M4 ticket-create is operator-authored only.
- It is not a hygiene-backfill milestone (M6). The "jump to palace wing" workflow stays at M6 per operator confirmation.
- It is not a hiring milestone (M7). The agent settings editor edits existing agents; it does not propose, approve, or create new agents.
- It is not a skill-browser milestone (M7).
- It is not the supervisor pg_notify channel extension (deferred entirely; the four scaffolded chips on `/activity` stay disabled). M4 does not modify supervisor pg_notify.
- It is not a fix for the cost-telemetry blind spot (separate work item).
- It is not a fix for the workspace-sandbox escape (separate work item).
- It is not a multi-user / multi-tenant product. Multiple operator accounts via M3 invites continue; per-customer isolation does not exist.
- It is not an approval-flow / dual-control milestone. Any logged-in operator does all mutations.
- It is not a redesign milestone. M3's polish-round visual language extends; it is not re-derived.
- It is not multi-tenant or multi-company.
- It is not a mobile-first design exercise.
- It is not a packaging milestone in the broad sense. Production Docker build for the dashboard already exists from M3; M4 may extend it but does not redo orchestration / CI / image-registry tooling.
- It is not a milestone that revises any sealed M2-arc surface. Supervisor spawn, finalize tool, M2.3 vault rules 1–6, `garrison_agent_*` roles, and MemPalace MCP wiring are read-only inputs to M4.

---

## Spec-kit flow

When M4 specification opens:

1. **Step 0 (precondition)**: amend `docs/security/vault-threat-model.md` per §"Scope deviation from committed doc". Commit before `/speckit.specify` runs. 15-20 minute doc edit.
2. **Step 0b (precondition)**: cut the M4 branch via `/speckit-git-feature` (or equivalent `git checkout -b 009-m4-...`). Do not let M4 work land on the M3 branch.
3. `/garrison-specify m4` against this context.
4. `/speckit.clarify` — resolve the open questions above before planning. The mutation event mechanism, drag-to-move transition path, vault write audit shape, and inline-edit audit trail are interconnected; the clarify pass should treat them together.
5. `/garrison-plan m4` — Next.js 16 server actions / API routes, Drizzle schema extensions, parked `garrison-dashboard` ML activation, audit shape implementation.
6. `/garrison-tasks m4` — break the plan into tasks. Expect a non-trivial scaffolding chunk (audit table or column extensions, mutation-event-mechanism wiring, parked ML activation, secret-value handling discipline) before per-surface mutation work.
7. `/speckit.analyze` — cross-artifact consistency check. Pay attention to invariants the M2.3 threat model bound (Rules 1–6) and to whether the chosen audit / mutation event shapes break M3's activity feed semantics.
8. `/garrison-implement m4`.
9. **Retro** — both markdown (`docs/retros/m4.md`) and MemPalace `wing_company / hall_events` drawer mirror per the M3-onwards retro policy. The threat-model retro questions moved from M2.3 (option-A UI coherence, rotation discipline, audit log usefulness) get answered here.

---

*Context written 2026-04-27 on branch `008-m3-dashboard`. The M4 branch will be cut at `/speckit-git-feature` time, after the threat-model amendment lands.*
