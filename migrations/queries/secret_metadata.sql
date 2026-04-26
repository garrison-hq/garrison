-- name: UpsertSecretMetadata :exec
-- M2.3 operator-driven seeding path (ops-checklist snippets). The trigger on
-- agent_role_secrets keeps allowed_role_slugs in sync automatically.
INSERT INTO secret_metadata
    (secret_path, customer_id, provenance, rotation_cadence, last_rotated_at)
VALUES (@secret_path, @customer_id, @provenance, @rotation_cadence, @last_rotated_at)
ON CONFLICT (secret_path, customer_id) DO UPDATE
    SET provenance       = EXCLUDED.provenance,
        rotation_cadence = EXCLUDED.rotation_cadence,
        last_rotated_at  = EXCLUDED.last_rotated_at,
        updated_at       = now();

-- name: TouchSecretLastAccessed :exec
-- Called by vault.WriteAuditRow on OutcomeGranted to update
-- secret_metadata.last_accessed_at (D3.4 from plan). Same transaction as
-- the vault_access_log INSERT.
UPDATE secret_metadata
   SET last_accessed_at = @last_accessed_at,
       updated_at       = now()
 WHERE secret_path = @secret_path AND customer_id = @customer_id;

-- name: ListStaleSecrets :many
-- Exists for M3 dashboard's "stale secrets" view. M2.3 does not call this.
SELECT secret_path, customer_id, provenance, rotation_cadence,
       last_rotated_at, last_accessed_at, allowed_role_slugs
  FROM secret_metadata
 WHERE last_rotated_at IS NOT NULL
   AND rotation_cadence <> 'never'
   AND (now() - last_rotated_at) > rotation_cadence;
