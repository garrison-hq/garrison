package ingress

import (
	"sync"
	"time"
)

// nowFunc is the clock seam. Production uses time.Now; tests inject a
// deterministic function to control bucket refill without sleeping.
type nowFunc func() time.Time

// bucket is one connector's token state. All fields are protected by
// the parent RateCap mutex.
type bucket struct {
	tokens      float64   // current available tokens (may be fractional during refill)
	lastRefill  time.Time // wall-clock time of the last Allow call (refill point)
	capacity    float64   // maximum tokens (= Burst configured at construction)
	refillPerNs float64   // tokens per nanosecond derived from RatePerMin
}

// RateCap is an in-process, per-connector token bucket. No goroutine or
// ticker: refill is computed lazily at each Allow call from elapsed
// wall-clock time (plan.md decision 8, ratecap design; FR-600/FR-602).
//
// Construct with NewRateCap; the zero value is not valid.
type RateCap struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     nowFunc
}

// NewRateCap returns a ready-to-use RateCap.
//
// ratePerMin is the sustained refill rate (tokens per minute). burst is
// the maximum instantaneous allowance (bucket capacity). now is the clock
// seam: pass nil to use time.Now (production), or a deterministic function
// for tests (TestRateCap_RefillsOverTime).
func NewRateCap(now nowFunc) *RateCap {
	if now == nil {
		now = time.Now
	}
	return &RateCap{
		buckets: make(map[string]*bucket),
		now:     now,
	}
}

// AddConnector registers a connector's rate parameters. Must be called
// before any Allow call for that connectorID. Calling it again for the
// same ID re-initialises the bucket (not safe to call concurrently with
// Allow on the same ID).
func (rc *RateCap) AddConnector(connectorID string, ratePerMin, burst int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.buckets[connectorID] = &bucket{
		tokens:      float64(burst), // start full
		lastRefill:  rc.now(),
		capacity:    float64(burst),
		refillPerNs: float64(ratePerMin) / (60 * 1e9),
	}
}

// Allow consumes one token from connectorID's bucket after refilling for
// elapsed wall-clock time. Returns true when a token was available (the
// delivery is within the rate cap). Returns false when the bucket is empty
// (the delivery is over-cap; the handler should return 429 and call
// throttle.FireIngressRateCap — FR-600, FR-602).
//
// If connectorID is not registered, Allow panics — callers must
// AddConnector before serving deliveries.
func (rc *RateCap) Allow(connectorID string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	b, ok := rc.buckets[connectorID]
	if !ok {
		panic("ingress: RateCap.Allow called for unregistered connector: " + connectorID)
	}

	now := rc.now()
	elapsed := now.Sub(b.lastRefill)
	b.lastRefill = now

	// Refill proportional to elapsed time, capped at capacity.
	if elapsed > 0 {
		b.tokens += float64(elapsed.Nanoseconds()) * b.refillPerNs
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
