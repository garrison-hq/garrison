package throttle

import (
	"encoding/json"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

// evaluatePause returns Decision.Allowed=false when the company's
// pause_until is in the future. NULL pause_until = always allow.
// Past pause_until = always allow (pause window has elapsed; the
// gate doesn't NULL the column out — the column carries the audit
// of when the last pause expired).
func evaluatePause(state store.GetCompanyThrottleStateRow, now time.Time) Decision {
	if !state.PauseUntil.Valid {
		return Decision{Allowed: true}
	}
	if !state.PauseUntil.Time.After(now) {
		// pause_until <= now → window expired
		return Decision{Allowed: true}
	}
	payload, _ := json.Marshal(map[string]any{
		"pause_until": state.PauseUntil.Time.Format(time.RFC3339),
	})
	return Decision{
		Allowed: false,
		Kind:    KindRateLimitPause,
		Payload: payload,
	}
}
