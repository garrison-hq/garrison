package garrisonmutate

// MutateErrorKind enumerates the typed error_kind values garrison-mutate
// verbs return on failure. The set is mirrored exactly by the
// chat_mutation_audit.outcome CHECK constraint (plus 'success').
//
// ErrToolCallCeilingReached is NOT a verb-side error — the chat policy
// emits it when the per-turn ceiling fires. It exists on the audit
// CHECK enum for forensic completeness; the verb layer never returns it.
type MutateErrorKind string

const (
	ErrValidationFailed       MutateErrorKind = "validation_failed"
	ErrLeakScanFailed         MutateErrorKind = "leak_scan_failed"
	ErrTicketStateChanged     MutateErrorKind = "ticket_state_changed"
	ErrConcurrencyCapFull     MutateErrorKind = "concurrency_cap_full"
	ErrInvalidTransition      MutateErrorKind = "invalid_transition"
	ErrResourceNotFound       MutateErrorKind = "resource_not_found"
	ErrToolCallCeilingReached MutateErrorKind = "tool_call_ceiling_reached"
)

// String returns the textual form of the error_kind, suitable for
// chat_mutation_audit.outcome and for tool_result.error_kind.
func (k MutateErrorKind) String() string { return string(k) }

// AllVerbErrorKinds enumerates every error_kind a verb can return. Used
// by tests asserting the audit-table CHECK constraint covers the
// vocabulary. Excludes ErrToolCallCeilingReached (emitted by chat
// policy, not by verbs).
func AllVerbErrorKinds() []MutateErrorKind {
	return []MutateErrorKind{
		ErrValidationFailed,
		ErrLeakScanFailed,
		ErrTicketStateChanged,
		ErrConcurrencyCapFull,
		ErrInvalidTransition,
		ErrResourceNotFound,
	}
}
