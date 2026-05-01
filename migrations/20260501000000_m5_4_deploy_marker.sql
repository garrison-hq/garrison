-- M5.4 deploy waypoint marker.
--
-- M5.4 ships the "WHAT THE CEO KNOWS" knowledge-base pane (see
-- specs/013-m5-4-knows-pane/spec.md). The migration footprint is
-- intentionally minimal:
--
-- 1. Company.md storage moves from the (documented-but-never-landed)
--    `companies.company_md TEXT NOT NULL` column to a MinIO bucket.
--    The column never existed in any prior migration, so there's no
--    DDL to remove. This migration's presence in goose_db_version
--    serves as a deploy waypoint correlated with the ARCHITECTURE.md
--    amendment landing (see specs/013-m5-4-knows-pane/spec.md ARCH-1).
--
-- 2. The supervisor's auth middleware reads the dashboard's better-auth
--    `sessions` table for cookie validation (see plan §"Dashboard ↔
--    supervisor auth flow"). The supervisor connects to Postgres as the
--    schema owner (the user listed in GARRISON_DATABASE_URL), so it
--    already has SELECT on `sessions`. No additional GRANT migration
--    is required — the original plan's GRANT was based on an incorrect
--    assumption about a `garrison_supervisor` constrained role that
--    does not exist in this codebase (only `garrison_agent_ro`,
--    `garrison_agent_mempalace`, `garrison_dashboard_app`,
--    `garrison_dashboard_ro` are defined; the supervisor itself
--    connects as the migration owner with full privileges).
--
-- This file is a marker-only migration: it produces no schema change.

-- +goose Up
SELECT 'M5.4 deploy waypoint: knowledge-base pane (Company.md → MinIO; sessions table read directly)' AS note;

-- +goose Down
SELECT 'M5.4 deploy waypoint: no DDL to revert' AS note;
