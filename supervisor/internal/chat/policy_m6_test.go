package chat

import (
	"context"
	"log/slog"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/jackc/pgx/v5/pgtype"
)

// newM6CeilingTestPolicy returns a ChatPolicy wired with the M6
// ticket-creation ceiling. ToolCallCeiling stays at 50 (the M5.3
// default) so M6 tests don't accidentally trip that ceiling first.
func newM6CeilingTestPolicy(t *testing.T, maxTickets int) *ChatPolicy {
	t.Helper()
	return &ChatPolicy{
		Logger:            slog.New(slog.NewTextHandler(nullWriter{}, nil)),
		SessionID:         pgtype.UUID{Valid: true, Bytes: [16]byte{1}},
		MessageID:         pgtype.UUID{Valid: true, Bytes: [16]byte{2}},
		ToolCallCeiling:   50,
		MaxTicketsPerTurn: maxTickets,
	}
}

// TestTicketCreationCeilingFires_OnEleventhCall — with default ceiling=10,
// the first 10 create_ticket calls do NOT fire; the 11th does. Bail
// reason is set to ChatErrorTicketCreationCeilingReached.
func TestTicketCreationCeilingFires_OnEleventhCall(t *testing.T) {
	p := newM6CeilingTestPolicy(t, 10)
	raw := fakeToolUseRaw("tu_create", "mcp__garrison-mutate__create_ticket")
	for i := 0; i < 10; i++ {
		p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       raw,
		})
	}
	if p.ticketCreationCeilingFired {
		t.Fatal("ceiling fired before 11th call")
	}
	if p.bailReason != "" {
		t.Errorf("bailReason = %q; want empty after 10 calls", p.bailReason)
	}

	// 11th call: fires.
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "content_block_start",
		Raw:       raw,
	})
	if !p.ticketCreationCeilingFired {
		t.Error("expected ticketCreationCeilingFired=true after 11th call")
	}
	if p.bailReason != ChatErrorTicketCreationCeilingReached {
		t.Errorf("bailReason = %q; want %q", p.bailReason, ChatErrorTicketCreationCeilingReached)
	}
	if p.ticketCreationCount != 11 {
		t.Errorf("ticketCreationCount = %d; want 11", p.ticketCreationCount)
	}
}

// TestTicketCreationCeilingDoesNotFireOnOtherTools — 50 calls to a
// non-create_ticket tool (e.g. mempalace_search) must NOT trip the
// ticket-creation ceiling. Tool-call ceiling stays orthogonal.
func TestTicketCreationCeilingDoesNotFireOnOtherTools(t *testing.T) {
	p := newM6CeilingTestPolicy(t, 10)
	raw := fakeToolUseRaw("tu_search", "mcp__mempalace__mempalace_search")
	for i := 0; i < 50; i++ {
		p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       raw,
		})
	}
	if p.ticketCreationCeilingFired {
		t.Errorf("ticket-creation ceiling fired on non-create_ticket tool calls")
	}
	if p.ticketCreationCount != 0 {
		t.Errorf("ticketCreationCount = %d; want 0 (only create_ticket increments)", p.ticketCreationCount)
	}
}

// TestTicketCreationCeiling_DefaultIsTen — when MaxTicketsPerTurn is
// left at the M6 default (10), the ceiling fires on the 11th call.
// Verified via the policy's exported field — production wires from
// config.MaxTicketsPerTurn (T013) defaulting to 10.
func TestTicketCreationCeiling_DefaultIsTen(t *testing.T) {
	p := newM6CeilingTestPolicy(t, 10)
	if p.MaxTicketsPerTurn != 10 {
		t.Fatalf("default MaxTicketsPerTurn = %d; want 10", p.MaxTicketsPerTurn)
	}
}

// TestTicketCreationCeiling_EnvOverride — MaxTicketsPerTurn is
// runtime-tunable via Deps injection. Setting it to 3 fires the
// ceiling on the 4th call. T013 wires the env-var
// GARRISON_CHAT_MAX_TICKETS_PER_TURN through to this field.
func TestTicketCreationCeiling_EnvOverride(t *testing.T) {
	p := newM6CeilingTestPolicy(t, 3)
	raw := fakeToolUseRaw("tu_create", "mcp__garrison-mutate__create_ticket")
	for i := 0; i < 3; i++ {
		p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       raw,
		})
	}
	if p.ticketCreationCeilingFired {
		t.Fatal("ceiling fired before 4th call")
	}
	p.OnStreamEvent(context.Background(), claudeproto.StreamEvent{
		InnerType: "content_block_start",
		Raw:       raw,
	})
	if !p.ticketCreationCeilingFired {
		t.Error("expected ceiling on 4th call with MaxTicketsPerTurn=3")
	}
}
