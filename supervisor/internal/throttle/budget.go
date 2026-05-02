package throttle

import (
	"encoding/json"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// evaluateBudget returns Decision.Allowed=false when the company has
// a daily_budget_usd set AND (rolling-24h cost + estimated next spawn)
// would exceed the budget. NULL budget = always allow; zero budget
// also defers (zero is the explicit "full pause on spend axis"
// signal — semantically equivalent to concurrency_cap=0 for the
// time axis).
func evaluateBudget(state store.GetCompanyThrottleStateRow, estimatedNext pgtype.Numeric) (Decision, error) {
	if !state.DailyBudgetUsd.Valid {
		// NULL budget — never enforce.
		return Decision{Allowed: true}, nil
	}
	current, err := numericToFloat(state.Cost24hUsd)
	if err != nil {
		return Decision{}, fmt.Errorf("evaluateBudget: parse current: %w", err)
	}
	estimated, err := numericToFloat(estimatedNext)
	if err != nil {
		return Decision{}, fmt.Errorf("evaluateBudget: parse estimated: %w", err)
	}
	budget, err := numericToFloat(state.DailyBudgetUsd)
	if err != nil {
		return Decision{}, fmt.Errorf("evaluateBudget: parse budget: %w", err)
	}
	// budget=0 → always defer; current+estimated>budget → defer.
	if budget == 0 || current+estimated > budget {
		payload, _ := json.Marshal(map[string]any{
			"current_24h_usd":    current,
			"estimated_next_usd": estimated,
			"budget_usd":         budget,
		})
		return Decision{
			Allowed: false,
			Kind:    KindCompanyBudgetExceeded,
			Payload: payload,
		}, nil
	}
	return Decision{Allowed: true}, nil
}

// numericToFloat converts a pgtype.Numeric to float64. Mirrors the
// helper in internal/chat/persistence.go (kept local so the throttle
// package doesn't reach across internal boundaries). Returns 0 for
// NULL or unparseable values; the caller uses the result for a
// direct comparison so 0 is the safe defer-allow choice.
func numericToFloat(n pgtype.Numeric) (float64, error) {
	if !n.Valid {
		return 0, nil
	}
	f, err := n.Float64Value()
	if err != nil {
		return 0, err
	}
	if !f.Valid {
		return 0, nil
	}
	return f.Float64, nil
}
