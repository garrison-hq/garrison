package chat

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/jackc/pgx/v5/pgtype"
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

// TestOnInitBailsOnFailedMCPServer pins the FR-108 fail-closed check:
// a single mcp_server with status="failed" causes OnInit to return
// RouterActionBail and stash a structured bailReason that
// commitAssistantTerminal / OnTerminate can surface to the dashboard.
// This is the no-DB path — the bail returns before
// TransitionMessageToStreaming runs.
func TestOnInitBailsOnFailedMCPServer(t *testing.T) {
	p := newPolicyForTest()
	action := p.OnInit(context.Background(), claudeproto.InitEvent{
		MCPServers: []claudeproto.MCPServer{
			{Name: "postgres", Status: "connected"},
			{Name: "mempalace", Status: "failed"},
		},
		Raw: []byte(`{"type":"system","subtype":"init"}`),
	})
	if action != claudeproto.RouterActionBail {
		t.Fatalf("OnInit action=%v; want RouterActionBail", action)
	}
	if p.bailReason == "" {
		t.Errorf("bailReason is empty; want a structured mcp_*_failed kind")
	}
	if !strings.Contains(p.bailReason, "mempalace") {
		t.Errorf("bailReason=%q; want mention of the failed mempalace server", p.bailReason)
	}
}

// TestOnInitBailsOnNeedsAuthMCPServer covers the second non-connected
// status the spike's enum lists. A "needs-auth" server is just as
// fatal as "failed" — the chat container can't start the MCP and
// shouldn't proceed.
func TestOnInitBailsOnNeedsAuthMCPServer(t *testing.T) {
	p := newPolicyForTest()
	action := p.OnInit(context.Background(), claudeproto.InitEvent{
		MCPServers: []claudeproto.MCPServer{
			{Name: "postgres", Status: "needs-auth"},
		},
	})
	if action != claudeproto.RouterActionBail {
		t.Fatalf("OnInit action=%v; want RouterActionBail on needs-auth", action)
	}
	if !strings.Contains(p.bailReason, "postgres") {
		t.Errorf("bailReason=%q; want mention of the needs-auth postgres server", p.bailReason)
	}
}

// TestOnStreamEvent_NonTextDeltaIsObservationalOnly: content_block_delta
// events that are NOT text_delta (e.g. thinking deltas, tool_use input
// deltas) must not accumulate into contentBuf or fire EmitDelta —
// otherwise the persisted assistant content would include thinking
// blocks the operator never saw.
func TestOnStreamEvent_NonTextDeltaIsObservationalOnly(t *testing.T) {
	p := newPolicyForTest()
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "content_block_delta",
		Inner:     claudeproto.StreamInner{DeltaType: "thinking_delta", DeltaText: "secret thinking"},
		Raw:       []byte(`{"type":"stream_event"}`),
	})
	if p.contentBuf.Len() != 0 {
		t.Errorf("contentBuf accumulated thinking-delta text; got %q", p.contentBuf.String())
	}
	if p.deltaSeq != 0 {
		t.Errorf("deltaSeq advanced on non-text-delta; got %d", p.deltaSeq)
	}
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1 (envelope still preserved)", got)
	}
}

// TestOnStreamEvent_MessageStartCapturesUsage: message_start events
// carry the input-token + cache breakdown that the terminal commit
// rolls into chat_messages. Pin every field so a wire-format change
// at the StreamInner shape is caught here.
func TestOnStreamEvent_MessageStartCapturesUsage(t *testing.T) {
	p := newPolicyForTest()
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "message_start",
		Inner: claudeproto.StreamInner{
			InputTokens:        1234,
			CacheReadInput:     5678,
			CacheCreationInput: 91,
		},
		Raw: []byte(`{"type":"stream_event"}`),
	})
	if p.tokensInput != 1234 {
		t.Errorf("tokensInput=%d; want 1234", p.tokensInput)
	}
	if p.cacheReadInput != 5678 {
		t.Errorf("cacheReadInput=%d; want 5678", p.cacheReadInput)
	}
	if p.cacheCreationInput != 91 {
		t.Errorf("cacheCreationInput=%d; want 91", p.cacheCreationInput)
	}
}

// TestOnStreamEvent_MessageStartResetsContentBuf: when a turn carries
// multiple message_start events (claude's response splits into text →
// tool_use → text round-trips), the committed content must be the
// LAST assistant message only — not the concatenation of every
// intermediate text block. Reproduces the M5.2 "pongYes, MemPalace
// is up" bug where two assistant messages got glued.
//
// EmitDelta requires a real Postgres pool to exercise the text_delta
// path; we simulate the post-first-message state by writing directly
// into contentBuf, then dispatch message_start and assert the buffer
// is empty. This isolates the reset behaviour without DB plumbing.
func TestOnStreamEvent_MessageStartResetsContentBuf(t *testing.T) {
	p := newPolicyForTest()
	// Simulate end-of-first-message state: contentBuf carries "pong"
	// (e.g. claude's first response message before a tool round-trip).
	p.contentBuf.WriteString("pong")
	if p.contentBuf.String() != "pong" {
		t.Fatalf("setup: contentBuf=%q; want %q", p.contentBuf.String(), "pong")
	}
	// Second message_start (after tool round-trip): contentBuf must
	// reset so the prior "pong" doesn't glue onto the next message.
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "message_start",
		Inner:     claudeproto.StreamInner{InputTokens: 200},
	})
	if p.contentBuf.Len() != 0 {
		t.Errorf("contentBuf not reset on message_start; got %q", p.contentBuf.String())
	}
}

// TestOnStreamEvent_MessageDeltaUpdatesOutputTokens: claude emits
// the running output-token count via message_delta.usage. The policy
// only updates when OutputTokens > 0 — guards against a zero-valued
// trailing event clobbering an earlier non-zero reading.
func TestOnStreamEvent_MessageDeltaUpdatesOutputTokens(t *testing.T) {
	p := newPolicyForTest()
	p.tokensOutput = 100 // simulate a prior message_delta
	// Zero OutputTokens must NOT overwrite.
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "message_delta",
		Inner:     claudeproto.StreamInner{OutputTokens: 0},
	})
	if p.tokensOutput != 100 {
		t.Errorf("tokensOutput got=%d; want 100 (zero must not clobber)", p.tokensOutput)
	}
	// Non-zero overwrites.
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "message_delta",
		Inner:     claudeproto.StreamInner{OutputTokens: 250},
	})
	if p.tokensOutput != 250 {
		t.Errorf("tokensOutput got=%d; want 250", p.tokensOutput)
	}
}

// TestOnStreamEvent_UnknownInnerTypeIsObservational: any inner type
// outside the known content_block_delta / message_start / message_delta
// triple is forward-compat — the policy still appends to rawEvents
// (so the terminal commit preserves the byte stream) but mutates no
// state.
func TestOnStreamEvent_UnknownInnerTypeIsObservational(t *testing.T) {
	p := newPolicyForTest()
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "future_inner_type",
		Raw:       []byte(`{"type":"stream_event"}`),
	})
	if p.contentBuf.Len() != 0 {
		t.Errorf("contentBuf mutated on unknown inner type")
	}
	if got := len(p.rawEvents); got != 1 {
		t.Errorf("rawEvents len=%d; want 1", got)
	}
}

// TestNewChatPolicyWiresDepsThrough: NewChatPolicy is a thin
// constructor — verify the SessionID / MessageID / GraceWrite
// passthrough so a regression at the constructor boundary doesn't
// silently zero out the message-correlation IDs (which the
// commit path keys writes on).
func TestNewChatPolicyWiresDepsThrough(t *testing.T) {
	deps := Deps{
		Logger: newPolicyForTest().Logger,
		// Pool / Queries left nil — constructor copies them through.
		TerminalWriteGrace: 7 * time.Second,
	}
	var sid, mid pgtype.UUID
	if err := sid.Scan("11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatalf("scan sid: %v", err)
	}
	if err := mid.Scan("22222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatalf("scan mid: %v", err)
	}
	p := NewChatPolicy(deps, sid, mid)
	if p.SessionID != sid {
		t.Errorf("SessionID not wired through")
	}
	if p.MessageID != mid {
		t.Errorf("MessageID not wired through")
	}
	if p.GraceWrite != 7*time.Second {
		t.Errorf("GraceWrite=%v; want 7s", p.GraceWrite)
	}
}
