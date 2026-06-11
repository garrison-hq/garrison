package garrisonmutate

import (
	"context"
	"encoding/json"
	"fmt"
)

// agentVerbNames is the M8 agent-caller surface: spec FR-005 grants
// spawned ticket agents create_ticket only. The full chat verb set
// (pause_agent, approve_hire, ...) stays operator-anchored; widening
// this list is a threat-model amendment, same as adding a verb to
// Verbs (chat-threat-model.md Rule 1).
func agentVerbNames() []string {
	return []string{"create_ticket"}
}

func verbAllowedForCaller(deps Deps, name string) bool {
	if !deps.AgentInstanceID.Valid {
		return true // chat mode: full registry
	}
	for _, n := range agentVerbNames() {
		if n == name {
			return true
		}
	}
	return false
}

// dispatch resolves a tools/call request to its registered Verb and
// invokes the handler. Returns the handler's Result + error directly;
// the JSON-RPC server wraps the response.
func dispatch(ctx context.Context, deps Deps, name string, args json.RawMessage) (Result, error) {
	v := FindVerb(name)
	if v == nil || !verbAllowedForCaller(deps, name) {
		return Result{}, fmt.Errorf("garrisonmutate: unknown verb %q", name)
	}
	return v.Handler(ctx, deps, args)
}

// toolDescriptor is the per-verb shape returned in tools/list responses
// (MCP protocol). Mirrors the finalize server's shape.
type toolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// listTools returns the tool descriptors for every registered verb.
// Per-verb input schemas are minimal in T004 (an open object); per-verb
// tasks (T005-T011) refine each schema as the verbs land.
func listTools() []toolDescriptor {
	out := make([]toolDescriptor, 0, len(Verbs))
	for i := range Verbs {
		v := Verbs[i]
		out = append(out, toolDescriptor{
			Name:        v.Name,
			Description: v.Description,
			InputSchema: openObjectSchema(),
		})
	}
	return out
}

// listToolsFor filters the registry by caller anchor: agent-mode
// servers advertise only the M8 agent-caller verbs, chat-mode servers
// the full set. dispatch enforces the same predicate, so a model that
// guesses a hidden verb name still gets method-not-found.
func listToolsFor(deps Deps) []toolDescriptor {
	all := listTools()
	if !deps.AgentInstanceID.Valid {
		return all
	}
	out := make([]toolDescriptor, 0, len(agentVerbNames()))
	for _, td := range all {
		if verbAllowedForCaller(deps, td.Name) {
			out = append(out, td)
		}
	}
	return out
}

// openObjectSchema is the placeholder schema used by stub handlers; per
// chat-threat-model.md Rule 1, the assistant cannot call an
// unregistered verb regardless of schema, so a permissive schema is
// safe at scaffold time. Per-verb tasks tighten the schema to the
// arg shape the handler validates.
func openObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	}
}
