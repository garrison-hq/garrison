//go:build integration || chaos || live_acceptance

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

	"github.com/jackc/pgx/v5/pgtype"
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

// SetAgentROPassword ALTERs the garrison_agent_ro role created by the
// M2.1 migration to use the supplied password. Tests that run real
// Claude + real pgmcp must call this with the same password they hand
// the supervisor via GARRISON_AGENT_RO_PASSWORD, otherwise pgmcp will
// fail to connect (the migration creates the role with LOGIN but no
// password — operators are expected to run the equivalent ALTER in
// production).
//
// Uses the shared pool directly (not via Start) so repeated helper
// calls do not re-TRUNCATE and wipe rows the caller just seeded.
func SetAgentROPassword(t *testing.T, password string) {
	t.Helper()
	initOnce.Do(func() { initErr = bootContainer() })
	if initErr != nil {
		t.Fatalf("testdb: init: %v", initErr)
	}
	if _, err := sharedPool.Exec(context.Background(),
		fmt.Sprintf(`ALTER ROLE garrison_agent_ro WITH PASSWORD '%s'`, password),
	); err != nil {
		t.Fatalf("SetAgentROPassword: %v", err)
	}
}

// SeedM21 inserts the M2.1 minimum working set: one company, one
// engineering department with the supplied workspace_path, and one
// active engineer agent row whose listens_for matches the supervisor's
// registered channel. Returns the engineering department ID so tests
// can compose tickets against it.
//
// The agent seed carries a non-trivial agent_md so checkHelloTxt-style
// length assertions (e.g. T010 integration test) have something to
// match. Model is pinned to the M2.1 default so the spawn argv carries
// the value operators expect in production logs.
func SeedM21(t *testing.T, workspacePath string) pgtype.UUID {
	t.Helper()
	pool := Start(t)
	ctx := context.Background()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'garrison test co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("SeedM21: insert company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 1, $2)
		RETURNING id`,
		companyID, workspacePath,
	).Scan(&deptID); err != nil {
		t.Fatalf("SeedM21: insert department: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
		VALUES (
			gen_random_uuid(), $1, 'engineer',
			'# Engineer (M2.1)\n\nIntegration-test seed body — the real agent_md ships via T003.',
			'claude-haiku-4-5-20251001',
			'[]'::jsonb, '[]'::jsonb,
			'["work.ticket.created.engineering.todo"]'::jsonb,
			NULL, 'active'
		)`,
		deptID,
	); err != nil {
		t.Fatalf("SeedM21: insert agent: %v", err)
	}
	return deptID
}

// SetAgentMempalacePassword — M2.2 equivalent of SetAgentROPassword for
// the garrison_agent_mempalace SELECT-only role. Tests that exercise the
// hygiene checker's dedicated connection must call this with the same
// password their test supervisor uses via GARRISON_AGENT_MEMPALACE_
// PASSWORD, otherwise the hygiene goroutine's dial fails.
func SetAgentMempalacePassword(t *testing.T, password string) {
	t.Helper()
	initOnce.Do(func() { initErr = bootContainer() })
	if initErr != nil {
		t.Fatalf("testdb: init: %v", initErr)
	}
	if _, err := sharedPool.Exec(context.Background(),
		fmt.Sprintf(`ALTER ROLE garrison_agent_mempalace WITH PASSWORD '%s'`, password),
	); err != nil {
		t.Fatalf("SetAgentMempalacePassword: %v", err)
	}
}

// SeedM22 inserts the M2.2 minimum working set: one company, one
// engineering department with the M2.2 4-column workflow, one engineer
// agent (palace_wing='wing_frontend_engineer', listens_for=
// created.engineering.in_dev), one qa-engineer agent (palace_wing=
// 'wing_qa_engineer', listens_for=transitioned.engineering.
// in_dev.qa_review). Returns both agent row IDs.
//
// Start's TRUNCATE-CASCADE wipes the migration's seed rows at test
// start, so SeedM22 re-INSERTs them. The agent_md fields are
// abbreviated integration-test bodies — tests that need the full
// T005 content read the committed files directly.
//
// workspacePath is the engineering department's workspace_path; typically
// a test-scoped temp directory.
func SeedM22(t *testing.T, workspacePath string) (engineerAgentID, qaEngineerAgentID pgtype.UUID) {
	t.Helper()
	pool := Start(t)
	ctx := context.Background()

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'garrison test co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("SeedM22: insert company: %v", err)
	}

	var deptID pgtype.UUID
	workflow := `{
	  "columns": [
	    {"slug":"todo","label":"To do","entry_from":["backlog"]},
	    {"slug":"in_dev","label":"In dev","entry_from":["todo"]},
	    {"slug":"qa_review","label":"QA review","entry_from":["in_dev"]},
	    {"slug":"done","label":"Done","entry_from":["qa_review"]}
	  ],
	  "transitions": {
	    "todo":["in_dev"], "in_dev":["qa_review"], "qa_review":["done"]
	  }
	}`
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path, workflow)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 1, $2, $3::jsonb)
		RETURNING id`,
		companyID, workspacePath, workflow,
	).Scan(&deptID); err != nil {
		t.Fatalf("SeedM22: insert department: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
		VALUES (
			gen_random_uuid(), $1, 'engineer',
			'# Engineer (M2.2 integration-test body)\n\nPlaceholder for real content from migrations/seed/engineer.md',
			'claude-haiku-4-5-20251001',
			'[]'::jsonb, '[]'::jsonb,
			'["work.ticket.created.engineering.in_dev"]'::jsonb,
			'wing_frontend_engineer', 'active'
		) RETURNING id`,
		deptID,
	).Scan(&engineerAgentID); err != nil {
		t.Fatalf("SeedM22: insert engineer: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (id, department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, palace_wing, status)
		VALUES (
			gen_random_uuid(), $1, 'qa-engineer',
			'# QA Engineer (M2.2 integration-test body)\n\nPlaceholder for real content from migrations/seed/qa-engineer.md',
			'claude-haiku-4-5-20251001',
			'[]'::jsonb, '[]'::jsonb,
			'["work.ticket.transitioned.engineering.in_dev.qa_review"]'::jsonb,
			'wing_qa_engineer', 'active'
		) RETURNING id`,
		deptID,
	).Scan(&qaEngineerAgentID); err != nil {
		t.Fatalf("SeedM22: insert qa-engineer: %v", err)
	}
	return engineerAgentID, qaEngineerAgentID
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
