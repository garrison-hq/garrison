package hygiene

import (
	"context"
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// RunSweep runs the periodic hygiene sweep on Deps.SweepInterval cadence.
// Each tick calls ListStuckHygieneTransitions and re-evaluates each row
// via the same evaluateAndWrite path as the LISTEN goroutine. This is
// the recovery mechanism for missed notifications (connection drops,
// in-flight races with the trigger + INSERT), identical in spirit to
// M1's processed_at fallback poll.
//
// Returns only on root-ctx cancellation. Survives transient query errors
// (logs and continues); only hard cancellation exits the loop.
func RunSweep(ctx context.Context, deps Deps) error {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	interval := deps.SweepInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	delay := deps.Delay
	if delay <= 0 {
		delay = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := sweepOnce(ctx, deps, delay); err != nil {
				// Log and continue — a transient query error should not
				// kill the sweep goroutine.
				deps.Logger.Warn("hygiene sweep tick failed", "err", err)
			}
		}
	}
}

// sweepOnce queries stuck rows (pending / NULL older than Delay) and
// evaluates each. Batches at LIMIT 100 per tick to bound memory and
// lock surface under load.
func sweepOnce(ctx context.Context, deps Deps, delay time.Duration) error {
	rows, err := deps.Queries.ListStuckHygieneTransitions(ctx, store.ListStuckHygieneTransitionsParams{
		Column1: intervalValue(delay),
		Limit:   100,
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		// ctx.Err() check each iteration so a shutdown mid-sweep exits
		// promptly without re-running another evaluateAndWrite (each
		// of which can take a second for the palace query).
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := evaluateAndWrite(ctx, deps, r.ID, r.TicketID, r.TriggeredByAgentInstanceID); err != nil {
			deps.Logger.Warn("hygiene sweep row failed; will retry next tick",
				"ticket_transition_id", uuidText(r.ID),
				"err", err,
			)
		}
	}
	return nil
}

// intervalValue converts a Go duration into the pgtype.Interval shape
// sqlc generates for the $1::interval cast. Microseconds is the most
// precise field; pgtype will render as appropriate.
func intervalValue(d time.Duration) pgtype.Interval {
	if d <= 0 {
		d = time.Second
	}
	return pgtype.Interval{
		Microseconds: d.Microseconds(),
		Valid:        true,
	}
}
