-- M10 ingress-connector queries (plan §sqlc).
-- Used by:
--   - supervisor/internal/ingress/ (handler pipeline: delivery insert,
--     ticket insert, delivery backfill, connector status reads).
--
-- All parameters use sqlc.arg(name) exclusively (M7 retro gotcha).
--
-- Note: department-slug → id resolution reuses the existing
-- SelectDepartmentIDBySlug from tickets.sql. Do NOT add a duplicate
-- query here (plan §sqlc / tasks.md T002 completion condition).

-- name: InsertIngressDelivery :one
-- Idempotency row insert (FR-201, plan decision 4). The unique constraint
-- on (connector_id, external_delivery_id) is the dedup signal: a 23505
-- unique-violation means this delivery is already recorded and the handler
-- should abort the ticket-insert path and return 200 with no side effects
-- (FR-202, ErrDuplicateDelivery). ticket_id starts NULL and is backfilled
-- after the ticket INSERT in the same transaction via BackfillIngressDeliveryTicket.
INSERT INTO ingress_deliveries (connector_id, external_delivery_id)
VALUES (
    sqlc.arg(connector_id)::text,
    sqlc.arg(external_delivery_id)::text
)
RETURNING id, connector_id, external_delivery_id, ticket_id, created_at;

-- name: BackfillIngressDeliveryTicket :exec
-- After InsertIngressTicket commits the ticket row, backfill the ticket_id
-- anchor on the delivery record so the two are linked (plan decision 4).
-- Both writes occur in the same transaction.
UPDATE ingress_deliveries
   SET ticket_id = sqlc.arg(ticket_id)
 WHERE id = sqlc.arg(id);

-- name: InsertIngressTicket :one
-- M9 InsertScheduledTicket mirror for ingress-mode firings: dept, rendered
-- objective + acceptance criteria, column_slug='todo', origin='ingress',
-- provenance in metadata. The tickets INSERT trigger emits the outbox row +
-- the existing work.ticket.created.<dept>.todo notify in this same tx
-- (FR-101). No new spawn path — the existing dispatcher picks it up.
-- tickets.origin='ingress' is a new unconstrained-text value; no SQL change
-- required (FR-502, SR10, plan decision 7).
-- The metadata JSONB carries the three M10 provenance keys (FR-500, plan
-- decision 5 / R2): ingress_connector, external_id, external_url.
INSERT INTO tickets (
    department_id, objective, acceptance_criteria, column_slug,
    metadata, origin
) VALUES (
    sqlc.arg(department_id),
    sqlc.arg(objective),
    sqlc.arg(acceptance_criteria),
    'todo',
    jsonb_build_object(
        'ingress_connector', sqlc.arg(ingress_connector)::text,
        'external_id',       sqlc.arg(external_id)::text,
        'external_url',      sqlc.arg(external_url)::text
    ),
    'ingress'
)
RETURNING id, created_at;

-- name: GetConnectorStatus :one
-- Connector-status surface read (FR-702, plan decision 14, resolution R3).
-- Returns the last delivery received timestamp + accepted delivery count
-- from ingress_deliveries (supervisor-writable, dashboard-readable via
-- GRANT SELECT), and the rate-cap breach count from throttle_events of
-- kind='ingress_rate_cap_exceeded' (also dashboard-readable). The
-- bad-signature-rejection count is an in-process atomic counter exposed
-- via GET /ingress/status on the dashboard-api port (not a DB row, per
-- FR-301 + plan R3) — this query covers only the DB-backed signals.
SELECT
    MAX(d.created_at)                                  AS last_delivery_at,
    COUNT(d.id)                                        AS accepted_count,
    COALESCE(
        (SELECT COUNT(*)
           FROM throttle_events t
          WHERE t.kind = 'ingress_rate_cap_exceeded'
            AND (t.payload->>'connector_id')::text = sqlc.arg(connector_id)::text
        ), 0
    )                                                  AS rate_cap_breach_count
  FROM ingress_deliveries d
 WHERE d.connector_id = sqlc.arg(connector_id)::text;
