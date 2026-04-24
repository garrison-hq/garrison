package finalize

import (
	"strings"
	"testing"
	"time"
)

// TestValidationError_Error_WithField pins the field-scoped Error()
// formatting used by Go's error-chain machinery. Format pinned as a
// contract so slog chains keep their structured shape.
func TestValidationError_Error_WithField(t *testing.T) {
	verr := &ValidationError{
		ErrorType: ErrorTypeSchema,
		Field:     "diary_entry.rationale",
		Message:   "too short",
	}
	got := verr.Error()
	want := "finalize: schema at diary_entry.rationale: too short"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestValidationError_Error_WithoutField pins the root-scoped format
// (empty Field takes the short branch).
func TestValidationError_Error_WithoutField(t *testing.T) {
	verr := &ValidationError{
		ErrorType: ErrorTypeSchema,
		Field:     "",
		Message:   "payload is not valid JSON",
	}
	got := verr.Error()
	want := "finalize: schema: payload is not valid JSON"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRenderHint_DecodeFailureUsesLineAndColumn covers the FailureDecode
// branch of renderHint: line + column + stripped message go into the
// agent-facing hint.
func TestRenderHint_DecodeFailureUsesLineAndColumn(t *testing.T) {
	verr := &ValidationError{
		Failure: FailureDecode,
		Line:    3,
		Column:  17,
		Message: "arguments are not a valid JSON object: unexpected end of input",
	}
	got := renderHint(verr)
	if !strings.Contains(got, "line 3 col 17") {
		t.Errorf("hint should mention line 3 col 17, got %q", got)
	}
	if !strings.Contains(got, "unexpected end of input") {
		t.Errorf("hint should include the stripped parser message, got %q", got)
	}
	// Stripped message should NOT carry the framing prefix.
	if strings.Contains(got, "arguments are not a valid JSON object:") {
		t.Errorf("hint should strip the framing prefix, got %q", got)
	}
}

// TestRenderHint_StateFailurePassesMessage covers the FailureState
// branch: the caller's message is handed through verbatim (no decoration).
func TestRenderHint_StateFailurePassesMessage(t *testing.T) {
	verr := &ValidationError{
		Failure: FailureState,
		Message: "finalize_ticket already succeeded for this agent_instance",
	}
	if got := renderHint(verr); got != verr.Message {
		t.Errorf("got %q, want %q", got, verr.Message)
	}
}

// TestRenderHint_UnknownFailureFallsBack covers the default branch of
// renderHint when Failure is a value outside the three known constants.
func TestRenderHint_UnknownFailureFallsBack(t *testing.T) {
	verr := &ValidationError{
		Failure: Failure("mystery"),
		Message: "something broke",
	}
	if got := renderHint(verr); got != "something broke" {
		t.Errorf("got %q, want fallback message", got)
	}
}

// TestRenderValidationHint_AllConstraints exercises every
// Constraint-specific branch of renderValidationHint so each template
// is pinned against its expected string.
func TestRenderValidationHint_AllConstraints(t *testing.T) {
	cases := []struct {
		name       string
		verr       *ValidationError
		wantSubstr []string
	}{
		{
			name: "required",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintRequired,
				Field: "ticket_id",
			},
			wantSubstr: []string{"`ticket_id`", "required", "cannot be empty"},
		},
		{
			name: "min_length_with_field",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintMinLength,
				Field: "outcome", Expected: "string with min length 10", Actual: "hi",
			},
			wantSubstr: []string{"`outcome`", "min length 10", "length 2"},
		},
		{
			name: "max_length",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintMaxLength,
				Field: "outcome", Expected: "string with max length 500", Actual: strings.Repeat("a", 600),
			},
			wantSubstr: []string{"max length 500", "length 600"},
		},
		{
			name: "min_items",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintMinItems,
				Field: "kg_triples", Expected: "array with min length 1", Actual: "0 items",
			},
			wantSubstr: []string{"`kg_triples`", "array", "0 items"},
		},
		{
			name: "max_items",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintMaxItems,
				Field: "kg_triples", Expected: "array with max length 100", Actual: "150 items",
			},
			wantSubstr: []string{"`kg_triples`", "150 items"},
		},
		{
			name: "type_mismatch",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintTypeMismatch,
				Field: "outcome", Expected: "string",
			},
			wantSubstr: []string{"`outcome`", "wrong type", "expected string"},
		},
		{
			name: "format",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintFormat,
				Field: "ticket_id", Expected: "UUID (8-4-4-4-12 hex)",
			},
			wantSubstr: []string{"`ticket_id`", "invalid format", "UUID"},
		},
		{
			name: "empty_field_renders_as_root",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: ConstraintRequired, Field: "",
			},
			wantSubstr: []string{"`(root)`", "required"},
		},
		{
			name: "unknown_constraint_falls_back_to_message",
			verr: &ValidationError{
				Failure: FailureValidation, Constraint: Constraint("mystery"),
				Message: "fallback text",
			},
			wantSubstr: []string{"fallback text"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderValidationHint(tc.verr)
			for _, s := range tc.wantSubstr {
				if !strings.Contains(got, s) {
					t.Errorf("hint %q missing substring %q", got, s)
				}
			}
		})
	}
}

// TestStripMessagePrefix_NoMatchReturnsUnchanged pins the branch where
// the message does NOT start with the framing prefix — the message is
// returned verbatim.
func TestStripMessagePrefix_NoMatchReturnsUnchanged(t *testing.T) {
	const msg = "some other decode error"
	if got := stripMessagePrefix(msg); got != msg {
		t.Errorf("got %q, want %q", got, msg)
	}
}

// TestValidateStringArray_AcceptsEmpty pins the happy-path nil return
// for the common case of a zero-length artifacts array.
func TestValidateStringArray_AcceptsEmpty(t *testing.T) {
	if err := validateStringArray(nil, "diary_entry.artifacts"); err != nil {
		t.Errorf("empty array should pass, got %v", err)
	}
}

// TestValidateStringArray_RejectsTooManyItems covers the array-length
// cap branch (ConstraintMaxItems).
func TestValidateStringArray_RejectsTooManyItems(t *testing.T) {
	arr := make([]string, ArtifactArrayMax+1)
	for i := range arr {
		arr[i] = "x"
	}
	verr := validateStringArray(arr, "diary_entry.artifacts")
	if verr == nil {
		t.Fatal("expected error, got nil")
	}
	if verr.Constraint != ConstraintMaxItems {
		t.Errorf("Constraint=%q, want %q", verr.Constraint, ConstraintMaxItems)
	}
}

// TestValidateStringArray_RejectsTooLongItem covers the per-item
// length-cap branch (ConstraintMaxLength with indexed field name).
func TestValidateStringArray_RejectsTooLongItem(t *testing.T) {
	arr := []string{"short", strings.Repeat("y", ArtifactItemMax+1)}
	verr := validateStringArray(arr, "diary_entry.artifacts")
	if verr == nil {
		t.Fatal("expected error, got nil")
	}
	if verr.Constraint != ConstraintMaxLength {
		t.Errorf("Constraint=%q, want %q", verr.Constraint, ConstraintMaxLength)
	}
	if !strings.Contains(verr.Field, "[1]") {
		t.Errorf("field should carry the offending index, got %q", verr.Field)
	}
}

// TestValidateTripleField_TooShort + _TooLong pin both bounds.
func TestValidateTripleField_TooShort(t *testing.T) {
	verr := validateTripleField("ab", "subject")
	if verr == nil || verr.Constraint != ConstraintMinLength {
		t.Fatalf("expected min-length error, got %+v", verr)
	}
}
func TestValidateTripleField_TooLong(t *testing.T) {
	verr := validateTripleField(strings.Repeat("z", TripleFieldMax+1), "object")
	if verr == nil || verr.Constraint != ConstraintMaxLength {
		t.Fatalf("expected max-length error, got %+v", verr)
	}
}
func TestValidateTripleField_OK(t *testing.T) {
	if verr := validateTripleField("agent_instance", "subject"); verr != nil {
		t.Errorf("valid triple field should pass, got %+v", verr)
	}
}

// TestParseValidFrom_NowSubstitutes pins the `"now"` literal path. The
// comparison is case-insensitive and whitespace-tolerant per the
// implementation.
func TestParseValidFrom_NowSubstitutes(t *testing.T) {
	ref := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	got, verr := parseValidFrom("  Now  ", "valid_from", ref)
	if verr != nil {
		t.Fatalf("\"now\" should parse, got %+v", verr)
	}
	if !got.Equal(ref) {
		t.Errorf("got %v, want %v (ref)", got, ref)
	}
}

// TestParseValidFrom_RFC3339Nano covers the fallback branch that accepts
// nanosecond precision when plain RFC3339 fails.
func TestParseValidFrom_RFC3339Nano(t *testing.T) {
	got, verr := parseValidFrom("2026-04-24T12:34:56.789Z", "valid_from", time.Time{})
	if verr != nil {
		t.Fatalf("RFC3339Nano should parse, got %+v", verr)
	}
	if got.Year() != 2026 || got.Nanosecond() == 0 {
		t.Errorf("parsed time lost precision: %v", got)
	}
}

// TestParseValidFrom_UnparseableReturnsFormatError covers the failure
// branch where neither "now" nor RFC3339 nor RFC3339Nano parses.
func TestParseValidFrom_UnparseableReturnsFormatError(t *testing.T) {
	_, verr := parseValidFrom("not-a-date", "valid_from", time.Time{})
	if verr == nil {
		t.Fatal("expected format error, got nil")
	}
	if verr.Constraint != ConstraintFormat {
		t.Errorf("Constraint=%q, want %q", verr.Constraint, ConstraintFormat)
	}
}
