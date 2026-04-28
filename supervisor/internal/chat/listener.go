package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"
)

// RunListener LISTENs on chat.message.sent, dispatches each notify to a
// per-message worker goroutine, and returns when ctx cancels (or on
// non-recoverable connection failure). The listener owns a dedicated
// pgx.Conn (LISTEN is connection-scoped); on connection drop it
// returns the error to the caller's errgroup — the supervisor restart
// is the recovery primitive.
//
// Per-message dispatch joins a child errgroup so SIGTERM cascades:
// in-flight workers complete (subject to TerminalWriteGrace) before
// RunListener returns.
func RunListener(ctx context.Context, deps Deps, worker *Worker) error {
	if deps.Pool == nil {
		return errors.New("chat: RunListener: nil pool")
	}
	conn, err := deps.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("chat: listener acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN \""+ChannelChatMessageSent+"\""); err != nil {
		return fmt.Errorf("chat: LISTEN: %w", err)
	}
	deps.Logger.Info("chat: listener LISTENing", "channel", ChannelChatMessageSent)

	g, gctx := errgroup.WithContext(ctx)
	for {
		notify, err := conn.Conn().WaitForNotification(gctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = g.Wait()
				return nil
			}
			_ = g.Wait()
			return fmt.Errorf("chat: WaitForNotification: %w", err)
		}
		payload := notify.Payload
		g.Go(func() error {
			return dispatchNotify(gctx, deps, worker, payload)
		})
	}
}

func dispatchNotify(ctx context.Context, deps Deps, worker *Worker, payload string) error {
	// Payload contract per FR-050: dashboard emits message_id as the
	// raw UUID text. We resolve it to (session_id, message_id) via
	// GetChatMessageByID; the worker then takes over.
	var msgID pgtype.UUID
	if err := msgID.Scan(payload); err != nil {
		deps.Logger.Warn("chat: invalid message_id in notify payload",
			"payload", payload, "err", err)
		return nil
	}
	row, err := deps.Queries.GetChatMessageByID(ctx, msgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			deps.Logger.Warn("chat: notify references unknown message_id",
				"message_id", uuidString(msgID))
			return nil
		}
		deps.Logger.Error("chat: GetChatMessageByID failed",
			"message_id", uuidString(msgID), "err", err)
		return nil // don't crash listener on lookup failure
	}
	if row.Role != "operator" {
		deps.Logger.Warn("chat: notify references non-operator row",
			"message_id", uuidString(msgID), "role", row.Role)
		return nil
	}
	if err := worker.HandleMessageInSession(ctx, row.SessionID, msgID); err != nil {
		deps.Logger.Error("chat: HandleMessageInSession failed",
			"session_id", uuidString(row.SessionID),
			"message_id", uuidString(msgID), "err", err)
	}
	return nil
}

// RunRestartSweep marks any pending/streaming chat_messages older than
// 60s as aborted with error_kind='supervisor_restart' and rolls their
// parent sessions to status='aborted'. Runs once at supervisor boot,
// BEFORE RunListener starts LISTEN. FR-083.
func RunRestartSweep(ctx context.Context, deps Deps) error {
	cutoff := time.Now().Add(-60 * time.Second)
	cutoffTS := pgtype.Timestamptz{}
	if err := cutoffTS.Scan(cutoff); err != nil {
		return fmt.Errorf("chat: restart sweep cutoff: %w", err)
	}
	ekVal := ErrorSupervisorRestart
	rows, err := deps.Queries.MarkPendingMessagesAborted(ctx, store.MarkPendingMessagesAbortedParams{
		ErrorKind: &ekVal,
		Cutoff:    cutoffTS,
	})
	if err != nil {
		return fmt.Errorf("chat: mark pending aborted: %w", err)
	}
	if len(rows) > 0 {
		if err := deps.Queries.AbortSessionsWithAbortedMessages(ctx, &ekVal); err != nil {
			return fmt.Errorf("chat: abort sessions: %w", err)
		}
		deps.Logger.Info("chat: restart sweep marked rows aborted",
			"count", len(rows))
	}
	return nil
}

// RunIdleSweep ticks every IdleSweepTick interval (default 60s) and
// marks active sessions whose newest chat_messages.created_at is older
// than SessionIdleTimeout as 'ended'. Joins the supervisor's main
// errgroup; respects ctx cancellation. FR-081.
func RunIdleSweep(ctx context.Context, deps Deps) error {
	if deps.SessionIdleTimeout <= 0 {
		deps.Logger.Info("chat: idle sweep disabled (SessionIdleTimeout <= 0)")
		<-ctx.Done()
		return nil
	}
	tick := deps.IdleSweepTick
	if tick <= 0 {
		tick = 60 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := runIdleSweepOnce(ctx, deps); err != nil {
				deps.Logger.Warn("chat: idle sweep tick failed", "err", err)
			}
		}
	}
}

func runIdleSweepOnce(ctx context.Context, deps Deps) error {
	cutoff := time.Now().Add(-deps.SessionIdleTimeout)
	cutoffTS := pgtype.Timestamptz{}
	if err := cutoffTS.Scan(cutoff); err != nil {
		return fmt.Errorf("idle sweep cutoff: %w", err)
	}
	endedIDs, err := deps.Queries.MarkActiveSessionsIdle(ctx, cutoffTS)
	if err != nil {
		return fmt.Errorf("mark idle: %w", err)
	}
	if len(endedIDs) == 0 {
		return nil
	}
	deps.Logger.Info("chat: idle sweep marked sessions ended", "count", len(endedIDs))
	// Per-session pg_notify so dashboards see the close. Each notify
	// runs in its own short tx.
	for _, id := range endedIDs {
		tx, err := deps.Pool.Begin(ctx)
		if err != nil {
			deps.Logger.Warn("chat: idle notify tx begin", "err", err)
			continue
		}
		if err := EmitSessionEnded(ctx, tx, id, "ended"); err != nil {
			_ = tx.Rollback(ctx)
			deps.Logger.Warn("chat: idle notify emit", "err", err)
			continue
		}
		_ = tx.Commit(ctx)
	}
	return nil
}
