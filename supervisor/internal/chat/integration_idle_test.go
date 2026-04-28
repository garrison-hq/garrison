//go:build integration

// SC-013 idle-timeout integration test. Runs the production
// RunIdleSweep loop with a tight tick + short timeout so the assertion
// completes in a few seconds.

package chat

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestM5_1_IdleSweep_MarksSessionEnded creates a session, backdates
// its newest message by 10s, sets a 5s idle timeout + 1s sweep tick,
// runs RunIdleSweep for 3 seconds, then asserts:
//   - the session was transitioned active → ended
//   - a `work.chat.session_ended` notify fired
//   - a subsequent operator message on the ended session terminal-
//     writes the assistant row with error_kind='session_ended'
func TestM5_1_IdleSweep_MarksSessionEnded(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	// Backdate the message so it's already older than the idle
	// timeout from the test's perspective.
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - interval '10 seconds' WHERE id = $1",
		op.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	deps := Deps{
		Pool:               pool,
		Queries:            q,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		SessionIdleTimeout: 5 * time.Second,
		IdleSweepTick:      500 * time.Millisecond,
	}

	// Subscribe to work.chat.session_ended on a separate connection so
	// we can verify the notify fires within the sweep window.
	listenConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("listen acquire: %v", err)
	}
	defer listenConn.Release()
	if _, err := listenConn.Exec(ctx, `LISTEN "work.chat.session_ended"`); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}

	// Run the sweep for 3 seconds (enough for ~6 ticks).
	sweepCtx, sweepCancel := context.WithTimeout(ctx, 3*time.Second)
	defer sweepCancel()
	go func() {
		_ = RunIdleSweep(sweepCtx, deps)
	}()

	// Wait for the work.chat.session_ended notify referencing our session.
	notifyCtx, notifyCancel := context.WithTimeout(ctx, 5*time.Second)
	defer notifyCancel()
	gotEnded := false
	for !gotEnded {
		notify, err := listenConn.Conn().WaitForNotification(notifyCtx)
		if err != nil {
			break
		}
		if notify.Channel == "work.chat.session_ended" {
			gotEnded = true
		}
	}
	if !gotEnded {
		t.Errorf("expected work.chat.session_ended notify; none received in 5s")
	}

	// Assert the session row transitioned active → ended.
	updated, err := q.GetChatSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetChatSession: %v", err)
	}
	if updated.Status != "ended" {
		t.Errorf("session.status = %q; want ended", updated.Status)
	}

	// Subsequent operator message on the ended session should land
	// with error_kind='session_ended'.
	op2, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("more"),
	})
	wDeps := minimalEdgeDeps(t, pool, q, &fakeVault{}, &fakeDockerExec{})
	w := NewWorker(wDeps, "/bin/true", "postgres://test/test", MempalaceWiring{})
	_ = w.HandleMessageInSession(ctx, sess.ID, op2.ID)

	// Look at the LAST assistant row (the one synthesised for op2).
	var status, ek string
	row := pool.QueryRow(ctx,
		`SELECT status, COALESCE(error_kind, '')
		   FROM chat_messages
		  WHERE session_id = $1 AND role = 'assistant'
		  ORDER BY turn_index DESC LIMIT 1`, sess.ID)
	if err := row.Scan(&status, &ek); err != nil {
		t.Fatalf("scan assistant row: %v", err)
	}
	if status != "failed" || ek != ErrorSessionEnded {
		t.Errorf("post-idle assistant row: status=%q error_kind=%q; want failed/%s",
			status, ek, ErrorSessionEnded)
	}
}

// Suppress unused-import warning (pgtype.UUID is via newUUID helper).
var _ pgtype.UUID
