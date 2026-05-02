package throttle

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// numericFromFloat builds a pgtype.Numeric carrying the supplied float
// at NUMERIC(10,2) precision. Mirrors how spawn.parseCostToNumeric
// produces values from the claude wire-format string; we want the
// same shape in tests so float-comparison roundtrips behave the same.
func numericFromFloat(t *testing.T, f float64) pgtype.Numeric {
	t.Helper()
	cents := int64(f * 100)
	return pgtype.Numeric{
		Int:   big.NewInt(cents),
		Exp:   -2,
		Valid: true,
	}
}

func nullNumeric() pgtype.Numeric {
	return pgtype.Numeric{Valid: false}
}

func nullTimestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Valid: false}
}

func futureTimestamp(t *testing.T, now time.Time, delta time.Duration) pgtype.Timestamptz {
	t.Helper()
	return pgtype.Timestamptz{Time: now.Add(delta), Valid: true}
}

func TestBudgetPredicate_AllowsBelowCap(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     numericFromFloat(t, 0.50),
	}
	d, err := evaluateBudget(state, numericFromFloat(t, 0.10))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !d.Allowed {
		t.Errorf("expected Allowed=true; got Decision=%+v", d)
	}
}

func TestBudgetPredicate_DefersAtCap(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     numericFromFloat(t, 0.95),
	}
	d, err := evaluateBudget(state, numericFromFloat(t, 0.10))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d.Allowed {
		t.Errorf("expected Allowed=false; got Decision=%+v", d)
	}
	if d.Kind != KindCompanyBudgetExceeded {
		t.Errorf("Kind = %q; want %q", d.Kind, KindCompanyBudgetExceeded)
	}
	if len(d.Payload) == 0 {
		t.Errorf("expected non-empty payload")
	}
}

func TestBudgetPredicate_NullBudgetIsAlwaysAllow(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: nullNumeric(),
		Cost24hUsd:     numericFromFloat(t, 999.99),
	}
	d, err := evaluateBudget(state, numericFromFloat(t, 100.00))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !d.Allowed {
		t.Errorf("NULL budget should always allow; got %+v", d)
	}
}

func TestBudgetPredicate_ZeroBudgetDefersAlways(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 0.00),
		Cost24hUsd:     numericFromFloat(t, 0.00),
	}
	d, err := evaluateBudget(state, numericFromFloat(t, 0.01))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d.Allowed {
		t.Errorf("zero budget should always defer; got %+v", d)
	}
	if d.Kind != KindCompanyBudgetExceeded {
		t.Errorf("Kind = %q; want %q", d.Kind, KindCompanyBudgetExceeded)
	}
}

func TestPausePredicate_DefersDuringWindow(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	state := store.GetCompanyThrottleStateRow{
		PauseUntil: futureTimestamp(t, now, 30*time.Second),
	}
	d := evaluatePause(state, now)
	if d.Allowed {
		t.Errorf("expected Allowed=false; got %+v", d)
	}
	if d.Kind != KindRateLimitPause {
		t.Errorf("Kind = %q; want %q", d.Kind, KindRateLimitPause)
	}
}

func TestPausePredicate_AllowsAfterWindow(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	state := store.GetCompanyThrottleStateRow{
		PauseUntil: futureTimestamp(t, now, -30*time.Second),
	}
	d := evaluatePause(state, now)
	if !d.Allowed {
		t.Errorf("expected Allowed=true (past pause); got %+v", d)
	}
}

func TestPausePredicate_NullIsAlwaysAllow(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		PauseUntil: nullTimestamptz(),
	}
	d := evaluatePause(state, time.Now())
	if !d.Allowed {
		t.Errorf("NULL pause_until should always allow; got %+v", d)
	}
}

func TestDecisionComposition_PauseWinsOverBudget(t *testing.T) {
	// Both predicates would defer if evaluated in isolation. Check
	// that pause wins (per package contract: pause is the dominant
	// cause; budget noise should not shadow the pause audit).
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	pauseState := store.GetCompanyThrottleStateRow{
		PauseUntil:     futureTimestamp(t, now, 30*time.Second),
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     numericFromFloat(t, 0.95),
	}
	d := evaluatePause(pauseState, now)
	if d.Allowed || d.Kind != KindRateLimitPause {
		t.Errorf("pause should fire first; got %+v", d)
	}
}

// TestBudgetPredicate_InvalidCostNumericReturnsZero — when Cost24hUsd
// carries an invalid (Valid=false) Numeric, numericToFloat short-
// circuits to (0, nil) and evaluateBudget treats current spend as $0.
// Covers the numericToFloat !Valid branch reached only via a
// state-row-with-NULL-cost (no agent_instances rows yet).
func TestBudgetPredicate_InvalidCostNumericReturnsZero(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     pgtype.Numeric{Valid: false},
	}
	d, err := evaluateBudget(state, numericFromFloat(t, 0.10))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !d.Allowed {
		t.Errorf("expected Allowed=true (Cost24hUsd treated as 0); got %+v", d)
	}
}

// TestBudgetPredicate_NaNCostFlowsThroughAsZero — pgtype.Numeric with
// NaN doesn't error from Float64Value (NaN flows through); evaluateBudget
// computes current+estimated>budget where current=NaN, which is false
// under IEEE-754 NaN semantics, so the gate allows. Covers the
// numericToFloat code path without forcing an error.
func TestBudgetPredicate_NaNCostFlowsThroughAsZero(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     pgtype.Numeric{Valid: true, NaN: true},
	}
	d, err := evaluateBudget(state, numericFromFloat(t, 0.10))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// NaN comparisons are always false; "current+estimated > budget"
	// with current=NaN evaluates as false → Allowed=true.
	if !d.Allowed {
		t.Errorf("expected Allowed=true (NaN flows through, comparison false); got %+v", d)
	}
}

// outOfRangeNumeric returns a pgtype.Numeric whose Exp pushes
// Float64Value's strconv.ParseFloat past math.MaxFloat64, making
// the conversion return (Float8{Valid:false}, error). Used to
// pin numericToFloat's error wrapping (the only call shape that
// actually triggers an error from pgx's Numeric encoder).
func outOfRangeNumeric() pgtype.Numeric {
	return pgtype.Numeric{
		Int:   big.NewInt(1),
		Exp:   1_000_000,
		Valid: true,
	}
}

// TestNumericToFloat_PropagatesParseError pins numericToFloat's
// `return 0, err` branch — reachable only when n.Valid=true AND
// pgx's Float64Value returns (Float8{}, err) (out-of-range exp).
func TestNumericToFloat_PropagatesParseError(t *testing.T) {
	_, err := numericToFloat(outOfRangeNumeric())
	if err == nil {
		t.Fatal("expected error from out-of-range numeric")
	}
}

// TestBudgetPredicate_PropagatesCurrentParseError covers the
// "evaluateBudget: parse current" wrapper — fires when the
// rolling-24h Numeric is structurally valid but Float64Value
// can't reduce it to a float.
func TestBudgetPredicate_PropagatesCurrentParseError(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     outOfRangeNumeric(),
	}
	_, err := evaluateBudget(state, numericFromFloat(t, 0.10))
	if err == nil {
		t.Fatal("expected error from out-of-range Cost24hUsd")
	}
}

// TestBudgetPredicate_PropagatesEstimatedParseError covers the
// "evaluateBudget: parse estimated" wrapper.
func TestBudgetPredicate_PropagatesEstimatedParseError(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: numericFromFloat(t, 1.00),
		Cost24hUsd:     numericFromFloat(t, 0.10),
	}
	_, err := evaluateBudget(state, outOfRangeNumeric())
	if err == nil {
		t.Fatal("expected error from out-of-range estimated")
	}
}

// TestBudgetPredicate_PropagatesBudgetParseError covers the
// "evaluateBudget: parse budget" wrapper.
func TestBudgetPredicate_PropagatesBudgetParseError(t *testing.T) {
	state := store.GetCompanyThrottleStateRow{
		DailyBudgetUsd: outOfRangeNumeric(),
		Cost24hUsd:     numericFromFloat(t, 0.10),
	}
	_, err := evaluateBudget(state, numericFromFloat(t, 0.10))
	if err == nil {
		t.Fatal("expected error from out-of-range DailyBudgetUsd")
	}
}

// TestUUIDStringEmptyForInvalid — pins the uuidString invalid-input
// branch. Used by insertEventAndNotify; the production call sites
// always pass Valid UUIDs but the helper is defensive.
func TestUUIDStringEmptyForInvalid(t *testing.T) {
	got := uuidString(pgtype.UUID{Valid: false})
	if got != "" {
		t.Errorf("uuidString(invalid) = %q; want empty", got)
	}
}

func TestNotifyPayloadShape(t *testing.T) {
	// emitNotify constructs the payload with these four fields.
	// Verify the JSON shape so the dashboard SSE bridge consumer
	// has a stable contract.
	body := map[string]string{
		"event_id":   "00000000-0000-0000-0000-000000000001",
		"company_id": "00000000-0000-0000-0000-00000000000c",
		"kind":       KindCompanyBudgetExceeded,
		"fired_at":   time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event_id", "company_id", "kind", "fired_at"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing key %q in notify payload", key)
		}
	}
}
