//go:build chaos

// M9 T018 — chaos test for the tick loop's concurrent-claim
// single-firing invariant (FR-101). Two tickOnce transactions racing
// over one due task must land exactly one run row and one firing —
// the SKIP LOCKED discipline in ClaimDueScheduledTasks, pinned up
// front per the M8 double-fire lesson (plan §Integration, chaos, and
// regression test plan).
//
// DB-backed via the shared testdb container (the M8 chaos-test shape:
// real Postgres locking semantics, no live stack needed). Runs under
// `go test -tags=chaos ./internal/schedule/...`.
//
// Helpers carry a chaos prefix so a combined-tag build
// (-tags="integration chaos") does not collide with the
// integration-tagged helpers in integration_test.go.

package schedule

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chaosTickDeps builds the Deps tickOnce needs, with a deterministic
// clock shared by both racing goroutines.
func chaosTickDeps(pool *pgxpool.Pool, now time.Time) Deps {
	return Deps{
		Pool:         pool,
		Queries:      store.New(pool),
		Logger:       slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)),
		TickInterval: time.Second,
		Now:          func() time.Time { return now },
	}
}

// chaosSeedDueTask inserts a ticket-mode task whose slot is already
// due (validation is T004's concern, not the tick loop's).
func chaosSeedDueTask(t *testing.T, q *store.Queries, deptID pgtype.UUID, nextFireAt time.Time) store.ScheduledTask {
	t.Helper()
	task, err := q.InsertScheduledTask(context.Background(), store.InsertScheduledTaskParams{
		Name:                       "chaos-digest",
		DepartmentID:               deptID,
		RoleSlug:                   "engineer",
		Mode:                       ModeTicket,
		ScheduleExpr:               "daily@09:00",
		NextFireAt:                 pgtype.Timestamptz{Time: nextFireAt, Valid: true},
		ObjectiveTemplate:          "Summarize activity since {{last_fired_at}}.",
		AcceptanceCriteriaTemplate: "Digest posted for the slot at {{fire_at}}.",
	})
	if err != nil {
		t.Fatalf("chaosSeedDueTask: %v", err)
	}
	return task
}

// chaosCountRows runs a COUNT(*) query and returns the count.
func chaosCountRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("chaosCountRows %q: %v", query, err)
	}
	return n
}

// chaosTickResult carries one goroutine's tickOnce return values.
type chaosTickResult struct {
	fired, skipped, deferred int
	err                      error
}

// TestConcurrentClaimSingleFiring — FR-101's single-firing invariant
// under concurrent claim attempts, then a direct probe of the SKIP
// LOCKED discipline that enforces it.
//
// Phase 1: two goroutines run tickOnce concurrently against one due
// task from a shared start barrier. Whatever interleaving Postgres
// produces (second claim skips the locked row, or re-evaluates after
// the first commit and finds the slot advanced), exactly one run row
// and one firing must land.
//
// Phase 2: with the task row's lock held by an explicit FOR UPDATE
// transaction, tickOnce must return immediately with zero claims — a
// plain FOR UPDATE claim would block on the held lock and trip the
// deadline instead. Releasing the lock makes the row claimable again,
// proving the zero-claim tick skipped rather than consumed the slot.
func TestConcurrentClaimSingleFiring(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	q := store.New(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	due := now.Add(-time.Hour)

	task := chaosSeedDueTask(t, q, deptID, due)
	deps := chaosTickDeps(pool, now)

	// --- Phase 1: barrier-released concurrent ticks -------------------
	start := make(chan struct{})
	results := make(chan chaosTickResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			tickCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			fired, skipped, deferred, err := tickOnce(tickCtx, deps)
			results <- chaosTickResult{fired, skipped, deferred, err}
		}()
	}
	close(start)

	var total chaosTickResult
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent tickOnce %d: %v", i, r.err)
			}
			total.fired += r.fired
			total.skipped += r.skipped
			total.deferred += r.deferred
		case <-time.After(60 * time.Second):
			t.Fatal("concurrent tickOnce never returned; claim transaction wedged")
		}
	}
	if total.fired != 1 || total.skipped != 0 || total.deferred != 0 {
		t.Fatalf("concurrent ticks = (fired=%d, skipped=%d, deferred=%d), want exactly (1, 0, 0) across both (FR-101)",
			total.fired, total.skipped, total.deferred)
	}

	// Exactly one run row, fired, anchored to the slot and a ticket.
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM scheduled_task_runs WHERE scheduled_task_id = $1`, task.ID,
	); n != 1 {
		t.Fatalf("run rows = %d, want exactly 1 (single firing, FR-101)", n)
	}
	var outcome string
	var ticketID pgtype.UUID
	var slotAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT outcome, ticket_id, slot_at FROM scheduled_task_runs WHERE scheduled_task_id = $1`, task.ID,
	).Scan(&outcome, &ticketID, &slotAt); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	if outcome != OutcomeFired {
		t.Fatalf("run outcome = %q, want %q", outcome, OutcomeFired)
	}
	if !ticketID.Valid {
		t.Fatal("fired run row has no ticket_id anchor (FR-201)")
	}
	if !slotAt.Time.Equal(due) {
		t.Fatalf("run slot_at = %v, want the claimed slot %v", slotAt.Time, due)
	}

	// One firing means one ticket and one ticket-created outbox row —
	// the losing claim produced no duplicate work in its own tx.
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM tickets WHERE department_id = $1`, deptID,
	); n != 1 {
		t.Fatalf("tickets = %d, want exactly 1", n)
	}
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM event_outbox WHERE channel = 'work.ticket.created.engineering.todo'`,
	); n != 1 {
		t.Fatalf("ticket-created outbox rows = %d, want exactly 1", n)
	}

	// Advanced exactly one future slot — not two (a double claim would
	// have advanced twice or raced the same target; either way the row
	// must now carry the single next slot computed from now).
	expr, err := Parse(task.ScheduleExpr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var nextFireAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT next_fire_at FROM scheduled_tasks WHERE id = $1`, task.ID,
	).Scan(&nextFireAt); err != nil {
		t.Fatalf("read next_fire_at: %v", err)
	}
	if !nextFireAt.Time.Equal(expr.Next(now)) {
		t.Fatalf("next_fire_at = %v, want the single future slot %v", nextFireAt.Time, expr.Next(now))
	}

	// --- Phase 2: SKIP LOCKED discipline under a held row lock --------
	// Re-arm the slot, then hold the row's lock in an explicit tx.
	if _, err := pool.Exec(ctx,
		`UPDATE scheduled_tasks SET next_fire_at = $1 WHERE id = $2`, due, task.ID,
	); err != nil {
		t.Fatalf("re-arm slot: %v", err)
	}
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx,
		`SELECT 1 FROM scheduled_tasks WHERE id = $1 FOR UPDATE`, task.ID,
	); err != nil {
		t.Fatalf("acquire row lock: %v", err)
	}

	// The locked row must be skipped, not waited on: a bounded context
	// turns a blocking (non-SKIP LOCKED) claim into a hard failure.
	lockedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	fired, skipped, deferred, err := tickOnce(lockedCtx, deps)
	cancel()
	if err != nil {
		t.Fatalf("tickOnce under held lock: %v (a SKIP LOCKED claim returns immediately)", err)
	}
	if fired+skipped+deferred != 0 {
		t.Fatalf("tickOnce under held lock = (fired=%d, skipped=%d, deferred=%d), want all zero (locked row skipped)",
			fired, skipped, deferred)
	}
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM scheduled_task_runs WHERE scheduled_task_id = $1`, task.ID,
	); n != 1 {
		t.Fatalf("run rows after locked tick = %d, want still 1", n)
	}

	// Release the lock: the still-due row is claimable again — the
	// locked tick skipped the slot, it did not consume it. Phase 1's
	// ticket is still open (todo), so this claim records the slot as
	// skipped_overlap (FR-202) — what matters here is that it was
	// claimed at all.
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("release row lock: %v", err)
	}
	fired, skipped, deferred, err = tickOnce(ctx, deps)
	if err != nil {
		t.Fatalf("post-release tickOnce: %v", err)
	}
	if fired != 0 || skipped != 1 || deferred != 0 {
		t.Fatalf("post-release tickOnce = (fired=%d, skipped=%d, deferred=%d), want (0, 1, 0): the released row claims again",
			fired, skipped, deferred)
	}
	if n := chaosCountRows(t, pool,
		`SELECT COUNT(*) FROM scheduled_task_runs WHERE scheduled_task_id = $1`, task.ID,
	); n != 2 {
		t.Fatalf("run rows after release = %d, want 2", n)
	}
}
