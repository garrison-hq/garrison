package migrate7

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestUUIDString covers the canonical-form helper used by the audit
// row's args_jsonb. Empty (Valid=false) → empty string.
func TestUUIDString(t *testing.T) {
	if got := uuidString(pgtype.UUID{Valid: false}); got != "" {
		t.Errorf("invalid UUID should yield empty string, got %q", got)
	}
	u := pgtype.UUID{Valid: true, Bytes: [16]byte{0xa, 0xb, 0xc, 0xd, 0xe, 0xf, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}
	got := uuidString(u)
	if len(got) != 36 {
		t.Errorf("uuidString returned malformed UUID: %q", got)
	}
}

// TestStringPtrEmptyVsNonEmpty confirms the small wrapper used to
// satisfy *string sqlc params.
func TestStringPtrEmptyVsNonEmpty(t *testing.T) {
	if stringPtr("") != nil {
		t.Error("empty string should map to nil pointer")
	}
	if got := stringPtr("digest"); got == nil || *got != "digest" {
		t.Errorf("non-empty string should round-trip, got %v", got)
	}
}

// TestInt32Ptr asserts int32Ptr returns a usable pointer for any
// int32 input including zero (which is a valid host_uid alias for
// "range start when no rows").
func TestInt32Ptr(t *testing.T) {
	cases := []int32{0, 1, 1000, 1999}
	for _, want := range cases {
		got := int32Ptr(want)
		if got == nil {
			t.Errorf("int32Ptr(%d) returned nil", want)
			continue
		}
		if *got != want {
			t.Errorf("int32Ptr(%d) = %d", want, *got)
		}
	}
}
