//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Goose version numbers bracketing the M9 migration. preM9Version is the
// last migration before 20260610000002_m9_scheduled_wakeups.sql; the test
// migrates up to it, seeds legacy-shape rows, then applies M9 on top so
// the new agent_instances_exactly_one_origin CHECK is validated against
// genuinely pre-existing data (not a fresh empty table).
const (
	preM9Version = 20260610000001
	m9Version    = 20260610000002
)

// TestM9MigrationRoundtrip — T001 completion gate.
//
//  1. Boot a fresh postgres:17 container (NOT the shared testdb harness:
//     this test needs to seed rows BEFORE the M9 migration applies, and
//     the shared container boots at head).
//  2. goose up-to the pre-M9 version; seed a legacy agent_instances row
//     (ticket_id NOT NULL — the only shape that exists pre-M9).
//  3. goose up to head → the M9 Up applies; ADD CONSTRAINT validates
//     existing rows, so success here proves pre-existing rows satisfy
//     the new exactly-one-origin CHECK. Assert it explicitly too.
//  4. Fingerprint the schema, goose down-to back past M9, goose up
//     again, fingerprint again: apply → rollback → apply must be
//     byte-for-byte stable.
func TestM9MigrationRoundtrip(t *testing.T) {
	ctx := context.Background()

	pgC, err := postgres.Run(ctx, "postgres:17",
		postgres.WithDatabase("m9roundtrip"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("run postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(context.Background()) })

	url, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrationsDir := repoMigrationsDir(t)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}

	// (2) Migrate to the world as it was just before M9, then seed a
	// legacy ticket-anchored agent_instances row.
	if err := goose.UpToContext(ctx, db, migrationsDir, preM9Version); err != nil {
		t.Fatalf("goose up-to %d: %v", preM9Version, err)
	}
	seedLegacyAgentInstance(ctx, t, db)

	// (3) Apply M9 on top of the seeded data. ADD CONSTRAINT validates
	// all existing rows, so an error here means pre-M9 rows violate the
	// new CHECK.
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		t.Fatalf("goose up (M9 apply over legacy rows): %v", err)
	}
	assertLegacyRowsSatisfyOriginCheck(ctx, t, db)

	fpFirst := schemaFingerprint(ctx, t, db)

	// (4) Roundtrip: down past M9, back up, fingerprint must not move.
	if err := goose.DownToContext(ctx, db, migrationsDir, preM9Version); err != nil {
		t.Fatalf("goose down-to %d (M9 rollback): %v", preM9Version, err)
	}

	// Post-rollback sanity: the M9 tables are gone and ticket_id is
	// NOT NULL again.
	var m9TableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public'
		    AND table_name IN ('scheduled_tasks', 'scheduled_task_runs')`,
	).Scan(&m9TableCount); err != nil {
		t.Fatalf("post-rollback table probe: %v", err)
	}
	if m9TableCount != 0 {
		t.Fatalf("M9 tables survived rollback: %d remaining", m9TableCount)
	}

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		t.Fatalf("goose up (M9 re-apply): %v", err)
	}
	fpSecond := schemaFingerprint(ctx, t, db)

	if fpFirst != fpSecond {
		t.Fatalf("schema fingerprint changed across apply → rollback → apply:\n--- first ---\n%s\n--- second ---\n%s", fpFirst, fpSecond)
	}
}

// repoMigrationsDir resolves the repo-root migrations/ directory from this
// file's source location (same approach as internal/testdb).
func repoMigrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
}

// seedLegacyAgentInstance inserts the minimum pre-M9 working set: one
// department, one ticket (whose INSERT trigger needs the department
// slug), and one agent_instances row anchored to that ticket — the only
// agent_instances shape that exists before M9.
func seedLegacyAgentInstance(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var deptID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO departments (slug, name, concurrency_cap)
		VALUES ('m9-roundtrip', 'M9 roundtrip', 1)
		RETURNING id`,
	).Scan(&deptID); err != nil {
		t.Fatalf("seed department: %v", err)
	}
	var ticketID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO tickets (department_id, objective)
		VALUES ($1, 'pre-M9 legacy ticket')
		RETURNING id`,
		deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_instances (department_id, ticket_id, status)
		VALUES ($1, $2, 'finished')`,
		deptID, ticketID,
	); err != nil {
		t.Fatalf("seed legacy agent_instance: %v", err)
	}
}

// assertLegacyRowsSatisfyOriginCheck verifies every pre-existing
// agent_instances row satisfies the new exactly-one-origin CHECK
// (ticket_id NOT NULL, scheduled_task_run_id NULL ⇒ sum = 1), and that
// the constraint itself landed validated (not NOT VALID).
func assertLegacyRowsSatisfyOriginCheck(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var total, satisfying int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE (ticket_id IS NOT NULL)::int
		                            + (scheduled_task_run_id IS NOT NULL)::int = 1)
		  FROM agent_instances`,
	).Scan(&total, &satisfying); err != nil {
		t.Fatalf("origin-check probe: %v", err)
	}
	if total == 0 {
		t.Fatal("expected at least one pre-existing agent_instances row")
	}
	if satisfying != total {
		t.Fatalf("%d of %d pre-existing agent_instances rows violate the exactly-one-origin CHECK", total-satisfying, total)
	}
	var validated bool
	if err := db.QueryRowContext(ctx, `
		SELECT convalidated FROM pg_constraint
		 WHERE conname = 'agent_instances_exactly_one_origin'`,
	).Scan(&validated); err != nil {
		t.Fatalf("constraint validated probe: %v", err)
	}
	if !validated {
		t.Fatal("agent_instances_exactly_one_origin landed NOT VALID; pre-existing rows were not checked")
	}
}

// schemaFingerprint renders a deterministic, sorted text dump of the
// columns, indexes, constraints, and dashboard grants for every table the
// M9 migration touches. Two fingerprints being equal means apply →
// rollback → apply reproduced the schema exactly.
func schemaFingerprint(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	const q = `
SELECT 'column: ' || table_name || '.' || column_name
       || ' type=' || data_type
       || ' nullable=' || is_nullable
       || ' default=' || COALESCE(column_default, '<none>') AS line
  FROM information_schema.columns
 WHERE table_schema = 'public'
   AND table_name IN ('scheduled_tasks', 'scheduled_task_runs',
                      'agent_instances', 'chat_mutation_audit')
UNION ALL
SELECT 'index: ' || indexname || ' def=' || indexdef
  FROM pg_indexes
 WHERE schemaname = 'public'
   AND tablename IN ('scheduled_tasks', 'scheduled_task_runs',
                     'agent_instances', 'chat_mutation_audit')
UNION ALL
SELECT 'constraint: ' || conrelid::regclass::text || '.' || conname
       || ' def=' || pg_get_constraintdef(oid)
  FROM pg_constraint
 WHERE conrelid <> 0
   AND conrelid::regclass::text IN ('scheduled_tasks', 'scheduled_task_runs',
                                    'agent_instances', 'chat_mutation_audit')
UNION ALL
SELECT 'grant: ' || grantee || ' ' || privilege_type || ' ON ' || table_name
  FROM information_schema.role_table_grants
 WHERE table_schema = 'public'
   AND grantee = 'garrison_dashboard_app'
   AND table_name IN ('scheduled_tasks', 'scheduled_task_runs')
ORDER BY 1`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("fingerprint query: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("fingerprint scan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("fingerprint rows: %v", err)
	}
	return strings.Join(lines, "\n")
}
