package finalize

// M9 T005 — mode switch + OneshotPayload tests. Ticket-mode behavior
// is pinned byte-for-byte by the pre-existing M2.2.1/M2.2.2 suites
// (FR-302); the tests here cover the oneshot side and the structural
// one-tool-per-mode guarantee (FR-304).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// newOneshotTestServer mirrors newTestServerLoop with the server (and
// handler) flipped into oneshot mode. Queries stays nil — the protocol
// tests below never reach the DB-backed double-commit guard (that
// guard's integration coverage lands with WriteFinalizeOneshot, T008).
func newOneshotTestServer() *server {
	h := &Handler{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Mode:   ModeOneshot,
	}
	return &server{
		handler: h,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		mode:    ModeOneshot,
	}
}

// validOneshotArgs is validPayload minus ticket_id — the baseline
// schema-valid finalize_oneshot arguments object.
func validOneshotArgs(t *testing.T) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validPayload()), &m); err != nil {
		t.Fatalf("decode valid payload: %v", err)
	}
	delete(m, "ticket_id")
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return raw
}

// mutateOneshotArgs applies a mutator to the baseline oneshot payload.
func mutateOneshotArgs(t *testing.T, mutate func(map[string]any)) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(validOneshotArgs(t), &m); err != nil {
		t.Fatalf("decode oneshot payload: %v", err)
	}
	mutate(m)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return raw
}

// goldenTicketDescriptorJSON is the frozen marshaled finalize_ticket
// descriptor (M9 review #6): generated once from the shipped code,
// which the post-M9 adversarial review verified matches the pre-M9
// bytes. The pin test compares BOTH the served tools/list entry AND
// ToolDescriptor() against this constant — a self-referential
// served-vs-ToolDescriptor comparison would silently track any future
// descriptor drift instead of catching it (FR-302).
const goldenTicketDescriptorJSON = `{"description":"Commit the ticket's structured completion. Schema: garrison.finalize_ticket.v1. The supervisor atomically writes the diary + KG triples to MemPalace and transitions the ticket when this call succeeds. This is the only way to complete a ticket in M2.2.1 onwards.","inputSchema":{"additionalProperties":false,"properties":{"diary_entry":{"additionalProperties":false,"properties":{"artifacts":{"items":{"maxLength":500,"type":"string"},"maxItems":50,"type":"array"},"blockers":{"items":{"maxLength":500,"type":"string"},"maxItems":50,"type":"array"},"discoveries":{"items":{"maxLength":500,"type":"string"},"maxItems":50,"type":"array"},"rationale":{"maxLength":4000,"minLength":50,"type":"string"}},"required":["rationale","artifacts","blockers","discoveries"],"type":"object"},"kg_triples":{"items":{"additionalProperties":false,"properties":{"object":{"minLength":3,"type":"string"},"predicate":{"minLength":3,"type":"string"},"subject":{"minLength":3,"type":"string"},"valid_from":{"description":"ISO-8601 timestamp or literal \"now\".","type":"string"}},"required":["subject","predicate","object","valid_from"],"type":"object"},"maxItems":100,"minItems":1,"type":"array"},"outcome":{"maxLength":500,"minLength":10,"type":"string"},"ticket_id":{"description":"UUID of the ticket being finalized.","type":"string"}},"required":["ticket_id","outcome","diary_entry","kg_triples"],"type":"object"},"name":"finalize_ticket"}`

// TestToolsListTicketModeUnchanged — a server with the zero-value mode
// (every M2.2.1-era construction path) returns exactly one tool whose
// JSON is byte-identical to the frozen pre-M9 golden, and
// ToolDescriptor() itself still marshals to the same golden — the
// finalize_ticket descriptor is byte-for-byte unaffected by the mode
// switch (FR-302, review #6).
func TestToolsListTicketModeUnchanged(t *testing.T) {
	srv := newTestServerLoop() // mode zero value → ticket
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	out := runLoop(t, srv, []string{req})
	responses := decodeResponses(t, out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses; want 1", len(responses))
	}
	result, _ := responses[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools count = %d; want exactly 1 (FR-304)", len(tools))
	}
	got, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatalf("re-marshal listed tool: %v", err)
	}
	if string(got) != goldenTicketDescriptorJSON {
		t.Errorf("ticket-mode tools/list descriptor drifted from the pre-M9 golden:\n   got: %s\ngolden: %s", got, goldenTicketDescriptorJSON)
	}
	descriptor, err := json.Marshal(ToolDescriptor())
	if err != nil {
		t.Fatalf("marshal ToolDescriptor: %v", err)
	}
	if string(descriptor) != goldenTicketDescriptorJSON {
		t.Errorf("ToolDescriptor() drifted from the pre-M9 golden:\n   got: %s\ngolden: %s", descriptor, goldenTicketDescriptorJSON)
	}

	// Explicit ModeTicket must behave identically to the zero value.
	srvExplicit := newTestServerLoop()
	srvExplicit.mode = ModeTicket
	outExplicit := runLoop(t, srvExplicit, []string{req})
	if outExplicit != out {
		t.Errorf("explicit ticket mode differs from zero-value mode:\n got: %s\nwant: %s", outExplicit, out)
	}
}

// TestToolsListOneshotModeSingleTool — oneshot mode lists exactly one
// tool, finalize_oneshot, and its schema carries no ticket_id (FR-304:
// the oneshot agent structurally cannot see finalize_ticket).
func TestToolsListOneshotModeSingleTool(t *testing.T) {
	srv := newOneshotTestServer()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	out := runLoop(t, srv, []string{req})
	responses := decodeResponses(t, out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses; want 1", len(responses))
	}
	result, _ := responses[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools count = %d; want exactly 1 (FR-304)", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "finalize_oneshot" {
		t.Errorf("tool name = %v; want finalize_oneshot", tool["name"])
	}
	desc, _ := tool["description"].(string)
	if !strings.Contains(desc, OneshotSchemaVersion) {
		t.Errorf("description missing schema version %q: %s", OneshotSchemaVersion, desc)
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if _, present := props["ticket_id"]; present {
		t.Error("oneshot inputSchema.properties carries ticket_id; FR-301 forbids it")
	}
	for _, key := range []string{"outcome", "diary_entry", "kg_triples"} {
		if _, present := props[key]; !present {
			t.Errorf("oneshot inputSchema.properties missing %q", key)
		}
	}
	required, _ := schema["required"].([]any)
	if len(required) != 3 {
		t.Errorf("required = %v; want exactly [outcome diary_entry kg_triples]", required)
	}
	for _, r := range required {
		if r == "ticket_id" {
			t.Error("oneshot required list carries ticket_id")
		}
	}
}

// TestOneshotPayloadRejectsTicketID — a finalize_oneshot payload that
// smuggles a ticket_id is rejected as an unknown field (FR-301: the
// tool MUST NOT accept a ticket identifier).
func TestOneshotPayloadRejectsTicketID(t *testing.T) {
	raw := mutateOneshotArgs(t, func(m map[string]any) {
		m["ticket_id"] = "0d4f6f4e-2c2b-4e1c-9c39-30bd95a3f0aa"
	})
	payload, verr := ValidateOneshot(raw)
	if payload != nil {
		t.Fatal("payload non-nil; want rejection")
	}
	if verr == nil {
		t.Fatal("expected ValidationError; got nil")
	}
	if verr.Field != "ticket_id" {
		t.Errorf("Field = %q; want ticket_id", verr.Field)
	}
	if verr.Failure != FailureValidation {
		t.Errorf("Failure = %q; want %q", verr.Failure, FailureValidation)
	}
	if verr.Constraint != ConstraintUnknownField {
		t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintUnknownField)
	}
	if verr.Hint == "" {
		t.Error("Hint is empty; FR-305 requires non-empty on every error")
	}
}

// TestOneshotPayloadValidatesDiaryAndTriples — ValidateOneshot reuses
// the finalize_ticket bounds verbatim (FR-301 "payload shape of
// finalize_ticket minus the ticket identifier"): diary rationale and
// KG-triple constraints reject identically, "now" substitutes, and the
// baseline payload passes.
func TestOneshotPayloadValidatesDiaryAndTriples(t *testing.T) {
	t.Run("happy path with now substitution", func(t *testing.T) {
		before := time.Now().UTC()
		payload, verr := ValidateOneshot(validOneshotArgs(t))
		if verr != nil {
			t.Fatalf("unexpected ValidationError: %v", verr)
		}
		if payload.Outcome == "" {
			t.Error("Outcome empty after validation")
		}
		if len(payload.KGTriples) == 0 {
			t.Fatal("KGTriples empty after validation")
		}
		for i, tr := range payload.KGTriples {
			if tr.ValidFrom.IsZero() {
				t.Errorf("KGTriples[%d].ValidFrom is zero; want concrete time", i)
			}
			if tr.ValidFrom.Before(before.Add(-time.Minute)) {
				t.Errorf("KGTriples[%d].ValidFrom = %v; suspiciously old", i, tr.ValidFrom)
			}
		}
	})

	t.Run("rationale below minimum rejected", func(t *testing.T) {
		raw := mutateOneshotArgs(t, func(m map[string]any) {
			d := m["diary_entry"].(map[string]any)
			d["rationale"] = "too short"
		})
		_, verr := ValidateOneshot(raw)
		if verr == nil {
			t.Fatal("expected ValidationError; got nil")
		}
		if verr.Field != "diary_entry.rationale" {
			t.Errorf("Field = %q; want diary_entry.rationale", verr.Field)
		}
		if verr.Constraint != ConstraintMinLength {
			t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMinLength)
		}
	})

	t.Run("empty kg_triples rejected", func(t *testing.T) {
		raw := mutateOneshotArgs(t, func(m map[string]any) {
			m["kg_triples"] = []any{}
		})
		_, verr := ValidateOneshot(raw)
		if verr == nil {
			t.Fatal("expected ValidationError; got nil")
		}
		if verr.Field != "kg_triples" {
			t.Errorf("Field = %q; want kg_triples", verr.Field)
		}
		if verr.Constraint != ConstraintMinItems {
			t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMinItems)
		}
	})

	t.Run("triple field below minimum rejected", func(t *testing.T) {
		raw := mutateOneshotArgs(t, func(m map[string]any) {
			triples := m["kg_triples"].([]any)
			triple := triples[0].(map[string]any)
			triple["subject"] = "ab"
		})
		_, verr := ValidateOneshot(raw)
		if verr == nil {
			t.Fatal("expected ValidationError; got nil")
		}
		if verr.Field != "kg_triples[0].subject" {
			t.Errorf("Field = %q; want kg_triples[0].subject", verr.Field)
		}
		if verr.Constraint != ConstraintMinLength {
			t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMinLength)
		}
	})

	t.Run("outcome below minimum rejected", func(t *testing.T) {
		raw := mutateOneshotArgs(t, func(m map[string]any) {
			m["outcome"] = "short"
		})
		_, verr := ValidateOneshot(raw)
		if verr == nil {
			t.Fatal("expected ValidationError; got nil")
		}
		if verr.Field != "outcome" {
			t.Errorf("Field = %q; want outcome", verr.Field)
		}
		if verr.Constraint != ConstraintMinLength {
			t.Errorf("Constraint = %q; want %q", verr.Constraint, ConstraintMinLength)
		}
	})
}

// TestOneshotModeRejectsFinalizeTicketCall — in oneshot mode a
// tools/call naming finalize_ticket is refused with JSON-RPC -32601
// (the unknown-tool path); the ticket-commit surface is structurally
// unreachable for an oneshot spawn (FR-304).
func TestOneshotModeRejectsFinalizeTicketCall(t *testing.T) {
	srv := newOneshotTestServer()
	req := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"finalize_ticket","arguments":{}}}`
	out := runLoop(t, srv, []string{req})
	responses := decodeResponses(t, out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses; want 1", len(responses))
	}
	errObj, _ := responses[0]["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected error response; got: %s", out)
	}
	code, _ := errObj["code"].(float64)
	if int(code) != errCodeMethodNotFound {
		t.Errorf("error code = %d; want %d", int(code), errCodeMethodNotFound)
	}
}

// ----------------------------------------------------------------------------
// handleOneshot coverage (M9 T020 top-up): the oneshot Handle path's
// double-commit guard and validation routing, exercised through a
// scripted store.DBTX so the unit suite reaches the same branches the
// live guard takes against Postgres (the DB-backed end-to-end pass is
// TestWriteFinalizeOneshotRejectsDoubleCommit, T008).
// ----------------------------------------------------------------------------

// oneshotStubRow scripts SelectScheduledTaskRunFinalizedState's Scan:
// dest[0] is &row.Finalized (interface{}), which receives the scripted
// value; a non-nil err short-circuits like a transport failure.
type oneshotStubRow struct {
	finalized any
	err       error
}

func (r oneshotStubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*interface{}); ok {
			*p = r.finalized
		}
	}
	return nil
}

// oneshotStubDBTX satisfies store.DBTX, serving the scripted row for
// every QueryRow (the guard issues exactly one).
type oneshotStubDBTX struct{ row oneshotStubRow }

func (oneshotStubDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (oneshotStubDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (s oneshotStubDBTX) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return s.row
}

// newOneshotHandler builds an oneshot-mode Handler over a scripted
// guard row.
func newOneshotHandler(row oneshotStubRow) *Handler {
	return &Handler{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Mode:           ModeOneshot,
		Queries:        store.New(oneshotStubDBTX{row: row}),
		ScheduledRunID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
	}
}

// oneshotHandleResult runs Handle and decodes the ToolResult body.
func oneshotHandleResult(t *testing.T, h *Handler, args json.RawMessage) ToolResult {
	t.Helper()
	raw, err := h.Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle returned Go error: %v", err)
	}
	body := decodeEnvelopeBody(t, raw)
	var result ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("body not valid ToolResult: %v", err)
	}
	return result
}

// TestOneshotHandleNilQueriesStateFailure — an oneshot-mode Handler
// with no Queries binding reports the internal state-check failure as
// a state-shaped rejection (mirrors ticket mode's
// TestHandle_NilQueriesReturnsStateFailure) rather than crashing.
func TestOneshotHandleNilQueriesStateFailure(t *testing.T) {
	h := &Handler{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Mode:           ModeOneshot,
		ScheduledRunID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
	}
	result := oneshotHandleResult(t, h, validOneshotArgs(t))
	if result.Ok {
		t.Error("ok=true on guard failure")
	}
	if result.Failure != FailureState {
		t.Errorf("Failure = %q; want %q", result.Failure, FailureState)
	}
	if !strings.Contains(result.Hint, "please retry") {
		t.Errorf("hint = %q; want a retry hint", result.Hint)
	}
}

// TestOneshotHandleGuardErrorStateFailure — a transport error from the
// guard query maps to the same internal state-check rejection.
func TestOneshotHandleGuardErrorStateFailure(t *testing.T) {
	h := newOneshotHandler(oneshotStubRow{err: errors.New("connection reset")})
	result := oneshotHandleResult(t, h, validOneshotArgs(t))
	if result.Ok {
		t.Error("ok=true on guard transport failure")
	}
	if result.Failure != FailureState {
		t.Errorf("Failure = %q; want %q", result.Failure, FailureState)
	}
}

// TestOneshotHandleUnexpectedFinalizedType — a finalized column that
// is not a bool (defensive: the query computes IS NOT NULL, so bool is
// guaranteed today) maps to the internal state-check rejection, not a
// crash or a silent pass.
func TestOneshotHandleUnexpectedFinalizedType(t *testing.T) {
	h := newOneshotHandler(oneshotStubRow{finalized: "yes"})
	result := oneshotHandleResult(t, h, validOneshotArgs(t))
	if result.Ok {
		t.Error("ok=true on unexpected finalized column type")
	}
	if result.Failure != FailureState {
		t.Errorf("Failure = %q; want %q", result.Failure, FailureState)
	}
}

// TestOneshotHandleAlreadyCommittedRejects — finalized=true (the run
// row already carries structured_outcome) rejects with the oneshot
// lifecycle objection BEFORE validating the repeated payload, even a
// schema-valid one (FR-260 analog).
func TestOneshotHandleAlreadyCommittedRejects(t *testing.T) {
	h := newOneshotHandler(oneshotStubRow{finalized: true})
	result := oneshotHandleResult(t, h, validOneshotArgs(t))
	if result.Ok {
		t.Error("ok=true on already-committed run")
	}
	if result.Failure != FailureState {
		t.Errorf("Failure = %q; want %q", result.Failure, FailureState)
	}
	const wantMsg = "finalize_oneshot already committed for this scheduled task run"
	if result.Message != wantMsg {
		t.Errorf("Message = %q; want %q", result.Message, wantMsg)
	}
	if result.Hint != wantMsg {
		t.Errorf("Hint = %q; want %q (Q9 message/hint parity)", result.Hint, wantMsg)
	}
}

// TestOneshotHandleSchemaRejection — with the guard reporting not yet
// finalized, an invalid payload flows to ValidateOneshot and returns
// the schema rejection envelope.
func TestOneshotHandleSchemaRejection(t *testing.T) {
	h := newOneshotHandler(oneshotStubRow{finalized: false})
	args := mutateOneshotArgs(t, func(m map[string]any) { delete(m, "outcome") })
	result := oneshotHandleResult(t, h, args)
	if result.Ok {
		t.Error("ok=true on schema-invalid payload")
	}
	if result.Field != "outcome" {
		t.Errorf("Field = %q; want outcome", result.Field)
	}
	if result.Attempt != 1 {
		t.Errorf("Attempt = %d; want 1", result.Attempt)
	}
}

// TestOneshotHandleAcceptsValidPayload — guard clear + schema-valid
// payload returns ok=true; the supervisor-side observer (not the
// handler) owns the WriteFinalizeOneshot commit.
func TestOneshotHandleAcceptsValidPayload(t *testing.T) {
	h := newOneshotHandler(oneshotStubRow{finalized: false})
	result := oneshotHandleResult(t, h, validOneshotArgs(t))
	if !result.Ok {
		t.Errorf("ok=false on valid payload: %+v", result)
	}
	if result.Attempt != 1 {
		t.Errorf("Attempt = %d; want 1", result.Attempt)
	}
}
