// Package actionbroker implements the Garrison M11 Action Broker:
// the tier policy classification authority (Thread 2) and the
// supervisor-side dispatcher worker (Thread 3) that executes approved
// external actions using vault-scoped credentials.
//
// policy.go is the SINGLE authority for action-type→tier classification
// (plan D4, FR-011). It must be consistent with the
// pending_actions_floor_is_approve CHECK in the M11 migration.
package actionbroker

// Tier is the tier an external action is classified into. The four
// string constants match the pending_actions.tier CHECK values.
type Tier string

const (
	// TierAuto — the dispatcher executes the action and logs; no operator
	// gate required. Appropriate for low-risk internal-state writes.
	TierAuto Tier = "auto"

	// TierNotify — the dispatcher executes the action and then surfaces it
	// post-hoc to the operator in the Outbox as a feed item. No operator
	// gate before execution, but the operator is informed after.
	TierNotify Tier = "notify"

	// TierApprove — blocked until the operator's explicit approve click.
	// The dispatcher does not execute an approve-tier row until its status
	// transitions to 'approved' via the operator Server Action. Public-
	// facing action types are on the permanent-Approve floor (FR-014) and
	// can never be reclassified to a lower tier.
	TierApprove Tier = "approve"

	// TierHumanOnly — the dispatcher never executes this action. The agent
	// prepares the payload; a human performs the action by hand and then
	// marks it done via the Outbox mark-as-done Server Action (FR-027).
	TierHumanOnly Tier = "human_only"
)

// floor is the permanent-Approve set (plan D5a, FR-014, SC-003).
// Action types listed here are classified as TierApprove regardless of
// what the policy map says. They are public-facing per ARCHITECTURE §M11
// ("posts, replies, releases, mail to non-customers, price/copy changes")
// and must never be reclassified downward.
//
// IMPORTANT: this set must remain in sync with the action_type list in
// the pending_actions_floor_is_approve CHECK constraint in migration
// 20260612000001_m11_action_broker.sql. TestFloorCheckMatchesPolicy
// asserts this (plan D5c, no drift).
var floor = map[string]struct{}{
	"github_issue_comment": {},
}

// policy is the deploy-time classification table (plan D4, FR-009,
// FR-010). Keyed on action type; values are the assigned Tier. Floor
// action types may be omitted here (the floor wins) or listed as
// TierApprove for documentation purposes; a floor entry at a lower tier
// is overridden by Classify's floor-first logic.
//
// This map is a compile-time constant — no operator surface modifies it
// in M11 alpha (FR-009). A second action type rides this table without
// re-architecture.
var policy = map[string]Tier{
	// github_issue_comment is on the permanent-Approve floor; listing it
	// here as TierApprove is redundant but documents the intent.
	"github_issue_comment": TierApprove,
}

// FloorActionTypes returns the slice of action types on the permanent-
// Approve floor. Exported so TestFloorCheckMatchesPolicy (policy_test.go)
// can assert the floor matches the migration's CHECK constraint without
// importing internal migration text.
func FloorActionTypes() []string {
	types := make([]string, 0, len(floor))
	for k := range floor {
		types = append(types, k)
	}
	return types
}

// Classify is the SINGLE classification authority for an action type
// (FR-011, plan D4/D5/D6). It returns the Tier and a human-readable
// reason string suitable for storing in pending_actions.tier_reason
// (FR-012).
//
// Classification order (plan D5a):
//  1. Floor check first — if actionType ∈ floor, return (TierApprove,
//     "permanent-Approve floor (public-facing)") regardless of what
//     policy says. This is the structural guarantee SC-003 relies on.
//  2. Policy table lookup — if actionType is in the policy map, return
//     the mapped tier with reason "deploy-time tier policy".
//  3. Unknown default — return (TierApprove,
//     "unclassified action type — safe-by-construction default") so a
//     newly-added action type is never silently auto/notify (FR-015).
//
// Classify never reads agent-supplied input; callers must never pass a
// tier value from the agent's request args (FR-005, plan D7 agent-only
// guard). The agent has no tier field in RequestExternalActionArgs.
func Classify(actionType string) (Tier, string) {
	// Layer 1: permanent-Approve floor wins unconditionally (SC-003).
	if _, inFloor := floor[actionType]; inFloor {
		return TierApprove, "permanent-Approve floor (public-facing)"
	}

	// Layer 2: deploy-time policy table.
	if t, inPolicy := policy[actionType]; inPolicy {
		return t, "deploy-time tier policy"
	}

	// Layer 3: safe-by-construction default for unclassified types (FR-015).
	return TierApprove, "unclassified action type — safe-by-construction default"
}
