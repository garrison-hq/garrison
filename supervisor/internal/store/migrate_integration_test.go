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

// Goose version numbers bracketing the M9, M10, and M11 migrations. Each
// preXVersion is the last applied migration before the named version; the
// roundtrip tests migrate up to preXVersion, seed legacy rows, then apply the
// target migration to prove pre-existing rows satisfy the new CHECKs.
const (
	preM9Version  = 20260610000001
	m9Version     = 20260610000002
	preM10Version = 20260610000002
	m10Version    = 20260612000000
	preM11Version = 20260612000000
	m11Version    = 20260612000001
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

// TestM10MigrationRoundtrip — T002 completion gate.
//
//  1. Boot a fresh postgres:17 container (NOT the shared testdb harness:
//     this test seeds rows before the M10 migration applies so the
//     throttle_events_kind_check extension is validated against real data).
//  2. goose up-to the pre-M10 version (M9 head); seed throttle_events rows
//     using the three M8/M9 kinds {company_budget_exceeded, rate_limit_pause,
//     dept_weekly_ticket_budget_exceeded} to prove pre-existing rows satisfy
//     the extended CHECK after M10 lands.
//  3. goose up to head → the M10 Up applies; the CHECK extension validates
//     existing rows, so success here proves pre-M9 rows satisfy the new
//     four-value CHECK.
//  4. Fingerprint the schema, goose down past M10, goose up again,
//     fingerprint again: apply → rollback → apply must be byte-for-byte stable.
func TestM10MigrationRoundtrip(t *testing.T) {
	ctx := context.Background()

	pgC, err := postgres.Run(ctx, "postgres:17",
		postgres.WithDatabase("m10roundtrip"),
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

	// (2) Migrate to M9 head, then seed throttle_events rows using the
	// three pre-M10 kinds so we can prove the extended CHECK still accepts them.
	if err := goose.UpToContext(ctx, db, migrationsDir, preM10Version); err != nil {
		t.Fatalf("goose up-to %d (pre-M10): %v", preM10Version, err)
	}
	seedPreM10ThrottleEvents(ctx, t, db)

	// (3) Apply M10 on top of the seeded data. The CHECK extension validates
	// all existing rows, so an error here means pre-M10 rows violate the
	// extended four-value CHECK.
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		t.Fatalf("goose up (M10 apply over legacy rows): %v", err)
	}
	assertPreM10ThrottleEventsSatisfyExtendedCheck(ctx, t, db)

	// Assert ingress_deliveries table exists with the expected columns.
	assertIngressDeliveriesTable(ctx, t, db)

	fpFirst := m10SchemaFingerprint(ctx, t, db)

	// (4) Roundtrip: down past M10, back up, fingerprint must not move.
	if err := goose.DownToContext(ctx, db, migrationsDir, preM10Version); err != nil {
		t.Fatalf("goose down-to %d (M10 rollback): %v", preM10Version, err)
	}

	// Post-rollback sanity: ingress_deliveries is gone.
	var tableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public'
		    AND table_name = 'ingress_deliveries'`,
	).Scan(&tableCount); err != nil {
		t.Fatalf("post-rollback table probe: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("ingress_deliveries survived M10 rollback")
	}

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		t.Fatalf("goose up (M10 re-apply): %v", err)
	}
	fpSecond := m10SchemaFingerprint(ctx, t, db)

	if fpFirst != fpSecond {
		t.Fatalf("M10 schema fingerprint changed across apply → rollback → apply:\n--- first ---\n%s\n--- second ---\n%s", fpFirst, fpSecond)
	}
}

// seedPreM10ThrottleEvents inserts throttle_events rows using the three
// pre-M10 kinds so the M10 migration's CHECK extension is validated against
// real data. Requires a company row (seeded here) since throttle_events has
// a company_id FK.
func seedPreM10ThrottleEvents(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var companyID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM companies LIMIT 1`,
	).Scan(&companyID); err != nil {
		// No company row exists; insert one.
		if err := db.QueryRowContext(ctx, `
			INSERT INTO companies (name, daily_budget_usd)
			VALUES ('M10 roundtrip', 1.00)
			RETURNING id`,
		).Scan(&companyID); err != nil {
			t.Fatalf("seed company: %v", err)
		}
	}
	for _, kind := range []string{
		"company_budget_exceeded",
		"rate_limit_pause",
		"dept_weekly_ticket_budget_exceeded",
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO throttle_events (company_id, kind, payload)
			VALUES ($1, $2, '{}')`,
			companyID, kind,
		); err != nil {
			t.Fatalf("seed throttle_event kind=%s: %v", kind, err)
		}
	}
}

// assertPreM10ThrottleEventsSatisfyExtendedCheck verifies every pre-M10
// throttle_events row satisfies the new four-value CHECK after M10 lands.
func assertPreM10ThrottleEventsSatisfyExtendedCheck(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var total, satisfying int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE kind IN (
		           'company_budget_exceeded',
		           'rate_limit_pause',
		           'dept_weekly_ticket_budget_exceeded',
		           'ingress_rate_cap_exceeded'))
		  FROM throttle_events`,
	).Scan(&total, &satisfying); err != nil {
		t.Fatalf("throttle_events CHECK probe: %v", err)
	}
	if total == 0 {
		t.Fatal("expected at least one pre-existing throttle_events row")
	}
	if satisfying != total {
		t.Fatalf("%d of %d pre-existing throttle_events rows would violate the extended M10 CHECK", total-satisfying, total)
	}
}

// assertIngressDeliveriesTable verifies the ingress_deliveries table exists
// with the expected column set after the M10 Up migration applies.
func assertIngressDeliveriesTable(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	const q = `
		SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name = 'ingress_deliveries'
		 ORDER BY column_name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("ingress_deliveries columns probe: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, col)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []string{"connector_id", "created_at", "external_delivery_id", "id", "ticket_id"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ingress_deliveries columns: got %v, want %v", got, want)
	}
}

// m10SchemaFingerprint renders a deterministic, sorted text dump of the
// columns, indexes, constraints, and grants for ingress_deliveries and
// throttle_events — the tables M10 touches. Two equal fingerprints mean
// apply → rollback → apply reproduced the schema exactly.
func m10SchemaFingerprint(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	const q = `
SELECT 'column: ' || table_name || '.' || column_name
       || ' type=' || data_type
       || ' nullable=' || is_nullable
       || ' default=' || COALESCE(column_default, '<none>') AS line
  FROM information_schema.columns
 WHERE table_schema = 'public'
   AND table_name IN ('ingress_deliveries', 'throttle_events')
UNION ALL
SELECT 'index: ' || indexname || ' def=' || indexdef
  FROM pg_indexes
 WHERE schemaname = 'public'
   AND tablename IN ('ingress_deliveries', 'throttle_events')
UNION ALL
SELECT 'constraint: ' || conrelid::regclass::text || '.' || conname
       || ' def=' || pg_get_constraintdef(oid)
  FROM pg_constraint
 WHERE conrelid <> 0
   AND conrelid::regclass::text IN ('ingress_deliveries', 'throttle_events')
UNION ALL
SELECT 'grant: ' || grantee || ' ' || privilege_type || ' ON ' || table_name
  FROM information_schema.role_table_grants
 WHERE table_schema = 'public'
   AND grantee = 'garrison_dashboard_app'
   AND table_name = 'ingress_deliveries'
ORDER BY 1`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("m10 fingerprint query: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("m10 fingerprint scan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("m10 fingerprint rows: %v", err)
	}
	return strings.Join(lines, "\n")
}

// TestM11MigrationRoundtrip — T002 completion gate.
//
//  1. Boot a fresh postgres:17 container (NOT the shared testdb harness:
//     this test seeds rows BEFORE the M11 migration applies so the
//     chat_mutation_audit CHECK amendments are validated against real data).
//  2. goose up-to preM11Version (M10 head); seed chat_mutation_audit rows
//     using the M10-era verb/resource-type sets to prove pre-existing rows
//     satisfy the extended CHECKs after M11 lands.
//  3. goose up to head → the M11 Up applies; CHECK amendments validate
//     all existing rows.
//  4. Assert the two new tables exist with the expected column sets and
//     that the pre-existing chat_mutation_audit rows satisfy the extended
//     CHECKs.
//  5. Fingerprint the schema, goose down past M11, goose up again,
//     fingerprint again: apply → rollback → apply must be byte-for-byte
//     stable.
func TestM11MigrationRoundtrip(t *testing.T) {
	ctx := context.Background()

	pgC, err := postgres.Run(ctx, "postgres:17",
		postgres.WithDatabase("m11roundtrip"),
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

	// (2) Migrate to M10 head, then seed chat_mutation_audit rows using the
	// M10-era verb and resource-type sets so we can prove the M11 CHECK
	// extensions still accept pre-existing rows.
	if err := goose.UpToContext(ctx, db, migrationsDir, preM11Version); err != nil {
		t.Fatalf("goose up-to %d (pre-M11): %v", preM11Version, err)
	}
	seedPreM11ChatMutationAuditRows(ctx, t, db)

	// (3) Apply M11 on top of the seeded data. The CHECK amendments validate
	// all existing rows; an error here means pre-M11 rows violate the extended
	// verb / affected_resource_type CHECKs.
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		t.Fatalf("goose up (M11 apply over legacy rows): %v", err)
	}

	// (4) Assert pre-existing rows satisfy the extended CHECKs and verify the
	// new tables exist with the expected columns.
	assertPreM11AuditRowsSatisfyExtendedChecks(ctx, t, db)
	assertPendingActionsTable(ctx, t, db)
	assertPendingActionOutcomesTable(ctx, t, db)

	fpFirst := m11SchemaFingerprint(ctx, t, db)

	// (5) Roundtrip: down past M11, back up, fingerprint must not move.
	if err := goose.DownToContext(ctx, db, migrationsDir, preM11Version); err != nil {
		t.Fatalf("goose down-to %d (M11 rollback): %v", preM11Version, err)
	}

	// Post-rollback sanity: the M11 tables are gone.
	var m11TableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public'
		    AND table_name IN ('pending_actions', 'pending_action_outcomes')`,
	).Scan(&m11TableCount); err != nil {
		t.Fatalf("post-rollback table probe: %v", err)
	}
	if m11TableCount != 0 {
		t.Fatalf("M11 tables survived rollback: %d remaining", m11TableCount)
	}

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		t.Fatalf("goose up (M11 re-apply): %v", err)
	}
	fpSecond := m11SchemaFingerprint(ctx, t, db)

	if fpFirst != fpSecond {
		t.Fatalf("M11 schema fingerprint changed across apply → rollback → apply:\n--- first ---\n%s\n--- second ---\n%s", fpFirst, fpSecond)
	}
}

// seedPreM11ChatMutationAuditRows inserts chat_mutation_audit rows using the
// M10-era verb and resource-type sets (none of the new M11 values) so the
// M11 migration's CHECK amendments are validated against genuinely pre-existing
// data — proving the extended CHECKs are backward-compatible.
//
// Requires: a company, chat_session, chat_message, agent_instance, and
// department row (seeded here) since chat_mutation_audit has FKs.
func seedPreM11ChatMutationAuditRows(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()

	// Seed the minimum working set (idempotent — if a company row already
	// exists from a prior migration step, reuse it).
	var companyID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&companyID); err != nil {
		if err := db.QueryRowContext(ctx,
			`INSERT INTO companies (name, daily_budget_usd) VALUES ('M11 roundtrip', 1.00) RETURNING id`,
		).Scan(&companyID); err != nil {
			t.Fatalf("seed company: %v", err)
		}
	}

	var deptID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO departments (slug, name, concurrency_cap, company_id)
		VALUES ('m11-roundtrip', 'M11 roundtrip', 1, $1)
		RETURNING id`, companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("seed department: %v", err)
	}

	var ticketID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO tickets (department_id, objective)
		VALUES ($1, 'pre-M11 legacy ticket')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	var agentInstanceID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO agent_instances (department_id, ticket_id, status)
		VALUES ($1, $2, 'finished')
		RETURNING id`, deptID, ticketID,
	).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seed agent_instance: %v", err)
	}

	// Insert one audit row per M10-era verb category and resource type,
	// using NULL for the chat_session_id and chat_message_id since M8+
	// allows NULL anchors for agent-caller rows. reversibility_class=1,
	// outcome='success' — both are valid under the existing CHECKs.
	for _, tc := range []struct {
		verb         string
		resourceType *string
	}{
		{"create_ticket", strPtr("ticket")},
		{"register_mcp_server", strPtr("mcp_server")},
		{"create_scheduled_task", strPtr("scheduled_task")},
		{"approve_hire", strPtr("hiring_proposal")},
		// resource-type NULL is valid too
		{"transition_ticket", nil},
	} {
		var rt interface{} = nil
		if tc.resourceType != nil {
			rt = *tc.resourceType
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO chat_mutation_audit
			    (verb, args_jsonb, outcome, reversibility_class,
			     affected_resource_type, agent_instance_id)
			VALUES ($1, '{}', 'success', 1, $2, $3)`,
			tc.verb, rt, agentInstanceID,
		); err != nil {
			t.Fatalf("seed audit row verb=%s: %v", tc.verb, err)
		}
	}
}

// strPtr returns a pointer to a string literal — helper for seedPreM11ChatMutationAuditRows.
func strPtr(s string) *string { return &s }

// assertPreM11AuditRowsSatisfyExtendedChecks verifies every pre-existing
// chat_mutation_audit row satisfies the M11 extended verb and resource-type
// CHECKs. All M10-era values must still be accepted by the wider set.
func assertPreM11AuditRowsSatisfyExtendedChecks(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	// Check that the verb constraint exists and is validated (not NOT VALID).
	// If the M11 Up applied without error the constraint validated existing rows;
	// we probe that explicitly here.
	var verbConstraintValidated bool
	if err := db.QueryRowContext(ctx, `
		SELECT convalidated FROM pg_constraint
		 WHERE conname = 'chat_mutation_audit_verb_check'`,
	).Scan(&verbConstraintValidated); err != nil {
		t.Fatalf("verb constraint probe: %v", err)
	}
	if !verbConstraintValidated {
		t.Fatal("chat_mutation_audit_verb_check landed NOT VALID; pre-existing rows were not checked")
	}

	// Verify total count > 0 (the seed produced rows) and all satisfy the
	// M11-era verb + resource-type sets.
	var total, satisfying int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE verb IN (
		           'create_ticket', 'edit_ticket', 'transition_ticket',
		           'pause_agent', 'resume_agent', 'spawn_agent', 'edit_agent_config',
		           'propose_hire',
		           'propose_skill_change', 'bump_skill_version',
		           'approve_hire', 'reject_hire',
		           'approve_skill_change', 'reject_skill_change',
		           'approve_version_bump', 'reject_version_bump',
		           'update_agent_md', 'grandfathered_at_m7',
		           'register_mcp_server',
		           'create_scheduled_task', 'edit_scheduled_task',
		           'pause_scheduled_task', 'resume_scheduled_task',
		           'delete_scheduled_task',
		           'request_external_action',
		           'approve_action', 'reject_action', 'mark_action_done'
		       )
		       AND (affected_resource_type IS NULL OR affected_resource_type IN (
		           'ticket', 'agent_role', 'hiring_proposal', 'mcp_server',
		           'scheduled_task', 'pending_action'
		       )))
		  FROM chat_mutation_audit`,
	).Scan(&total, &satisfying); err != nil {
		t.Fatalf("audit rows check probe: %v", err)
	}
	if total == 0 {
		t.Fatal("expected at least one pre-existing chat_mutation_audit row")
	}
	if satisfying != total {
		t.Fatalf("%d of %d pre-M11 chat_mutation_audit rows do not satisfy the M11 extended CHECKs", total-satisfying, total)
	}
}

// assertPendingActionsTable verifies pending_actions exists with the expected
// column set after the M11 Up migration applies.
func assertPendingActionsTable(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	const q = `
		SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name = 'pending_actions'
		 ORDER BY column_name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("pending_actions columns probe: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, col)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []string{
		"action_type", "agent_instance_id", "approved_by", "created_at",
		"dispatched_at", "id", "rendered_payload", "status",
		"target", "ticket_id", "tier", "tier_reason",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pending_actions columns: got %v, want %v", got, want)
	}
}

// assertPendingActionOutcomesTable verifies pending_action_outcomes exists
// with the expected column set after the M11 Up migration applies.
func assertPendingActionOutcomesTable(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	const q = `
		SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name = 'pending_action_outcomes'
		 ORDER BY column_name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("pending_action_outcomes columns probe: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, col)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	want := []string{
		"agent_instance_id", "created_at", "detail", "id",
		"outcome", "pending_action_id", "structured_outcome",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pending_action_outcomes columns: got %v, want %v", got, want)
	}
}

// m11SchemaFingerprint renders a deterministic, sorted text dump of the
// columns, indexes, constraints, and grants for the tables M11 touches.
// Two equal fingerprints mean apply → rollback → apply reproduced the schema
// exactly.
func m11SchemaFingerprint(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	const q = `
SELECT 'column: ' || table_name || '.' || column_name
       || ' type=' || data_type
       || ' nullable=' || is_nullable
       || ' default=' || COALESCE(column_default, '<none>') AS line
  FROM information_schema.columns
 WHERE table_schema = 'public'
   AND table_name IN ('pending_actions', 'pending_action_outcomes', 'chat_mutation_audit')
UNION ALL
SELECT 'index: ' || indexname || ' def=' || indexdef
  FROM pg_indexes
 WHERE schemaname = 'public'
   AND tablename IN ('pending_actions', 'pending_action_outcomes')
UNION ALL
SELECT 'constraint: ' || conrelid::regclass::text || '.' || conname
       || ' def=' || pg_get_constraintdef(oid)
  FROM pg_constraint
 WHERE conrelid <> 0
   AND conrelid::regclass::text IN ('pending_actions', 'pending_action_outcomes',
                                    'chat_mutation_audit')
UNION ALL
SELECT 'grant: ' || grantee || ' ' || privilege_type || ' ON ' || table_name
  FROM information_schema.role_table_grants
 WHERE table_schema = 'public'
   AND grantee = 'garrison_dashboard_app'
   AND table_name IN ('pending_actions', 'pending_action_outcomes')
ORDER BY 1`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("m11 fingerprint query: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("m11 fingerprint scan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("m11 fingerprint rows: %v", err)
	}
	return strings.Join(lines, "\n")
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
