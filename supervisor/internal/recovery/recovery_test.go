package recovery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/recovery"
)

type stubQuerier struct {
	callCount int
	returnN   int64
	returnErr error
}

func (s *stubQuerier) RecoverStaleRunning(ctx context.Context) (int64, error) {
	s.callCount++
	return s.returnN, s.returnErr
}

func TestRunOnceCallsRecoverStaleRunningExactlyOnce(t *testing.T) {
	stub := &stubQuerier{returnN: 3}

	n, err := recovery.RunOnce(context.Background(), stub)
	if err != nil {
		t.Fatalf("RunOnce: unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("RunOnce returned %d, want 3", n)
	}
	if stub.callCount != 1 {
		t.Errorf("RecoverStaleRunning called %d times, want exactly 1", stub.callCount)
	}
}

func TestRunOncePropagatesQuerierError(t *testing.T) {
	want := errors.New("fake db failure")
	stub := &stubQuerier{returnErr: want}

	_, err := recovery.RunOnce(context.Background(), stub)
	if err == nil {
		t.Fatalf("RunOnce: want error, got nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("RunOnce err = %v, want wrapping %v", err, want)
	}
}
