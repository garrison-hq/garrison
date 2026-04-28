package chat

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestUUIDString_Invalid pins the ""-on-invalid contract. Log lines
// callers depend on this for "no UUID yet" rendering instead of
// "00000000-0000-0000-0000-000000000000".
func TestUUIDString_Invalid(t *testing.T) {
	if got := uuidString(pgtype.UUID{}); got != "" {
		t.Errorf("uuidString(invalid) = %q; want empty string", got)
	}
}

// TestUUIDString_Valid round-trips a known UUID through the helper to
// pin the canonical 8-4-4-4-12 hyphen layout. The helper is a hot path
// (every chat log line + every notify), so a regression on the hyphen
// positions would silently produce malformed UUIDs in log/audit rows.
func TestUUIDString_Valid(t *testing.T) {
	var u pgtype.UUID
	if err := u.Scan("11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := uuidString(u); got != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("uuidString = %q", got)
	}
}

// TestNumericToFloat_NullReturnsZero pins the NULL → 0 contract used
// by EnsureCostCapNotExceeded for sessions that have never seen a
// turn (NULL total_cost_usd, no rollup yet).
func TestNumericToFloat_NullReturnsZero(t *testing.T) {
	var n pgtype.Numeric // .Valid stays false
	got, err := numericToFloat(n)
	if err != nil {
		t.Fatalf("numericToFloat(null): %v", err)
	}
	if got != 0 {
		t.Errorf("numericToFloat(null) = %v; want 0", got)
	}
}

// TestNumericToFloat_ValidValue pins the happy path: a valid Numeric
// with a real value should round-trip to its float64 equivalent
// (within IEEE-754 precision; 2.5 is exactly representable).
func TestNumericToFloat_ValidValue(t *testing.T) {
	var n pgtype.Numeric
	if err := n.Scan("2.5"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got, err := numericToFloat(n)
	if err != nil {
		t.Fatalf("numericToFloat: %v", err)
	}
	if got != 2.5 {
		t.Errorf("numericToFloat(2.5) = %v; want 2.5", got)
	}
}

// TestEnsureCostCapNotExceeded_NullCostFailsOpen: a NULL TotalCostUsd
// (session that has never paid) reads as zero and is therefore below
// any positive cap. This is the new-session happy path; the existing
// suite covers BelowCap with a numericFromFloat fixture but doesn't
// pin the NULL case explicitly.
func TestEnsureCostCapNotExceeded_NullCostFailsOpen(t *testing.T) {
	deps := Deps{
		SessionCostCapUSD: 5.0,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	sess := store.ChatSession{} // TotalCostUsd zero/invalid
	if err := EnsureCostCapNotExceeded(context.Background(), deps, sess); err != nil {
		t.Errorf("null cost returned err=%v; want nil (new sessions get a turn)", err)
	}
}
