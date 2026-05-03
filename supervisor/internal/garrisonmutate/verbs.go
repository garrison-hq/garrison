package garrisonmutate

import (
	"context"
	"encoding/json"
)

// Result is the structured tool_result payload garrison-mutate verbs
// return. Surfaces directly as the chip-renderable shape on the
// dashboard side.
type Result struct {
	Success             bool   `json:"success"`
	AffectedResourceID  string `json:"affected_resource_id,omitempty"`
	AffectedResourceURL string `json:"affected_resource_url,omitempty"`
	ErrorKind           string `json:"error_kind,omitempty"`
	Message             string `json:"message,omitempty"`
}

// HandlerFunc is a verb's per-call entry point. The dispatcher in
// tool.go invokes it after looking the verb up by name in the Verbs
// slice. Errors returned here become tool_result.is_error=true frames;
// the typed Result.ErrorKind carries the specific kind.
type HandlerFunc func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error)

// Verb is one entry in the sealed verb registry. ReversibilityClass
// matches the chat_mutation_audit.reversibility_class CHECK constraint
// (1 / 2 / 3) per chat-threat-model.md §5.
type Verb struct {
	Name                 string
	Handler              HandlerFunc
	ReversibilityClass   int
	AffectedResourceType string // "ticket" / "agent_role" / "hiring_proposal"
	Description          string // surfaces in tools/list
}

// Verbs is the SINGLE SOURCE OF TRUTH for the registered chat-driven
// mutation set. Per chat-threat-model.md Rule 1, adding a verb requires
// editing this file, the threat-model amendment's reversibility table,
// the per-verb handler, and the registry test
// (TestVerbsRegistryMatchesEnumeration). Removing a verb requires the
// same.
//
// Handlers are defined in per-domain files: verbs_tickets.go,
// verbs_agents.go, verbs_hiring.go. Each verb's per-call business
// logic lands as part of the M5.3 task list (T005-T011); the registry
// here exists so the sealed-allow-list test (T004) passes against
// stub handlers before the verb logic ships (FR-433 ordering).
// Verbs entries use forwarding closures so reassignments to the
// per-verb handler vars (init-time replacement of stub→real in the
// per-domain files) propagate at call time. A naive `Handler:
// handleCreateTicket` in the slice literal would freeze the stub
// function value at package-init phase 1; the closure resolves the
// var lookup per call.
var Verbs = []Verb{
	{
		Name: "create_ticket",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleCreateTicket(ctx, deps, args)
		},
		ReversibilityClass:   3,
		AffectedResourceType: "ticket",
		Description:          "Create a new ticket in the named department.",
	},
	{
		Name: "edit_ticket",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleEditTicket(ctx, deps, args)
		},
		ReversibilityClass:   2,
		AffectedResourceType: "ticket",
		Description:          "Edit an existing ticket's editable fields.",
	},
	{
		Name: "transition_ticket",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleTransitionTicket(ctx, deps, args)
		},
		ReversibilityClass:   1,
		AffectedResourceType: "ticket",
		Description:          "Move a ticket to a different Kanban column.",
	},
	{
		Name: "pause_agent",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handlePauseAgent(ctx, deps, args)
		},
		ReversibilityClass:   1,
		AffectedResourceType: "agent_role",
		Description:          "Pause an agent role so the supervisor stops spawning new instances.",
	},
	{
		Name: "resume_agent",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleResumeAgent(ctx, deps, args)
		},
		ReversibilityClass:   1,
		AffectedResourceType: "agent_role",
		Description:          "Resume a previously-paused agent role.",
	},
	{
		Name: "spawn_agent",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleSpawnAgent(ctx, deps, args)
		},
		ReversibilityClass:   3,
		AffectedResourceType: "agent_role",
		Description:          "Manually spawn an agent instance (respects per-department concurrency cap).",
	},
	{
		Name: "edit_agent_config",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleEditAgentConfig(ctx, deps, args)
		},
		ReversibilityClass:   2,
		AffectedResourceType: "agent_role",
		Description:          "Edit an agent role's configuration. Pre-tx leak-scan rejects secrets.",
	},
	{
		Name: "propose_hire",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleProposeHire(ctx, deps, args)
		},
		ReversibilityClass:   3,
		AffectedResourceType: "hiring_proposal",
		Description:          "Write a hiring proposal row visible on the operator's stopgap page.",
	},
	{
		Name: "propose_skill_change",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleProposeSkillChange(ctx, deps, args)
		},
		ReversibilityClass:   3,
		AffectedResourceType: "hiring_proposal",
		Description:          "Propose adding, removing, or bumping skills on an existing agent. Operator review required before install.",
	},
	{
		Name: "bump_skill_version",
		Handler: func(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
			return handleBumpSkillVersion(ctx, deps, args)
		},
		ReversibilityClass:   3,
		AffectedResourceType: "hiring_proposal",
		Description:          "Propose bumping one installed skill to a new version. Operator review required before install.",
	},
}

// FindVerb returns the Verb entry for name, or nil if not registered.
func FindVerb(name string) *Verb {
	for i := range Verbs {
		if Verbs[i].Name == name {
			return &Verbs[i]
		}
	}
	return nil
}

// VerbNames returns the registered verb names as a sorted-by-registry
// slice; convenience for tests + tools/list.
func VerbNames() []string {
	out := make([]string, 0, len(Verbs))
	for i := range Verbs {
		out = append(out, Verbs[i].Name)
	}
	return out
}

// stubHandler is the placeholder until per-verb tasks (T005-T011)
// replace it with the real per-verb business logic. Returns
// ErrValidationFailed so any chat that calls a registered verb during
// T004's window gets a typed failure rather than a panic. Each
// per-verb file replaces its named handler with the real
// implementation.
func stubHandler(_ context.Context, _ Deps, _ json.RawMessage) (Result, error) {
	return Result{
		Success:   false,
		ErrorKind: string(ErrValidationFailed),
		Message:   "garrison-mutate verb not yet implemented (T004 stub)",
	}, nil
}

// Per-verb handler stubs. Replaced by the per-domain files
// (verbs_tickets.go, verbs_agents.go, verbs_hiring.go) as T005-T011
// land. Until then, these stubs satisfy the registry's HandlerFunc
// type and let the sealed-allow-list test pass.
var (
	handleCreateTicket       HandlerFunc = stubHandler
	handleEditTicket         HandlerFunc = stubHandler
	handleTransitionTicket   HandlerFunc = stubHandler
	handlePauseAgent         HandlerFunc = stubHandler
	handleResumeAgent        HandlerFunc = stubHandler
	handleSpawnAgent         HandlerFunc = stubHandler
	handleEditAgentConfig    HandlerFunc = stubHandler
	handleProposeHire        HandlerFunc = stubHandler
	handleProposeSkillChange HandlerFunc = stubHandler
	handleBumpSkillVersion   HandlerFunc = stubHandler
)
