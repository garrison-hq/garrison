package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// PollQuerier is the subset of *store.Queries the poller needs. Narrow here
// so unit tests can substitute a stub without a real Postgres. Production
// passes either a pool-backed *store.Queries or a tx-bound one — both
// satisfy DBTX underneath.
type PollQuerier interface {
	SelectUnprocessedEvents(ctx context.Context) ([]store.SelectUnprocessedEventsRow, error)
}

// StateWriter is the seam between events and internal/health: the poller
// records each successful poll's wall time so /health can answer "has the
// pipeline made forward progress recently?" plan.md §"/health endpoint"
// thresholds that at 2 * PollInterval.
type StateWriter interface {
	RecordPoll(ts time.Time)
}

// pollOnce runs the FR-007 fallback query once and dispatches each returned
// row through the dispatcher. The store query already caps at 100 rows
// (NFR-009). On success it records the poll wall-time via state.
//
// Errors are returned to the caller; the poll ticker logs and continues on
// transient DB errors — a dropped pool-connection is the listener's problem,
// not the poller's, so there is no reconnect logic here.
func pollOnce(ctx context.Context, q PollQuerier, state StateWriter, dispatcher *Dispatcher) (int, error) {
	rows, err := q.SelectUnprocessedEvents(ctx)
	if err != nil {
		return 0, fmt.Errorf("events: SelectUnprocessedEvents: %w", err)
	}
	// Record the poll wall-time AFTER a successful SELECT — this is the
	// moment we know the pipeline is talking to Postgres.
	if state != nil {
		state.RecordPoll(time.Now())
	}
	dispatched := 0
	for _, r := range rows {
		// The poll path reuses the notification payload shape so dispatcher
		// logic is identical to the LISTEN path. event_outbox.payload already
		// contains the full trigger-written blob, but we only need event_id
		// in the envelope, matching plan.md §"pg_notify contract".
		envelope, err := json.Marshal(struct {
			EventID string `json:"event_id"`
		}{EventID: formatUUID(r.ID)})
		if err != nil {
			return dispatched, fmt.Errorf("events: marshal poll envelope: %w", err)
		}
		if err := dispatcher.Dispatch(ctx, r.Channel, string(envelope)); err != nil {
			// Continue on dispatch errors; a malformed payload for one event
			// should not block the rest of the batch. Caller aggregates.
			continue
		}
		dispatched++
	}
	return dispatched, nil
}

// formatUUID renders a pgtype.UUID as its canonical 8-4-4-4-12 hex string.
// Duplicated deliberately from internal/spawn to keep events free of a spawn
// import — four lines of fmt is cheaper than a shared util package.
func formatUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		u.Bytes[0:4],
		u.Bytes[4:6],
		u.Bytes[6:8],
		u.Bytes[8:10],
		u.Bytes[10:16],
	)
}
