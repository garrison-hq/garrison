//go:build integration

package spawn

import (
	"context"
	"errors"
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

// seedM6Throttle inserts a (company, department, ticket, event_outbox)
// chain shaped to feed prepareSpawn. dailyBudgetUSD and pauseUntilDelta
// are optional — pass nil to leave the column NULL. Returns the event_id
// + the company_id so tests can assert against throttle_events.
func seedM6Throttle(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	dailyBudgetUSD *string,
	pauseUntilDelta *time.Duration,
	preloadCostUSD string,
) (eventID, companyID, ticketID, deptID pgtype.UUID) {
	t.Helper()

	// Wipe throttle_events so this test starts from zero — the testdb
	// truncate sweep covers companies CASCADE which transitively wipes
	// throttle_events too (verified above), but be explicit so a future
	// schema change that breaks the cascade fails the test loud.
	if _, err := pool.Exec(ctx, "TRUNCATE throttle_events RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate throttle_events: %v", err)
	}

	// Insert company with optional budget / pause_until.
	var inserts []string
	args := []any{"throttle-spawn-test-co"}
	cols := []string{"name"}
	values := []string{"$1"}
	if dailyBudgetUSD != nil {
		cols = append(cols, "daily_budget_usd")
		values = append(values, fmt.Sprintf("$%d", len(args)+1))
		args = append(args, *dailyBudgetUSD)
	}
	if pauseUntilDelta != nil {
		cols = append(cols, "pause_until")
		values = append(values, fmt.Sprintf("NOW() + INTERVAL '%d seconds'", int((*pauseUntilDelta).Seconds())))
	}
	for i := range cols {
		inserts = append(inserts, cols[i]+"="+values[i])
		_ = inserts // silence unused — we build the SQL via Sprintf below
	}
	stmt := fmt.Sprintf("INSERT INTO companies (id, %s) VALUES (gen_random_uuid(), %s) RETURNING id",
		joinComma(cols), joinComma(values))
	if err := pool.QueryRow(ctx, stmt, args...).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, '/tmp/throttle-spawn-test')
		RETURNING id`,
		companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'throttle gate test ticket', 'in_dev')
		RETURNING id`,
		deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	if preloadCostUSD != "" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_instances (id, ticket_id, department_id, role_slug, status, started_at, total_cost_usd)
			VALUES (gen_random_uuid(), $1, $2, 'engineer', 'succeeded', NOW() - INTERVAL '1 hour', $3::NUMERIC)
		`, ticketID, deptID, preloadCostUSD); err != nil {
			t.Fatalf("preload agent_instances: %v", err)
		}
	}

	payload := fmt.Sprintf(
		`{"ticket_id":"%s","department_id":"%s","column_slug":"in_dev"}`,
		uuidString(ticketID), uuidString(deptID),
	)
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.ticket.created.engineering.in_dev', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	return eventID, companyID, ticketID, deptID
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func depsForThrottleTest(pool *pgxpool.Pool) Deps {
	q := store.New(pool)
	var defaultCost pgtype.Numeric
	_ = defaultCost.Scan("0.10")
	return Deps{
		Pool:    pool,
		Queries: q,
		Logger:  slog.New(slog.DiscardHandler),
		Throttle: throttle.Deps{
			Pool:                pool,
			Logger:              slog.New(slog.DiscardHandler),
			DefaultSpawnCostUSD: defaultCost,
			RateLimitBackOff:    60 * time.Second,
			Now:                 time.Now,
		},
	}
}

// TestPrepareSpawn_DefersOnBudgetExceeded — company has daily_budget_usd
// set to $1.00 and a preloaded agent_instances row totaling $0.95 within
// the rolling-24h window. prepareSpawn should fire the budget-defer audit
// row and return ErrSpawnDeferred so Spawn() leaves event_outbox unprocessed.
func TestPrepareSpawn_DefersOnBudgetExceeded(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	budget := "1.00"
	eventID, companyID, _, _ := seedM6Throttle(t, ctx, pool, &budget, nil, "0.95")

	deps := depsForThrottleTest(pool)

	_, err := prepareSpawn(ctx, deps, eventID, "engineer")
	if !errors.Is(err, ErrSpawnDeferred) {
		t.Fatalf("prepareSpawn err = %v; want ErrSpawnDeferred", err)
	}

	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM throttle_events WHERE company_id = $1 AND kind = 'company_budget_exceeded'`,
		companyID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("count throttle_events: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("throttle_events count = %d; want 1", auditCount)
	}

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if processed.Valid {
		t.Errorf("event_outbox.processed_at should remain NULL after defer; got %v", processed.Time)
	}
}

// TestPrepareSpawn_DefersOnPause — company has pause_until set to a future
// timestamp (rate-limit-pause window). prepareSpawn should return
// ErrSpawnDeferred but NOT write a throttle_events row (the pause audit
// was already written by OnRateLimit when the rate-limit event landed —
// T008's responsibility, not the gate's).
func TestPrepareSpawn_DefersOnPause(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	pause := 60 * time.Second
	eventID, companyID, _, _ := seedM6Throttle(t, ctx, pool, nil, &pause, "")

	deps := depsForThrottleTest(pool)

	_, err := prepareSpawn(ctx, deps, eventID, "engineer")
	if !errors.Is(err, ErrSpawnDeferred) {
		t.Fatalf("prepareSpawn err = %v; want ErrSpawnDeferred", err)
	}

	// No audit row should land — the pause was set externally (OnRateLimit
	// wired in T008 writes the rate_limit_pause throttle_events row at the
	// moment the rate-limit event arrives).
	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM throttle_events WHERE company_id = $1`,
		companyID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("count throttle_events: %v", err)
	}
	if auditCount != 0 {
		t.Errorf("throttle_events count = %d; want 0 (pause's audit is OnRateLimit's job)", auditCount)
	}

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if processed.Valid {
		t.Errorf("event_outbox.processed_at should remain NULL after defer; got %v", processed.Time)
	}
}

// TestPrepareSpawn_AllowsWhenBothNull — company has neither daily_budget_usd
// nor pause_until set. prepareSpawn should proceed normally and return a
// populated spawnPrep with a fresh agent_instances row.
func TestPrepareSpawn_AllowsWhenBothNull(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	eventID, companyID, _, _ := seedM6Throttle(t, ctx, pool, nil, nil, "")

	deps := depsForThrottleTest(pool)

	prep, err := prepareSpawn(ctx, deps, eventID, "engineer")
	if err != nil {
		t.Fatalf("prepareSpawn err = %v; want nil", err)
	}
	if !prep.instanceID.Valid {
		t.Errorf("expected populated instanceID; got %+v", prep)
	}

	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM throttle_events WHERE company_id = $1`,
		companyID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("count throttle_events: %v", err)
	}
	if auditCount != 0 {
		t.Errorf("throttle_events count = %d; want 0", auditCount)
	}
}
