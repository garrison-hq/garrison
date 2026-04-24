//go:build integration

package vault_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestInfisicalTestcontainerBootstrap verifies that StartInfisical boots the
// three-container stack, that a machine identity can be created, a secret
// seeded, and the vault.Client can fetch it — returning the correct bytes
// while keeping all log output clean of the raw value.
func TestInfisicalTestcontainerBootstrap(t *testing.T) {
	harness := vault.StartInfisical(t)

	// Create a machine identity scoped to the test workspace.
	clientID, clientSecret, err := harness.CreateMachineIdentity("garrison-bootstrap-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}
	if clientID == "" {
		t.Fatal("CreateMachineIdentity: returned empty clientID")
	}

	// Seed a secret. The Infisical API takes a folder path + key name
	// separately; vault.Client.Fetch receives the full path and calls
	// splitSecretPath to separate them.
	const folderPath = "/garrison-test/operator"
	const secretKey = "BOOTSTRAP_KEY"
	const secretValue = "super-secret-bootstrap-value-abc123xyz789"
	if err := harness.SeedSecret(folderPath, secretKey, secretValue); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}

	// Capture slog output to assert the raw value never appears.
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx := context.Background()
	client, err := vault.NewClient(ctx, vault.ClientConfig{
		SiteURL:      harness.URL(),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		CustomerID:   "00000000-0000-0000-0000-000000000001", // arbitrary — not used in fetch path
		ProjectID:    harness.ProjectID(),
		Environment:  harness.Environment(),
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	grants := []vault.GrantRow{
		{
			EnvVarName: "BOOTSTRAP_KEY",
			SecretPath: folderPath + "/" + secretKey,
		},
	}

	fetched, err := client.Fetch(ctx, grants)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	sv, ok := fetched["BOOTSTRAP_KEY"]
	if !ok {
		t.Fatal("Fetch: BOOTSTRAP_KEY missing from result map")
	}

	// Assert value bytes match what was seeded.
	if got := string(sv.UnsafeBytes()); got != secretValue { //nolint:vaultlog
		t.Errorf("SecretValue bytes: got %q, want %q", got, secretValue)
	}

	// Assert LogValue returns [REDACTED], not the raw bytes.
	if lv := sv.LogValue().String(); lv != "[REDACTED]" {
		t.Errorf("LogValue: got %q, want [REDACTED]", lv)
	}

	// Assert the supervisor's slog output contains no raw secret bytes.
	if strings.Contains(logBuf.String(), secretValue) {
		t.Errorf("slog output contains raw secret value; output: %s", logBuf.String())
	}

	sv.Zero()
}

// TestVaultFetchRoundTripWithAudit verifies the full fetch → WriteAuditRow
// round trip: the secret is fetched from a live Infisical container, the
// audit row is written to Garrison's Postgres (via testdb), and both the
// vault_access_log row and secret_metadata.last_accessed_at are observable
// after commit. Satisfies T011's second required test.
func TestVaultFetchRoundTripWithAudit(t *testing.T) {
	harness := vault.StartInfisical(t)
	pool := testdb.Start(t)

	// Seed a fresh machine identity for this test.
	clientID, clientSecret, err := harness.CreateMachineIdentity("garrison-audit-roundtrip-ml")
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	// Seed the Infisical secret.
	const folderPath = "/garrison-test/ops"
	const secretKey = "ROUNDTRIP_KEY"
	const secretValue = "round-trip-secret-value-xyz987mnop456"
	if err := harness.SeedSecret(folderPath, secretKey, secretValue); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}
	const fullSecretPath = folderPath + "/" + secretKey

	// Build the minimal Garrison-Postgres fixture: company → department →
	// ticket → agent_instance + secret_metadata. Mirrors seedAuditFixture
	// but is self-contained here because we need the companyID for
	// vault_access_log and secret_metadata rows.
	ctx := context.Background()

	// Clean vault tables in case testdb shared state carries leftovers.
	_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "TRUNCATE vault_access_log, agent_role_secrets, secret_metadata")
	})

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'roundtrip test co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		 VALUES (gen_random_uuid(), $1, 'rt-dept', 'RT Dept', 1, '/tmp/rt-test')
		 RETURNING id`, companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (id, department_id, objective)
		 VALUES (gen_random_uuid(), $1, 'roundtrip test ticket')
		 RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	var agentInstanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (id, department_id, ticket_id, status)
		 VALUES (gen_random_uuid(), $1, $2, 'running')
		 RETURNING id`, deptID, ticketID,
	).Scan(&agentInstanceID); err != nil {
		t.Fatalf("insert agent_instance: %v", err)
	}

	// Seed the secret_metadata row so WriteAuditRow can update last_accessed_at.
	if _, err := pool.Exec(ctx,
		`INSERT INTO secret_metadata (secret_path, customer_id, provenance, rotation_cadence)
		 VALUES ($1, $2, 'test-operator', '90 days')`,
		fullSecretPath, companyID,
	); err != nil {
		t.Fatalf("insert secret_metadata: %v", err)
	}

	// Build vault.Client using the harness machine identity.
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client, err := vault.NewClient(ctx, vault.ClientConfig{
		SiteURL:      harness.URL(),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		CustomerID:   "00000000-0000-0000-0000-000000000001",
		ProjectID:    harness.ProjectID(),
		Environment:  harness.Environment(),
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	grants := []vault.GrantRow{
		{
			EnvVarName: "ROUNDTRIP_KEY",
			SecretPath: fullSecretPath,
		},
	}

	fetched, err := client.Fetch(ctx, grants)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	sv, ok := fetched["ROUNDTRIP_KEY"]
	if !ok {
		t.Fatal("Fetch: ROUNDTRIP_KEY missing from result map")
	}
	defer sv.Zero()

	// Verify fetched bytes before writing audit (zero happens in defer).
	if got := string(sv.UnsafeBytes()); got != secretValue { //nolint:vaultlog
		t.Errorf("SecretValue bytes: got %q, want %q", got, secretValue)
	}

	// Write audit row inside a transaction, then commit.
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := vault.AuditRow{
		AgentInstanceID: agentInstanceID,
		TicketID:        ticketID,
		SecretPath:      fullSecretPath,
		CustomerID:      companyID,
		Outcome:         vault.OutcomeGranted,
		Timestamp:       now,
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := vault.WriteAuditRow(ctx, tx, row); err != nil {
		t.Fatalf("WriteAuditRow: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Assert one vault_access_log row with correct fields.
	var logCount int
	var loggedPath string
	var loggedOutcome string
	if err := pool.QueryRow(ctx,
		`SELECT count(*), secret_path, outcome
		 FROM vault_access_log
		 WHERE agent_instance_id = $1
		 GROUP BY secret_path, outcome`,
		agentInstanceID,
	).Scan(&logCount, &loggedPath, &loggedOutcome); err != nil {
		t.Fatalf("select vault_access_log: %v", err)
	}
	if logCount != 1 {
		t.Errorf("vault_access_log row count: got %d, want 1", logCount)
	}
	if loggedPath != fullSecretPath {
		t.Errorf("vault_access_log.secret_path: got %q, want %q", loggedPath, fullSecretPath)
	}
	if loggedOutcome != string(vault.OutcomeGranted) {
		t.Errorf("vault_access_log.outcome: got %q, want %q", loggedOutcome, vault.OutcomeGranted)
	}

	// Assert secret_metadata.last_accessed_at was updated by WriteAuditRow.
	var lastAccessed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT last_accessed_at FROM secret_metadata WHERE secret_path = $1`,
		fullSecretPath,
	).Scan(&lastAccessed); err != nil {
		t.Fatalf("select last_accessed_at: %v", err)
	}
	if !lastAccessed.Valid {
		t.Error("secret_metadata.last_accessed_at: expected non-NULL after OutcomeGranted, got NULL")
	}

	// Assert slog output is clean of the raw secret value.
	if strings.Contains(logBuf.String(), secretValue) {
		t.Errorf("slog output contains raw secret value; output: %s", logBuf.String())
	}
}
