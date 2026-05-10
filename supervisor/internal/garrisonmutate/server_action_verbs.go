package garrisonmutate

import (
	"context"
	"encoding/json"
)

// ServerActionVerbs is the M8 registry for verbs callable ONLY by the
// dashboard's Server Actions (NOT by the chat surface). Mirrors the
// shape of Verbs but lives behind a distinct slice so the chat-side
// tool-list path cannot accidentally expose them to LLMs.
//
// Per FR-306 + plan.md §6, register_mcp_server is Server-Action-only
// because:
//   - The operator approves MCP-server additions, not the LLM.
//   - The audit row's anchoring is different (server-action-anchored;
//     both chat_session_id + agent_instance_id NULL).
//   - The reactive worker (mcpserverwork.Worker) writes the audit row
//     when MCPJungle returns, not at Server-Action commit time, to
//     preserve the single-row FR-306 invariant.
//
// Tests assert TestVerbsSlicesDisjoint: no entry in Verbs appears in
// ServerActionVerbs and vice versa.
var ServerActionVerbs = []Verb{
	{
		Name: "register_mcp_server",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleRegisterMcpServer(ctx, deps, args)
		},
		ReversibilityClass:   2,
		AffectedResourceType: "mcp_server",
		Description:          "Register a new MCP server with MCPJungle for the active customer.",
	},
}

// FindServerActionVerb mirrors FindVerb for the Server-Action slice.
func FindServerActionVerb(name string) *Verb {
	for i := range ServerActionVerbs {
		if ServerActionVerbs[i].Name == name {
			return &ServerActionVerbs[i]
		}
	}
	return nil
}

// handleRegisterMcpServer is the Server-Action verb implementation.
// Lives in register_mcp_server.go; declared here as a package-level
// var so server_action_verbs.go's slice literal can reference it
// before init.
var handleRegisterMcpServer HandlerFunc = stubHandler
