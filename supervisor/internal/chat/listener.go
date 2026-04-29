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

// RunListener LISTENs on chat.message.sent and dispatches each notify
// to a per-message worker goroutine. Reconnects on transient pgx errors
// with exponential backoff (100ms → 30s cap), matching the hygiene/events
// listener pattern — a Postgres backend drop must not cascade-kill the
// supervisor.
//
// Per-message dispatch joins a child errgroup scoped to the lifetime of
// each LISTEN cycle, so SIGTERM cascades cleanly: in-flight workers
// complete (subject to TerminalWriteGrace) before RunListener returns.
func RunListener(ctx context.Context, deps Deps, worker *Worker) error {
	if deps.Pool == nil {
		return errors.New("chat: RunListener: nil pool")
	}
	backoff := 100 * time.Millisecond
	const maxBackoff = 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := runChatListenCycle(ctx, deps, worker)
		if ctx.Err() != nil {
			return nil
		}
		deps.Logger.Warn("chat: listener lost connection; reconnecting",
			"err", err, "backoff", backoff)
		if sleepCtx(ctx, backoff) != nil {
			return nil
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runChatListenCycle acquires a conn, issues LISTEN, and dispatches
// notifications until the conn dies or ctx cancels. Returns nil on
// ctx-cancel and a wrapped error on any other failure (so the caller
// can reconnect).
func runChatListenCycle(ctx context.Context, deps Deps, worker *Worker) error {
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

// sleepCtx sleeps for d or until ctx is cancelled. Returns ctx.Err() on
// cancel and nil otherwise.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
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
//
// M5.2 — also runs a second pass that detects "orphan operator rows":
// sessions whose newest chat_messages row is role='operator', older than
// the same 60s cutoff, with no assistant pair at turn_index+1. The
// pending-message pass cannot catch these because there's no pending/
// streaming row to filter on; the orphan case happens when the supervisor
// crashes between the operator INSERT (committed by the dashboard) and
// the assistant pending-row creation (supervisor side). For each match
// the sweep synthesises an aborted assistant row at turn_index+1 with
// error_kind='supervisor_restart' and rolls the session to 'aborted'.
//
// Per FR-292 + plan §1.14: no pg_notify is emitted for synthetic rows.
// The dashboard sees the aborted state on next page load or SSE reconnect.
//
// Per AGENTS.md concurrency rule 6: orphan terminal writes run under
// context.WithoutCancel(ctx) + TerminalWriteGrace so a SIGTERM mid-sweep
// still commits the synthetic row instead of leaving the operator-side
// row dangling.
func RunRestartSweep(ctx context.Context, deps Deps) error {
	cutoff := time.Now().Add(-60 * time.Second)
	cutoffTS := pgtype.Timestamptz{}
	if err := cutoffTS.Scan(cutoff); err != nil {
		return fmt.Errorf("chat: restart sweep cutoff: %w", err)
	}

	// First pass — M5.1: pending/streaming messages older than 60s.
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

	// Second pass — M5.2: orphan operator rows (FR-290–292).
	if err := runOrphanOperatorSweep(ctx, deps, cutoffTS); err != nil {
		return fmt.Errorf("chat: orphan sweep: %w", err)
	}
	return nil
}

// orphanSyntheticContent is the literal content the orphan-row sweep
// writes into the synthetic assistant row. Exported as a const so tests
// can pin the exact string (UI side renders this verbatim per FR-271).
const orphanSyntheticContent = "[supervisor restarted before this turn could complete]"

// runOrphanOperatorSweep detects sessions where the newest chat_messages
// row is role='operator' older than the supplied cutoff, with no
// assistant pair, and synthesises an aborted assistant row +
// chat_sessions.status='aborted' for each. Pure additive: never touches
// the pending-message pass's outputs.
func runOrphanOperatorSweep(ctx context.Context, deps Deps, cutoffTS pgtype.Timestamptz) error {
	orphans, err := deps.Queries.FindOrphanedOperatorSessions(ctx, cutoffTS)
	if err != nil {
		return fmt.Errorf("find orphans: %w", err)
	}
	if len(orphans) == 0 {
		return nil
	}

	grace := deps.TerminalWriteGrace
	if grace <= 0 {
		grace = 5 * time.Second
	}

	synthesised := 0
	for _, o := range orphans {
		if err := writeOrphanSyntheticTerminal(ctx, deps, o, grace); err != nil {
			deps.Logger.Warn("chat: orphan sweep synthesise failed",
				"session_id", uuidString(o.SessionID),
				"err", err)
			continue
		}
		synthesised++
		deps.Logger.Info("chat: orphan sweep synthesised aborted",
			"session_id", uuidString(o.SessionID),
			"turn_index", o.OrphanTurnIndex+1)
	}
	deps.Logger.Info("chat: orphan sweep summary",
		"matched", len(orphans), "synthesised", synthesised)
	return nil
}

// writeOrphanSyntheticTerminal commits the synthetic-aborted assistant
// row + flips the session to aborted, both inside one tx, under a
// WithoutCancel grace context so a SIGTERM mid-sweep still gets the
// invariant committed.
func writeOrphanSyntheticTerminal(parent context.Context, deps Deps, o store.FindOrphanedOperatorSessionsRow, grace time.Duration) error {
	twCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), grace)
	defer cancel()

	tx, err := deps.Pool.Begin(twCtx)
	if err != nil {
		return fmt.Errorf("tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback(twCtx) }()

	q := deps.Queries.WithTx(tx)
	content := orphanSyntheticContent
	ek := ErrorSupervisorRestart
	if err := q.InsertSyntheticAssistantAborted(twCtx, store.InsertSyntheticAssistantAbortedParams{
		SessionID: o.SessionID,
		TurnIndex: o.OrphanTurnIndex + 1,
		Content:   &content,
		ErrorKind: &ek,
	}); err != nil {
		return fmt.Errorf("insert synthetic: %w", err)
	}
	if err := q.MarkSessionAborted(twCtx, o.SessionID); err != nil {
		return fmt.Errorf("mark session aborted: %w", err)
	}
	if err := tx.Commit(twCtx); err != nil {
		return fmt.Errorf("commit: %w", err)
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
