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
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true)
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
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true)
	if status != "failed" || reason != ExitClaudeError {
		t.Errorf("Adjudicate(claude_error) = (%q, %q); want (failed, claude_error)", status, reason)
	}
}

func TestAdjudicateNoResult(t *testing.T) {
	r := Result{ResultSeen: false}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, true)
	if status != "failed" || reason != ExitNoResult {
		t.Errorf("Adjudicate(no_result) = (%q, %q); want (failed, no_result)", status, reason)
	}
}

func TestAdjudicateAcceptanceFailed(t *testing.T) {
	r := Result{ResultSeen: true, IsError: false, TotalCostUSD: "0.001"}
	status, reason := Adjudicate(r, WaitDetail{ExitCode: 0}, false)
	if status != "failed" || reason != ExitAcceptanceFailed {
		t.Errorf("Adjudicate(acceptance_failed) = (%q, %q); want (failed, acceptance_failed)", status, reason)
	}
}

func TestAdjudicateTimeout(t *testing.T) {
	status, reason := Adjudicate(Result{}, WaitDetail{ContextErr: context.DeadlineExceeded}, true)
	if status != "timeout" || reason != ExitTimeout {
		t.Errorf("Adjudicate(timeout) = (%q, %q); want (timeout, timeout)", status, reason)
	}
}

func TestAdjudicateShutdown(t *testing.T) {
	status, reason := Adjudicate(Result{}, WaitDetail{ShutdownInitiated: true, ContextErr: context.Canceled}, true)
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
	status, reason := Adjudicate(r, WaitDetail{}, true)
	if status != "failed" || reason != "mcp_postgres_failed" {
		t.Errorf("Adjudicate(mcp_bail) = (%q, %q); want (failed, mcp_postgres_failed)", status, reason)
	}
}

func TestAdjudicateParseError(t *testing.T) {
	r := Result{ParseError: true}
	status, reason := Adjudicate(r, WaitDetail{}, true)
	if status != "failed" || reason != ExitParseError {
		t.Errorf("Adjudicate(parse_error) = (%q, %q); want (failed, parse_error)", status, reason)
	}
}

func TestAdjudicateSignaled(t *testing.T) {
	r := Result{ResultSeen: false}
	status, reason := Adjudicate(r, WaitDetail{Signaled: true, Signal: syscall.SIGKILL, ExitCode: -1}, true)
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
	status, reason := Adjudicate(r, WaitDetail{ContextErr: context.DeadlineExceeded}, true)
	if reason != ExitTimeout {
		t.Errorf("timeout+no_result → %q; want timeout (timeout outranks no_result)", reason)
	}
	if status != "timeout" {
		t.Errorf("status = %q; want timeout", status)
	}
}

func TestAdjudicateShutdownOutranksClaudeError(t *testing.T) {
	r := Result{ResultSeen: true, IsError: true, TotalCostUSD: "0.001"}
	status, reason := Adjudicate(r, WaitDetail{ShutdownInitiated: true, ContextErr: context.Canceled}, true)
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
	_, reason := Adjudicate(r, WaitDetail{ShutdownInitiated: true, ContextErr: context.Canceled, Signaled: true, Signal: syscall.SIGKILL}, false)
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

	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, discardLogger(), nil)
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
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, discardLogger(),
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
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, discardLogger(),
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
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, discardLogger(), nil)
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
	r, err := Run(context.Background(), bytes.NewBufferString(stream), pgtype.UUID{}, discardLogger(), nil)
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
	_, err := Run(context.Background(), reader, pgtype.UUID{}, discardLogger(), nil)
	if err == nil {
		t.Fatal("Run: want error from reader, got nil")
	}
}

func TestPipelineRunRequiresLogger(t *testing.T) {
	_, err := Run(context.Background(), bytes.NewBufferString(""), pgtype.UUID{}, nil, nil)
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
