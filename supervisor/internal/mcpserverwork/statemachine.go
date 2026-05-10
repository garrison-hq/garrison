package mcpserverwork

// State is one of the four mcp_servers.status values enforced by the
// M8 migration's CHECK constraint. The worker's state-machine is
// linear:
//
//	pending ──┬─► registered    (success path)
//	          │
//	          └─► failed         (any non-success terminal)
//
// 'deregistered' is reserved for the operator-triggered teardown path
// (M9+ polish). The worker never produces or consumes that state.
type State string

const (
	StatePending      State = "pending"
	StateRegistered   State = "registered"
	StateFailed       State = "failed"
	StateDeregistered State = "deregistered"
)

// ValidTerminalTransition reports whether moving from `from` to `to`
// is a legal worker-side transition. Returns true only for
// pending→registered and pending→failed; every other pair is rejected.
// Used by the worker's defensive guard (skip rows already in a
// terminal state — protects against double-fire from LISTEN + poll).
func ValidTerminalTransition(from, to State) bool {
	if from != StatePending {
		return false
	}
	return to == StateRegistered || to == StateFailed
}
