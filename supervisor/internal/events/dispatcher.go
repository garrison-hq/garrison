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

// Dispatcher is the static route table built once at process startup (FR-014).
// The handler map is copied into the dispatcher so callers cannot mutate the
// routing after construction — dynamic registration is explicitly out of
// scope for M1.
type Dispatcher struct {
	handlers map[string]Handler
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
	if err := h(ctx, eventID); err != nil {
		return fmt.Errorf("events: handler for %q: %w", channel, err)
	}
	return nil
}

// Channels returns the set of channels the dispatcher knows how to route.
// The caller uses this to issue one LISTEN per channel after connect. Order
// is unspecified; callers should not rely on it.
func (d *Dispatcher) Channels() []string {
	out := make([]string, 0, len(d.handlers))
	for k := range d.handlers {
		out = append(out, k)
	}
	return out
}
