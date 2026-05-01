//go:build integration

package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newPolicyWithPool wires a real pool + queries + a fresh assistant
// chat_message into a ChatPolicy so the M5.3 emit + commit paths run
// against real DB rows. Returns the policy and the seeded session +
// message IDs for assertions.
func newPolicyWithPool(t *testing.T, ceiling int) (*ChatPolicy, *pgxpool.Pool, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()

	truncate := func() {
		_, _ = pool.Exec(ctx, "TRUNCATE chat_messages, chat_sessions CASCADE")
	}
	truncate()
	t.Cleanup(truncate)

	operatorID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xee}}
	var sessionID, opMsgID, asstMsgID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd)
		 VALUES ($1, 'active', 0) RETURNING id`, operatorID).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status, content)
		 VALUES ($1, 0, 'operator', 'completed', 'do the thing') RETURNING id`,
		sessionID).Scan(&opMsgID); err != nil {
		t.Fatalf("seed operator msg: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status)
		 VALUES ($1, 1, 'assistant', 'pending') RETURNING id`,
		sessionID).Scan(&asstMsgID); err != nil {
		t.Fatalf("seed assistant msg: %v", err)
	}

	q := store.New(pool)
	policy := &ChatPolicy{
		Pool:            pool,
		Queries:         q,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		SessionID:       sessionID,
		MessageID:       asstMsgID,
		GraceWrite:      time.Second,
		ToolCallCeiling: ceiling,
	}
	return policy, pool, sessionID, asstMsgID
}

// TestPolicyLifecycle_StreamingTurnWithToolUseAndCommit exercises the
// realistic happy-path flow: OnInit → OnStreamEvent for a tool_use
// content block (which fires emitScrubIfPossible + emitToolUseFromRaw)
// → OnResult (which commits the terminal row + RollUpSessionCost +
// EmitMessageSent). Verifies the message row landed at status='completed'
// and the session cost rolled up.
func TestPolicyLifecycle_StreamingTurnWithToolUseAndCommit(t *testing.T) {
	p, pool, sessionID, msgID := newPolicyWithPool(t, 50)
	ctx := context.Background()

	// OnInit transitions message to 'streaming' (best-effort).
	if action := p.OnInit(ctx, claudeproto.InitEvent{
		MCPServers: []claudeproto.MCPServer{{Name: "garrison-mutate", Status: "connected"}},
		SessionID:  "claude-sess-1",
		Model:      "claude-test",
	}); action != claudeproto.RouterActionContinue {
		t.Fatalf("OnInit returned %v; want continue", action)
	}

	// Drive a content_block_start tool_use event — exercises
	// handleToolUseBlockStart → emitScrubIfPossible + emitToolUseFromRaw.
	rawToolUseStart := []byte(`{"event":{"content_block":{"type":"tool_use","id":"tu-1","name":"create_ticket","input":{"x":1}}}}`)
	p.OnStreamEvent(ctx, claudeproto.StreamEvent{
		InnerType: "content_block_start",
		Raw:       rawToolUseStart,
	})

	// A text_delta event accumulates content into contentBuf.
	rawTextDelta := []byte(`{"event":{"delta":{"type":"text_delta","text":"hello"}}}`)
	p.OnStreamEvent(ctx, claudeproto.StreamEvent{
		InnerType: "content_block_delta",
		Inner:     claudeproto.StreamInner{DeltaType: "text_delta", DeltaText: "hello"},
		Raw:       rawTextDelta,
	})

	// OnResult commits the terminal row.
	resRaw := []byte(`{"type":"result","total_cost_usd":"0.01","is_error":false}`)
	p.OnResult(ctx, claudeproto.ResultEvent{
		TotalCostUSD: json.Number("0.01"),
		IsError:      false,
		Raw:          resRaw,
	})

	// Assert chat_messages row is now completed.
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM chat_messages WHERE id = $1`, msgID).Scan(&status); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "completed" {
		t.Errorf("status = %q; want completed", status)
	}

	// Session cost rolled up.
	var cost string
	if err := pool.QueryRow(ctx, `SELECT total_cost_usd::text FROM chat_sessions WHERE id = $1`, sessionID).Scan(&cost); err != nil {
		t.Fatalf("session cost: %v", err)
	}
	if !strings.HasPrefix(cost, "0.01") {
		t.Errorf("session cost = %q; want 0.01...", cost)
	}
}

// TestPolicyLifecycle_OnResultWithIsErrorMarksFailed: when the result
// event carries is_error=true, the terminal commit lands at
// status='failed' with error_kind set to claude_runtime_error.
func TestPolicyLifecycle_OnResultWithIsErrorMarksFailed(t *testing.T) {
	p, pool, _, msgID := newPolicyWithPool(t, 50)
	ctx := context.Background()
	p.OnResult(ctx, claudeproto.ResultEvent{
		TotalCostUSD: json.Number("0.05"),
		IsError:      true,
		Raw:          []byte(`{"type":"result","is_error":true}`),
	})
	var status string
	var ek *string
	if err := pool.QueryRow(ctx,
		`SELECT status, error_kind FROM chat_messages WHERE id = $1`, msgID).
		Scan(&status, &ek); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q; want failed", status)
	}
	if ek == nil || *ek == "" {
		t.Errorf("error_kind should be set; got %v", ek)
	}
}

// TestPolicyLifecycle_ToolCallCeilingFires drives more tool_use events
// than the ceiling — exercises maybeFireToolCallCeiling's emit branch
// (the EmitAssistantError pg_notify call) and bailReason population.
func TestPolicyLifecycle_ToolCallCeilingFires(t *testing.T) {
	p, _, _, _ := newPolicyWithPool(t, 2) // ceiling = 2
	ctx := context.Background()

	rawToolUse := []byte(`{"event":{"content_block":{"type":"tool_use","id":"tu-x","name":"create_ticket","input":{}}}}`)
	for i := 0; i < 3; i++ {
		p.OnStreamEvent(ctx, claudeproto.StreamEvent{
			InnerType: "content_block_start",
			Raw:       rawToolUse,
		})
	}
	if !p.ceilingFired {
		t.Error("ceilingFired should be true after exceeding ceiling")
	}
	if p.bailReason != ChatErrorToolCallCeilingReached {
		t.Errorf("bailReason = %q; want ceiling-reached", p.bailReason)
	}
}

// TestPolicyLifecycle_TerminalWriteErrorPersists: with a real pool and
// queries, terminalWriteError actually writes the failed row + error_kind.
func TestPolicyLifecycle_TerminalWriteErrorPersists(t *testing.T) {
	p, pool, _, msgID := newPolicyWithPool(t, 50)
	ctx := context.Background()
	p.terminalWriteError(ctx, ErrorClaudeRuntimeError)

	var status string
	var ek *string
	if err := pool.QueryRow(ctx,
		`SELECT status, error_kind FROM chat_messages WHERE id = $1`, msgID).Scan(&status, &ek); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q; want failed", status)
	}
	if ek == nil || *ek != string(ErrorClaudeRuntimeError) {
		t.Errorf("error_kind = %v; want %q", ek, ErrorClaudeRuntimeError)
	}
}

// TestPolicyLifecycle_OnTerminateBailWritesError: OnTerminate("bail")
// with a populated bailReason should terminal-write with that reason.
func TestPolicyLifecycle_OnTerminateBailWritesError(t *testing.T) {
	p, pool, _, msgID := newPolicyWithPool(t, 50)
	p.bailReason = ErrorKind("mcp_garrison_mutate_failed")
	ctx := context.Background()
	p.OnTerminate(ctx, "bail")
	var ek *string
	if err := pool.QueryRow(ctx,
		`SELECT error_kind FROM chat_messages WHERE id = $1`, msgID).Scan(&ek); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if ek == nil || *ek != "mcp_garrison_mutate_failed" {
		t.Errorf("error_kind = %v; want mcp_garrison_mutate_failed", ek)
	}
}
