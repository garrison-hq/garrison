//go:build integration

package spawn

// M9 T007 integration suite: SpawnOneshot's prep transaction against a
// real Postgres — gate defer (FR-400/FR-401), poll re-dispatch clearing
// gate_deferred back to fired (FR-401), and the origin instance row
// (agent_instances_exactly_one_origin: scheduled_task_run_id set,
// ticket_id NULL). The run branch rides the M1 fake-agent harness
// (deps.FakeAgentCmd) exactly like Spawn's own integration suites.

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// oneshotFixture is the seeded (company → department → scheduled task →
// run → outbox event) chain SpawnOneshot consumes.
type oneshotFixture struct {
	eventID   pgtype.UUID
	runID     pgtype.UUID
	taskID    pgtype.UUID
	deptID    pgtype.UUID
	companyID pgtype.UUID
}

// seedOneshot inserts the fixture chain. pauseUntilDelta sets
// companies.pause_until relative to now (nil leaves it NULL; a negative
// delta seeds an expired pause window). runOutcome seeds the run row's
// starting outcome ('fired' for fresh dispatches, 'gate_deferred' for
// the FR-401 retry path).
func seedOneshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pauseUntilDelta *time.Duration, runOutcome string) oneshotFixture {
	t.Helper()
	var fx oneshotFixture

	pauseSQL := "NULL"
	if pauseUntilDelta != nil {
		pauseSQL = fmt.Sprintf("NOW() + INTERVAL '%d seconds'", int((*pauseUntilDelta).Seconds()))
	}
	if err := pool.QueryRow(ctx, fmt.Sprintf(
		`INSERT INTO companies (id, name, pause_until) VALUES (gen_random_uuid(), 'oneshot-test-co', %s) RETURNING id`,
		pauseSQL,
	)).Scan(&fx.companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, $2)
		RETURNING id`,
		fx.companyID, t.TempDir(),
	).Scan(&fx.deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO scheduled_tasks (
			name, department_id, role_slug, mode, schedule_expr,
			next_fire_at, objective_template, acceptance_criteria_template, last_fired_at
		) VALUES ($1, $2, 'engineer', 'oneshot', 'every@30m',
			NOW() + INTERVAL '30 minutes',
			'Summarize activity since {{last_fired_at}}.',
			'Digest posted for the slot at {{fire_at}}.',
			NOW())
		RETURNING id`,
		t.Name(), fx.deptID,
	).Scan(&fx.taskID); err != nil {
		t.Fatalf("insert scheduled task: %v", err)
	}

	var detail *string
	if runOutcome == "gate_deferred" {
		d := "seeded prior gate defer"
		detail = &d
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO scheduled_task_runs (scheduled_task_id, slot_at, outcome, detail)
		VALUES ($1, NOW(), $2, $3)
		RETURNING id`,
		fx.taskID, runOutcome, detail,
	).Scan(&fx.runID); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	payload := fmt.Sprintf(
		`{"scheduled_task_run_id":"%s","role_slug":"engineer","department_id":"%s"}`,
		uuidString(fx.runID), uuidString(fx.deptID),
	)
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.scheduled.oneshot_due', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&fx.eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return fx
}

func oneshotDeps(pool *pgxpool.Pool, fakeCmd string) Deps {
	var defaultCost pgtype.Numeric
	_ = defaultCost.Scan("0.10")
	return Deps{
		Pool:              pool,
		Queries:           store.New(pool),
		Logger:            slog.New(slog.DiscardHandler),
		SubprocessTimeout: 30 * time.Second,
		FakeAgentCmd:      fakeCmd,
		Throttle: throttle.Deps{
			Pool:                pool,
			Logger:              slog.New(slog.DiscardHandler),
			DefaultSpawnCostUSD: defaultCost,
			RateLimitBackOff:    60 * time.Second,
			Now:                 time.Now,
		},
	}
}

type oneshotRunRow struct {
	Outcome         string
	Detail          *string
	AgentInstanceID pgtype.UUID
}

func readRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID pgtype.UUID) oneshotRunRow {
	t.Helper()
	var r oneshotRunRow
	if err := pool.QueryRow(ctx,
		`SELECT outcome, detail, agent_instance_id FROM scheduled_task_runs WHERE id = $1`, runID,
	).Scan(&r.Outcome, &r.Detail, &r.AgentInstanceID); err != nil {
		t.Fatalf("read run: %v", err)
	}
	return r
}

// TestSpawnOneshotGateDeferUpdatesRun — paused company → SpawnOneshot
// defers: run outcome gate_deferred with a detail, processed_at stays
// NULL (the poll fallback re-checks after the window, FR-401), and no
// agent_instances row lands.
func TestSpawnOneshotGateDeferUpdatesRun(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	pause := 120 * time.Second
	fx := seedOneshot(t, ctx, pool, &pause, "fired")

	deps := oneshotDeps(pool, "")
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("SpawnOneshot err = %v; want nil (defer is not an error)", err)
	}

	run := readRun(t, ctx, pool, fx.runID)
	if run.Outcome != "gate_deferred" {
		t.Errorf("run outcome = %q; want gate_deferred", run.Outcome)
	}
	if run.Detail == nil || *run.Detail == "" {
		t.Error("run detail should carry the defer reason; got empty")
	}
	if run.AgentInstanceID.Valid {
		t.Errorf("run.agent_instance_id should be NULL after defer; got %s", uuidString(run.AgentInstanceID))
	}

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, fx.eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if processed.Valid {
		t.Errorf("event_outbox.processed_at should remain NULL after defer; got %v", processed.Time)
	}

	var instances int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_instances WHERE department_id = $1`, fx.deptID,
	).Scan(&instances); err != nil {
		t.Fatalf("count agent_instances: %v", err)
	}
	if instances != 0 {
		t.Errorf("agent_instances count = %d; want 0 after gate defer", instances)
	}
}

// TestSpawnOneshotRetryAfterGateClearsToFired — a previously deferred
// run plus an expired pause window: the poll re-dispatch succeeds, the
// run outcome clears back to fired BEFORE the instance insert (FR-401 —
// gate_deferred is non-terminal for oneshot), and the origin instance
// row lands.
func TestSpawnOneshotRetryAfterGateClearsToFired(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	expired := -60 * time.Second
	fx := seedOneshot(t, ctx, pool, &expired, "gate_deferred")

	deps := oneshotDeps(pool, "/bin/echo oneshot-retry")
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("SpawnOneshot err = %v; want nil", err)
	}

	run := readRun(t, ctx, pool, fx.runID)
	if run.Outcome != "fired" {
		t.Errorf("run outcome = %q; want fired after successful re-dispatch", run.Outcome)
	}
	if run.Detail != nil {
		t.Errorf("run detail = %q; want NULL after clearing back to fired", *run.Detail)
	}
	if !run.AgentInstanceID.Valid {
		t.Fatal("run.agent_instance_id should be backfilled after the instance insert")
	}

	var originRunID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT scheduled_task_run_id FROM agent_instances WHERE id = $1`, run.AgentInstanceID,
	).Scan(&originRunID); err != nil {
		t.Fatalf("read agent_instances origin: %v", err)
	}
	if uuidString(originRunID) != uuidString(fx.runID) {
		t.Errorf("instance scheduled_task_run_id = %s; want %s", uuidString(originRunID), uuidString(fx.runID))
	}
}

// TestSpawnOneshotInsertsOriginInstance — the happy path's instance row
// carries the oneshot origin: scheduled_task_run_id set, ticket_id
// NULL, the agent_instances_exactly_one_origin CHECK satisfied, and the
// run row backfilled with the instance anchor (plan decision 5).
func TestSpawnOneshotInsertsOriginInstance(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	fx := seedOneshot(t, ctx, pool, nil, "fired")

	deps := oneshotDeps(pool, "/bin/echo oneshot-ok")
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("SpawnOneshot err = %v; want nil", err)
	}

	run := readRun(t, ctx, pool, fx.runID)
	if run.Outcome != "fired" {
		t.Errorf("run outcome = %q; want fired (instance terminal state is the completion surface)", run.Outcome)
	}
	if !run.AgentInstanceID.Valid {
		t.Fatal("run.agent_instance_id should be backfilled at spawn")
	}

	var (
		ticketID pgtype.UUID
		originID pgtype.UUID
		roleSlug string
		status   string
	)
	if err := pool.QueryRow(ctx, `
		SELECT ticket_id, scheduled_task_run_id, role_slug, status
		  FROM agent_instances WHERE id = $1`, run.AgentInstanceID,
	).Scan(&ticketID, &originID, &roleSlug, &status); err != nil {
		t.Fatalf("read agent_instances: %v", err)
	}
	if ticketID.Valid {
		t.Errorf("instance ticket_id = %s; want NULL for oneshot origin", uuidString(ticketID))
	}
	if uuidString(originID) != uuidString(fx.runID) {
		t.Errorf("instance scheduled_task_run_id = %s; want %s", uuidString(originID), uuidString(fx.runID))
	}
	if roleSlug != "engineer" {
		t.Errorf("instance role_slug = %q; want engineer", roleSlug)
	}
	if status != "succeeded" {
		t.Errorf("instance status = %q; want succeeded (fake agent exit 0)", status)
	}

	// Dedupe: a second dispatch of the same event is a no-op.
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("second SpawnOneshot err = %v; want nil", err)
	}
	var instances int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_instances WHERE department_id = $1`, fx.deptID,
	).Scan(&instances); err != nil {
		t.Fatalf("count agent_instances: %v", err)
	}
	if instances != 1 {
		t.Errorf("agent_instances count = %d; want 1 (LockEventForProcessing dedupe)", instances)
	}
}
