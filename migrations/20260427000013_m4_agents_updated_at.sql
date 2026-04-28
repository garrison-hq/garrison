-- M4 — add agents.updated_at + auto-bump trigger.
--
-- M2.1's agents table shipped without updated_at (M2.3's
-- agent_role_secrets and secret_metadata both have it; the
-- M2.1-era tables don't). M4's agent settings editor (T013)
-- uses optimistic locking via lib/locks/version.ts:checkAndUpdate
-- per FR-101 — that requires updated_at as the version token.
--
-- Approach:
--
--   1. ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now().
--      Existing rows get the migration-time now() value as their
--      initial version token, which is fine — no operator has
--      cached a stale token across this migration boundary.
--
--   2. CREATE FUNCTION + BEFORE UPDATE trigger to bump
--      updated_at on every row UPDATE. Mirrors the
--      lib/locks/version.ts:checkAndUpdate helper which
--      explicitly bumps it via SET updated_at = now() —
--      having a trigger as a backstop ensures any UPDATE
--      from any source (psql, ad-hoc supervisor write,
--      drizzle-kit migration generator's output) keeps
--      the column current.
--
-- The supervisor's existing reads of agents (internal/agents
-- startup-once cache) are unaffected — they don't read
-- updated_at and the trigger is purely additive.
--
-- This migration is paired with T013's agent settings editor.
-- Without it, T013's optimistic-lock check on agent.md edits
-- would fail with "column updated_at does not exist."

-- +goose Up
ALTER TABLE agents
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_agents_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agents_set_updated_at
    BEFORE UPDATE ON agents
    FOR EACH ROW
    EXECUTE FUNCTION bump_agents_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS agents_set_updated_at ON agents;
DROP FUNCTION IF EXISTS bump_agents_updated_at();
ALTER TABLE agents DROP COLUMN updated_at;
