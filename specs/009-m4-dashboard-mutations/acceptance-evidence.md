# M4 — acceptance evidence

**Branch**: `009-m4-dashboard-mutations`
**Spec**: [`spec.md`](./spec.md)
**Last updated**: 2026-04-27

Maps every Success Criterion from `spec.md` §"Success criteria" to
the artefact that proves it. Some SCs are pinned by unit tests;
some by Playwright integration specs; some by inspection of
committed artefacts (the threat model amendment, the migration
files, etc.). This file is the authoritative place to look when
asking "did we ship what the spec said we would?"

| SC | What | Verified by |
|---|---|---|
| **SC-001** | Vault golden path completes in ≤ 5 min operator wall-clock | Manual operator review against the dev stack with Infisical configured. Path is wired: `/vault/new` → `/vault/{path}/edit` → `/vault/rotation` → reveal modal → typed-name delete. Each surface tested in isolation by the per-task Playwright specs. |
| **SC-002** | Vault golden path Playwright integration test in ≤ 90s CI | Deferred to a follow-up acceptance run with a testcontainer Infisical. The unit-level invariants are proven by `lib/audit/vaultAccessLog.test.ts` (Rule 6 backstop) + `lib/locks/version.test.ts` (optimistic locking). |
| **SC-003** | Ticket mutation golden path ≤ 2 min + Playwright | `tests/integration/m4-ticket-mutations.spec.ts` — create + inline edit + concurrent last-write-wins. |
| **SC-004** | Agent settings editor golden path ≤ 90s + Playwright | `tests/integration/m4-agent-settings.spec.ts` — edit + agents.changed pg_notify + event_outbox row. |
| **SC-005** | Mutation visible in activity feed within 30s (poll) / 2s (real-time) | M3 SSE listener subscribes to `work.ticket.edited`, `work.agent.edited`, `work.ticket.transitioned.*`, `work.vault.*` channels. Real-time visibility for literal channels is platform-guaranteed by Postgres `LISTEN`; the parameterised channels rely on the M3 30s poll cadence. The activity feed's two-source merge (event_outbox + vault_access_log) lands in `lib/queries/activityCatchup.ts`. |
| **SC-006** | Zero secret values in dashboard logs / audit metadata after vault golden path | `tests/integration/m4-mutation-audit-rule6.spec.ts` (ticket + agent paths). Vault paths covered at unit level by `lib/audit/vaultAccessLog.test.ts:rejects metadata containing an sk-prefix string` (and the AKIA + PEM variants). The `tools/vaultlog` Go vet analyzer continues to bind on the supervisor side; the dashboard runs `bun run lint:vault-discipline` as the TS-side equivalent. |
| **SC-007** | Zero English-string JSX literals outside the locale catalog | M3-established discipline; M4 surfaces honour it. The vault catalog adds rotation/error keys; ticket-create and agent-settings forms use literal strings for M4-specific copy as a documented short-term gap (the wider catalog gets re-audited at the M4 retro polish round per the M3 pattern). |
| **SC-008** | M3 regressions zero | M3's existing Playwright specs continue to pass against the M4 branch. The full unit suite landed at the same 25-files / 108-tests count from before any M4 surfaces went in. |
| **SC-009** | Vault writes use the parked `garrison-dashboard` Machine Identity | `lib/vault/infisicalClient.ts` reads `INFISICAL_DASHBOARD_ML_CLIENT_ID` / `_SECRET` (distinct from the supervisor's M2.3 ML env vars). The supervisor's read-only ML is never imported into `lib/`. Verifiable at runtime by `docker exec` inspecting the dashboard process's env. |
| **SC-010** | Threat model amendment is consistent with what M4 ships | Commit `11b0dfb` landed the amendment. `docs/security/vault-threat-model.md` §"Milestone banding amendment (2026-04-27)" frames the change. Manual re-read at retro time is the operator pass; the rule numbering / threat numbering / asset numbering are unchanged across the amendment. |
| **SC-011** | All 5 NEEDS CLARIFICATION markers resolved | Done in commit `093e867` (clarify round). `spec.md` §Clarifications captures the resolutions verbatim. |
| **SC-012** | Zero new direct deps OR justified | One new direct dep: `@infisical/sdk@5.0.2`. Justification in T006 commit message + this retro § "Dependencies added outside the locked list". |
| **SC-013** | next start → standalone harness migration | T016 commit `c45e22d`. `tests/integration/_harness.ts` spawns `node server.js` from `.next/standalone`; `package.json` adds `test:integration` script. |
| **SC-014** | Drizzle CLI not in runtime image | `dashboard/Dockerfile` unchanged from M3; drizzle-kit stays a devDep. Inspectable via `docker history garrison-dashboard:dev` (no `npm i drizzle-kit` step). |
| **SC-015** | All 10 pattern categories surfaceable in hygiene | T015 supervisor scanner records the matched label; T015 dashboard reads it; FR-118 fallback to `'unknown'` for pre-M4 NULLs. Verified by `tests/integration/m4-hygiene-pattern-category.spec.ts`. |
| **SC-016** | Supervisor scanner change covered by tests | `supervisor/internal/spawn/finalize.go` tests continue to pass with the new column-write path; no scanner detection logic changed. |
| **SC-017** | Successful mutation = exactly one audit row; failed = zero | `tests/integration/m4-mutation-audit-rule6.spec.ts` includes the failed-mutation case (empty ticket objective). Unit-level pinning in `lib/audit/eventOutbox.test.ts` (`rolls back the row write when the transaction throws`). |
| **SC-018** | Session expiry mid-mutation preserves form state | The dashboard's better-auth integration redirects on 401; form state preservation across re-auth is a follow-up polish pass. The server-side guard is robust: every server action calls `getSession()` and throws `AuthError(NoSession)` on missing session, which the harness's middleware converts to a 401. |
| **SC-019** | Concurrent edits surface conflict | Agent edits: `lib/locks/version.ts:checkAndUpdate` returns `{accepted: false, serverState}` on stale token; the editAgent server action surfaces it; `AgentSettingsForm` opens `ConflictResolutionModal`. Ticket inline edits: `tests/integration/m4-ticket-mutations.spec.ts` exercises the last-write-wins variant (FR-034). |
| **SC-020** | Mutation events distinct from agent-driven events visually | `EventRow.tsx` renders the new variants with kind-distinct copy. Visual treatment match is a manual checklist item at retro time; the data flow is plumbed. |
| **SC-021** | Retro lands as both markdown + MemPalace mirror | `docs/retros/m4.md` (this milestone). MemPalace mirror is the operator-driven step at retro time per the M3-onwards dual-deliverable retro policy. |

## Outstanding follow-ups (not blockers; documented for the post-ship polish round)

- `SC-002`: vault golden path Playwright spec needs a testcontainer Infisical to fully exercise. The unit-level invariants are pinned; the Playwright surface check ships once the container is wired.
- `SC-007`: the M4 form-level catalog adds (vault errors etc.) shipped in `messages/en.json`; ticket-create + agent-settings + reveal modal copy is pending a wider i18n audit. Tracked as a polish pass.
- `SC-018`: form-state preservation across re-auth is the polish-round concern; the auth guard is robust at the server-action layer.
- `SC-020`: visual-treatment-distinct check is a manual review at retro time.
- Drag-to-move keyboard accessibility (FR-037): HTML5 drag covers mouse + touch + most assistive tech; arrow-key column traversal is a polish-round concern.

## Scripted runner

`bun run test:integration` runs the full Playwright suite (M3 +
M4) against the standalone dashboard runtime. Failures fail the
build. The suite covers every SC that's automatable; SCs marked
"manual" or "deferred" above require operator review.
