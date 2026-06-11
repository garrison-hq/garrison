package schedule

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestRunLoopReturnsNilOnContextCancel pins the errgroup contract: a
// graceful shutdown (root context cancelled) exits the loop with nil,
// not an error that would poison sibling subsystems. Deps.Pool stays
// nil on purpose — the cancelled context must win the select before
// any tick touches the database.
func TestRunLoopReturnsNilOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunLoop(ctx, Deps{
			TickInterval: time.Hour,
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLoop returned %v on context cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunLoop did not return within 5s of context cancellation")
	}
}

// TestEffectiveClaimLimitDefaults pins the ClaimLimit default of 20
// (plan §1) and the pass-through for explicit positive values.
func TestEffectiveClaimLimitDefaults(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int32
	}{
		{name: "zero defaults to 20", limit: 0, want: 20},
		{name: "negative defaults to 20", limit: -3, want: 20},
		{name: "explicit value passes through", limit: 5, want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveClaimLimit(Deps{ClaimLimit: tc.limit}); got != tc.want {
				t.Fatalf("effectiveClaimLimit(%d) = %d, want %d", tc.limit, got, tc.want)
			}
		})
	}
}

// TestOverlapDetailPerMode pins the human-readable skip reasons
// recorded on skipped_overlap run rows for each mode.
func TestOverlapDetailPerMode(t *testing.T) {
	if d := overlapDetail(ModeTicket); d != "previously fired ticket still open; slot skipped (FR-202)" {
		t.Fatalf("ticket overlap detail = %q", d)
	}
	if d := overlapDetail(ModeOneshot); d != "previous oneshot firing still in flight; slot skipped (FR-303)" {
		t.Fatalf("oneshot overlap detail = %q", d)
	}
}
