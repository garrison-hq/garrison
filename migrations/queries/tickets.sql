-- name: GetTicketByID :one
SELECT * FROM tickets WHERE id = $1;

-- name: InsertTicket :one
INSERT INTO tickets (department_id, objective)
VALUES ($1, $2)
RETURNING *;

-- name: UpdateTicketColumnSlug :exec
UPDATE tickets SET column_slug = $2 WHERE id = $1;

-- name: InsertTicketTransition :one
INSERT INTO ticket_transitions (ticket_id, from_column, to_column, triggered_by_agent_instance_id)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: SelectTicketObjective :one
-- M2.2.1: read the ticket's objective text for the FR-263 diary
-- serialization — the supervisor's atomic writer prepends this prose
-- to the drawer body so mempalace_search queries keyed on objective
-- text return the drawer (semantic-similarity fails on raw UUIDs per
-- M2.2 live-run finding).
SELECT objective FROM tickets WHERE id = $1;

-- name: InsertChatTicket :one
-- M5.3 garrison-mutate.create_ticket. Writes a ticket row with
-- origin='ceo_chat' and the FK back to the originating chat session
-- so forensic queries can join chat_session→tickets without
-- needing chat_mutation_audit. Caller is the garrison-mutate verb's
-- transaction; the audit row INSERT runs in the same tx.
-- column_slug + metadata use NULLABLE sentinel inputs so the verb can
-- pass NULL to mean "use default"; the verb resolves "todo" / "{}"
-- before calling rather than relying on COALESCE-on-cast SQL that
-- confuses sqlc's parameter-name inference.
-- M6 T010: parent_ticket_id passes through as a NULLABLE sentinel; verb
-- handler validates same-department + non-closed parent BEFORE calling
-- this query (cross-table CHECK can't enforce both; verb error message
-- has to be operator-readable anyway).
INSERT INTO tickets (
    department_id, objective, acceptance_criteria, column_slug,
    metadata, origin, created_via_chat_session_id, parent_ticket_id
)
VALUES (@department_id, @objective, @acceptance_criteria, @column_slug, @metadata, 'ceo_chat', @created_via_chat_session_id, @parent_ticket_id)
RETURNING id, department_id, objective, created_at, column_slug, acceptance_criteria, metadata, origin, created_via_chat_session_id, parent_ticket_id;

-- name: SelectDepartmentIDBySlug :one
-- garrison-mutate verbs accept department_slug from the chat assistant;
-- internal queries use department_id (UUID). Resolves the slug→id
-- before INSERT/UPDATE; returns no rows if the slug doesn't exist.
SELECT id FROM departments WHERE slug = $1;

-- name: LockTicketForUpdate :one
-- M5.3 concurrent-mutation conflict resolution per chat-threat-model.md
-- Rule 4 + plan §4.3. SELECT ... FOR UPDATE NOWAIT returns immediately
-- with PostgreSQL error 55P03 (lock_not_available) if another tx holds
-- the row lock. The verb maps that error to ErrTicketStateChanged.
SELECT id FROM tickets WHERE id = $1 FOR UPDATE NOWAIT;

-- name: UpdateTicketEditableFields :exec
-- garrison-mutate.edit_ticket: partial update of operator-editable
-- fields. The verb resolves COALESCE semantics on the Go side: it
-- reads the before-state, merges with the args, and passes the
-- final values here. metadata is JSONB; the Go side handles merge.
UPDATE tickets
   SET objective = @objective,
       acceptance_criteria = @acceptance_criteria,
       metadata = @metadata
 WHERE id = @id;

-- name: GetTicketColumnAndDept :one
-- Pre-transition snapshot used by transition_ticket: returns the
-- current column_slug + department_id so the verb can compute
-- from_column for the audit row + ticket_transitions row.
SELECT column_slug, department_id FROM tickets WHERE id = $1;
