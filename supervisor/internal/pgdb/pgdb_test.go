package pgdb_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/pgdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackoffStartsAtInitial(t *testing.T) {
	var bo pgdb.Backoff
	if got := bo.Next(); got != pgdb.InitialBackoff {
		t.Errorf("first Next() = %s, want %s", got, pgdb.InitialBackoff)
	}
}

func TestBackoffMonotonicAndDoubles(t *testing.T) {
	var bo pgdb.Backoff
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
	}
	var prev time.Duration
	for i, w := range want {
		got := bo.Next()
		if got != w {
			t.Errorf("Next() iter %d = %s, want %s", i, got, w)
		}
		if got < prev {
			t.Errorf("Next() iter %d regressed: %s < %s", i, got, prev)
		}
		prev = got
	}
}

func TestBackoffCapsAtMax(t *testing.T) {
	var bo pgdb.Backoff
	var last time.Duration
	for i := 0; i < 30; i++ {
		last = bo.Next()
	}
	if last != pgdb.MaxBackoff {
		t.Errorf("after 30 Next() calls, last = %s, want cap %s", last, pgdb.MaxBackoff)
	}
	for i := 0; i < 5; i++ {
		if got := bo.Next(); got != pgdb.MaxBackoff {
			t.Errorf("saturation iter %d = %s, want %s", i, got, pgdb.MaxBackoff)
		}
	}
}

func TestBackoffResetReturnsToInitial(t *testing.T) {
	var bo pgdb.Backoff
	bo.Next()
	bo.Next()
	bo.Next()
	bo.Reset()
	if got := bo.Next(); got != pgdb.InitialBackoff {
		t.Errorf("after Reset, Next() = %s, want %s", got, pgdb.InitialBackoff)
	}
}

// fakeDialer implements pgdb.Dialer without touching Postgres. It fails the
// first failUntil pool-dial attempts, then succeeds on subsequent attempts
// returning nil pool/conn — the tests in this file do not dereference them.
type fakeDialer struct {
	failUntil     int
	dialPoolCalls int
	dialConnCalls int
}

func (f *fakeDialer) DialPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	f.dialPoolCalls++
	if f.dialPoolCalls <= f.failUntil {
		return nil, errors.New("fake: pool dial failed")
	}
	return nil, nil
}

func (f *fakeDialer) DialConn(ctx context.Context, url string) (*pgx.Conn, error) {
	f.dialConnCalls++
	return nil, nil
}

func TestConnectRetriesWithBackoff(t *testing.T) {
	fd := &fakeDialer{failUntil: 3}
	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d) }
	cfg := &config.Config{DatabaseURL: "postgres://fake"}

	_, _, err := pgdb.ConnectWith(context.Background(), cfg, fd, sleep)
	if err != nil {
		t.Fatalf("ConnectWith: unexpected error: %v", err)
	}
	if fd.dialPoolCalls != 4 {
		t.Errorf("dialPoolCalls = %d, want 4 (3 fails + 1 success)", fd.dialPoolCalls)
	}
	if fd.dialConnCalls != 1 {
		t.Errorf("dialConnCalls = %d, want 1", fd.dialConnCalls)
	}
	wantSleeps := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
	}
	if len(sleeps) != len(wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
	}
	for i, w := range wantSleeps {
		if sleeps[i] != w {
			t.Errorf("sleeps[%d] = %s, want %s", i, sleeps[i], w)
		}
	}
}

func TestConnectResetsBackoffAcrossInvocations(t *testing.T) {
	// FR-017 semantics: each top-level Connect invocation starts the backoff
	// from InitialBackoff. This is trivially true because ConnectWith builds a
	// fresh Backoff each call, but pinning it in a test guards against a
	// future refactor that leaks state across calls.
	cfg := &config.Config{DatabaseURL: "postgres://fake"}

	var sleepsRun1 []time.Duration
	fd1 := &fakeDialer{failUntil: 2}
	_, _, err := pgdb.ConnectWith(context.Background(), cfg, fd1,
		func(d time.Duration) { sleepsRun1 = append(sleepsRun1, d) })
	if err != nil {
		t.Fatalf("run1: unexpected error: %v", err)
	}

	var sleepsRun2 []time.Duration
	fd2 := &fakeDialer{failUntil: 2}
	_, _, err = pgdb.ConnectWith(context.Background(), cfg, fd2,
		func(d time.Duration) { sleepsRun2 = append(sleepsRun2, d) })
	if err != nil {
		t.Fatalf("run2: unexpected error: %v", err)
	}

	if len(sleepsRun1) == 0 || sleepsRun1[0] != pgdb.InitialBackoff {
		t.Errorf("run1[0] = %v, want %s", sleepsRun1, pgdb.InitialBackoff)
	}
	if len(sleepsRun2) == 0 || sleepsRun2[0] != pgdb.InitialBackoff {
		t.Errorf("run2[0] = %v, want %s (backoff should reset across Connect calls)", sleepsRun2, pgdb.InitialBackoff)
	}
}

func TestConnectRespectsCtxCancel(t *testing.T) {
	fd := &fakeDialer{failUntil: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	sleep := func(d time.Duration) {
		attempts++
		if attempts >= 2 {
			cancel()
		}
	}
	cfg := &config.Config{DatabaseURL: "postgres://fake"}

	_, _, err := pgdb.ConnectWith(ctx, cfg, fd, sleep)
	if err == nil {
		t.Fatalf("ConnectWith: want error on ctx cancel, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want wrapping context.Canceled", err)
	}
}
