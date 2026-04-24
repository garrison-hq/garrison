-- M2.3 migration: Infisical secret vault — three tables + denorm-sync trigger.
--
--   agent_role_secrets  (FR-411): per-role grants; authoritative Rule 2 policy
--   vault_access_log    (FR-412): audit record, no secret values
--   secret_metadata     (FR-413): denormalized metadata for M3 read surfaces
--
-- Introduces Garrison's first Postgres trigger function for denorm sync of
-- secret_metadata.allowed_role_slugs (FR-413a, spec Session 2026-04-24).
-- Follows M2.2's emit_ticket_transitioned trigger style with goose
-- StatementBegin/End delimiters for the multi-statement function body.
--
-- NO seed rows inserted; zero grants at M2.3 ship (FR-414).
-- garrison_agent_ro receives NO grants on vault tables — the vault is opaque
-- to agent-facing DB connections per threat model Rule 3 / Rule 6.

-- +goose Up

-- Section 1 — agent_role_secrets (FR-411).
-- PK on (role_slug, env_var_name, customer_id) enforces the uniqueness
-- constraint: a role cannot bind the same env var name twice.
CREATE TABLE agent_role_secrets (
    role_slug        TEXT        NOT NULL,
    secret_path      TEXT        NOT NULL,
    env_var_name     TEXT        NOT NULL,
    customer_id      UUID        NOT NULL,
    granted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by       TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (role_slug, env_var_name, customer_id)
    -- No FK on role_slug: agents.role_slug is unique per-department only
    -- (UNIQUE(department_id, role_slug)), not globally. Integrity is
    -- maintained at the application layer via ListGrantsForRole.
);
CREATE INDEX idx_agent_role_secrets_secret_path
    ON agent_role_secrets (secret_path, customer_id);

-- Section 2 — vault_access_log (FR-412). No secret-value column by design.
-- Append-only; never updated or deleted.
CREATE TABLE vault_access_log (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_instance_id   UUID        NOT NULL REFERENCES agent_instances(id),
    ticket_id           UUID        NULL     REFERENCES tickets(id),
    secret_path         TEXT        NOT NULL,
    customer_id         UUID        NOT NULL,
    outcome             TEXT        NOT NULL,
    timestamp           TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_vault_access_log_agent_instance ON vault_access_log (agent_instance_id);
CREATE INDEX idx_vault_access_log_ticket
    ON vault_access_log (ticket_id)
    WHERE ticket_id IS NOT NULL;

-- Section 3 — secret_metadata (FR-413).
-- Composite PK (secret_path, customer_id). rotation_cadence defaults to
-- '90 days' per spec Q7 and FR-413 ("allowed: 30d, 90d, never").
CREATE TABLE secret_metadata (
    secret_path        TEXT        NOT NULL,
    customer_id        UUID        NOT NULL,
    provenance         TEXT        NOT NULL,
    rotation_cadence   INTERVAL    NOT NULL DEFAULT '90 days',
    last_rotated_at    TIMESTAMPTZ NULL,
    last_accessed_at   TIMESTAMPTZ NULL,
    allowed_role_slugs TEXT[]      NOT NULL DEFAULT '{}',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (secret_path, customer_id)
);

-- Section 4 — denorm sync trigger (FR-413a).
-- Rebuilds secret_metadata.allowed_role_slugs whenever agent_role_secrets
-- is modified. Handles INSERT, UPDATE (including path/customer_id renames
-- that affect both OLD and NEW tuples), and DELETE.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rebuild_secret_metadata_role_slugs()
RETURNS TRIGGER AS $$
BEGIN
    IF (TG_OP = 'INSERT') THEN
        UPDATE secret_metadata
           SET allowed_role_slugs = (
               SELECT COALESCE(array_agg(DISTINCT role_slug ORDER BY role_slug), '{}')
                 FROM agent_role_secrets
                WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id
           ),
               updated_at = now()
         WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id;
        RETURN NEW;

    ELSIF (TG_OP = 'UPDATE') THEN
        -- Rebuild the NEW tuple.
        UPDATE secret_metadata
           SET allowed_role_slugs = (
               SELECT COALESCE(array_agg(DISTINCT role_slug ORDER BY role_slug), '{}')
                 FROM agent_role_secrets
                WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id
           ),
               updated_at = now()
         WHERE secret_path = NEW.secret_path AND customer_id = NEW.customer_id;
        -- If the (secret_path, customer_id) key changed, also rebuild OLD tuple.
        IF (OLD.secret_path, OLD.customer_id) IS DISTINCT FROM (NEW.secret_path, NEW.customer_id) THEN
            UPDATE secret_metadata
               SET allowed_role_slugs = (
                   SELECT COALESCE(array_agg(DISTINCT role_slug ORDER BY role_slug), '{}')
                     FROM agent_role_secrets
                    WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id
               ),
                   updated_at = now()
             WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id;
        END IF;
        RETURN NEW;

    ELSIF (TG_OP = 'DELETE') THEN
        UPDATE secret_metadata
           SET allowed_role_slugs = (
               SELECT COALESCE(array_agg(DISTINCT role_slug ORDER BY role_slug), '{}')
                 FROM agent_role_secrets
                WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id
           ),
               updated_at = now()
         WHERE secret_path = OLD.secret_path AND customer_id = OLD.customer_id;
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER agent_role_secrets_allowed_role_slugs_sync
AFTER INSERT OR UPDATE OR DELETE ON agent_role_secrets
FOR EACH ROW EXECUTE FUNCTION rebuild_secret_metadata_role_slugs();

-- Section 5 — access policy.
-- garrison_agent_ro receives NO grants on vault tables.
-- The vault is opaque to agent-facing DB connections per threat model Rule 3
-- and Rule 6. M3's dashboard will introduce a dedicated role when it needs
-- read access to these tables.

-- +goose Down
DROP TRIGGER IF EXISTS agent_role_secrets_allowed_role_slugs_sync ON agent_role_secrets;
DROP FUNCTION IF EXISTS rebuild_secret_metadata_role_slugs();
DROP INDEX IF EXISTS idx_vault_access_log_ticket;
DROP INDEX IF EXISTS idx_vault_access_log_agent_instance;
DROP INDEX IF EXISTS idx_agent_role_secrets_secret_path;
DROP TABLE IF EXISTS secret_metadata;
DROP TABLE IF EXISTS vault_access_log;
DROP TABLE IF EXISTS agent_role_secrets;
