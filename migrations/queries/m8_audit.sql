-- M8 audit-row queries.
-- Used by supervisor/internal/garrisonmutate (agent-anchored writes) +
-- dashboard /activity surface (agent-anchored filter).

-- name: InsertAgentAnchoredAudit :one
-- Variant of InsertChatMutationAudit accepting agent_instance_id. One and
-- only one of chat_session_id or agent_instance_id should be non-NULL for
-- create_ticket rows (FR-005 — Go-side enforcement; DB CHECK would break
-- M7-era Server Action rows that have both anchors NULL legitimately).
INSERT INTO chat_mutation_audit (
    chat_session_id, chat_message_id, agent_instance_id,
    verb, args_jsonb, outcome,
    reversibility_class, affected_resource_id, affected_resource_type
) VALUES (
    sqlc.arg(chat_session_id),
    sqlc.arg(chat_message_id),
    sqlc.arg(agent_instance_id),
    sqlc.arg(verb),
    sqlc.arg(args_jsonb),
    sqlc.arg(outcome),
    sqlc.arg(reversibility_class),
    sqlc.arg(affected_resource_id),
    sqlc.arg(affected_resource_type)
)
RETURNING id, created_at;

-- name: ListAuditByAgentInstance :many
-- FR-502 read-side: filter audit rows by agent_instance_id. Used by the
-- dashboard /activity?agent_instance_id=<uuid> surface.
SELECT id, chat_session_id, chat_message_id, agent_instance_id,
       verb, args_jsonb, outcome, reversibility_class,
       affected_resource_id, affected_resource_type, retention_class,
       created_at
  FROM chat_mutation_audit
 WHERE agent_instance_id = sqlc.arg(agent_instance_id)
 ORDER BY created_at DESC
 LIMIT sqlc.arg(limit_n);

-- name: ResolveAgentAuditAnchors :one
-- SC-012 forensic reconstruction: given an agent-anchored audit row, return
-- the originating ticket + agent role within one query.
SELECT
    cma.id AS audit_id,
    cma.verb,
    cma.outcome,
    cma.created_at AS audit_created_at,
    ai.id AS agent_instance_id,
    ai.ticket_id,
    ai.role_slug,
    a.id AS agent_id
  FROM chat_mutation_audit cma
  JOIN agent_instances ai ON ai.id = cma.agent_instance_id
  LEFT JOIN agents a ON a.department_id = ai.department_id
                    AND a.role_slug = ai.role_slug
 WHERE cma.id = sqlc.arg(audit_id);
