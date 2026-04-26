-- +goose Up
-- M2.3 gap-patch: add mcp_config column to agents so the supervisor can
-- include agent-specific MCP servers in the per-invocation config and Rule 3
-- (RejectVaultServers) can scan them before spawn.
-- The column is a JSON object whose keys are server names and values are
-- mcpServerSpec objects ({command, args?, env?}). Default is empty — existing
-- agents keep their existing baseline servers (postgres / mempalace / finalize).
ALTER TABLE agents
    ADD COLUMN mcp_config JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE agents DROP COLUMN IF EXISTS mcp_config;
