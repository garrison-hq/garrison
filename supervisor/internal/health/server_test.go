package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/health"
)

type stubPinger struct {
	err error
}

func (s stubPinger) Ping(_ context.Context) error { return s.err }

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }

const pollInterval = 5 * time.Second

func TestHandler200WhenPingOKAndPollFresh(t *testing.T) {
	state := health.NewState()
	clock := fixedClock{now: time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)}
	// LastPollAt well inside the 2*pollInterval = 10s freshness window.
	state.RecordPoll(clock.now.Add(-3 * time.Second))

	h := health.NewHandler(state, stubPinger{err: nil}, pollInterval, clock)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHandler503WhenPingFails(t *testing.T) {
	state := health.NewState()
	clock := fixedClock{now: time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)}
	state.RecordPoll(clock.now.Add(-1 * time.Second)) // poll is fresh

	h := health.NewHandler(state, stubPinger{err: errors.New("db down")}, pollInterval, clock)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandler503WhenPollStale(t *testing.T) {
	state := health.NewState()
	clock := fixedClock{now: time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)}
	// 11s since last poll > 2 * 5s = 10s threshold.
	state.RecordPoll(clock.now.Add(-11 * time.Second))

	h := health.NewHandler(state, stubPinger{err: nil}, pollInterval, clock)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (poll stale)", rec.Code)
	}
}

func TestHandler503BeforeFirstPoll(t *testing.T) {
	// No RecordPoll call → LastPollAt is the zero time, which must fail
	// the freshness test even if the ping succeeds.
	state := health.NewState()
	clock := fixedClock{now: time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)}

	h := health.NewHandler(state, stubPinger{err: nil}, pollInterval, clock)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (no poll yet)", rec.Code)
	}
}
