package finalize

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// discardLogger returns a slog.Logger that drops every record. Local
// helper so handler_test.go doesn't depend on other test files in the
// package.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// TestUUIDString_ValidRendersCanonical pins the pgtype.UUID → canonical
// 8-4-4-4-12 hex conversion used in every structured-log line. The
// bytes chosen exercise the full hex alphabet including values that
// hit the high nibble.
func TestUUIDString_ValidRendersCanonical(t *testing.T) {
	u := pgtype.UUID{
		Bytes: [16]byte{
			0xc0, 0xba, 0xb6, 0x55, 0x06, 0x75, 0x4e, 0x8a,
			0xb5, 0xdd, 0xa6, 0xe3, 0xf2, 0xa2, 0xaf, 0x6a,
		},
		Valid: true,
	}
	got := uuidString(u)
	want := "c0bab655-0675-4e8a-b5dd-a6e3f2a2af6a"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestUUIDString_InvalidReturnsEmpty pins that a pgtype.UUID with
// Valid=false renders as an empty string, so a null ticket_id or
// agent_instance_id can't leak zero-bytes into logs as a bogus
// all-zero UUID.
func TestUUIDString_InvalidReturnsEmpty(t *testing.T) {
	if got := uuidString(pgtype.UUID{Valid: false}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestMCPContentEnvelope_ShapeAndDecode pins the MCP tool_result wire
// shape: `{"content":[{"type":"text","text":"<body>"}]}`. Tests decode
// the outer envelope then re-decode the inner `text` as JSON to confirm
// the body round-trips.
func TestMCPContentEnvelope_ShapeAndDecode(t *testing.T) {
	body := []byte(`{"ok":true,"attempt":1}`)
	env := mcpContentEnvelope(body)

	var outer map[string]any
	if err := json.Unmarshal(env, &outer); err != nil {
		t.Fatalf("envelope not valid JSON: %v", err)
	}
	content, _ := outer["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(content))
	}
	item, _ := content[0].(map[string]any)
	if typ, _ := item["type"].(string); typ != "text" {
		t.Errorf("content[0].type = %q, want \"text\"", typ)
	}
	text, _ := item["text"].(string)
	if text != string(body) {
		t.Errorf("content[0].text = %q, want %q", text, string(body))
	}

	var inner map[string]any
	if err := json.Unmarshal([]byte(text), &inner); err != nil {
		t.Fatalf("inner body not valid JSON: %v", err)
	}
}

// TestNewHandler_DefaultsNilLogger pins the nil-logger fallback. A
// caller that constructs a Handler with logger=nil should get a
// non-nil handler whose Logger field is slog.Default() — never nil.
func TestNewHandler_DefaultsNilLogger(t *testing.T) {
	h := NewHandler(nil, pgtype.UUID{Valid: true}, nil)
	if h == nil {
		t.Fatal("NewHandler returned nil")
	}
	if h.Logger == nil {
		t.Fatal("Handler.Logger should fall back to slog.Default(), got nil")
	}
}

// TestNewHandler_UsesProvidedLogger pins that an explicit logger is
// kept, not replaced with slog.Default().
func TestNewHandler_UsesProvidedLogger(t *testing.T) {
	logger := discardLogger()
	h := NewHandler(nil, pgtype.UUID{Valid: true}, logger)
	if h.Logger != logger {
		t.Fatal("Handler.Logger should retain the provided logger")
	}
}

// TestOkResult_IncludesAttempt pins that okResult's body reports the
// current attempts counter and ok=true, wrapped in the MCP envelope.
func TestOkResult_IncludesAttempt(t *testing.T) {
	h := &Handler{Logger: discardLogger(), attempts: 3}
	raw := h.okResult()
	body := decodeEnvelopeBody(t, raw)

	var result ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("body not valid ToolResult: %v", err)
	}
	if !result.Ok {
		t.Error("ok=false on success")
	}
	if result.Attempt != 3 {
		t.Errorf("attempt=%d, want 3", result.Attempt)
	}
}

// TestStateRejectionResult_ShapesMatchQ3 pins the already-committed
// (FR-260) response per Clarification Q3: failure="state", ErrorType
// kept for M2.2.1 compat, hint and message both non-empty and equal.
func TestStateRejectionResult_ShapesMatchQ3(t *testing.T) {
	h := &Handler{Logger: discardLogger(), attempts: 1}
	raw := h.stateRejectionResult()
	body := decodeEnvelopeBody(t, raw)

	var result ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("body not valid ToolResult: %v", err)
	}
	if result.Ok {
		t.Error("ok=true on state rejection")
	}
	if result.Failure != FailureState {
		t.Errorf("failure=%q, want %q", result.Failure, FailureState)
	}
	if result.Hint == "" || result.Message == "" {
		t.Error("hint and message should both be set on state rejection")
	}
	if result.Hint != result.Message {
		t.Errorf("hint (%q) should equal message (%q) per Q9 backward-compat", result.Hint, result.Message)
	}
}

// TestErrorResult_TruncatesActualField pins FR-303: the Actual field is
// capped at ActualTruncateMax (100 chars) in the wire shape, regardless
// of how long the raw value in the ValidationError was.
func TestErrorResult_TruncatesActualField(t *testing.T) {
	h := &Handler{Logger: discardLogger(), attempts: 1}
	verr := &ValidationError{
		ErrorType:  ErrorTypeSchema,
		Field:      "diary_entry.rationale",
		Message:    "too long",
		Failure:    FailureValidation,
		Constraint: ConstraintMaxLength,
		Expected:   "string with max length 100",
		Actual:     strings.Repeat("x", 300),
		Hint:       "shorten to <=100 chars",
	}
	raw := h.errorResult(verr)
	body := decodeEnvelopeBody(t, raw)

	var result ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("body not valid ToolResult: %v", err)
	}
	if len(result.Actual) != ActualTruncateMax {
		t.Errorf("len(Actual) = %d, want %d", len(result.Actual), ActualTruncateMax)
	}
	if result.Failure != FailureValidation {
		t.Errorf("Failure=%q, want %q", result.Failure, FailureValidation)
	}
	if result.Constraint != ConstraintMaxLength {
		t.Errorf("Constraint=%q, want %q", result.Constraint, ConstraintMaxLength)
	}
}

// TestErrorResult_NoTruncationBelowCap pins the non-truncation path:
// an Actual string shorter than ActualTruncateMax passes through
// unchanged.
func TestErrorResult_NoTruncationBelowCap(t *testing.T) {
	h := &Handler{Logger: discardLogger(), attempts: 2}
	verr := &ValidationError{
		ErrorType: ErrorTypeSchema,
		Field:     "ticket_id",
		Actual:    "short",
		Failure:   FailureValidation,
	}
	raw := h.errorResult(verr)
	body := decodeEnvelopeBody(t, raw)

	var result ToolResult
	_ = json.Unmarshal(body, &result)
	if result.Actual != "short" {
		t.Errorf("Actual=%q, want \"short\"", result.Actual)
	}
	if result.Attempt != 2 {
		t.Errorf("attempt=%d, want 2", result.Attempt)
	}
}

// TestHandle_NilQueriesReturnsStateFailure pins the
// checkAlreadyCommitted-error → stateFailure-envelope path. A Handler
// constructed with no Queries (e.g. test isolation) triggers the
// "internal error checking finalize state" branch, which must return
// a state-shaped rejection rather than crash.
func TestHandle_NilQueriesReturnsStateFailure(t *testing.T) {
	h := &Handler{Logger: discardLogger(), Queries: nil, attempts: 0}
	raw, err := h.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handle should not return Go error on DB failure, got %v", err)
	}

	body := decodeEnvelopeBody(t, raw)
	var result ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("body not valid ToolResult: %v", err)
	}
	if result.Ok {
		t.Error("ok=true on DB failure")
	}
	if result.Failure != FailureState {
		t.Errorf("Failure=%q, want %q", result.Failure, FailureState)
	}
	if !strings.Contains(result.Hint, "please retry") {
		t.Errorf("state-failure hint should mention retry, got %q", result.Hint)
	}
}

// decodeEnvelopeBody extracts the inner body JSON from an MCP
// tool_result envelope produced by mcpContentEnvelope.
func decodeEnvelopeBody(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatalf("envelope not valid JSON: %v", err)
	}
	if len(outer.Content) == 0 {
		t.Fatal("empty content array")
	}
	return []byte(outer.Content[0].Text)
}
