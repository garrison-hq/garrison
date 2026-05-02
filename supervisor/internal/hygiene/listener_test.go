package hygiene

import (
	"context"
	"errors"
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
// TestResolveKGFactsForEvaluator_NilCallbackReturnsNilNil — the M6
// T012 listener wiring leaves the missing_kg_facts predicate
// unsignalled when the operator hasn't wired KGFactsQuery (production
// fallthrough on the M2.x code path). The evaluator's nil-check then
// skips the predicate.
func TestResolveKGFactsForEvaluator_NilCallbackReturnsNilNil(t *testing.T) {
	deps := Deps{KGFactsQuery: nil}
	facts, err := resolveKGFactsForEvaluator(context.Background(), deps, "ticket_x")
	if err != nil {
		t.Fatalf("expected nil err; got %v", err)
	}
	if facts != nil {
		t.Errorf("expected nil facts slice; got %v", facts)
	}
}

// TestResolveKGFactsForEvaluator_PassesTicketIDToCallback — wired
// callback receives the same ticket id text the listener uses for
// the palace-side query.
func TestResolveKGFactsForEvaluator_PassesTicketIDToCallback(t *testing.T) {
	var received string
	stub := []PalaceTriple{{Subject: "ticket_y", Predicate: "p", Object: "o"}}
	deps := Deps{
		KGFactsQuery: func(_ context.Context, ticketID string) ([]PalaceTriple, error) {
			received = ticketID
			return stub, nil
		},
	}
	got, err := resolveKGFactsForEvaluator(context.Background(), deps, "ticket_y")
	if err != nil {
		t.Fatalf("expected nil err; got %v", err)
	}
	if received != "ticket_y" {
		t.Errorf("callback received ticket id = %q; want ticket_y", received)
	}
	if len(got) != 1 || got[0].Subject != "ticket_y" {
		t.Errorf("returned facts = %v; want stub passthrough", got)
	}
}

// TestResolveKGFactsForEvaluator_PropagatesCallbackError — sidecar
// failure surfaces via the err return so the evaluator's
// missing_kg_facts predicate skips per the soft-gates posture
// (Constitution IV).
func TestResolveKGFactsForEvaluator_PropagatesCallbackError(t *testing.T) {
	deps := Deps{
		KGFactsQuery: func(_ context.Context, _ string) ([]PalaceTriple, error) {
			return nil, errors.New("sidecar boom")
		},
	}
	_, err := resolveKGFactsForEvaluator(context.Background(), deps, "ticket_x")
	if err == nil {
		t.Fatal("expected error propagation; got nil")
	}
}

// TestBuildEvaluationInputCarriesAllFields pins the evaluator-input
// composition the M6 listener wiring builds. Each field round-trips
// through buildEvaluationInput unchanged.
func TestBuildEvaluationInputCarriesAllFields(t *testing.T) {
	now := time.Now()
	args := buildEvaluationInputArgs{
		TicketIDText: "ticket_z",
		WindowStart:  now.Add(-1 * time.Hour),
		WindowEnd:    now,
		PalaceWing:   "wing_eng",
		Drawers:      []PalaceDrawer{{Wing: "wing_eng", Body: "x"}},
		KGTriples:    []PalaceTriple{{Subject: "a"}},
		PalaceErr:    errors.New("x"),
		Threshold:    200,
		KGFacts:      []PalaceTriple{{Subject: "b"}},
		KGFactsErr:   errors.New("y"),
	}
	in := buildEvaluationInput(args)
	if in.TicketIDText != "ticket_z" || in.PalaceWing != "wing_eng" {
		t.Errorf("fields not threaded: %+v", in)
	}
	if in.ThinDiaryThreshold != 200 {
		t.Errorf("threshold not threaded; got %d", in.ThinDiaryThreshold)
	}
	if len(in.KGFactsForTicket) != 1 || in.KGFactsForTicket[0].Subject != "b" {
		t.Errorf("KGFactsForTicket not threaded: %+v", in.KGFactsForTicket)
	}
	if in.KGFactsForTicketErr == nil {
		t.Error("KGFactsForTicketErr not threaded")
	}
	if in.PalaceErr == nil {
		t.Error("PalaceErr not threaded")
	}
	if !in.RunWindowStart.Equal(args.WindowStart) || !in.RunWindowEnd.Equal(args.WindowEnd) {
		t.Errorf("run window not threaded: got [%v, %v]; want [%v, %v]",
			in.RunWindowStart, in.RunWindowEnd, args.WindowStart, args.WindowEnd)
	}
}

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
