package garrisonmutate

import "testing"

// TestMutateErrorKindCoversAllOutcomes asserts the MutateErrorKind enum
// covers every value in the chat_mutation_audit.outcome CHECK
// constraint (other than 'success'). Any drift between the migration's
// CHECK and Go's enum is a forensic bug — auditing rows would land
// without a Go-side handler.
func TestMutateErrorKindCoversAllOutcomes(t *testing.T) {
	wantInOutcomeCheck := []string{
		"validation_failed",
		"leak_scan_failed",
		"ticket_state_changed",
		"concurrency_cap_full",
		"invalid_transition",
		"resource_not_found",
		"tool_call_ceiling_reached",
	}
	have := map[string]struct{}{}
	for _, k := range []MutateErrorKind{
		ErrValidationFailed,
		ErrLeakScanFailed,
		ErrTicketStateChanged,
		ErrConcurrencyCapFull,
		ErrInvalidTransition,
		ErrResourceNotFound,
		ErrToolCallCeilingReached,
	} {
		have[k.String()] = struct{}{}
	}
	for _, want := range wantInOutcomeCheck {
		if _, ok := have[want]; !ok {
			t.Errorf("migration CHECK enum %q has no Go MutateErrorKind", want)
		}
	}
}

// TestAllVerbErrorKindsExcludesCeiling verifies AllVerbErrorKinds()
// (the verb-side handler vocabulary) does not include the chat-policy
// ceiling-reached value, which is emitted by the chat policy and not
// by garrisonmutate verbs themselves.
func TestAllVerbErrorKindsExcludesCeiling(t *testing.T) {
	for _, k := range AllVerbErrorKinds() {
		if k == ErrToolCallCeilingReached {
			t.Errorf("AllVerbErrorKinds includes ErrToolCallCeilingReached; expected exclusion (chat-policy origin)")
		}
	}
}
