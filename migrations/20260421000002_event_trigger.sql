-- emit_ticket_created trigger function + ticket_created_emit trigger,
-- verbatim from specs/_context/m1-context.md §"Data model for M1".
-- The function body uses StatementBegin/End sentinels so goose does not
-- split the plpgsql body on its internal semicolons.

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION emit_ticket_created() RETURNS trigger AS $$
DECLARE
  event_id UUID;
  payload JSONB;
BEGIN
  payload := jsonb_build_object(
    'ticket_id', NEW.id,
    'department_id', NEW.department_id,
    'created_at', NEW.created_at
  );
  INSERT INTO event_outbox (channel, payload)
    VALUES ('work.ticket.created', payload)
    RETURNING id INTO event_id;
  PERFORM pg_notify('work.ticket.created', jsonb_build_object('event_id', event_id)::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER ticket_created_emit
  AFTER INSERT ON tickets
  FOR EACH ROW
  EXECUTE FUNCTION emit_ticket_created();

-- +goose Down
DROP TRIGGER IF EXISTS ticket_created_emit ON tickets;
DROP FUNCTION IF EXISTS emit_ticket_created();
