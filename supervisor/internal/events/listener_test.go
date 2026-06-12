//go:build integration

package events

// M9 T009 channel-registration pin: work.scheduled.oneshot_due is wired
// in cmd/supervisor as a BASE dispatcher channel routed to a handler
// invoking spawn.SpawnOneshot. These tests pin the two delivery paths
// against a real Postgres:
//
//   - LISTEN: the dotted channel name survives events.listen's
//     double-quoted LISTEN statement (M6 retro gotcha 3) and a
//     NotifyOneshotDue emission reaches the registered handler.
//   - Poll fallback: an unprocessed oneshot event_outbox row reaches
//     the same handler through pollOnce with no oneshot-specific poll
//     code — it is an ordinary outbox row routed by channel.
//
// White-box (package events) so listen/pollOnce are callable directly;
// the handler closes over real spawn deps in fake-agent mode, exactly
// the shape main.go's buildDispatcherWithExtras constructs.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/schedule"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// oneshotDueFixture is the seeded company → department → scheduled task
// → fired run → unprocessed outbox row chain a dispatched oneshot event
// consumes (mirrors internal/spawn's T007 fixture).
type oneshotDueFixture struct {
	eventID pgtype.UUID
	runID   pgtype.UUID
	deptID  pgtype.UUID
}

// seedOneshotDue inserts the fixture chain with no gate obstacles
// (no company pause, no budgets) so SpawnOneshot's fake-agent run
// completes and the event reaches processed_at.
func seedOneshotDue(t *testing.T, ctx context.Context, pool *pgxpool.Pool) oneshotDueFixture {
	t.Helper()
	var fx oneshotDueFixture

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'oneshot-dispatch-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, $2)
		RETURNING id`,
		companyID, t.TempDir(),
	).Scan(&fx.deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}

	var taskID pgtype.UUID
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
	).Scan(&taskID); err != nil {
		t.Fatalf("insert scheduled task: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO scheduled_task_runs (scheduled_task_id, slot_at, outcome)
		VALUES ($1, NOW(), 'fired')
		RETURNING id`,
		taskID,
	).Scan(&fx.runID); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	payload := fmt.Sprintf(
		`{"scheduled_task_run_id":"%s","role_slug":"engineer","department_id":"%s"}`,
		formatUUID(fx.runID), formatUUID(fx.deptID),
	)
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.scheduled.oneshot_due', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&fx.eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return fx
}

// oneshotSpawnDeps builds the fake-agent spawn.Deps the handler closes
// over — the minimal shape internal/spawn's own T007 integration suite
// uses.
func oneshotSpawnDeps(pool *pgxpool.Pool) spawn.Deps {
	var defaultCost pgtype.Numeric
	_ = defaultCost.Scan("0.10")
	return spawn.Deps{
		Pool:              pool,
		Queries:           store.New(pool),
		Logger:            slog.New(slog.DiscardHandler),
		SubprocessTimeout: 30 * time.Second,
		FakeAgentCmd:      "/bin/echo oneshot-dispatch-ok",
		Throttle: throttle.Deps{
			Pool:                pool,
			Logger:              slog.New(slog.DiscardHandler),
			DefaultSpawnCostUSD: defaultCost,
			RateLimitBackOff:    60 * time.Second,
			Now:                 time.Now,
		},
	}
}

// assertOneshotSpawned asserts the spawn side effects landed: the run
// row backfilled with its origin instance and the outbox row marked
// processed (the handler's SpawnOneshot ran to terminal commit).
func assertOneshotSpawned(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fx oneshotDueFixture) {
	t.Helper()
	var (
		outcome    string
		instanceID pgtype.UUID
	)
	if err := pool.QueryRow(ctx,
		`SELECT outcome, agent_instance_id FROM scheduled_task_runs WHERE id = $1`, fx.runID,
	).Scan(&outcome, &instanceID); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if outcome != "fired" {
		t.Errorf("run outcome = %q; want fired", outcome)
	}
	if !instanceID.Valid {
		t.Error("run.agent_instance_id should be backfilled after the dispatched spawn")
	}

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, fx.eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Error("event_outbox.processed_at should be set after the dispatched spawn")
	}
}

// TestOneshotChannelDispatchesToSpawn — emit NotifyOneshotDue against a
// live LISTEN loop whose dispatcher registers work.scheduled.oneshot_due
// as a base channel (the main.go wiring shape) and assert the handler is
// invoked with the outbox row's event id and the spawn side effects land.
func TestOneshotChannelDispatchesToSpawn(t *testing.T) {
	pool := testdb.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fx := seedOneshotDue(t, ctx, pool)
	deps := oneshotSpawnDeps(pool)

	var (
		once    sync.Once
		mu      sync.Mutex
		got     pgtype.UUID
		handled = make(chan struct{})
	)
	handler := func(hctx context.Context, eventID pgtype.UUID) error {
		err := spawn.SpawnOneshot(hctx, deps, eventID)
		once.Do(func() {
			mu.Lock()
			got = eventID
			mu.Unlock()
			close(handled)
		})
		return err
	}
	dispatcher := NewDispatcher(map[string]Handler{schedule.ChannelOneshotDue: handler})

	conn, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatalf("dial listen conn: %v", err)
	}
	listenDone := make(chan error, 1)
	go func() { listenDone <- listen(ctx, conn, dispatcher, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() {
		cancel()
		<-listenDone
		_ = conn.Close(context.Background())
	})

	// The first notify races the goroutine's LISTEN statements, so emit
	// until the handler observes a dispatch. Re-dispatch is safe: the
	// dispatcher dedupes in-flight ids and SpawnOneshot dedupes
	// processed events via LockEventForProcessing.
	q := store.New(pool)
	notify := fmt.Sprintf(
		`{"event_id":"%s","scheduled_task_run_id":"%s","role_slug":"engineer","department_id":"%s"}`,
		formatUUID(fx.eventID), formatUUID(fx.runID), formatUUID(fx.deptID),
	)
	deadline := time.After(15 * time.Second)
emit:
	for {
		if err := q.NotifyOneshotDue(ctx, notify); err != nil {
			t.Fatalf("NotifyOneshotDue: %v", err)
		}
		select {
		case <-handled:
			break emit
		case <-deadline:
			t.Fatal("timed out waiting for the oneshot channel to dispatch")
		case <-time.After(150 * time.Millisecond):
		}
	}

	mu.Lock()
	gotID := formatUUID(got)
	mu.Unlock()
	if gotID != formatUUID(fx.eventID) {
		t.Errorf("handler invoked with event_id %s; want %s", gotID, formatUUID(fx.eventID))
	}
	assertOneshotSpawned(t, ctx, pool, fx)
}

// TestPollFallbackPicksUpOneshotEvents — no NOTIFY is ever emitted: the
// unprocessed work.scheduled.oneshot_due outbox row must reach the
// registered handler through the ordinary pollOnce path, with no
// oneshot-specific poll code in existence to invoke.
func TestPollFallbackPicksUpOneshotEvents(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	fx := seedOneshotDue(t, ctx, pool)
	deps := oneshotSpawnDeps(pool)

	var got pgtype.UUID
	handler := func(hctx context.Context, eventID pgtype.UUID) error {
		got = eventID
		return spawn.SpawnOneshot(hctx, deps, eventID)
	}
	dispatcher := NewDispatcher(map[string]Handler{schedule.ChannelOneshotDue: handler})

	q := store.New(pool)
	n, err := pollOnce(ctx, q, nil, dispatcher)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("pollOnce dispatched %d events; want 1", n)
	}
	if formatUUID(got) != formatUUID(fx.eventID) {
		t.Errorf("handler invoked with event_id %s; want %s", formatUUID(got), formatUUID(fx.eventID))
	}
	assertOneshotSpawned(t, ctx, pool, fx)

	// The processed row must not re-dispatch on the next poll.
	n, err = pollOnce(ctx, q, nil, dispatcher)
	if err != nil {
		t.Fatalf("second pollOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("second pollOnce dispatched %d events; want 0 (row processed)", n)
	}
}
