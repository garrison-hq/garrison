-- M5.3 chat-driven mutation audit queries.
-- Used by supervisor/internal/garrisonmutate/audit.go for write-side
-- and by integration tests + (eventually) operator-side forensic
-- surfaces for read-side. The dashboard's read-only access is via the
-- garrison_dashboard_app role's SELECT grant from the M5.3 migration.

-- name: InsertChatMutationAudit :one
-- Records every garrison-mutate verb call (success or failure). For
-- success rows, runs in the same transaction as the data write. For
-- failure rows, runs in a separate audit-only transaction (the
-- data-side ROLLBACK invalidates a same-tx audit INSERT). args_jsonb
-- captures the full args including operator-typed text passed via the
-- chat (Rule 6 backstop applies to PG_NOTIFY payloads, NOT to this
-- table — chat-content text in args_jsonb is forensically required).
INSERT INTO chat_mutation_audit (
    chat_session_id, chat_message_id, verb, args_jsonb, outcome,
    reversibility_class, affected_resource_id, affected_resource_type
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at;

-- name: GetChatMutationAuditByID :one
SELECT * FROM chat_mutation_audit WHERE id = $1;

-- name: ListChatMutationAuditForSession :many
SELECT * FROM chat_mutation_audit
WHERE chat_session_id = $1
ORDER BY created_at DESC
LIMIT $2;
