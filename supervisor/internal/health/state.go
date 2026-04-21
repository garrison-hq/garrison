// Package health owns the HTTP /health endpoint, the 500ms-timeout SELECT 1
// ping, and the shared State struct that the fallback poller updates after
// each successful poll. It does not own Postgres connectivity (pgdb), the
// LISTEN loop (events), or any other runtime wiring.
package health

import (
	"sync"
	"time"
)

// State is the mutable readiness snapshot shared between the fallback poller
// (which writes LastPollAt) and the /health handler (which reads it alongside
// a synchronous SELECT 1). Safe for concurrent use via the embedded mutex.
// Exposed as a pointer so a single instance is threaded through all
// subsystems at process start; creating multiple instances is a wiring bug.
type State struct {
	mu         sync.RWMutex
	lastPollAt time.Time
	lastPingAt time.Time
	lastPingOK bool
}

// NewState returns a zero-valued State. LastPollAt is the Go zero time until
// the poller first records a cycle, which makes /health return 503 correctly
// during startup before the initial poll completes.
func NewState() *State {
	return &State{}
}

// RecordPoll implements events.StateWriter. Called by the fallback poller
// after every successful SelectUnprocessedEvents so the handler can answer
// "has the pipeline made forward progress recently?".
func (s *State) RecordPoll(ts time.Time) {
	s.mu.Lock()
	s.lastPollAt = ts
	s.mu.Unlock()
}

// LastPollAt is the timestamp of the most recent successful poll. The zero
// time means "no poll has completed yet" and is the state /health observes
// during the brief window between process start and the first poll cycle.
func (s *State) LastPollAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastPollAt
}

// RecordPing updates the cached SELECT 1 result and timestamp. Called by the
// /health handler on every request (there is no background pinger in M1);
// caching the result is only for observability/logging, not for serving
// stale data — the handler decides ok/503 from the ping it just ran.
func (s *State) RecordPing(ts time.Time, ok bool) {
	s.mu.Lock()
	s.lastPingAt = ts
	s.lastPingOK = ok
	s.mu.Unlock()
}

// LastPing returns the most recent (timestamp, ok) recorded by RecordPing.
// Zero time means no ping has been issued yet.
func (s *State) LastPing() (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastPingAt, s.lastPingOK
}
