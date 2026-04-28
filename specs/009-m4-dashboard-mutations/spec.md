# Feature specification: M4 — operator dashboard mutations

**Feature branch**: `009-m4-dashboard-mutations`
**Created**: 2026-04-27
**Status**: Draft
**Input**: User description: "M4 — three operator-facing mutation surfaces (ticket mutations, vault writes, agent settings editor) plus three M3-retro carryovers (Drizzle CLI / production migration story, next start → standalone harness migration, suspected_secret_emitted pattern-category schema extension)."

**Binding context**: `specs/_context/m4-context.md` (committed `0c2f760`). The threat model amendment (commit `11b0dfb`) is the precondition for this spec; it landed before drafting.

**Out-of-scope inheritance**: M5 (CEO chat), M6 (CEO ticket decomposition + hygiene backfill workflow), M7 (hiring + skills), M8 (agent-spawned tickets + MCP registry) all stay out per the M4 context. Cost-telemetry blind spot and workspace-sandbox escape stay surfaced-not-fixed (see `docs/issues/`).

---

## Clarifications

### Session 2026-04-27 (five markers resolved)

- **FR-029** (operator drag → supervisor wake-up loop): operator drags emit on the same `work.ticket.transitioned.<dept>.<from>.<to>` channels agent finalize transitions emit on; agents listening on those channels spawn on operator drags as they would on agent finalizes. Event-handling consistency outweighs the rare unwanted-spawn case; per-channel mute or per-drag opt-out is deferred to a future milestone.
- **FR-030** (`operator_initiated` rows in weekly hygiene metrics): excluded by default from "needs-review" aggregate counts. A filter chip surfaces them on demand. Per RATIONALE §5, operator drags are not failure modes; including them in triage backlogs dilutes the signal.
- **FR-031** (inline-edit field set): exhaustive — title, description, priority, assigned-agent. Other `tickets` columns are not inline-editable in M4. Set can grow in M5+ if specific gaps surface.
- **FR-071** (secret-reveal "reason" prompt): no reason prompt. Single-click confirm; audit row captures who / what / when. At solo-operator scale, the operator is the auditor; reason capture is friction without a reader. Revisit if multi-operator audit workflows make it useful.
- **FR-100** (supervisor `internal/agents` cache invalidation): supervisor adds a `pg_notify`-driven cache invalidator listening on a new `agents.changed` channel. Dashboard emits `pg_notify('agents.changed', <role_slug>)` on every successful `agents` write. Preserves M2.1's startup cache invariant for the no-edits common case; wiring is one LISTEN goroutine + a cache.Reset() callback. The new channel is the only supervisor-pg_notify-set extension in M4; supervisor-emitted channels (the 4 disabled activity-feed chips) remain unchanged.

---

## User scenarios & testing *(mandatory)*

### User story 1 — operator manages vault secrets, grants, and rotation through the dashboard (priority: P1)

The operator opens the dashboard, navigates to `/vault`, and performs the full set of vault write operations without leaving the dashboard or running `psql` / `infisical` CLI commands. They can create a new secret with a path under the operator-provenance prefix, edit an existing secret's metadata or value with a diff view of what's changing, delete a secret with a warning naming the agent roles it would orphan, add and remove grant rows in `agent_role_secrets`, initiate rotation through Infisical's API for supported backends or paste a new value for unsupported backends, manage the path tree (create / rename / move), reveal a secret's value through a deliberate modal action with auto-hide, and read a unified audit history of every read and write event against the secret.

**Why this priority**: M4's headline. The M2.3 → M3 → M4 banding deferred all vault write surfaces specifically to this milestone. The parked `garrison-dashboard` Machine Identity activates here. The threat-model amendment (Rule 7, §5 milestone tags, §7 retro questions) was specifically to clear the way for this story. Of the three mutation surfaces, this one closes the deliberate scope deviation that's been carried since M2.3 shipped.

**Independent test**: A clean operator session can complete the create → edit → grant → rotate → reveal → audit flow against a real Infisical instance without operator intervention beyond clicking and typing in the dashboard. Verified end-to-end by a Playwright integration suite seeded with a starter secret and one agent role.

**Acceptance scenarios**:

1. **Given** a logged-in operator session and a fresh Infisical instance, **when** the operator creates a secret at `/<customer_id>/operator/test_api_key` with value `sk-example`, **then** the secret persists in Infisical, `secret_metadata` carries the new row with `provenance='operator_entered'`, a `vault_access_log` row records `outcome='secret_created'` with no value in any column, and the activity feed renders the create event within one poll cycle.
2. **Given** an existing secret and an existing agent role with no grant, **when** the operator adds the grant `(role_slug='engineer', env_var_name='TEST_API_KEY', secret_path='/<customer_id>/operator/test_api_key')` through the matrix view, **then** `agent_role_secrets` carries the new row, the `rebuild_secret_metadata_role_slugs` trigger updates the denorm column, a `vault_access_log` row records `outcome='grant_added'`, and the next agent spawn for `role_slug='engineer'` injects the env var.
3. **Given** a secret with `rotation_provider='infisical_native'`, **when** the operator clicks "rotate now" and confirms, **then** the dashboard calls Infisical's rotation API, `secret_metadata.last_rotated_at` updates inside the same DB transaction as the audit row, the rotation completes or fails atomically, and `vault_access_log` records `outcome='rotation_completed'` or `outcome='rotation_failed'`.
4. **Given** a secret with `rotation_provider='manual_paste'`, **when** the operator pastes a new value into the rotation modal and confirms, **then** the dashboard writes the new value to Infisical, updates `secret_metadata.last_rotated_at`, and records `outcome='rotation_completed'`.
5. **Given** an existing secret, **when** the operator clicks "show value", confirms the reveal modal, and the secret renders, **then** `vault_access_log` records `outcome='value_revealed'`, the value auto-hides 30 seconds after first render, and the operator can copy-to-clipboard within the modal without the value persisting in any DOM node after auto-hide.
6. **Given** an attempted secret deletion, **when** the secret is referenced by one or more rows in `agent_role_secrets`, **then** the dashboard surfaces the affected role slugs and requires typed-name confirmation before proceeding.
7. **Given** a rotation that partially fails (Infisical API succeeds but the local audit write fails), **then** the dashboard surfaces a hygiene-critical alert naming the partial state, leaves the new value in Infisical, and offers a per-step retry path for the operator to drive recovery.

---

### User story 2 — operator creates and acts on tickets directly in the dashboard (priority: P2)

The operator opens a department's Kanban view, creates a new ticket via a form (operator-authored, not CEO-decomposed), drags existing tickets between columns to record state transitions, and edits ticket metadata (title, description, priority, assigned agent) inline without creating a transition row. Every state-changing action lands in the activity feed alongside agent-driven events, and inline edits land with field-level diffs in the audit stream.

**Why this priority**: Daily operator ergonomics. The frequency of ticket mutations dwarfs the frequency of vault writes; once vault writes ship (P1), ticket mutations become the most-used dashboard surface. P2 because it depends on no parked infrastructure and can ship in parallel with P1's complexity.

**Independent test**: A clean operator session can create a ticket, drag it across all four Kanban columns, edit each editable field inline, and observe every action in the activity feed within one poll cycle. Verified by Playwright against the seeded dev stack.

**Acceptance scenarios**:

1. **Given** the engineering Kanban view, **when** the operator opens the create-ticket form, fills title / description / priority / target-column, and submits, **then** a new row appears in `tickets`, an `event_outbox` row records the create event, the activity feed renders it, and the ticket appears in the source column without a page reload.
2. **Given** an existing ticket in `in_dev`, **when** the operator drags it to `in_review`, **then** a `ticket_transitions` row records the move with `hygiene_status='operator_initiated'` and `agent_instance_id=NULL`, an `event_outbox` row fires, the activity feed renders the transition with operator-initiated visual treatment distinct from agent finalize transitions, and the hygiene table categorizes the row separately from failure modes.
3. **Given** an existing ticket open in the detail panel, **when** the operator edits the title from "X" to "Y", **then** the dashboard writes the field-level diff to an `event_outbox` row with `before='X'` / `after='Y'`, the `tickets` row updates, the activity feed renders an "edited title" line, and no `ticket_transitions` row is created.
4. **Given** two operator sessions both viewing the same ticket detail, **when** both submit inline edits to the same field, **then** the later write overwrites (last-write-wins), no conflict UI is shown, and both writes appear in the audit stream so the divergence is reconstructible.
5. **Given** a ticket inline edit submitted by a session whose better-auth cookie has expired mid-form, **when** the save attempt receives a 401, **then** the dashboard preserves the form state client-side, surfaces a "session expired — log in to retry" toast, and on re-auth restores the form so the operator can resubmit without re-typing.

---

### User story 3 — operator edits existing agent configurations through the dashboard (priority: P3)

The operator opens `/agents`, clicks an existing agent, and edits the agent.md body, model tier (haiku / sonnet / opus), concurrency cap, listens_for channel patterns, and skills slug array. The dashboard rejects saves whose agent.md contains a fetched secret value verbatim (Rule 1 leak-scan parity at the dashboard's save path). Saved edits take effect at the next spawn for the role; the dashboard surfaces a "N instances currently running with prior config" indicator until those instances exit.

**Why this priority**: Less frequent than ticket or vault mutations, lower urgency. The current operator workaround (edit agent rows via `psql`) is tolerable in the short term; this milestone closes the gap without blocking other work.

**Independent test**: A clean operator session can open an existing agent, edit each editable field, save, and see the edit reflected on the next spawn for that role. Verified by integration test that triggers a spawn after an edit and asserts the new config takes effect.

**Acceptance scenarios**:

1. **Given** an existing agent `engineer`, **when** the operator opens the agent editor and submits a valid agent.md change, **then** the `agents` row updates, an `event_outbox` row records the edit with field-level diff, the activity feed renders it, and a banner indicates "N running instances with prior config — change takes effect on next spawn for this role."
2. **Given** an existing agent and a vault grant for `(engineer, STRIPE_KEY, /…)` with a fetched value `sk_live_...`, **when** the operator submits an agent.md that contains the literal value `sk_live_...`, **then** the dashboard rejects the save with an operator-facing error naming the matching pattern, the agent.md is not persisted, and no `event_outbox` row fires.
3. **Given** two operator sessions both opening the same agent.md, **when** both submit edits, **then** the second save fails with a conflict UI showing the diff between the operator's draft and the latest saved version, and the operator chooses to overwrite, merge, or discard.

---

### User story 4 — hygiene table surfaces the matched suspected-secret pattern category (priority: P3)

When the M2.3 finalize-path scanner emits a `suspected_secret_emitted` row, the M3 hygiene table currently shows only "secret-shape." M4 surfaces the matched pattern label (one of the 10 M2.3 categories: `sk-prefix`, `xoxb`, `aws-akia`, `pem-header`, `github-pat`, `github-app`, `github-user`, `github-server`, `github-refresh`, `bearer-shape`) so the operator can triage without opening logs.

**Why this priority**: M3-retro carryover. Small surface, narrow scope, but operator-facing. P3 alongside US3 because it's an additive improvement to an existing M3 surface.

**Independent test**: Seed a `ticket_transitions` row with `hygiene_status='suspected_secret_emitted'` and the new pattern-category column populated; the hygiene table renders the category and supports filtering by it.

**Acceptance scenarios**:

1. **Given** a `ticket_transitions` row with `hygiene_status='suspected_secret_emitted'` and `suspected_secret_pattern_category='sk-prefix'`, **when** the hygiene table renders, **then** the row shows the category label distinctly from other failure-mode rows.
2. **Given** the hygiene table with rows in multiple pattern categories, **when** the operator filters by category, **then** only matching rows render.
3. **Given** an existing pre-M4 row (no pattern-category populated), **when** the row renders, **then** the dashboard shows 'unknown' rather than crashing, and the row remains filterable.

---

### Edge cases

- Operator drags a ticket while an agent is mid-finalize on the same ticket: race resolution between the operator's transition write and the supervisor's finalize-path transition write. Resolution: Postgres row locking on `tickets.id`; whichever transaction commits second sees the column moved already and either retries against the new state or surfaces a conflict.
- Operator deletes a secret currently grant-mapped to a running agent's spawned subprocess: the running subprocess already has the value in its env; the deletion only removes the grant for future spawns. Banner copy must communicate this explicitly.
- Operator edits agent.md for a role with running instances: edit takes effect on next spawn only; running instances complete their current ticket with prior config.
- Network partition during a multi-step rotation: per-step idempotent retry path; partial state surfaces in a hygiene-critical alert; operator drives recovery.
- Operator session expires while a vault reveal modal is open with the value visible: auto-hide fires regardless of session state; on the next operator action, 401 redirects to login.
- Two operators redeem invites simultaneously, then both immediately attempt secret edits: both have valid sessions; first-write-wins on the schema layer; the optimistic-locking conflict UI catches the race.
- Operator pastes a malformed value into the rotation modal (e.g., truncated PEM): Infisical's value validation rejects; dashboard surfaces the rejection with the operator-facing error vocabulary; no audit row written for the failed write.
- Operator creates a secret at a path that exists in Infisical but is missing from `secret_metadata` (manual Infisical edit between Garrison sync passes): dashboard detects the divergence, surfaces a "metadata desync" alert, and offers a re-sync action.
- Operator creates a ticket and drags it through multiple columns rapidly: each transition is its own `ticket_transitions` row; the activity feed renders them in order without coalescing.
- Operator initiates rotation, the API call succeeds, but the local DB transaction fails: the new value is in Infisical, the old metadata persists locally; `outcome='rotation_failed'` records the divergence; operator drives recovery from the alert.

---

## Requirements *(mandatory)*

### Functional requirements

#### Authorization and session management (FR-001 through FR-009)

- **FR-001**: Every mutation route MUST require a valid better-auth session. Unauthenticated requests redirect to login; expired sessions return 401.
- **FR-002**: Any operator with a valid session MUST be authorized to perform every mutation in scope. No per-action permissions, no per-role gating, no approval flows, no dual-control.
- **FR-003**: Mutations MUST use Next.js server actions where applicable, leveraging better-auth's built-in CSRF protection on cookie-bound sessions. API routes are used only where server actions cannot apply (e.g., long-polling rotation status).
- **FR-004**: Confirmation friction MUST be applied per the following tiers:
  - **Typed-name confirmation**: delete-secret, delete-grant, agent.md "clear all" (operator types the entity name to confirm).
  - **Single-click confirmation**: rotation initiation, agent settings save when running instances would be affected, multi-secret path moves, ticket-create form submission.
  - **Inline (no friction)**: inline ticket edits (title / description / priority / assigned-agent), drag-to-move on Kanban, grant *additions* (additive, not destructive).
- **FR-005**: When better-auth session expires mid-mutation, the dashboard MUST preserve form state client-side, surface a "session expired — log in to retry" toast, and on re-authentication restore the form so the operator can resubmit without re-typing. No server-side draft persistence.
- **FR-006**: Mutation routes MUST NOT bypass the dashboard's better-auth middleware via direct Postgres or Infisical access from the operator's browser. All mutations flow through dashboard server-side code.
- **FR-007**: The dashboard MUST NOT expose Infisical's web UI to operators directly. Per threat model §5 (option A), all vault interaction routes through Garrison's UI.
- **FR-008**: Multi-operator concurrent sessions remain valid (per M3 invite redemption); M4 introduces no per-session locks at the auth layer.
- **FR-009**: Server actions and API routes that perform mutations MUST be tested against an unauthenticated client and an expired session in the integration suite.

#### Mutation event audit and activity feed (FR-010 through FR-024)

- **FR-010**: Every mutation MUST appear in the activity feed without a separate viewer. M3's existing two-source merge (LISTEN + `event_outbox` poll) MUST extend to render mutation events.
- **FR-011**: Ticket and agent mutations MUST write rows to `event_outbox`. The `event_outbox` table becomes a shared write surface between the supervisor and the dashboard; the spec respects existing supervisor write semantics (sequence, transactional bracketing) and adds the dashboard as a second writer with the same discipline.
- **FR-012**: Vault mutations MUST write rows to `vault_access_log` with extended `outcome` enum values:
  - `secret_created` — new row in Infisical + `secret_metadata`.
  - `secret_edited` — value or metadata changed for an existing secret.
  - `secret_deleted` — secret removed from Infisical and `secret_metadata`.
  - `grant_added` — new row in `agent_role_secrets`.
  - `grant_removed` — row deleted from `agent_role_secrets`.
  - `rotation_initiated` — rotation started but not yet confirmed by Infisical.
  - `rotation_completed` — rotation succeeded end-to-end (new value in Infisical, metadata updated).
  - `rotation_failed` — rotation failed at any step; the row records the step that failed.
  - `value_revealed` — secret value rendered to the DOM through a deliberate operator action.
- **FR-013**: `vault_access_log` MUST gain a `metadata` JSONB column carrying write-specific context (which fields changed, target path, rotation step that failed, etc.). The column MUST NOT contain secret values; the threat model Rule 6 invariant continues to bind, with the dashboard's TS-side leak-scan discipline (FR-018) enforcing it.
- **FR-014**: `event_outbox` payloads for ticket and agent mutations MUST include a field-level diff (`{before: <prior value>, after: <new value>}` per changed field). For ticket inline edits this is the only audit surface; no separate `ticket_edits` table is created.
- **FR-015**: Mutation writes MUST emit `pg_notify` on a dedicated channel pattern so the M3 activity feed sees them in real-time, not only on the next poll cycle. The exact channel naming follows M3's `work.ticket.*` pattern; vault mutations use a parallel `work.vault.*` pattern. The supervisor's pg_notify channel set is NOT modified by M4.
- **FR-016**: The activity feed MUST render mutation events with visual treatment distinct from agent-driven events but consistent with M3's polish-round visual language (status dots, columnTone, model chips).
- **FR-017**: The dashboard MUST run a TypeScript-side equivalent of the supervisor's `tools/vaultlog` discipline: code review and a CI-time check reject mutation paths that pass secret values to logger functions, console output, or any persistence layer outside Infisical.
- **FR-018**: Secret values MUST NEVER persist to dashboard-owned Postgres tables, dashboard log streams, dashboard event payloads, or any client-side storage other than the temporarily-rendered DOM during an active reveal modal.
- **FR-019**: The activity feed catch-up query (M3's `lib/queries/activityCatchup.ts`) MUST extend to merge mutation events from `event_outbox` and write events from `vault_access_log` without losing the chronological ordering established in M3.
- **FR-020**: Every mutation event row MUST be auditable by inspection: the operator (or future investigator) can answer "who did what when" by reading audit rows alone, without joining against ephemeral state.
- **FR-021**: The activity feed MUST NOT silently drop mutation events. Backpressure is handled the same way M3 handled it (M3 retro §"What got fixed"); volume from operator mutations is bounded by operator concurrency (small).
- **FR-022**: Failed mutations (validation errors, conflict UI rejections, network failures) MUST NOT write event_outbox or vault_access_log rows. Only successfully committed mutations land in the audit stream.
- **FR-023**: Reveal events (`outcome='value_revealed'`) MUST NOT include any portion of the secret value in the audit row's `metadata` column or anywhere else.
- **FR-024**: The dashboard MUST NOT expose mutation event payloads to clients in a form that includes secret values; client-side activity feed rendering of vault events shows the action and the secret path, never the value.

#### Ticket mutations (FR-025 through FR-049) — US2

- **FR-025**: The dashboard MUST provide a create-ticket form accessible from the department Kanban view. Fields: title (required), description (required, multi-line), priority (enum), target department (FK), target column (typically the source column for the department's Kanban; the form prefills the source column).
- **FR-026**: Submitting the create-ticket form MUST insert a row in `tickets` with operator-authored provenance and write a corresponding `event_outbox` row.
- **FR-027**: Drag-to-move on the Kanban view MUST write a `ticket_transitions` row. The new row MUST have `hygiene_status='operator_initiated'` (added to the enum vocabulary) and `agent_instance_id=NULL`. Inserting the transition row updates `tickets.column_slug` atomically (single transaction, mirroring M2.2 transition semantics).
- **FR-028**: The hygiene table MUST display `operator_initiated` rows in a category distinct from failure modes (`finalize_never_called`, `suspected_secret_emitted`, sandbox-escape variants). Filter chips MUST allow operator to include / exclude operator-initiated transitions.
- **FR-029**: Operator-initiated transitions MUST emit on the same `work.ticket.transitioned.<dept>.<from>.<to>` channel set that agent finalize transitions emit on. Agents listening on those channels spawn on operator drags as they would on agent-driven transitions. The hygiene table distinguishes the two transition origins via the new `hygiene_status='operator_initiated'` value (FR-027); the channel set does not.
- **FR-030**: Operator-initiated transitions MUST be excluded by default from the hygiene table's "needs-review" aggregate counts and weekly hygiene metrics. A filter chip MUST surface them on demand. Per RATIONALE §5, operator drags are not failure modes and including them in triage backlogs dilutes signal.
- **FR-031**: Inline ticket edits MUST support exactly the following fields (set is exhaustive for M4):
  - title
  - description
  - priority
  - assigned-agent

  Other `tickets` columns (system columns, `dept_slug`, `column_slug`, foreign-key columns, plus any `due_date` / `labels` if present in the schema) MUST NOT be inline-editable in M4. The set can grow in a future milestone if specific operator-workflow gaps surface.
- **FR-032**: Inline ticket edits MUST NOT create `ticket_transitions` rows. The transition table is reserved for column moves (whether agent-driven or operator-driven).
- **FR-033**: Inline edits MUST emit `event_outbox` rows with field-level `before` / `after` diffs (FR-014). The activity feed MUST render an "edited <field> from X to Y" line.
- **FR-034**: Inline edits MUST use last-write-wins semantics. No optimistic locking, no conflict UI; concurrent inline edits both succeed in chronological order, both audit rows are preserved, and the divergence is reconstructible from the audit stream.
- **FR-035**: Drag-to-move MUST be visually consistent with M3's Kanban read view — same column tones, same card treatment — extended with a drag-handle affordance and a drop-indicator.
- **FR-036**: When the operator drops a card on the same column it started in (no-op move), no transition row is written and the activity feed records nothing.
- **FR-037**: Drag-to-move MUST be keyboard-accessible per WCAG 2.2: arrow-key navigation through cards within a column, modifier-arrow to move across columns, with visible focus and an announce-region for the move.
- **FR-038**: The create-ticket form MUST validate priority against the existing `priority` enum, target department against existing `departments.slug`, and target column against the department's column set.
- **FR-039**: The create-ticket form MUST run client-side validation (required fields, field length caps) AND server-side validation (FK constraints, enum membership). Client-side validation MUST NOT be load-bearing for safety.
- **FR-040**: Ticket inline-edit form fields MUST support the same i18n and theming behavior as M3 read surfaces. No English-string JSX literals outside the locale catalog.
- **FR-041**: Drag-to-move and inline edits MUST preserve M3's responsive behavior at ≥768px viewports. Mobile / sub-768px is out of scope (continues from M3).
- **FR-042**: Operator drags during a Postgres connection drop MUST fail gracefully — the card snaps back to the source column, the operator sees a "couldn't save" toast, no transition row is written, and the action can be retried.
- **FR-043**: Concurrent drag-to-move and supervisor-side finalize against the same ticket MUST be serialized at the Postgres layer. Whichever transaction commits second observes the moved column and either retries (idempotent) or surfaces a conflict (operator-facing).
- **FR-044**: The Kanban surface MUST poll for external transitions (agent finalize events) at the same cadence M3 established (per M3 retro §"Seven read surfaces"), so an operator's view stays consistent with concurrent agent-driven transitions without manual refresh.
- **FR-045**: When inline edits are saved, the dashboard MUST refresh affected surfaces (ticket detail panel, Kanban card text) optimistically with the new value while the server confirms, and roll back on save failure.
- **FR-046**: Ticket creation, drag-to-move, and inline edits MUST be exercised by the M4 acceptance Playwright suite against a seeded dev stack.
- **FR-047**: Acceptance criteria from M3 (read surface integrity, activity feed correctness, theme parity, responsive behavior) MUST continue to hold under M4's data shapes; the M3 golden-path Playwright spec extends rather than is replaced.
- **FR-048**: Ticket inline edits MUST surface the audit history in the ticket detail panel: a chronologically-ordered list of edits with editor identity, timestamp, field, before, after.
- **FR-049**: Ticket creation MUST allow assigning the new ticket to an agent role at create time (optional). Unassigned tickets get picked up by the supervisor's normal listens_for routing.

#### Vault writes (FR-050 through FR-094) — US1

- **FR-050**: The dashboard MUST activate the parked `garrison-dashboard` Machine Identity from M2.3 retro Q4 for all vault write operations. The supervisor's read-only Machine Identity MUST NOT be used for any dashboard-side write.
- **FR-051**: The dashboard's runtime configuration MUST distinguish between the two Machine Identity credentials by name and never confuse them. Verifiable by inspecting the dashboard's runtime environment and Infisical's machine-identity audit log.
- **FR-052**: The secret CRUD form MUST support fields: name (display), value (write-only, never returned to client unrequested), environment, path, provenance (enum from threat model Rule 4), `rotation_cadence` (interval), `rotation_provider` (enum, FR-072).
- **FR-053**: Secret create MUST validate the path conforms to threat model Rule 4 conventions: structure `/<customer_id>/<provenance>/<remainder>`. The `<provenance>` segment is a dropdown over the Rule 4 enum (`operator`, `oauth`, `bootstrap`, `customer`).
- **FR-054**: Secret create writes to Infisical first; on Infisical success, the dashboard writes the corresponding `secret_metadata` row in the same DB transaction as the `vault_access_log` row.
- **FR-055**: Secret create MUST handle Infisical API failures with operator-facing error vocabulary that maps to the M2.3-enumerated supervisor failure modes (`vault_unavailable`, `vault_auth_expired`, `vault_permission_denied`, `vault_rate_limited`) plus M4-specific write failures (`vault_path_already_exists`, `vault_validation_rejected`).
- **FR-056**: Secret edit MUST present a diff view of changing fields before commit. Value diffs render as "value will change" without showing either old or new value.
- **FR-057**: Secret edit MUST issue a single Infisical update call per save; partial-failure handling per FR-094.
- **FR-058**: Secret delete MUST surface the affected agent roles ("this secret is used by roles X, Y, Z") before requiring typed-name confirmation. The role list comes from `agent_role_secrets` joined to `secret_metadata.allowed_role_slugs`.
- **FR-059**: Secret delete proceeds only after typed-name confirmation. The deletion removes the secret from Infisical and from `secret_metadata` in the same DB transaction as the audit row write.
- **FR-060**: Deleting a secret currently grant-mapped to running agent instances MUST surface a banner explaining that running instances retain the env-var value but future spawns will not see it. The deletion does NOT signal running instances.
- **FR-061**: Grant additions MUST insert rows in `agent_role_secrets` with `granted_by` set to the operator's user ID. The `rebuild_secret_metadata_role_slugs` trigger fires automatically per M2.3.
- **FR-062**: Grant removals MUST delete rows in `agent_role_secrets`. The trigger updates the denorm column.
- **FR-063**: Grant edits MUST be exercised through the matrix view (the M3 access-control matrix) extended with add/remove affordances, not as a separate form.
- **FR-064**: Rotation initiation MUST consult `secret_metadata.rotation_provider` and dispatch through the appropriate code path:
  - `infisical_native`: call Infisical's rotation API; await response; update `secret_metadata.last_rotated_at`.
  - `manual_paste`: open a modal accepting a new value; on confirm, write the new value to Infisical and update `last_rotated_at`.
  - `not_rotatable`: rotation is disabled in the UI; tooltip explains why.
- **FR-065**: Rotation MUST be initiable from two surfaces (per threat model §6 Q6 dual-surface default):
  - Inline on the secret view (per-secret button).
  - The dedicated `/vault/rotation` dashboard listing all stale + soon-stale secrets.
- **FR-066**: The `/vault/rotation` dashboard MUST list secrets where `last_rotated_at + rotation_cadence < now()` (stale) and where `last_rotated_at + rotation_cadence < now() + 7 days` (soon-stale) in two visually-distinct sections.
- **FR-067**: Path / environment tree creation MUST validate the new path's structure against Rule 4 conventions (FR-053).
- **FR-068**: Path / environment tree rename MUST issue a single Infisical move call per affected secret. Multi-secret moves require typed-name confirmation.
- **FR-069**: Path / environment tree move MUST update `secret_metadata.secret_path` for every affected secret in the same DB transaction as the audit rows. Partial Infisical failure surfaces per FR-094.
- **FR-070**: The secret reveal modal MUST require a single-click confirmation before rendering the value. Auto-hide fires 30 seconds after first render. Copy-to-clipboard is available within the modal; closing the modal removes the value from the DOM immediately.
- **FR-071**: The reveal modal MUST NOT require a reason or justification field. Single-click confirmation is sufficient; the audit row (FR-012 `value_revealed` outcome) captures who, what, and when. A reason field can be added in a future milestone if a multi-operator audit workflow makes it useful.
- **FR-072**: `secret_metadata` MUST gain a `rotation_provider` column (enum: `infisical_native`, `manual_paste`, `not_rotatable`). The column is populated at secret creation based on path-prefix conventions (operator-entered defaults to `manual_paste`; OAuth secrets default to `infisical_native` if Infisical supports the backend) plus an Infisical metadata read.
- **FR-073**: The vault audit log viewer (M3's read surface) MUST extend to render the new write `outcome` values with consistent visual treatment.
- **FR-074**: The vault audit log viewer MUST allow filtering by outcome category (read events vs. write events) and by individual outcome values.
- **FR-075**: Operator-facing error UI MUST present each Infisical failure mode with: operator-readable message, suggested next action, link to the relevant `docs/ops-checklist.md` section if applicable. The vocabulary covers:
  - M2.3-enumerated read failures: `vault_unavailable`, `vault_auth_expired`, `vault_permission_denied`, `vault_rate_limited`, `vault_secret_not_found`.
  - M4-specific write failures: `vault_path_already_exists`, `vault_validation_rejected`, `vault_rotation_unsupported`, `vault_grant_conflict`, `vault_secret_in_use_cannot_delete`.
- **FR-076**: Vault-mutation `pg_notify` events MUST fire on a dedicated channel pattern (`work.vault.*`) so the activity feed sees them in real-time. The supervisor does not subscribe to these channels (vault writes are dashboard-domain).
- **FR-077**: All vault write surfaces MUST use Next.js server actions; no client-side direct calls to Infisical.
- **FR-078**: Vault values MUST NEVER appear in dashboard server logs, dashboard client logs, browser local/session storage, or any persistence layer other than Infisical itself.
- **FR-079**: The dashboard MUST scan saved agent.md content (FR-088) at agent settings save time using a TypeScript-side replication of `internal/vault/scan.go` patterns. The supervisor's spawn-time scan continues to run as defense-in-depth.
- **FR-080**: When secret creation fails after Infisical writes the value but before the local DB transaction commits, the dashboard MUST surface a hygiene-critical alert naming the divergence ("secret exists in Infisical, missing from `secret_metadata`") and offer a re-sync action.
- **FR-081**: Secret value validation (e.g., PEM format check, base64 check) MUST be optional and configurable per `provenance` category. The default is no validation; operator can opt-in per secret.
- **FR-082**: The matrix view (agent-role-to-secret) MUST update reactively when grants change. M3's read view extends with optimistic-locking-protected edit affordances.
- **FR-083**: Grant adds and removes MUST emit single audit rows per row changed. Bulk operations are not in scope for M4.
- **FR-084**: Optimistic locking on secret edits MUST use `secret_metadata.updated_at` (or equivalent versioning column) as the conflict-detection key. Concurrent edits surface a conflict UI with the diff between drafts.
- **FR-085**: The path tree view MUST visualize secrets organized by path prefix, with create / rename / move affordances at each level.
- **FR-086**: The path tree view MUST be searchable by path substring, by provenance, and by allowed role slug (joined through `agent_role_secrets`).
- **FR-087**: The vault read role (`garrison_dashboard_ro`) from M3 MUST NOT be granted any write privilege. Vault writes flow exclusively through the dashboard's runtime logic that uses the `garrison-dashboard` Machine Identity for Infisical writes and (separately) the dashboard app role for `vault_access_log` and `secret_metadata` writes.
- **FR-088**: agent.md leak-scan parity (FR-079) — saving an agent.md whose body contains the verbatim value of any secret currently fetchable for any agent role MUST reject the save with an operator-facing error naming the matching pattern. The scan covers all 10 M2.3 patterns (`sk-prefix`, `xoxb`, `aws-akia`, `pem-header`, `github-pat`, `github-app`, `github-user`, `github-server`, `github-refresh`, `bearer-shape`).
- **FR-089**: When a secret is created with `provenance='customer_delegated'`, the `customer_id` field MUST be required and validated against the existing `companies` table.
- **FR-090**: The dashboard MUST treat Infisical's authoritative store as the source of truth for secret existence; `secret_metadata` is a denormalized mirror per threat model Rule 4. Conflicts (Infisical has a secret `secret_metadata` does not) surface as desync alerts.
- **FR-091**: Secret deletion MUST be reversible only from the Infisical side (point-in-time recovery per threat model §5). The dashboard does not offer undo.
- **FR-092**: Vault write rate-limits at the Infisical layer MUST surface to the operator via `vault_rate_limited` error UI; the dashboard does not retry-with-backoff (consistent with M2.3 fail-fast policy per supervisor M2.3 retro Q5).
- **FR-093**: All vault write routes MUST be exercised by the M4 acceptance Playwright suite against a real Infisical container.
- **FR-094**: Multi-step mutations (rotation = generate-new + write-to-Infisical + revoke-old; multi-secret path move) MUST handle partial failures with: fail-open semantics (partial state persists); a hygiene-critical alert naming exactly which step failed and what state Infisical is in; per-step idempotent retry path; operator-driven recovery (no automatic rollback).

#### Agent settings editor (FR-095 through FR-114) — US3

- **FR-095**: The dashboard MUST provide an agent settings editor accessible from the agents registry. The editor edits existing agents only; creating new agents (hiring) stays at M7.
- **FR-096**: The agent settings editor MUST support editing: agent.md (markdown body, multi-line), model tier (haiku / sonnet / opus, displayed via the M3 polish-round model-tier chip vocabulary), concurrency cap (positive integer), listens_for (channel pattern array), skills (slug array drawn from `.claude/skills/`).
- **FR-097**: Saving an agent.md whose body contains the verbatim value of a fetchable secret for the agent's role MUST reject the save (FR-088).
- **FR-098**: Saved agent edits take effect at the next spawn for the role. Already-spawned subprocesses retain prior config until they exit.
- **FR-099**: The agent settings editor MUST surface a banner indicator: "N instances currently running with prior config — change takes effect on next spawn for this role." The count comes from `agent_instances` filtered to `role_slug` and `exit_reason IS NULL`.
- **FR-100**: The supervisor MUST add a `pg_notify`-driven cache invalidator that listens on a new channel `agents.changed`. The dashboard MUST emit `pg_notify('agents.changed', <role_slug>)` on every successful write to the `agents` table (insert / update / delete). The supervisor's `internal/agents` cache resets the affected role's entry on receipt; subsequent spawns for that role re-read from `agents`. The startup-cache invariant for the no-edits common case is preserved. Wiring is a single LISTEN goroutine on the supervisor side plus a `cache.Reset(roleSlug)` callback; the dashboard's emission piggy-backs on the existing transactional write to `agents` (single SQL transaction, `pg_notify` issued inside it).
- **FR-101**: Concurrent edits to the same agent MUST use optimistic locking. Conflicts surface a UI showing the diff between the operator's draft and the latest saved version; the operator chooses to overwrite, merge manually, or discard.
- **FR-102**: The agent settings editor MUST validate model against the existing model enum.
- **FR-103**: The agent settings editor MUST validate concurrency cap as a positive integer ≤ a configurable max (default 10 per role per M2.1 framing).
- **FR-104**: The agent settings editor MUST validate listens_for entries against a documented channel-pattern syntax (e.g., `work.ticket.created.<dept>` literal or wildcard pattern).
- **FR-105**: The agent settings editor MUST validate skills slugs against the directory listing of `.claude/skills/`. Unknown slugs are rejected.
- **FR-106**: Agent edits emit `event_outbox` rows with field-level diffs. Multi-line edits to agent.md serialize the full before/after so the audit stream supports diff rendering.
- **FR-107**: The activity feed MUST render agent-edit events with a "changed agent.md" or "changed model" or "changed concurrency" line linking to the agent detail.
- **FR-108**: The agent registry surface MUST surface the running-instance count per agent (already in M3) and indicate when an edit was made since the last spawn.
- **FR-109**: Agent edits MUST be exercised by the M4 acceptance Playwright suite, including the leak-scan rejection flow.
- **FR-110**: The agent settings editor MUST honor M3's i18n discipline — no English-string JSX literals outside the locale catalog.
- **FR-111**: The agent settings editor MUST support both dark and light themes per M3 parity.
- **FR-112**: The agent settings editor MUST be responsive at ≥768px viewports per M3 baseline. Larger viewports get a side-by-side preview/edit treatment; smaller fall back to stacked.
- **FR-113**: The agent settings editor MUST NOT support deletion of agents in M4. Deletion is part of agent lifecycle (decommissioning), out of scope.
- **FR-114**: The agent settings editor MUST NOT support creation of agents (hiring stays M7).

#### Suspected-secret pattern category surfacing (FR-115 through FR-119) — US4

- **FR-115**: The supervisor's `internal/finalize/scanAndRedactPayload` MUST be extended to write the matched pattern label to a new column on `ticket_transitions`. The column name is `suspected_secret_pattern_category` (text, nullable, enumerated by the 10 M2.3 categories plus 'unknown').
- **FR-116**: The migration adding `suspected_secret_pattern_category` MUST be a goose-managed SQL migration (not Drizzle-owned), per the M3 split between supervisor-domain and dashboard-domain schema. Drizzle re-introspects after the migration lands.
- **FR-117**: The hygiene table MUST render the pattern category alongside the failure-mode badge. The category is filterable.
- **FR-118**: Existing pre-M4 rows (column NULL) MUST render as 'unknown' rather than a missing-value error. Backfill is not required.
- **FR-119**: The supervisor change MUST be exercised by the M4 acceptance suite (or the supervisor's existing finalize integration suite extended to assert the new column).

#### M3-retro carryovers — Drizzle CLI and `next start` migration (FR-120 through FR-123)

- **FR-120**: The Drizzle CLI MUST stay out of the runtime image (M3 retro Q1 default). Operators continue to run migrations from a separate ops shell. M4 explicitly reaffirms this rather than revisiting.
- **FR-121**: The dashboard's existing `dashboard/Dockerfile` MUST NOT add drizzle-kit to the runtime stage. M4-introduced schema migrations (audit table extensions, optional tables) follow the existing M3 split: supervisor-domain via goose, dashboard-domain via Drizzle from the ops shell.
- **FR-122**: The dashboard's test harness MUST migrate from `next start` to the standalone runtime (`node server.js` against the standalone bundle output) for closer production parity. This is the M3 retro Q2 default revisit.
- **FR-123**: The Playwright acceptance suite MUST run against the standalone harness; integration tests using the harness MUST pass without the `next start` warning that M3 retro flagged.

#### Cross-cutting — visual language, theming, i18n, deps (FR-124 through FR-135)

- **FR-124**: All M4 surfaces MUST reuse M3's polish-round visual language: type scale (10.5px microcopy through 40px KPI), `font-tabular` on numeric columns, `columnTone()` for workflow column palettes, status chip / tag conventions, card pattern (`bg-surface-1 border border-border-1 rounded`), section header rhythm, day-separator strip where applicable.
- **FR-125**: New M4-specific UI primitives (mutation confirm dialog, diff view for secret/agent edits, destructive-action friction pattern, conflict-resolution UI for optimistic locking, reveal modal) MUST extend the language consistently — same density, same color semantics, same animation primitives.
- **FR-126**: Reveal modal animations MUST honor `prefers-reduced-motion: reduce` per M3's CSS animation primitives (`garrison-fade-in`, `garrison-slide-in-right`).
- **FR-127**: All user-facing strings MUST flow through the next-intl locale catalog. No English-string JSX literals outside the catalog. The `zz` stub locale used in M3 acceptance tests continues to verify the locale-swap path.
- **FR-128**: All M4 surfaces MUST render correctly in both dark and light themes per M3 parity.
- **FR-129**: All M4 surfaces MUST be responsive at ≥768px / 1024px / 1280px viewports per M3 baseline. Sub-768px is out of scope.
- **FR-130**: New dashboard-side dependencies MUST target zero net additions. Any unavoidable additions get a one-paragraph justification in the M4 retro per M3 discipline (M3 retro §"Dependencies added outside the locked list").
- **FR-131**: Pagination and filtering on M4-extended list views (vault list with create button, agents registry with edit, hygiene table with pattern-category filter) MUST follow M3's URL-state filter chip + cursor pagination pattern. No new vocabulary.
- **FR-132**: Server-Component vs. Client-Component split per surface MUST follow M3's pattern: Server Components for static reads; Client Components only where mutation state, optimistic updates, or interactivity demand them.
- **FR-133**: M4 surfaces MUST NOT regress M3's accessibility behavior: WCAG 2.2 AA for color contrast, focus management, keyboard navigation. Drag-to-move keyboard accessibility (FR-037) is the new accessibility-critical surface.
- **FR-134**: M3's existing acceptance suite (Playwright golden path) MUST continue to pass under M4's data shapes. M4's acceptance suite extends the M3 suite, not replaces it.
- **FR-135**: The dependency-justification entries for any new direct deps MUST appear in `docs/retros/m4.md` per the dual-deliverable retro policy (markdown + MemPalace mirror) carried forward from M3.

### Key entities

Existing entities that gain new operations or columns:

- **`tickets`** — gains operator-authored creation, drag-to-move state changes (already supported via `ticket_transitions`), and inline field edits. No new columns; existing columns become editable.
- **`ticket_transitions`** — gains:
  - new `hygiene_status` enum value `operator_initiated`
  - new column `suspected_secret_pattern_category` (text, nullable, enumerated)
  - new write path: dashboard-originated transitions on operator drag
- **`agents`** — gains write operations through the dashboard (edit existing rows). Schema unchanged.
- **`agent_role_secrets`** — gains write operations through the dashboard (insert and delete rows for grant editing). Schema unchanged.
- **`secret_metadata`** — gains:
  - new column `rotation_provider` (enum: `infisical_native`, `manual_paste`, `not_rotatable`)
  - new write path: dashboard-originated metadata writes synchronized with Infisical writes
  - existing `rebuild_secret_metadata_role_slugs` trigger continues to fire on grant changes
- **`vault_access_log`** — gains:
  - extended `outcome` enum: 9 new values per FR-012
  - new column `metadata` (JSONB, nullable, no secret values per Rule 6)
  - new write path: dashboard-originated audit rows for write actions
- **`event_outbox`** — gains a second writer (the dashboard) alongside the existing supervisor writer. Schema unchanged.

No new tables introduced. The M4 schema extensions are additive: enum values, columns, no migrations that drop or rename existing structures.

External entities:

- **Infisical secret store** — the source of truth for secret values. M4 activates the parked `garrison-dashboard` Machine Identity for write operations.

---

## Success criteria *(mandatory)*

### Measurable outcomes

- **SC-001**: A clean operator session completes the full vault write golden path (create secret → add grant → rotate → reveal → delete) end-to-end against a real Infisical instance in under 5 minutes of operator wall-clock time, without leaving the dashboard or running CLI commands.
- **SC-002**: The vault write golden path is exercised by a Playwright integration test that runs against a testcontainer-seeded Infisical and Postgres instance and completes in under 90 seconds of CI wall-clock time.
- **SC-003**: The ticket mutation golden path (create → drag through 4 columns → inline edit each editable field → observe in activity feed) completes in under 2 minutes of operator wall-clock time and is exercised by a Playwright integration test.
- **SC-004**: The agent settings editor golden path (open existing agent → edit each editable field → save → observe banner → trigger spawn → verify new config) completes in under 90 seconds of operator wall-clock time and is exercised by a Playwright integration test.
- **SC-005**: Every mutation in scope (FR-012 vault outcomes, FR-014 ticket and agent diffs) appears in the activity feed within one poll cycle (30 seconds) of being committed. Real-time `pg_notify`-driven mutations appear within 2 seconds.
- **SC-006**: Code-level audit shows zero secret values in dashboard server logs, dashboard client logs, browser storage, dashboard Postgres tables, and `vault_access_log.metadata` after exercising the full vault golden path. Audit method: post-test grep across logs + SQL inspection of vault audit rows.
- **SC-007**: Code-level audit shows zero English-string JSX literals outside the locale catalog. Verifiable by extending M3's existing audit pass.
- **SC-008**: M3 regressions are zero. The M3 golden-path Playwright spec passes unchanged under M4's data shapes.
- **SC-009**: All vault write routes use the `garrison-dashboard` Machine Identity. Verifiable by inspecting dashboard runtime configuration and Infisical's machine-identity audit log after a full acceptance run.
- **SC-010**: The threat model amendment (commit `11b0dfb`) is consistent with what M4 ships. Verifiable by re-reading the amended threat model after M4 ships and confirming no new contradictions.
- **SC-011**: All five `[NEEDS CLARIFICATION]` markers (FR-029, FR-030, FR-031, FR-071, FR-100) are resolved by the 2026-04-27 clarification round (see §Clarifications above). The spec carries no unresolved markers entering `/garrison-plan`.
- **SC-012**: Zero new direct dashboard dependencies, OR each new direct dependency carries a one-paragraph justification in `docs/retros/m4.md` per M3 discipline.
- **SC-013**: The `next start` → standalone test harness migration (FR-122) ships in M4. Acceptance: the M4 Playwright suite runs against the standalone runtime and passes without the `next start` warning.
- **SC-014**: The Drizzle CLI stays out of the runtime image (FR-120, FR-121). Acceptance: `dashboard/Dockerfile` carries no `drizzle-kit` install line, and `docs/ops-checklist.md` documents the ops-shell migration procedure for any new M4 dashboard-domain migrations.
- **SC-015**: All 10 suspected-secret pattern categories from M2.3 (`sk-prefix`, `xoxb`, `aws-akia`, `pem-header`, `github-pat`, `github-app`, `github-user`, `github-server`, `github-refresh`, `bearer-shape`) are surfaceable in the hygiene table when populated, and existing pre-M4 rows render as 'unknown' without crashing.
- **SC-016**: The supervisor change for FR-115 (write the matched pattern label) is covered by the supervisor's existing finalize integration suite extended with a new assertion.
- **SC-017**: Every vault, ticket, and agent mutation that succeeds writes exactly one audit row (vault → `vault_access_log`; ticket / agent → `event_outbox`). Failed mutations write zero audit rows. Verifiable by exercising the failure paths in integration tests and asserting audit row counts.
- **SC-018**: Operator session expiry mid-mutation (FR-005) preserves form state and restores it on re-auth. Verifiable by Playwright with cookie expiry simulation.
- **SC-019**: Concurrent edits to the same agent (FR-101) surface the conflict UI; concurrent inline ticket edits (FR-034) both succeed in chronological order. Verifiable by integration test with two simulated operator sessions.
- **SC-020**: The activity feed renders mutation events with visual treatment distinct from agent-driven events (FR-016) without violating M3's polish-round visual language. Verifiable by visual regression test or operator review.
- **SC-021**: The retro lands as both `docs/retros/m4.md` (canonical markdown) and a MemPalace `wing_company / hall_events` drawer mirror per the M3-onwards retro policy. Both are non-optional.

---

## Assumptions

- **M3 is shipped and stable**. The dashboard's M3 surfaces, polish-round visual language, better-auth scaffolding, invite flow, sidebar / topbar shell, theme switcher, i18n catalog, SSE activity feed, soft-poll surfaces, two-Postgres-role split (`garrison_dashboard_app` and `garrison_dashboard_ro`) all continue to function as M3 retro documents.
- **The threat-model amendment landed before this spec**. Commit `11b0dfb` reflects the M2.3/M3/M4 milestone banding and Rule 7's split between rotation data model (M2.3) and rotation UI (M4). The spec is consistent with the amended threat model, not the original 2026-04-22 draft.
- **The parked `garrison-dashboard` Machine Identity is created in Infisical and configured with appropriate write permissions**. M2.3 retro Q4 confirmed the ML is parked; M4 activates it. Operators provision the ML's credentials before deploying M4.
- **Single operator pool continues**. M3's invite-driven multi-account model continues unchanged; M4 introduces no per-operator role distinctions.
- **Better-auth session model is unchanged**. M3's email/password provider with the drizzle adapter continues; M4 adds no new auth providers.
- **The supervisor's *emitted* pg_notify channel set is unchanged in M4**. The 4 disabled activity-feed chips (`ticket.commented`, `agent.spawned`, `agent.completed`, `hygiene.flagged`) remain disabled. Future milestone TBD picks them up. The supervisor *listens* on one new channel introduced by M4 — `agents.changed`, dashboard-emitted, for `internal/agents` cache invalidation per FR-100.
- **The hygiene-backfill workflow stays at M6**. M4 does NOT pull forward the "jump to palace wing" affordance from M3 retro / M3 context.
- **Cost-telemetry blind spot stays surfaced-not-fixed** (`docs/issues/cost-telemetry-blind-spot.md`). M4 inherits M3's caveat-icon treatment without modifying the underlying supervisor signal-handling.
- **Workspace-sandbox escape stays surfaced-not-fixed** (`docs/issues/agent-workspace-sandboxing.md`). M4 displays the failure mode in hygiene + ticket detail per M3; the per-agent Docker container fix is post-M4 work.
- **CEO chat (M5), CEO ticket decomposition + hygiene backfill (M6), hiring + skills (M7), agent-spawned tickets + cross-dept dependencies + MCP registry (M8)** all continue to band as ARCHITECTURE.md describes.
- **M4 introduces no new infrastructure components**. Postgres, Infisical, MemPalace, the supervisor, and the dashboard continue from M3; M4 extends the dashboard process and adds one supervisor-side migration (FR-115).
- **Operators provision the runtime config for the `garrison-dashboard` ML**. The credentials are set via environment variables consistent with M2.3's bootstrap-secret discipline (per threat model §6 Q7 / M2.3 spec resolution); `docs/ops-checklist.md` gains an M4 section documenting the procedure.
