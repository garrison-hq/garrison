# `lib/actions/` — Server Actions home

This directory holds the dashboard's mutation server actions for M4. Each
action follows the canonical 5-step transactional flow established in
T003 of `specs/009-m4-dashboard-mutations/plan.md` §"Server action mutation
flow":

1. **Authenticate**: read better-auth session via `lib/auth/session.ts`.
   No session → throw 401 (FR-001).
2. **Authorize**: any-logged-in operator passes (FR-002). No per-action
   gating, no approval flows, no dual-control.
3. **Validate input**:
   - Field-level (required, format, enum membership)
   - Reference integrity (FK-equivalent checks against current state)
4. **Open transaction** (`appDb.transaction(async (tx) => { … })`):
   1. Optimistic lock check (where applicable, FR-101 / FR-084):
      `lib/locks/version.ts:checkAndUpdate`. Stale version →
      `ConflictError(StaleVersion, serverState)`.
   2. Write the mutation (INSERT / UPDATE / DELETE on the target table).
   3. Build the audit row:
      - vault: `vault_access_log` row with extended outcome + JSONB
        metadata (`lib/audit/vaultAccessLog.ts:writeVaultMutationLog`).
      - ticket / agent: `event_outbox` row with field-level diff
        (`lib/audit/eventOutbox.ts:writeMutationEventToOutbox`).
   4. Issue `pg_notify` on the appropriate channel via
      `lib/audit/pgNotify.ts:emitPgNotify`. The notify is enqueued
      for COMMIT time (Phase 0 research item 2 in `plan.md`).
   5. Commit transaction (atomic with the pg_notify per Postgres
      semantics).
5. **Return** the typed result (or throw `VaultError` / `ConflictError`
   as appropriate).

## Threat-model invariants (binding throughout)

- **Rule 1**: agent.md saves run a TS-side leak-scan
  (`lib/vault/leakScan.ts`) before persistence — a save containing a
  fetchable secret value verbatim is rejected.
- **Rule 6**: no audit row carries a secret value. The
  `lib/audit/vaultAccessLog.ts` helper applies a defensive shape-scan
  on the metadata JSONB payload as a runtime backstop; the TS-side
  discipline check (`lib/vault/discipline-check.ts` in T006) is the
  primary boundary at CI time.
- **Failed mutations write zero audit rows** (FR-022). Validation
  errors, conflicts, and Infisical failures abort the transaction
  before any INSERT lands.

## Naming / structure conventions

- One file per domain: `tickets.ts`, `vault.ts`, `agents.ts`. Don't
  mix surfaces.
- Server actions are top-level exports with `'use server'` at the file
  head (or per-export, depending on Next.js 16 conventions chosen at
  T007).
- Action signatures follow `plan.md` §"Concrete interfaces" verbatim;
  return types are typed objects, errors are typed exceptions
  (`ConflictError`, `VaultError`).
- Catalog keys for operator-facing copy live under `errors.<domain>.*`
  in `messages/en.json`.
