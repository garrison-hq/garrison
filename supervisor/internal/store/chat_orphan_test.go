//go:build integration

// M5.2 — sqlc query unit tests for the orphan-row sweep extension
// (T002). Runs against the shared testcontainer pool.

package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func ptrFn(s string) *string { return &s }

func ts(at time.Time) pgtype.Timestamptz {
	var t pgtype.Timestamptz
	_ = t.Scan(at)
	return t
}

// makeUUID returns a pgtype.UUID from os.Getpid + counter so consecutive
// calls within a single test produce unique values without bringing in
// crypto/rand.
var orphanCounter int

func makeUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	orphanCounter++
	now := time.Now().UnixNano()
	return pgtype.UUID{
		Bytes: [16]byte{
			byte(orphanCounter), byte(orphanCounter >> 8),
			byte(now), byte(now >> 8), byte(now >> 16), byte(now >> 24),
			byte(now >> 32), byte(now >> 40), byte(now >> 48), byte(now >> 56),
			0xCA, 0xFE, 0xBA, 0xBE, 0x12, 0x34,
		},
		Valid: true,
	}
}

// TestFindOrphanedOperatorSessionsMatchesExpected — seed a session with
// a single operator row aged 120s and no assistant pair; assert the
// query returns exactly one row with the seeded session_id +
// operator_message_id + orphan_turn_index.
func TestFindOrphanedOperatorSessionsMatchesExpected(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	sess, err := q.CreateChatSession(ctx, makeUUID(t))
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	op, err := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("hello"),
	})
	if err != nil {
		t.Fatalf("InsertOperatorMessage: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - interval '120 seconds' WHERE id = $1",
		op.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rows, err := q.FindOrphanedOperatorSessions(ctx, ts(time.Now().Add(-60*time.Second)))
	if err != nil {
		t.Fatalf("FindOrphanedOperatorSessions: %v", err)
	}
	var match *store.FindOrphanedOperatorSessionsRow
	for i := range rows {
		if rows[i].SessionID == sess.ID {
			match = &rows[i]
			break
		}
	}
	if match == nil {
		t.Fatalf("seeded session not in result")
	}
	if match.OperatorMessageID != op.ID {
		t.Errorf("operator_message_id mismatch")
	}
	if match.OrphanTurnIndex != op.TurnIndex {
		t.Errorf("orphan_turn_index=%d; want %d", match.OrphanTurnIndex, op.TurnIndex)
	}
}

// TestFindOrphanedOperatorSessionsIgnoresAssistantPair — seed an
// operator-assistant pair (assistant 'completed') with the operator
// aged 120s. Assert the session does NOT appear in the orphan result.
func TestFindOrphanedOperatorSessionsIgnoresAssistantPair(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	sess, _ := q.CreateChatSession(ctx, makeUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("paired"),
	})
	asst, _ := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: sess.ID, TurnIndex: op.TurnIndex + 1,
	})
	if err := q.CommitAssistantTerminal(ctx, store.CommitAssistantTerminalParams{
		ID:      asst.ID,
		Status:  "completed",
		Content: ptrFn("ok"),
	}); err != nil {
		t.Fatalf("CommitAssistantTerminal: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - interval '120 seconds' WHERE id = $1",
		op.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rows, _ := q.FindOrphanedOperatorSessions(ctx, ts(time.Now().Add(-60*time.Second)))
	for _, r := range rows {
		if r.SessionID == sess.ID {
			t.Errorf("paired session present in orphan result; want excluded")
		}
	}
}

// TestFindOrphanedOperatorSessionsRespectsCutoff — seed an operator row
// at exactly the cutoff boundary and assert the strict-less-than
// predicate excludes it. Pin the boundary so a future kernel-clock
// drift bug won't slip orphans through silently.
func TestFindOrphanedOperatorSessionsRespectsCutoff(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	sess, _ := q.CreateChatSession(ctx, makeUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("boundary"),
	})
	// Exact cutoff: created_at = cutoff (i.e., NOT strictly less than).
	cutoff := time.Now().Add(-60 * time.Second)
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = $1 WHERE id = $2",
		cutoff, op.ID); err != nil {
		t.Fatalf("set boundary created_at: %v", err)
	}

	rows, _ := q.FindOrphanedOperatorSessions(ctx, ts(cutoff))
	for _, r := range rows {
		if r.SessionID == sess.ID {
			t.Errorf("boundary row included in orphan result; want excluded by strict less-than")
		}
	}
}

// TestFindOrphanedOperatorSessionsIgnoresMultipleOlderTurns — seed a
// session with op-asst-op-asst-op (final operator is the orphan).
// Earlier completed pairs must not appear; only the final orphan row
// matches because it's the latest turn_index without a successor.
func TestFindOrphanedOperatorSessionsIgnoresMultipleOlderTurns(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	sess, _ := q.CreateChatSession(ctx, makeUUID(t))

	// Turn 0 — operator + completed assistant.
	op0, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("turn 0 op"),
	})
	asst0, _ := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: sess.ID, TurnIndex: op0.TurnIndex + 1,
	})
	_ = q.CommitAssistantTerminal(ctx, store.CommitAssistantTerminalParams{
		ID: asst0.ID, Status: "completed", Content: ptrFn("a0"),
	})

	// Turn 2 — operator + completed assistant.
	op2, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("turn 2 op"),
	})
	asst2, _ := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: sess.ID, TurnIndex: op2.TurnIndex + 1,
	})
	_ = q.CommitAssistantTerminal(ctx, store.CommitAssistantTerminalParams{
		ID: asst2.ID, Status: "completed", Content: ptrFn("a2"),
	})

	// Turn 4 — operator only, the orphan. Backdate it.
	op4, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("turn 4 op"),
	})
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - interval '120 seconds' WHERE id = $1",
		op4.ID); err != nil {
		t.Fatalf("backdate orphan: %v", err)
	}

	rows, _ := q.FindOrphanedOperatorSessions(ctx, ts(time.Now().Add(-60*time.Second)))
	matches := 0
	for _, r := range rows {
		if r.SessionID == sess.ID {
			matches++
			if r.OrphanTurnIndex != op4.TurnIndex {
				t.Errorf("orphan_turn_index=%d; want %d (final operator turn)",
					r.OrphanTurnIndex, op4.TurnIndex)
			}
		}
	}
	if matches != 1 {
		t.Errorf("matches=%d; want exactly 1 (only the final orphan, not prior pairs)", matches)
	}
}

// TestInsertSyntheticAssistantAbortedRespectsTurnIndexUnique — seed
// operator at turn 5; insert synthetic at turn 6 (succeeds); attempt
// a second synthetic insert at turn 6 (must fail with the
// session_id+turn_index UNIQUE violation). Pins schema integrity
// against accidental double-sweep replays.
func TestInsertSyntheticAssistantAbortedRespectsTurnIndexUnique(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	sess, _ := q.CreateChatSession(ctx, makeUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("synthetic test"),
	})

	turnSyn := op.TurnIndex + 1
	if err := q.InsertSyntheticAssistantAborted(ctx, store.InsertSyntheticAssistantAbortedParams{
		SessionID: sess.ID,
		TurnIndex: turnSyn,
		Content:   ptrFn("[supervisor restarted]"),
		ErrorKind: ptrFn("supervisor_restart"),
	}); err != nil {
		t.Fatalf("first InsertSyntheticAssistantAborted: %v", err)
	}

	err := q.InsertSyntheticAssistantAborted(ctx, store.InsertSyntheticAssistantAbortedParams{
		SessionID: sess.ID,
		TurnIndex: turnSyn,
		Content:   ptrFn("[supervisor restarted again]"),
		ErrorKind: ptrFn("supervisor_restart"),
	})
	if err == nil {
		t.Fatal("second InsertSyntheticAssistantAborted at same turn returned nil; want UNIQUE violation")
	}
	if !strings.Contains(err.Error(), "chat_messages_session_id_turn_index_key") &&
		!strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error %v doesn't mention chat_messages unique constraint or duplicate key", err)
	}
}
