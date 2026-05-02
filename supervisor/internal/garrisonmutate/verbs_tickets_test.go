package garrisonmutate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// validationDeps returns a Deps suitable for argument-validation tests
// that never reach the DB. Pool is nil; the verb's validation path
// short-circuits before any pool call.
func validationDeps() Deps {
	return Deps{
		ChatSessionID: pgtype.UUID{Valid: true, Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}},
		ChatMessageID: pgtype.UUID{Valid: true, Bytes: [16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}},
	}
}

// expectValidationFailure runs the verb with the given raw args and
// asserts the result is a validation_failed Result with the expected
// substring in Message. Returns the Result for further assertions.
func expectValidationFailure(t *testing.T, h HandlerFunc, raw string, wantSubstr string) Result {
	t.Helper()
	r, err := h(context.Background(), validationDeps(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if r.Success {
		t.Errorf("expected Success=false; got %+v", r)
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, wantSubstr) {
		t.Errorf("Message %q missing %q", r.Message, wantSubstr)
	}
	return r
}

// TestCreateTicketRejectsMissingObjective covers FR-414 validation for
// create_ticket: missing required field returns ErrValidationFailed
// without touching the DB.
func TestCreateTicketRejectsMissingObjective(t *testing.T) {
	expectValidationFailure(t, realCreateTicketHandler,
		`{"department_slug":"growth"}`,
		"objective is required",
	)
}

// TestCreateTicketRejectsMissingDepartment covers FR-414 validation
// for create_ticket: missing department_slug returns ErrValidationFailed.
func TestCreateTicketRejectsMissingDepartment(t *testing.T) {
	expectValidationFailure(t, realCreateTicketHandler,
		`{"objective":"Fix the kanban drag bug"}`,
		"department_slug is required",
	)
}

// TestCreateTicketRejectsOversizedObjective covers FR-414 validation
// for create_ticket: objective length bound rejection.
func TestCreateTicketRejectsOversizedObjective(t *testing.T) {
	huge := strings.Repeat("x", 10001)
	body, _ := json.Marshal(map[string]string{
		"objective":       huge,
		"department_slug": "growth",
	})
	expectValidationFailure(t, realCreateTicketHandler, string(body),
		"objective exceeds")
}

// TestCreateTicketRejectsMalformedJSON covers the parse path: invalid
// JSON returns ErrValidationFailed surfacing the parse error.
func TestCreateTicketRejectsMalformedJSON(t *testing.T) {
	expectValidationFailure(t, realCreateTicketHandler, `{not json`,
		"parse args")
}

// TestCreateTicketRejectsBadParentTicketUUID covers the M6 T010 UUID
// parse path: an obviously-malformed parent_ticket_id is rejected at
// the verb level before any DB call. Pool stays nil because the
// parent-validation short-circuit fires before lookupCreateTicketDept.
// (It's reachable via the integration suite too, but this unit test
// pins the parse-error message verbatim without needing a Postgres
// container.)
func TestCreateTicketRejectsBadParentTicketUUID(t *testing.T) {
	// We use the helper's resolveParentTicketID directly because the
	// outer realCreateTicketHandler requires Pool.Begin() to reach the
	// parent-validation branch.
	_, res, err := resolveParentTicketID(context.Background(), nil,
		"not-a-uuid", pgtype.UUID{Valid: true, Bytes: [16]byte{1}})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil {
		t.Fatal("expected validation Result; got nil")
	}
	if !strings.Contains(res.Message, "not a valid UUID") {
		t.Errorf("Message %q missing 'not a valid UUID'", res.Message)
	}
}

// TestCreateTicketEmptyParentReturnsZeroUUID covers the
// no-parent-supplied path: empty string short-circuits to (zero,
// nil, nil) so the caller passes a NULL parent_ticket_id to InsertChatTicket.
func TestCreateTicketEmptyParentReturnsZeroUUID(t *testing.T) {
	got, res, err := resolveParentTicketID(context.Background(), nil,
		"", pgtype.UUID{Valid: true, Bytes: [16]byte{1}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil Result for empty parent; got %+v", res)
	}
	if got.Valid {
		t.Errorf("expected zero UUID; got valid %v", got)
	}
}

// TestParseCreateTicketArgsRejectsMalformedJSON covers the parse-args
// branch of the M6 T010 helper extraction. Mirrors the existing
// TestCreateTicketRejectsMalformedJSON shape but routes through
// parseCreateTicketArgs directly.
func TestParseCreateTicketArgsRejectsMalformedJSON(t *testing.T) {
	_, res := parseCreateTicketArgs([]byte(`{not json`))
	if res == nil {
		t.Fatal("expected validation Result for malformed JSON")
	}
	if !strings.Contains(res.Message, "parse args") {
		t.Errorf("Message %q missing 'parse args'", res.Message)
	}
}

// TestBuildCreateTicketMetadata_EmptyReturnsBraces — args.Metadata empty
// → b="{}" so InsertChatTicket gets a valid JSONB literal.
func TestBuildCreateTicketMetadata_EmptyReturnsBraces(t *testing.T) {
	b, res := buildCreateTicketMetadata(CreateTicketArgs{})
	if res != nil {
		t.Fatalf("expected nil Result; got %+v", res)
	}
	if string(b) != "{}" {
		t.Errorf("metadata = %q; want {}", string(b))
	}
}

// TestBuildCreateTicketMetadata_PopulatedRoundTrips — non-empty
// Metadata round-trips through json.Marshal cleanly.
func TestBuildCreateTicketMetadata_PopulatedRoundTrips(t *testing.T) {
	b, res := buildCreateTicketMetadata(CreateTicketArgs{
		Metadata: map[string]any{"priority": "high", "labels": []string{"bug"}},
	})
	if res != nil {
		t.Fatalf("expected nil Result; got %+v", res)
	}
	if !strings.Contains(string(b), `"priority":"high"`) {
		t.Errorf("metadata round-trip missing priority: %s", b)
	}
}

// TestBuildCreateTicketMetadata_RejectsUnmarshallable — a Metadata
// payload that contains an unmarshallable value (channels are not
// json-marshallable per encoding/json's contract) surfaces as a
// validation_failed Result. In production CreateTicketArgs.Metadata
// is constructed from json.Unmarshal so this branch is unreachable
// from operator input, but the helper's defensive shape is pinned
// here so a future caller-introduced regression is caught.
func TestBuildCreateTicketMetadata_RejectsUnmarshallable(t *testing.T) {
	args := CreateTicketArgs{
		Metadata: map[string]any{"chan": make(chan int)},
	}
	b, res := buildCreateTicketMetadata(args)
	if res == nil {
		t.Fatalf("expected validation Result; got bytes=%q", string(b))
	}
	if !strings.Contains(res.Message, "invalid metadata") {
		t.Errorf("Message %q missing 'invalid metadata'", res.Message)
	}
}

// TestEditTicketRejectsMissingTicketID covers edit_ticket validation:
// ticket_id is required.
func TestEditTicketRejectsMissingTicketID(t *testing.T) {
	expectValidationFailure(t, realEditTicketHandler,
		`{"objective":"updated"}`,
		"ticket_id is required",
	)
}

// TestEditTicketRejectsAllFieldsAbsent covers edit_ticket validation:
// at-least-one-field-required (otherwise the verb is a no-op masquerade).
func TestEditTicketRejectsAllFieldsAbsent(t *testing.T) {
	expectValidationFailure(t, realEditTicketHandler,
		`{"ticket_id":"00000000-0000-0000-0000-000000000001"}`,
		"at least one of",
	)
}

// TestEditTicketRejectsInvalidTicketID covers UUID parse failure.
func TestEditTicketRejectsInvalidTicketID(t *testing.T) {
	expectValidationFailure(t, realEditTicketHandler,
		`{"ticket_id":"not-a-uuid","objective":"x"}`,
		"invalid ticket_id",
	)
}

// TestTransitionTicketRejectsMissingTicketID covers transition_ticket
// validation.
func TestTransitionTicketRejectsMissingTicketID(t *testing.T) {
	expectValidationFailure(t, realTransitionTicketHandler,
		`{"to_column":"qa-review"}`,
		"ticket_id is required",
	)
}

// TestTransitionTicketRejectsMissingToColumn covers transition_ticket
// validation.
func TestTransitionTicketRejectsMissingToColumn(t *testing.T) {
	expectValidationFailure(t, realTransitionTicketHandler,
		`{"ticket_id":"00000000-0000-0000-0000-000000000001"}`,
		"to_column is required",
	)
}

// TestRegisteredHandlersAreRealNotStubs verifies the package init() in
// verbs_tickets.go replaced the stub handlers for the ticket verbs.
// Defense against accidental revert: if a future refactor drops the
// init() registration, the dispatch path falls back to stubs and this
// test catches it.
func TestRegisteredHandlersAreRealNotStubs(t *testing.T) {
	for _, name := range []string{"create_ticket", "edit_ticket", "transition_ticket"} {
		v := FindVerb(name)
		if v == nil {
			t.Errorf("FindVerb(%q) = nil", name)
			continue
		}
		// Call the handler with malformed JSON — real handlers return
		// a parse-args validation failure; stubs return a "not yet
		// implemented" message.
		r, _ := v.Handler(context.Background(), validationDeps(), json.RawMessage(`{not json`))
		if strings.Contains(r.Message, "not yet implemented") {
			t.Errorf("verb %q still using stubHandler; expected real handler", name)
		}
	}
}
