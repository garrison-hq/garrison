//go:build integration || chaos

// Package testdb provides a shared Postgres harness for integration tests
// (T013). A single testcontainers-go postgres:17 container is booted lazily
// on the first Start(t) call and reused by every subsequent test in the
// same `go test` invocation; Ryuk cleans it up when the test process exits.
// Each test registers a t.Cleanup that TRUNCATEs the four M1 tables so
// state does not leak between tests.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	initOnce        sync.Once
	initErr         error
	sharedPool      *pgxpool.Pool
	sharedURL       string
	sharedContainer testcontainers.Container
)

// Start returns a migrated *pgxpool.Pool backed by a shared postgres:17
// container. The first call boots the container and applies every
// migration (M1 + M2.1); later calls reuse the same pool. Each Start
// call both TRUNCATEs eagerly (so tests are order-independent regardless
// of migration seeds) and registers a post-test TRUNCATE via t.Cleanup
// (so the next test still starts clean even if the pool grew new rows
// this run). M2.1 tables (agents, companies, ticket_transitions) are
// included so CASCADE semantics wipe the full working set.
func Start(t *testing.T) *pgxpool.Pool {
	t.Helper()
	initOnce.Do(func() { initErr = bootContainer() })
	if initErr != nil {
		t.Fatalf("testdb: init: %v", initErr)
	}
	truncate := func() {
		_, _ = sharedPool.Exec(context.Background(),
			"TRUNCATE agent_instances, event_outbox, tickets, ticket_transitions, agents, departments, companies RESTART IDENTITY CASCADE")
	}
	truncate()
	t.Cleanup(truncate)
	return sharedPool
}

// URL exposes the shared postgres connection string so tests that need to
// open their own pool (e.g. to exercise pgxpool-level behaviour) can do so
// without re-dialing through testcontainers.
func URL(t *testing.T) string {
	t.Helper()
	initOnce.Do(func() { initErr = bootContainer() })
	if initErr != nil {
		t.Fatalf("testdb: init: %v", initErr)
	}
	return sharedURL
}

// Container exposes the underlying testcontainers-go handle so chaos tests
// can Stop/Start the Postgres container to simulate outages. Callers that
// stop the container are responsible for restarting it before the test
// finishes — otherwise the shared harness is unusable by subsequent tests.
func Container(t *testing.T) testcontainers.Container {
	t.Helper()
	initOnce.Do(func() { initErr = bootContainer() })
	if initErr != nil {
		t.Fatalf("testdb: init: %v", initErr)
	}
	return sharedContainer
}

func bootContainer() error {
	ctx := context.Background()

	pgC, err := postgres.Run(ctx, "postgres:17",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return fmt.Errorf("run postgres container: %w", err)
	}

	url, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("connection string: %w", err)
	}

	if err := applyMigrations(ctx, url); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("pgxpool new: %w", err)
	}

	sharedPool = pool
	sharedURL = url
	sharedContainer = pgC
	return nil
}

// applyMigrations resolves the repo-root migrations/ directory from the
// source location of this file and drives goose against it via the pgx
// stdlib driver. Using a path rather than go:embed keeps this file free of
// build-time coupling to the migrations staging Makefile target used by
// cmd/supervisor.
func applyMigrations(ctx context.Context, url string) error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("runtime.Caller failed")
	}
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")

	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("sql.Open: %w", err)
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose.SetDialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("goose.UpContext: %w", err)
	}
	return nil
}
