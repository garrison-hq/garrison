package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/pgdb"
	"github.com/garrison-hq/garrison/supervisor/internal/recovery"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps bundles the runtime collaborators Run needs. Constructed by
// cmd/supervisor (T012) and passed verbatim.
type Deps struct {
	// Pool is the shared pgxpool used by pollOnce and by recovery on startup.
	Pool *pgxpool.Pool
	// Queries is a pool-bound *store.Queries. Handler callbacks still open
	// their own transactions via Pool for the dedupe/terminal writes.
	Queries *store.Queries
	// InitialListenConn is the dedicated *pgx.Conn that main.go connected
	// and advisory-locked before calling Run. Run takes ownership: closes it
	// on every LISTEN-loop exit and redials on reconnect.
	InitialListenConn *pgx.Conn
	// Dialer is used to redial the dedicated LISTEN connection after a drop.
	// Production passes a realDialer; tests substitute a fake. The pool is
	// never redialed — pgxpool handles its own per-conn reconnects.
	Dialer pgdb.Dialer
	// DatabaseURL is the URL Dialer.DialConn receives on redial.
	DatabaseURL string
	// Dispatcher is the static route table (FR-014).
	Dispatcher *Dispatcher
	// State is the health-shared LastPollAt recorder. nil is tolerated for
	// tests that don't wire health.
	State StateWriter
	// PollInterval is the fallback-poll cadence between LISTEN notifications.
	PollInterval time.Duration
	// Logger receives one structured record per lifecycle transition.
	Logger *slog.Logger
}

// Run drives the pg_notify listener-connection lifecycle from plan.md
// §"pg_notify listener connection lifecycle" — including reconnect — until
// ctx is cancelled. The initial caller must have already (a) established
// Pool + InitialListenConn, (b) acquired the FR-018 advisory lock on the
// listen conn, and (c) NOT yet run recovery. Run executes recovery as step 3
// of the initial-connect sequence and omits it from every reconnect per the
// plan's "Note on reconnect path and recovery".
//
// The return value is nil on ctx-cancel (clean shutdown) or a wrapped error
// if an unrecoverable failure occurs — e.g. recovery query fails on startup
// in a way that isn't transient.
func Run(ctx context.Context, deps Deps) error {
	// Step 3: FR-011 recovery — once per process, before the first poll.
	n, err := recovery.RunOnce(ctx, deps.Queries)
	if err != nil {
		return fmt.Errorf("events: initial recovery: %w", err)
	}
	deps.Logger.Info("startup recovery ran", "reconciled", n)

	conn := deps.InitialListenConn
	initial := true
	var bo pgdb.Backoff

	for {
		if err := ctx.Err(); err != nil {
			if conn != nil {
				_ = conn.Close(ctx)
			}
			return nil
		}

		// Step 4 (initial) and "poll before LISTEN" (reconnect): one poll
		// drain before (re-)issuing LISTEN so events that arrived during the
		// outage are not missed (FR-007 + acceptance scenario US3 step 2).
		if _, err := pollOnce(ctx, deps.Queries, deps.State, deps.Dispatcher); err != nil {
			deps.Logger.Error("initial poll failed", "error", err)
			// Fall through to LISTEN; the ticker in runPollTicker will retry.
		} else if initial {
			deps.Logger.Info("initial fallback poll ran")
		}

		// Step 5–6: LISTEN until error or ctx cancel; ticker-driven polls
		// continue in a sibling goroutine.
		listenErr := runListenWithPollTicker(ctx, conn, deps)
		if conn != nil {
			_ = conn.Close(context.Background())
			conn = nil
		}
		if ctx.Err() != nil {
			return nil
		}

		// LISTEN returned an error — treat as a reconnect signal.
		deps.Logger.Warn("LISTEN loop exited; will reconnect", "error", listenErr)

		// NFR-002 backoff before redialing.
		wait := bo.Next()
		deps.Logger.Info("reconnect backoff", "wait", wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		newConn, err := deps.Dialer.DialConn(ctx, deps.DatabaseURL)
		if err != nil {
			deps.Logger.Error("reconnect dial failed", "error", err)
			continue // backoff doubles on next loop iteration
		}
		bo.Reset()
		conn = newConn
		initial = false
	}
}

// runListenWithPollTicker runs the LISTEN loop and a poll ticker as siblings
// with shared cancellation: whichever returns first, the other is signalled
// to stop via the returned cancel and its error is returned. Channel-based
// fan-in keeps the signature simple; a full errgroup would be overkill for
// two cooperating goroutines whose failure modes are identical.
func runListenWithPollTicker(ctx context.Context, conn *pgx.Conn, deps Deps) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	listenDone := make(chan error, 1)
	pollDone := make(chan error, 1)

	go func() { listenDone <- listen(subCtx, conn, deps.Dispatcher, deps.Logger) }()
	go func() { pollDone <- runPollTicker(subCtx, deps) }()

	select {
	case err := <-listenDone:
		cancel()
		<-pollDone
		return err
	case err := <-pollDone:
		cancel()
		<-listenDone
		return err
	}
}

// runPollTicker fires pollOnce every cfg.PollInterval until ctx is cancelled.
// Errors from pollOnce are logged and the ticker continues — a single
// transient DB error should not bring the poller down. An error return from
// runPollTicker is reserved for fatal conditions (currently: none in M1).
func runPollTicker(ctx context.Context, deps Deps) error {
	t := time.NewTicker(deps.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := pollOnce(ctx, deps.Queries, deps.State, deps.Dispatcher); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				deps.Logger.Error("poll cycle failed", "error", err)
			}
		}
	}
}
