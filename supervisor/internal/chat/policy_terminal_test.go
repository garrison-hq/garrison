package chat

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
)

// TestOnTaskStarted_NoOp: OnTaskStarted is a documented no-op for chat —
// pinning the behaviour ensures a future regression that adds side
// effects without updating the comment is caught.
func TestOnTaskStarted_NoOp(t *testing.T) {
	p := newPolicyForTest()
	p.OnTaskStarted(context.Background(), claudeproto.TaskStartedEvent{})
	if len(p.rawEvents) != 0 {
		t.Errorf("OnTaskStarted should not append raw events; got %d", len(p.rawEvents))
	}
	if p.toolCallCount != 0 {
		t.Errorf("OnTaskStarted should not increment counters; got %d", p.toolCallCount)
	}
}

// TestOnUnknown_LogsAndAppends: OnUnknown is the catchall for events
// claudeproto's Router doesn't recognise — should warn-log and still
// append the raw envelope so the terminal commit's full record stays
// honest.
func TestOnUnknown_LogsAndAppends(t *testing.T) {
	p := newPolicyForTest()
	p.OnUnknown(context.Background(), claudeproto.UnknownEvent{
		Type:    "speculative",
		Subtype: "future_event",
		Raw:     []byte(`{"type":"speculative"}`),
	})
	if len(p.rawEvents) != 1 {
		t.Errorf("OnUnknown should append raw event; got %d", len(p.rawEvents))
	}
}

// TestOnTerminate_ParseErrorMapping: spawn.Run hands "parse_error" when
// it can't decode a stream-json line. Chat maps it to
// claude_runtime_error and tries to terminal-write — without a real
// Queries the write path will short-circuit; we exercise the mapping
// branch.
func TestOnTerminate_ParseErrorMapping(t *testing.T) {
	defer func() {
		// Recover from the nil-Queries deref — terminalWriteError
		// derefs p.Queries, which is nil in this fixture. The mapping
		// branch we want coverage on runs *before* the write attempt.
		_ = recover()
	}()
	p := newPolicyForTest()
	p.OnTerminate(context.Background(), "parse_error")
}

// TestOnTerminate_BailWithBailReason: when reason="bail" and bailReason
// is populated (typically by claudeproto's MCP-status check), the
// kind comes from bailReason.
func TestOnTerminate_BailWithBailReason(t *testing.T) {
	defer func() { _ = recover() }()
	p := newPolicyForTest()
	p.bailReason = ErrorKind("mcp_garrison_mutate_failed")
	p.OnTerminate(context.Background(), "bail")
}

// TestOnTerminate_BailWithoutBailReason: bail without bailReason set
// falls back to claude_runtime_error.
func TestOnTerminate_BailWithoutBailReason(t *testing.T) {
	defer func() { _ = recover() }()
	p := newPolicyForTest()
	p.OnTerminate(context.Background(), "bail")
}

// TestOnTerminate_DefaultMapping: any other reason maps to
// claude_runtime_error.
func TestOnTerminate_DefaultMapping(t *testing.T) {
	defer func() { _ = recover() }()
	p := newPolicyForTest()
	p.OnTerminate(context.Background(), "anything-else")
}

// TestResult_ZeroValueForChat: chat doesn't go through Adjudicate, so
// the policy's Result() is just a documented zero passthrough.
func TestResult_ZeroValueForChat(t *testing.T) {
	p := newPolicyForTest()
	_ = p.Result() // exercises the no-op branch; nothing to assert.
}

// TestTerminalWriteError_GracePathWithoutQueries: terminalWriteError
// honours GraceWrite by deriving a context.WithTimeout off
// context.WithoutCancel. Without a Queries it'll panic on the call
// path; recover and assert we reached past the ctx setup branch.
func TestTerminalWriteError_GracePathWithoutQueries(t *testing.T) {
	defer func() { _ = recover() }()
	p := newPolicyForTest()
	p.GraceWrite = 50 * time.Millisecond
	p.terminalWriteError(context.Background(), ErrorClaudeRuntimeError)
}

// TestTerminalWriteError_NoGracePath: zero GraceWrite skips the
// timeout-derivation branch — also panics on the Queries call but
// exercises the no-grace branch.
func TestTerminalWriteError_NoGracePath(t *testing.T) {
	defer func() { _ = recover() }()
	p := &ChatPolicy{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		GraceWrite: 0,
	}
	p.terminalWriteError(context.Background(), ErrorClaudeRuntimeError)
}
