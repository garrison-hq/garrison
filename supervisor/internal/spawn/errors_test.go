package spawn

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsDeferred — pins the M6 T007 sentinel-recognition predicate the
// dispatcher uses to distinguish deferred-spawn errors (leave
// event_outbox.processed_at NULL, log info, no error-log) from real
// failures.
func TestIsDeferred(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("boom"), false},
		{"sentinel direct", ErrSpawnDeferred, true},
		{"sentinel wrapped", fmt.Errorf("wrap: %w", ErrSpawnDeferred), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDeferred(tc.err); got != tc.want {
				t.Errorf("IsDeferred(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}
