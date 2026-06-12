package schedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// stubRow satisfies pgx.Row with a scripted Scan error. A nil err
// leaves the scan destinations zero-valued, which is all the
// happy-path existence probes need.
type stubRow struct{ err error }

func (r stubRow) Scan(...any) error { return r.err }

// stubDBTX is a DB-free store.DBTX for the unit tests: the
// scheduled_tasks name probe reports no live row (name is unique) and
// the department/agent existence probes report a hit. The DB-backed
// rejection paths are covered by the integration-tagged tests in
// validate_integration_test.go.
type stubDBTX struct{}

func (stubDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (stubDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (stubDBTX) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	if strings.Contains(sql, "FROM scheduled_tasks") {
		return stubRow{err: pgx.ErrNoRows}
	}
	return stubRow{}
}

func validInput() ValidationInput {
	return ValidationInput{
		Name:               "weekly-digest",
		RoleSlug:           "engineer",
		ScheduleExpr:       "daily@09:00",
		ObjectiveTemplate:  "Summarize activity since {{last_fired_at}}.",
		AcceptanceTemplate: "Digest posted for the slot at {{fire_at}}.",
		DepartmentID:       pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Mode:               ModeTicket,
	}
}

func TestValidateTaskRejectsSubMinimumInterval(t *testing.T) {
	q := store.New(stubDBTX{})
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)

	in := validInput()
	in.ScheduleExpr = "every@5m"

	_, err := ValidateTask(context.Background(), q, 15*time.Minute, now, in)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ValidateTask(every@5m, min 15m) error = %v, want *ValidationError", err)
	}
	if ve.Field != "schedule_expr" {
		t.Fatalf("ValidationError.Field = %q, want %q", ve.Field, "schedule_expr")
	}

	// The same expression passes once the minimum allows it — the
	// rejection above is the interval check, not the grammar.
	if _, err := ValidateTask(context.Background(), q, 5*time.Minute, now, in); err != nil {
		t.Fatalf("ValidateTask(every@5m, min 5m) returned error: %v", err)
	}
}

func TestValidateTaskComputesNextFire(t *testing.T) {
	q := store.New(stubDBTX{})

	cases := []struct {
		name string
		expr string
		now  time.Time
		want time.Time
	}{
		{
			name: "daily before slot fires same day",
			expr: "daily@09:00",
			now:  time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "daily after slot fires next day",
			expr: "daily@09:00",
			now:  time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC),
			want: time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly walks to weekday",
			expr: "weekly@mon@08:00",
			now:  time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC), // a Wednesday
			want: time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC),
		},
		{
			name: "every adds the interval",
			expr: "every@45m",
			now:  time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC),
			want: time.Date(2026, 6, 10, 8, 45, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			in.ScheduleExpr = tc.expr

			next, err := ValidateTask(context.Background(), q, 15*time.Minute, tc.now, in)
			if err != nil {
				t.Fatalf("ValidateTask(%q) returned error: %v", tc.expr, err)
			}
			if !next.Equal(tc.want) {
				t.Fatalf("ValidateTask(%q) next = %v, want %v", tc.expr, next, tc.want)
			}
			if !next.After(tc.now) {
				t.Fatalf("ValidateTask(%q) next = %v is not strictly after now %v", tc.expr, next, tc.now)
			}
		})
	}
}

func TestValidateTaskRejectsPureInputProblems(t *testing.T) {
	q := store.New(stubDBTX{})
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		mutate    func(*ValidationInput)
		wantField string
	}{
		{"malformed grammar", func(in *ValidationInput) { in.ScheduleExpr = "*/5 * * * *" }, "schedule_expr"},
		{"unknown mode", func(in *ValidationInput) { in.Mode = "cron" }, "mode"},
		{"empty objective template", func(in *ValidationInput) { in.ObjectiveTemplate = "" }, "objective_template"},
		{"empty acceptance template", func(in *ValidationInput) { in.AcceptanceTemplate = "" }, "acceptance_criteria_template"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)

			_, err := ValidateTask(context.Background(), q, 15*time.Minute, now, in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidateTask error = %v, want *ValidationError", err)
			}
			if ve.Field != tc.wantField {
				t.Fatalf("ValidationError.Field = %q, want %q", ve.Field, tc.wantField)
			}
		})
	}
}

// TestValidationErrorFormatsFieldAndMsg pins the typed rejection's
// error string shape — the chat verb and dashboardapi endpoint both
// surface it verbatim as the validation_failed detail.
func TestValidationErrorFormatsFieldAndMsg(t *testing.T) {
	e := &ValidationError{Field: "schedule_expr", Msg: "boom"}
	if got, want := e.Error(), "invalid schedule_expr: boom"; got != want {
		t.Fatalf("Error() = %q; want %q", got, want)
	}
}

// scriptedDBTX scripts each existence probe independently so the
// database-failure (non-ErrNoRows) wrapper branches are reachable
// without fault-injecting a real Postgres: ValidateTask must surface
// those as plain errors, NOT *ValidationError (they are operator-
// uncorrectable transport failures, FR-105's boundary).
type scriptedDBTX struct {
	nameErr error // SELECT ... FROM scheduled_tasks
	deptErr error // SELECT ... FROM departments
	roleErr error // SELECT ... FROM agents
}

func (scriptedDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (scriptedDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (s scriptedDBTX) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM scheduled_tasks"):
		return stubRow{err: s.nameErr}
	case strings.Contains(sql, "FROM departments"):
		return stubRow{err: s.deptErr}
	default:
		return stubRow{err: s.roleErr}
	}
}

// TestValidateTaskSurfacesDBFailuresAsPlainErrors — each DB-backed
// probe's transport-failure branch wraps and returns the error without
// converting it to a ValidationError.
func TestValidateTaskSurfacesDBFailuresAsPlainErrors(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	boom := errors.New("connection reset")
	cases := []struct {
		name     string
		db       scriptedDBTX
		wantWrap string
	}{
		{
			name:     "name probe failure",
			db:       scriptedDBTX{nameErr: boom, deptErr: nil, roleErr: nil},
			wantWrap: "check name uniqueness",
		},
		{
			name:     "department probe failure",
			db:       scriptedDBTX{nameErr: pgx.ErrNoRows, deptErr: boom, roleErr: nil},
			wantWrap: "check department existence",
		},
		{
			name:     "role probe failure",
			db:       scriptedDBTX{nameErr: pgx.ErrNoRows, deptErr: nil, roleErr: boom},
			wantWrap: "check role existence",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateTask(context.Background(), store.New(tc.db), 15*time.Minute, now, validInput())
			if err == nil {
				t.Fatal("ValidateTask = nil error; want a wrapped DB failure")
			}
			var ve *ValidationError
			if errors.As(err, &ve) {
				t.Fatalf("err = %v is a ValidationError; DB failures must stay plain errors", err)
			}
			if !errors.Is(err, boom) {
				t.Errorf("err = %v; want it to wrap the probe failure", err)
			}
			if !strings.Contains(err.Error(), tc.wantWrap) {
				t.Errorf("err = %v; want the %q wrap", err, tc.wantWrap)
			}
		})
	}
}

// TestValidateTaskRejectsUnknownRole — an ErrNoRows from the agent
// probe is the operator-correctable "no active agent with that role"
// rejection (FR-105), typed on role_slug.
func TestValidateTaskRejectsUnknownRole(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	db := scriptedDBTX{nameErr: pgx.ErrNoRows, deptErr: nil, roleErr: pgx.ErrNoRows}
	_, err := ValidateTask(context.Background(), store.New(db), 15*time.Minute, now, validInput())
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v; want *ValidationError", err)
	}
	if ve.Field != "role_slug" {
		t.Errorf("Field = %q; want role_slug", ve.Field)
	}
	if !strings.Contains(ve.Msg, "no active agent") {
		t.Errorf("Msg = %q; want the no-active-agent detail", ve.Msg)
	}
}
