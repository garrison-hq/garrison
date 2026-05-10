package throttle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// KindDeptWeeklyBudgetExceeded is the M8 throttle-event kind emitted
// when the per-department weekly ticket-creation budget gate fires.
// Mirrors the SQL CHECK extension in the M8 migration.
const KindDeptWeeklyBudgetExceeded = "dept_weekly_ticket_budget_exceeded"

// ErrDeptWeeklyBudgetExceeded is the typed sentinel returned by
// CheckDeptWeekly when create_ticket would exceed the per-department
// weekly budget. Mappable via errors.Is at the verb-level caller.
var ErrDeptWeeklyBudgetExceeded = errors.New("throttle: dept weekly ticket-creation budget exceeded")

// DeptWeeklyDecision is the result of CheckDeptWeekly. Allowed=true
// means the caller may insert the new ticket; Allowed=false means
// reject + caller writes the throttle_events row via FireDeptWeekly.
type DeptWeeklyDecision struct {
	Allowed      bool
	CurrentCount int64
	// Budget is the configured weekly_ticket_budget. nil = unlimited
	// (M8 alpha default); allowed is always true in that case.
	Budget *int32
	// DepartmentSlug carries the target department's slug for the
	// audit row's payload (forensic clarity). Empty when the query
	// returned no rows (e.g., department deleted between resolve and
	// gate-check — race; treat as Allowed=true since the FK on the
	// ticket insert will surface the deletion as resource_not_found).
	DepartmentSlug string
}

// CheckDeptWeekly evaluates whether a new ticket against deptID would
// exceed the rolling-7-day per-department budget. Pass q bound to the
// caller's tx so the count + check + subsequent insert all see the
// same snapshot.
//
// NULL budget = unlimited (M8 alpha default per FR-200). Non-NULL
// budget: gate fires if (current_count + 1) > budget.
func CheckDeptWeekly(
	ctx context.Context,
	q *store.Queries,
	deptID pgtype.UUID,
) (DeptWeeklyDecision, error) {
	state, err := q.GetDeptWeeklyState(ctx, deptID)
	if err != nil {
		return DeptWeeklyDecision{}, fmt.Errorf("throttle: GetDeptWeeklyState: %w", err)
	}
	d := DeptWeeklyDecision{
		CurrentCount:   state.CurrentCount,
		Budget:         state.WeeklyTicketBudget,
		DepartmentSlug: state.DepartmentSlug,
	}
	if state.WeeklyTicketBudget == nil {
		d.Allowed = true
		return d, nil
	}
	if state.CurrentCount+1 > int64(*state.WeeklyTicketBudget) {
		d.Allowed = false
		return d, nil
	}
	d.Allowed = true
	return d, nil
}

// FireDeptWeekly writes the throttle_events row + emits the
// work.throttle.event pg_notify when CheckDeptWeekly fires. Composes
// inside the caller's tx (q is bound). The throttle_events.company_id
// field is required by M6's schema, so callers pass the target
// department's company; the gate is per-department but the event row
// shares M6's table.
func FireDeptWeekly(
	ctx context.Context,
	q *store.Queries,
	companyID pgtype.UUID,
	decision DeptWeeklyDecision,
	deptID pgtype.UUID,
	attemptedCallerID string,
) error {
	budgetVal := int32(0)
	if decision.Budget != nil {
		budgetVal = *decision.Budget
	}
	payload, err := json.Marshal(map[string]any{
		"department_id":       uuidString(deptID),
		"dept_slug":           decision.DepartmentSlug,
		"current_count":       decision.CurrentCount,
		"budget":              budgetVal,
		"attempted_caller_id": attemptedCallerID,
	})
	if err != nil {
		return fmt.Errorf("throttle: marshal dept-weekly payload: %w", err)
	}
	return insertEventAndNotify(ctx, q, companyID, KindDeptWeeklyBudgetExceeded, payload)
}
