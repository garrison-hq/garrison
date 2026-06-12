package ingress

import (
	"testing"
	"time"
)

// TestRateCap_AllowsWithinBurst verifies that the first Burst calls to
// Allow all return true — the bucket starts full (FR-600).
func TestRateCap_AllowsWithinBurst(t *testing.T) {
	const burst = 5
	rc := NewRateCap(nil) // production clock; bucket doesn't refill in this window
	rc.AddConnector("test-conn", 60, burst)

	for i := 0; i < burst; i++ {
		if !rc.Allow("test-conn") {
			t.Errorf("Allow call %d returned false; want true (within burst of %d)", i+1, burst)
		}
	}
}

// TestRateCap_RejectsOverBurst verifies that the Burst+1th call within
// the same instant returns false (bucket is empty, FR-602).
func TestRateCap_RejectsOverBurst(t *testing.T) {
	const burst = 3
	// Pin the clock so time never advances → zero refill between calls.
	frozen := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	rc := NewRateCap(func() time.Time { return frozen })
	rc.AddConnector("test-conn", 60, burst)

	for i := 0; i < burst; i++ {
		rc.Allow("test-conn") //nolint:errcheck — drain without asserting
	}
	if rc.Allow("test-conn") {
		t.Errorf("Allow call %d returned true; want false (over burst of %d)", burst+1, burst)
	}
}

// TestRateCap_RefillsOverTime verifies that after injecting elapsed time
// equal to one full refill interval the bucket accepts new tokens
// (deterministic-clock seam, plan decision 8).
func TestRateCap_RefillsOverTime(t *testing.T) {
	const ratePerMin = 60 // one token per second
	const burst = 2

	base := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	tick := base
	rc := NewRateCap(func() time.Time { return tick })
	rc.AddConnector("test-conn", ratePerMin, burst)

	// Drain the bucket completely.
	for i := 0; i < burst; i++ {
		if !rc.Allow("test-conn") {
			t.Fatalf("drain call %d should have been allowed", i+1)
		}
	}
	if rc.Allow("test-conn") {
		t.Fatal("bucket should be empty after draining burst tokens")
	}

	// Advance clock by one full second → one token refilled at 60/min rate.
	tick = tick.Add(time.Second)

	if !rc.Allow("test-conn") {
		t.Error("Allow returned false after advancing clock by one refill interval; want true")
	}
}

// TestRateCap_PerConnectorIsolation verifies that two connector IDs have
// independent buckets — exhausting one does not affect the other.
func TestRateCap_PerConnectorIsolation(t *testing.T) {
	const burst = 2
	frozen := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	rc := NewRateCap(func() time.Time { return frozen })
	rc.AddConnector("conn-a", 60, burst)
	rc.AddConnector("conn-b", 60, burst)

	// Drain conn-a completely.
	for i := 0; i < burst; i++ {
		rc.Allow("conn-a") //nolint:errcheck
	}

	// conn-a is empty.
	if rc.Allow("conn-a") {
		t.Error("conn-a should be exhausted; got true")
	}
	// conn-b must be unaffected.
	if !rc.Allow("conn-b") {
		t.Error("conn-b should be independent of conn-a; got false")
	}
}
