-- Grant garrison_agent_ro SELECT on agent_instances.
--
-- M2.2.1 wired the finalize MCP handler to query agent_instances
-- for the already-committed precheck (internal/finalize/handler.go
-- SelectAgentInstanceFinalizedState → status, exit_reason). The
-- finalize MCP connects as garrison_agent_ro via GARRISON_DATABASE_URL,
-- but the role's grants (migrations/20260422000003_m2_1_claude_invocation.sql
-- §Section 2) never included agent_instances — only tickets,
-- ticket_transitions, departments, agents.
--
-- Result: every finalize_ticket call failed the precheck with
-- `permission denied for table agent_instances`, which the handler
-- surfaces as {"ok":false,"failure":"state","hint":"internal error
-- checking finalize state; please retry"}. Agents retry, fail the
-- same way, exhaust 3 attempts, exit finalize_invalid — retros never
-- commit.
--
-- Fix: grant SELECT on agent_instances to garrison_agent_ro. Matches
-- the grant garrison_agent_mempalace already has from the M2.2
-- migration. Read-only, no other privileges needed.

-- +goose Up
GRANT SELECT ON agent_instances TO garrison_agent_ro;

-- +goose Down
REVOKE SELECT ON agent_instances FROM garrison_agent_ro;
