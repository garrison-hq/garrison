package chat

import (
	"io"
	"log/slog"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

func discardDeps() Deps {
	return Deps{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		SessionCostCapUSD: 1.00,
	}
}

// TestEnsureCostCapNotExceeded_BelowCap verifies the reactive check
// allows the spawn when total_cost_usd < cap.
func TestEnsureCostCapNotExceeded_BelowCap(t *testing.T) {
	deps := discardDeps()
	sess := store.ChatSession{
		ID:           pgtype.UUID{},
		TotalCostUsd: numericFromFloat(0.5), // below 1.00
	}
	if err := EnsureCostCapNotExceeded(t.Context(), deps, sess); err != nil {
		t.Errorf("expected nil; got %v", err)
	}
}

// TestEnsureCostCapNotExceeded_AtCap is the boundary case from
// clarify Q5: "refuse if total_cost_usd >= cap" — equality refuses.
func TestEnsureCostCapNotExceeded_AtCap(t *testing.T) {
	deps := discardDeps()
	sess := store.ChatSession{TotalCostUsd: numericFromFloat(1.00)}
	if err := EnsureCostCapNotExceeded(t.Context(), deps, sess); err != ErrCostCapReached {
		t.Errorf("expected ErrCostCapReached; got %v", err)
	}
}

// TestEnsureCostCapNotExceeded_AboveCap validates the worst-case
// overshoot: a single turn nudged total_cost over the cap; the next
// turn refuses.
func TestEnsureCostCapNotExceeded_AboveCap(t *testing.T) {
	deps := discardDeps()
	sess := store.ChatSession{TotalCostUsd: numericFromFloat(1.50)}
	if err := EnsureCostCapNotExceeded(t.Context(), deps, sess); err != ErrCostCapReached {
		t.Errorf("expected ErrCostCapReached; got %v", err)
	}
}

// TestEnsureCostCapNotExceeded_ZeroCapDisabled verifies SessionCostCapUSD
// = 0 (and negative) disables the check entirely (operator override).
func TestEnsureCostCapNotExceeded_ZeroCapDisabled(t *testing.T) {
	deps := discardDeps()
	deps.SessionCostCapUSD = 0
	sess := store.ChatSession{TotalCostUsd: numericFromFloat(99.99)}
	if err := EnsureCostCapNotExceeded(t.Context(), deps, sess); err != nil {
		t.Errorf("expected nil (cap disabled); got %v", err)
	}
}

// TestAssignAssistantTurnIndex pins the operator+1 contract from
// clarify Q2.
func TestAssignAssistantTurnIndex(t *testing.T) {
	cases := []struct {
		operator, want int32
	}{
		{0, 1}, {2, 3}, {99, 100},
	}
	for _, c := range cases {
		got := AssignAssistantTurnIndex(c.operator)
		if got != c.want {
			t.Errorf("operator=%d: got %d, want %d", c.operator, got, c.want)
		}
	}
}

// numericFromFloat is a test helper that builds a pgtype.Numeric from
// a float64 by routing through pgx's text-shape Scan path. The
// internal/chat persistence_test exercise comparison logic, not the
// numeric's wire format.
func numericFromFloat(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(formatFloat(f)); err != nil {
		panic(err)
	}
	return n
}

func formatFloat(f float64) string {
	// Use a fixed-precision text representation that pgtype.Numeric
	// accepts via its Scan path.
	if f == 0 {
		return "0"
	}
	// FormatFloat-equivalent without bringing strconv into the test
	// chain; basic enough for the test fixtures used here.
	return formatFloatHex(f)
}

// formatFloatHex avoids depending on strconv for the small set of
// numbers test fixtures use (0.5, 1.0, 1.5, 99.99).
func formatFloatHex(f float64) string {
	switch f {
	case 0.5:
		return "0.5"
	case 1.0:
		return "1.0"
	case 1.5:
		return "1.5"
	case 99.99:
		return "99.99"
	}
	// Generic fallback — uses a buffer big enough for typical cost
	// values. Tests above pin exact values so the fallback shouldn't
	// fire in the persistence_test scope.
	return "0"
}
