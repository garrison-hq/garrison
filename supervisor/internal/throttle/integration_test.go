//go:build integration

package throttle_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// numericFromString builds a pgtype.Numeric from a decimal string at
// NUMERIC(10,2) precision. Mirrors how spawn.parseCostToNumeric writes
// numerics today; we want the same shape here so the integration tests
// roundtrip the same way the spawn-prep gate will.
func numericFromString(t testing.TB, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("numericFromString(%q): %v", s, err)
	}
	return n
}

// numericFromCents builds a NUMERIC(10,2) from an integer-cents value.
// Convenience for tests that compose values without parsing strings.
func numericFromCents(cents int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(cents), Exp: -2, Valid: true}
}

// listenForNotify spawns a single LISTEN connection on the supplied
// channel and returns a chan that yields each payload as it arrives.
// The connection lifetime is bounded by t.Cleanup.
func listenForNotify(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channel string) <-chan string {
	t.Helper()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	if _, err := conn.Exec(ctx, `LISTEN "`+channel+`"`); err != nil {
		conn.Release()
		t.Fatalf("LISTEN %s: %v", channel, err)
	}
	out := make(chan string, 4)
	doneCh := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-doneCh:
				return
			default:
			}
			waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			notif, err := conn.Conn().WaitForNotification(waitCtx)
			cancel()
			if err != nil {
				return
			}
			out <- notif.Payload
		}
	}()
	t.Cleanup(func() {
		close(doneCh)
		conn.Release()
	})
	return out
}

// truncateAll wipes every M2.x table this package touches so each test
// starts from a clean slate. testdb.Start's per-test TRUNCATE doesn't
// include throttle_events (it's an M6 addition), so we do it here.
func truncateAll(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, "TRUNCATE throttle_events RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate throttle_events: %v", err)
	}
}

func TestSpawnDeferredOnBudgetExceeded(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	truncateAll(t, ctx, pool)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO companies (id, name, daily_budget_usd) VALUES (gen_random_uuid(), 'test-co', 1.00) RETURNING id
	`).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'engineering', 1, '/tmp') RETURNING id
	`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}
	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'preload', 'in_dev') RETURNING id
	`, deptID).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_instances (id, ticket_id, department_id, role_slug, status, started_at, total_cost_usd)
		VALUES (gen_random_uuid(), $1, $2, 'engineer', 'succeeded', NOW() - INTERVAL '1 hour', 0.95)
	`, ticketID, deptID); err != nil {
		t.Fatalf("preload agent_instances: %v", err)
	}

	q := store.New(pool)
	deps := throttle.Deps{
		Pool:                pool,
		Logger:              slog.Default(),
		DefaultSpawnCostUSD: numericFromCents(10), // $0.10
		RateLimitBackOff:    60 * time.Second,
		Now:                 time.Now,
	}
	d, err := throttle.Check(ctx, deps, q, companyID)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatalf("expected defer; got Decision=%+v", d)
	}
	if d.Kind != throttle.KindCompanyBudgetExceeded {
		t.Errorf("Kind = %q; want %q", d.Kind, throttle.KindCompanyBudgetExceeded)
	}

	notifyCh := listenForNotify(t, ctx, pool, throttle.ChannelThrottleEvent)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		qtx := store.New(tx)
		return throttle.FireBudgetDefer(ctx, qtx, companyID,
			numericFromString(t, "0.95"), numericFromString(t, "0.10"), numericFromString(t, "1.00"))
	}); err != nil {
		t.Fatalf("FireBudgetDefer: %v", err)
	}

	rows, err := q.ListThrottleEventsByCompany(ctx, store.ListThrottleEventsByCompanyParams{
		CompanyID: companyID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListThrottleEventsByCompany: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if rows[0].Kind != throttle.KindCompanyBudgetExceeded {
		t.Errorf("kind = %q; want %q", rows[0].Kind, throttle.KindCompanyBudgetExceeded)
	}

	select {
	case body := <-notifyCh:
		var decoded map[string]string
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Errorf("decode notify body: %v", err)
		}
		if decoded["kind"] != throttle.KindCompanyBudgetExceeded {
			t.Errorf("notify kind = %q", decoded["kind"])
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("notify never arrived")
	}
}

func TestSpawnAllowedAfterBudgetWindowExpires(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	truncateAll(t, ctx, pool)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO companies (id, name, daily_budget_usd) VALUES (gen_random_uuid(), 'test-co', 1.00) RETURNING id
	`).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'engineering', 1, '/tmp') RETURNING id
	`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}
	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'old', 'done') RETURNING id
	`, deptID).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_instances (id, ticket_id, department_id, role_slug, status, started_at, total_cost_usd)
		VALUES (gen_random_uuid(), $1, $2, 'engineer', 'succeeded', NOW() - INTERVAL '25 hours', 0.95)
	`, ticketID, deptID); err != nil {
		t.Fatalf("preload: %v", err)
	}
	q := store.New(pool)
	deps := throttle.Deps{
		Pool:                pool,
		Logger:              slog.Default(),
		DefaultSpawnCostUSD: numericFromCents(10),
		RateLimitBackOff:    60 * time.Second,
		Now:                 time.Now,
	}
	d, err := throttle.Check(ctx, deps, q, companyID)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Errorf("25h-old preload should not count toward 24h sum; got %+v", d)
	}
}

func TestSpawnDeferredDuringRateLimitPause(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	truncateAll(t, ctx, pool)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO companies (id, name, pause_until) VALUES (gen_random_uuid(), 'test-co', NOW() + INTERVAL '60 seconds') RETURNING id
	`).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	q := store.New(pool)
	deps := throttle.Deps{
		Pool:                pool,
		Logger:              slog.Default(),
		DefaultSpawnCostUSD: numericFromCents(5),
		RateLimitBackOff:    60 * time.Second,
		Now:                 time.Now,
	}
	d, err := throttle.Check(ctx, deps, q, companyID)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d.Allowed {
		t.Fatalf("expected defer during pause; got %+v", d)
	}
	if d.Kind != throttle.KindRateLimitPause {
		t.Errorf("Kind = %q; want %q", d.Kind, throttle.KindRateLimitPause)
	}
}

func TestSpawnAllowedAfterPauseExpires(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	truncateAll(t, ctx, pool)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO companies (id, name, pause_until) VALUES (gen_random_uuid(), 'test-co', NOW() - INTERVAL '60 seconds') RETURNING id
	`).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	q := store.New(pool)
	deps := throttle.Deps{
		Pool:                pool,
		Logger:              slog.Default(),
		DefaultSpawnCostUSD: numericFromCents(5),
		RateLimitBackOff:    60 * time.Second,
		Now:                 time.Now,
	}
	d, err := throttle.Check(ctx, deps, q, companyID)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Allowed {
		t.Errorf("expected allow after pause expires; got %+v", d)
	}
}
