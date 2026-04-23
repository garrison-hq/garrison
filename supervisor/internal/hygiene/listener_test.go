package hygiene

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestSleepCtxReturnsOnCancel — internal helper respects ctx.Done().
func TestSleepCtxReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sleepCtx(ctx, 5*time.Second)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ctx.Err() on cancel, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sleepCtx did not return within 500ms of cancel")
	}
}

// TestSleepCtxReturnsOnElapsed — timer path fires.
func TestSleepCtxReturnsOnElapsed(t *testing.T) {
	start := time.Now()
	if err := sleepCtx(context.Background(), 30*time.Millisecond); err != nil {
		t.Fatalf("sleepCtx returned err: %v", err)
	}
	if d := time.Since(start); d < 25*time.Millisecond {
		t.Fatalf("sleepCtx returned too early: %s", d)
	}
}

// TestUUIDText — 8-4-4-4-12 hex form.
func TestUUIDText(t *testing.T) {
	u := pgtype.UUID{Valid: true}
	for i := range u.Bytes {
		u.Bytes[i] = byte(i)
	}
	got := uuidText(u)
	want := "00010203-0405-0607-0809-0a0b0c0d0e0f"
	if got != want {
		t.Errorf("uuidText=%q; want %q", got, want)
	}
	if uuidText(pgtype.UUID{}) != "" {
		t.Errorf("invalid UUID should return empty string")
	}
}

// TestEvaluatePurePipelineOnPalaceError — composition check. The pure
// Evaluate rule returns Pending when PalaceErr ≠ nil; this is what the
// listener path relies on to produce at-most-once-to-terminal behavior
// when the palace is unreachable.
func TestEvaluatePurePipelineOnPalaceError(t *testing.T) {
	got := Evaluate(EvaluationInput{
		TicketIDText:   "ticket_x",
		RunWindowStart: time.Now().Add(-1 * time.Hour),
		RunWindowEnd:   time.Now(),
		PalaceWing:     "wing_frontend_engineer",
		PalaceErr:      io.ErrUnexpectedEOF,
	})
	if got != StatusPending {
		t.Fatalf("Evaluate returned %s on palace error; want %s", got, StatusPending)
	}
}

// TestRunSweepRespectsShutdown — ctx cancellation makes the sweep
// goroutine exit promptly. Queries/Palace are nil (never consulted
// because cancel fires before the first tick).
func TestRunSweepRespectsShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	deps := Deps{
		SweepInterval:      100 * time.Millisecond,
		Delay:              5 * time.Second,
		TerminalWriteGrace: time.Second,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	var wg sync.WaitGroup
	var retErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		retErr = RunSweep(ctx, deps)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		if retErr == nil {
			t.Fatal("RunSweep returned nil; expected ctx.Err()")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunSweep did not exit within 500ms of cancel")
	}
}

// TestIntervalValue — microsecond math.
func TestIntervalValue(t *testing.T) {
	got := intervalValue(5 * time.Second)
	if !got.Valid {
		t.Fatal("expected Valid=true")
	}
	if got.Microseconds != 5_000_000 {
		t.Errorf("got %d microseconds; want 5_000_000", got.Microseconds)
	}
	if intervalValue(0).Microseconds != 1_000_000 {
		t.Errorf("zero duration should floor to 1s")
	}
}

// NOTE: RunListener's end-to-end behaviour (LISTEN reconnection backoff,
// notification dispatch, at-most-once discipline under duplicate payloads)
// is covered in T017/T018 integration tests against a real Postgres. Unit-
// testing RunListener here would require mocking pgdb.Dialer → *pgx.Conn
// which pgx's concrete types don't expose a clean seam for. The
// integration tests are the right layer.
