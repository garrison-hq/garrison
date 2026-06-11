package schedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ValidationError is the typed rejection ValidateTask returns for any
// operator-correctable input problem (FR-105). Callers (the chat verb,
// the dashboardapi endpoint, and — defensively — the tick loop) map it
// to validation_failed with Field/Msg as the detail.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Msg)
}

// Task modes mirror the SQL CHECK constraint on scheduled_tasks.mode.
const (
	ModeTicket  = "ticket"
	ModeOneshot = "oneshot"
)

// ValidationInput carries the operator-supplied fields of a scheduled
// task create/edit, pre-resolved to a department ID (slug resolution
// is the caller's concern — chat verb and dashboard resolve
// differently).
type ValidationInput struct {
	Name, RoleSlug, ScheduleExpr, ObjectiveTemplate, AcceptanceTemplate string
	DepartmentID                                                        pgtype.UUID
	Mode                                                                string
}

// ValidateTask enforces every FR-105 invariant for scheduled-task
// creation and edits: grammar parses, effective firing interval is at
// or above the operator-tunable minimum (FR-404), first computed slot
// is future-dated, name is unique among live tasks, department and
// role exist, templates are non-empty, and mode is in the enum. On
// success it returns the computed next_fire_at; on rejection a
// *ValidationError. Any other error is a database failure, not a
// validation outcome.
//
// Check order: pure (DB-free) checks first, so grammar/interval/
// template/mode rejections never cost a round-trip; DB-backed checks
// (name, department, role) follow.
func ValidateTask(ctx context.Context, q *store.Queries, minInterval time.Duration, now time.Time, in ValidationInput) (time.Time, error) {
	if in.Mode != ModeTicket && in.Mode != ModeOneshot {
		return time.Time{}, &ValidationError{Field: "mode", Msg: fmt.Sprintf("unknown mode %q (want ticket or oneshot)", in.Mode)}
	}
	if in.ObjectiveTemplate == "" {
		return time.Time{}, &ValidationError{Field: "objective_template", Msg: "must not be empty"}
	}
	if in.AcceptanceTemplate == "" {
		return time.Time{}, &ValidationError{Field: "acceptance_criteria_template", Msg: "must not be empty"}
	}

	expr, err := Parse(in.ScheduleExpr)
	if err != nil {
		var pe *ParseError
		if errors.As(err, &pe) {
			return time.Time{}, &ValidationError{Field: "schedule_expr", Msg: pe.Error()}
		}
		return time.Time{}, fmt.Errorf("parse schedule expression: %w", err)
	}
	if expr.MinInterval() < minInterval {
		return time.Time{}, &ValidationError{
			Field: "schedule_expr",
			Msg:   fmt.Sprintf("effective interval %s is below the minimum firing interval %s (FR-404)", expr.MinInterval(), minInterval),
		}
	}
	next := expr.Next(now)
	// True by construction (Next is strictly future) — kept as an
	// explicit assertion per FR-105.
	if !next.After(now) {
		return time.Time{}, &ValidationError{Field: "schedule_expr", Msg: "first computed slot is not future-dated"}
	}

	// Name uniqueness among live tasks only (soft-deleted names are
	// reusable, matching idx_scheduled_tasks_name_live).
	if _, err := q.SelectScheduledTaskByName(ctx, in.Name); err == nil {
		return time.Time{}, &ValidationError{Field: "name", Msg: fmt.Sprintf("a scheduled task named %q already exists", in.Name)}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, fmt.Errorf("check name uniqueness: %w", err)
	}

	if _, err := q.GetDepartmentByID(ctx, in.DepartmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, &ValidationError{Field: "department_id", Msg: "department does not exist"}
		}
		return time.Time{}, fmt.Errorf("check department existence: %w", err)
	}

	if _, err := q.GetAgentByDepartmentAndRole(ctx, store.GetAgentByDepartmentAndRoleParams{
		DepartmentID: in.DepartmentID,
		RoleSlug:     in.RoleSlug,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, &ValidationError{Field: "role_slug", Msg: fmt.Sprintf("no active agent with role %q in the department", in.RoleSlug)}
		}
		return time.Time{}, fmt.Errorf("check role existence: %w", err)
	}

	return next, nil
}
