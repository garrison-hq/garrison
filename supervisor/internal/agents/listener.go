// agents.changed cache invalidator (M4 / T014).
//
// The dashboard's editAgent server action emits
// pg_notify('agents.changed', role_slug) on every successful agents
// row write. The supervisor listens on a dedicated pgx.Conn (separate
// from the supervisor's main pool, per AGENTS.md §Concurrency rule 1
// and the M2.1 patterns for LISTEN connections). On each notification,
// Cache.Reset re-reads the active-agent set so the next spawn for the
// affected role picks up the new config. The startup-once cache
// invariant (FR-100) is preserved for the no-edits common case.

package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelName is the literal pg_notify channel the dashboard emits on.
// Centralised here so dashboard + supervisor agree by reference.
const ChannelName = "agents.changed"

const (
	initialBackoff = 100 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// StartChangeListener opens a dedicated pgx.Conn from the pool, issues
// LISTEN agents.changed, and runs a goroutine that drives Cache.Reset
// on every notification. The goroutine accepts the supervisor's root
// context per AGENTS.md §Concurrency rule 1; on ctx.Done() it closes
// the connection cleanly and returns nil.
//
// Returns an error from the initial LISTEN setup. Goroutine errors
// post-startup are logged via slog and recovered (the listener
// reconnects with backoff so a transient connection drop doesn't
// silently disable cache invalidation).
func StartChangeListener(ctx context.Context, pool *pgxpool.Pool, cache *Cache) error {
	if pool == nil {
		return errors.New("agents: StartChangeListener requires non-nil pool")
	}
	if cache == nil {
		return errors.New("agents: StartChangeListener requires non-nil cache")
	}

	// Acquire a dedicated connection for LISTEN (cannot share with
	// the pool's other consumers — LISTEN is connection-scoped).
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("agents: acquire LISTEN conn: %w", err)
	}

	if _, err := conn.Exec(ctx, `LISTEN "`+ChannelName+`"`); err != nil {
		conn.Release()
		return fmt.Errorf("agents: LISTEN %s: %w", ChannelName, err)
	}

	go runListenerLoop(ctx, conn, cache)

	return nil
}

// runListenerLoop is the goroutine body, extracted from
// StartChangeListener so the per-iteration handlers can compose
// without piling up into one cognitively-heavy function (linter
// S3776).
func runListenerLoop(ctx context.Context, conn *pgxpool.Conn, cache *Cache) {
	defer conn.Release()

	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			handleListenError(ctx, conn, err, backoff)
			if ctx.Err() != nil {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = initialBackoff
		applyNotification(ctx, cache, notification.Payload)
	}
}

// handleListenError logs the WaitForNotification error, sleeps
// for `backoff`, and attempts a re-LISTEN. Returns when the sleep
// finishes or the context is cancelled.
func handleListenError(ctx context.Context, conn *pgxpool.Conn, err error, backoff time.Duration) {
	slog.WarnContext(ctx, "agents.changed: WaitForNotification error; reconnecting",
		slog.String("err", err.Error()),
		slog.Duration("backoff", backoff),
	)
	select {
	case <-ctx.Done():
		return
	case <-time.After(backoff):
	}
	if _, relistenErr := conn.Exec(ctx, `LISTEN "`+ChannelName+`"`); relistenErr != nil {
		slog.WarnContext(ctx, "agents.changed: re-LISTEN failed",
			slog.String("err", relistenErr.Error()),
		)
	}
}

// applyNotification calls Cache.Reset and logs the outcome.
func applyNotification(ctx context.Context, cache *Cache, roleSlug string) {
	if err := cache.Reset(ctx, roleSlug); err != nil {
		slog.WarnContext(ctx, "agents.changed: cache reset failed",
			slog.String("role_slug", roleSlug),
			slog.String("err", err.Error()),
		)
		return
	}
	slog.InfoContext(ctx, "agents.changed: cache reset complete",
		slog.String("role_slug", roleSlug),
	)
}

// pgx.Conn type alias for tests that want to construct a fake.
// (The listener uses *pgxpool.Pool in production.)
var _ = pgx.ErrNoRows
