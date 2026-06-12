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
//
// M9 adds four scheduled-task entries (chat-threat-model.md §5,
// Server-Action verb registry). Per plan decision 11 their execution is
// dashboard-side: the Server Action writes the row change + the
// chat_mutation_audit row (verb name from this registry, chat anchors
// NULL, affected_resource_type='scheduled_task') in one drizzle tx. The
// entries here exist for the registry/tier table + audit CHECK
// alignment; no supervisor-side dispatch path ever invokes them, so
// their handlers are typed refusals (serverActionRegistryOnlyHandler).
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
	{
		Name:                 "edit_scheduled_task",
		Handler:              serverActionRegistryOnlyHandler("edit_scheduled_task"),
		ReversibilityClass:   2,
		AffectedResourceType: "scheduled_task",
		Description:          "Edit a scheduled task's editable fields (dashboard Server Action only; diff captured in args_jsonb).",
	},
	{
		Name:                 "pause_scheduled_task",
		Handler:              serverActionRegistryOnlyHandler("pause_scheduled_task"),
		ReversibilityClass:   1,
		AffectedResourceType: "scheduled_task",
		Description:          "Pause a scheduled task so the tick loop stops firing it (dashboard Server Action only).",
	},
	{
		Name:                 "resume_scheduled_task",
		Handler:              serverActionRegistryOnlyHandler("resume_scheduled_task"),
		ReversibilityClass:   1,
		AffectedResourceType: "scheduled_task",
		Description:          "Resume a paused scheduled task; next_fire_at recomputes advance-only, no catch-up firing (dashboard Server Action only).",
	},
	{
		Name:                 "delete_scheduled_task",
		Handler:              serverActionRegistryOnlyHandler("delete_scheduled_task"),
		ReversibilityClass:   3,
		AffectedResourceType: "scheduled_task",
		Description:          "Soft-delete a scheduled task; run history and audit rows survive (dashboard Server Action only; pre-state snapshot in args_jsonb).",
	},
}

// serverActionRegistryOnlyHandler backs registry entries whose
// execution lives entirely dashboard-side (M9 scheduled-task CRUD, plan
// decision 11). Returns a typed validation_failed rather than reusing
// stubHandler so a misrouted call reports the actual contract instead
// of "not yet implemented".
func serverActionRegistryOnlyHandler(verb string) HandlerFunc {
	return func(_ context.Context, _ Deps, _ json.RawMessage) (Result, error) {
		return Result{
			Success:   false,
			ErrorKind: string(ErrValidationFailed),
			Message:   verb + " executes dashboard-side as a Server Action; it has no supervisor-side handler",
		}, nil
	}
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
