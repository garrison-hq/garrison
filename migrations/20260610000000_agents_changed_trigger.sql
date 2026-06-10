-- agents.changed DB trigger (FR-014 amendment, 2026-06-10).
--
-- M4's dashboard editAgent Server Action emits
-- pg_notify('agents.changed', role_slug) explicitly, but hire approval
-- can land an agents row from other surfaces too (chat approve_hire
-- verb, dashboard ApproveHire action, operator SQL). The supervisor
-- rebuilds its roster-derived dispatch routes on agents.changed, so
-- the emission must be surface-independent: a row-level trigger.
-- The dashboard's explicit emit becomes a harmless duplicate (the
-- cache reset + route rebuild are idempotent).

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION emit_agents_changed() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('agents.changed', NEW.role_slug);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER agents_changed_emit
  AFTER INSERT OR UPDATE ON agents
  FOR EACH ROW
  EXECUTE FUNCTION emit_agents_changed();

-- +goose Down
DROP TRIGGER IF EXISTS agents_changed_emit ON agents;
DROP FUNCTION IF EXISTS emit_agents_changed();
