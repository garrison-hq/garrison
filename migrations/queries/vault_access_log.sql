-- name: InsertVaultAccessLog :exec
-- FR-412: append-only; no UPDATE / DELETE paths. customer_id carried for
-- future multi-tenant partitioning. No secret-value column by design.
INSERT INTO vault_access_log
    (agent_instance_id, ticket_id, secret_path, customer_id, outcome, timestamp)
VALUES (@agent_instance_id, @ticket_id, @secret_path, @customer_id, @outcome, @timestamp);

-- name: ListVaultAccessByTicket :many
-- Exists specifically for M3's dashboard; M2.3 does not call this query.
SELECT id, agent_instance_id, secret_path, outcome, timestamp
  FROM vault_access_log
 WHERE ticket_id = @ticket_id
 ORDER BY timestamp DESC;
