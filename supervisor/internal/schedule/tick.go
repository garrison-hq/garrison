package schedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// defaultClaimLimit bounds the per-tick claim transaction size when
// Deps.ClaimLimit is unset. Remaining due tasks claim on the next
// tick — worst-case one-tick slip, within FR-102's drift tolerance.
const defaultClaimLimit = 20

// RunLoop drives the M9 tick loop: every Deps.TickInterval it runs
// one tickOnce claim-and-fire transaction (FR-101/FR-102). Managed by
// main's errgroup; returns nil on context cancellation so a graceful
// shutdown does not poison sibling subsystems. Transient tick errors
// are logged and the ticker continues — a single failed tick must not
// bring the supervisor down (M1 poll-ticker precedent,
// events/reconnect.go).
func RunLoop(ctx context.Context, deps Deps) error {
	ticker := time.NewTicker(deps.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			fired, skipped, deferred, err := tickOnce(ctx, deps)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				deps.Logger.Error("schedule: tick failed", "error", err)
				continue
			}
			if fired+skipped+deferred > 0 {
				deps.Logger.Info("schedule: tick fired due tasks",
					"fired", fired,
					"skipped_overlap", skipped,
					"gate_deferred", deferred,
				)
			}
		}
	}
}

// effectiveClaimLimit resolves Deps.ClaimLimit, defaulting to 20.
func effectiveClaimLimit(deps Deps) int32 {
	if deps.ClaimLimit <= 0 {
		return defaultClaimLimit
	}
	return int32(deps.ClaimLimit)
}

// tickOnce is plan §1's firing transaction: one short tx claiming due
// tasks (FOR UPDATE SKIP LOCKED — each due slot fires at most once
// regardless of concurrent claim attempts, FR-101), firing each per
// its mode, advancing every processed task exactly one future slot
// (FR-104), then committing. Paused and soft-deleted tasks are
// excluded by the claim query itself. When no tasks are due the tick
// performs no spawns and consumes no model tokens (FR-109).
//
// All-or-nothing on error: a DB failure mid-task rolls the whole
// claim back; the still-due rows are re-claimed on the next tick.
func tickOnce(ctx context.Context, deps Deps) (fired, skipped, deferred int, err error) {
	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("schedule: begin tick tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := deps.Queries.WithTx(tx)
	tasks, err := q.ClaimDueScheduledTasks(ctx, effectiveClaimLimit(deps))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("schedule: ClaimDueScheduledTasks: %w", err)
	}
	for _, task := range tasks {
		outcome, ferr := fireClaimedTask(ctx, q, tx, deps, task, now().UTC())
		if ferr != nil {
			return 0, 0, 0, ferr
		}
		switch outcome {
		case OutcomeFired:
			fired++
		case OutcomeSkippedOverlap:
			skipped++
		case OutcomeGateDeferred:
			deferred++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, 0, fmt.Errorf("schedule: commit tick tx: %w", err)
	}
	return fired, skipped, deferred, nil
}

// fireClaimedTask processes one claimed task inside the tick tx:
// parse → overlap predicate → mode-specific firing → advance. Returns
// the run outcome written for the slot, or "" for the corrupt-
// expression defensive path (no run row, task left un-advanced).
func fireClaimedTask(ctx context.Context, q *store.Queries, tx pgx.Tx, deps Deps, task store.ScheduledTask, now time.Time) (string, error) {
	// Defensive only — validation (FR-105) makes a corrupt expression
	// unreachable through any authoring surface. A corrupted row logs
	// at error, fires nothing, and stays un-advanced for operator
	// repair (it cannot be advanced anyway: Next needs the grammar).
	expr, err := Parse(task.ScheduleExpr)
	if err != nil {
		deps.Logger.Error("schedule: corrupt schedule expression; task left un-advanced for operator repair",
			"scheduled_task_id", uuidString(task.ID),
			"name", task.Name,
			"schedule_expr", task.ScheduleExpr,
			"error", err,
		)
		return "", nil
	}

	overlapping, err := hasOverlap(ctx, q, task)
	if err != nil {
		return "", err
	}

	var outcome string
	switch {
	case overlapping:
		// Skip-and-advance (FR-202 ticket half, FR-303 oneshot half):
		// no new work, a typed run record, slot consumed.
		detail := overlapDetail(task.Mode)
		if _, err := insertRun(ctx, q, task, OutcomeSkippedOverlap, &detail, pgtype.UUID{}); err != nil {
			return "", err
		}
		outcome = OutcomeSkippedOverlap
	case task.Mode == ModeOneshot:
		outcome, err = fireOneshotMode(ctx, q, tx, task)
	default:
		outcome, err = fireTicketMode(ctx, q, task, now)
	}
	if err != nil {
		return "", err
	}

	// Always exactly one future slot regardless of outcome — collapse,
	// skip, and defer all consume the slot (FR-104, Q6/Q7 semantics).
	// last_fired_at updates ONLY when the slot's outcome is fired
	// (FR-107: {{last_fired_at}} means the previous *firing*, not the
	// previous claim).
	if err := q.AdvanceScheduledTask(ctx, store.AdvanceScheduledTaskParams{
		NextFireAt: pgtype.Timestamptz{Time: expr.Next(now), Valid: true},
		Fired:      outcome == OutcomeFired,
		FiredAt:    pgtype.Timestamptz{Time: now, Valid: true},
		ID:         task.ID,
	}); err != nil {
		return "", fmt.Errorf("schedule: AdvanceScheduledTask: %w", err)
	}
	return outcome, nil
}

// hasOverlap evaluates the per-mode overlap predicate against the
// tick tx's snapshot.
func hasOverlap(ctx context.Context, q *store.Queries, task store.ScheduledTask) (bool, error) {
	if task.Mode == ModeOneshot {
		overlapping, err := q.HasRunningOneshotForTask(ctx, task.ID)
		if err != nil {
			return false, fmt.Errorf("schedule: HasRunningOneshotForTask: %w", err)
		}
		return overlapping, nil
	}
	overlapping, err := q.HasOpenTicketForTask(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("schedule: HasOpenTicketForTask: %w", err)
	}
	return overlapping, nil
}

// overlapDetail renders the human-readable reason recorded on a
// skipped_overlap run row.
func overlapDetail(mode string) string {
	if mode == ModeOneshot {
		return "previous oneshot firing still in flight; slot skipped (FR-303)"
	}
	return "previously fired ticket still open; slot skipped (FR-202)"
}
