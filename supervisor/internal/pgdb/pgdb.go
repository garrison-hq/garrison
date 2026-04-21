// Package pgdb owns Postgres connectivity: a shared *pgxpool.Pool for general
// queries and a dedicated *pgx.Conn for LISTEN, plus the FR-018 advisory lock
// primitive in advisory.go. It does not own LISTEN semantics or pool queries;
// only the connection primitives.
package pgdb

import (
	"context"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InitialBackoff and MaxBackoff bound the FR-017 initial-connect and NFR-002
// reconnect exponential backoff: 100ms doubling up to a 30s cap.
const (
	InitialBackoff = 100 * time.Millisecond
	MaxBackoff     = 30 * time.Second
)

// Dialer is the seam between pgdb and pgx so unit tests can substitute a fake
// that records call counts without needing a real Postgres. Production code
// uses realDialer.
type Dialer interface {
	DialPool(ctx context.Context, url string) (*pgxpool.Pool, error)
	DialConn(ctx context.Context, url string) (*pgx.Conn, error)
}

type realDialer struct{}

func (realDialer) DialPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, url)
}

func (realDialer) DialConn(ctx context.Context, url string) (*pgx.Conn, error) {
	return pgx.Connect(ctx, url)
}

// Connect establishes the shared pool and the dedicated LISTEN connection,
// retrying with the FR-017 100ms→30s exponential backoff until success or ctx
// cancellation. The returned *pgx.Conn is the connection on which callers will
// later LISTEN and hold the advisory lock.
func Connect(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, *pgx.Conn, error) {
	sleep := func(d time.Duration) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	return ConnectWith(ctx, cfg, realDialer{}, sleep)
}

// ConnectWith is the dependency-injection form of Connect. sleep is called
// between retries with the computed backoff duration; production passes an
// interruptible time.Timer-based sleep, tests pass a recording no-op.
func ConnectWith(ctx context.Context, cfg *config.Config, d Dialer, sleep func(time.Duration)) (*pgxpool.Pool, *pgx.Conn, error) {
	var bo Backoff
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("pgdb: connect cancelled: %w", err)
		}
		pool, poolErr := d.DialPool(ctx, cfg.DatabaseURL)
		if poolErr == nil {
			conn, connErr := d.DialConn(ctx, cfg.DatabaseURL)
			if connErr == nil {
				return pool, conn, nil
			}
			if pool != nil {
				pool.Close()
			}
		}
		sleep(bo.Next())
	}
}

// Backoff is a zero-value-usable exponential backoff calculator for FR-017 /
// NFR-002. First Next() returns InitialBackoff; subsequent calls double until
// they saturate at MaxBackoff. Reset returns to the zero state.
type Backoff struct {
	current time.Duration
}

// Next returns the next wait duration and advances internal state.
func (b *Backoff) Next() time.Duration {
	if b.current == 0 {
		b.current = InitialBackoff
		return b.current
	}
	b.current *= 2
	if b.current > MaxBackoff {
		b.current = MaxBackoff
	}
	return b.current
}

// Reset returns the backoff to its zero state so the next Next() call yields
// InitialBackoff again. Reconnect logic calls this after each successful
// LISTEN lifecycle so the next outage starts at the floor.
func (b *Backoff) Reset() {
	b.current = 0
}
