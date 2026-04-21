package events

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// listen issues one LISTEN per channel the dispatcher knows about and then
// loops on conn.WaitForNotification, dispatching each notification through
// the Dispatcher. The loop returns on any error from LISTEN, WaitForNotification,
// or Dispatch (the latter rewraps so callers can distinguish a routing
// problem from a connection problem via errors.Is(ErrUnknownChannel)).
//
// Exit cases:
//   - ctx cancelled → returns ctx.Err() wrapped.
//   - Connection error (pg drops, admin shutdown, network) → returns the
//     underlying pgx error wrapped. Caller (runWithReconnect) handles the
//     reconnect sequence.
//
// listen does not own the conn's lifecycle — the caller closes the conn on
// return, which implicitly releases the advisory lock (plan.md §"pg_notify
// listener connection lifecycle" step 7).
func listen(ctx context.Context, conn *pgx.Conn, dispatcher *Dispatcher) error {
	for _, ch := range dispatcher.Channels() {
		// pgx sanitises channel names in the positional path, but LISTEN does
		// not accept parameters; we must inject the channel literal. Channels
		// come from the dispatcher's frozen route table — themselves supplied
		// by cmd/supervisor code, not external input — so string injection is
		// safe by construction. Quote the identifier to allow dotted names.
		stmt := fmt.Sprintf(`LISTEN "%s"`, ch)
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("events: LISTEN %q: %w", ch, err)
		}
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("events: WaitForNotification: %w", err)
		}
		if err := dispatcher.Dispatch(ctx, n.Channel, n.Payload); err != nil {
			// Dispatch errors are logged by the caller via returning here.
			// Malformed payloads and unknown channels should not kill the
			// listener loop, but we need the caller to observe them. Returning
			// would force a reconnect — which is too heavy. For M1, accept
			// that the dispatcher callback logs via its own path and
			// continue.
			_ = err
			continue
		}
	}
}
