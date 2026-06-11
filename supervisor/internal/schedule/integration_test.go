//go:build integration

package schedule

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testObjectiveTemplate  = "Summarize activity since {{last_fired_at}}."
	testAcceptanceTemplate = "Digest posted for the slot at {{fire_at}}."
)

// tickDeps builds the Deps tickOnce needs, with a deterministic clock.
func tickDeps(pool *pgxpool.Pool, logger *slog.Logger, now time.Time) Deps {
	return Deps{
		Pool:         pool,
		Queries:      store.New(pool),
		Logger:       logger,
		TickInterval: time.Second,
		Now:          func() time.Time { return now },
	}
}

// fixedNow returns a microsecond-truncated UTC wall time so values
// written through timestamptz columns round-trip exactly.
func fixedNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// seedScheduledTask inserts a task row directly (validation is T004's
// concern, not the tick loop's) with the supplied next_fire_at.
func seedScheduledTask(t *testing.T, q *store.Queries, deptID pgtype.UUID, name, mode, expr string, nextFireAt time.Time) store.ScheduledTask {
	t.Helper()
	task, err := q.InsertScheduledTask(context.Background(), store.InsertScheduledTaskParams{
		Name:                       name,
		DepartmentID:               deptID,
		RoleSlug:                   "engineer",
		Mode:                       mode,
		ScheduleExpr:               expr,
		NextFireAt:                 pgtype.Timestamptz{Time: nextFireAt, Valid: true},
		ObjectiveTemplate:          testObjectiveTemplate,
		AcceptanceCriteriaTemplate: testAcceptanceTemplate,
	})
	if err != nil {
		t.Fatalf("seedScheduledTask: %v", err)
	}
	return task
}

// listRuns returns every run row for the task, oldest first.
func listRuns(t *testing.T, pool *pgxpool.Pool, taskID pgtype.UUID) []store.ScheduledTaskRun {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, scheduled_task_id, slot_at, fired_at, outcome, detail,
		       ticket_id, agent_instance_id, structured_outcome
		  FROM scheduled_task_runs
		 WHERE scheduled_task_id = $1
		 ORDER BY fired_at`, taskID)
	if err != nil {
		t.Fatalf("listRuns: %v", err)
	}
	defer rows.Close()
	var runs []store.ScheduledTaskRun
	for rows.Next() {
		var r store.ScheduledTaskRun
		if err := rows.Scan(&r.ID, &r.ScheduledTaskID, &r.SlotAt, &r.FiredAt, &r.Outcome,
			&r.Detail, &r.TicketID, &r.AgentInstanceID, &r.StructuredOutcome); err != nil {
			t.Fatalf("listRuns scan: %v", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("listRuns rows: %v", err)
	}
	return runs
}

// rereadTask reloads the task row by id.
func rereadTask(t *testing.T, pool *pgxpool.Pool, taskID pgtype.UUID) store.ScheduledTask {
	t.Helper()
	var task store.ScheduledTask
	if err := pool.QueryRow(context.Background(), `
		SELECT id, name, mode, schedule_expr, next_fire_at, paused, last_fired_at
		  FROM scheduled_tasks WHERE id = $1`, taskID,
	).Scan(&task.ID, &task.Name, &task.Mode, &task.ScheduleExpr,
		&task.NextFireAt, &task.Paused, &task.LastFiredAt); err != nil {
		t.Fatalf("rereadTask: %v", err)
	}
	return task
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("countRows %q: %v", query, err)
	}
	return n
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
}

func TestTickOnceAdvancesExactlyOneSlot(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	// Slot two hours overdue — the recovery-collapse claim shape.
	task := seedScheduledTask(t, q, deptID, "digest", ModeTicket, "daily@09:00", now.Add(-2*time.Hour))

	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired != 1 || skipped != 0 || deferred != 0 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want (1, 0, 0)", fired, skipped, deferred)
	}

	// Exactly one fired run row, anchored to the slot and the ticket.
	runs := listRuns(t, pool, task.ID)
	if len(runs) != 1 {
		t.Fatalf("run rows = %d, want 1", len(runs))
	}
	if runs[0].Outcome != OutcomeFired {
		t.Fatalf("run outcome = %q, want %q", runs[0].Outcome, OutcomeFired)
	}
	if !runs[0].TicketID.Valid {
		t.Fatal("fired run row has no ticket_id anchor (FR-201)")
	}
	if !runs[0].SlotAt.Time.Equal(now.Add(-2 * time.Hour)) {
		t.Fatalf("run slot_at = %v, want the claimed next_fire_at %v", runs[0].SlotAt.Time, now.Add(-2*time.Hour))
	}

	// The ticket landed in todo with the rendered (never-fired) objective.
	var objective, acceptance, columnSlug string
	if err := pool.QueryRow(ctx,
		`SELECT objective, acceptance_criteria, column_slug FROM tickets WHERE id = $1`,
		runs[0].TicketID,
	).Scan(&objective, &acceptance, &columnSlug); err != nil {
		t.Fatalf("read fired ticket: %v", err)
	}
	if columnSlug != "todo" {
		t.Fatalf("ticket column_slug = %q, want todo", columnSlug)
	}
	if objective != "Summarize activity since never." {
		t.Fatalf("rendered objective = %q", objective)
	}
	wantAcceptance := "Digest posted for the slot at " + now.Format(time.RFC3339) + "."
	if acceptance != wantAcceptance {
		t.Fatalf("rendered acceptance = %q, want %q", acceptance, wantAcceptance)
	}

	// The tickets INSERT trigger emitted the existing channel's outbox
	// row in the same tx (FR-200).
	if n := countRows(t, pool,
		`SELECT COUNT(*) FROM event_outbox WHERE channel = 'work.ticket.created.engineering.todo'`,
	); n != 1 {
		t.Fatalf("ticket-created outbox rows = %d, want 1", n)
	}

	// Advanced exactly one future slot (no backfill, FR-104), with
	// last_fired_at = this firing's timestamp (FR-107).
	expr, err := Parse(task.ScheduleExpr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.Equal(expr.Next(now)) {
		t.Fatalf("next_fire_at = %v, want exactly one future slot %v", reread.NextFireAt.Time, expr.Next(now))
	}
	if !reread.LastFiredAt.Valid || !reread.LastFiredAt.Time.Equal(now) {
		t.Fatalf("last_fired_at = %v (valid=%v), want %v", reread.LastFiredAt.Time, reread.LastFiredAt.Valid, now)
	}

	// Nothing due anymore: a second tick fires nothing (FR-109).
	fired, skipped, deferred, err = tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("second tickOnce: %v", err)
	}
	if fired+skipped+deferred != 0 {
		t.Fatalf("second tickOnce = (fired=%d, skipped=%d, deferred=%d), want all zero", fired, skipped, deferred)
	}
	if n := len(listRuns(t, pool, task.ID)); n != 1 {
		t.Fatalf("run rows after idle tick = %d, want 1", n)
	}
}

func TestTickOnceSkipsOverlapTicketMode(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	task := seedScheduledTask(t, q, deptID, "digest", ModeTicket, "daily@09:00", now.Add(-time.Hour))

	// Previous slot's ticket is still open (todo, not done).
	openTicket, err := q.InsertScheduledTicket(ctx, store.InsertScheduledTicketParams{
		DepartmentID:       deptID,
		Objective:          "previous slot's work",
		AcceptanceCriteria: func() *string { s := "still open"; return &s }(),
		RoleSlug:           "engineer",
	})
	if err != nil {
		t.Fatalf("insert open ticket: %v", err)
	}
	if _, err := q.InsertScheduledTaskRun(ctx, store.InsertScheduledTaskRunParams{
		ScheduledTaskID: task.ID,
		SlotAt:          pgtype.Timestamptz{Time: now.Add(-25 * time.Hour), Valid: true},
		Outcome:         OutcomeFired,
		TicketID:        openTicket.ID,
	}); err != nil {
		t.Fatalf("insert prior fired run: %v", err)
	}
	ticketsBefore := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID)

	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired != 0 || skipped != 1 || deferred != 0 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want (0, 1, 0)", fired, skipped, deferred)
	}

	// No second ticket (FR-202); typed run record; slot consumed.
	if n := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID); n != ticketsBefore {
		t.Fatalf("tickets = %d, want unchanged %d", n, ticketsBefore)
	}
	runs := listRuns(t, pool, task.ID)
	if len(runs) != 2 {
		t.Fatalf("run rows = %d, want 2", len(runs))
	}
	latest := runs[len(runs)-1]
	if latest.Outcome != OutcomeSkippedOverlap {
		t.Fatalf("latest run outcome = %q, want %q", latest.Outcome, OutcomeSkippedOverlap)
	}
	if latest.TicketID.Valid {
		t.Fatal("skipped_overlap run row carries a ticket_id, want NULL")
	}
	if latest.Detail == nil || !strings.Contains(*latest.Detail, "FR-202") {
		t.Fatalf("skipped_overlap detail = %v, want the FR-202 reason", latest.Detail)
	}

	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.After(now) {
		t.Fatalf("next_fire_at = %v, want advanced past %v", reread.NextFireAt.Time, now)
	}
	// A skipped slot is not a firing: last_fired_at untouched (FR-107).
	if reread.LastFiredAt.Valid {
		t.Fatalf("last_fired_at = %v, want NULL after a skipped slot", reread.LastFiredAt.Time)
	}
}

func TestTickOnceSkipsOverlapOneshotMode(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	task := seedScheduledTask(t, q, deptID, "probe", ModeOneshot, "every@30m", now.Add(-time.Minute))

	// Previous firing fired but not yet dispatched (agent_instance_id
	// NULL) — in-flight per the tick→dispatch-window predicate.
	if _, err := q.InsertScheduledTaskRun(ctx, store.InsertScheduledTaskRunParams{
		ScheduledTaskID: task.ID,
		SlotAt:          pgtype.Timestamptz{Time: now.Add(-31 * time.Minute), Valid: true},
		Outcome:         OutcomeFired,
	}); err != nil {
		t.Fatalf("insert prior fired run: %v", err)
	}

	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired != 0 || skipped != 1 || deferred != 0 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want (0, 1, 0)", fired, skipped, deferred)
	}

	// No oneshot due event lands for a skipped slot (FR-303).
	if n := countRows(t, pool,
		`SELECT COUNT(*) FROM event_outbox WHERE channel = $1`, ChannelOneshotDue,
	); n != 0 {
		t.Fatalf("oneshot outbox rows = %d, want 0", n)
	}
	runs := listRuns(t, pool, task.ID)
	if len(runs) != 2 {
		t.Fatalf("run rows = %d, want 2", len(runs))
	}
	latest := runs[len(runs)-1]
	if latest.Outcome != OutcomeSkippedOverlap {
		t.Fatalf("latest run outcome = %q, want %q", latest.Outcome, OutcomeSkippedOverlap)
	}
	if latest.Detail == nil || !strings.Contains(*latest.Detail, "FR-303") {
		t.Fatalf("skipped_overlap detail = %v, want the FR-303 reason", latest.Detail)
	}
	if reread := rereadTask(t, pool, task.ID); !reread.NextFireAt.Time.After(now) {
		t.Fatalf("next_fire_at = %v, want advanced past %v", reread.NextFireAt.Time, now)
	}
}

func TestTickOnceGateDeferredWritesEvidence(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	// Department weekly budget 0: every ticket-mode firing rejects.
	if _, err := pool.Exec(ctx,
		`UPDATE departments SET weekly_ticket_budget = 0 WHERE id = $1`, deptID,
	); err != nil {
		t.Fatalf("set weekly_ticket_budget: %v", err)
	}
	task := seedScheduledTask(t, q, deptID, "digest", ModeTicket, "daily@09:00", now.Add(-time.Hour))

	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired != 0 || skipped != 0 || deferred != 1 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want (0, 0, 1)", fired, skipped, deferred)
	}

	// No ticket; a gate_deferred run row with the typed reason.
	if n := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID); n != 0 {
		t.Fatalf("tickets = %d, want 0", n)
	}
	runs := listRuns(t, pool, task.ID)
	if len(runs) != 1 {
		t.Fatalf("run rows = %d, want 1", len(runs))
	}
	if runs[0].Outcome != OutcomeGateDeferred {
		t.Fatalf("run outcome = %q, want %q", runs[0].Outcome, OutcomeGateDeferred)
	}
	if runs[0].Detail == nil || !strings.Contains(*runs[0].Detail, "FR-402") {
		t.Fatalf("gate_deferred detail = %v, want the FR-402 reason", runs[0].Detail)
	}

	// throttle.FireDeptWeekly evidence landed in the same tx.
	if n := countRows(t, pool, fmt.Sprintf(
		`SELECT COUNT(*) FROM throttle_events
		  WHERE kind = '%s' AND (payload->>'department_id')::uuid = $1`,
		"dept_weekly_ticket_budget_exceeded"), deptID,
	); n != 1 {
		t.Fatalf("dept-weekly throttle_events rows = %d, want 1", n)
	}

	// Slot consumed without a firing: advanced, last_fired_at NULL
	// (ticket-mode gate_deferred is terminal for the slot).
	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.After(now) {
		t.Fatalf("next_fire_at = %v, want advanced past %v", reread.NextFireAt.Time, now)
	}
	if reread.LastFiredAt.Valid {
		t.Fatalf("last_fired_at = %v, want NULL after a deferred slot", reread.LastFiredAt.Time)
	}
}

func TestTickOncePausedTaskNotClaimed(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	due := now.Add(-time.Hour)
	task := seedScheduledTask(t, q, deptID, "digest", ModeTicket, "daily@09:00", due)
	if _, err := pool.Exec(ctx, `UPDATE scheduled_tasks SET paused = TRUE WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("pause task: %v", err)
	}

	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired+skipped+deferred != 0 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want all zero", fired, skipped, deferred)
	}

	// Nothing fired, nothing recorded as missed-pending-backfill
	// (FR-106), and the due slot is untouched.
	if n := len(listRuns(t, pool, task.ID)); n != 0 {
		t.Fatalf("run rows = %d, want 0", n)
	}
	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.Equal(due) {
		t.Fatalf("next_fire_at = %v, want untouched %v", reread.NextFireAt.Time, due)
	}
}

func TestTickOnceCorruptExprLogsAndSkips(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	due := now.Add(-time.Hour)
	task := seedScheduledTask(t, q, deptID, "digest", ModeTicket, "daily@09:00", due)
	// Corrupt the row out-of-band — unreachable through any authoring
	// surface (FR-105 validation); the tick loop's path is defensive.
	if _, err := pool.Exec(ctx,
		`UPDATE scheduled_tasks SET schedule_expr = '0 9 * * *' WHERE id = $1`, task.ID,
	); err != nil {
		t.Fatalf("corrupt schedule_expr: %v", err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, logger, now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired+skipped+deferred != 0 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want all zero", fired, skipped, deferred)
	}

	// Fires nothing, stays un-advanced for operator repair, logs at
	// error.
	if n := len(listRuns(t, pool, task.ID)); n != 0 {
		t.Fatalf("run rows = %d, want 0", n)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID); n != 0 {
		t.Fatalf("tickets = %d, want 0", n)
	}
	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.Equal(due) {
		t.Fatalf("next_fire_at = %v, want un-advanced %v", reread.NextFireAt.Time, due)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "corrupt schedule expression") {
		t.Fatalf("expected an error-level corrupt-expression log, got: %q", logged)
	}
}

// -----------------------------------------------------------------------
// M9 T017 — recovery collapse + pause/resume + zero idle
// -----------------------------------------------------------------------

func TestRecoveryCollapseFiresOnce(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	// Supervisor "down" across three hourly slots: next_fire_at sits
	// three intervals in the past (-3h; the -2h and -1h slots were
	// never written — the row only ever carries the earliest due slot).
	task := seedScheduledTask(t, q, deptID, "hourly-probe", ModeTicket, "every@1h", now.Add(-3*time.Hour))

	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if fired != 1 || skipped != 0 || deferred != 0 {
		t.Fatalf("tickOnce = (fired=%d, skipped=%d, deferred=%d), want (1, 0, 0)", fired, skipped, deferred)
	}

	// Exactly one run for three missed slots — collapse, not backfill
	// (FR-104). The single run is anchored to the claimed (earliest
	// missed) slot.
	runs := listRuns(t, pool, task.ID)
	if len(runs) != 1 {
		t.Fatalf("run rows = %d, want exactly 1 (no backfill, FR-104)", len(runs))
	}
	if runs[0].Outcome != OutcomeFired {
		t.Fatalf("run outcome = %q, want %q", runs[0].Outcome, OutcomeFired)
	}
	if !runs[0].SlotAt.Time.Equal(now.Add(-3 * time.Hour)) {
		t.Fatalf("run slot_at = %v, want the claimed slot %v", runs[0].SlotAt.Time, now.Add(-3*time.Hour))
	}

	// One firing means one ticket — intermediate slots produced nothing.
	if n := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID); n != 1 {
		t.Fatalf("tickets = %d, want exactly 1 (no backfill, FR-104)", n)
	}

	// Advanced to the single next future slot computed from now, not
	// to any of the stale intermediate slots.
	expr, err := Parse(task.ScheduleExpr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.Equal(expr.Next(now)) {
		t.Fatalf("next_fire_at = %v, want the single future slot %v", reread.NextFireAt.Time, expr.Next(now))
	}
	if !reread.NextFireAt.Time.After(now) {
		t.Fatalf("next_fire_at = %v, want strictly future of %v", reread.NextFireAt.Time, now)
	}

	// A follow-up tick at the same instant finds nothing due: the
	// collapse consumed every missed slot in one firing.
	fired, skipped, deferred, err = tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("second tickOnce: %v", err)
	}
	if fired+skipped+deferred != 0 {
		t.Fatalf("second tickOnce = (fired=%d, skipped=%d, deferred=%d), want all zero", fired, skipped, deferred)
	}
	if n := len(listRuns(t, pool, task.ID)); n != 1 {
		t.Fatalf("run rows after follow-up tick = %d, want still 1", n)
	}
}

func TestPauseResumeAdvanceOnly(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	// Paused across two daily slots: next_fire_at two days stale.
	task := seedScheduledTask(t, q, deptID, "daily-digest", ModeTicket, "daily@09:00", now.Add(-48*time.Hour))
	if _, err := pool.Exec(ctx, `UPDATE scheduled_tasks SET paused = TRUE WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("pause task: %v", err)
	}

	// Ticks during the pause claim nothing and record nothing — pausing
	// stops firings without recording missed slots (FR-106).
	for i := 0; i < 2; i++ {
		fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
		if err != nil {
			t.Fatalf("paused tickOnce %d: %v", i, err)
		}
		if fired+skipped+deferred != 0 {
			t.Fatalf("paused tickOnce %d = (fired=%d, skipped=%d, deferred=%d), want all zero", i, fired, skipped, deferred)
		}
	}
	if n := len(listRuns(t, pool, task.ID)); n != 0 {
		t.Fatalf("run rows during pause = %d, want 0 (FR-106: no missed-slot records)", n)
	}

	// Resume with the FR-106 advance-only recompute — the same
	// next-future-slot computation the dashboard resume action obtains
	// from POST /schedule/validate. The two stale slots are dropped.
	expr, err := Parse(task.ScheduleExpr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resumedNext := expr.Next(now)
	if _, err := pool.Exec(ctx,
		`UPDATE scheduled_tasks SET paused = FALSE, next_fire_at = $1 WHERE id = $2`,
		resumedNext, task.ID,
	); err != nil {
		t.Fatalf("resume task: %v", err)
	}

	// Post-resume tick: nothing due, no catch-up firing for the slots
	// missed while paused.
	fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
	if err != nil {
		t.Fatalf("post-resume tickOnce: %v", err)
	}
	if fired+skipped+deferred != 0 {
		t.Fatalf("post-resume tickOnce = (fired=%d, skipped=%d, deferred=%d), want all zero (no catch-up, FR-106)", fired, skipped, deferred)
	}
	if n := len(listRuns(t, pool, task.ID)); n != 0 {
		t.Fatalf("run rows after resume = %d, want 0", n)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID); n != 0 {
		t.Fatalf("tickets after resume = %d, want 0", n)
	}

	// next_fire_at is future-only; nothing fired, so last_fired_at
	// stays NULL (FR-107).
	reread := rereadTask(t, pool, task.ID)
	if !reread.NextFireAt.Time.Equal(resumedNext) {
		t.Fatalf("next_fire_at = %v, want the resumed future slot %v", reread.NextFireAt.Time, resumedNext)
	}
	if !reread.NextFireAt.Time.After(now) {
		t.Fatalf("next_fire_at = %v, want strictly future of %v", reread.NextFireAt.Time, now)
	}
	if reread.Paused {
		t.Fatal("task still paused after resume")
	}
	if reread.LastFiredAt.Valid {
		t.Fatalf("last_fired_at = %v, want NULL (no slot ever fired)", reread.LastFiredAt.Time)
	}
}

func TestZeroIdleCost(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := fixedNow()

	// Tasks exist in both modes but none are due — the steady idle
	// state between slots (SC-008 proxy).
	ticketTask := seedScheduledTask(t, q, deptID, "future-digest", ModeTicket, "daily@09:00", now.Add(time.Hour))
	oneshotTask := seedScheduledTask(t, q, deptID, "future-probe", ModeOneshot, "every@30m", now.Add(30*time.Minute))

	const nTicks = 5
	for i := 0; i < nTicks; i++ {
		fired, skipped, deferred, err := tickOnce(ctx, tickDeps(pool, discardLogger(), now))
		if err != nil {
			t.Fatalf("idle tickOnce %d: %v", i, err)
		}
		if fired+skipped+deferred != 0 {
			t.Fatalf("idle tickOnce %d = (fired=%d, skipped=%d, deferred=%d), want all zero (FR-109)", i, fired, skipped, deferred)
		}
	}

	// Zero runs, zero instances — and nothing queued that would spawn
	// later: no tickets, no oneshot-due outbox rows.
	if n := countRows(t, pool, `SELECT COUNT(*) FROM scheduled_task_runs`); n != 0 {
		t.Fatalf("scheduled_task_runs = %d, want 0 after %d idle ticks", n, nTicks)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM agent_instances`); n != 0 {
		t.Fatalf("agent_instances = %d, want 0 after %d idle ticks", n, nTicks)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID); n != 0 {
		t.Fatalf("tickets = %d, want 0 after %d idle ticks", n, nTicks)
	}
	if n := countRows(t, pool, `SELECT COUNT(*) FROM event_outbox WHERE channel = $1`, ChannelOneshotDue); n != 0 {
		t.Fatalf("oneshot outbox rows = %d, want 0 after %d idle ticks", n, nTicks)
	}

	// The idle ticks touched neither task's slot.
	for _, tc := range []struct {
		task store.ScheduledTask
		due  time.Time
	}{
		{ticketTask, now.Add(time.Hour)},
		{oneshotTask, now.Add(30 * time.Minute)},
	} {
		reread := rereadTask(t, pool, tc.task.ID)
		if !reread.NextFireAt.Time.Equal(tc.due) {
			t.Fatalf("task %q next_fire_at = %v, want untouched %v", tc.task.Name, reread.NextFireAt.Time, tc.due)
		}
	}
}

// -----------------------------------------------------------------------
// M9 T016 — golden-path suite bridge
// -----------------------------------------------------------------------

// TickOnce exposes the unexported tickOnce claim-and-fire transaction
// to the golden-path tests (golden_path_integration_test.go, package
// schedule_test). Those tests must live in the external test package:
// they drive the dispatcher into internal/spawn, and spawn imports
// schedule (oneshot.go), so an in-package test importing spawn is an
// import cycle the Go toolchain rejects ("import cycle not allowed in
// test"). Test-only surface — integration-tagged, no production caller.
func TickOnce(ctx context.Context, deps Deps) (fired, skipped, deferred int, err error) {
	return tickOnce(ctx, deps)
}
