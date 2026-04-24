package finalize

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// decodeToolResult unwraps the MCP content envelope around a
// ToolResult body. Most M2.2.2 tests inspect the rich-error fields
// on the wire, so they need to strip the envelope first.
func decodeToolResult(t *testing.T, raw json.RawMessage) ToolResult {
	t.Helper()
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v (raw: %s)", err, raw)
	}
	if len(env.Content) != 1 || env.Content[0].Type != "text" {
		t.Fatalf("envelope shape unexpected: %s", raw)
	}
	var tr ToolResult
	if err := json.Unmarshal([]byte(env.Content[0].Text), &tr); err != nil {
		t.Fatalf("decode body: %v (text: %s)", err, env.Content[0].Text)
	}
	return tr
}

// newTestHandler constructs a Handler suitable for testing the
// error-shape helpers directly (errorResult, stateRejectionResult).
// Queries is nil — the helpers under test don't touch the DB.
func newTestHandler() *Handler {
	return &Handler{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestRichErrorRequiredConstraint — missing `ticket_id` → Failure=
// validation, Constraint=required, Field="ticket_id", Hint non-empty.
// Pins the Required branch of renderHint.
func TestRichErrorRequiredConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { delete(m, "ticket_id") })
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Failure != FailureValidation {
		t.Errorf("Failure = %q; want %q", verr.Failure, FailureValidation)
	}
	if verr.Constraint != ConstraintRequired {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintRequired)
	}
	if verr.Field != "ticket_id" {
		t.Errorf("Field = %q; want ticket_id", verr.Field)
	}
	if verr.Hint == "" {
		t.Error("Hint is empty; FR-305 requires non-empty on every error")
	}
}

// TestRichErrorMinLengthConstraint — rationale length 2 → Failure=
// validation, Constraint=min_length, Expected describes the rule,
// Actual carries the rejected value.
func TestRichErrorMinLengthConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		d := m["diary_entry"].(map[string]any)
		d["rationale"] = "ok" // 2 chars, below RationaleMin=50
	})
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Constraint != ConstraintMinLength {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMinLength)
	}
	if !strings.Contains(verr.Expected, "min length 50") {
		t.Errorf("Expected = %q; want substring 'min length 50'", verr.Expected)
	}
	if verr.Actual != "ok" {
		t.Errorf("Actual = %q; want 'ok'", verr.Actual)
	}
	if !strings.Contains(verr.Hint, "diary_entry.rationale") {
		t.Errorf("Hint = %q; want substring 'diary_entry.rationale'", verr.Hint)
	}
}

// TestRichErrorMaxLengthConstraint — rationale > 4000 chars →
// Constraint=max_length, Hint non-empty.
func TestRichErrorMaxLengthConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		d := m["diary_entry"].(map[string]any)
		d["rationale"] = strings.Repeat("a", RationaleMax+1)
	})
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Constraint != ConstraintMaxLength {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMaxLength)
	}
	if verr.Hint == "" {
		t.Error("Hint is empty")
	}
}

// TestRichErrorMinItemsConstraint — empty kg_triples → Constraint=
// min_items, Field="kg_triples", Actual carries "0 items".
func TestRichErrorMinItemsConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["kg_triples"] = []any{} })
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Constraint != ConstraintMinItems {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMinItems)
	}
	if verr.Field != "kg_triples" {
		t.Errorf("Field = %q; want kg_triples", verr.Field)
	}
	if !strings.Contains(verr.Actual, "0") {
		t.Errorf("Actual = %q; want mention of 0 items", verr.Actual)
	}
}

// TestRichErrorMaxItemsConstraint — 101 kg_triples → Constraint=
// max_items, Field="kg_triples".
func TestRichErrorMaxItemsConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		template := map[string]any{
			"subject":    "agent",
			"predicate":  "did",
			"object":     "ticket",
			"valid_from": "now",
		}
		many := make([]any, KGTripleArrayMax+1)
		for i := range many {
			many[i] = template
		}
		m["kg_triples"] = many
	})
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Constraint != ConstraintMaxItems {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMaxItems)
	}
	if verr.Field != "kg_triples" {
		t.Errorf("Field = %q; want kg_triples", verr.Field)
	}
}

// TestRichErrorTypeMismatchConstraint — `outcome` emitted as a JSON
// number instead of a string → *json.UnmarshalTypeError routed to
// Constraint=type_mismatch per newDecodeOrTypeError.
func TestRichErrorTypeMismatchConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["outcome"] = 42 })
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Failure != FailureValidation {
		t.Errorf("Failure = %q; want %q (type mismatch is validation, not decode)",
			verr.Failure, FailureValidation)
	}
	if verr.Constraint != ConstraintTypeMismatch {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintTypeMismatch)
	}
	if verr.Expected == "" {
		t.Error("Expected is empty; should describe the required Go type")
	}
}

// TestRichErrorFormatConstraint — malformed UUID → Constraint=format,
// Field="ticket_id".
func TestRichErrorFormatConstraint(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["ticket_id"] = "not-a-uuid" })
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Constraint != ConstraintFormat {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintFormat)
	}
	if verr.Field != "ticket_id" {
		t.Errorf("Field = %q; want ticket_id", verr.Field)
	}
	if !strings.Contains(verr.Expected, "UUID") {
		t.Errorf("Expected = %q; want substring 'UUID'", verr.Expected)
	}
}

// TestRichErrorActualTruncatedTo100Chars — a min-length failure whose
// full Actual value is > 100 chars gets truncated to 100 at the
// tool_result wire layer (per FR-303). The in-memory ValidationError
// still carries the full value — truncation is a handler concern.
func TestRichErrorActualTruncatedTo100Chars(t *testing.T) {
	longStr := strings.Repeat("x", 200) // below RationaleMax, above 100
	// Use max-length to force a rejection we can control.
	verr := &ValidationError{
		ErrorType:  ErrorTypeSchema,
		Field:      "diary_entry.rationale",
		Message:    "rationale too long",
		Failure:    FailureValidation,
		Constraint: ConstraintMaxLength,
		Expected:   "string with max length 100",
		Actual:     longStr,
		Hint:       "shorten rationale",
	}
	h := newTestHandler()
	raw := h.errorResult(verr)
	tr := decodeToolResult(t, raw)
	if len(tr.Actual) != ActualTruncateMax {
		t.Errorf("Actual length on wire = %d; want %d (truncated per FR-303)",
			len(tr.Actual), ActualTruncateMax)
	}
}

// TestRichErrorAlreadyCommittedHasFailureState — the already-committed
// response per FR-260 + Clarification 2026-04-23 Q3 carries
// Failure="state" with empty Constraint/Expected/Actual/Line/Column/
// Excerpt and Hint describing the lifecycle objection. SC-301 explicit
// clause.
func TestRichErrorAlreadyCommittedHasFailureState(t *testing.T) {
	h := newTestHandler()
	raw := h.stateRejectionResult()
	tr := decodeToolResult(t, raw)

	if tr.Ok {
		t.Error("Ok = true; want false for state rejection")
	}
	if tr.Failure != FailureState {
		t.Errorf("Failure = %q; want %q", tr.Failure, FailureState)
	}
	if tr.Field != "" {
		t.Errorf("Field = %q; want empty for state rejection", tr.Field)
	}
	if tr.Constraint != ConstraintNone {
		t.Errorf("Constraint = %q; want empty for state rejection", tr.Constraint)
	}
	if tr.Expected != "" || tr.Actual != "" || tr.Excerpt != "" {
		t.Errorf("schema-shape fields non-empty: Expected=%q Actual=%q Excerpt=%q",
			tr.Expected, tr.Actual, tr.Excerpt)
	}
	if tr.Line != 0 || tr.Column != 0 {
		t.Errorf("Line=%d Column=%d; want 0 for state rejection", tr.Line, tr.Column)
	}
	if tr.Hint != "finalize_ticket already succeeded for this agent_instance" {
		t.Errorf("Hint = %q; want the lifecycle-objection message", tr.Hint)
	}
	// Q9 backward compat: M2.2.1 message field still carries the same text.
	if tr.Message != tr.Hint {
		t.Errorf("Message = %q; want equal to Hint for backward compat", tr.Message)
	}
}

// TestRichErrorDecodeFailureCarriesPosition — truncated JSON →
// Failure=decode with Line/Column/Excerpt populated, validation-only
// fields empty per Clarification Q1.
func TestRichErrorDecodeFailureCarriesPosition(t *testing.T) {
	raw := json.RawMessage(`{"ticket_id":`) // truncated mid-object
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Failure != FailureDecode {
		t.Errorf("Failure = %q; want %q", verr.Failure, FailureDecode)
	}
	if verr.Line <= 0 {
		t.Errorf("Line = %d; want > 0", verr.Line)
	}
	if verr.Column <= 0 {
		t.Errorf("Column = %d; want > 0", verr.Column)
	}
	// Validation-only fields must be empty (Clarification Q1 wire-
	// shape-stability).
	if verr.Constraint != ConstraintNone {
		t.Errorf("Constraint = %q; want empty for decode failure", verr.Constraint)
	}
	if verr.Expected != "" || verr.Actual != "" {
		t.Errorf("validation-only fields non-empty for decode: Expected=%q Actual=%q",
			verr.Expected, verr.Actual)
	}
	if verr.Hint == "" {
		t.Error("Hint is empty; FR-305 requires non-empty on every error")
	}
}
