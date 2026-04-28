package spawn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/jackc/pgx/v5/pgtype"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runWithFinalize is a test helper that constructs a FinalizePolicy
// (the renamed FinalizePolicy) and calls Run with it. Pre-M5.1 tests
// passed (instanceID, ticketID, finalize, onBail) directly to Run; the
// Policy refactor moved that state onto the policy struct, so tests
// either go through this helper or construct the policy explicitly.
// The instanceID/ticketID arguments are intentionally zero-value here
// because pipeline_test.go's fixtures don't use them.
func runWithFinalize(stream io.Reader, onBail func(string), finalize FinalizeDeps) (Result, error) {
	policy := NewFinalizePolicy(discardLogger(), pgtype.UUID{}, pgtype.UUID{}, &Result{}, finalize, onBail)
	return Run(context.Background(), stream, policy, discardLogger())
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

// TestAdjudicateBudgetExceededTakesPrecedenceOverIsError — M2.2.2
// FR-306 / SC-304. A Result with ResultSeen=true, IsError=true, and a
// budget-shaped TerminalReason classifies as budget_exceeded (the
// cost root cause) rather than claude_error (the symptom). Pre-M2.2.2
// the same input returned claude_error, hiding cost spikes behind a
// generic error bucket — M2.2.1 live-run append documented the bug.
func TestAdjudicateBudgetExceededTakesPrecedenceOverIsError(t *testing.T) {
	r := Result{
		ResultSeen:     true,
		IsError:        true, // would have returned claude_error pre-M2.2.2
		TerminalReason: "error_max_budget_usd",
		TotalCostUSD:   "0.26",
	}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true, FinalizeState{})
	if status != "failed" || reason != ExitBudgetExceeded {
		t.Errorf("Adjudicate(IsError + budget) = (%q, %q); want (failed, budget_exceeded)",
			status, reason)
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

	r, err := runWithFinalize(bytes.NewBufferString(stream), nil, FinalizeDeps{})
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
	r, err := runWithFinalize(bytes.NewBufferString(stream),
		func(reason string) { bailReason = reason }, FinalizeDeps{})
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
	r, err := runWithFinalize(bytes.NewBufferString(stream),
		func(reason string) { bailReason = reason }, FinalizeDeps{})
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
	r, err := runWithFinalize(bytes.NewBufferString(stream), nil, FinalizeDeps{})
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
	r, err := runWithFinalize(bytes.NewBufferString(stream), nil, FinalizeDeps{})
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
	_, err := runWithFinalize(reader, nil, FinalizeDeps{})
	if err == nil {
		t.Fatal("Run: want error from reader, got nil")
	}
}

func TestPipelineRunRequiresLogger(t *testing.T) {
	policy := NewFinalizePolicy(discardLogger(), pgtype.UUID{}, pgtype.UUID{}, &Result{}, FinalizeDeps{}, nil)
	_, err := Run(context.Background(), bytes.NewBufferString(""), policy, nil)
	if err == nil {
		t.Error("Run(nil logger): want error, got nil")
	}
}

func TestPipelineRunRequiresPolicy(t *testing.T) {
	_, err := Run(context.Background(), bytes.NewBufferString(""), nil, discardLogger())
	if err == nil {
		t.Error("Run(nil policy): want error, got nil")
	}
}

// TestPolicy_FinalizeUnchanged exercises the canonical happy-path stream
// (init → assistant → user → rate_limit → result) against the post-
// refactor Run signature using NewFinalizePolicy. Every observable
// Result field must match what the pre-M5.1 Run signature would have
// produced — the Policy refactor is "just packaging," not a behaviour
// change. If this test starts failing, the refactor introduced a
// regression in the M2.2.1 finalize path.
func TestPolicy_FinalizeUnchanged(t *testing.T) {
	stream := strings.Join([]string{
		fixtureInit,
		fixtureAssistant,
		fixtureUser,
		fixtureRateLimit,
		fixtureResult,
	}, "\n") + "\n"

	r, err := runWithFinalize(bytes.NewBufferString(stream), nil, FinalizeDeps{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.ResultSeen || !r.AssistantSeen || r.MCPBailed || r.ParseError {
		t.Errorf("flags off: %+v", r)
	}
	if r.TerminalReason != "success" || r.TotalCostUSD != "0.003" || r.SessionID != "sess-1" {
		t.Errorf("fields off: %+v", r)
	}
}

// TestPolicy_RunCallsOnTerminateOnParseError verifies the new
// OnTerminate hook fires with reason="parse_error" when the scanner
// hits a malformed NDJSON line (and that FinalizePolicy translates
// that into the legacy onBail closure call with ExitParseError).
func TestPolicy_RunCallsOnTerminateOnParseError(t *testing.T) {
	stream := fixtureInit + "\n" + fixtureMalformed + "\n"
	var bailReason string
	_, err := runWithFinalize(
		bytes.NewBufferString(stream),
		func(reason string) { bailReason = reason },
		FinalizeDeps{},
	)
	if err == nil {
		t.Fatal("Run: want parse error, got nil")
	}
	if bailReason != ExitParseError {
		t.Errorf("OnTerminate→bailFn reason = %q; want %q", bailReason, ExitParseError)
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

	policy := NewFinalizePolicy(logger, pgtype.UUID{}, pgtype.UUID{}, &Result{}, FinalizeDeps{}, nil)
	_, err := Run(context.Background(), strings.NewReader(stream), policy, logger)
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

// -------- M2.2.1 finalize observer ---------------------------------------

// finalizeRouter builds a FinalizePolicy wired with a fresh FinalizeState,
// a buffer-backed logger, optional commit/bail hooks.
func finalizeRouter(onCommit func(json.RawMessage) error, onBail func(string)) (*FinalizePolicy, *FinalizeState, *bytes.Buffer) {
	state := &FinalizeState{Expected: true}
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := &FinalizePolicy{
		logger: logger,
		result: &Result{},
		finalize: &finalizeHook{
			state:    state,
			onCommit: onCommit,
			onBail:   onBail,
		},
	}
	return r, state, logBuf
}

func assistantWithFinalize(toolUseID string, input []byte) claudeproto.AssistantEvent {
	return claudeproto.AssistantEvent{
		ContentBlockCount: 1,
		ContentTypes:      []string{"tool_use"},
		ToolUses: []claudeproto.ToolUseBlock{
			{Name: "finalize_ticket", ToolUseID: toolUseID, InputRaw: input},
		},
	}
}

func userWithFinalizeResult(toolUseID string, ok bool, errorType string) claudeproto.UserEvent {
	body := `{"ok":` + boolToJSON(ok) + `,"attempt":1`
	if !ok {
		body += `,"error_type":"` + errorType + `","field":"kg_triples","message":"test"`
	}
	body += "}"
	return claudeproto.UserEvent{
		ToolResults: []claudeproto.ToolResultSummary{
			{ToolUseID: toolUseID, IsError: false, Detail: body},
		},
	}
}

func boolToJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestFinalizeAttemptCounterIncrementsOnEachToolUse — three consecutive
// failed tool_use/tool_result pairs drive the counter 1, 2, 3.
func TestFinalizeAttemptCounterIncrementsOnEachToolUse(t *testing.T) {
	r, state, _ := finalizeRouter(nil, func(string) {})
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		toolID := "tu-" + string(rune('0'+i))
		r.OnAssistant(ctx, assistantWithFinalize(toolID, []byte(`{"bad":"payload"}`)))
		r.OnUser(ctx, userWithFinalizeResult(toolID, false, "schema"))
		if r.finalize.attempts != i {
			t.Errorf("after call %d: attempts=%d; want %d", i, r.finalize.attempts, i)
		}
	}
	if !state.CapExhausted {
		t.Error("CapExhausted=false after 3 failed attempts; want true")
	}
}

// TestFinalizeAttemptCapTriggersSIGTERM — 3rd failed tool_result fires
// onBail with exit_reason=finalize_invalid.
func TestFinalizeAttemptCapTriggersSIGTERM(t *testing.T) {
	var bailReason string
	r, _, _ := finalizeRouter(nil, func(reason string) { bailReason = reason })
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		toolID := "tu-" + string(rune('0'+i))
		r.OnAssistant(ctx, assistantWithFinalize(toolID, []byte(`{"bad":"payload"}`)))
		r.OnUser(ctx, userWithFinalizeResult(toolID, false, "schema"))
	}
	if bailReason != ExitFinalizeInvalid {
		t.Errorf("bailReason=%q; want %q", bailReason, ExitFinalizeInvalid)
	}
}

// TestFinalizeAttemptCounterIgnoresPostCommitCalls — once Committed=true,
// subsequent finalize tool_use events do not increment the counter.
func TestFinalizeAttemptCounterIgnoresPostCommitCalls(t *testing.T) {
	var commitCalls int
	r, state, _ := finalizeRouter(func(json.RawMessage) error { commitCalls++; return nil }, func(string) {})
	ctx := context.Background()
	r.OnAssistant(ctx, assistantWithFinalize("tu-1", []byte(`{"ok":"payload"}`)))
	r.OnUser(ctx, userWithFinalizeResult("tu-1", true, ""))
	if !state.Committed {
		t.Fatal("Committed=false after first ok; want true")
	}
	attemptsAfterFirst := r.finalize.attempts
	r.OnAssistant(ctx, assistantWithFinalize("tu-2", []byte(`{"second":"call"}`)))
	r.OnUser(ctx, userWithFinalizeResult("tu-2", false, "schema"))
	if r.finalize.attempts != attemptsAfterFirst {
		t.Errorf("post-commit attempt incremented counter: before=%d after=%d",
			attemptsAfterFirst, r.finalize.attempts)
	}
	if commitCalls != 1 {
		t.Errorf("onCommit invoked %d times; want 1", commitCalls)
	}
}

// TestFinalizeObserverLogsEveryToolUse — FR-276: each finalize tool_use
// produces exactly one info-level "finalize tool_use" log entry, and
// the paired tool_result produces a "finalize tool_result" entry
// carrying ok / error_type / field.
func TestFinalizeObserverLogsEveryToolUse(t *testing.T) {
	r, _, logBuf := finalizeRouter(nil, func(string) {})
	ctx := context.Background()
	r.OnAssistant(ctx, assistantWithFinalize("tu-log-1", []byte(`{"x":1}`)))
	r.OnUser(ctx, userWithFinalizeResult("tu-log-1", false, "schema"))

	logs := logBuf.String()
	if !strings.Contains(logs, `"msg":"finalize tool_use"`) {
		t.Errorf("missing finalize tool_use log line:\n%s", logs)
	}
	if !strings.Contains(logs, `"msg":"finalize tool_result"`) {
		t.Errorf("missing finalize tool_result log line:\n%s", logs)
	}
	if !strings.Contains(logs, `"tool_use_id":"tu-log-1"`) {
		t.Errorf("log lines missing tool_use_id:\n%s", logs)
	}
	if !strings.Contains(logs, `"ok":false`) {
		t.Errorf("log line missing ok field:\n%s", logs)
	}
	if !strings.Contains(logs, `"error_type":"schema"`) {
		t.Errorf("log line missing error_type field:\n%s", logs)
	}
}
