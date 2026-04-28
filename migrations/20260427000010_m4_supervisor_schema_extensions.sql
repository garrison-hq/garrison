-- M4 — schema extensions for operator dashboard mutations.
--
-- Three additive ALTER TABLE statements. The plan referred to
-- "ALTER TYPE ... ADD VALUE" extensions for hygiene_status and
-- vault_access_log.outcome — but those columns are TEXT in the
-- current schema (M2.1 / M2.3), not Postgres ENUM types. New
-- conventional values for those columns are added at the
-- application layer (Drizzle TS for the dashboard, Go enum-like
-- patterns for the supervisor); no schema migration is needed
-- for them. This migration only carries the three column
-- additions the plan calls for.
--
--   1. ticket_transitions.suspected_secret_pattern_category
--      (FR-115). Lets the supervisor's scanAndRedactPayload
--      record which of the 10 M2.3 patterns matched, so the
--      hygiene table can render category-distinct rows. Nullable
--      because pre-M4 rows have no category populated; the M4
--      hygiene UI renders NULL as 'unknown' (FR-118). No CHECK
--      constraint — pattern set may grow in future supervisor
--      milestones; supervisor scanner is the source of truth
--      (consistent with the M2.1 / M2.3 unconstrained pattern on
--      hygiene_status and outcome).
--
--   2. secret_metadata.rotation_provider (FR-072). Three-value
--      enumeration (infisical_native / manual_paste / not_rotatable)
--      drives the rotation UI dispatch in T009. CHECK constraint
--      added because the value set is finite and stable; the
--      supervisor doesn't write this column today, so a CHECK is
--      safe and gives an early DB-level signal on bad writes.
--      DEFAULT 'manual_paste' is the operator-safe fallback per
--      FR-072 — operator can re-classify via the M4 vault-edit UI
--      after migration applies (per ops-checklist M4 section).
--
--   3. vault_access_log.metadata (FR-013). JSONB column carrying
--      write-specific context (which fields changed, target path,
--      rotation step that failed, etc.). Nullable; only mutation
--      rows populate it. The threat model Rule 6 invariant — never
--      a secret value here — is enforced at the application layer
--      (FR-017 TS-side discipline check; supervisor's vaultlog
--      vet analyzer continues to bind on the Go side).
--
-- Down section reverses cleanly: drop the column / index / CHECK.
-- No enum-rollback complexity because no Postgres ENUM was
-- introduced.

-- +goose Up
ALTER TABLE ticket_transitions
    ADD COLUMN suspected_secret_pattern_category TEXT;

CREATE INDEX idx_ticket_transitions_pattern_category
    ON ticket_transitions(suspected_secret_pattern_category)
    WHERE suspected_secret_pattern_category IS NOT NULL;

ALTER TABLE secret_metadata
    ADD COLUMN rotation_provider TEXT NOT NULL DEFAULT 'manual_paste'
    CHECK (rotation_provider IN ('infisical_native', 'manual_paste', 'not_rotatable'));

ALTER TABLE vault_access_log
    ADD COLUMN metadata JSONB;

-- +goose Down
ALTER TABLE vault_access_log DROP COLUMN metadata;
ALTER TABLE secret_metadata DROP COLUMN rotation_provider;
DROP INDEX IF EXISTS idx_ticket_transitions_pattern_category;
ALTER TABLE ticket_transitions DROP COLUMN suspected_secret_pattern_category;
