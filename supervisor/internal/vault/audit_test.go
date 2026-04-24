//go:build integration

package vault_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// seedAuditFixture inserts the minimal chain (company → department → ticket →
// agent_instance) needed to satisfy vault_access_log's FK constraints.
// Returns (agentInstanceID, companyID, secretPath) for use in AuditRow.
func seedAuditFixture(t *testing.T) (pgtype.UUID, pgtype.UUID, string) {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()

	// vault tables have no FK to the tables testdb.Start truncates, so clean them explicitly.
	cleanVault := func() {
		_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
	}
	cleanVault()
	t.Cleanup(cleanVault)

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'vault test co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("seedAuditFixture: insert company: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		 VALUES (gen_random_uuid(), $1, 'vault-dept', 'Vault Dept', 1, '/tmp/vault-test')
		 RETURNING id`, companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("seedAuditFixture: insert department: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (id, department_id, objective)
		 VALUES (gen_random_uuid(), $1, 'audit test ticket')
		 RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("seedAuditFixture: insert ticket: %v", err)
	}

	var agentInstanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (id, department_id, ticket_id, status)
		 VALUES (gen_random_uuid(), $1, $2, 'running')
		 RETURNING id`, deptID, ticketID,
	).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seedAuditFixture: insert agent_instance: %v", err)
	}

	const secretPath = "/vault-test-co/prod/MY_KEY"
	if _, err := pool.Exec(ctx,
		`INSERT INTO secret_metadata (secret_path, customer_id, provenance, rotation_cadence)
		 VALUES ($1, $2, 'test', '90 days')`,
		secretPath, companyID,
	); err != nil {
		t.Fatalf("seedAuditFixture: insert secret_metadata: %v", err)
	}

	return agentInstanceID, companyID, secretPath
}

func TestWriteAuditRowGranted(t *testing.T) {
	pool := testdb.Start(t)
	agentInstanceID, companyID, secretPath := seedAuditFixture(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	now := time.Now().UTC().Truncate(time.Millisecond)
	row := vault.AuditRow{
		AgentInstanceID: agentInstanceID,
		SecretPath:      secretPath,
		CustomerID:      companyID,
		Outcome:         vault.OutcomeGranted,
		Timestamp:       now,
	}

	if err := vault.WriteAuditRow(ctx, tx, row); err != nil {
		t.Fatalf("WriteAuditRow: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Assert vault_access_log row exists.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM vault_access_log WHERE secret_path=$1 AND outcome=$2`,
		secretPath, vault.OutcomeGranted,
	).Scan(&count); err != nil {
		t.Fatalf("count vault_access_log: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 vault_access_log row, got %d", count)
	}

	// Assert secret_metadata.last_accessed_at was updated.
	var lastAccessed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT last_accessed_at FROM secret_metadata WHERE secret_path=$1`,
		secretPath,
	).Scan(&lastAccessed); err != nil {
		t.Fatalf("select last_accessed_at: %v", err)
	}
	if !lastAccessed.Valid {
		t.Error("expected last_accessed_at to be set, got NULL")
	}
}

func TestWriteAuditRowDeniedNoGrant(t *testing.T) {
	pool := testdb.Start(t)
	agentInstanceID, companyID, secretPath := seedAuditFixture(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	row := vault.AuditRow{
		AgentInstanceID: agentInstanceID,
		SecretPath:      secretPath,
		CustomerID:      companyID,
		Outcome:         vault.OutcomeDeniedNoGrant,
		Timestamp:       time.Now().UTC(),
	}

	if err := vault.WriteAuditRow(ctx, tx, row); err != nil {
		t.Fatalf("WriteAuditRow: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Assert one log row.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM vault_access_log WHERE outcome=$1`, vault.OutcomeDeniedNoGrant,
	).Scan(&count); err != nil {
		t.Fatalf("count vault_access_log: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 vault_access_log row, got %d", count)
	}

	// secret_metadata.last_accessed_at must remain NULL (DeniedNoGrant does not touch it).
	var lastAccessed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT last_accessed_at FROM secret_metadata WHERE secret_path=$1`, secretPath,
	).Scan(&lastAccessed); err != nil {
		t.Fatalf("select last_accessed_at: %v", err)
	}
	if lastAccessed.Valid {
		t.Error("expected last_accessed_at to remain NULL for DeniedNoGrant, but it was set")
	}
}

func TestWriteAuditRowErrorAuditing(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	// Begin a transaction and deliberately abort it with a bad query so that
	// any subsequent SQL call fails with "current transaction is aborted".
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Force the transaction into an aborted state.
	_, _ = tx.Exec(ctx, `SELECT 1/0`)

	row := vault.AuditRow{
		SecretPath: "/some/path",
		Outcome:    vault.OutcomeGranted,
		Timestamp:  time.Now().UTC(),
	}

	err = vault.WriteAuditRow(ctx, tx, row)
	if !errors.Is(err, vault.ErrVaultAuditFailed) {
		t.Errorf("expected ErrVaultAuditFailed, got %v", err)
	}
}
