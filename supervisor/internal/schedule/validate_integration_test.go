//go:build integration

package schedule_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/schedule"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// randomUUID returns a valid pgtype.UUID that does not exist in any
// table (random v4).
func randomUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if _, err := rand.Read(u.Bytes[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	u.Bytes[6] = (u.Bytes[6] & 0x0f) | 0x40
	u.Bytes[8] = (u.Bytes[8] & 0x3f) | 0x80
	u.Valid = true
	return u
}

func validIntegrationInput(deptID pgtype.UUID) schedule.ValidationInput {
	return schedule.ValidationInput{
		Name:               "weekly-digest",
		RoleSlug:           "engineer",
		ScheduleExpr:       "daily@09:00",
		ObjectiveTemplate:  "Summarize activity since {{last_fired_at}}.",
		AcceptanceTemplate: "Digest posted for the slot at {{fire_at}}.",
		DepartmentID:       deptID,
		Mode:               "ticket",
	}
}

func TestValidateTaskRejectsUnknownDepartment(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()
	now := time.Now().UTC()

	// No seed: the random department ID cannot exist.
	in := validIntegrationInput(randomUUID(t))

	_, err := schedule.ValidateTask(ctx, q, 15*time.Minute, now, in)
	var ve *schedule.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ValidateTask error = %v, want *ValidationError", err)
	}
	if ve.Field != "department_id" {
		t.Fatalf("ValidationError.Field = %q, want %q", ve.Field, "department_id")
	}
}

func TestValidateTaskRejectsDuplicateName(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := time.Now().UTC()

	in := validIntegrationInput(deptID)

	// First validate succeeds against the seeded department + engineer
	// role and yields the computed next slot.
	next, err := schedule.ValidateTask(ctx, q, 15*time.Minute, now, in)
	if err != nil {
		t.Fatalf("first ValidateTask returned error: %v", err)
	}
	if !next.After(now) {
		t.Fatalf("next = %v is not strictly after now %v", next, now)
	}

	if _, err := q.InsertScheduledTask(ctx, store.InsertScheduledTaskParams{
		Name:                       in.Name,
		DepartmentID:               deptID,
		RoleSlug:                   in.RoleSlug,
		Mode:                       in.Mode,
		ScheduleExpr:               in.ScheduleExpr,
		NextFireAt:                 pgtype.Timestamptz{Time: next, Valid: true},
		ObjectiveTemplate:          in.ObjectiveTemplate,
		AcceptanceCriteriaTemplate: in.AcceptanceTemplate,
	}); err != nil {
		t.Fatalf("InsertScheduledTask: %v", err)
	}

	// Second validate with the same name rejects on uniqueness.
	_, err = schedule.ValidateTask(ctx, q, 15*time.Minute, now, in)
	var ve *schedule.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("duplicate-name ValidateTask error = %v, want *ValidationError", err)
	}
	if ve.Field != "name" {
		t.Fatalf("ValidationError.Field = %q, want %q", ve.Field, "name")
	}

	// Soft-deleting the live row frees the name (live-rows-only
	// uniqueness, idx_scheduled_tasks_name_live).
	if _, err := pool.Exec(ctx,
		`UPDATE scheduled_tasks SET deleted_at = now() WHERE name = $1`, in.Name,
	); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	if _, err := schedule.ValidateTask(ctx, q, 15*time.Minute, now, in); err != nil {
		t.Fatalf("ValidateTask after soft delete returned error: %v", err)
	}
}
