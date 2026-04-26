-- name: ListGrantsForRole :many
-- Rule 2 per-role grant query (FR-409). Returns zero rows for a role with
-- no grants; callers skip the Infisical fetch in that case.
SELECT env_var_name, secret_path, customer_id
  FROM agent_role_secrets
 WHERE role_slug = @role_slug AND customer_id = @customer_id
 ORDER BY env_var_name;

-- name: InsertGrant :exec
-- Used only by migrations at M2.3; M4 will layer on top. The trigger on
-- agent_role_secrets rebuilds secret_metadata.allowed_role_slugs automatically.
INSERT INTO agent_role_secrets
    (role_slug, secret_path, env_var_name, customer_id, granted_by)
VALUES (@role_slug, @secret_path, @env_var_name, @customer_id, @granted_by);
