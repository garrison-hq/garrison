package spawn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// -------- Adjudicate decision table -------------------------------------

func TestAdjudicateSuccess(t *testing.T) {
	r := Result{
		ResultSeen:     true,
		TerminalReason: "success",
		TotalCostUSD:   "0.003",
		IsError:        false,
	}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true, FinalizeState{})
	if status != "succeeded" || reason != ExitCompleted {
		t.Errorf("Adjudicate(success) = (%q, %q); want (succeeded, completed)", status, reason)
	}
}

func TestAdjudicateClaudeError(t *testing.T) {
	r := Result{
		ResultSeen:     true,
		TerminalReason: "error_during_execution",
		TotalCostUSD:   "0.004",
		IsError:        true,
	}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true, FinalizeState{})
	if status != "failed" || reason != ExitClaudeError {
		t.Errorf("Adjudicate(claude_error) = (%q, %q); want (failed, claude_error)", status, reason)
	}
}

func TestAdjudicateNoResult(t *testing.T) {
	r := Result{ResultSeen: false}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true, FinalizeState{})
	if status != "failed" || reason != ExitNoResult {
		t.Errorf("Adjudicate(no_result) = (%q, %q); want (failed, no_result)", status, reason)
	}
}

func TestAdjudicateAcceptanceFailed(t *testing.T) {
	r := Result{ResultSeen: true, IsError: false, TotalCostUSD: "0.001"}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, false, FinalizeState{})
	if status != "failed" || reason != ExitAcceptanceFailed {
		t.Errorf("Adjudicate(acceptance_failed) = (%q, %q); want (failed, acceptance_failed)", status, reason)
	}
}

func TestAdjudicateTimeout(t *testing.T) {
	status, reason := Adjudicate(Result{}, WaitDetail{ContextErr: context.DeadlineExceeded}, true, FinalizeState{})
	if status != "timeout" || reason != ExitTimeout {
		t.Errorf("Adjudicate(timeout) = (%q, %q); want (timeout, timeout)", status, reason)
	}
}

func TestAdjudicateShutdown(t *testing.T) {
	status, reason := Adjudicate(Result{}, WaitDetail{ShutdownInitiated: true, ContextErr: context.Canceled}, true, FinalizeState{})
	if status != "failed" || reason != ExitSupervisorShutdown {
		t.Errorf("Adjudicate(shutdown) = (%q, %q); want (failed, supervisor_shutdown)", status, reason)
	}
}

func TestAdjudicateMCPBail(t *testing.T) {
	r := Result{
		MCPBailed:         true,
		MCPOffenderName:   "postgres",
		MCPOffenderStatus: "failed",
	}
	status, reason := Adjudicate(r, WaitDetail{}, true, FinalizeState{})
	if status != "failed" || reason != "mcp_postgres_failed" {
		t.Errorf("Adjudicate(mcp_bail) = (%q, %q); want (failed, mcp_postgres_failed)", status, reason)
	}
}

func TestAdjudicateParseError(t *testing.T) {
	r := Result{ParseError: true}
	status, reason := Adjudicate(r, WaitDetail{}, true, FinalizeState{})
	if status != "failed" || reason != ExitParseError {
		t.Errorf("Adjudicate(parse_error) = (%q, %q); want (failed, parse_error)", status, reason)
	}
}

func TestAdjudicateSignaled(t *testing.T) {
	r := Result{ResultSeen: false}
	status, reason := Adjudicate(r, WaitDetail{Signaled: true, Signal: syscall.SIGKILL, ExitCode: -1}, true, FinalizeState{})
	if status != "failed" {
		t.Errorf("Adjudicate(signaled) status = %q; want failed", status)
	}
	if !strings.HasPrefix(reason, "signaled_") {
		t.Errorf("Adjudicate(signaled) reason = %q; want signaled_* prefix", reason)
	}
}

// Precedence pins — plan §pipeline.Adjudicate calls these out by name.

func TestAdjudicateTimeoutOutranksNoResult(t *testing.T) {
	r := Result{ResultSeen: false}
	status, reason := Adjudicate(r, WaitDetail{ContextErr: context.DeadlineExceeded}, true, FinalizeState{})
	if reason != ExitTimeout {
		t.Errorf("timeout+no_result → %q; want timeout (timeout outranks no_result)", reason)
	}
	if status != "timeout" {
		t.Errorf("status = %q; want timeout", status)
	}
}

func TestAdjudicateShutdownOutranksClaudeError(t *testing.T) {
	r := Result{ResultSeen: true, IsError: true, TotalCostUSD: "0.001"}
	status, reason := Adjudicate(r, WaitDetail{ShutdownInitiated: true, ContextErr: context.Canceled}, true, FinalizeState{})
	if reason != ExitSupervisorShutdown {
		t.Errorf("shutdown+claude_error → %q; want supervisor_shutdown", reason)
	}
	if status != "failed" {
		t.Errorf("status = %q; want failed", status)
	}
}

func TestAdjudicateMCPBailOutranksEverything(t *testing.T) {
	r := Result{
		MCPBailed:         true,
		MCPOffenderName:   "postgres",
		MCPOffenderStatus: "failed",
		ParseError:        true, // would otherwise say parse_error
		ResultSeen:        true, // would otherwise outrank acceptance
		IsError:           true, // would otherwise say claude_error
	}
	_, reason := Adjudicate(r, WaitDetail{ShutdownInitiated: true, ContextErr: context.Canceled, Signaled: true, Signal: syscall.SIGKILL}, false, FinalizeState{})
	if reason != "mcp_postgres_failed" {
		t.Errorf("mcp bail should win over every other cause; got %q", reason)
	}
}

// -------- pipeline.Run behaviour ----------------------------------------

// fixtureInit is a minimal system/init line with a single postgres MCP
// server reported as connected. Keeps the test independent of the long
// sample in docs/research/m2-spike.md.
const (
	fixtureInit = `{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-haiku-4-5-20251001","cwd":"/workspaces/engineering","tools":["Read","Write"],"mcp_servers":[{"name":"postgres","status":"connected"}]}`

	fixtureAssistant = `{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"hi"}]}}`

	fixtureUser = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":false,"content":[{"type":"text","text":"ok"}]}]}}`

	fixtureRateLimit = `{"type":"rate_limit_event","session_id":"sess-1","uuid":"u-1","rate_limit_info":{"status":"warn","resetsAt":1700000000,"rateLimitType":"input","utilization":0.8,"isUsingOverage":false,"surpassedThreshold":0.75}}`

	fixtureResult = `{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"duration_api_ms":1000,"total_cost_usd":0.003,"stop_reason":"end_turn","result":"done","session_id":"sess-1"}`

	fixtureInitBadMCP = `{"type":"system","subtype":"init","session_id":"sess-1","mcp_servers":[{"name":"postgres","status":"failed"}]}`

	fixtureUnknown = `{"type":"mystery","subtype":"new-thing"}`

	fixtureMalformed = `{"type":"result","subtype":` // truncated
)

func TestPipelineRunRoutesAllEvents(t *testing.T) {
	stream := strings.Join([]string{
		fixtureInit,
		fixtureAssistant,
		fixtureUser,
		fixtureRateLimit,
		fixtureResult,
	}, "\n") + "\n"

	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, pgtype.UUID{}, discardLogger(), nil)
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	if !r.ResultSeen {
		t.Errorf("ResultSeen=false; want true after result event")
	}
	if !r.AssistantSeen {
		t.Errorf("AssistantSeen=false; want true after assistant event")
	}
	if r.MCPBailed {
		t.Errorf("MCPBailed=true; want false (init was healthy)")
	}
	if r.ParseError {
		t.Errorf("ParseError=true; want false")
	}
	if r.TotalCostUSD != "0.003" {
		t.Errorf("TotalCostUSD = %q; want 0.003 (json.Number preserves input)", r.TotalCostUSD)
	}
	if r.TerminalReason != "success" {
		t.Errorf("TerminalReason = %q; want success", r.TerminalReason)
	}
	if r.SessionID != "sess-1" {
		t.Errorf("SessionID = %q; want sess-1", r.SessionID)
	}
}

func TestPipelineRunMCPBailInvokesCallback(t *testing.T) {
	stream := fixtureInitBadMCP + "\n"
	var bailReason string
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, pgtype.UUID{}, discardLogger(),
		func(reason string) { bailReason = reason })
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	if !r.MCPBailed {
		t.Fatalf("MCPBailed=false; want true")
	}
	if r.MCPOffenderName != "postgres" || r.MCPOffenderStatus != "failed" {
		t.Errorf("offender = (%q, %q); want (postgres, failed)",
			r.MCPOffenderName, r.MCPOffenderStatus)
	}
	if bailReason != "mcp_postgres_failed" {
		t.Errorf("onBail reason = %q; want mcp_postgres_failed", bailReason)
	}
}

func TestPipelineRunParseErrorBails(t *testing.T) {
	stream := fixtureInit + "\n" + fixtureMalformed + "\n"
	var bailReason string
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, pgtype.UUID{}, discardLogger(),
		func(reason string) { bailReason = reason })
	if err == nil {
		t.Fatal("Run: want parse error, got nil")
	}
	if !r.ParseError {
		t.Error("ParseError=false; want true")
	}
	if bailReason != ExitParseError {
		t.Errorf("onBail reason = %q; want parse_error", bailReason)
	}
}

func TestPipelineRunTreatsUnknownEventAsContinue(t *testing.T) {
	stream := fixtureInit + "\n" + fixtureUnknown + "\n" + fixtureResult + "\n"
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, pgtype.UUID{}, discardLogger(), nil)
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	if r.ParseError {
		t.Error("ParseError=true; want false (unknown is not a parse error)")
	}
	if !r.ResultSeen {
		t.Error("ResultSeen=false; want true — run should have continued past the unknown event")
	}
}

func TestPipelineRunSkipsBlankLines(t *testing.T) {
	stream := "\n\n" + fixtureInit + "\n\n" + fixtureResult + "\n\n"
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, pgtype.UUID{}, discardLogger(), nil)
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	if !r.ResultSeen {
		t.Error("ResultSeen=false; blank lines should be ignored, not bail the pipeline")
	}
}

// A fake reader that returns lines then a synthetic non-EOF error, used to
// verify Run surfaces non-EOF read errors.
type erroringReader struct {
	remaining []byte
	err       error
}

func (e *erroringReader) Read(p []byte) (int, error) {
	if len(e.remaining) > 0 {
		n := copy(p, e.remaining)
		e.remaining = e.remaining[n:]
		return n, nil
	}
	return 0, e.err
}

func TestPipelineRunSurfacesReadError(t *testing.T) {
	want := errors.New("synthetic io failure")
	reader := &erroringReader{remaining: []byte(fixtureInit + "\n"), err: want}
	_, err := Run(context.Background(), reader, pgtype.UUID{}, pgtype.UUID{}, discardLogger(), nil)
	if err == nil {
		t.Fatal("Run: want error from reader, got nil")
	}
}

func TestPipelineRunRequiresLogger(t *testing.T) {
	_, err := Run(context.Background(), bytes.NewBufferString(""), pgtype.UUID{}, pgtype.UUID{}, nil, nil)
	if err == nil {
		t.Error("Run(nil logger): want error, got nil")
	}
}

// -------- uuidString ----------------------------------------------------

func TestUUIDStringFormatsCanonical(t *testing.T) {
	var u pgtype.UUID
	// 01020304-0506-0708-090a-0b0c0d0e0f10
	for i := range u.Bytes {
		u.Bytes[i] = byte(i + 1)
	}
	u.Valid = true
	got := uuidString(u)
	want := "01020304-0506-0708-090a-0b0c0d0e0f10"
	if got != want {
		t.Errorf("uuidString() = %q; want %q", got, want)
	}
}

func TestUUIDStringEmptyForInvalid(t *testing.T) {
	var u pgtype.UUID
	if got := uuidString(u); got != "" {
		t.Errorf("uuidString(invalid) = %q; want empty", got)
	}
}

// recordingHandler captures every slog record into an in-memory slice so
// tests can assert on specific log-line field shapes. Not concurrency-
// safe (tests call it serially).
type recordingHandler struct {
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

// recordAttrs extracts all {key → value} pairs from a slog.Record for
// straightforward assertion.
func recordAttrs(r slog.Record) map[string]any {
	out := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.Any()
		return true
	})
	return out
}

// TestPipelineLogsMempalaceToolUsePairs verifies FR-218: for every
// mempalace_* tool_use the pipeline emits a pending info-level log, and
// for every matching tool_result it emits a follow-up info-level log
// with outcome ∈ {"ok", "error"}.
func TestPipelineLogsMempalaceToolUsePairs(t *testing.T) {
	// NDJSON: init → assistant(tool_use mempalace_add_drawer) → user(ok)
	//         → assistant(tool_use mempalace_kg_add)           → user(error)
	//         → result success.
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","cwd":"/","session_id":"s","model":"m","tools":[],"mcp_servers":[{"name":"postgres","status":"connected"},{"name":"mempalace","status":"connected"}]}`,
		`{"type":"assistant","message":{"model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"mempalace_add_drawer","input":{"wing":"w","room":"hall_events","content":"..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":false,"content":[{"type":"text","text":"ok"}]}]}}`,
		`{"type":"assistant","message":{"model":"m","content":[{"type":"tool_use","id":"toolu_2","name":"mempalace_kg_add","input":{"subject":"a","predicate":"p","object":"b"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":[{"type":"text","text":"kg write failed"}]}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":100,"total_cost_usd":0.05,"stop_reason":"end_turn"}`,
	}, "\n")

	rec := &recordingHandler{}
	logger := slog.New(rec)

	_, err := Run(context.Background(), strings.NewReader(stream), pgtype.UUID{}, pgtype.UUID{}, logger, nil)
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}

	// Count mempalace tool_use + tool_result lines.
	var toolUses, toolResults []slog.Record
	for _, r := range rec.records {
		switch r.Message {
		case "mempalace tool_use":
			toolUses = append(toolUses, r)
		case "mempalace tool_result":
			toolResults = append(toolResults, r)
		}
	}
	if len(toolUses) != 2 {
		t.Errorf("mempalace tool_use lines=%d; want 2", len(toolUses))
	}
	if len(toolResults) != 2 {
		t.Errorf("mempalace tool_result lines=%d; want 2", len(toolResults))
	}

	// Outcome on tool_uses must be 'pending'.
	for _, tu := range toolUses {
		if recordAttrs(tu)["outcome"] != "pending" {
			t.Errorf("tool_use outcome=%v; want pending", recordAttrs(tu)["outcome"])
		}
	}
	// Outcome on the two tool_results: ok, then error.
	outcomes := []any{recordAttrs(toolResults[0])["outcome"], recordAttrs(toolResults[1])["outcome"]}
	if outcomes[0] != "ok" || outcomes[1] != "error" {
		t.Errorf("tool_result outcomes=%v; want [ok error]", outcomes)
	}

	// All four lines must carry tool_name + tool_use_id.
	for i, r := range append(append([]slog.Record{}, toolUses...), toolResults...) {
		attrs := recordAttrs(r)
		if attrs["tool_name"] == nil || attrs["tool_use_id"] == nil {
			t.Errorf("line %d missing tool_name/tool_use_id: %v", i, attrs)
		}
	}
}

// TestAdjudicateBudgetExceeded — M2.2 / FR-220: result with TerminalReason
// containing "budget" → (failed, budget_exceeded).
func TestAdjudicateBudgetExceeded(t *testing.T) {
	got, reason := Adjudicate(
		Result{ResultSeen: true, IsError: false, TerminalReason: "budget_exceeded", TotalCostUSD: "0.11"},
		WaitDetail{ExitCode: 0},
		false, // helloTxtOK — irrelevant; budget path outranks
		FinalizeState{},
	)
	if got != "failed" || reason != ExitBudgetExceeded {
		t.Errorf("got (%s, %s); want (failed, %s)", got, reason, ExitBudgetExceeded)
	}
}

// TestAdjudicateBudgetCaseInsensitive — Max_Budget_USD_Exceeded / other
// capitalizations should also match.
func TestAdjudicateBudgetCaseInsensitive(t *testing.T) {
	cases := []string{"budget_exceeded", "Budget_Exceeded", "MAX_BUDGET_USD_EXCEEDED", "stopped_due_to_budget"}
	for _, tr := range cases {
		t.Run(tr, func(t *testing.T) {
			got, reason := Adjudicate(
				Result{ResultSeen: true, TerminalReason: tr, TotalCostUSD: "0.11"},
				WaitDetail{ExitCode: 0},
				false,
				FinalizeState{},
			)
			if got != "failed" || reason != ExitBudgetExceeded {
				t.Errorf("terminal_reason=%q → (%s, %s); want (failed, %s)", tr, got, reason, ExitBudgetExceeded)
			}
		})
	}
}

// TestAdjudicateBudgetDoesNotOverrideMCPBail — MCPBail precedence wins.
func TestAdjudicateBudgetDoesNotOverrideMCPBail(t *testing.T) {
	got, reason := Adjudicate(
		Result{
			MCPBailed:         true,
			MCPOffenderName:   "mempalace",
			MCPOffenderStatus: "failed",
			ResultSeen:        true,
			TerminalReason:    "budget_exceeded",
		},
		WaitDetail{ExitCode: 0},
		false,
		FinalizeState{},
	)
	if got != "failed" || reason != "mcp_mempalace_failed" {
		t.Errorf("got (%s, %s); want (failed, mcp_mempalace_failed) — MCP bail must outrank budget", got, reason)
	}
}

// TestAdjudicateBudgetWinsOverFinalizeInvalid — M2.2.1 T002 / SC-258:
// when the retry counter SIGTERMs the subprocess AND the subprocess
// already reported a budget-shaped result, budget_exceeded is the
// canonical exit reason (not finalize_invalid).
func TestAdjudicateBudgetWinsOverFinalizeInvalid(t *testing.T) {
	got, reason := Adjudicate(
		Result{ResultSeen: true, TerminalReason: "budget_exceeded", TotalCostUSD: "0.10"},
		WaitDetail{Signaled: true, Signal: syscall.SIGTERM, ExitCode: -1},
		true, // helloTxtOK irrelevant
		FinalizeState{Expected: true, Attempted: true, CapExhausted: true},
	)
	if got != "failed" || reason != ExitBudgetExceeded {
		t.Errorf("got (%s, %s); want (failed, %s) — budget must outrank finalize_invalid",
			got, reason, ExitBudgetExceeded)
	}
}

// TestAdjudicateTimeoutWinsOverNeverCalled — M2.2.1 T002: when a
// subprocess times out before the agent can even attempt finalize,
// the exit reason is "timeout", not "finalize_never_called". Timeout
// is the external cause; finalize_never_called is the internal
// observation that would apply if the subprocess exited cleanly.
func TestAdjudicateTimeoutWinsOverNeverCalled(t *testing.T) {
	got, reason := Adjudicate(
		Result{ResultSeen: false},
		WaitDetail{ContextErr: context.DeadlineExceeded},
		true,
		FinalizeState{Expected: true, Attempted: false, Committed: false},
	)
	if got != "timeout" || reason != ExitTimeout {
		t.Errorf("got (%s, %s); want (timeout, %s) — timeout must outrank finalize_never_called",
			got, reason, ExitTimeout)
	}
}

// TestAdjudicateFinalizeInvalidOnSignaledCapExhausted — the counter-
// driven SIGTERM path: subprocess killed via SIGTERM with
// CapExhausted=true and no budget result should yield finalize_invalid
// rather than the generic signaled_SIGTERM.
func TestAdjudicateFinalizeInvalidOnSignaledCapExhausted(t *testing.T) {
	got, reason := Adjudicate(
		Result{ResultSeen: true, TerminalReason: "stopped_by_user", TotalCostUSD: "0.05"},
		WaitDetail{Signaled: true, Signal: syscall.SIGTERM, ExitCode: -1},
		true,
		FinalizeState{Expected: true, Attempted: true, CapExhausted: true},
	)
	if got != "failed" || reason != ExitFinalizeInvalid {
		t.Errorf("got (%s, %s); want (failed, %s)", got, reason, ExitFinalizeInvalid)
	}
}

// TestAdjudicateFinalizeNeverCalledOnCleanExit — subprocess exits
// cleanly without ever calling finalize_ticket; for roles that expect
// finalize, Adjudicate classifies as finalize_never_called.
func TestAdjudicateFinalizeNeverCalledOnCleanExit(t *testing.T) {
	got, reason := Adjudicate(
		Result{ResultSeen: true, TerminalReason: "success", IsError: false, TotalCostUSD: "0.02"},
		WaitDetail{ExitCode: 0},
		true, // helloTxtOK=true (M2.2 roles pre-pass the gate)
		FinalizeState{Expected: true, Attempted: false, Committed: false},
	)
	if got != "failed" || reason != ExitFinalizeNeverCalled {
		t.Errorf("got (%s, %s); want (failed, %s)", got, reason, ExitFinalizeNeverCalled)
	}
}

// TestAdjudicateFinalizeNotExpectedPreservesM22 — for roles where
// FinalizeState.Expected=false (M1/M2.1 fake-agent path, M2.2 engineer
// on todo column), Adjudicate's M2.2 behaviour is preserved exactly.
func TestAdjudicateFinalizeNotExpectedPreservesM22(t *testing.T) {
	got, reason := Adjudicate(
		Result{ResultSeen: true, TerminalReason: "success", IsError: false, TotalCostUSD: "0.01"},
		WaitDetail{ExitCode: 0},
		true,
		FinalizeState{Expected: false},
	)
	if got != "succeeded" || reason != ExitCompleted {
		t.Errorf("got (%s, %s); want (succeeded, completed) — non-finalize role must behave identically to M2.2",
			got, reason)
	}
}
