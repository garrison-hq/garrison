package events

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/pgdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
)

// fakeRedialDialer is a minimal pgdb.Dialer that lets redialUntilSuccess
// be exercised without standing up a real Postgres. The DialConn method
// fails the first failUntil calls, then returns a non-nil sentinel
// *pgx.Conn so the redial loop terminates. The pool side is unused by
// redialUntilSuccess and panics if called — failure caught loud.
type fakeRedialDialer struct {
	failUntil int
	calls     atomic.Int32
	conn      *pgx.Conn
}

func (f *fakeRedialDialer) DialPool(_ context.Context, _ string) (*pgxpool.Pool, error) {
	panic("DialPool should not be called by redialUntilSuccess")
}

func (f *fakeRedialDialer) DialConn(_ context.Context, _ string) (*pgx.Conn, error) {
	n := int(f.calls.Add(1))
	if n <= f.failUntil {
		return nil, errors.New("simulated dial failure")
	}
	return f.conn, nil
}

func TestRedialUntilSuccessReturnsConnAfterRetries(t *testing.T) {
	// Sentinel non-nil *pgx.Conn so the helper has something to return.
	// We never call any methods on it; nil sentinel would defeat the
	// "must be non-nil unless ctx is done" contract redialUntilSuccess
	// promises.
	sentinel := &pgx.Conn{}

	fd := &fakeRedialDialer{failUntil: 2, conn: sentinel}
	deps := Deps{
		Dialer:      fd,
		DatabaseURL: "postgres://unused",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	bo := pgdb.Backoff{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := redialUntilSuccess(ctx, deps, &bo)
	if got != sentinel {
		t.Fatalf("redialUntilSuccess: want sentinel conn back, got %v", got)
	}
	if c := fd.calls.Load(); c != 3 {
		t.Errorf("expected 3 dial attempts (2 failures + 1 success), got %d", c)
	}
}

func TestRedialUntilSuccessReturnsNilOnCtxCancel(t *testing.T) {
	// Dialer that always fails — only ctx cancellation can break the loop.
	fd := &fakeRedialDialer{failUntil: 1 << 30, conn: nil}
	deps := Deps{
		Dialer:      fd,
		DatabaseURL: "postgres://unused",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	bo := pgdb.Backoff{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got := redialUntilSuccess(ctx, deps, &bo)
	if got != nil {
		t.Fatalf("redialUntilSuccess: want nil on ctx cancel, got %v", got)
	}
}

func TestRedialUntilSuccessImmediateSuccess(t *testing.T) {
	// No failures — first dial succeeds. Smoke-tests the happy-path
	// exit and confirms the backoff is consulted exactly once.
	sentinel := &pgx.Conn{}
	fd := &fakeRedialDialer{failUntil: 0, conn: sentinel}
	deps := Deps{
		Dialer:      fd,
		DatabaseURL: "postgres://unused",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	bo := pgdb.Backoff{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got := redialUntilSuccess(ctx, deps, &bo)
	if got != sentinel {
		t.Fatalf("redialUntilSuccess: want sentinel conn, got %v", got)
	}
	if c := fd.calls.Load(); c != 1 {
		t.Errorf("expected 1 dial attempt, got %d", c)
	}
}
