package chat

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/jackc/pgx/v5/pgtype"
)

// newCeilingTestPolicy returns a ChatPolicy suitable for ceiling-track
// tests. Pool is nil; the policy short-circuits emit calls so the test
// exercises only the count + bailReason logic.
func newCeilingTestPolicy(t *testing.T, ceiling int) *ChatPolicy {
	t.Helper()
	return &ChatPolicy{
		Logger:          slog.New(slog.NewTextHandler(nullWriter{}, nil)),
		SessionID:       pgtype.UUID{Valid: true, Bytes: [16]byte{1}},
		MessageID:       pgtype.UUID{Valid: true, Bytes: [16]byte{2}},
		ToolCallCeiling: ceiling,
	}
}

// nullWriter discards log output to keep test stdout clean.
type nullWriter struct{}

func (nullWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeToolUseRaw composes a synthetic content_block_start raw event
// shape mirroring claudeproto's wire format. The policy detects
// tool_use via a substring match on `"content_block":{"type":"tool_use"`,
// which assumes the real claude wire format puts "type" first inside
// content_block. Build the JSON string verbatim to match.
func fakeToolUseRaw(toolUseID, toolName string) []byte {
	return []byte(`{"event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"` +
		toolUseID + `","name":"` + toolName + `","input":{}}}}`)
}

// fakeToolUseRaw is also unmarshaled by extractToolUseFromContentBlockStart,
// which uses encoding/json (key-order independent). Both consumers
// stay aligned because the substring-detection requires "type":"tool_use"
// at the start of content_block; real claude wire output meets that
// invariant.
var _ = json.Marshal // keep encoding/json import even if no other tests need it

// TestExtractToolUseFromContentBlockStart pins the parser used by the
// policy to surface tool_use_id + tool_name + args from claude's wire
// shape. Runs without a DB.
func TestExtractToolUseFromContentBlockStart(t *testing.T) {
	raw := fakeToolUseRaw("tu_123", "garrison-mutate.create_ticket")
	id, name, args := extractToolUseFromContentBlockStart(raw)
	if id != "tu_123" {
		t.Errorf("toolUseID = %q; want tu_123", id)
	}
	if name != "garrison-mutate.create_ticket" {
		t.Errorf("toolName = %q", name)
	}
	if string(args) == "" {
		t.Error("args empty; want JSON")
	}
}

// TestExtractToolUseRejectsNonToolUseBlock asserts the parser returns
// empty for content blocks that aren't tool_use (e.g., text blocks).
func TestExtractToolUseRejectsNonToolUseBlock(t *testing.T) {
	raw := []byte(`{"event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`)
	id, _, _ := extractToolUseFromContentBlockStart(raw)
	if id != "" {
		t.Errorf("text block returned toolUseID = %q; want empty", id)
	}
}

// TestToolCallCounterIncrementsOnToolUse drives the policy's
// OnStreamEvent with a synthetic content_block_start for tool_use and
// asserts the toolCallCount increments. Pool stays nil; emit calls
// short-circuit cleanly.
func TestToolCallCounterIncrementsOnToolUse(t *testing.T) {
	p := newCeilingTestPolicy(t, 50)
	for i := 0; i < 5; i++ {
		raw := fakeToolUseRaw("tu_x", "postgres.query")
		p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       raw,
		})
	}
	if p.toolCallCount != 5 {
		t.Errorf("toolCallCount = %d; want 5", p.toolCallCount)
	}
	if p.ceilingFired {
		t.Error("ceilingFired = true; want false (5 < 50)")
	}
	if p.bailReason != "" {
		t.Errorf("bailReason = %q; want empty", p.bailReason)
	}
}

// TestToolCallCeilingFiresWhenExceeded drives the policy past the
// ceiling and asserts ceilingFired flips, bailReason is set to
// tool_call_ceiling_reached, and ceilingFired stays sticky (does
// not re-emit on subsequent calls).
func TestToolCallCeilingFiresWhenExceeded(t *testing.T) {
	p := newCeilingTestPolicy(t, 3)
	for i := 0; i < 5; i++ {
		raw := fakeToolUseRaw("tu_y", "postgres.query")
		p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       raw,
		})
	}
	if p.toolCallCount != 5 {
		t.Errorf("toolCallCount = %d; want 5", p.toolCallCount)
	}
	if !p.ceilingFired {
		t.Error("ceilingFired = false; want true (5 > 3)")
	}
	if p.bailReason != ChatErrorToolCallCeilingReached {
		t.Errorf("bailReason = %q; want %q", p.bailReason, ChatErrorToolCallCeilingReached)
	}
}

// TestNonToolUseEventDoesNotIncrementCounter pins the counter rule:
// only content_block_start events with tool_use type increment the
// counter. Text blocks etc. don't count.
func TestNonToolUseEventDoesNotIncrementCounter(t *testing.T) {
	p := newCeilingTestPolicy(t, 50)
	textRaw := []byte(`{"event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`)
	for i := 0; i < 100; i++ {
		p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       textRaw,
		})
	}
	if p.toolCallCount != 0 {
		t.Errorf("toolCallCount = %d after 100 text blocks; want 0", p.toolCallCount)
	}
}

// TestNewChatPolicyDefaultsCeilingTo50 covers the default-value path —
// when Deps.ToolCallCeiling is 0/unset, NewChatPolicy applies the M5.3
// default of 50 (FR-530 / chat-threat-model.md Rule 4).
func TestNewChatPolicyDefaultsCeilingTo50(t *testing.T) {
	deps := Deps{Logger: slog.New(slog.NewTextHandler(nullWriter{}, nil))}
	p := NewChatPolicy(deps, pgtype.UUID{Valid: true}, pgtype.UUID{Valid: true})
	if p.ToolCallCeiling != 50 {
		t.Errorf("default ceiling = %d; want 50", p.ToolCallCeiling)
	}
}

// TestNewChatPolicyHonorsExplicitCeiling pins the override path.
func TestNewChatPolicyHonorsExplicitCeiling(t *testing.T) {
	deps := Deps{
		Logger:          slog.New(slog.NewTextHandler(nullWriter{}, nil)),
		ToolCallCeiling: 7,
	}
	p := NewChatPolicy(deps, pgtype.UUID{Valid: true}, pgtype.UUID{Valid: true})
	if p.ToolCallCeiling != 7 {
		t.Errorf("explicit ceiling = %d; want 7", p.ToolCallCeiling)
	}
}

// TestErrorKindAddedForCeilingReached pins that the chat error_kind
// vocabulary has the M5.3 ceiling value defined and is the same string
// as the audit table CHECK accepts.
func TestErrorKindAddedForCeilingReached(t *testing.T) {
	if ErrorToolCallCeilingReached != "tool_call_ceiling_reached" {
		t.Errorf("ErrorToolCallCeilingReached = %q; want tool_call_ceiling_reached", ErrorToolCallCeilingReached)
	}
	if !strings.EqualFold(ChatErrorToolCallCeilingReached, ErrorToolCallCeilingReached) {
		t.Errorf("alias drift: ChatErrorToolCallCeilingReached=%q vs ErrorToolCallCeilingReached=%q",
			ChatErrorToolCallCeilingReached, ErrorToolCallCeilingReached)
	}
}
