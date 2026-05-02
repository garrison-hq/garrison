// Package throttle implements the M6 spawn-prep gate predicates +
// audit-row writer + pg_notify emitter for the per-company budget +
// rate-limit pause actuators (specs/015-m6-decomposition-hygiene-throttle
// FR-030..FR-045). Public surface is composable inside the caller's
// Postgres transaction; no internal tx is opened.

package throttle

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelThrottleEvent is the pg_notify channel emitted when a
// throttle_events row is inserted. The dashboard's
// /api/sse/throttle SSE bridge (M6 T015) subscribes here.
const ChannelThrottleEvent = "work.throttle.event"

// Kind enumerates the two M6 throttle-event kinds. Mirrors the SQL
// CHECK constraint on throttle_events.kind.
const (
	KindCompanyBudgetExceeded = "company_budget_exceeded"
	KindRateLimitPause        = "rate_limit_pause"
)

// Decision is the result of throttle.Check. Allowed=true means the
// caller may proceed to claim the event_outbox row + insert the
// agent_instances row. Allowed=false means defer; Kind names which
// predicate fired (caller may write the matching audit row via
// FireBudgetDefer or FirePause).
type Decision struct {
	Allowed bool
	Kind    string
	Payload json.RawMessage
}

// Deps carries the runtime configuration the throttle package needs.
// Constructed once at supervisor boot in cmd/supervisor/main.go and
// passed into spawn.Deps + chat policy.
type Deps struct {
	Pool                *pgxpool.Pool
	Logger              *slog.Logger
	DefaultSpawnCostUSD pgtype.Numeric
	RateLimitBackOff    time.Duration
	// Now is the time source. Defaults to time.Now in production;
	// tests inject a deterministic clock for pause-window assertions.
	Now func() time.Time
}

// Check evaluates pause then budget for the supplied company. Pause
// wins over budget when both fire: a paused company should not have
// its budget audit-noised by gate-fire-deferrals while the pause is
// the dominant cause. Caller is responsible for passing a
// *store.Queries bound to its transaction; Check uses q directly so
// it composes inside the caller's tx.
func Check(
	ctx context.Context,
	deps Deps,
	q *store.Queries,
	companyID pgtype.UUID,
) (Decision, error) {
	state, err := q.GetCompanyThrottleState(ctx, companyID)
	if err != nil {
		return Decision{}, fmt.Errorf("throttle: GetCompanyThrottleState: %w", err)
	}
	now := deps.Now()
	if d := evaluatePause(state, now); !d.Allowed {
		return d, nil
	}
	d, err := evaluateBudget(state, deps.DefaultSpawnCostUSD)
	if err != nil {
		return Decision{}, fmt.Errorf("throttle: evaluateBudget: %w", err)
	}
	return d, nil
}

// FirePause writes the pause_until UPDATE + the throttle_events audit
// row + the pg_notify in the supplied tx (q is bound to the caller's
// tx). Called by spawn/pipeline.OnRateLimit when claude returns
// rate_limit_event with status='rejected'.
func FirePause(
	ctx context.Context,
	deps Deps,
	q *store.Queries,
	companyID pgtype.UUID,
	detail RateLimitDetail,
) error {
	now := deps.Now()
	pauseUntil := pgtype.Timestamptz{Time: now.Add(deps.RateLimitBackOff), Valid: true}
	if err := q.UpdateCompanyPauseUntil(ctx, store.UpdateCompanyPauseUntilParams{
		ID:         companyID,
		PauseUntil: pauseUntil,
	}); err != nil {
		return fmt.Errorf("throttle: UpdateCompanyPauseUntil: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"pause_until":           pauseUntil.Time.Format(time.RFC3339),
		"back_off_seconds":      int(deps.RateLimitBackOff.Seconds()),
		"rate_limit_status":     detail.Status,
		"rate_limit_type":       detail.RateLimitType,
		"rate_limit_total_cost": detail.TotalCostUSD,
	})
	if err != nil {
		return fmt.Errorf("throttle: marshal pause payload: %w", err)
	}
	if err := insertEventAndNotify(ctx, q, companyID, KindRateLimitPause, payload); err != nil {
		return fmt.Errorf("throttle: FirePause: %w", err)
	}
	return nil
}

// FireBudgetDefer writes the throttle_events audit row + pg_notify
// in the supplied tx. Called by spawn.prepareSpawn when the budget
// predicate defers. Does NOT touch companies.pause_until — budget
// defers are stateless from a row-state perspective; the next poll
// re-evaluates by re-reading GetCompanyThrottleState.
func FireBudgetDefer(
	ctx context.Context,
	q *store.Queries,
	companyID pgtype.UUID,
	current, estimated, budget pgtype.Numeric,
) error {
	currentF, _ := numericToFloat(current)
	estimatedF, _ := numericToFloat(estimated)
	budgetF, _ := numericToFloat(budget)
	payload, err := json.Marshal(map[string]any{
		"current_24h_usd":    currentF,
		"estimated_next_usd": estimatedF,
		"budget_usd":         budgetF,
	})
	if err != nil {
		return fmt.Errorf("throttle: marshal budget payload: %w", err)
	}
	if err := insertEventAndNotify(ctx, q, companyID, KindCompanyBudgetExceeded, payload); err != nil {
		return fmt.Errorf("throttle: FireBudgetDefer: %w", err)
	}
	return nil
}

// RateLimitDetail carries the forensic fields from the claude
// rate_limit_event into the throttle_events.payload JSON. The
// supervisor's pipeline.OnRateLimit reads the same fields off
// claudeproto.RateLimitEvent.
type RateLimitDetail struct {
	Status        string
	RateLimitType string
	TotalCostUSD  string
}
