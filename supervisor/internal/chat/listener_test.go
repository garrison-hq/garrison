package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSleepCtxReturnsNilWhenDurationElapses pins the happy path of
// sleepCtx — the helper used by RunListener's reconnect backoff loop.
// 5ms is well under any test timeout but long enough that the timer
// is the path that fires (not the ctx-cancel path).
func TestSleepCtxReturnsNilWhenDurationElapses(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	if err := sleepCtx(ctx, 5*time.Millisecond); err != nil {
		t.Errorf("sleepCtx returned err=%v; want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Millisecond {
		t.Errorf("sleepCtx returned in %v; want ≥ ~5ms", elapsed)
	}
}

// TestSleepCtxReturnsImmediatelyOnCancelledCtx pins the reconnect-
// loop's shutdown path — when the supervisor's gctx is already
// cancelled, the listener must not waste a backoff window before
// returning. Pre-cancel the ctx, pass a long duration, assert the
// helper returns within the cancel path, not the timer path.
func TestSleepCtxReturnsImmediatelyOnCancelledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := sleepCtx(ctx, 10*time.Second)
	if err == nil {
		t.Fatal("sleepCtx returned nil err; want ctx.Err()")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx err=%v; want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("sleepCtx waited %v on cancelled ctx; want ≤ ~100ms (timer path fired instead of cancel path)", elapsed)
	}
}

// TestSleepCtxZeroDurationFastPath: callers occasionally pass a
// zero/negative duration (e.g. backoff math underflow). The helper
// short-circuits without scheduling a timer — useful both for speed
// and to avoid time.NewTimer's "<= 0 d" panic-free-but-undefined
// behaviour.
func TestSleepCtxZeroDurationFastPath(t *testing.T) {
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Errorf("sleepCtx(_, 0) = %v; want nil", err)
	}
	if err := sleepCtx(context.Background(), -1*time.Second); err != nil {
		t.Errorf("sleepCtx(_, -1s) = %v; want nil", err)
	}
}

// TestRunListenerRejectsNilPool: the function-level guard catches
// misconfigured callers (cmd/supervisor never reaches this path
// because main.go panics at config-load if pool is nil, but the
// chat package's public API remains defensive). Without this guard,
// runChatListenCycle would attempt Pool.Acquire on a nil receiver
// and segfault inside the reconnect loop.
func TestRunListenerRejectsNilPool(t *testing.T) {
	err := RunListener(context.Background(), Deps{}, &Worker{})
	if err == nil {
		t.Fatal("RunListener with nil Pool returned nil err; want guard error")
	}
	if !strings.Contains(err.Error(), "nil pool") {
		t.Errorf("err=%v; want a nil-pool sentinel error", err)
	}
}
