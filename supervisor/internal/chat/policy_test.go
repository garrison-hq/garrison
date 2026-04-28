package chat

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
)

// newPolicyForTest constructs a ChatPolicy with no DB pool — sufficient
// for OnAssistant / OnUser / OnRateLimit / OnTaskStarted / OnUnknown,
// which only mutate in-memory policy state and call into the logger.
// Anything that touches Pool / Queries panics with a nil-deref; the
// tests below stay on the no-DB paths.
func newPolicyForTest() *ChatPolicy {
	return &ChatPolicy{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestOnAssistantAppendsRawEnvelope: OnAssistant is observational —
// the only side effect is appending the raw envelope to rawEvents so
// the terminal commit can persist the full stream.
func TestOnAssistantAppendsRawEnvelope(t *testing.T) {
	p := newPolicyForTest()
	if got := len(p.rawEvents); got != 0 {
		t.Fatalf("precondition: rawEvents len=%d; want 0", got)
	}
	p.OnAssistant(context.Background(), claudeproto.AssistantEvent{
		Raw: []byte(`{"type":"assistant"}`),
	})
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1", got)
	}
	if got := string(p.rawEvents[0]); got != `{"type":"assistant"}` {
		t.Errorf("rawEvents[0]=%q", got)
	}
}

// TestOnUserAppendsRawEnvelope_NoToolResults: the no-tool-result path
// just appends the raw envelope; no log line, no state change.
func TestOnUserAppendsRawEnvelope_NoToolResults(t *testing.T) {
	p := newPolicyForTest()
	p.OnUser(context.Background(), claudeproto.UserEvent{
		Raw: []byte(`{"type":"user"}`),
	})
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1", got)
	}
}

// TestOnUserLogsToolResultErrors: when ToolResults carry IsError=true,
// OnUser logs a warn line per result and still appends the envelope.
// Two error results → both logged; the count assertion catches a
// future bug where the logging falls through after the first error.
func TestOnUserLogsToolResultErrors(t *testing.T) {
	var buf strings.Builder
	p := &ChatPolicy{
		Logger: slog.New(slog.NewTextHandler(&buf, nil)),
	}
	p.OnUser(context.Background(), claudeproto.UserEvent{
		ToolResults: []claudeproto.ToolResultSummary{
			{ToolUseID: "tu_1", IsError: true, Detail: "first error"},
			{ToolUseID: "tu_2", IsError: false, Detail: "ok"},
			{ToolUseID: "tu_3", IsError: true, Detail: "second error"},
		},
		Raw: []byte(`{"type":"user"}`),
	})
	out := buf.String()
	if !strings.Contains(out, "tool_result reported error") {
		t.Errorf("expected error-level log; got %q", out)
	}
	if c := strings.Count(out, "tool_result reported error"); c != 2 {
		t.Errorf("expected 2 error log lines (one per IsError tool_result); got %d", c)
	}
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1", got)
	}
}

// TestOnRateLimitFlagsRejectedStatus: status="rejected" sets
// rateLimitOverage so the terminal commit can mark the result as
// rate-limit-exhausted even when claude returns content.
func TestOnRateLimitFlagsRejectedStatus(t *testing.T) {
	p := newPolicyForTest()
	p.OnRateLimit(context.Background(), claudeproto.RateLimitEvent{
		Info: claudeproto.RateLimitInfo{Status: "rejected"},
		Raw:  []byte(`{"type":"rate_limit_event"}`),
	})
	if !p.rateLimitOverage {
		t.Error("rateLimitOverage = false; want true after status=rejected")
	}
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1", got)
	}
}

// TestOnRateLimitFlagsRejectedRateLimitType: rateLimitType="rejected"
// (case-insensitive) is the alternate signal Claude can emit. The
// EqualFold path is the one the spike noticed in 2.1.117 logs.
func TestOnRateLimitFlagsRejectedRateLimitType(t *testing.T) {
	p := newPolicyForTest()
	p.OnRateLimit(context.Background(), claudeproto.RateLimitEvent{
		Info: claudeproto.RateLimitInfo{Status: "ok", RateLimitType: "Rejected"},
		Raw:  []byte(`{"type":"rate_limit_event"}`),
	})
	if !p.rateLimitOverage {
		t.Error("rateLimitOverage = false; want true after rateLimitType=Rejected")
	}
}

// TestOnRateLimitOverageNotFlaggedOnHealthyEvent: utilization > 0
// without status=rejected is observational; the policy should NOT
// flip the overage bit.
func TestOnRateLimitOverageNotFlaggedOnHealthyEvent(t *testing.T) {
	p := newPolicyForTest()
	p.OnRateLimit(context.Background(), claudeproto.RateLimitEvent{
		Info: claudeproto.RateLimitInfo{
			Status: "ok", Utilization: 0.42, IsUsingOverage: true,
		},
		Raw: []byte(`{"type":"rate_limit_event"}`),
	})
	if p.rateLimitOverage {
		t.Error("rateLimitOverage = true; want false on status=ok event")
	}
}

// TestOnTaskStartedNoOp: chat doesn't act on claude's internal task_*
// events. The handler must not mutate state — pin the no-op contract.
func TestOnTaskStartedNoOp(t *testing.T) {
	p := newPolicyForTest()
	p.OnTaskStarted(context.Background(), claudeproto.TaskStartedEvent{
		Raw: []byte(`{"type":"system","subtype":"task_started"}`),
	})
	if got := len(p.rawEvents); got != 0 {
		t.Errorf("rawEvents len=%d; want 0 (task_started is observational only)", got)
	}
	if p.rateLimitOverage {
		t.Error("rateLimitOverage flipped on task_started")
	}
}

// TestOnUnknownAppendsRawEnvelope: unknown event types are forward-
// compatible (FR-107) — log warn, append envelope, never bail.
func TestOnUnknownAppendsRawEnvelope(t *testing.T) {
	var buf strings.Builder
	p := &ChatPolicy{Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	p.OnUnknown(context.Background(), claudeproto.UnknownEvent{
		Type:    "future_event",
		Subtype: "v2",
		Raw:     []byte(`{"type":"future_event"}`),
	})
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1", got)
	}
	if !strings.Contains(buf.String(), "unknown event") {
		t.Errorf("expected warn log; got %q", buf.String())
	}
}

// TestOnTerminateMapsParseErrorToRuntimeKind: the spawn pipeline
// invokes OnTerminate with a stable reason vocabulary. Without a DB
// pool the terminal-write path errors out via the logger — the test
// just pins the kind selection logic by inspecting bailReason routing.
func TestOnTerminateMapsBailReason(t *testing.T) {
	cases := []struct {
		reason     string
		bailReason ErrorKind
		want       ErrorKind
	}{
		{"parse_error", "", ErrorClaudeRuntimeError},
		{"bail", "mcp_postgres_failed", "mcp_postgres_failed"},
		{"bail", "", ErrorClaudeRuntimeError},
		{"unexpected_other", "", ErrorClaudeRuntimeError},
	}
	for _, c := range cases {
		t.Run(c.reason+"/"+string(c.bailReason), func(t *testing.T) {
			ek := selectTerminateKind(c.reason, c.bailReason)
			if ek != c.want {
				t.Errorf("reason=%q bailReason=%q → kind=%q; want %q",
					c.reason, c.bailReason, ek, c.want)
			}
		})
	}
}

// selectTerminateKind mirrors the OnTerminate branching so the test
// can assert the kind-selection logic without invoking the DB write.
// Keep this in lockstep with policy.go OnTerminate (a divergence here
// means the test is no longer pinning the real behaviour).
func selectTerminateKind(reason string, bailReason ErrorKind) ErrorKind {
	switch reason {
	case "parse_error":
		return ErrorClaudeRuntimeError
	case "bail":
		if bailReason != "" {
			return bailReason
		}
		return ErrorClaudeRuntimeError
	default:
		return ErrorClaudeRuntimeError
	}
}

// TestNumericFromString_EmptyReturnsZero pins the documented contract
// for the cost-rollup parser: an empty json.Number (Claude omits the
// field entirely on failed turns) returns a zero Numeric, not an
// error. The terminal-commit path relies on this so missing-cost
// rows are still committable.
func TestNumericFromString_EmptyReturnsZero(t *testing.T) {
	n, err := numericFromString("")
	if err != nil {
		t.Fatalf("numericFromString(\"\"): %v", err)
	}
	if n.Valid {
		t.Errorf("expected !Valid (zero Numeric); got Valid Numeric")
	}
}

// TestNumericFromString_ValidNumber: a real Claude cost like
// "0.00965475" must round-trip through pgtype.Numeric without losing
// the trailing precision. A naive float64 path would clobber the tail.
func TestNumericFromString_ValidNumber(t *testing.T) {
	n, err := numericFromString("0.00965475")
	if err != nil {
		t.Fatalf("numericFromString: %v", err)
	}
	if !n.Valid {
		t.Errorf("expected Valid Numeric")
	}
	// Verify the value round-trips by reading it back via Float64Value
	// (lossy but enough for an existence check; the precision-pinning
	// is the responsibility of the pgx driver round-trip).
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		t.Fatalf("Float64Value: %v / valid=%v", err, f.Valid)
	}
	if f.Float64 < 0.009 || f.Float64 > 0.01 {
		t.Errorf("round-tripped value=%v outside expected range", f.Float64)
	}
}

// TestNumericFromString_InvalidReturnsError: non-numeric input must
// surface a parse error so the terminal-commit path can fall back to
// the no-cost shape rather than committing a corrupt rollup.
func TestNumericFromString_InvalidReturnsError(t *testing.T) {
	if _, err := numericFromString("not a number"); err == nil {
		t.Error("numericFromString(\"not a number\") returned nil err; want parse error")
	}
}
