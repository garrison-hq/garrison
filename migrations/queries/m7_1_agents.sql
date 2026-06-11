-- M7.1 agent queries.
-- Boot-time container shape reconcile (plan §9): cmd/supervisor lists
-- every agent that owns a per-agent container, builds the desired
-- ContainerSpec for each, and hands the set to ReconcileShape.

-- name: ListAgentsForContainerReconcile :many
-- host_uid IS NOT NULL covers grandfathered AND hired agents — hired
-- agents never receive last_grandfathered_at (analyze C1), but both
-- populations get a host_uid when their container is provisioned.
-- ORDER BY keeps reconcile logs and tests deterministic.
SELECT id, role_slug, host_uid, image_digest
FROM agents
WHERE host_uid IS NOT NULL
ORDER BY role_slug;
