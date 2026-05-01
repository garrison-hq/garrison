//go:build integration

package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// transportFixture seeds a fresh chat_session + chat_message so the
// transport-layer Emit helpers have IDs to bind into the
// notify-payload JSON. pg_notify is fire-and-forget so we don't assert
// receipt — exercising the Emit path is enough to record coverage on
// the JSON shape, payload-size guard, and pgx Exec path.
func newTransportFixture(t *testing.T) (*pgxpool.Pool, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()
	truncate := func() {
		_, _ = pool.Exec(ctx, "TRUNCATE chat_messages, chat_sessions CASCADE")
	}
	truncate()
	t.Cleanup(truncate)

	operatorID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xde}}
	var sessionID, messageID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd)
		 VALUES ($1, 'active', 0) RETURNING id`, operatorID).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status, content)
		 VALUES ($1, 0, 'operator', 'completed', 'hello') RETURNING id`,
		sessionID).Scan(&messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return pool, sessionID, messageID
}

func TestEmitDelta_HappyPath(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	if err := EmitDelta(context.Background(), pool, msgID, 0, 0, "hello world"); err != nil {
		t.Fatalf("EmitDelta: %v", err)
	}
}

func TestEmitDelta_RejectsOversizedPayload(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	huge := strings.Repeat("a", 8000) // exceeds MaxNotifyPayloadBytes
	err := EmitDelta(context.Background(), pool, msgID, 0, 0, huge)
	if err == nil || !strings.Contains(err.Error(), "exceeds ceiling") {
		t.Errorf("expected size-guard error; got %v", err)
	}
}

func TestEmitScrub_HappyPath(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	if err := EmitScrub(context.Background(), pool, msgID, 1, 5); err != nil {
		t.Fatalf("EmitScrub: %v", err)
	}
}

func TestEmitToolUse_HappyPath(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	args := json.RawMessage(`{"objective":"x","department_slug":"engineering"}`)
	if err := EmitToolUse(context.Background(), pool, msgID, "tool-use-id-1", "create_ticket", args); err != nil {
		t.Fatalf("EmitToolUse: %v", err)
	}
}

func TestEmitToolUse_TruncatesOversized(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	huge := json.RawMessage(`{"x":"` + strings.Repeat("a", 8000) + `"}`)
	if err := EmitToolUse(context.Background(), pool, msgID, "tool-use-id-2", "create_ticket", huge); err != nil {
		t.Fatalf("EmitToolUse should retry with trimmed args; got %v", err)
	}
}

func TestEmitToolResult_HappyPath(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	res := json.RawMessage(`{"success":true,"affected_resource_id":"abc"}`)
	if err := EmitToolResult(context.Background(), pool, msgID, "tool-use-id-3", false, res); err != nil {
		t.Fatalf("EmitToolResult: %v", err)
	}
}

func TestEmitToolResult_TruncatesOversized(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	huge := json.RawMessage(`{"data":"` + strings.Repeat("z", 8000) + `"}`)
	if err := EmitToolResult(context.Background(), pool, msgID, "tool-use-id-4", false, huge); err != nil {
		t.Fatalf("EmitToolResult should retry with trimmed result; got %v", err)
	}
}

func TestEmitAssistantError_HappyPath(t *testing.T) {
	pool, _, msgID := newTransportFixture(t)
	if err := EmitAssistantError(context.Background(), pool, msgID, "tool_call_ceiling_reached", "exceeded 50 tool calls"); err != nil {
		t.Fatalf("EmitAssistantError: %v", err)
	}
}

func TestEmitSessionStarted_AndEnded_AndMessageSent_InTx(t *testing.T) {
	pool, sessionID, msgID := newTransportFixture(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	userID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xee}}
	if err := EmitSessionStarted(ctx, tx, sessionID, userID); err != nil {
		t.Fatalf("EmitSessionStarted: %v", err)
	}
	if err := EmitMessageSent(ctx, tx, sessionID, msgID); err != nil {
		t.Fatalf("EmitMessageSent: %v", err)
	}
	if err := EmitSessionEnded(ctx, tx, sessionID, "ended"); err != nil {
		t.Fatalf("EmitSessionEnded: %v", err)
	}
}

func TestEnsureActiveSession_ActiveReturnsRow(t *testing.T) {
	pool, sessionID, _ := newTransportFixture(t)
	q := store.New(pool)
	got, err := EnsureActiveSession(context.Background(), q, sessionID)
	if err != nil {
		t.Fatalf("EnsureActiveSession: %v", err)
	}
	if got.ID != sessionID {
		t.Errorf("returned session id mismatch: %v vs %v", got.ID, sessionID)
	}
	if got.Status != "active" {
		t.Errorf("status = %q; want active", got.Status)
	}
}

func TestEnsureActiveSession_EndedReturnsError(t *testing.T) {
	pool, sessionID, _ := newTransportFixture(t)
	if _, err := pool.Exec(context.Background(),
		`UPDATE chat_sessions SET status = 'ended', ended_at = NOW() WHERE id = $1`,
		sessionID); err != nil {
		t.Fatalf("end session: %v", err)
	}
	q := store.New(pool)
	_, err := EnsureActiveSession(context.Background(), q, sessionID)
	if err == nil {
		t.Fatal("expected error when session is ended")
	}
}

func TestInsertAssistantPending_InsertsRow(t *testing.T) {
	pool, sessionID, _ := newTransportFixture(t)
	q := store.New(pool)
	got, err := InsertAssistantPending(context.Background(), q, sessionID, 1)
	if err != nil {
		t.Fatalf("InsertAssistantPending: %v", err)
	}
	if got.SessionID != sessionID {
		t.Errorf("session FK mismatch")
	}
	if got.Role != "assistant" || got.Status != "pending" {
		t.Errorf("row shape wrong: role=%q status=%q", got.Role, got.Status)
	}
	if got.TurnIndex != 1 {
		t.Errorf("turn_index = %d; want 1", got.TurnIndex)
	}
}
