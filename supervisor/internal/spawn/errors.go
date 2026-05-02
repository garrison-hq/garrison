package spawn

import "errors"

// ErrSpawnDeferred is the M6 T007 sentinel returned by prepareSpawn when
// the throttle gate (internal/throttle) defers the spawn. Spawn() catches
// this error and returns nil to the dispatcher so the event_outbox row
// stays unprocessed and the next poll retries — same posture as the
// existing concurrency-cap-deferred path. The audit trail is captured in
// throttle_events (budget defer) or pre-existing pause_until + the
// rate-limit-pause throttle_events row (set by OnRateLimit).
var ErrSpawnDeferred = errors.New("spawn: deferred by throttle gate")

// IsDeferred reports whether the supplied error is a deferral signal that
// the dispatcher should treat as "leave event_outbox.processed_at NULL,
// log at info, do not error-log". Currently true for ErrSpawnDeferred;
// future deferral causes layer onto this predicate.
func IsDeferred(err error) bool {
	return errors.Is(err, ErrSpawnDeferred)
}
