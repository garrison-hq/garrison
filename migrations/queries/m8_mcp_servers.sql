-- M8 MCP-server registry queries.
-- Used by:
--   - supervisor/internal/mcpserverwork/worker.go (read pending rows;
--     UPDATE status on completion).
--   - dashboard/lib/queries/mcpServers.ts (read-side via Drizzle; the
--     dashboard's Server Action writes via Drizzle directly per plan §13).
--   - supervisor/internal/garrisonmutate/register_mcp_server.go (the
--     Server-Action verb writes through Drizzle on the dashboard side,
--     but unit tests for the supervisor-side may use these queries).

-- name: InsertMcpServer :one
-- Server Action write path (M8 alpha calls this from the dashboard's
-- mcpServer.ts via Drizzle directly; this query is for supervisor-side
-- test fixtures + completeness). Row lands with status='pending'; the
-- INSERT trigger emits the pg_notify; the worker picks it up.
INSERT INTO mcp_servers (
    customer_slug, name, transport, url, bearer_token_path, registered_by
) VALUES (
    sqlc.arg(customer_slug),
    sqlc.arg(name),
    sqlc.arg(transport),
    sqlc.arg(url),
    sqlc.arg(bearer_token_path),
    sqlc.arg(registered_by)
)
RETURNING id, created_at, status;

-- name: UpdateMcpServerStatus :exec
-- Worker write path. On MCPJungle API success: status='registered',
-- registered_at=NOW(). On failure: status='failed' + failure_reason. The
-- worker also writes one chat_mutation_audit row with verb=
-- 'register_mcp_server' + outcome='success'|'failed' (FR-306).
UPDATE mcp_servers
   SET status = sqlc.arg(status),
       failure_reason = sqlc.arg(failure_reason),
       registered_at = CASE WHEN sqlc.arg(status) = 'registered' THEN NOW() ELSE registered_at END,
       updated_at = NOW()
 WHERE id = sqlc.arg(id);

-- name: GetMcpServerByID :one
SELECT id, customer_slug, name, transport, url, bearer_token_path,
       status, failure_reason, registered_by, registered_at,
       created_at, updated_at
  FROM mcp_servers
 WHERE id = sqlc.arg(id);

-- name: ListMcpServersByCustomer :many
SELECT id, customer_slug, name, transport, url, bearer_token_path,
       status, failure_reason, registered_by, registered_at,
       created_at, updated_at
  FROM mcp_servers
 WHERE customer_slug = sqlc.arg(customer_slug)
 ORDER BY created_at DESC;

-- name: ListPendingMcpServers :many
-- Used by the supervisor's mcpserverwork worker at startup to recover any
-- registration requests that landed while the worker was down. Idempotent
-- — re-enqueues each pending row's MCPJungle API call.
SELECT id, customer_slug, name, transport, url, bearer_token_path
  FROM mcp_servers
 WHERE status = 'pending'
 ORDER BY created_at ASC;
