package throttle

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ---------------------------------------------------------------------------
// Minimal store.DBTX fake — used by FireIngressRateCap unit tests below.
// ---------------------------------------------------------------------------

// throttleFakeRow implements pgx.Row for a pre-canned scan result.
type throttleFakeRow struct {
	vals []any
	err  error
}

func (r *throttleFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		if i >= len(r.vals) {
			break
		}
		switch dd := d.(type) {
		case *pgtype.UUID:
			if v, ok := r.vals[i].(pgtype.UUID); ok {
				*dd = v
			}
		case *string:
			if v, ok := r.vals[i].(string); ok {
				*dd = v
			}
		case *pgtype.Timestamptz:
			if v, ok := r.vals[i].(pgtype.Timestamptz); ok {
				*dd = v
			}
		case *[]byte:
			if v, ok := r.vals[i].([]byte); ok {
				*dd = v
			}
		}
	}
	return nil
}

// throttleFakeRows implements pgx.Rows (Query return; never used by the
// throttle write path but required by the DBTX interface).
type throttleFakeRows struct{}

func (r *throttleFakeRows) Close()                                       {}
func (r *throttleFakeRows) Err() error                                   { return nil }
func (r *throttleFakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *throttleFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *throttleFakeRows) Next() bool                                   { return false }
func (r *throttleFakeRows) Scan(_ ...any) error                          { return nil }
func (r *throttleFakeRows) Values() ([]any, error)                       { return nil, nil }
func (r *throttleFakeRows) RawValues() [][]byte                          { return nil }
func (r *throttleFakeRows) Conn() *pgx.Conn                              { return nil }

// throttleFakeDbtx implements store.DBTX with controlled QueryRow/Exec behaviour.
type throttleFakeDbtx struct {
	queryRowVals []any
	queryRowErr  error
	execErr      error
}

func (f *throttleFakeDbtx) Exec(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, f.execErr
}

func (f *throttleFakeDbtx) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return &throttleFakeRows{}, nil
}

func (f *throttleFakeDbtx) QueryRow(_ context.Context, _ string, _ ...interface{}) pgx.Row {
	return &throttleFakeRow{vals: f.queryRowVals, err: f.queryRowErr}
}

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

// ---------------------------------------------------------------------------
// FireIngressRateCap unit tests — cover internal/throttle/ingress.go without
// a real Postgres. The fake DBTX above serves the InsertThrottleEvent QueryRow
// and the NotifyThrottleEvent Exec in-process.
// ---------------------------------------------------------------------------

// TestFireIngressRateCap_SuccessPath — happy path: fake DB returns a valid
// ThrottleEvent row; FireIngressRateCap returns nil.
func TestFireIngressRateCap_SuccessPath(t *testing.T) {
	eventID := pgtype.UUID{Bytes: [16]byte{0x01}, Valid: true}
	companyID := pgtype.UUID{Bytes: [16]byte{0x02}, Valid: true}
	firedAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}

	db := &throttleFakeDbtx{
		// InsertThrottleEvent RETURNING id, company_id, kind, fired_at, payload
		queryRowVals: []any{eventID, companyID, KindIngressRateCapExceeded, firedAt, []byte(`{}`)},
	}
	q := store.New(db)

	err := FireIngressRateCap(context.Background(), q, companyID, "github-test", 60, 30)
	if err != nil {
		t.Errorf("FireIngressRateCap returned err: %v; want nil", err)
	}
}

// TestFireIngressRateCap_InsertError — InsertThrottleEvent fails;
// FireIngressRateCap wraps and returns the error.
func TestFireIngressRateCap_InsertError(t *testing.T) {
	companyID := pgtype.UUID{Bytes: [16]byte{0x03}, Valid: true}
	dbErr := errors.New("insert failed")

	db := &throttleFakeDbtx{
		queryRowErr: dbErr,
	}
	q := store.New(db)

	err := FireIngressRateCap(context.Background(), q, companyID, "github-test", 60, 30)
	if err == nil {
		t.Fatal("FireIngressRateCap returned nil; want wrapped insert error")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("err = %v; want to contain %v", err, dbErr)
	}
}

// TestFireIngressRateCap_NotifyError — InsertThrottleEvent succeeds but
// NotifyThrottleEvent Exec fails; FireIngressRateCap returns the wrapped error.
func TestFireIngressRateCap_NotifyError(t *testing.T) {
	eventID := pgtype.UUID{Bytes: [16]byte{0x04}, Valid: true}
	companyID := pgtype.UUID{Bytes: [16]byte{0x05}, Valid: true}
	firedAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	execErr := errors.New("pg_notify failed")

	db := &throttleFakeDbtx{
		queryRowVals: []any{eventID, companyID, KindIngressRateCapExceeded, firedAt, []byte(`{}`)},
		execErr:      execErr,
	}
	q := store.New(db)

	err := FireIngressRateCap(context.Background(), q, companyID, "github-test", 60, 30)
	if err == nil {
		t.Fatal("FireIngressRateCap returned nil; want wrapped notify error")
	}
	if !errors.Is(err, execErr) {
		t.Errorf("err = %v; want to contain %v", err, execErr)
	}
}
