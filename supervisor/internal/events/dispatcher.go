// Package events owns the per-channel event dispatcher, the LISTEN loop on a
// dedicated *pgx.Conn, the fallback poller against the shared pool, and the
// reconnect state machine that sequences the two. It does not own subprocess
// execution (internal/spawn) or HTTP health (internal/health); it is the
// routing and I/O layer between pg_notify and those consumers.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"
)

// Handler is the per-channel callback the dispatcher invokes once it has
// parsed a notification payload down to an event_id UUID. Handlers are
// expected to be idempotent because the same event_id may arrive via both
// LISTEN and the fallback poll (plan.md §"Dedupe on handling").
type Handler func(ctx context.Context, eventID pgtype.UUID) error

// ErrUnknownChannel is returned by Dispatch when a notification arrives on a
// channel the dispatcher has no handler for. FR-014 requires this to be a
// loud startup/runtime error rather than a silent drop.
var ErrUnknownChannel = errors.New("events: no handler registered for channel")

// Dispatcher routes channels to handlers. The base table is built once
// at process startup and frozen (FR-014); on top of it sits a dynamic
// overlay for roster-derived routes (FR-014 amendment, 2026-06-10):
// M7 hires must start dispatching when approved, not at the next
// restart, so cmd/supervisor swaps the overlay on every agents.changed
// cache reset via SetDynamicRoutes. Base routes win on conflict, so
// the M2.2 engineering channels and the M8 worker channel can never be
// shadowed by a roster row.
//
// New overlay channels are picked up by the fallback poll immediately
// (pollOnce routes through this table every PollInterval) and by
// LISTEN at the next reconnect cycle — worst-case notify latency for a
// fresh hire is one poll interval, not a restart.
//
// inFlight dedupes concurrent dispatches of the same event_id across the
// LISTEN and poll paths. The FR-006 SELECT ... FOR UPDATE check in spawn
// only catches the completed-event case (processed_at set); it does not
// prevent two concurrent handlers from both seeing processed_at=NULL
// during the subprocess window. In-memory dedupe is sound for M1 because
// FR-018 guarantees exactly one supervisor holds the advisory lock.
type Dispatcher struct {
	handlers map[string]Handler
	inFlight sync.Map

	mu      sync.RWMutex
	dynamic map[string]Handler
}

// NewDispatcher returns a Dispatcher with the supplied route table frozen in.
// A nil handlers map is tolerated and yields a dispatcher that rejects every
// channel; simpler than a separate zero-value constructor.
func NewDispatcher(handlers map[string]Handler) *Dispatcher {
	copied := make(map[string]Handler, len(handlers))
	for k, v := range handlers {
		copied[k] = v
	}
	return &Dispatcher{handlers: copied}
}

// Dispatch parses payload as the pg_notify envelope defined in
// data-model.md §"Payloads" (`{"event_id": "<uuid>"}`), looks up the handler
// by channel, and invokes it. Errors:
//
//   - ErrUnknownChannel when channel is unregistered.
//   - A wrapped json error when payload is not valid JSON or missing event_id.
//   - Whatever the handler returns, wrapped with the channel for context.
//
// Dispatch is safe for concurrent use because handlers is frozen at
// construction and no other state is mutated.
func (d *Dispatcher) Dispatch(ctx context.Context, channel, payload string) error {
	h, ok := d.handlers[channel]
	if !ok {
		d.mu.RLock()
		h, ok = d.dynamic[channel]
		d.mu.RUnlock()
	}
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownChannel, channel)
	}
	var env struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return fmt.Errorf("events: malformed payload on channel %q: %w", channel, err)
	}
	if env.EventID == "" {
		return fmt.Errorf("events: payload on channel %q missing event_id", channel)
	}
	var eventID pgtype.UUID
	if err := eventID.Scan(env.EventID); err != nil {
		return fmt.Errorf("events: invalid event_id %q on channel %q: %w", env.EventID, channel, err)
	}
	if _, loaded := d.inFlight.LoadOrStore(eventID, struct{}{}); loaded {
		return nil
	}
	defer d.inFlight.Delete(eventID)
	if err := h(ctx, eventID); err != nil {
		return fmt.Errorf("events: handler for %q: %w", channel, err)
	}
	return nil
}

// Channels returns the set of channels the dispatcher knows how to route
// (base + dynamic overlay). The caller uses this to issue one LISTEN per
// channel after connect. Order is unspecified; callers should not rely
// on it.
func (d *Dispatcher) Channels() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]string, 0, len(d.handlers)+len(d.dynamic))
	for k := range d.handlers {
		out = append(out, k)
	}
	for k := range d.dynamic {
		if _, shadowed := d.handlers[k]; !shadowed {
			out = append(out, k)
		}
	}
	return out
}

// SetDynamicRoutes atomically replaces the dynamic overlay. Channels
// present in the frozen base table are ignored at Dispatch time (base
// wins), so callers may pass roster-derived maps without filtering.
// Safe for concurrent use with Dispatch/Channels.
func (d *Dispatcher) SetDynamicRoutes(routes map[string]Handler) {
	copied := make(map[string]Handler, len(routes))
	for k, v := range routes {
		copied[k] = v
	}
	d.mu.Lock()
	d.dynamic = copied
	d.mu.Unlock()
}
