//go:build integration

package dashboardapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// fullValidateBody renders the review-#4 full-validation body against
// the SeedM21 fixture (engineering department, active engineer agent).
func fullValidateBody(name, deptID, roleSlug, expr string) string {
	return fmt.Sprintf(
		`{"schedule_expr":%q,"mode":"ticket","name":%q,"department_id":%q,"role_slug":%q,"objective_template":"Summarize activity since {{last_fired_at}}.","acceptance_criteria_template":"Digest posted for {{fire_at}}."}`,
		expr, name, deptID, roleSlug,
	)
}

func uuidText(t *testing.T, u pgtype.UUID) string {
	t.Helper()
	v, err := u.Value()
	if err != nil {
		t.Fatalf("uuidText: %v", err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("uuidText: %T", v)
	}
	return s
}

// TestScheduleValidateFullBodyHappyPath (M9 review #4): a full body
// against real queries passes every FR-105 invariant and returns the
// standard 200 {ok, next_fire_at, min_interval_ok} shape.
func TestScheduleValidateFullBodyHappyPath(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	h := newScheduleValidateHandler(store.New(pool), 15*time.Minute, func() time.Time { return fixedNow }, nil)

	rec := scheduleValidatePOST(t, h, fullValidateBody("digest", uuidText(t, deptID), "engineer", "daily@06:00"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body scheduleValidateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if !body.OK || !body.MinIntervalOK {
		t.Errorf("ok=%v min_interval_ok=%v; want both true", body.OK, body.MinIntervalOK)
	}
	// 06:00 already passed at fixedNow 12:00 → tomorrow 06:00.
	if want := time.Date(2026, 6, 11, 6, 0, 0, 0, time.UTC); !body.NextFireAt.Equal(want) {
		t.Errorf("next_fire_at=%v; want %v", body.NextFireAt, want)
	}
}

// TestScheduleValidateFullBodyUnknownRole (M9 review #4): a role with
// no active agent in the department rejects 422 with the typed
// role_slug field detail — the gap the pre-review handler (expression-
// only, nil queries) could not catch.
func TestScheduleValidateFullBodyUnknownRole(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	h := newScheduleValidateHandler(store.New(pool), 15*time.Minute, func() time.Time { return fixedNow }, nil)

	rec := scheduleValidatePOST(t, h, fullValidateBody("digest", uuidText(t, deptID), "astrologer", "daily@06:00"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d; want 422; body=%s", rec.Code, rec.Body.String())
	}
	var body scheduleFieldErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if body.Error != "validation_failed" {
		t.Errorf("error=%q; want validation_failed", body.Error)
	}
	if body.Field != "role_slug" {
		t.Errorf("field=%q; want role_slug", body.Field)
	}
	if body.Message == "" {
		t.Error("message empty; want the no-active-agent detail")
	}
}

// TestScheduleValidateFullBodyDuplicateLiveName (M9 review #4): a name
// already carried by a live task rejects 422 with field=name BEFORE the
// dashboard's write tx — closing analyze-finding U2's index-collision-
// only detection.
func TestScheduleValidateFullBodyDuplicateLiveName(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	h := newScheduleValidateHandler(q, 15*time.Minute, func() time.Time { return fixedNow }, nil)

	if _, err := q.InsertScheduledTask(context.Background(), store.InsertScheduledTaskParams{
		Name:                       "digest",
		DepartmentID:               deptID,
		RoleSlug:                   "engineer",
		Mode:                       "ticket",
		ScheduleExpr:               "daily@06:00",
		NextFireAt:                 pgtype.Timestamptz{Time: fixedNow.Add(time.Hour), Valid: true},
		ObjectiveTemplate:          "o",
		AcceptanceCriteriaTemplate: "a",
	}); err != nil {
		t.Fatalf("seed live task: %v", err)
	}

	rec := scheduleValidatePOST(t, h, fullValidateBody("digest", uuidText(t, deptID), "engineer", "daily@06:00"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d; want 422; body=%s", rec.Code, rec.Body.String())
	}
	var body scheduleFieldErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if body.Error != "validation_failed" || body.Field != "name" {
		t.Errorf("error=%q field=%q; want validation_failed/name", body.Error, body.Field)
	}

	// Soft-deleting the live row frees the name (idx_scheduled_tasks_
	// name_live semantics ride through SelectScheduledTaskByName).
	if _, err := pool.Exec(context.Background(),
		`UPDATE scheduled_tasks SET deleted_at = NOW() WHERE name = 'digest'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	rec = scheduleValidatePOST(t, h, fullValidateBody("digest", uuidText(t, deptID), "engineer", "daily@06:00"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200 after soft delete; body=%s", rec.Code, rec.Body.String())
	}
}
